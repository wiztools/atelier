import {useCallback, useEffect, useLayoutEffect, useRef, useState} from 'react';
import {fitTransform, ViewTransform, zoomAround} from './coordinates';
import {MaskPoint, MaskStroke, drawOverlay} from './MaskBrushTool';

// CanvasStage hosts the image being edited: the zoom/pan/fit surface, the
// overlay canvas that renders the selection, and the pointer routing that
// feeds strokes (image-space points) to the parent. The canonical mask raster
// lives with the parent (the tool's state); the stage only renders it.
//
// Zoom: ctrl/⌘-wheel and trackpad pinch zoom around the cursor; plain
// scrolling pans; the corner buttons zoom/fit for mouse-only setups.
// Double-click refits. Painting is the left button; Option-drag subtracts
// (the parent reads the modifier from the stroke events).

type StageProps = {
  imageUrl: string;
  imageWidth: number;
  imageHeight: number;
  maskCanvas: HTMLCanvasElement | null;
  maskVersion: number;
  liveStroke: MaskStroke | null;
  cursor: MaskPoint | null;
  // brushRadius is the brush half-size in DISPLAY pixels; the stage converts
  // it to image pixels (dividing by the zoom) for strokes and the cursor ring
  // so both stay a constant size on screen at any zoom level.
  brushRadius: number;
  onStroke: (kind: 'start' | 'extend' | 'end', point: MaskPoint, subtract: boolean, imageRadius: number) => void;
  onCursor: (point: MaskPoint | null) => void;
};

export function CanvasStage(props: StageProps) {
  const containerRef = useRef<HTMLDivElement | null>(null);
  const overlayRef = useRef<HTMLCanvasElement | null>(null);
  const [view, setView] = useState<ViewTransform>({scale: 1, offsetX: 0, offsetY: 0});
  const [box, setBox] = useState({width: 0, height: 0});
  const panState = useRef<{x: number; y: number; offsetX: number; offsetY: number} | null>(null);
  const painting = useRef(false);

  // Fit whenever the image or the box changes.
  useLayoutEffect(() => {
    if (!props.imageWidth || !props.imageHeight || !box.width || !box.height) {
      return;
    }
    setView(fitTransform(props.imageWidth, props.imageHeight, box.width, box.height));
  }, [props.imageWidth, props.imageHeight, box.width, box.height]);

  useEffect(() => {
    const element = containerRef.current;
    if (!element) {
      return;
    }
    const observer = new ResizeObserver(() => {
      setBox({width: element.clientWidth, height: element.clientHeight});
    });
    observer.observe(element);
    setBox({width: element.clientWidth, height: element.clientHeight});
    return () => observer.disconnect();
  }, []);

  // Overlay rendering: committed mask tint + live stroke + cursor. The cursor
  // ring is sized in image pixels (display size ÷ zoom) so it reads as a
  // constant on-screen radius.
  useEffect(() => {
    const canvas = overlayRef.current;
    if (!canvas || !props.maskCanvas) {
      return;
    }
    if (canvas.width !== props.imageWidth || canvas.height !== props.imageHeight) {
      canvas.width = props.imageWidth;
      canvas.height = props.imageHeight;
    }
    drawOverlay(canvas, props.maskCanvas, props.liveStroke, props.cursor, props.brushRadius / view.scale);
  }, [props.maskCanvas, props.maskVersion, props.liveStroke, props.cursor, props.brushRadius, props.imageWidth, props.imageHeight, view.scale]);

  // Pointer → image pixels. The bounding rect is measured AFTER the stage's
  // translate+scale transform, so normalizing the pointer by the rect's size
  // yields the image-space coordinate directly — no view transform on top
  // (applying one would double-transform and paint away from the cursor).
  const pointerToImage = useCallback(
    (event: React.PointerEvent<HTMLCanvasElement>): MaskPoint => {
      const canvas = overlayRef.current;
      if (!canvas || !canvas.width || !canvas.height) {
        return {x: 0, y: 0};
      }
      const rect = canvas.getBoundingClientRect();
      if (rect.width <= 0 || rect.height <= 0) {
        return {x: 0, y: 0};
      }
      return {
        x: ((event.clientX - rect.left) / rect.width) * canvas.width,
        y: ((event.clientY - rect.top) / rect.height) * canvas.height,
      };
    },
    [],
  );

  const onWheel = (event: React.WheelEvent) => {
    event.preventDefault();
    if (event.ctrlKey || event.metaKey) {
      const rect = containerRef.current?.getBoundingClientRect();
      if (!rect) {
        return;
      }
      const next = zoomAround(view, view.scale * (1 - event.deltaY / 300), event.clientX - rect.left, event.clientY - rect.top);
      setView(next);
      return;
    }
    setView((current) => ({...current, offsetX: current.offsetX - event.deltaX, offsetY: current.offsetY - event.deltaY}));
  };

  const onPointerDown = (event: React.PointerEvent<HTMLCanvasElement>) => {
    const point = pointerToImage(event);
    if (event.button === 1 || event.button === 2 || (event.button === 0 && event.shiftKey)) {
      panState.current = {x: event.clientX, y: event.clientY, offsetX: view.offsetX, offsetY: view.offsetY};
      (event.target as HTMLCanvasElement).setPointerCapture(event.pointerId);
      event.preventDefault();
      return;
    }
    if (event.button !== 0) {
      return;
    }
    painting.current = true;
    (event.target as HTMLCanvasElement).setPointerCapture(event.pointerId);
    props.onStroke('start', point, event.altKey, props.brushRadius / view.scale);
    event.preventDefault();
  };

  const onPointerMove = (event: React.PointerEvent<HTMLCanvasElement>) => {
    const point = pointerToImage(event);
    if (panState.current) {
      const rect = containerRef.current?.getBoundingClientRect();
      if (rect) {
        setView((current) => ({
          ...current,
          offsetX: panState.current!.offsetX + (event.clientX - panState.current!.x),
          offsetY: panState.current!.offsetY + (event.clientY - panState.current!.y),
        }));
      }
      return;
    }
    props.onCursor(point);
    if (painting.current) {
      props.onStroke('extend', point, event.altKey, props.brushRadius / view.scale);
    }
  };

  const onPointerUp = (event: React.PointerEvent<HTMLCanvasElement>) => {
    if (panState.current) {
      panState.current = null;
      return;
    }
    if (painting.current) {
      painting.current = false;
      props.onStroke('end', pointerToImage(event), event.altKey, props.brushRadius / view.scale);
    }
  };

  const zoomBy = (factor: number) => {
    const rect = containerRef.current?.getBoundingClientRect();
    if (!rect) {
      return;
    }
    setView((current) => zoomAround(current, current.scale * factor, rect.width / 2, rect.height / 2));
  };

  const fit = () => {
    if (props.imageWidth && props.imageHeight && box.width && box.height) {
      setView(fitTransform(props.imageWidth, props.imageHeight, box.width, box.height));
    }
  };

  return (
    <div
      className="editor-stage"
      ref={containerRef}
      onWheel={onWheel}
      onContextMenu={(event) => event.preventDefault()}
    >
      <div
        className="editor-stage-content"
        style={{
          transform: `translate(${view.offsetX}px, ${view.offsetY}px) scale(${view.scale})`,
          width: props.imageWidth,
          height: props.imageHeight,
        }}
      >
        <img
          className="editor-stage-image"
          src={props.imageUrl}
          alt=""
          draggable={false}
        />
        <canvas
          className="editor-stage-overlay"
          ref={overlayRef}
          onPointerDown={onPointerDown}
          onPointerMove={onPointerMove}
          onPointerUp={onPointerUp}
          onPointerLeave={() => props.onCursor(null)}
          style={{cursor: 'crosshair'}}
        />
      </div>
      <div className="editor-stage-controls">
        <button type="button" onClick={() => zoomBy(1.25)} aria-label="Zoom in" title="Zoom in">+</button>
        <button type="button" onClick={() => zoomBy(0.8)} aria-label="Zoom out" title="Zoom out">−</button>
        <button type="button" onClick={fit} aria-label="Fit to window" title="Fit to window">⤢</button>
        <span className="editor-stage-zoom">{Math.round(view.scale * 100)}%</span>
      </div>
    </div>
  );
}
