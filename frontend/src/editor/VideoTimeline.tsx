import {useRef} from 'react';
import {VideoReframeInterpolation, VideoReframeParams} from './videoReframe';

function clamp(value: number, min: number, max: number): number {
  if (max < min) return min;
  return Math.min(max, Math.max(min, value));
}

function formatTime(value: number): string {
  const seconds = Math.max(0, Number.isFinite(value) ? value : 0);
  const whole = Math.floor(seconds);
  const minutes = Math.floor(whole / 60);
  const rest = whole % 60;
  const fraction = Math.floor((seconds - whole) * 10);
  return `${minutes}:${String(rest).padStart(2, '0')}.${fraction}`;
}

function pointerTime(event: React.PointerEvent, element: HTMLElement, duration: number): number {
  const bounds = element.getBoundingClientRect();
  const x = clamp(event.clientX - bounds.left, 0, bounds.width);
  return bounds.width > 0 ? x * duration / bounds.width : 0;
}

export function VideoTimeline(props: {
  params: VideoReframeParams;
  currentTime: number;
  playing: boolean;
  muted: boolean;
  busy: boolean;
  selectedMarkerIndex: number;
  thumbnails: string[];
  onSeek: (timeSeconds: number) => void;
  onTogglePlay: () => void;
  onToggleMuted: () => void;
  onAddMarker: () => void;
  onSelectMarker: (index: number) => void;
  onMoveMarker: (index: number, timeSeconds: number) => void;
  onCommitMarkerTime: (index: number, timeSeconds: number) => void;
  onCancelMarkerMove: () => void;
  onDeleteMarker: (index: number) => void;
  onSetInterpolation: (index: number, interpolation: VideoReframeInterpolation) => void;
}) {
  const trackRef = useRef<HTMLDivElement | null>(null);
  const dragMarker = useRef<number | null>(null);
  const duration = props.params.source.durationSeconds;
  const selectedMarker = props.params.markers[props.selectedMarkerIndex];

  function seek(event: React.PointerEvent<HTMLDivElement>) {
    if (dragMarker.current !== null) return;
    const track = trackRef.current;
    if (!track) return;
    props.onSeek(pointerTime(event, track, duration));
  }

  function startMarkerDrag(index: number, event: React.PointerEvent<HTMLButtonElement>) {
    event.stopPropagation();
    props.onSelectMarker(index);
    if (index === 0 || props.busy) return;
    dragMarker.current = index;
    event.currentTarget.setPointerCapture(event.pointerId);
    event.preventDefault();
  }

  function moveMarker(event: React.PointerEvent<HTMLButtonElement>) {
    event.stopPropagation();
    if (dragMarker.current === null) return;
    const track = trackRef.current;
    if (!track) return;
    props.onMoveMarker(dragMarker.current, pointerTime(event, track, duration));
    event.preventDefault();
  }

  function endMarkerDrag(event: React.PointerEvent<HTMLButtonElement>) {
    if (dragMarker.current !== null) {
      const track = trackRef.current;
      if (track) props.onCommitMarkerTime(dragMarker.current, pointerTime(event, track, duration));
    }
    dragMarker.current = null;
  }

  return (
    <div className="video-timeline">
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
        <span className="video-timeline-time">{formatTime(props.currentTime)}</span>
        <button
          type="button"
          className="video-icon-button"
          onClick={props.onAddMarker}
          disabled={props.busy || props.params.markers.length >= 100}
          aria-label="Add a framing marker at the playhead"
          title="Add marker at the playhead"
        >
          ⚑+
        </button>
        <button
          type="button"
          className="video-icon-button"
          disabled={props.busy || props.selectedMarkerIndex <= 0}
          onClick={() => props.onDeleteMarker(props.selectedMarkerIndex)}
          aria-label="Delete the selected marker"
          title="Delete marker"
        >
          ⚑-
        </button>
        <label className="video-mini-field">
          Time
          <input
            type="number"
            min={0}
            max={duration}
            step={0.1}
            value={selectedMarker?.timeSeconds ?? 0}
            disabled={props.busy || props.selectedMarkerIndex === 0}
            onChange={(event) => props.onCommitMarkerTime(props.selectedMarkerIndex, Number(event.target.value))}
          />
        </label>
        <label className="video-mini-field">
          Move
          <select
            value={selectedMarker?.interpolationToNext ?? 'smooth'}
            disabled={props.busy || !selectedMarker}
            onChange={(event) => props.onSetInterpolation(props.selectedMarkerIndex, event.target.value as VideoReframeInterpolation)}
          >
            <option value="smooth">Smooth</option>
            <option value="linear">Linear</option>
            <option value="hold">Hold</option>
          </select>
        </label>
      </div>
      <div
        ref={trackRef}
        className="video-timeline-track"
        onPointerDown={seek}
        onPointerMove={(event) => {
          if (event.buttons === 1) seek(event);
        }}
      >
        <div className="video-timeline-thumbs">
          {props.thumbnails.slice(0, 30).map((url, index) => (
            <img key={`${url}-${index}`} src={url} alt="" draggable={false} />
          ))}
        </div>
        <div className="video-timeline-playhead" style={{left: `${duration ? clamp(props.currentTime / duration, 0, 1) * 100 : 0}%`}} />
        {props.params.markers.map((marker, index) => (
          <button
            key={index}
            type="button"
            className={`video-timeline-marker${index === props.selectedMarkerIndex ? ' selected' : ''}${index === 0 ? ' protected' : ''}`}
            style={{left: `${duration ? clamp(marker.timeSeconds / duration, 0, 1) * 100 : 0}%`}}
            onClick={(event) => {
              event.stopPropagation();
              props.onSelectMarker(index);
              props.onSeek(marker.timeSeconds);
            }}
            onPointerDown={(event) => startMarkerDrag(index, event)}
            onPointerMove={moveMarker}
            onPointerUp={endMarkerDrag}
            onPointerCancel={() => {if (dragMarker.current !== null) props.onCancelMarkerMove(); dragMarker.current = null;}}
            onLostPointerCapture={() => {if (dragMarker.current !== null) props.onCancelMarkerMove(); dragMarker.current = null;}}
            aria-label={`Framing marker at ${formatTime(marker.timeSeconds)}`}
            title={index === 0 ? 'Start marker' : 'Drag to retime marker'}
          />
        ))}
      </div>
      <input type="range" min={0} max={duration} step={0.001} value={props.currentTime}
        aria-label="Video playhead" onChange={(event) => props.onSeek(Number(event.currentTarget.value))} />
    </div>
  );
}
