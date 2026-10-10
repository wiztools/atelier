import {useEffect, useRef, useState} from 'react';
import {drawWaveformPeaks} from './audioWaveform';
import {formatTime} from './AudioTimeline';

// AudioStage is the Cut & trim tool's stage: the source clip as a waveform
// with no timeline machinery. During playback it hops over removed footage
// (each skip lands on the start of the next kept region); when paused the
// playhead can rest anywhere, including inside a removed range the timeline
// dims. The audio element drives playback and stays silent here — the
// transport lives in the timeline row, like the video stage's.
export function AudioStage(props: {
  sourceUrl: string;
  duration: number;
  peaks: number[] | null;
  currentTime: number;
  playing: boolean;
  muted: boolean;
  skipRanges: {start: number; end: number}[];
  onTimeChange: (timeSeconds: number) => void;
  onPlayingChange: (playing: boolean) => void;
  onMutedChange: (muted: boolean) => void;
}) {
  const audioRef = useRef<HTMLAudioElement | null>(null);
  const canvasRef = useRef<HTMLCanvasElement | null>(null);
  const rafRef = useRef<number | null>(null);
  const [previewError, setPreviewError] = useState('');

  // Live props for the frame callback, which would otherwise close over a
  // stale skip table.
  const skipRef = useRef(props.skipRanges);
  skipRef.current = props.skipRanges;
  const playingRef = useRef(props.playing);
  playingRef.current = props.playing;
  const timeChangeRef = useRef(props.onTimeChange);
  timeChangeRef.current = props.onTimeChange;

  useEffect(() => {
    const audio = audioRef.current;
    if (!audio || props.playing || audio.seeking || Math.abs(audio.currentTime - props.currentTime) <= 0.001) return;
    audio.currentTime = props.currentTime;
  }, [props.currentTime, props.sourceUrl, props.playing]);

  useEffect(() => {
    const audio = audioRef.current;
    if (!audio) return;
    if (props.playing) {
      audio.play().catch(() => {
        if (audio.muted) {
          props.onPlayingChange(false);
          return;
        }
        // Playback policy can refuse unmuted playback outside a user gesture —
        // fall back to a silent retry instead of a dead play button.
        audio.muted = true;
        props.onMutedChange(true);
        void audio.play().catch(() => props.onPlayingChange(false));
      });
    } else {
      audio.pause();
    }
  }, [props.playing, props.sourceUrl]);

  useEffect(() => {
    const audio = audioRef.current;
    if (!audio) return;
    let stopped = false;
    const tick = () => {
      if (stopped) return;
      const time = audio.currentTime ?? 0;
      if (!audio.seeking && playingRef.current) {
        // Playback-only skip: a paused playhead may rest inside removed
        // footage so the user can see exactly where a cut lands.
        const skipped = skipRef.current.find((range) => time >= range.start && time < range.end);
        if (skipped) {
          audio.currentTime = skipped.end;
        } else {
          timeChangeRef.current(time);
        }
      }
      rafRef.current = window.requestAnimationFrame(tick);
    };
    rafRef.current = window.requestAnimationFrame(tick);
    return () => {
      stopped = true;
      if (rafRef.current !== null) window.cancelAnimationFrame(rafRef.current);
      rafRef.current = null;
    };
  }, [props.sourceUrl]);

  useEffect(() => {
    const canvas = canvasRef.current;
    if (!canvas) return;
    const draw = () => drawWaveformPeaks(canvas, props.peaks);
    draw();
    const observer = new ResizeObserver(draw);
    observer.observe(canvas);
    return () => observer.disconnect();
  }, [props.peaks]);

  return (
    <div className="audio-stage">
      <div className="audio-stage-panel">
        <div className="audio-stage-panel-title">
          <span>Source</span>
          <span>{formatTime(props.currentTime)} / {formatTime(props.duration)}</span>
        </div>
        <div className="audio-stage-media-frame">
          <canvas ref={canvasRef} className="audio-stage-waveform" aria-hidden="true" />
          {props.peaks === null ? <span className="audio-stage-waveform-note">Waveform unavailable — playback and cutting still work.</span> : null}
          <audio
            ref={audioRef}
            className="audio-stage-source"
            src={props.sourceUrl}
            muted={props.muted}
            preload="auto"
            onLoadedMetadata={() => {
              if (audioRef.current) audioRef.current.currentTime = props.currentTime;
            }}
            onEnded={() => props.onPlayingChange(false)}
            onError={() => setPreviewError('The audio could not be played.')}
            onPlay={() => props.onPlayingChange(true)}
            onPause={() => props.onPlayingChange(false)}
          />
          {previewError ? <span className="audio-stage-preview-error">Preview unavailable</span> : null}
        </div>
      </div>
    </div>
  );
}
