import {useEffect, useRef, useState} from 'react';
import {formatTime} from './VideoTimelineControls';

type FrameVideo = HTMLVideoElement & {
  requestVideoFrameCallback?: (callback: (now: number, metadata: {mediaTime: number}) => void) => number;
  cancelVideoFrameCallback?: (handle: number) => void;
};

// VideoTrimStage is the trim tool's stage: the source clip full-width with no
// crop machinery. During playback it hops over removed footage (each skip
// lands on the start of the next kept region); when paused the playhead can
// rest anywhere, including inside a removed range the timeline dims.
export function VideoTrimStage(props: {
  sourceUrl: string;
  currentTime: number;
  playing: boolean;
  muted: boolean;
  skipRanges: {start: number; end: number}[];
  duration: number;
  onTimeChange: (timeSeconds: number) => void;
  onPlayingChange: (playing: boolean) => void;
  onMutedChange: (muted: boolean) => void;
}) {
  const videoRef = useRef<FrameVideo | null>(null);
  const frameRef = useRef<number | null>(null);
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
    const video = videoRef.current;
    if (!video || props.playing || video.seeking || Math.abs(video.currentTime - props.currentTime) <= 0.001) return;
    video.currentTime = props.currentTime;
  }, [props.currentTime, props.sourceUrl, props.playing]);

  useEffect(() => {
    const video = videoRef.current;
    if (!video) return;
    if (props.playing) {
      video.play().catch(() => {
        if (video.muted) {
          props.onPlayingChange(false);
          return;
        }
        // Playback policy can refuse unmuted playback outside a user gesture —
        // fall back to a silent retry instead of a dead play button.
        video.muted = true;
        props.onMutedChange(true);
        void video.play().catch(() => props.onPlayingChange(false));
      });
    } else {
      video.pause();
    }
  }, [props.playing, props.sourceUrl]);

  useEffect(() => {
    const video = videoRef.current;
    if (!video) return;
    let stopped = false;
    const tick = (_now?: number, metadata?: {mediaTime: number}) => {
      if (stopped) return;
      const time = metadata?.mediaTime ?? video.currentTime ?? 0;
      if (!video.seeking) {
        // Playback-only skip: a paused playhead may rest inside removed
        // footage so the user can see exactly where a cut lands.
        if (playingRef.current) {
          const skipped = skipRef.current.find((range) => time >= range.start && time < range.end);
          if (skipped) {
            video.currentTime = skipped.end;
            return;
          }
          timeChangeRef.current(time);
        }
      }
      if (video.requestVideoFrameCallback) {
        frameRef.current = video.requestVideoFrameCallback(tick);
      } else {
        rafRef.current = window.requestAnimationFrame(() => tick());
      }
    };
    if (video.requestVideoFrameCallback) {
      frameRef.current = video.requestVideoFrameCallback(tick);
    } else {
      rafRef.current = window.requestAnimationFrame(() => tick());
    }
    return () => {
      stopped = true;
      if (frameRef.current !== null && video.cancelVideoFrameCallback) video.cancelVideoFrameCallback(frameRef.current);
      if (rafRef.current !== null) window.cancelAnimationFrame(rafRef.current);
      frameRef.current = null;
      rafRef.current = null;
    };
  }, [props.sourceUrl]);

  return (
    <div className="video-stage solo">
      <div className="video-stage-panel">
        <div className="video-stage-panel-title">
          <span>Source</span>
          <span>{formatTime(props.currentTime)} / {formatTime(props.duration)}</span>
        </div>
        <div className="video-stage-media-frame">
          <video
            ref={videoRef}
            className="video-stage-source"
            src={props.sourceUrl}
            playsInline
            muted={props.muted}
            preload="auto"
            onLoadedMetadata={() => {
              if (videoRef.current) videoRef.current.currentTime = props.currentTime;
            }}
            onEnded={() => props.onPlayingChange(false)}
            onError={() => setPreviewError('The video could not be played.')}
            onPlay={() => props.onPlayingChange(true)}
            onPause={() => props.onPlayingChange(false)}
          />
          {previewError ? <span className="video-stage-preview-error">Preview unavailable</span> : null}
        </div>
      </div>
    </div>
  );
}
