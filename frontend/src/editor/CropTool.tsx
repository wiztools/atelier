import {useRef} from 'react';
import {CropHandle, CropRect, Point, drawCrop, moveCrop, resizeCrop} from './cropGeometry';

const handles: {id: CropHandle; label: string; x: number; y: number; cursor: string}[] = [
  {id: 'nw', label: 'top left', x: 0, y: 0, cursor: 'nwse-resize'},
  {id: 'n', label: 'top', x: .5, y: 0, cursor: 'ns-resize'},
  {id: 'ne', label: 'top right', x: 1, y: 0, cursor: 'nesw-resize'},
  {id: 'e', label: 'right', x: 1, y: .5, cursor: 'ew-resize'},
  {id: 'se', label: 'bottom right', x: 1, y: 1, cursor: 'nwse-resize'},
  {id: 's', label: 'bottom', x: .5, y: 1, cursor: 'ns-resize'},
  {id: 'sw', label: 'bottom left', x: 0, y: 1, cursor: 'nesw-resize'},
  {id: 'w', label: 'left', x: 0, y: .5, cursor: 'ew-resize'},
];

export function CropTool(props: {
  rect: CropRect; width: number; height: number; ratio: number | null; scale: number; disabled: boolean;
  onChange: (rect: CropRect) => void;
  onCommit: (before: CropRect) => void;
  onGesture: (active: boolean) => void;
}) {
  const root = useRef<HTMLDivElement>(null);
  const drag = useRef<{start: Point; rect: CropRect; action: CropHandle | 'move' | 'draw'; last: CropRect} | null>(null);
  const point = (event: React.PointerEvent): Point => {
    const bounds = root.current!.getBoundingClientRect();
    return {x: (event.clientX - bounds.left) * props.width / bounds.width, y: (event.clientY - bounds.top) * props.height / bounds.height};
  };
  function begin(event: React.PointerEvent<HTMLDivElement>) {
    if (drag.current || props.disabled || event.button !== 0 || event.shiftKey) return;
    const start = point(event);
    const handle = (event.target as HTMLElement).closest<HTMLElement>('[data-crop-handle]')?.dataset.cropHandle as CropHandle | undefined;
    const r = props.rect;
    const inside = start.x >= r.x && start.x <= r.x + r.width && start.y >= r.y && start.y <= r.y + r.height;
    drag.current = {start, rect: {...r}, last: {...r}, action: handle || (inside ? 'move' : 'draw')};
    event.currentTarget.setPointerCapture(event.pointerId);
    props.onGesture(true);
    event.preventDefault();
  }
  function update(event: React.PointerEvent<HTMLDivElement>) {
    const current = drag.current;
    if (!current) return;
    const end = point(event);
    const dx = end.x - current.start.x, dy = end.y - current.start.y;
    const next = current.action === 'move'
      ? moveCrop(current.rect, dx, dy, props.width, props.height)
      : current.action === 'draw'
        ? drawCrop(current.start, end, props.width, props.height, props.ratio)
        : resizeCrop(current.rect, current.action, dx, dy, props.width, props.height, props.ratio);
    current.last = next;
    props.onChange(next);
  }
  function finish(cancelled: boolean) {
    const current = drag.current;
    if (!current) return;
    drag.current = null;
    if (cancelled) props.onChange(current.rect);
    else if (JSON.stringify(current.rect) !== JSON.stringify(current.last)) props.onCommit(current.rect);
    props.onGesture(false);
  }
  const r = props.rect;
  return <div ref={root} className="editor-crop-overlay" onPointerDown={begin} onPointerMove={update}
    onPointerUp={(event) => {update(event); finish(false);}}
    onPointerCancel={() => finish(true)} onLostPointerCapture={() => finish(true)}>
    <svg width="100%" height="100%" viewBox={`0 0 ${props.width} ${props.height}`} aria-hidden="true">
      <path fill="rgba(0,0,0,.58)" fillRule="evenodd" d={`M0 0H${props.width}V${props.height}H0Z M${r.x} ${r.y}h${r.width}v${r.height}h-${r.width}Z`} />
      <rect x={r.x} y={r.y} width={r.width} height={r.height} fill="transparent" stroke="white" strokeWidth="1.5" vectorEffect="non-scaling-stroke" />
      {[1, 2].map((i) => <g key={i} stroke="rgba(255,255,255,.55)" strokeWidth="1" style={{pointerEvents: 'none'}}>
        <line x1={r.x + r.width * i / 3} y1={r.y} x2={r.x + r.width * i / 3} y2={r.y + r.height} vectorEffect="non-scaling-stroke" />
        <line x1={r.x} y1={r.y + r.height * i / 3} x2={r.x + r.width} y2={r.y + r.height * i / 3} vectorEffect="non-scaling-stroke" />
      </g>)}
    </svg>
    {handles.map((handle) => <button key={handle.id} type="button" data-crop-handle={handle.id}
      className="editor-crop-handle" disabled={props.disabled} aria-label={`Resize crop ${handle.label}`}
      style={{left: r.x + r.width * handle.x, top: r.y + r.height * handle.y, width: 18 / props.scale, height: 18 / props.scale, cursor: handle.cursor}}
      onKeyDown={(event) => {
        if (!event.key.startsWith('Arrow') || props.disabled) return;
        event.preventDefault(); event.stopPropagation();
        const step = event.shiftKey ? 10 : 1;
        const dx = event.key === 'ArrowLeft' ? -step : event.key === 'ArrowRight' ? step : 0;
        const dy = event.key === 'ArrowUp' ? -step : event.key === 'ArrowDown' ? step : 0;
        props.onCommit(r);
        props.onChange(resizeCrop(r, handle.id, dx, dy, props.width, props.height, props.ratio));
      }}><span style={{width: 8 / props.scale, height: 8 / props.scale}} /></button>)}
  </div>;
}
