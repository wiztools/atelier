import {ReactNode, useRef} from 'react';

// The transport row both video timelines share: play/pause, mute, marker/cut
// hops, frame steps, and the time readout. Tool-specific buttons and
// mini-fields render after the readout via children. Stepping hops through
// stepTargets — the reframe markers' times or the trim cuts' times.
export function VideoTimelineControls(props: {
  playing: boolean;
  muted: boolean;
  busy: boolean;
  currentTime: number;
  duration: number;
  stepTargets: number[];
  onTogglePlay: () => void;
  onToggleMuted: () => void;
  onSeek: (timeSeconds: number) => void;
  children?: ReactNode;
}) {
  const markerEpsilon = 0.001;
  const frameStepSeconds = 1 / 30;

  function stepTarget(direction: -1 | 1): number | null {
    if (direction === -1) {
      for (let index = props.stepTargets.length - 1; index >= 0; index--) {
        if (props.stepTargets[index] < props.currentTime - markerEpsilon) return props.stepTargets[index];
      }
      return null;
    }
    for (const target of props.stepTargets) {
      if (target > props.currentTime + markerEpsilon) return target;
    }
    return null;
  }

  function stepTo(direction: -1 | 1) {
    const target = stepTarget(direction);
    if (target === null) return;
    props.onSeek(target);
  }

  function stepFrame(direction: -1 | 1) {
    const next = Math.max(0, Math.min(props.duration, props.currentTime + direction * frameStepSeconds));
    if (next === props.currentTime) return;
    props.onSeek(next);
  }

  return (
    <div className="video-timeline-controls">
      <button
        type="button"
        className="video-icon-button"
        onClick={props.onTogglePlay}
        aria-label={props.playing ? 'Pause video' : 'Play video'}
        title={props.playing ? 'Pause' : 'Play'}
      >
        {props.playing ? '❚❚' : '▶'}
      </button>
      <button
        type="button"
        className="video-icon-button"
        onClick={props.onToggleMuted}
        aria-label={props.muted ? 'Unmute preview' : 'Mute preview'}
        title={props.muted ? 'Unmute' : 'Mute'}
      >
        {props.muted ? '🔇' : '🔊'}
      </button>
      <button
        type="button"
        className="video-icon-button"
        onClick={() => stepTo(-1)}
        disabled={props.busy || stepTarget(-1) === null}
        aria-label="Seek to the previous marker"
        title="Previous marker"
      >
        |◀
      </button>
      <button
        type="button"
        className="video-icon-button"
        onClick={() => stepFrame(-1)}
        disabled={props.busy || props.currentTime <= 0}
        aria-label="Step the playhead back one frame"
        title="Step back one frame"
      >
        ◀◀
      </button>
      <span className="video-timeline-time">{formatTime(props.currentTime)}</span>
      <button
        type="button"
        className="video-icon-button"
        onClick={() => stepFrame(1)}
        disabled={props.busy || props.currentTime >= props.duration}
        aria-label="Step the playhead forward one frame"
        title="Step forward one frame"
      >
        ▶▶
      </button>
      <button
        type="button"
        className="video-icon-button"
        onClick={() => stepTo(1)}
        disabled={props.busy || stepTarget(1) === null}
        aria-label="Seek to the next marker"
        title="Next marker"
      >
        ▶|
      </button>
      {props.children}
    </div>
  );
}

function clamp(value: number, min: number, max: number): number {
  if (max < min) return min;
  return Math.min(max, Math.max(min, value));
}

export function formatTime(value: number): string {
  const seconds = Math.max(0, Number.isFinite(value) ? value : 0);
  const whole = Math.floor(seconds);
  const minutes = Math.floor(whole / 60);
  const rest = whole % 60;
  const fraction = Math.floor((seconds - whole) * 10);
  return `${minutes}:${String(rest).padStart(2, '0')}.${fraction}`;
}

export function pointerTime(event: React.PointerEvent, element: HTMLElement, duration: number): number {
  const bounds = element.getBoundingClientRect();
  const x = clamp(event.clientX - bounds.left, 0, bounds.width);
  return bounds.width > 0 ? x * duration / bounds.width : 0;
}

// useMarkerDrag is the grab-to-retime behavior both timelines give their
// markers: pointer capture while dragging, commit on release, restore on
// cancel. index === null means no drag is in flight.
export function useMarkerDrag(handlers: {
  onMove: (index: number, timeSeconds: number) => void;
  onCommit: (index: number, timeSeconds: number) => void;
  onCancel: () => void;
}) {
  const dragging = useRef<number | null>(null);
  return {
    start(index: number, event: React.PointerEvent<HTMLButtonElement>): void {
      dragging.current = index;
      event.currentTarget.setPointerCapture(event.pointerId);
      event.preventDefault();
    },
    move(event: React.PointerEvent<HTMLButtonElement>, track: HTMLElement | null, duration: number): void {
      if (dragging.current === null) return;
      if (!track) return;
      handlers.onMove(dragging.current, pointerTime(event, track, duration));
      event.preventDefault();
    },
    end(event: React.PointerEvent<HTMLButtonElement>, track: HTMLElement | null, duration: number): void {
      if (dragging.current === null) return;
      if (track) handlers.onCommit(dragging.current, pointerTime(event, track, duration));
      dragging.current = null;
    },
    cancel(): void {
      if (dragging.current !== null) handlers.onCancel();
      dragging.current = null;
    },
    active(): boolean {
      return dragging.current !== null;
    },
  };
}
