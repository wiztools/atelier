import {CropHandle, CropRect, drawCrop, fitCrop, moveCrop, pixelCrop, resizeCrop} from '../src/editor/cropGeometry';

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message);
}

function near(a: number, b: number, tolerance = 1e-6): boolean {
  return Math.abs(a - b) <= tolerance;
}

function inside(rect: CropRect, width = 400, height = 300): boolean {
  return rect.x >= -1e-6 && rect.y >= -1e-6 && rect.width >= 1 && rect.height >= 1 &&
    rect.x + rect.width <= width + 1e-6 && rect.y + rect.height <= height + 1e-6;
}

function ratioOk(rect: CropRect, ratio: number): boolean {
  return near(rect.width / rect.height, ratio, 1e-6);
}

const fitted = fitCrop(400, 300, 16 / 9);
assert(inside(fitted), 'fitCrop keeps the crop inside');
assert(ratioOk(fitted, 16 / 9), 'fitCrop preserves requested ratio');

const moved = moveCrop({x: 300, y: 250, width: 120, height: 80}, 80, 80, 400, 300);
assert(moved.x === 280 && moved.y === 220, 'moveCrop clamps without resizing');
assert(moved.width === 120 && moved.height === 80, 'moveCrop preserves size');

const free = resizeCrop({x: 50, y: 40, width: 100, height: 80}, 'nw', -100, -50, 400, 300, null);
assert(free.x === 0 && free.y === 0 && free.width === 150 && free.height === 120, 'free corner resize expands independently');

for (const handle of ['n', 's', 'e', 'w', 'ne', 'nw', 'se', 'sw'] as CropHandle[]) {
  const next = resizeCrop({x: 100, y: 80, width: 160, height: 90}, handle, handle.includes('w') ? -80 : 80, handle.includes('n') ? -80 : 80, 400, 300, 16 / 9);
  assert(inside(next), `locked ${handle} resize stays inside`);
  assert(ratioOk(next, 16 / 9), `locked ${handle} resize keeps ratio`);
}

const edge = resizeCrop({x: 20, y: 20, width: 160, height: 90}, 'nw', -500, -500, 400, 300, 16 / 9);
assert(inside(edge), 'boundary locked resize stays inside');
assert(ratioOk(edge, 16 / 9), 'boundary locked resize keeps ratio');

const drawn = drawCrop({x: 300, y: 200}, {x: 120, y: 90}, 400, 300, 4 / 3);
assert(inside(drawn), 'reverse draw stays inside');
assert(ratioOk(drawn, 4 / 3), 'reverse draw keeps fixed ratio');
assert(near(drawn.x + drawn.width, 300) && near(drawn.y + drawn.height, 200), 'reverse draw anchors at the starting point');

const tiny = fitCrop(1, 1, 16 / 9);
assert(tiny.width <= 1 && tiny.height <= 1 && tiny.width > 0 && tiny.height > 0, 'tiny fixed fit stays inside as draft geometry');
assert(ratioOk(tiny, 16 / 9), 'tiny fixed fit keeps the requested draft ratio');
const movedTiny = moveCrop(tiny, 1, 1, 1, 1);
assert(ratioOk(movedTiny, 16 / 9), 'moving a tiny fixed draft keeps its ratio');
const tinyPixel = pixelCrop(tiny, 1, 1, 16 / 9);
assert(tinyPixel.width === 1 && tinyPixel.height === 1, 'tiny pixel crop remains at least one pixel');

let seed = 1967;
const random = () => {seed = (Math.imul(seed, 1664525) + 1013904223) >>> 0; return seed / 4294967296;};
for (let i = 0; i < 500; i++) {
  const rect = {
    x: random() * 390,
    y: random() * 290,
    width: 1 + random() * 220,
    height: 1 + random() * 180,
  };
  const handle = (['n', 's', 'e', 'w', 'ne', 'nw', 'se', 'sw'] as CropHandle[])[i % 8];
  const next = resizeCrop(rect, handle, random() * 500 - 250, random() * 500 - 250, 400, 300, 1);
  assert(inside(next), 'random resize stays inside');
  assert(ratioOk(next, 1), 'random locked resize keeps square ratio');
  const pixel = pixelCrop(next, 400, 300, 1);
  assert(pixel.x >= 0 && pixel.y >= 0 && pixel.width >= 1 && pixel.height >= 1, 'pixel crop is positive');
  assert(pixel.x + pixel.width <= 400 && pixel.y + pixel.height <= 300, 'pixel crop stays inside');
  assert(Math.abs(pixel.width - pixel.height) <= 1, 'pixel crop respects one-pixel ratio bound');
}

const edgePixels = pixelCrop({x: .6, y: .6, width: 1.6, height: 1.6}, 10, 10, null);
assert(edgePixels.x === 1 && edgePixels.y === 1 && edgePixels.width === 1 && edgePixels.height === 1, 'free crop rounds its edges consistently');
for (const ratio of [1, 4 / 3, 3 / 2, 16 / 9, 4 / 5, 9 / 16]) {
  for (const [width, height] of [[1, 1], [7, 3], [1000, 700]]) {
    const fitted = fitCrop(width, height, ratio);
    for (const handle of ['n', 's', 'e', 'w', 'ne', 'nw', 'se', 'sw'] as CropHandle[]) {
      const resized = resizeCrop(fitted, handle, -10000, 10000, width, height, ratio);
      assert(ratioOk(resized, ratio), `ratio ${ratio} survives boundary ${handle} on ${width}x${height}`);
      const moved = moveCrop(resized, 10000, -10000, width, height);
      assert(ratioOk(moved, ratio), 'moving a constrained draft does not change its ratio');
      assert(moved.x >= 0 && moved.y >= 0 && moved.x + moved.width <= width + 1e-6 && moved.y + moved.height <= height + 1e-6, 'constrained move stays in bounds');
      const pixel = pixelCrop(moved, width, height, ratio);
      assert(Math.abs(pixel.width - pixel.height * ratio) <= 1.01 || Math.abs(pixel.height - pixel.width / ratio) <= 1.01, 'saved ratio meets one-pixel bound');
    }
  }
  for (const end of [{x: 100, y: 100}, {x: 300, y: 100}, {x: 100, y: 250}, {x: 300, y: 250}]) {
    const drawn = drawCrop({x: 200, y: 150}, end, 400, 300, ratio);
    assert(ratioOk(drawn, ratio), 'new crop retains ratio in all four draw directions');
    assert(near(end.x < 200 ? drawn.x + drawn.width : drawn.x, 200), 'draw keeps the x anchor');
    assert(near(end.y < 150 ? drawn.y + drawn.height : drawn.y, 150), 'draw keeps the y anchor');
  }
}
console.log('cropGeometry assertions passed');
