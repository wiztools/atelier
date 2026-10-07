// The mask brush tool: the stroke model, the canonical mask raster, and the
// overlay rendering. Strokes are recorded in image pixel space and replayed
// onto an offscreen canvas at the image's exact dimensions. The canonical
// mask canvas carries the selection in its ALPHA channel — transparent where
// the image is preserved, opaque white where the AI may edit — so the tinted
// overlay, the paint visibility, and undo/redo all read one raster. The
// submission export converts that to the black-and-white PNG the backend
// validates (white = editable, black = preserved). Undo/redo operates on
// strokes, never on pixels.

export type MaskPoint = {x: number; y: number};

export type MaskStroke = {
  points: MaskPoint[];
  radius: number;
  mode: 'add' | 'subtract';
};

// paintStroke applies one stroke onto the mask canvas: opaque white
// round-capped segments for add, destination-out for subtract.
function paintStroke(ctx: CanvasRenderingContext2D, stroke: MaskStroke) {
  if (stroke.points.length === 0) {
    return;
  }
  ctx.save();
  ctx.globalCompositeOperation = stroke.mode === 'subtract' ? 'destination-out' : 'source-over';
  ctx.strokeStyle = '#ffffff';
  ctx.fillStyle = '#ffffff';
  ctx.lineWidth = stroke.radius * 2;
  ctx.lineJoin = 'round';
  ctx.lineCap = 'round';
  const first = stroke.points[0];
  if (stroke.points.length === 1) {
    ctx.beginPath();
    ctx.arc(first.x, first.y, stroke.radius, 0, Math.PI * 2);
    ctx.fill();
  } else {
    ctx.beginPath();
    ctx.moveTo(first.x, first.y);
    for (const point of stroke.points.slice(1)) {
      ctx.lineTo(point.x, point.y);
    }
    ctx.stroke();
  }
  ctx.restore();
}

// repaintMask rebuilds the whole mask raster from the stroke list (the
// undo/redo path; live drawing paints incrementally instead). Unselected
// areas stay fully transparent — the alpha channel IS the selection.
export function repaintMask(canvas: HTMLCanvasElement, strokes: MaskStroke[]) {
  const ctx = canvas.getContext('2d');
  if (!ctx) {
    return;
  }
  ctx.globalCompositeOperation = 'source-over';
  ctx.clearRect(0, 0, canvas.width, canvas.height);
  for (const stroke of strokes) {
    paintStroke(ctx, stroke);
  }
}

// exportMaskPNG converts the canonical alpha-selection raster into the
// black-and-white PNG the backend validates: opaque white where selected
// (anti-aliased edges keep their partial strength), black where preserved.
export function exportMaskPNG(mask: HTMLCanvasElement): string {
  const out = document.createElement('canvas');
  out.width = mask.width;
  out.height = mask.height;
  const ctx = out.getContext('2d');
  if (!ctx) {
    return mask.toDataURL('image/png');
  }
  ctx.fillStyle = '#000000';
  ctx.fillRect(0, 0, out.width, out.height);
  ctx.drawImage(mask, 0, 0);
  return out.toDataURL('image/png');
}

// drawOverlay renders the visible overlay: the committed selection tinted
// magenta (keyed on the mask's alpha, so unselected areas show the image
// untouched), the in-progress stroke live, and the brush cursor ring. The
// overlay canvas shares the mask canvas's dimensions (image pixel space);
// cursorRadius is already in image pixels (the stage converts from display
// pixels so the ring stays a constant size on screen at any zoom).
export function drawOverlay(
  canvas: HTMLCanvasElement,
  mask: HTMLCanvasElement,
  liveStroke: MaskStroke | null,
  cursor: MaskPoint | null,
  cursorRadius: number,
) {
  const ctx = canvas.getContext('2d');
  if (!ctx) {
    return;
  }
  ctx.clearRect(0, 0, canvas.width, canvas.height);

  // Committed selection: magenta everywhere, then keep it only where the
  // mask has alpha — with the alpha-as-selection raster this tints exactly
  // the painted region.
  ctx.save();
  ctx.globalCompositeOperation = 'source-over';
  ctx.fillStyle = 'rgba(255, 45, 140, 0.45)';
  ctx.fillRect(0, 0, canvas.width, canvas.height);
  ctx.globalCompositeOperation = 'destination-in';
  ctx.drawImage(mask, 0, 0);
  ctx.restore();

  if (liveStroke) {
    ctx.save();
    ctx.globalAlpha = 0.45;
    ctx.globalCompositeOperation = 'source-over';
    ctx.strokeStyle = '#ff2d8c';
    ctx.fillStyle = '#ff2d8c';
    ctx.lineWidth = liveStroke.radius * 2;
    ctx.lineJoin = 'round';
    ctx.lineCap = 'round';
    const first = liveStroke.points[0];
    if (liveStroke.points.length === 1) {
      ctx.beginPath();
      ctx.arc(first.x, first.y, liveStroke.radius, 0, Math.PI * 2);
      ctx.fill();
    } else {
      ctx.beginPath();
      ctx.moveTo(first.x, first.y);
      for (const point of liveStroke.points.slice(1)) {
        ctx.lineTo(point.x, point.y);
      }
      ctx.stroke();
    }
    ctx.restore();
  }

  if (cursor) {
    ctx.save();
    ctx.strokeStyle = 'rgba(255, 255, 255, 0.9)';
    ctx.lineWidth = 1.5;
    ctx.beginPath();
    ctx.arc(cursor.x, cursor.y, cursorRadius, 0, Math.PI * 2);
    ctx.stroke();
    ctx.strokeStyle = 'rgba(0, 0, 0, 0.55)';
    ctx.lineWidth = 1;
    ctx.beginPath();
    ctx.arc(cursor.x, cursor.y, cursorRadius + 1.5, 0, Math.PI * 2);
    ctx.stroke();
    ctx.restore();
  }
}

// maskHasSelection samples the mask raster downscaled — enough to catch an
// all-subtract history at Generate time (the backend re-validates the real
// PNG; this only gates the button and spares a round trip).
export function maskHasSelection(mask: HTMLCanvasElement): boolean {
  const ctx = mask.getContext('2d');
  if (!ctx) {
    return false;
  }
  const step = Math.max(1, Math.floor(Math.max(mask.width, mask.height) / 256));
  const {data} = ctx.getImageData(0, 0, mask.width, mask.height);
  for (let y = 0; y < mask.height; y += step) {
    for (let x = 0; x < mask.width; x += step) {
      const idx = (y * mask.width + x) * 4;
      // Alpha IS the selection channel on the canonical raster.
      if (data[idx + 3] > 0) {
        return true;
      }
    }
  }
  return false;
}
