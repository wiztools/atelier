import {useEffect, useMemo, useRef, useState} from 'react';
import * as AppBindings from '../../wailsjs/go/main/App';
import {SaveComic, SaveComicEdit} from '../../wailsjs/go/main/App';
import {main} from '../../wailsjs/go/models';
import {EditsPanel} from './EditsPanel';
import {EditorHeader} from './EditorHeader';
import {MediaPicker} from './MediaPicker';
import {
  ComicDoc,
  ComicMediaRef,
  ComicPoint,
  addPage,
  addPanel,
  addUnplacedImage,
  comicAspectPresets,
  comicBorderMax,
  comicDefaultAspect,
  comicDocSummary,
  comicGridPanels,
  comicGutterMax,
  comicImagePlacement,
  comicMaxPanelVertices,
  comicPageAspect,
  comicPageSize,
  comicPointInPolygon,
  comicPolygonPointsString,
  comicProblems,
  comicRectPoints,
  duplicatePage,
  movePanel,
  normalizeComicDoc,
  removePage,
  removePanel,
  removeUnplacedImage,
  renameComic,
  sameComicDoc,
  setPageAspect,
  setPanelFit,
  setPanelFocus,
  setPanelImage,
  setPanelPoints,
  setPanelZoom,
  setComicStyle,
  toWireDoc,
  validPanelGeometry,
} from './comicModel';
import './comicEditor.css';

export type ComicEditorHandle = {
  source: main.ComicSourceInfo;
  // The comic conversation's library scope for the media picker, resolved
  // from its own record when the editor opens — the openSubtitleEditor rule:
  // a project conversation outside the sidebar's recency window keeps its
  // library scope, an independent conversation passes none.
  libraryID: string;
};

type EditorLogEntry = {
  name: string;
  pages: number;
  at: string;
  artifactId: string;
  url: string;
};

type Selection =
  | {kind: 'doc'}
  | {kind: 'panel'; panelID: string};

type Tool = 'select' | 'rect' | 'polygon';

type Drag =
  | {kind: 'panel-move'; panelID: string; startPage: ComicPoint; original: ComicPoint[]}
  | {kind: 'vertex'; panelID: string; index: number}
  | {kind: 'rect'; start: ComicPoint}
  | {kind: 'pan-focus'; panelID: string; startClient: {x: number; y: number}; startFocusX: number; startFocusY: number};

// The Edits panel's height clamp needs this editor's content floor: header +
// toolbar + the stage's minimum + paddings.
const editsPanelBodyReservePx = 700;
const undoLimit = 100;
// The canvas viewBox width; the height follows the page's aspect ratio.
const pageViewWidth = 1000;

function formatEditorError(error: unknown): string {
  if (typeof error === 'string') return error;
  if (error instanceof Error) return error.message;
  try {
    return JSON.stringify(error);
  } catch {
    return String(error);
  }
}

function asArray<T>(value: T[] | null | undefined): T[] {
  return Array.isArray(value) ? value : [];
}

// artifactImageURL is the display URL for a ref — the hydration convention:
// the binding hands back an ABSOLUTE path, and the asset handler serves
// "/atelier-artifact" + absolute path (the library-asset shape), so a 404
// degrades to an empty panel the same way a missing poster does.
function artifactImageURL(ref: ComicMediaRef): string {
  if (!ref.path) return '';
  if (ref.path.startsWith('/atelier-artifact/')) return ref.path;
  return '/atelier-artifact' + ref.path;
}

// clampRing bounds a polygon into the page — the live form of
// validPanelGeometry's clamp, applied before the gate so a drag at the page
// edge sticks to it instead of vibrating.
function clampRing(points: ComicPoint[]): ComicPoint[] {
  return points.map((point) => ({
    x: Math.min(1, Math.max(0, point.x)),
    y: Math.min(1, Math.max(0, point.y)),
  }));
}

export function ComicEditor(props: {
  handle: ComicEditorHandle;
  libraryID?: string;
  // True while hidden beneath another screen (Settings): shortcuts detach,
  // all edit state stays.
  suspended?: boolean;
  onClose: () => void;
  onOpenParent: (conversationID: string) => void;
  onDirtyChange?: (dirty: boolean) => void;
  // Fired after a save lands a new artifact on the conversation so the parent
  // can refresh the sidebar and the assets panel.
  onSavedToConversation?: () => void;
}) {
  const {source} = props.handle;
  const sourceConversationID = useState(source.conversationId)[0];

  const [doc, setDoc] = useState<ComicDoc>(() => normalizeComicDoc(source.doc));
  const [baseline, setBaseline] = useState<ComicDoc>(() => normalizeComicDoc(source.doc));
  const [undoStack, setUndoStack] = useState<ComicDoc[]>([]);
  const [redoStack, setRedoStack] = useState<ComicDoc[]>([]);
  const [selection, setSelection] = useState<Selection>({kind: 'doc'});
  const [nameDraft, setNameDraft] = useState(() => normalizeComicDoc(source.doc).name);

  const [activeArtifactID, setActiveArtifactID] = useState(source.artifactId ?? '');
  const [loadingRevision, setLoadingRevision] = useState(false);
  const [loadError, setLoadError] = useState('');
  const [missingRefs, setMissingRefs] = useState<Set<string>>(() => new Set(asArray(source.missingRefs)));

  const [mediaAssets, setMediaAssets] = useState<main.ConversationAsset[]>([]);
  const [pickerOpen, setPickerOpen] = useState(false);
  const [pickerError, setPickerError] = useState('');
  const [addingIDs, setAddingIDs] = useState<string[]>([]);

  const [log, setLog] = useState<EditorLogEntry[]>([]);
  const [saving, setSaving] = useState(false);
  const [saveError, setSaveError] = useState('');

  const [tool, setTool] = useState<Tool>('select');
  const [activePageID, setActivePageID] = useState(() => normalizeComicDoc(source.doc).pages[0]?.id ?? '');
  // The polygon pen's placed points, in page coordinates.
  const [draftPoints, setDraftPoints] = useState<ComicPoint[]>([]);
  const [cursorPage, setCursorPage] = useState<ComicPoint | null>(null);
  const [drag, setDrag] = useState<Drag | null>(null);
  // The tray item a pointer-drag carries: artifact id plus its display URL.
  const [trayDrag, setTrayDrag] = useState<{ref: ComicMediaRef; client: {x: number; y: number}} | null>(null);

  // A ref mirror of the doc so async picker adds always commit against the
  // newest state (the video editor's stale-resolve discipline).
  const docRef = useRef(doc);
  useEffect(() => {
    docRef.current = doc;
  }, [doc]);

  const dirty = !sameComicDoc(doc, baseline);
  const problems = useMemo(() => comicProblems(doc), [doc]);
  const page = doc.pages.find((candidate) => candidate.id === activePageID) ?? doc.pages[0];
  const pageAspect = useMemo(() => (page ? comicPageAspect(page) : {ratio: 1, name: comicDefaultAspect}), [page]);
  const pageSize = useMemo(() => comicPageSize(pageAspect.ratio, pageViewWidth), [pageAspect]);
  const selectedPanel = selection.kind === 'panel' ? doc.pages.flatMap((candidate) => candidate.panels).find((panel) => panel.id === selection.panelID) : undefined;
  const refCounts = useMemo(() => {
    const counts: Record<string, number> = {};
    for (const candidate of doc.pages) {
      for (const panel of candidate.panels) {
        if (panel.image) counts[panel.image.ref.artifactId] = (counts[panel.image.ref.artifactId] ?? 0) + 1;
      }
    }
    return counts;
  }, [doc]);

  // commit folds one edit into the document: the previous doc lands on the
  // undo stack (capped), redo clears.
  const commit = (next: ComicDoc) => {
    setUndoStack((stack) => [...stack, docRef.current].slice(-undoLimit));
    setRedoStack([]);
    setDoc(next);
    docRef.current = next;
  };
  // Drags coalesce to ONE undo entry, taken lazily: a pointerdown that never
  // moves (a plain select click) pushes nothing.
  const pushUndoSnapshot = () => {
    setUndoStack((stack) => [...stack, docRef.current].slice(-undoLimit));
    setRedoStack([]);
  };
  const dragChangedRef = useRef(false);
  const applyLive = (next: ComicDoc) => {
    if (sameComicDoc(next, docRef.current)) {
      return;
    }
    if (!dragChangedRef.current) {
      dragChangedRef.current = true;
      pushUndoSnapshot();
    }
    setDoc(next);
    docRef.current = next;
  };
  const undo = () => {
    setUndoStack((stack) => {
      if (!stack.length) return stack;
      const previous = stack[stack.length - 1];
      setRedoStack((redo) => [...redo, docRef.current].slice(-undoLimit));
      setDoc(previous);
      docRef.current = previous;
      return stack.slice(0, -1);
    });
  };
  const redo = () => {
    setRedoStack((stack) => {
      if (!stack.length) return stack;
      const next = stack[stack.length - 1];
      setUndoStack((history) => [...history, docRef.current].slice(-undoLimit));
      setDoc(next);
      docRef.current = next;
      return stack.slice(0, -1);
    });
  };

  useEffect(() => {
    props.onDirtyChange?.(dirty);
  }, [dirty]);

  // The picker's dual-scope candidates: the conversation's assets widened to
  // its library — the same fold the composer performs, images only.
  useEffect(() => {
    let cancelled = false;
    const load = async () => {
      try {
        const conversationAssets = asArray(await AppBindings.ListConversationAssets(sourceConversationID));
        let combined = conversationAssets;
        if (props.libraryID) {
          const libraryAssets = asArray(await AppBindings.ListLibraryAssets(props.libraryID));
          const seen = new Set(combined.map((asset) => asset.id));
          combined = [...combined, ...libraryAssets.filter((asset) => !seen.has(asset.id))];
        }
        if (!cancelled) {
          setMediaAssets(combined.filter((asset) => asset.kind === 'image'));
        }
      } catch (error) {
        if (!cancelled) {
          setLoadError(`Loading the conversation's media failed: ${formatEditorError(error)}`);
        }
      }
    };
    void load();
    return () => {
      cancelled = true;
    };
  }, [sourceConversationID, props.libraryID]);

  const svgRef = useRef<SVGSVGElement | null>(null);
  const clientToPage = (clientX: number, clientY: number): ComicPoint => {
    const rect = svgRef.current?.getBoundingClientRect();
    if (!rect || rect.width <= 0 || rect.height <= 0) {
      return {x: 0, y: 0};
    }
    return {
      x: Math.min(1, Math.max(0, (clientX - rect.left) / rect.width)),
      y: Math.min(1, Math.max(0, (clientY - rect.top) / rect.height)),
    };
  };

  // ——— panel gestures ———

  const beginPanelDrag = (event: React.PointerEvent, panelID: string, alt: boolean) => {
    if (event.button !== 0 || props.suspended) {
      return;
    }
    event.preventDefault();
    event.stopPropagation();
    setSelection({kind: 'panel', panelID});
    const panel = docRef.current.pages.flatMap((candidate) => candidate.panels).find((candidate) => candidate.id === panelID);
    if (!panel) {
      return;
    }
    dragChangedRef.current = false;
    if (alt && panel.image && panel.image.fit === 'cover') {
      // Alt-drag pans a cover crop; a plain drag moves the frame.
      setDrag({kind: 'pan-focus', panelID, startClient: {x: event.clientX, y: event.clientY}, startFocusX: panel.image.focusX, startFocusY: panel.image.focusY});
      return;
    }
    setDrag({kind: 'panel-move', panelID, startPage: clientToPage(event.clientX, event.clientY), original: panel.points.map((point) => ({...point}))});
  };

  const beginVertexDrag = (event: React.PointerEvent, panelID: string, index: number) => {
    if (event.button !== 0 || props.suspended) {
      return;
    }
    event.preventDefault();
    event.stopPropagation();
    setSelection({kind: 'panel', panelID});
    dragChangedRef.current = false;
    setDrag({kind: 'vertex', panelID, index});
  };

  // insertVertex splits the edge under the cursor at its midpoint (commit —
  // one undo entry); deleteVertex removes one (⌥-click), refusing when the
  // result would break the geometry gate.
  const insertVertex = (panelID: string, edgeIndex: number) => {
    const panel = docRef.current.pages.flatMap((candidate) => candidate.panels).find((candidate) => candidate.id === panelID);
    if (!panel || panel.points.length >= comicMaxPanelVertices) {
      return;
    }
    const a = panel.points[edgeIndex];
    const b = panel.points[(edgeIndex + 1) % panel.points.length];
    const ring = [...panel.points.map((point) => ({...point}))];
    ring.splice(edgeIndex + 1, 0, {x: (a.x + b.x) / 2, y: (a.y + b.y) / 2});
    commit(setPanelPoints(docRef.current, panelID, ring));
  };

  const deleteVertex = (panelID: string, index: number) => {
    const panel = docRef.current.pages.flatMap((candidate) => candidate.panels).find((candidate) => candidate.id === panelID);
    if (!panel || panel.points.length <= 3) {
      return;
    }
    const ring = panel.points.filter((_, candidateIndex) => candidateIndex !== index);
    if (!validPanelGeometry(ring)) {
      return;
    }
    commit(setPanelPoints(docRef.current, panelID, ring));
  };

  const placePolygonPoint = (event: React.PointerEvent | React.MouseEvent) => {
    if (props.suspended) {
      return;
    }
    const point = clientToPage(event.clientX, event.clientY);
    if (draftPoints.length >= 3) {
      const first = draftPoints[0];
      const dx = (point.x - first.x) * pageSize.width;
      const dy = (point.y - first.y) * pageSize.height;
      if (Math.hypot(dx, dy) <= 14) {
        closePolygonDraft();
        return;
      }
    }
    setDraftPoints((points) => (points.length >= comicMaxPanelVertices ? points : [...points, point]));
    setCursorPage(point);
  };

  const closePolygonDraft = () => {
    if (draftPoints.length < 3 || !page) {
      setDraftPoints([]);
      return;
    }
    const next = addPanel(docRef.current, page.id, draftPoints);
    const created = next.pages.find((candidate) => candidate.id === page.id)?.panels.find((panel) => !docRef.current.pages.flatMap((candidate) => candidate.panels).some((existing) => existing.id === panel.id));
    commit(next);
    if (created) {
      setSelection({kind: 'panel', panelID: created.id});
    }
    setDraftPoints([]);
    setTool('select');
  };

  const cancelDraft = () => {
    setDraftPoints([]);
    setTool('select');
  };

  // ——— global drag / keyboard effects ———

  useEffect(() => {
    if (!drag) {
      return;
    }
    const onMove = (event: PointerEvent) => {
      switch (drag.kind) {
        case 'panel-move': {
          const current = docRef.current;
          const panel = current.pages.flatMap((candidate) => candidate.panels).find((candidate) => candidate.id === drag.panelID);
          if (!panel) {
            return;
          }
          const now = clientToPage(event.clientX, event.clientY);
          const dx = now.x - drag.startPage.x;
          const dy = now.y - drag.startPage.y;
          applyLive(setPanelPoints(current, drag.panelID, clampRing(drag.original.map((point) => ({x: point.x + dx, y: point.y + dy})))));
          return;
        }
        case 'vertex': {
          const current = docRef.current;
          const panel = current.pages.flatMap((candidate) => candidate.panels).find((candidate) => candidate.id === drag.panelID);
          if (!panel) {
            return;
          }
          const now = clientToPage(event.clientX, event.clientY);
          const ring = panel.points.map((point, index) => (index === drag.index ? now : {...point}));
          applyLive(setPanelPoints(current, drag.panelID, clampRing(ring)));
          return;
        }
        case 'rect':
          setCursorPage(clientToPage(event.clientX, event.clientY));
          return;
        case 'pan-focus': {
          const current = docRef.current;
          const panel = current.pages.flatMap((candidate) => candidate.panels).find((candidate) => candidate.id === drag.panelID);
          if (!panel?.image) {
            return;
          }
          const rect = svgRef.current?.getBoundingClientRect();
          if (!rect || rect.width <= 0) {
            return;
          }
          const dx = (event.clientX - drag.startClient.x) / rect.width;
          const dy = (event.clientY - drag.startClient.y) / rect.height;
          applyLive(setPanelFocus(current, drag.panelID, drag.startFocusX - dx, drag.startFocusY - dy));
          return;
        }
      }
    };
    const onUp = () => {
      if (drag.kind === 'rect' && cursorPage && page) {
        const ring = validPanelGeometry(comicRectPoints(Math.min(drag.start.x, cursorPage.x), Math.min(drag.start.y, cursorPage.y), Math.abs(cursorPage.x - drag.start.x), Math.abs(cursorPage.y - drag.start.y)));
        if (ring) {
          const next = addPanel(docRef.current, page.id, ring);
          const created = next.pages.find((candidate) => candidate.id === page.id)?.panels.find((panel) => !docRef.current.pages.flatMap((candidate) => candidate.panels).some((existing) => existing.id === panel.id));
          commit(next);
          if (created) {
            setSelection({kind: 'panel', panelID: created.id});
          }
        }
      }
      dragChangedRef.current = false;
      setDrag(null);
      setCursorPage(null);
    };
    window.addEventListener('pointermove', onMove);
    window.addEventListener('pointerup', onUp);
    return () => {
      window.removeEventListener('pointermove', onMove);
      window.removeEventListener('pointerup', onUp);
    };
  }, [drag, cursorPage, page]);

  // The tray item a pointer carries: a floating ghost follows the cursor and
  // the drop lands by hit test — over a panel assigns, over empty canvas
  // stamps a default rect panel with the image in it. A press that never
  // moves (≤6px) reads as a click: the selected panel takes the image, or a
  // centered default panel is stamped when none is selected.
  useEffect(() => {
    if (!trayDrag) {
      return;
    }
    const start = trayDrag.client;
    const onMove = (event: PointerEvent) => {
      setTrayDrag((current) => (current ? {...current, client: {x: event.clientX, y: event.clientY}} : current));
    };
    const onUp = (event: PointerEvent) => {
      const carried = trayDrag;
      setTrayDrag(null);
      if (!carried || !page) {
        return;
      }
      const clicked = Math.hypot(event.clientX - start.x, event.clientY - start.y) <= 6;
      const svgRect = svgRef.current?.getBoundingClientRect();
      const current = docRef.current;
      const assignOrCreate = (point: ComicPoint) => {
        const target = clicked && selection.kind === 'panel'
          ? current.pages.flatMap((candidate) => candidate.panels).find((panel) => panel.id === selection.panelID)
          : page.panels.find((panel) => comicPointInPolygon(panel.points, point.x, point.y));
        if (target) {
          commit(setPanelImage(current, target.id, carried.ref));
          setSelection({kind: 'panel', panelID: target.id});
          return;
        }
        const halfW = 0.15;
        const halfH = 0.11;
        const ring = comicRectPoints(Math.max(0, point.x - halfW), Math.max(0, point.y - halfH), Math.min(1, point.x + halfW) - Math.max(0, point.x - halfW), Math.min(1, point.y + halfH) - Math.max(0, point.y - halfH));
        const next = addPanel(current, page.id, ring);
        const created = next.pages.find((candidate) => candidate.id === page.id)?.panels.find((panel) => !current.pages.flatMap((candidate) => candidate.panels).some((existing) => existing.id === panel.id));
        if (!created) {
          return;
        }
        commit(setPanelImage(next, created.id, carried.ref));
        setSelection({kind: 'panel', panelID: created.id});
      };
      if (clicked) {
        // A click fills the selected panel, or stamps a centered default.
        const target = selection.kind === 'panel'
          ? current.pages.flatMap((candidate) => candidate.panels).find((panel) => panel.id === selection.panelID)
          : undefined;
        if (target) {
          commit(setPanelImage(current, target.id, carried.ref));
        } else {
          assignOrCreate({x: 0.5, y: 0.5});
        }
        return;
      }
      if (!svgRect) {
        return;
      }
      const within = event.clientX >= svgRect.left && event.clientX <= svgRect.right && event.clientY >= svgRect.top && event.clientY <= svgRect.bottom;
      if (!within) {
        return;
      }
      assignOrCreate(clientToPage(event.clientX, event.clientY));
    };
    window.addEventListener('pointermove', onMove);
    window.addEventListener('pointerup', onUp);
    return () => {
      window.removeEventListener('pointermove', onMove);
      window.removeEventListener('pointerup', onUp);
    };
  }, [trayDrag, page, selection]);

  // deleteSelectedPanel returns the image to the palette (the tray entry
  // never left), so nothing is ever lost by a delete.
  const deleteSelectedPanel = () => {
    if (selection.kind !== 'panel') {
      return;
    }
    commit(removePanel(docRef.current, selection.panelID));
    setSelection({kind: 'doc'});
  };

  useEffect(() => {
    if (props.suspended) {
      return;
    }
    const onKeyDown = (event: KeyboardEvent) => {
      const target = event.target as HTMLElement | null;
      const typing = target && (target.tagName === 'INPUT' || target.tagName === 'TEXTAREA' || target.isContentEditable);
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 'z') {
        event.preventDefault();
        if (event.shiftKey) {
          redo();
        } else {
          undo();
        }
        return;
      }
      if (typing) {
        return;
      }
      if (event.key === 'Escape') {
        if (draftPoints.length > 0 || tool !== 'select') {
          cancelDraft();
        } else {
          setSelection({kind: 'doc'});
        }
        return;
      }
      if (event.key === 'Enter' && tool === 'polygon' && draftPoints.length >= 3) {
        event.preventDefault();
        closePolygonDraft();
        return;
      }
      if ((event.key === 'Delete' || event.key === 'Backspace') && selection.kind === 'panel') {
        event.preventDefault();
        deleteSelectedPanel();
      }
    };
    window.addEventListener('keydown', onKeyDown);
    return () => window.removeEventListener('keydown', onKeyDown);
  });

  // ——— media intake ———

  // addMedia resolves and measures one picked image, then lands it in the
  // unplaced tray — the seed's landing and the picker's are one shape.
  const addMedia = async (asset: main.ConversationAsset) => {
    setPickerError('');
    setAddingIDs((ids) => [...ids, asset.id]);
    try {
      const wireRef = await AppBindings.ResolveComicMedia(sourceConversationID, asset.id);
      commit(addUnplacedImage(docRef.current, normalizeComicRef(wireRef)));
    } catch (error) {
      setPickerError(formatEditorError(error));
    } finally {
      setAddingIDs((ids) => ids.filter((id) => id !== asset.id));
    }
  };

  const imageAssets = mediaAssets;

  // ——— persistence ———

  const persistRevision = async (): Promise<string> => {
    const result = await SaveComicEdit(main.SaveComicEditRequest.createFrom({
      conversationId: sourceConversationID,
      sourceArtifactId: activeArtifactID || undefined,
      doc: toWireDoc(docRef.current),
    }));
    const normalized = normalizeComicDoc(result.doc);
    setLog((entries) => [
      {name: normalized.name, pages: normalized.pages.length, at: new Date().toLocaleTimeString(), artifactId: result.artifactId, url: result.url},
      ...entries,
    ]);
    // Re-baseline on exactly what landed (the server's normalization wins).
    setDoc(normalized);
    setBaseline(normalizeComicDoc(normalized));
    docRef.current = normalized;
    setNameDraft(normalized.name);
    setActiveArtifactID(result.artifactId);
    if (!normalized.pages.some((candidate) => candidate.id === activePageID)) {
      setActivePageID(normalized.pages[0]?.id ?? '');
    }
    props.onSavedToConversation?.();
    return result.artifactId;
  };

  const saveDoc = async () => {
    setSaving(true);
    setSaveError('');
    try {
      await persistRevision();
    } catch (error) {
      setSaveError(formatEditorError(error));
    } finally {
      setSaving(false);
    }
  };

  const loadRevision = async (artifactID: string) => {
    setLoadingRevision(true);
    setLoadError('');
    try {
      const reloaded = await AppBindings.ResolveComicEditSource(sourceConversationID, artifactID);
      const normalized = normalizeComicDoc(reloaded.doc);
      setDoc(normalized);
      setBaseline(normalizeComicDoc(normalized));
      docRef.current = normalized;
      setActiveArtifactID(reloaded.artifactId ?? artifactID);
      setMissingRefs(new Set(asArray(reloaded.missingRefs)));
      setActivePageID(normalized.pages[0]?.id ?? '');
      setSelection({kind: 'doc'});
      setUndoStack([]);
      setRedoStack([]);
    } catch (error) {
      setLoadError(formatEditorError(error));
    } finally {
      setLoadingRevision(false);
    }
  };

  const downloadSaved = async (url: string, index: number) => {
    try {
      await SaveComic(main.SaveTranscriptRequest.createFrom({
        path: url,
        suggestedName: `${doc.name.replace(/\s+/g, '-').toLowerCase() || 'comic'}-${Date.now()}-${index + 1}`,
      }));
    } catch (error) {
      setSaveError(formatEditorError(error));
    }
  };

  // ——— derived render values ———

  const gridStamp = (rows: number, cols: number) => {
    if (!page) {
      return;
    }
    let next = docRef.current;
    for (const ring of comicGridPanels(rows, cols, docRef.current.style.gutter)) {
      next = addPanel(next, page.id, ring);
    }
    commit(next);
    setSelection({kind: 'doc'});
  };

  const missingInPanel = selectedPanel?.image ? missingRefs.has(selectedPanel.image.ref.artifactId) : false;

  const renderPanel = (panel: (typeof page.panels)[number]) => {
    const points = comicPolygonPointsString(panel.points, pageSize.width);
    const selected = selection.kind === 'panel' && selection.panelID === panel.id;
    const missing = panel.image ? missingRefs.has(panel.image.ref.artifactId) : false;
    return (
      <g key={panel.id}>
        {panel.image ? (
          <defs>
            <clipPath id={`comic-clip-${panel.id}`}>
              <polygon points={points} />
            </clipPath>
          </defs>
        ) : null}
        <polygon
          points={points}
          className={`comic-panel-bg${selected ? ' selected' : ''}${missing ? ' missing' : ''}`}
          onPointerDown={(event) => beginPanelDrag(event, panel.id, event.altKey)}
          onDoubleClick={(event) => {
            if (tool !== 'select' || props.suspended) return;
            // A double-click on the frame inserts a vertex at the nearest
            // edge midpoint (the irregular-panel affordance).
            const point = clientToPage(event.clientX, event.clientY);
            let best = -1;
            let bestDistance = Infinity;
            panel.points.forEach((a, index) => {
              const b = panel.points[(index + 1) % panel.points.length];
              const mx = (a.x + b.x) / 2;
              const my = (a.y + b.y) / 2;
              const distance = Math.hypot((point.x - mx) * pageSize.width, (point.y - my) * pageSize.height);
              if (distance < bestDistance) {
                bestDistance = distance;
                best = index;
              }
            });
            if (best >= 0 && bestDistance <= 24) {
              insertVertex(panel.id, best);
            }
          }}
        >
          <title>{panel.image ? panel.image.ref.artifactId : 'Empty panel — drag art from the tray'}</title>
        </polygon>
        {panel.image && !missing ? (() => {
          const placement = comicImagePlacement(panel.points, panel.image.ref.width, panel.image.ref.height, panel.image.fit, panel.image.focusX, panel.image.focusY, panel.image.zoom, pageSize.width);
          return (
            <image
              href={artifactImageURL(panel.image.ref)}
              x={placement.x}
              y={placement.y}
              width={placement.width}
              height={placement.height}
              clipPath={`url(#comic-clip-${panel.id})`}
              preserveAspectRatio="none"
              style={{pointerEvents: 'none'}}
            />
          );
        })() : null}
        <polygon points={points} className={`comic-panel-border${missing ? ' missing' : ''}`} style={{strokeWidth: doc.style.border * pageSize.width, pointerEvents: 'none'}} />
        {selected ? (
          <>
            {panel.points.map((point, index) => (
              <circle
                key={index}
                cx={point.x * pageSize.width}
                cy={point.y * pageSize.height}
                r={7}
                className="comic-vertex"
                onPointerDown={(event) => beginVertexDrag(event, panel.id, index)}
                onClick={(event) => {
                  if (event.altKey) {
                    deleteVertex(panel.id, index);
                  }
                }}
              >
                <title>Drag to move the corner · ⌥-click to delete</title>
              </circle>
            ))}
            {panel.points.map((a, index) => {
              const b = panel.points[(index + 1) % panel.points.length];
              return (
                <circle
                  key={`edge-${index}`}
                  cx={((a.x + b.x) / 2) * pageSize.width}
                  cy={((a.y + b.y) / 2) * pageSize.height}
                  r={5}
                  className="comic-edge-midpoint"
                  onClick={() => tool === 'select' && insertVertex(panel.id, index)}
                >
                  <title>Click to add a corner here</title>
                </circle>
              );
            })}
          </>
        ) : null}
      </g>
    );
  };

  const rectPreview = drag?.kind === 'rect' && cursorPage ? (
    <rect
      x={Math.min(drag.start.x, cursorPage.x) * pageSize.width}
      y={Math.min(drag.start.y, cursorPage.y) * pageSize.height}
      width={Math.abs(cursorPage.x - drag.start.x) * pageSize.width}
      height={Math.abs(cursorPage.y - drag.start.y) * pageSize.height}
      className="comic-rect-preview"
    />
  ) : null;

  const polygonPreview = tool === 'polygon' && draftPoints.length > 0 ? (
    <g style={{pointerEvents: 'none'}}>
      <polygon
        points={draftPoints.concat(cursorPage && draftPoints.length >= 2 ? [cursorPage] : []).map((point) => `${point.x * pageSize.width},${point.y * pageSize.height}`).join(' ')}
        className="comic-polygon-preview"
      />
      {draftPoints.map((point, index) => (
        <circle key={index} cx={point.x * pageSize.width} cy={point.y * pageSize.height} r={index === 0 && draftPoints.length >= 3 ? 9 : 5} className={index === 0 && draftPoints.length >= 3 ? 'comic-vertex first' : 'comic-vertex'} />
      ))}
    </g>
  ) : null;

  return (
    <section className="comic-editor">
      <EditorHeader
        title={doc.name}
        parentAvailable
        parentConversationID={sourceConversationID}
        busyLabel={saving ? 'Saving…' : loadingRevision ? 'Loading…' : ''}
        onClose={props.onClose}
        onOpenParent={props.onOpenParent}
      />
      <div className="comic-toolbar">
        <input
          className="comic-name"
          value={nameDraft}
          onChange={(event) => {
            setNameDraft(event.target.value);
            commit(renameComic(docRef.current, event.target.value));
          }}
          onBlur={() => setNameDraft(doc.name)}
          aria-label="Comic name"
        />
        <span className="comic-summary">{comicDocSummary(doc)}</span>
        <div className="comic-toolbar-spacer" />
        <div className="comic-tools" role="group" aria-label="Panel tools">
          <button type="button" className={tool === 'select' ? 'active' : ''} title="Select and shape panels (⌥-drag inside a cover panel pans the crop)" onClick={() => cancelDraft()}>Select</button>
          <button type="button" className={tool === 'rect' ? 'active' : ''} title="Drag on the page to draw a rectangular panel" onClick={() => {setDraftPoints([]); setTool('rect');}}>Rect</button>
          <button type="button" className={tool === 'polygon' ? 'active' : ''} title="Click to place corners; click the first corner or press Enter to close" onClick={() => {setDraftPoints([]); setTool('polygon');}}>Pen</button>
        </div>
        <div className="comic-tools" role="group" aria-label="Grid stamps">
          {([[1, 2], [2, 2], [2, 3], [3, 3]] as const).map(([rows, cols]) => (
            <button key={`${rows}x${cols}`} type="button" title={`Stamp a ${rows}×${cols} grid of panels`} onClick={() => gridStamp(rows, cols)}>
              {rows}×{cols}
            </button>
          ))}
        </div>
        <button type="button" className="comic-tool" disabled={tool !== 'select' && draftPoints.length === 0} title="Back to selecting (Esc also works)" onClick={() => cancelDraft()}>Done</button>
        <div className="comic-toolbar-spacer" />
        <button type="button" onClick={undo} disabled={!undoStack.length} title="Undo (⌘Z)">↺</button>
        <button type="button" onClick={redo} disabled={!redoStack.length} title="Redo (⇧⌘Z)">↻</button>
        <button type="button" className="editor-generate" disabled={saving || problems.length > 0} title={problems.length > 0 ? problems[0] : 'Save the working book as a new revision on the conversation'} onClick={() => void saveDoc()}>
          {saving ? 'Saving…' : activeArtifactID ? 'Save revision' : 'Save to conversation'}
        </button>
      </div>
      {problems.length > 0 ? (
        <div className="comic-warning">
          {problems.slice(0, 3).map((problem, index) => (
            <div key={index}>{problem}</div>
          ))}
        </div>
      ) : null}
      {missingRefs.size > 0 ? (
        <div className="comic-warning">
          {Array.from(missingRefs).map((id) => (
            <div key={id}>The file for {id} is missing on disk — its panels render empty until the asset is restored.</div>
          ))}
        </div>
      ) : null}
      {asArray(source.warnings).length > 0 && log.length === 0 && !activeArtifactID ? (
        <div className="comic-seed-warnings">
          {asArray(source.warnings).map((warning, index) => (
            <div key={index}>{warning}</div>
          ))}
        </div>
      ) : null}
      <div className="comic-editor-main">
        <div className="comic-workbench">
          <div className="comic-pages" role="tablist" aria-label="Pages">
            {doc.pages.map((candidate, index) => (
              <div key={candidate.id} className={`comic-page-tab${candidate.id === page?.id ? ' active' : ''}`}>
                <button type="button" role="tab" aria-selected={candidate.id === page?.id} onClick={() => {setActivePageID(candidate.id); setSelection({kind: 'doc'}); setDraftPoints([]);}}>
                  {index + 1} · {comicPageAspect(candidate).name === 'custom' ? `${comicPageAspect(candidate).ratio.toFixed(2)}` : comicPageAspect(candidate).name}
                </button>
                {doc.pages.length > 1 ? (
                  <button type="button" className="comic-page-tab-remove" title={`Delete page ${index + 1}`} onClick={() => {commit(removePage(docRef.current, candidate.id)); if (activePageID === candidate.id) setActivePageID(docRef.current.pages[0]?.id ?? '');}}>×</button>
                ) : null}
              </div>
            ))}
            <button type="button" className="comic-page-add" title="Add a page" disabled={doc.pages.length >= 64} onClick={() => {const next = addPage(docRef.current); commit(next); setActivePageID(next.pages[next.pages.length - 1].id); setSelection({kind: 'doc'});}}>＋</button>
            <button type="button" className="comic-page-add" title="Duplicate this page" disabled={!page} onClick={() => {if (page) {const next = duplicatePage(docRef.current, page.id); commit(next); setActivePageID(next.pages[doc.pages.indexOf(page) + 1]?.id ?? page.id);}}}>⧉</button>
          </div>
          <div className="comic-stage">
            {page ? (
              <div className="comic-page-frame" style={{aspectRatio: `${pageSize.width} / ${pageSize.height}`}}>
                <svg
                  ref={svgRef}
                  className={`comic-page-svg tool-${tool}`}
                  viewBox={`0 0 ${pageSize.width} ${pageSize.height}`}
                  onPointerDown={(event) => {
                    if (event.button !== 0) return;
                    if (tool === 'rect') {
                      dragChangedRef.current = false;
                      setDrag({kind: 'rect', start: clientToPage(event.clientX, event.clientY)});
                      setCursorPage(clientToPage(event.clientX, event.clientY));
                    } else if (tool === 'polygon') {
                      placePolygonPoint(event);
                    } else {
                      setSelection({kind: 'doc'});
                    }
                  }}
                  onDoubleClick={(event) => {
                    if (tool === 'polygon' && draftPoints.length >= 3) {
                      event.preventDefault();
                      closePolygonDraft();
                    }
                  }}
                  onPointerMove={(event) => {
                    if (tool === 'polygon' || drag?.kind === 'rect') {
                      setCursorPage(clientToPage(event.clientX, event.clientY));
                    }
                  }}
                >
                  <rect x={0} y={0} width={pageSize.width} height={pageSize.height} className="comic-page-bg" />
                  {page.panels.map(renderPanel)}
                  {rectPreview}
                  {polygonPreview}
                </svg>
              </div>
            ) : (
              <div className="comic-stage-empty">This book has no pages.</div>
            )}
            <div className="comic-tray" aria-label="Unplaced art">
              <div className="comic-tray-head">
                <span>Art tray</span>
                <button type="button" onClick={() => setPickerOpen(true)} title="Add images from the conversation or its library">＋ Add art</button>
              </div>
              {doc.unplaced.length === 0 ? (
                <p className="comic-tray-empty">No art yet — picked images wait here until a panel takes them.</p>
              ) : (
                <div className="comic-tray-items">
                  {doc.unplaced.map((ref) => (
                    <div
                      key={ref.artifactId}
                      className={`comic-tray-item${trayDrag?.ref.artifactId === ref.artifactId ? ' dragging' : ''}`}
                      title={`${ref.artifactId} · ${ref.width}×${ref.height} — drag onto the page, or click to fill the selected panel`}
                      onPointerDown={(event) => {
                        if (event.button !== 0 || props.suspended) return;
                        event.preventDefault();
                        setTrayDrag({ref, client: {x: event.clientX, y: event.clientY}});
                      }}
                    >
                      <img src={artifactImageURL(ref)} alt={ref.artifactId} draggable={false} />
                      <span>{ref.artifactId.slice(0, 10)}</span>
                      <button
                        type="button"
                        className="comic-tray-remove"
                        title="Remove from the tray (panels keep their copies)"
                        onClick={(event) => {
                          event.stopPropagation();
                          commit(removeUnplacedImage(docRef.current, ref.artifactId));
                        }}
                      >
                        ×
                      </button>
                    </div>
                  ))}
                </div>
              )}
            </div>
          </div>
        </div>
        <aside className="comic-inspector">
          {pickerOpen ? (
            <MediaPicker
              assets={imageAssets}
              editedConversationID={sourceConversationID}
              kinds={['image']}
              refCounts={refCounts}
              busyIDs={addingIDs}
              error={pickerError}
              ariaLabel="Add art to the comic"
              emptyText="No images found — attach or generate art in the conversation first (HEIC/AVIF sources need convert_image first)."
              refNoun="comic"
              onAdd={(asset) => void addMedia(asset)}
              onClose={() => setPickerOpen(false)}
            />
          ) : null}
          {selection.kind === 'panel' && selectedPanel ? (
            <>
              <strong>Panel</strong>
              <p className="hint">{selectedPanel.points.length} corners · {selectedPanel.image ? `${selectedPanel.image.ref.width}×${selectedPanel.image.ref.height} art` : 'no art'}</p>
              {selectedPanel.image ? (
                <>
                  <div className="comic-field-row">
                    <label className="comic-field" title="Cover fills the frame and crops around the focus; contain fits the whole image inside">
                      <span>Fit</span>
                      <select value={selectedPanel.image.fit} onChange={(event) => commit(setPanelFit(docRef.current, selectedPanel.id, event.target.value === 'contain' ? 'contain' : 'cover'))}>
                        <option value="cover">Cover (fill &amp; crop)</option>
                        <option value="contain">Contain (fit whole)</option>
                      </select>
                    </label>
                  </div>
                  {selectedPanel.image.fit === 'cover' ? (
                    <>
                      <label className="comic-range" title="Which part of the image a cover crop shows horizontally — or ⌥-drag inside the panel">
                        <span>Crop X</span>
                        <input type="range" min={0} max={1} step={0.01} value={selectedPanel.image.focusX} onChange={(event) => applyLive(setPanelFocus(docRef.current, selectedPanel.id, parseFloat(event.target.value), selectedPanel.image!.focusY))} onPointerUp={() => {dragChangedRef.current = false;}} />
                      </label>
                      <label className="comic-range" title="Which part of the image a cover crop shows vertically">
                        <span>Crop Y</span>
                        <input type="range" min={0} max={1} step={0.01} value={selectedPanel.image.focusY} onChange={(event) => applyLive(setPanelFocus(docRef.current, selectedPanel.id, selectedPanel.image!.focusX, parseFloat(event.target.value)))} onPointerUp={() => {dragChangedRef.current = false;}} />
                      </label>
                      <label className="comic-range" title="Push in on a detail (1 = fill exactly)">
                        <span>Zoom</span>
                        <input type="range" min={1} max={4} step={0.05} value={selectedPanel.image.zoom} onChange={(event) => applyLive(setPanelZoom(docRef.current, selectedPanel.id, parseFloat(event.target.value)))} onPointerUp={() => {dragChangedRef.current = false;}} />
                      </label>
                    </>
                  ) : null}
                  {missingInPanel ? <p className="comic-warning">This panel's image file is missing on disk.</p> : null}
                  <button type="button" className="comic-danger" onClick={() => commit(setPanelImage(docRef.current, selectedPanel.id, null))}>Remove art</button>
                </>
              ) : (
                <p className="hint">Drag art from the tray onto this panel.</p>
              )}
              <div className="comic-field-row">
                <button type="button" title="Paint this panel over its neighbors (z-order)" onClick={() => {if (page) commit(movePanel(docRef.current, page.id, selectedPanel.id, page.panels.length - 1));}}>Bring to front</button>
                <button type="button" title="Sink this panel beneath its neighbors" onClick={() => {if (page) commit(movePanel(docRef.current, page.id, selectedPanel.id, 0));}}>Send to back</button>
              </div>
              <button type="button" className="comic-danger" onClick={deleteSelectedPanel}>Delete panel</button>
            </>
          ) : (
            <>
              <strong>Page</strong>
              <label className="comic-field" title="The page's shape">
                <span>Aspect</span>
                <select value={page?.aspect ?? comicDefaultAspect} onChange={(event) => {if (page) commit(setPageAspect(docRef.current, page.id, event.target.value));}} disabled={!page}>
                  {comicAspectPresets.map((preset) => (
                    <option key={preset.name} value={preset.name}>{preset.label}</option>
                  ))}
                  <option value="custom">Custom…</option>
                </select>
              </label>
              {page?.aspect === 'custom' ? (
                <label className="comic-field" title="Width ÷ height, clamped to 0.25–4">
                  <span>Ratio (w÷h)</span>
                  <input
                    type="number"
                    step={0.05}
                    min={0.25}
                    max={4}
                    value={page.customAspect || 0.75}
                    onChange={(event) => {if (page) commit(setPageAspect(docRef.current, page.id, 'custom', parseFloat(event.target.value)));}}
                    disabled={!page}
                  />
                </label>
              ) : null}
              <strong>Book</strong>
              <label className="comic-range" title="The whitespace the grid stamps leave between panels">
                <span>Gutter</span>
                <input type="range" min={0} max={comicGutterMax} step={0.002} value={doc.style.gutter} onChange={(event) => applyLive(setComicStyle(docRef.current, {gutter: parseFloat(event.target.value)}))} onPointerUp={() => {dragChangedRef.current = false;}} />
                <span className="comic-range-value">{Math.round(doc.style.gutter * 1000) / 10}%</span>
              </label>
              <label className="comic-range" title="Every panel's border stroke, as a share of page width">
                <span>Border</span>
                <input type="range" min={0} max={comicBorderMax} step={0.0005} value={doc.style.border} onChange={(event) => applyLive(setComicStyle(docRef.current, {border: parseFloat(event.target.value)}))} onPointerUp={() => {dragChangedRef.current = false;}} />
                <span className="comic-range-value">{Math.round(doc.style.border * 1000) / 10}%</span>
              </label>
              <p className="hint">Select a panel to shape it: drag corners, ⌥-click a corner to remove it, double-click an edge to add one.</p>
            </>
          )}
          {saveError ? <div className="editor-error">{saveError}</div> : null}
          {loadError ? <div className="editor-error">{loadError}</div> : null}
        </aside>
      </div>
      <EditsPanel count={log.length} emptyText="Nothing saved yet." bodyReservePx={editsPanelBodyReservePx}>
        <ul className="editor-op-list">
          {log.map((entry, index) => (
            <li key={index} className="editor-op editor-op-done">
              <span className="editor-op-thumb-glyph">✓</span>
              <div className="editor-op-meta">
                <span className="editor-op-title">Saved · {entry.name} · {entry.pages} {entry.pages === 1 ? 'page' : 'pages'} · {entry.artifactId.slice(0, 12)}</span>
                <span className="editor-op-sub" title={entry.url}>{entry.at} → attached to the conversation</span>
              </div>
              <div className="editor-op-actions">
                <button
                  type="button"
                  disabled={loadingRevision || entry.artifactId === activeArtifactID}
                  title={entry.artifactId === activeArtifactID ? 'This revision is already on the stage' : 'Continue editing from this revision'}
                  onClick={() => void loadRevision(entry.artifactId)}
                >
                  Use as source
                </button>
                <button type="button" onClick={() => void downloadSaved(entry.url, index)}>Download</button>
              </div>
            </li>
          ))}
        </ul>
      </EditsPanel>
      {trayDrag ? (
        <div className="comic-tray-ghost" style={{left: trayDrag.client.x, top: trayDrag.client.y}}>
          <img src={artifactImageURL(trayDrag.ref)} alt="" draggable={false} />
        </div>
      ) : null}
    </section>
  );
}

// Local imports kept beside the component body (the model is pure; this one
// adapter keeps the picker's landing terse).
function normalizeComicRef(wire: any): ComicMediaRef {
  return normalizeComicDoc({pages: [], unplaced: [wire]}).unplaced[0];
}
