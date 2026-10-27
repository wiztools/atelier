// Comic document model: the pure logic behind the comic editor. The editor
// holds one ComicDoc and derives everything from the ops below; undo/redo is
// full-doc snapshots, so every op returns a new doc and a no-op returns a
// clone. This module mirrors comic.go's rules (validation semantics, winding,
// self-intersection, clamps, crop math) and stays free of React and the
// wailsjs bindings so test/run-comic-model-tests.cjs can compile it alone,
// like the other editor models.

export type ComicMediaRef = {
  artifactId: string;
  conversationId?: string;
  path: string;
  mimeType?: string;
  kind: 'image';
  width: number;
  height: number;
};

export type ComicPoint = {x: number; y: number};

export type ComicFit = 'cover' | 'contain';

export type ComicPanelImage = {
  ref: ComicMediaRef;
  fit: ComicFit;
  focusX: number;
  focusY: number;
  zoom: number;
};

export type ComicPanel = {
  id: string;
  points: ComicPoint[];
  image: ComicPanelImage | null;
};

export type ComicPage = {
  id: string;
  aspect: string;
  customAspect: number;
  panels: ComicPanel[];
  texts: ComicText[];
};

export type ComicTextKind = 'balloon' | 'caption' | 'sfx';

// ComicTail aims a balloon at its speaker: one normalized point on the PAGE
// (not the box — a dragged balloon keeps its aim).
export type ComicTail = {x: number; y: number};

export type ComicText = {
  id: string;
  kind: ComicTextKind;
  x: number;
  y: number;
  width: number;
  height: number;
  text: string;
  size: number;
  tail: ComicTail | null;
};

export type ComicStyle = {
  gutter: number;
  border: number;
};

export type ComicDoc = {
  schemaVersion: number;
  name: string;
  pages: ComicPage[];
  unplaced: ComicMediaRef[];
  style: ComicStyle;
};

// Mirrors comic.go's constants; keep both sides in step.
export const comicMaxPages = 64;
export const comicMaxPanelsPerPage = 24;
export const comicMaxPanelVertices = 16;
export const comicMaxUnplacedImages = 50;
export const comicMaxTextsPerPage = 20;
export const comicMinPanelArea = 0.001;
export const comicGutterMin = 0.0;
export const comicGutterMax = 0.15;
export const comicGutterDefault = 0.02;
export const comicBorderMin = 0.0;
export const comicBorderMax = 0.03;
export const comicBorderDefault = 0.004;
export const comicFocusDefault = 0.5;
export const comicZoomMin = 1.0;
export const comicZoomMax = 4.0;
export const comicCustomAspectMin = 0.25;
export const comicCustomAspectMax = 4.0;
export const comicTextMinDimension = 0.02;
export const comicTextSizeMin = 0.5;
export const comicTextSizeMax = 4.0;
export const comicDefaultAspect = 'comic';
export const comicDefaultName = 'Untitled comic';

// comicTextKinds orders the lettering vocabulary for pickers.
export const comicTextKinds: ComicTextKind[] = ['balloon', 'caption', 'sfx'];

// comicTextDefaults is the creation box per kind — balloon, caption box,
// burst — in normalized page units, with the burst's louder font scale.
export const comicTextDefaults: Record<ComicTextKind, {w: number; h: number; size: number}> = {
  balloon: {w: 0.16, h: 0.1, size: 1},
  caption: {w: 0.2, h: 0.07, size: 1},
  sfx: {w: 0.16, h: 0.12, size: 1.8},
};

// comicAspectPresets names the page shapes, width ÷ height — Go's map with a
// stable order for pickers.
export const comicAspectPresets: Array<{name: string; ratio: number; label: string}> = [
  {name: 'comic', ratio: 6.625 / 10.25, label: 'Comic (6.625″ × 10.25″)'},
  {name: 'manga', ratio: 5.0 / 7.5, label: 'Manga (5″ × 7.5″)'},
  {name: 'portrait', ratio: 3.0 / 4.0, label: 'Portrait (3:4)'},
  {name: 'square', ratio: 1.0, label: 'Square (1:1)'},
  {name: 'landscape', ratio: 4.0 / 3.0, label: 'Landscape (4:3)'},
  {name: 'widescreen', ratio: 16.0 / 9.0, label: 'Widescreen (16:9)'},
  {name: 'tall', ratio: 9.0 / 16.0, label: 'Tall strip (9:16)'},
];

function finite(...values: number[]): boolean {
  return values.every((value) => Number.isFinite(value));
}

// comicClampRange mirrors comicClampRange: non-finite falls back.
export function comicClampRange(value: number, min: number, max: number, fallback: number): number {
  if (!finite(value)) return fallback;
  return Math.min(max, Math.max(min, value));
}

// newComicID mirrors Go's randomID shape: <prefix>_<24 hex chars>.
export function newComicID(prefix: string): string {
  const bytes = new Uint8Array(12);
  if (typeof crypto !== 'undefined' && 'getRandomValues' in crypto) {
    crypto.getRandomValues(bytes);
  } else {
    for (let i = 0; i < bytes.length; i++) {
      bytes[i] = Math.floor(Math.random() * 256);
    }
  }
  const hex = Array.from(bytes, (b) => b.toString(16).padStart(2, '0')).join('');
  return `${prefix}_${hex}`;
}

// comicSignedArea is the shoelace signed area — positive when the polygon
// winds clockwise in y-down SVG space. Mirrors comic.go.
export function comicSignedArea(points: ComicPoint[]): number {
  let sum = 0;
  for (let i = 0; i < points.length; i++) {
    const a = points[i];
    const b = points[(i + 1) % points.length];
    sum += a.x * b.y - b.x * a.y;
  }
  return sum / 2;
}

// comicNormalizeWinding rewrites the ring to clockwise (in place on the
// caller's array) so both spellings of the same shape match the stored form.
export function comicNormalizeWinding(points: ComicPoint[]): void {
  if (comicSignedArea(points) < 0) {
    points.reverse();
  }
}

function comicCross(a: ComicPoint, b: ComicPoint, p: ComicPoint): number {
  return (b.x - a.x) * (p.y - a.y) - (b.y - a.y) * (p.x - a.x);
}

// comicSegmentsCross — the strict straddle test: touching at a point or
// riding collinear does not count. Mirrors comic.go.
export function comicSegmentsCross(a1: ComicPoint, a2: ComicPoint, b1: ComicPoint, b2: ComicPoint): boolean {
  const d1 = comicCross(b1, b2, a1);
  const d2 = comicCross(b1, b2, a2);
  const d3 = comicCross(a1, a2, b1);
  const d4 = comicCross(a1, a2, b2);
  return ((d1 > 0 && d2 < 0) || (d1 < 0 && d2 > 0)) && ((d3 > 0 && d4 < 0) || (d3 < 0 && d4 > 0));
}

// comicSelfIntersects reports whether any two non-adjacent edges properly
// cross — the one polygon state the editor refuses to build. The live
// counterpart of Go's validator: a vertex drop that would bowtie is refused
// here before a save can ever reach Go's refusal.
export function comicSelfIntersects(points: ComicPoint[]): boolean {
  const n = points.length;
  for (let i = 0; i < n; i++) {
    for (let j = i + 1; j < n; j++) {
      if (j === i + 1 || (i === 0 && j === n - 1)) {
        continue; // adjacent edges share a vertex
      }
      if (comicSegmentsCross(points[i], points[(i + 1) % n], points[j], points[(j + 1) % n])) {
        return true;
      }
    }
  }
  return false;
}

// panelArea is the polygon's share of the page (normalized coordinates, so
// the shoelace area IS the page fraction).
export function panelArea(points: ComicPoint[]): number {
  return Math.abs(comicSignedArea(points));
}

// comicPageAspect resolves width ÷ height and the display name — Go's twin,
// unknown names falling back to the default preset.
export function comicPageAspect(page: {aspect: string; customAspect: number}): {ratio: number; name: string} {
  if (page.aspect === 'custom' && finite(page.customAspect) && page.customAspect > 0) {
    return {ratio: comicClampRange(page.customAspect, comicCustomAspectMin, comicCustomAspectMax, 1), name: 'custom'};
  }
  const preset = comicAspectPresets.find((candidate) => candidate.name === page.aspect);
  if (preset) return {ratio: preset.ratio, name: preset.name};
  const fallback = comicAspectPresets.find((candidate) => candidate.name === comicDefaultAspect)!;
  return {ratio: fallback.ratio, name: fallback.name};
}

// comicPageSize is the page in pixels at a render width — the SVG canvas's
// coordinate space.
export function comicPageSize(ratio: number, width = 1000): {width: number; height: number} {
  return {width, height: Math.round(width / ratio)};
}

// normalizeComicMediaRef adapts one wire ref (ResolveComicMedia's return)
// into the model's form — the picker's add path. A foreign ref's kind rides
// through as-is (cast to the union): comicProblems names a non-image ref
// rather than silently re-typing it, mirroring Go's refusal.
export function normalizeComicMediaRef(ref: any): ComicMediaRef {
  const kind = typeof ref?.kind === 'string' && ref.kind ? ref.kind : 'image';
  return {
    artifactId: typeof ref?.artifactId === 'string' ? ref.artifactId : '',
    conversationId: typeof ref?.conversationId === 'string' ? ref.conversationId : undefined,
    path: typeof ref?.path === 'string' ? ref.path : '',
    mimeType: typeof ref?.mimeType === 'string' ? ref.mimeType : undefined,
    kind: kind as ComicMediaRef['kind'],
    width: Math.max(0, Math.floor(ref?.width ?? 0)),
    height: Math.max(0, Math.floor(ref?.height ?? 0)),
  };
}

function normalizePanelImage(wire: any): ComicPanelImage {
  return {
    ref: normalizeComicMediaRef(wire?.ref),
    fit: wire?.fit === 'contain' ? 'contain' : 'cover',
    // Focus and zoom clamp in normalize (display-sane, the clip-gain
    // pattern); vertex geometry stays raw for the live gate and Go's save.
    focusX: comicClampRange(wire?.focusX, 0, 1, 0),
    focusY: comicClampRange(wire?.focusY, 0, 1, 0),
    zoom: comicClampRange(wire?.zoom, comicZoomMin, comicZoomMax, 1),
  };
}

// newPanelImage builds the model's creation form — the explicit defaults the
// editor writes (center focus, exact cover).
export function newPanelImage(ref: ComicMediaRef): ComicPanelImage {
  return {ref: {...ref}, fit: 'cover', focusX: comicFocusDefault, focusY: comicFocusDefault, zoom: 1};
}

function normalizeComicText(wire: any): ComicText {
  // A foreign kind rides through (cast to the union) so comicProblems names
  // it rather than silently re-typing the element — the media-ref rule.
  const rawKind = typeof wire?.kind === 'string' && wire.kind ? wire.kind : 'balloon';
  const kind = rawKind as ComicTextKind;
  return {
    id: wire?.id ?? '',
    kind,
    x: finite(wire?.x) ? wire.x : 0,
    y: finite(wire?.y) ? wire.y : 0,
    width: finite(wire?.width) ? wire.width : 0,
    height: finite(wire?.height) ? wire.height : 0,
    text: typeof wire?.text === 'string' ? wire.text : '',
    // Missing or non-positive reads the default 1 (Go's rule); a positive
    // value clamps into [0.5, 4].
    size: finite(wire?.size) && wire.size > 0 ? comicClampRange(wire.size, comicTextSizeMin, comicTextSizeMax, 1) : 1,
    tail: wire?.tail && kind === 'balloon'
      ? {x: comicClampRange(wire.tail.x, 0, 1, 0), y: comicClampRange(wire.tail.y, 0, 1, 0)}
      : null,
  };
}

// normalizeComicDoc adapts the Go wire shape (omitempty means absent) into
// the model's always-present form. Accepts the generated main.ComicDoc
// instance or a plain object.
export function normalizeComicDoc(wire: any): ComicDoc {
  const doc: ComicDoc = {
    schemaVersion: typeof wire?.schemaVersion === 'number' ? wire.schemaVersion : 1,
    name: typeof wire?.name === 'string' && wire.name.trim() ? wire.name : comicDefaultName,
    pages: (Array.isArray(wire?.pages) ? wire.pages : []).map((page: any) => ({
      id: page?.id ?? '',
      aspect: typeof page?.aspect === 'string' && page.aspect ? page.aspect : comicDefaultAspect,
      customAspect: finite(page?.customAspect) ? page.customAspect : 0,
      panels: (Array.isArray(page?.panels) ? page.panels : []).map((panel: any) => ({
        id: panel?.id ?? '',
        points: (Array.isArray(panel?.points) ? panel.points : []).map((point: any) => ({
          x: finite(point?.x) ? point.x : 0,
          y: finite(point?.y) ? point.y : 0,
        })),
        image: panel?.image ? normalizePanelImage(panel.image) : null,
      })),
      texts: (Array.isArray(page?.texts) ? page.texts : []).map(normalizeComicText),
    })),
    unplaced: (Array.isArray(wire?.unplaced) ? wire.unplaced : []).map(normalizeComicMediaRef),
    style: {
      gutter: comicClampRange(wire?.style?.gutter, comicGutterMin, comicGutterMax, 0),
      border: comicClampRange(wire?.style?.border, comicBorderMin, comicBorderMax, 0),
    },
  };
  return doc;
}

// toWireDoc is the plain-object shape SaveComicEdit takes.
export function toWireDoc(doc: ComicDoc): any {
  return {
    schemaVersion: doc.schemaVersion,
    name: doc.name,
    pages: doc.pages.map((page) => ({
      id: page.id,
      ...(page.aspect === comicDefaultAspect ? {} : {aspect: page.aspect}),
      ...(page.aspect === 'custom' ? {customAspect: page.customAspect} : {}),
      panels: page.panels.map((panel) => ({
        id: panel.id,
        points: panel.points.map((point) => ({x: point.x, y: point.y})),
        ...(panel.image
          ? {
              image: {
                ref: {...panel.image.ref},
                fit: panel.image.fit,
                focusX: panel.image.focusX,
                focusY: panel.image.focusY,
                zoom: panel.image.zoom,
              },
            }
          : {}),
      })),
      texts: page.texts.map((text) => ({
        id: text.id,
        kind: text.kind,
        x: text.x,
        y: text.y,
        width: text.width,
        height: text.height,
        text: text.text,
        size: text.size,
        ...(text.tail ? {tail: {...text.tail}} : {}),
      })),
    })),
    unplaced: doc.unplaced.map((ref) => ({...ref})),
    style: {...doc.style},
  };
}

// seedComicDoc is the seeded-only contract's frontend half — Go's twin: one
// blank page in the default format, refs queued onto the unplaced tray in
// reference order, house style.
export function seedComicDoc(name: string, refs: ComicMediaRef[]): ComicDoc {
  return normalizeComicDoc({
    schemaVersion: 1,
    name: name.trim() || comicDefaultName,
    pages: [{id: newComicID('cpg'), aspect: comicDefaultAspect, panels: [], texts: []}],
    unplaced: refs,
    style: {gutter: comicGutterDefault, border: comicBorderDefault},
  });
}

export function cloneComicDoc(doc: ComicDoc): ComicDoc {
  return normalizeComicDoc(toWireDoc(doc));
}

export function sameComicDoc(a: ComicDoc, b: ComicDoc): boolean {
  return JSON.stringify(toWireDoc(a)) === JSON.stringify(toWireDoc(b));
}

// ——— pure ops (every one returns a new doc; a refusal returns a clone) ———

function updatePage(doc: ComicDoc, pageID: string, update: (page: ComicPage) => ComicPage): ComicDoc {
  return {...doc, pages: doc.pages.map((page) => (page.id === pageID ? update(page) : page))};
}

function updatePanel(doc: ComicDoc, panelID: string, update: (panel: ComicPanel) => ComicPanel): ComicDoc {
  return {...doc, pages: doc.pages.map((page) => ({...page, panels: page.panels.map((panel) => (panel.id === panelID ? update(panel) : panel))}))};
}

function findPanel(doc: ComicDoc, panelID: string): {page: ComicPage; panel: ComicPanel} | null {
  for (const page of doc.pages) {
    const panel = page.panels.find((candidate) => candidate.id === panelID);
    if (panel) return {page, panel};
  }
  return null;
}

export function renameComic(doc: ComicDoc, name: string): ComicDoc {
  const trimmed = name.trim();
  return {...doc, name: trimmed || comicDefaultName};
}

export function addPage(doc: ComicDoc, aspect = comicDefaultAspect): ComicDoc {
  if (doc.pages.length >= comicMaxPages) return cloneComicDoc(doc);
  const page: ComicPage = {id: newComicID('cpg'), aspect, customAspect: 0, panels: [], texts: []};
  return {...doc, pages: [...doc.pages, page]};
}

export function duplicatePage(doc: ComicDoc, pageID: string): ComicDoc {
  const index = doc.pages.findIndex((page) => page.id === pageID);
  if (index < 0 || doc.pages.length >= comicMaxPages) return cloneComicDoc(doc);
  const source = doc.pages[index];
  const copy: ComicPage = {
    id: newComicID('cpg'),
    aspect: source.aspect,
    customAspect: source.customAspect,
    panels: source.panels.map((panel) => ({
      id: newComicID('pnl'),
      points: panel.points.map((point) => ({...point})),
      // The image ref is SHARED — the same artifact in two panels is legal
      // (reuse), and the tray entry stays single.
      image: panel.image ? {...panel.image, ref: {...panel.image.ref}} : null,
    })),
    texts: source.texts.map((text) => ({...text, tail: text.tail ? {...text.tail} : null})),
  };
  const pages = [...doc.pages.slice(0, index + 1), copy, ...doc.pages.slice(index + 1)];
  return {...doc, pages};
}

// removePage refuses (no-ops) on the last page — a comic always has one.
export function removePage(doc: ComicDoc, pageID: string): ComicDoc {
  if (doc.pages.length <= 1) return cloneComicDoc(doc);
  return {...doc, pages: doc.pages.filter((page) => page.id !== pageID)};
}

export function setPageAspect(doc: ComicDoc, pageID: string, aspect: string, customAspect?: number): ComicDoc {
  if (aspect !== 'custom' && !comicAspectPresets.some((preset) => preset.name === aspect)) {
    return cloneComicDoc(doc);
  }
  return updatePage(doc, pageID, (page) => ({
    ...page,
    aspect,
    customAspect: aspect === 'custom' ? comicClampRange(customAspect ?? page.customAspect, comicCustomAspectMin, comicCustomAspectMax, 1) : 0,
  }));
}

export function setComicStyle(doc: ComicDoc, patch: Partial<ComicStyle>): ComicDoc {
  return {
    ...doc,
    style: {
      gutter: comicClampRange(patch.gutter !== undefined ? patch.gutter : doc.style.gutter, comicGutterMin, comicGutterMax, comicGutterDefault),
      border: comicClampRange(patch.border !== undefined ? patch.border : doc.style.border, comicBorderMin, comicBorderMax, comicBorderDefault),
    },
  };
}

// validPanelGeometry is the gate addPanel and setPanelPoints share — the
// Go validator's rules, mirrored: 3–16 finite vertices inside the page, no
// bowtie, above the area floor. Returns the normalized (clamped,
// clockwise-wound) ring, or null when Go would refuse.
export function validPanelGeometry(points: ComicPoint[]): ComicPoint[] | null {
  if (points.length < 3 || points.length > comicMaxPanelVertices) return null;
  const ring = points.map((point) => ({
    x: comicClampRange(point.x, 0, 1, 0),
    y: comicClampRange(point.y, 0, 1, 0),
  }));
  if (!ring.every((point) => finite(point.x, point.y))) return null;
  comicNormalizeWinding(ring);
  if (comicSelfIntersects(ring)) return null;
  if (panelArea(ring) < comicMinPanelArea) return null;
  return ring;
}

export function addPanel(doc: ComicDoc, pageID: string, points: ComicPoint[], index?: number): ComicDoc {
  const page = doc.pages.find((candidate) => candidate.id === pageID);
  if (!page || page.panels.length >= comicMaxPanelsPerPage) return cloneComicDoc(doc);
  const ring = validPanelGeometry(points);
  if (!ring) return cloneComicDoc(doc);
  const panel: ComicPanel = {id: newComicID('pnl'), points: ring, image: null};
  const at = index === undefined ? page.panels.length : Math.min(Math.max(0, index), page.panels.length);
  const panels = [...page.panels.slice(0, at), panel, ...page.panels.slice(at)];
  return updatePage(doc, pageID, (current) => ({...current, panels}));
}

// movePanel reorders z — the array order is the paint order, later on top.
export function movePanel(doc: ComicDoc, pageID: string, panelID: string, toIndex: number): ComicDoc {
  const page = doc.pages.find((candidate) => candidate.id === pageID);
  if (!page) return cloneComicDoc(doc);
  const from = page.panels.findIndex((panel) => panel.id === panelID);
  if (from < 0) return cloneComicDoc(doc);
  const to = Math.min(Math.max(0, Math.round(toIndex)), page.panels.length - 1);
  if (from === to) return cloneComicDoc(doc);
  const panels = [...page.panels];
  const [panel] = panels.splice(from, 1);
  panels.splice(to, 0, panel);
  return updatePage(doc, pageID, (current) => ({...current, panels}));
}

export function removePanel(doc: ComicDoc, panelID: string): ComicDoc {
  return {...doc, pages: doc.pages.map((page) => ({...page, panels: page.panels.filter((panel) => panel.id !== panelID)}))};
}

// setPanelPoints re-shapes a panel through the geometry gate; a drop Go
// would refuse (a bowtie, a sliver) is a no-op — the editor previews the
// rules live, so the user never asks blind.
export function setPanelPoints(doc: ComicDoc, panelID: string, points: ComicPoint[]): ComicDoc {
  if (!findPanel(doc, panelID)) return cloneComicDoc(doc);
  const ring = validPanelGeometry(points);
  if (!ring) return cloneComicDoc(doc);
  return updatePanel(doc, panelID, (panel) => ({...panel, points: ring}));
}

// setPanelImage assigns (or re-assigns) a measured ref — the tray drag's
// landing. Passing null removes the image (the ref stays in the tray per the
// editor's own bookkeeping).
export function setPanelImage(doc: ComicDoc, panelID: string, ref: ComicMediaRef | null): ComicDoc {
  if (!findPanel(doc, panelID)) return cloneComicDoc(doc);
  return updatePanel(doc, panelID, (panel) => ({
    ...panel,
    image: ref && ref.width > 0 && ref.height > 0 ? newPanelImage(ref) : null,
  }));
}

export function setPanelFit(doc: ComicDoc, panelID: string, fit: ComicFit): ComicDoc {
  return updatePanel(doc, panelID, (panel) => (panel.image ? {...panel, image: {...panel.image, fit}} : panel));
}

// setPanelFocus pans a cover crop — the drag-inside-a-panel gesture's op.
// Contain ignores focus.
export function setPanelFocus(doc: ComicDoc, panelID: string, focusX: number, focusY: number): ComicDoc {
  return updatePanel(doc, panelID, (panel) =>
    panel.image
      ? {...panel, image: {...panel.image, focusX: comicClampRange(focusX, 0, 1, comicFocusDefault), focusY: comicClampRange(focusY, 0, 1, comicFocusDefault)}}
      : panel);
}

export function setPanelZoom(doc: ComicDoc, panelID: string, zoom: number): ComicDoc {
  return updatePanel(doc, panelID, (panel) =>
    panel.image ? {...panel, image: {...panel.image, zoom: comicClampRange(zoom, comicZoomMin, comicZoomMax, 1)}} : panel);
}

export function addUnplacedImage(doc: ComicDoc, ref: ComicMediaRef): ComicDoc {
  if (ref.width <= 0 || ref.height <= 0 || doc.unplaced.length >= comicMaxUnplacedImages) return cloneComicDoc(doc);
  if (doc.unplaced.some((candidate) => candidate.artifactId === ref.artifactId)) return cloneComicDoc(doc);
  return {...doc, unplaced: [...doc.unplaced, {...ref}]};
}

// removeUnplacedImage drops a tray entry by artifact id.
export function removeUnplacedImage(doc: ComicDoc, artifactId: string): ComicDoc {
  return {...doc, unplaced: doc.unplaced.filter((ref) => ref.artifactId !== artifactId)};
}

// ——— lettering ops (page-level text elements; the mirrors of Go's
// comicNormalizeTexts rules) ———

function findText(doc: ComicDoc, textID: string): ComicText | undefined {
  for (const page of doc.pages) {
    const text = page.texts.find((candidate) => candidate.id === textID);
    if (text) return text;
  }
  return undefined;
}

function updateText(doc: ComicDoc, textID: string, update: (text: ComicText) => ComicText): ComicDoc {
  return {...doc, pages: doc.pages.map((page) => ({...page, texts: page.texts.map((text) => (text.id === textID ? update(text) : text))}))};
}

// addText stamps a new lettering element of the kind, centered on the
// requested point (the page center when omitted) in the kind's default box.
export function addText(doc: ComicDoc, pageID: string, kind: ComicTextKind, x = 0.5, y = 0.5): ComicDoc {
  const page = doc.pages.find((candidate) => candidate.id === pageID);
  if (!page || page.texts.length >= comicMaxTextsPerPage) return cloneComicDoc(doc);
  const defaults = comicTextDefaults[kind];
  const text: ComicText = {
    id: newComicID('txt'),
    kind,
    x: Math.min(1 - defaults.w, Math.max(0, x - defaults.w / 2)),
    y: Math.min(1 - defaults.h, Math.max(0, y - defaults.h / 2)),
    width: defaults.w,
    height: defaults.h,
    text: '',
    size: defaults.size,
    tail: null,
  };
  return updatePage(doc, pageID, (current) => ({...current, texts: [...current.texts, text]}));
}

// setTextRect moves/resizes a lettering element; a box below the per-side
// floor is a no-op (the editor's handles preview the rule).
export function setTextRect(doc: ComicDoc, textID: string, x: number, y: number, width: number, height: number): ComicDoc {
  if (!findText(doc, textID)) return cloneComicDoc(doc);
  if (!finite(x, y, width, height)) return cloneComicDoc(doc);
  const cx = comicClampRange(x, 0, 1, 0);
  const cy = comicClampRange(y, 0, 1, 0);
  const cw = comicClampRange(width, 0, 1, 0);
  const ch = comicClampRange(height, 0, 1, 0);
  if (cw < comicTextMinDimension || ch < comicTextMinDimension) return cloneComicDoc(doc);
  return updateText(doc, textID, (text) => ({...text, x: cx, y: cy, width: cw, height: ch}));
}

export function setTextContent(doc: ComicDoc, textID: string, content: string): ComicDoc {
  return updateText(doc, textID, (text) => ({...text, text: content}));
}

// setTextKind re-letters the element; only balloons keep a tail.
export function setTextKind(doc: ComicDoc, textID: string, kind: ComicTextKind): ComicDoc {
  return updateText(doc, textID, (text) => ({
    ...text,
    kind,
    tail: kind === 'balloon' ? text.tail : null,
  }));
}

export function setTextTail(doc: ComicDoc, textID: string, x: number, y: number): ComicDoc {
  if (!findText(doc, textID)) return cloneComicDoc(doc);
  return updateText(doc, textID, (text) =>
    text.kind === 'balloon' ? {...text, tail: {x: comicClampRange(x, 0, 1, 0), y: comicClampRange(y, 0, 1, 0)}} : text);
}

export function setTextSize(doc: ComicDoc, textID: string, size: number): ComicDoc {
  return updateText(doc, textID, (text) => ({
    ...text,
    size: finite(size) && size > 0 ? comicClampRange(size, comicTextSizeMin, comicTextSizeMax, 1) : text.size,
  }));
}

export function removeText(doc: ComicDoc, textID: string): ComicDoc {
  return {...doc, pages: doc.pages.map((page) => ({...page, texts: page.texts.filter((text) => text.id !== textID)}))};
}

// comicSFXPoints builds the burst polygon for a sound effect — alternating
// outer/inner points on the box's ellipse (spikes pairs), in normalized page
// coordinates.
export function comicSFXPoints(text: ComicText): ComicPoint[] {
  const cx = text.x + text.width / 2;
  const cy = text.y + text.height / 2;
  const rx = text.width / 2;
  const ry = text.height / 2;
  const spikes = 12;
  const points: ComicPoint[] = [];
  for (let i = 0; i < spikes * 2; i++) {
    const angle = (Math.PI * 2 * i) / (spikes * 2) - Math.PI / 2;
    const scale = i % 2 === 0 ? 1 : 0.72;
    points.push({x: cx + Math.cos(angle) * rx * scale, y: cy + Math.sin(angle) * ry * scale});
  }
  return points;
}

// comicBalloonTailBase resolves the tail triangle's two base points on the
// bubble's edge flanking the direction to the tip — the triangle
// [tip, baseA, baseB] renders UNDER the bubble fill, so its base edge never
// shows; the base arc is a quarter-radian wide.
export function comicBalloonTailBase(text: ComicText, spike = 0.125): {a: ComicPoint; b: ComicPoint} {
  const tip = text.tail ?? {x: text.x + text.width / 2, y: text.y + text.height};
  const cx = text.x + text.width / 2;
  const cy = text.y + text.height / 2;
  const rx = text.width / 2;
  const ry = text.height / 2;
  const angle = Math.atan2((tip.y - cy) / (ry || 1), (tip.x - cx) / (rx || 1));
  const pointAt = (theta: number) => ({x: cx + Math.cos(theta) * rx, y: cy + Math.sin(theta) * ry});
  return {a: pointAt(angle - spike), b: pointAt(angle + spike)};
}

// comicWrapText wraps a lettering string to a pixel width the way SVG's
// manual wrapping must: greedy words, the approximate Helvetica advance of
// 0.6em per character (digits and capitals run wider; lettering is hand-tuned
// anyway). Empty lines survive.
export function comicWrapText(content: string, boxWidthPx: number, fontSizePx: number): string[] {
  const charWidth = Math.max(1, fontSizePx * 0.6);
  const maxChars = Math.max(4, Math.floor(boxWidthPx / charWidth));
  const lines: string[] = [];
  for (const paragraph of content.split('\n')) {
    const words = paragraph.split(/\s+/).filter(Boolean);
    if (!words.length) {
      lines.push('');
      continue;
    }
    let current = '';
    for (const word of words) {
      const candidate = current ? `${current} ${word}` : word;
      if (candidate.length <= maxChars || !current) {
        current = candidate;
      } else {
        lines.push(current);
        current = word;
      }
    }
    if (current) lines.push(current);
  }
  return lines.length ? lines : [''];
}

// comicProblems mirrors validateComicDoc's refusals — the states the reducers
// refuse to build, so a hand-edited or foreign document still gets named
// reasons before Save lights up.
export function comicProblems(doc: ComicDoc): string[] {
  const problems: string[] = [];
  if (doc.pages.length === 0) {
    problems.push('a comic needs at least one page');
  }
  if (doc.pages.length > comicMaxPages) {
    problems.push(`a comic supports at most ${comicMaxPages} pages`);
  }
  if (doc.unplaced.length > comicMaxUnplacedImages) {
    problems.push(`a comic holds at most ${comicMaxUnplacedImages} unplaced images`);
  }
  const ids = new Set<string>();
  const claim = (id: string, label: string) => {
    if (!id.trim()) {
      problems.push(`${label} has an empty id`);
    } else if (ids.has(id)) {
      problems.push(`${label} reuses id ${id}`);
    }
    ids.add(id);
  };
  doc.pages.forEach((page, pageIndex) => {
    const label = `page ${pageIndex + 1}`;
    claim(page.id, label);
    if (page.aspect !== 'custom' && !comicAspectPresets.some((preset) => preset.name === page.aspect)) {
      problems.push(`${label} has unknown aspect ${page.aspect}`);
    }
    if (page.aspect === 'custom' && !(finite(page.customAspect) && page.customAspect > 0)) {
      problems.push(`${label}'s custom aspect must be a positive width ÷ height`);
    }
    if (page.panels.length > comicMaxPanelsPerPage) {
      problems.push(`${label} supports at most ${comicMaxPanelsPerPage} panels`);
    }
    if (page.texts.length > comicMaxTextsPerPage) {
      problems.push(`${label} supports at most ${comicMaxTextsPerPage} text elements`);
    }
    page.texts.forEach((text, textIndex) => {
      const textLabel = `${label} text ${textIndex + 1}`;
      claim(text.id, textLabel);
      if (!comicTextKinds.includes(text.kind)) {
        problems.push(`${textLabel}'s kind must be "balloon", "caption", or "sfx"`);
      }
      if (text.width < comicTextMinDimension || text.height < comicTextMinDimension) {
        problems.push(`${textLabel}'s box is too small (the minimum is ${comicTextMinDimension} of the page per side)`);
      }
    });
    page.panels.forEach((panel, panelIndex) => {
      const panelLabel = `${label} panel ${panelIndex + 1}`;
      claim(panel.id, panelLabel);
      if (panel.points.length < 3 || panel.points.length > comicMaxPanelVertices) {
        problems.push(`${panelLabel}'s polygon needs 3–${comicMaxPanelVertices} vertices`);
        return;
      }
      if (!validPanelGeometry(panel.points)) {
        problems.push(`${panelLabel}'s polygon crosses itself or is too small`);
      }
      if (panel.image) {
        if (panel.image.ref.kind !== 'image') {
          problems.push(`${panelLabel}'s image references ${panel.image.ref.kind || 'unknown'} media — comic panels hold images`);
        }
        if (panel.image.ref.width <= 0 || panel.image.ref.height <= 0) {
          problems.push(`${panelLabel}'s image was never measured`);
        }
      }
    });
  });
  doc.unplaced.forEach((ref, index) => {
    if (ref.kind !== 'image') {
      problems.push(`unplaced image ${index + 1} references ${ref.kind || 'unknown'} media — the comic takes stills only`);
    }
    if (ref.width <= 0 || ref.height <= 0) {
      problems.push(`unplaced image ${index + 1} was never measured`);
    }
  });
  return problems;
}

// ——— crop math (the SVG renderer's inputs; pure and table-tested) ———

// panelBoundingBox is the polygon's axis-aligned bounds in page coordinates.
export function panelBoundingBox(points: ComicPoint[]): {x: number; y: number; w: number; h: number} {
  let minX = Infinity;
  let minY = Infinity;
  let maxX = -Infinity;
  let maxY = -Infinity;
  for (const point of points) {
    minX = Math.min(minX, point.x);
    minY = Math.min(minY, point.y);
    maxX = Math.max(maxX, point.x);
    maxY = Math.max(maxY, point.y);
  }
  return {x: minX, y: minY, w: maxX - minX, h: maxY - minY};
}

export type ComicImagePlacement = {
  x: number;
  y: number;
  width: number;
  height: number;
};

// comicImagePlacement resolves where an <image> sits so a cover crop shows
// the focus, or a contain fit rests centered — in PAGE pixel coordinates.
// Cover: scale fills the panel's bounding box (× zoom), and the visible
// window slides so the focus fraction of the image sits at the focus
// fraction of the box — that sliding IS focus panning. Contain: the whole
// image fits, centered; focus is ignored. Points arrive in normalized page
// coordinates; each axis scales by ITS dimension (x by the page width, y by
// the page height — the two are only equal on a square page).
export function comicImagePlacement(
  points: ComicPoint[],
  imageWidth: number,
  imageHeight: number,
  fit: ComicFit,
  focusX: number,
  focusY: number,
  zoom: number,
  pageWidth: number,
  pageHeight: number,
): ComicImagePlacement {
  const box = panelBoundingBox(points);
  const bw = box.w * pageWidth;
  const bh = box.h * pageHeight;
  const safeImageWidth = imageWidth > 0 ? imageWidth : 1;
  const safeImageHeight = imageHeight > 0 ? imageHeight : 1;
  if (fit === 'contain') {
    const scale = Math.min(bw / safeImageWidth, bh / safeImageHeight);
    const width = safeImageWidth * scale;
    const height = safeImageHeight * scale;
    return {x: box.x * pageWidth + (bw - width) / 2, y: box.y * pageHeight + (bh - height) / 2, width, height};
  }
  const coverScale = Math.max(bw / safeImageWidth, bh / safeImageHeight);
  const scale = coverScale * comicClampRange(zoom, comicZoomMin, comicZoomMax, 1);
  const width = safeImageWidth * scale;
  const height = safeImageHeight * scale;
  const focus = comicClampRange(focusX, 0, 1, comicFocusDefault);
  const focusYClamped = comicClampRange(focusY, 0, 1, comicFocusDefault);
  const visibleLeft = focus * Math.max(0, width - bw);
  const visibleTop = focusYClamped * Math.max(0, height - bh);
  return {x: box.x * pageWidth - visibleLeft, y: box.y * pageHeight - visibleTop, width, height};
}

// comicPointInPolygon is the ray-casting hit test the tray-drop and
// select gestures share — inside() in normalized page coordinates.
export function comicPointInPolygon(points: ComicPoint[], x: number, y: number): boolean {
  let inside = false;
  for (let i = 0, j = points.length - 1; i < points.length; j = i++) {
    const a = points[i];
    const b = points[j];
    if ((a.y > y) !== (b.y > y) && x < ((b.x - a.x) * (y - a.y)) / (b.y - a.y) + a.x) {
      inside = !inside;
    }
  }
  return inside;
}

// ——— authoring aids (the grid stamps and default panels) ———

// comicGridPanels stamps an rows×cols grid of rectangular panels with the
// gutter between them — the "a normal page, fast" path. Returns page-
// normalized rings ready for addPanel.
export function comicGridPanels(rows: number, cols: number, gutter: number): ComicPoint[][] {
  const safeRows = Math.min(5, Math.max(1, Math.round(rows)));
  const safeCols = Math.min(5, Math.max(1, Math.round(cols)));
  const half = comicClampRange(gutter, comicGutterMin, comicGutterMax, comicGutterDefault) / 2;
  const rings: ComicPoint[][] = [];
  for (let row = 0; row < safeRows; row++) {
    for (let col = 0; col < safeCols; col++) {
      const x0 = col / safeCols + (col === 0 ? 0 : half);
      const x1 = (col + 1) / safeCols - (col === safeCols - 1 ? 0 : half);
      const y0 = row / safeRows + (row === 0 ? 0 : half);
      const y1 = (row + 1) / safeRows - (row === safeRows - 1 ? 0 : half);
      rings.push(comicRectPoints(x0, y0, x1 - x0, y1 - y0));
    }
  }
  return rings;
}

// comicRectPoints builds a clockwise rectangle ring.
export function comicRectPoints(x: number, y: number, w: number, h: number): ComicPoint[] {
  return [
    {x, y},
    {x: x + w, y},
    {x: x + w, y: y + h},
    {x, y: y + h},
  ];
}

// comicPolygonPointsString renders an SVG points attribute for a polygon —
// page pixel coordinates, each axis by its own dimension.
export function comicPolygonPointsString(points: ComicPoint[], pageWidth: number, pageHeight: number): string {
  return points.map((point) => `${(point.x * pageWidth).toFixed(2)},${(point.y * pageHeight).toFixed(2)}`).join(' ');
}

// comicDocSummary words the header's one-line record — Go's summary shape.
export function comicDocSummary(doc: ComicDoc): string {
  const panels = doc.pages.reduce((count, page) => count + page.panels.length, 0);
  const pages = doc.pages.length === 1 ? '1 page' : `${doc.pages.length} pages`;
  const panelPhrase = panels === 1 ? '1 panel' : `${panels} panels`;
  const aspect = doc.pages.length > 0 ? comicPageAspect(doc.pages[0]).name : comicDefaultAspect;
  return `${pages} · ${panelPhrase} · ${aspect} format`;
}
