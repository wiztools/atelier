import {useRef} from 'react';
import {formatTime, pointerTime, useMarkerDrag, VideoTimelineControls} from './VideoTimelineControls';
import {maxVideoTrimRegions, trimRegionBounds, VideoTrimDraft} from './videoTrimModel';

function clamp(value: number, min: number, max: number): number {
  if (max < min) return min;
  return Math.min(max, Math.max(min, value));
}

// VideoTrimTimeline is the trim/cut tool's timeline: the filmstrip dims under
// striped overlays wherever footage is marked removed, and draggable cut
// markers (the reframe idiom) sit on the boundaries. Region commands target
// the playhead's region — position the playhead, then cut or keep.
export function VideoTrimTimeline(props: {
  draft: VideoTrimDraft;
  duration: number;
  currentTime: number;
  playing: boolean;
  muted: boolean;
  busy: boolean;
  selectedCutIndex: number;
  thumbnails: string[];
  onSeek: (timeSeconds: number) => void;
  onTogglePlay: () => void;
  onToggleMuted: () => void;
  onAddCut: () => void;
  onDeleteCut: (index: number) => void;
  onSelectCut: (index: number) => void;
  onMoveCut: (index: number, timeSeconds: number) => void;
  onCommitCutTime: (index: number, timeSeconds: number) => void;
  onCancelCutMove: () => void;
  onToggleRegionRemoved: () => void;
}) {
  const trackRef = useRef<HTMLDivElement | null>(null);
  const drag = useMarkerDrag({
    onMove: props.onMoveCut,
    onCommit: props.onCommitCutTime,
    onCancel: props.onCancelCutMove,
  });
  const selectedCut = props.selectedCutIndex >= 0 ? props.draft.cuts[props.selectedCutIndex] : undefined;
  const regions = trimRegionBounds(props.draft, props.duration);
  const playheadRegion = regionIndexAt(regions, props.currentTime);

  function seek(event: React.PointerEvent<HTMLDivElement>) {
    if (drag.active()) return;
    const track = trackRef.current;
    if (!track) return;
    props.onSeek(pointerTime(event, track, props.duration));
  }

  function startCutDrag(index: number, event: React.PointerEvent<HTMLButtonElement>) {
    event.stopPropagation();
    props.onSelectCut(index);
    if (props.busy) return;
    drag.start(index, event);
  }

  return (
    <div className="video-timeline">
      <VideoTimelineControls
        playing={props.playing}
        muted={props.muted}
        busy={props.busy}
        currentTime={props.currentTime}
        duration={props.duration}
        stepTargets={props.draft.cuts.map((cut) => cut.timeSeconds)}
        onTogglePlay={props.onTogglePlay}
        onToggleMuted={props.onToggleMuted}
        onSeek={props.onSeek}
      >
        <button
          type="button"
          className="video-icon-button"
          onClick={props.onAddCut}
          disabled={props.busy || props.draft.cuts.length >= maxVideoTrimRegions - 1}
          aria-label="Add a cut at the playhead"
          title="Add cut at the playhead"
        >
          ⚑+
        </button>
        <button
          type="button"
          className="video-icon-button"
          disabled={props.busy || props.selectedCutIndex < 0}
          onClick={() => props.onDeleteCut(props.selectedCutIndex)}
          aria-label="Delete the selected cut"
          title="Delete cut"
        >
          ⚑-
        </button>
        <button
          type="button"
          className="video-icon-button"
          onClick={props.onToggleRegionRemoved}
          disabled={props.busy}
          aria-label={regions[playheadRegion]?.removed ? 'Keep the footage at the playhead' : 'Remove the footage at the playhead'}
          title={regions[playheadRegion]?.removed ? 'Keep at playhead' : 'Remove at playhead'}
        >
          {regions[playheadRegion]?.removed ? '✓' : '✂'}
        </button>
        <label className="video-mini-field">
          Time
          <input
            type="number"
            min={0}
            max={props.duration}
            step={0.1}
            value={selectedCut?.timeSeconds ?? 0}
            disabled={props.busy || !selectedCut}
            onChange={(event) => props.onCommitCutTime(props.selectedCutIndex, Number(event.target.value))}
          />
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
        {regions.filter((region) => region.removed && region.end - region.start > 0).map((region, index) => (
          <div
            key={index}
            className="video-timeline-region"
            style={{
              left: `${clamp(region.start / props.duration, 0, 1) * 100}%`,
              width: `${clamp((region.end - region.start) / props.duration, 0, 1) * 100}%`,
            }}
            title="Removed footage"
          />
        ))}
        <div className="video-timeline-playhead" style={{left: `${props.duration ? clamp(props.currentTime / props.duration, 0, 1) * 100 : 0}%`}} />
        {props.draft.cuts.map((cut, index) => (
          <button
            key={index}
            type="button"
            className={`video-timeline-marker cut${index === props.selectedCutIndex ? ' selected' : ''}`}
            style={{left: `${props.duration ? clamp(cut.timeSeconds / props.duration, 0, 1) * 100 : 0}%`}}
            onClick={(event) => {
              event.stopPropagation();
              props.onSelectCut(index);
              props.onSeek(cut.timeSeconds);
            }}
            onPointerDown={(event) => startCutDrag(index, event)}
            onPointerMove={(event) => drag.move(event, trackRef.current, props.duration)}
            onPointerUp={(event) => drag.end(event, trackRef.current, props.duration)}
            onPointerCancel={() => {drag.cancel();}}
            onLostPointerCapture={() => {drag.cancel();}}
            aria-label={`Cut at ${formatTime(cut.timeSeconds)}`}
            title="Drag to move the cut"
          />
        ))}
      </div>
    </div>
  );
}

function regionIndexAt(regions: {start: number; end: number}[], time: number): number {
  for (let index = 0; index < regions.length; index++) {
    if (time < regions[index].end || index === regions.length - 1) return index;
  }
  return regions.length - 1;
}
