export type CropRect = {x: number; y: number; width: number; height: number};
export type Point = {x: number; y: number};
export type CropHandle = 'n' | 's' | 'e' | 'w' | 'ne' | 'nw' | 'se' | 'sw';

const minSize = 1;

function finite(value: number, fallback = 0): number {
  return Number.isFinite(value) ? value : fallback;
}

function clamp(value: number, min: number, max: number): number {
  if (max < min) return min;
  return Math.min(max, Math.max(min, value));
}

function imageBounds(width: number, height: number) {
  return {width: Math.max(0, finite(width)), height: Math.max(0, finite(height))};
}

function clampRectMin(rect: CropRect, imageWidth: number, imageHeight: number, minWidth: number, minHeight: number): CropRect {
  const bounds = imageBounds(imageWidth, imageHeight);
  if (bounds.width <= 0 || bounds.height <= 0) return {x: 0, y: 0, width: 0, height: 0};
  const width = clamp(finite(rect.width, minWidth), Math.min(minWidth, bounds.width), bounds.width);
  const height = clamp(finite(rect.height, minHeight), Math.min(minHeight, bounds.height), bounds.height);
  return {
    x: clamp(finite(rect.x), 0, bounds.width - width),
    y: clamp(finite(rect.y), 0, bounds.height - height),
    width,
    height,
  };
}

function clampRect(rect: CropRect, imageWidth: number, imageHeight: number): CropRect {
  return clampRectMin(rect, imageWidth, imageHeight, minSize, minSize);
}

function clampDraftRect(rect: CropRect, imageWidth: number, imageHeight: number): CropRect {
  return clampRectMin(rect, imageWidth, imageHeight, 0.000001, 0.000001);
}

export function fitCrop(width: number, height: number, ratio: number | null, center?: Point): CropRect {
  const bounds = imageBounds(width, height);
  if (bounds.width <= 0 || bounds.height <= 0) return {x: 0, y: 0, width: 0, height: 0};
  if (!ratio || ratio <= 0 || !Number.isFinite(ratio)) {
    return {x: 0, y: 0, width: bounds.width, height: bounds.height};
  }
  let cropWidth = bounds.width;
  let cropHeight = cropWidth / ratio;
  if (cropHeight > bounds.height) {
    cropHeight = bounds.height;
    cropWidth = cropHeight * ratio;
  }
  const cx = center ? clamp(finite(center.x, bounds.width / 2), 0, bounds.width) : bounds.width / 2;
  const cy = center ? clamp(finite(center.y, bounds.height / 2), 0, bounds.height) : bounds.height / 2;
  return clampDraftRect({x: cx - cropWidth / 2, y: cy - cropHeight / 2, width: cropWidth, height: cropHeight}, bounds.width, bounds.height);
}

export function moveCrop(rect: CropRect, dx: number, dy: number, width: number, height: number): CropRect {
  const current = clampDraftRect(rect, width, height);
  return clampDraftRect({...current, x: current.x + finite(dx), y: current.y + finite(dy)}, width, height);
}

function anchorFor(rect: CropRect, handle: CropHandle): Point {
  switch (handle) {
    case 'nw': return {x: rect.x + rect.width, y: rect.y + rect.height};
    case 'n': return {x: rect.x + rect.width / 2, y: rect.y + rect.height};
    case 'ne': return {x: rect.x, y: rect.y + rect.height};
    case 'e': return {x: rect.x, y: rect.y + rect.height / 2};
    case 'se': return {x: rect.x, y: rect.y};
    case 's': return {x: rect.x + rect.width / 2, y: rect.y};
    case 'sw': return {x: rect.x + rect.width, y: rect.y};
    case 'w': return {x: rect.x + rect.width, y: rect.y + rect.height / 2};
  }
}

function signedAxes(handle: CropHandle): {x: -1 | 0 | 1; y: -1 | 0 | 1} {
  return {
    x: handle.includes('w') ? -1 : handle.includes('e') ? 1 : 0,
    y: handle.includes('n') ? -1 : handle.includes('s') ? 1 : 0,
  };
}

function fixedResize(rect: CropRect, handle: CropHandle, dx: number, dy: number, imageWidth: number, imageHeight: number, ratio: number): CropRect {
  const anchor = anchorFor(rect, handle);
  const axes = signedAxes(handle);
  const pointer = {
    x: (axes.x < 0 ? rect.x : axes.x > 0 ? rect.x + rect.width : rect.x + rect.width / 2) + finite(dx),
    y: (axes.y < 0 ? rect.y : axes.y > 0 ? rect.y + rect.height : rect.y + rect.height / 2) + finite(dy),
  };

  let nextWidth = rect.width;
  let nextHeight = rect.height;
  if (axes.x && axes.y) {
    const rawWidth = Math.max(minSize, axes.x * (pointer.x - anchor.x));
    const rawHeight = Math.max(minSize, axes.y * (pointer.y - anchor.y));
    if (Math.abs(finite(dx)) / Math.max(rect.width, minSize) >= Math.abs(finite(dy)) / Math.max(rect.height, minSize)) {
      nextWidth = rawWidth;
      nextHeight = nextWidth / ratio;
    } else {
      nextHeight = rawHeight;
      nextWidth = nextHeight * ratio;
    }
    const maxWidth = axes.x > 0 ? imageWidth - anchor.x : anchor.x;
    const maxHeight = axes.y > 0 ? imageHeight - anchor.y : anchor.y;
    nextWidth = Math.min(nextWidth, maxWidth, maxHeight * ratio);
    nextHeight = nextWidth / ratio;
    return clampDraftRect({
      x: axes.x > 0 ? anchor.x : anchor.x - nextWidth,
      y: axes.y > 0 ? anchor.y : anchor.y - nextHeight,
      width: nextWidth,
      height: nextHeight,
    }, imageWidth, imageHeight);
  }

  if (axes.x) {
    nextWidth = Math.max(minSize, axes.x * (pointer.x - anchor.x));
    const maxWidthByX = axes.x > 0 ? imageWidth - anchor.x : anchor.x;
    const maxHeightCentered = 2 * Math.min(anchor.y, imageHeight - anchor.y);
    nextWidth = Math.min(nextWidth, maxWidthByX, maxHeightCentered * ratio);
    nextHeight = nextWidth / ratio;
    return clampDraftRect({
      x: axes.x > 0 ? anchor.x : anchor.x - nextWidth,
      y: anchor.y - nextHeight / 2,
      width: nextWidth,
      height: nextHeight,
    }, imageWidth, imageHeight);
  }

  nextHeight = Math.max(minSize, axes.y * (pointer.y - anchor.y));
  const maxHeightByY = axes.y > 0 ? imageHeight - anchor.y : anchor.y;
  const maxWidthCentered = 2 * Math.min(anchor.x, imageWidth - anchor.x);
  nextHeight = Math.min(nextHeight, maxHeightByY, maxWidthCentered / ratio);
  nextWidth = nextHeight * ratio;
  return clampDraftRect({
    x: anchor.x - nextWidth / 2,
    y: axes.y > 0 ? anchor.y : anchor.y - nextHeight,
    width: nextWidth,
    height: nextHeight,
  }, imageWidth, imageHeight);
}

export function resizeCrop(rect: CropRect, handle: CropHandle, dx: number, dy: number, width: number, height: number, ratio: number | null): CropRect {
  if (!ratio || ratio <= 0 || !Number.isFinite(ratio)) {
    const current = clampRect(rect, width, height);
    let left = current.x;
    let top = current.y;
    let right = current.x + current.width;
    let bottom = current.y + current.height;
    if (handle.includes('w')) left = clamp(left + finite(dx), 0, right - minSize);
    if (handle.includes('e')) right = clamp(right + finite(dx), left + minSize, width);
    if (handle.includes('n')) top = clamp(top + finite(dy), 0, bottom - minSize);
    if (handle.includes('s')) bottom = clamp(bottom + finite(dy), top + minSize, height);
    return clampRect({x: left, y: top, width: right - left, height: bottom - top}, width, height);
  }
  const current = clampDraftRect(rect, width, height);
  return fixedResize(current, handle, dx, dy, width, height, ratio);
}

export function drawCrop(start: Point, end: Point, width: number, height: number, ratio: number | null): CropRect {
  const bounds = imageBounds(width, height);
  const a = {x: clamp(finite(start.x), 0, bounds.width), y: clamp(finite(start.y), 0, bounds.height)};
  const b = {x: clamp(finite(end.x), 0, bounds.width), y: clamp(finite(end.y), 0, bounds.height)};
  if (!ratio || ratio <= 0 || !Number.isFinite(ratio)) {
    return clampRect({
      x: Math.min(a.x, b.x),
      y: Math.min(a.y, b.y),
      width: Math.abs(b.x - a.x),
      height: Math.abs(b.y - a.y),
    }, width, height);
  }
  const signX = b.x < a.x ? -1 : 1;
  const signY = b.y < a.y ? -1 : 1;
  const rawWidth = Math.max(minSize, Math.abs(b.x - a.x));
  const rawHeight = Math.max(minSize, Math.abs(b.y - a.y));
  let cropWidth = rawWidth / rawHeight >= ratio ? rawWidth : rawHeight * ratio;
  let cropHeight = cropWidth / ratio;
  const maxWidth = signX > 0 ? bounds.width - a.x : a.x;
  const maxHeight = signY > 0 ? bounds.height - a.y : a.y;
  cropWidth = Math.min(cropWidth, maxWidth, maxHeight * ratio);
  cropHeight = cropWidth / ratio;
  return clampDraftRect({
    x: signX > 0 ? a.x : a.x - cropWidth,
    y: signY > 0 ? a.y : a.y - cropHeight,
    width: cropWidth,
    height: cropHeight,
  }, width, height);
}

export function pixelCrop(rect: CropRect, width: number, height: number, ratio: number | null): CropRect {
  const current = ratio && ratio > 0 && Number.isFinite(ratio) ? clampDraftRect(rect, width, height) : clampRect(rect, width, height);
  if (current.width <= 0 || current.height <= 0) return {x: 0, y: 0, width: 0, height: 0};
  if (!ratio || ratio <= 0 || !Number.isFinite(ratio)) {
    const x = Math.round(current.x), y = Math.round(current.y);
    return {x, y, width: Math.round(current.x + current.width) - x, height: Math.round(current.y + current.height) - y};
  }
  let x = Math.round(current.x);
  let y = Math.round(current.y);
  let cropWidth = Math.max(1, Math.round(current.width));
  let cropHeight = Math.max(1, Math.round(current.height));
  if (ratio && ratio > 0 && Number.isFinite(ratio)) {
    const widthFromHeight = Math.max(1, Math.round(cropHeight * ratio));
    const heightFromWidth = Math.max(1, Math.round(cropWidth / ratio));
    if (Math.abs(widthFromHeight - current.width) <= Math.abs(heightFromWidth - current.height)) {
      cropWidth = widthFromHeight;
    } else {
      cropHeight = heightFromWidth;
    }
  }
  cropWidth = Math.min(cropWidth, Math.max(1, Math.floor(width)));
  cropHeight = Math.min(cropHeight, Math.max(1, Math.floor(height)));
  x = clamp(x, 0, Math.max(0, Math.floor(width) - cropWidth));
  y = clamp(y, 0, Math.max(0, Math.floor(height) - cropHeight));
  return {x, y, width: cropWidth, height: cropHeight};
}
