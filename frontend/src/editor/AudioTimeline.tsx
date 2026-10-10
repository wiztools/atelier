import {ReactNode, useEffect, useRef} from 'react';
import {pointerTime, useMarkerDrag} from './VideoTimelineControls';
import {drawWaveformPeaks} from './audioWaveform';
import {maxAudioTrimRegions, trimRegionBounds, AudioTrimDraft} from './audioTrimModel';

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

// The audio timeline's transport row: play/pause, mute, cut hops, 0.1s steps,
// and the time readout — the video timeline's shared row with audio labels
// and a coarser step (a waveform has no frames). Tool-specific buttons and
// mini-fields render after the readout via children.
function AudioTimelineControls(props: {
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
  const stepSeconds = 0.1;

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

  function stepSmall(direction: -1 | 1) {
    const next = Math.max(0, Math.min(props.duration, props.currentTime + direction * stepSeconds));
    if (next === props.currentTime) return;
    props.onSeek(next);
  }

  return (
    <div className="audio-timeline-controls">
      <button
        type="button"
        className="audio-icon-button"
        onClick={props.onTogglePlay}
        aria-label={props.playing ? 'Pause audio' : 'Play audio'}
        title={props.playing ? 'Pause' : 'Play'}
      >
        {props.playing ? '❚❚' : '▶'}
      </button>
      <button
        type="button"
        className="audio-icon-button"
        onClick={props.onToggleMuted}
        aria-label={props.muted ? 'Unmute preview' : 'Mute preview'}
        title={props.muted ? 'Unmute' : 'Mute'}
      >
        {props.muted ? '🔇' : '🔊'}
      </button>
      <button
        type="button"
        className="audio-icon-button"
        onClick={() => stepTo(-1)}
        disabled={props.busy || stepTarget(-1) === null}
        aria-label="Seek to the previous cut"
        title="Previous cut"
      >
        |◀
      </button>
      <button
        type="button"
        className="audio-icon-button"
        onClick={() => stepSmall(-1)}
        disabled={props.busy || props.currentTime <= 0}
        aria-label="Step the playhead back"
        title="Step back"
      >
        ◀◀
      </button>
      <span className="audio-timeline-time">{formatTime(props.currentTime)}</span>
      <button
        type="button"
        className="audio-icon-button"
        onClick={() => stepSmall(1)}
        disabled={props.busy || props.currentTime >= props.duration}
        aria-label="Step the playhead forward"
        title="Step forward"
      >
        ▶▶
      </button>
      <button
        type="button"
        className="audio-icon-button"
        onClick={() => stepTo(1)}
        disabled={props.busy || stepTarget(1) === null}
        aria-label="Seek to the next cut"
        title="Next cut"
      >
        ▶|
      </button>
      {props.children}
    </div>
  );
}

// AudioTimeline is the Cut & trim tool's timeline: the waveform dims under
// striped overlays wherever audio is marked removed, and draggable cut markers
// (the video trim idiom) sit on the boundaries. Region commands target the
// playhead's region — position the playhead, then cut or keep.
export function AudioTimeline(props: {
  draft: AudioTrimDraft;
  duration: number;
  currentTime: number;
  playing: boolean;
  muted: boolean;
  busy: boolean;
  selectedCutIndex: number;
  peaks: number[] | null;
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
  const canvasRef = useRef<HTMLCanvasElement | null>(null);
  const drag = useMarkerDrag({
    onMove: props.onMoveCut,
    onCommit: props.onCommitCutTime,
    onCancel: props.onCancelCutMove,
  });
  const selectedCut = props.selectedCutIndex >= 0 ? props.draft.cuts[props.selectedCutIndex] : undefined;
  const regions = trimRegionBounds(props.draft, props.duration);
  const playheadRegion = regionIndexAt(regions, props.currentTime);

  // The track's waveform strip is decorative (regions, markers, and the
  // playhead carry the information), so it draws imperatively on peaks and
  // size changes instead of going through React's diff.
  useEffect(() => {
    const canvas = canvasRef.current;
    if (!canvas) return;
    const draw = () => drawWaveformPeaks(canvas, props.peaks);
    draw();
    const observer = new ResizeObserver(draw);
    observer.observe(canvas);
    return () => observer.disconnect();
  }, [props.peaks]);

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
    <div className="audio-timeline">
      <AudioTimelineControls
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
          className="audio-icon-button"
          onClick={props.onAddCut}
          disabled={props.busy || props.draft.cuts.length >= maxAudioTrimRegions - 1}
          aria-label="Add a cut at the playhead"
          title="Add cut at the playhead"
        >
          ⚑+
        </button>
        <button
          type="button"
          className="audio-icon-button"
          disabled={props.busy || props.selectedCutIndex < 0}
          onClick={() => props.onDeleteCut(props.selectedCutIndex)}
          aria-label="Delete the selected cut"
          title="Delete cut"
        >
          ⚑-
        </button>
        <button
          type="button"
          className="audio-icon-button"
          onClick={props.onToggleRegionRemoved}
          disabled={props.busy}
          aria-label={regions[playheadRegion]?.removed ? 'Keep the audio at the playhead' : 'Remove the audio at the playhead'}
          title={regions[playheadRegion]?.removed ? 'Keep at playhead' : 'Remove at playhead'}
        >
          {regions[playheadRegion]?.removed ? '✓' : '✂'}
        </button>
        <label className="audio-mini-field">
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
      </AudioTimelineControls>
      <div
        ref={trackRef}
        className="audio-timeline-track"
        onPointerDown={seek}
        onPointerMove={(event) => {
          if (event.buttons === 1) seek(event);
        }}
      >
        <canvas ref={canvasRef} className="audio-timeline-waveform" aria-hidden="true" />
        {regions.filter((region) => region.removed && region.end - region.start > 0).map((region, index) => (
          <div
            key={index}
            className="audio-timeline-region"
            style={{
              left: `${clamp(region.start / props.duration, 0, 1) * 100}%`,
              width: `${clamp((region.end - region.start) / props.duration, 0, 1) * 100}%`,
            }}
            title="Removed audio"
          />
        ))}
        <div className="audio-timeline-playhead" style={{left: `${props.duration ? clamp(props.currentTime / props.duration, 0, 1) * 100 : 0}%`}} />
        {props.draft.cuts.map((cut, index) => (
          <button
            key={index}
            type="button"
            className={`audio-timeline-marker cut${index === props.selectedCutIndex ? ' selected' : ''}`}
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
