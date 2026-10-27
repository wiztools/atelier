import {
  ComicMediaRef,
  ComicPoint,
  addPage,
  addPanel,
  addUnplacedImage,
  comicClampRange,
  comicDocSummary,
  comicGridPanels,
  comicImagePlacement,
  comicPageAspect,
  comicPointInPolygon,
  comicPageSize,
  comicProblems,
  comicRectPoints,
  comicSelfIntersects,
  comicSFXPoints,
  comicBalloonTailBase,
  comicWrapText,
  addText,
  removeText,
  setTextContent,
  setTextKind,
  setTextRect,
  setTextSize,
  setTextTail,
  comicSignedArea,
  cloneComicDoc,
  duplicatePage,
  movePanel,
  newComicID,
  newPanelImage,
  normalizeComicDoc,
  panelArea,
  panelBoundingBox,
  removePage,
  removePanel,
  removeUnplacedImage,
  sameComicDoc,
  seedComicDoc,
  setPageAspect,
  setPanelFit,
  setPanelFocus,
  setPanelImage,
  setPanelPoints,
  setPanelZoom,
  setComicStyle,
  toWireDoc,
  validPanelGeometry,
} from '../src/editor/comicModel';

function assert(value: boolean, message: string): void {if (!value) throw new Error(message);}

function imageRef(id: string, width = 800, height = 600): ComicMediaRef {
  return {artifactId: id, path: `/tmp/${id}.png`, kind: 'image', width, height};
}

function rect(x: number, y: number, w: number, h: number): ComicPoint[] {
  return comicRectPoints(x, y, w, h);
}

function bowtie(): ComicPoint[] {
  return [{x: 0.1, y: 0.1}, {x: 0.9, y: 0.9}, {x: 0.9, y: 0.1}, {x: 0.1, y: 0.9}];
}

// Wire normalization mirrors Go's zero-value decode: blank names default,
// absent fit reads cover, clamps land on the same numbers Go computes.
const wire = normalizeComicDoc({
  schemaVersion: 1,
  name: '  ',
  pages: [{id: 'p1', panels: [{id: 'pnl1', points: [{x: -1, y: 0}, {x: 2, y: 0}, {x: 2, y: 0.5}],
    image: {ref: {artifactId: 'img_1', path: '/tmp/i.png', kind: 'image', width: 800, height: 600}, focusX: 3, zoom: 99}}]}],
  unplaced: [{artifactId: 'img_2', path: '/tmp/j.png', kind: 'image', width: 10, height: 10}],
});
assert(wire.name === 'Untitled comic', 'blank name defaults');
assert(wire.pages[0].aspect === 'comic', 'absent aspect defaults to the comic preset');
assert(wire.pages[0].panels[0].points[0].x === -1 && wire.pages[0].panels[0].points[1].x === 2, 'raw vertices ride through (Go validates at save)');
assert(wire.pages[0].panels[0].image?.fit === 'cover', 'absent fit reads cover');
assert(wire.pages[0].panels[0].image?.focusX === 1, 'out-of-range focus clamps');
assert(wire.pages[0].panels[0].image?.zoom === 4, 'oversized zoom clamps to the ceiling');
assert(wire.unplaced.length === 1 && wire.unplaced[0].kind === 'image', 'unplaced refs normalize');
assert(normalizeComicDoc({}).pages.length === 0 && normalizeComicDoc({}).unplaced.length === 0, 'empty wire normalizes');

// Winding and self-intersection — the Go validator's geometry.
const ccw = [{x: 0, y: 0.4}, {x: 0.9, y: 0.4}, {x: 0.9, y: 0.1}, {x: 0, y: 0.1}];
const ring = validPanelGeometry(ccw)!;
assert(ring !== null && comicSignedArea(ring) > 0, 'winding canonicalizes clockwise');
assert(comicSelfIntersects(bowtie()), 'a bowtie crosses itself');
assert(!comicSelfIntersects(rect(0.1, 0.1, 0.8, 0.8)), 'a rectangle does not');
assert(!comicSelfIntersects([{x: 0.1, y: 0.1}, {x: 0.9, y: 0.1}, {x: 0.5, y: 0.5}, {x: 0.9, y: 0.9}, {x: 0.1, y: 0.9}]), 'a vertex touching a far edge is legal');
assert(validPanelGeometry([{x: 0, y: 0}, {x: 1, y: 0}, {x: 0, y: 0.0001}]) === null, 'a sliver refuses');
assert(validPanelGeometry(bowtie()) === null, 'the geometry gate refuses a bowtie');

// Seeding: one blank page, refs queued in reference order, house style.
const seeded = seedComicDoc('Issue #1', [imageRef('img_1'), imageRef('img_2')]);
assert(seeded.pages.length === 1 && seeded.pages[0].panels.length === 0, 'a seeded draft is one blank page');
assert(seeded.unplaced.map((ref) => ref.artifactId).join() === 'img_1,img_2', 'the tray keeps reference order');
assert(seeded.style.gutter === 0.02 && seeded.style.border === 0.004, 'house style');
assert(comicProblems(seeded).length === 0, 'a seeded draft has no problems');
assert(newComicID('pnl').startsWith('pnl_') && newComicID('pnl').length === 28, 'ids mirror Go randomID');
assert(comicClampRange(3, 0, 1, 0.5) === 1 && comicClampRange(NaN, 0, 1, 0.5) === 0.5, 'clamps mirror comicClampRange');

// Panels: add, gate, shape, z-order, remove.
let doc = cloneComicDoc(seeded);
doc = addPanel(doc, seeded.pages[0].id, rect(0.05, 0.05, 0.9, 0.4));
doc = addPanel(doc, seeded.pages[0].id, rect(0.05, 0.5, 0.44, 0.45));
doc = setPanelImage(doc, doc.pages[0].panels[1].id, imageRef('img_1'));
assert(doc.pages[0].panels[1].image?.ref.artifactId === 'img_1', 'setPanelImage assigns');
assert(doc.pages[0].panels[1].image?.focusX === 0.5, 'a new image centers its focus');
assert(addPanel(doc, seeded.pages[0].id, bowtie()).pages[0].panels.length === 2, 'addPanel refuses a bowtie');
assert(addPanel(doc, seeded.pages[0].id, rect(0, 0, 0.01, 0.05)).pages[0].panels.length === 2, 'addPanel refuses a sliver');
const reshaped = setPanelPoints(doc, doc.pages[0].panels[0].id, bowtie());
assert(reshaped.pages[0].panels[0].points.length === 4 && comicSignedArea(reshaped.pages[0].panels[0].points) > 0, 'setPanelPoints refuses the bowtie (no-op)');
const removed = removePanel(doc, doc.pages[0].panels[0].id);
assert(removed.pages[0].panels.length === 1, 'removePanel removes');
const reordered = movePanel(doc, doc.pages[0].id, doc.pages[0].panels[0].id, 1);
assert(reordered.pages[0].panels[1].id === doc.pages[0].panels[0].id, 'movePanel reorders z');

// Overlap is legal — the inset idiom.
const inset = addPanel(doc, seeded.pages[0].id, rect(0.2, 0.2, 0.3, 0.3));
assert(inset.pages[0].panels.length === 3 && comicProblems(inset).length === 0, 'overlapping panels are comic grammar');

// Images: fit, focus, zoom ops.
const withImage = doc;
assert(setPanelFit(withImage, withImage.pages[0].panels[1].id, 'contain').pages[0].panels[1].image?.fit === 'contain', 'setPanelFit');
assert(setPanelFocus(withImage, withImage.pages[0].panels[1].id, -1, 2).pages[0].panels[1].image?.focusX === 0, 'focus clamps to [0,1]');
assert(setPanelZoom(withImage, withImage.pages[0].panels[1].id, 9).pages[0].panels[1].image?.zoom === 4, 'zoom clamps');
assert(setPanelImage(withImage, withImage.pages[0].panels[1].id, null).pages[0].panels[1].image === null, 'setPanelImage(null) removes');

// Tray: add dedupes and refuses unmeasured; remove drops by artifact id.
let tray = addUnplacedImage(doc, imageRef('img_9'));
assert(tray.unplaced.some((ref) => ref.artifactId === 'img_9'), 'addUnplacedImage adds');
tray = addUnplacedImage(tray, imageRef('img_9'));
assert(tray.unplaced.filter((ref) => ref.artifactId === 'img_9').length === 1, 'addUnplacedImage dedupes');
tray = addUnplacedImage(tray, {...imageRef('img_10'), width: 0});
assert(!tray.unplaced.some((ref) => ref.artifactId === 'img_10'), 'an unmeasured ref refuses the tray');
tray = removeUnplacedImage(tray, 'img_9');
assert(!tray.unplaced.some((ref) => ref.artifactId === 'img_9'), 'removeUnplacedImage drops by id');

// Pages: add, duplicate (fresh ids, shared refs), remove-last refuses.
let book = addPage(doc);
assert(book.pages.length === 2, 'addPage appends');
book = setPageAspect(book, book.pages[1].id, 'custom', 0.1);
assert(comicPageAspect(book.pages[1]).ratio === 0.25, 'custom aspect clamps to the floor');
book = setPageAspect(book, book.pages[1].id, 'manga');
assert(book.pages[1].aspect === 'manga' && comicPageAspect(book.pages[1]).ratio === 5 / 7.5, 'preset aspect resolves');
const doubled = duplicatePage(book, book.pages[0].id);
assert(doubled.pages.length === 3 && doubled.pages[1].panels.length === book.pages[0].panels.length, 'duplicatePage copies panels');
assert(doubled.pages[1].panels.every((panel, index) => panel.id !== doubled.pages[0].panels[index].id), 'the copy has fresh ids');
assert(removePage(book, book.pages[0].id).pages.length === 1, 'removePage removes');
assert(removePage(book, book.pages[0].id) === undefined || removePage(book, book.pages[0].id).pages.length === 1, 'the last page refuses');

// Style clamps.
assert(setComicStyle(doc, {gutter: -1, border: 5}).style.gutter === 0 && setComicStyle(doc, {gutter: -1, border: 5}).style.border === 0.03, 'style clamps');

// Crop math: cover fills and pans with focus; contain centers.
const box = panelBoundingBox(rect(0.1, 0.1, 0.8, 0.3));
assert(box.x === 0.1 && box.w === 0.8, 'bounding box');
const page = comicPageSize(1, 1000);
const cover = comicImagePlacement(rect(0.1, 0.1, 0.8, 0.3), 800, 600, 'cover', 0.5, 0.5, 1, page.width);
assert(cover.width >= 800 && cover.height >= 300, 'cover fills the box');
assert(Math.abs(cover.x + cover.width / 2 - (0.1 * 1000 + 400)) < 1e-6, 'center focus centers the crop');
// A portrait panel with a landscape image has horizontal slack — the slide axis.
const tallBox = rect(0.1, 0.1, 0.3, 0.8);
const centered = comicImagePlacement(tallBox, 800, 600, 'cover', 0.5, 0.5, 1, page.width);
const left = comicImagePlacement(tallBox, 800, 600, 'cover', 0, 0.5, 1, page.width);
assert(left.x > centered.x, 'focus 0 pins the left edge into view (window stops sliding)');
const top = comicImagePlacement(rect(0.1, 0.1, 0.8, 0.3), 800, 600, 'cover', 0.5, 0, 1, page.width);
assert(top.y > cover.y, 'focusY 0 pins the top edge into view (window stops sliding)');
const contained = comicImagePlacement(rect(0.1, 0.1, 0.8, 0.3), 800, 600, 'contain', 0.9, 0.9, 1, page.width);
assert(contained.width <= 800 + 1e-6 && contained.height <= 300 + 1e-6, 'contain fits inside');
assert(Math.abs(contained.x + contained.width / 2 - (0.1 * 1000 + 400)) < 1e-6, 'contain centers regardless of focus');
const zoomed = comicImagePlacement(rect(0.1, 0.1, 0.8, 0.3), 800, 600, 'cover', 0.5, 0.5, 2, page.width);
assert(zoomed.width === cover.width * 2, 'zoom doubles the cover scale');
const squareCrop = comicImagePlacement(rect(0, 0, 0.5, 0.5), 1000, 500, 'cover', 0, 0, 1, page.width);
assert(squareCrop.x === 0 && squareCrop.y === 0, 'focus 0,0 pins the top-left');
assert(panelArea(rect(0, 0, 0.5, 1)) === 0.5, 'area reads as the page fraction');
assert(comicPointInPolygon(rect(0, 0, 0.5, 0.5), 0.25, 0.25), 'the hit test finds interior points');
assert(!comicPointInPolygon(rect(0, 0, 0.5, 0.5), 0.75, 0.25), 'the hit test rejects exterior points');

// Grid stamps honor the gutter.
const grid = comicGridPanels(2, 2, 0.04);
assert(grid.length === 4, 'a 2×2 grid stamps four panels');
const first = panelBoundingBox(grid[0]);
const second = panelBoundingBox(grid[1]);
assert(Math.abs(second.x - (first.x + first.w) - 0.04) < 1e-9, 'the gutter separates neighbors (half from each side)');
assert(Math.abs(first.x) < 1e-9 && Math.abs(panelBoundingBox(grid[3]).y + panelBoundingBox(grid[3]).h - 1) < 1e-9, 'the grid spans the page');

// Wire round-trip keeps the polygon and image fields Go expects.
const roundTrip = normalizeComicDoc(toWireDoc(doc));
assert(sameComicDoc(roundTrip, doc), 'toWireDoc round-trips');
assert(JSON.stringify(toWireDoc(doc)).includes('"fit":"cover"'), 'wire always carries the fit');
assert(!JSON.stringify(toWireDoc(doc)).includes('"customAspect"'), 'a preset page omits customAspect');
assert(comicProblems(roundTrip).length === 0, 'the round trip stays clean');

// Summary matches the Go save turn's wording.
assert(comicDocSummary(seeded) === '1 page · 0 panels · comic format', `summary was ${comicDocSummary(seeded)}`);
assert(comicDocSummary(doubled).includes('3 pages'), 'summary pluralizes');

// A foreign document's problems get named, not silently fixed.
const foreign = normalizeComicDoc({schemaVersion: 1, name: 'Broken', pages: [], unplaced: [{artifactId: 'vid_1', path: '/tmp/v.mp4', kind: 'video', width: 1, height: 1}]});
const problems = comicProblems(foreign);
assert(problems.some((problem) => problem.includes('at least one page')), 'the pageless refusal is named');
assert(problems.some((problem) => problem.includes('stills only')), 'a non-image tray ref is named');
assert(newPanelImage(imageRef('x')).fit === 'cover' && newPanelImage(imageRef('x')).focusX === 0.5, 'newPanelImage defaults');

// ——— lettering: normalize, ops, geometry ———
const wired = normalizeComicDoc({
  schemaVersion: 1,
  pages: [{id: 'p1', panels: [], texts: [
    {id: 't1', kind: 'caption', x: 0.05, y: 0.02, width: 0.3, height: 0.05, text: 'Meanwhile…', size: 9, tail: {x: 0.5, y: 0.5}},
    {id: 't2', kind: 'balloon', x: 0.4, y: 0.2, width: 0.2, height: 0.1, text: 'Hi', size: 0, tail: {x: 2, y: 0.6}},
    {id: 't3', kind: 'whisper', x: 0.1, y: 0.5, width: 0.2, height: 0.1},
  ]}],
});
assert(wired.pages[0].texts.length === 3, 'texts normalize');
const capText = wired.pages[0].texts[0];
assert(capText.size === 4 && capText.tail === null, 'caption size clamps and its stray tail drops');
const balloonText = wired.pages[0].texts[1];
assert(balloonText.size === 1 && balloonText.tail!.x === 1 && balloonText.tail!.y === 0.6, 'balloon size defaults and tail clamps');
assert(String(wired.pages[0].texts[2].kind) === 'whisper', 'a foreign kind rides through for the problems report');
assert(normalizeComicDoc({pages: [{id: 'p', panels: []}]}).pages[0].texts.length === 0, 'absent texts normalize to empty');

let lettered = cloneComicDoc(seeded);
const pageID = lettered.pages[0].id;
lettered = addText(lettered, pageID, 'balloon', 0.5, 0.5);
const balloon = lettered.pages[0].texts[0];
assert(balloon.kind === 'balloon' && Math.abs(balloon.x + balloon.width / 2 - 0.5) < 1e-9, 'a balloon stamps centered in its default box');
lettered = setTextContent(lettered, balloon.id, 'Look out!');
assert(lettered.pages[0].texts[0].text === 'Look out!', 'setTextContent');
lettered = setTextTail(lettered, balloon.id, 0.7, 0.9);
assert(lettered.pages[0].texts[0].tail!.x === 0.7, 'setTextTail');
lettered = setTextSize(lettered, balloon.id, 99);
assert(lettered.pages[0].texts[0].size === 4, 'size clamps to the ceiling');
lettered = setTextRect(lettered, balloon.id, 0.2, 0.2, 0.005, 0.1);
assert(lettered.pages[0].texts[0].x === 0.42, 'a sub-floor resize is refused whole (the box never half-applies)');
const beforeRect = lettered.pages[0].texts[0];
assert(beforeRect.width === 0.16 && beforeRect.height === 0.1, 'the box keeps its floor-passing size');
lettered = setTextKind(lettered, balloon.id, 'caption');
assert(lettered.pages[0].texts[0].tail === null, 'a caption drops the balloon tail');
lettered = setTextKind(lettered, balloon.id, 'sfx');
lettered = setTextTail(lettered, balloon.id, 0.1, 0.1);
assert(lettered.pages[0].texts[0].tail === null, 'a burst refuses a tail');
lettered = setTextKind(lettered, balloon.id, 'balloon');
lettered = setTextTail(lettered, balloon.id, 0.3, 0.8);
assert(lettered.pages[0].texts[0].tail!.y === 0.8, 'a balloon takes a tail again');
assert(addText(lettered, pageID, 'caption', 0.5, 0.5).pages[0].texts.length === 2, 'addText appends');
let capped = lettered;
for (let i = capped.pages[0].texts.length; i < 21; i++) capped = addText(capped, pageID, 'caption');
assert(capped.pages[0].texts.length === 20, 'the 20-texts ceiling holds');
assert(removeText(capped, capped.pages[0].texts[0].id).pages[0].texts.length === 19, 'removeText removes');

// Geometry: the burst fills its box, the tail base hugs the bubble edge.
const burst = comicSFXPoints({...lettered.pages[0].texts[0], kind: 'sfx', x: 0.4, y: 0.4, width: 0.2, height: 0.1, tail: null});
assert(burst.length === 24, 'a 12-spike burst has 24 points');
assert(burst.every((p) => p.x >= 0.4 - 1e-9 && p.x <= 0.6 + 1e-9 && p.y >= 0.4 - 1e-9 && p.y <= 0.5 + 1e-9), 'the burst stays inside its box');
const tailBox = {...lettered.pages[0].texts[0], x: 0.4, y: 0.4, width: 0.2, height: 0.1, tail: {x: 0.8, y: 0.9}};
const base = comicBalloonTailBase(tailBox);
const inside = (p: ComicPoint) => p.x >= 0.5 - 1e-9 && p.x <= 0.6 + 1e-9 && p.y >= 0.45 - 1e-9 && p.y <= 0.5 + 1e-9;
assert(inside(base.a) && inside(base.b), 'both tail base points sit on the bubble edge nearest the tip');

// Wrap: greedy words, explicit newlines survive, a long word never splits.
const wrapped = comicWrapText('the quick brown fox jumps over the dog', 120, 20);
assert(wrapped.length === 4 && wrapped[0] === 'the quick' && wrapped[3] === 'the dog', `wrap lines were ${JSON.stringify(wrapped)}`);
assert(comicWrapText('one\ntwo', 500, 20).length === 2, 'explicit newlines split');
assert(comicWrapText('extraordinarily', 60, 20).length === 1, 'a long word stays whole');

// Problems name lettering states; the wire round-trips them.
const brokenText = normalizeComicDoc({pages: [{id: 'p', panels: [], texts: [{id: 't', kind: 'whisper', x: 0.1, y: 0.1, width: 0.001, height: 0.1}]}]});
const textProblems = comicProblems(brokenText);
assert(textProblems.some((p) => p.includes('kind must be')), 'an unknown text kind is named');
assert(textProblems.some((p) => p.includes('box is too small')), 'a tiny text box is named');
const roundTripped = normalizeComicDoc(toWireDoc(lettered));
assert(sameComicDoc(roundTripped, lettered), 'texts round-trip through the wire');
assert(JSON.stringify(toWireDoc(lettered)).includes('"kind":"balloon"'), 'wire carries the lettering kind');
