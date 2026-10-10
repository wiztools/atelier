import {useRef} from 'react';
import {VideoReframeInterpolation, VideoReframeParams} from './videoReframe';
import {formatTime, pointerTime, useMarkerDrag, VideoTimelineControls} from './VideoTimelineControls';

function clamp(value: number, min: number, max: number): number {
  if (max < min) return min;
  return Math.min(max, Math.max(min, value));
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
  const drag = useMarkerDrag({
    onMove: props.onMoveMarker,
    onCommit: props.onCommitMarkerTime,
    onCancel: props.onCancelMarkerMove,
  });
  const duration = props.params.source.durationSeconds;
  const selectedMarker = props.params.markers[props.selectedMarkerIndex];

  function seek(event: React.PointerEvent<HTMLDivElement>) {
    if (drag.active()) return;
    const track = trackRef.current;
    if (!track) return;
    props.onSeek(pointerTime(event, track, duration));
  }

  function startMarkerDrag(index: number, event: React.PointerEvent<HTMLButtonElement>) {
    event.stopPropagation();
    props.onSelectMarker(index);
    if (index === 0 || props.busy) return;
    drag.start(index, event);
  }

  return (
    <div className="video-timeline">
      <VideoTimelineControls
        playing={props.playing}
        muted={props.muted}
        busy={props.busy}
        currentTime={props.currentTime}
        duration={duration}
        stepTargets={props.params.markers.map((marker) => marker.timeSeconds)}
        onTogglePlay={props.onTogglePlay}
        onToggleMuted={props.onToggleMuted}
        onSeek={props.onSeek}
      >
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
      </VideoTimelineControls>
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
            onPointerMove={(event) => drag.move(event, trackRef.current, duration)}
            onPointerUp={(event) => drag.end(event, trackRef.current, duration)}
            onPointerCancel={() => {drag.cancel();}}
            onLostPointerCapture={() => {drag.cancel();}}
            aria-label={`Framing marker at ${formatTime(marker.timeSeconds)}`}
            title={index === 0 ? 'Start marker' : 'Drag to retime marker'}
          />
        ))}
      </div>
    </div>
  );
}
