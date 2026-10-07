// Canvas-stage coordinate mapping — the editor's one coordinate system.
// Everything the user draws (mask strokes now, a crop rect later) is stored in
// IMAGE pixel space; the stage's zoom/pan/fit lives in a ViewTransform that
// maps between display pixels and image pixels. Display pixel density never
// enters the math: the canvases are sized in image pixels and scaled with CSS
// transforms, so a Retina display cannot skew the mask alignment.

export type ViewTransform = {
  // scale is display pixels per image pixel.
  scale: number;
  // offset is the image-space origin's position in display pixels, relative
  // to the stage's content box.
  offsetX: number;
  offsetY: number;
};

// fitTransform fits the image inside a display box with breathing room,
// centering it.
export function fitTransform(imgW: number, imgH: number, boxW: number, boxH: number, padding = 24): ViewTransform {
  if (imgW <= 0 || imgH <= 0 || boxW <= 0 || boxH <= 0) {
    return {scale: 1, offsetX: 0, offsetY: 0};
  }
  const scale = Math.min((boxW - padding * 2) / imgW, (boxH - padding * 2) / imgH);
  const clamped = Math.max(scale, 0.01);
  return {
    scale: clamped,
    offsetX: (boxW - imgW * clamped) / 2,
    offsetY: (boxH - imgH * clamped) / 2,
  };
}

export function imageToDisplay(t: ViewTransform, x: number, y: number): {x: number; y: number} {
  return {x: t.offsetX + x * t.scale, y: t.offsetY + y * t.scale};
}

export function displayToImage(t: ViewTransform, x: number, y: number): {x: number; y: number} {
  return {x: (x - t.offsetX) / t.scale, y: (y - t.offsetY) / t.scale};
}

// clampScale keeps the zoom inside a sane envelope: never below one image
// pixel per ten display pixels, never above 16x.
export function clampScale(scale: number): number {
  return Math.min(16, Math.max(0.1, scale));
}

// zoomAround keeps the image-space point under the cursor stationary while
// the scale changes — the standard wheel-zoom anchor.
export function zoomAround(t: ViewTransform, nextScale: number, displayX: number, displayY: number): ViewTransform {
  const scale = clampScale(nextScale);
  const image = displayToImage(t, displayX, displayY);
  return {
    scale,
    offsetX: displayX - image.x * scale,
    offsetY: displayY - image.y * scale,
  };
}
