import {useEffect, useMemo, useRef, useState} from 'react';
import {VideoReframeParams, VideoReframeRect, evaluateVideoReframe} from './videoReframe';
import {clampVideoReframeRect} from './videoTimelineModel';

type FrameVideo = HTMLVideoElement & {
  requestVideoFrameCallback?: (callback: (now: number, metadata: {mediaTime: number}) => void) => number;
  cancelVideoFrameCallback?: (handle: number) => void;
};

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

function pointerToSourcePoint(event: React.PointerEvent, element: HTMLElement, params: VideoReframeParams): {x: number; y: number} {
  const bounds = element.getBoundingClientRect();
  const sourceRatio = params.source.width / params.source.height;
  const boxRatio = bounds.width / bounds.height;
  let left = bounds.left;
  let top = bounds.top;
  let width = bounds.width;
  let height = bounds.height;
  if (boxRatio > sourceRatio) {
    width = height * sourceRatio;
    left += (bounds.width - width) / 2;
  } else {
    height = width / sourceRatio;
    top += (bounds.height - height) / 2;
  }
  return {
    x: clamp((event.clientX - left) * params.source.width / width, 0, params.source.width),
    y: clamp((event.clientY - top) * params.source.height / height, 0, params.source.height),
  };
}

export function VideoStage(props: {
  sourceUrl: string;
  params: VideoReframeParams;
  currentTime: number;
  playing: boolean;
  muted: boolean;
  busy: boolean;
  selectedMarkerIndex: number;
  onTimeChange: (timeSeconds: number) => void;
  onPlayingChange: (playing: boolean) => void;
  onMutedChange: (muted: boolean) => void;
  onCommitFraming: (timeSeconds: number, rect: VideoReframeRect) => void;
  onSelectMarker: (index: number) => void;
}) {
  const videoRef = useRef<FrameVideo | null>(null);
  const canvasRef = useRef<HTMLCanvasElement | null>(null);
  const overlayRef = useRef<HTMLDivElement | null>(null);
  const frameRef = useRef<number | null>(null);
  const rafRef = useRef<number | null>(null);
  const dragRef = useRef<{startX: number; startY: number; rect: VideoReframeRect; latest: VideoReframeRect; time: number} | null>(null);
  const [dragRect, setDragRect] = useState<VideoReframeRect | null>(null);
  const [previewError, setPreviewError] = useState('');

  const displayedRect = dragRect ?? evaluateVideoReframe(props.params, props.currentTime);
  const selectedMarker = props.params.markers[props.selectedMarkerIndex];
  const outputStyle = useMemo(() => ({
    aspectRatio: `${props.params.output.width} / ${props.params.output.height}`,
  }), [props.params.output.width, props.params.output.height]);

  const draw = (frameTime?: number) => {
    const video = videoRef.current;
    const canvas = canvasRef.current;
    if (!video || !canvas || video.readyState < 2 || video.seeking) return;
    const rect = dragRef.current?.latest ?? evaluateVideoReframe(props.params, frameTime ?? video.currentTime);
    if (canvas.width !== props.params.output.width) canvas.width = props.params.output.width;
    if (canvas.height !== props.params.output.height) canvas.height = props.params.output.height;
    const context = canvas.getContext('2d');
    if (!context) return;
    try {
      context.clearRect(0, 0, canvas.width, canvas.height);
      context.drawImage(video, rect.x, rect.y, rect.width, rect.height, 0, 0, canvas.width, canvas.height);
      if (previewError) setPreviewError('');
    } catch (error) {
      setPreviewError(error instanceof Error ? error.message : String(error));
    }
  };

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
        if (!dragRef.current) props.onTimeChange(time);
        draw(time);
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
  }, [props.params, props.sourceUrl, dragRect]);

  useEffect(() => {
    draw();
  }, [props.params, dragRect, props.currentTime, props.sourceUrl]);

  function beginDrag(event: React.PointerEvent<HTMLDivElement>) {
    if (props.busy || event.button !== 0) return;
    const overlay = overlayRef.current;
    if (!overlay) return;
    const point = pointerToSourcePoint(event, overlay, props.params);
    const rect = evaluateVideoReframe(props.params, props.currentTime);
    if (point.x < rect.x || point.x > rect.x + rect.width || point.y < rect.y || point.y > rect.y + rect.height) return;
    props.onPlayingChange(false);
    videoRef.current?.pause();
    dragRef.current = {startX: point.x, startY: point.y, rect, latest: rect, time: props.currentTime};
    setDragRect(rect);
    event.currentTarget.setPointerCapture(event.pointerId);
    event.preventDefault();
  }

  function moveDrag(event: React.PointerEvent<HTMLDivElement>) {
    const drag = dragRef.current;
    const overlay = overlayRef.current;
    if (!drag || !overlay) return;
    const point = pointerToSourcePoint(event, overlay, props.params);
    const next = clampVideoReframeRect({
      ...drag.rect,
      x: drag.rect.x + point.x - drag.startX,
      y: drag.rect.y + point.y - drag.startY,
    }, props.params.source);
    drag.latest = next;
    setDragRect(next);
    event.preventDefault();
  }

  function finishDrag(cancelled: boolean) {
    const drag = dragRef.current;
    if (!drag) return;
    const finalRect = drag.latest;
    dragRef.current = null;
    setDragRect(null);
    if (!cancelled && JSON.stringify(finalRect) !== JSON.stringify(drag.rect)) {
      props.onCommitFraming(drag.time, finalRect);
    }
  }

  return (
    <div className="video-stage">
      <div className="video-stage-panel">
        <div className="video-stage-panel-title">
          <span>Source</span>
          <span>{formatTime(props.currentTime)} / {formatTime(props.params.source.durationSeconds)}</span>
        </div>
        <div className="video-stage-media-frame" ref={overlayRef}>
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
            onLoadedData={() => draw()}
            onSeeked={() => draw()}
            onEnded={() => props.onPlayingChange(false)}
            onError={() => setPreviewError('The video could not be played.')}
            onPlay={() => props.onPlayingChange(true)}
            onPause={() => props.onPlayingChange(false)}
          />
          <div
            className="video-stage-overlay"
            onPointerDown={beginDrag}
            onPointerMove={moveDrag}
            onPointerUp={(event) => { moveDrag(event); finishDrag(false); }}
            onPointerCancel={() => finishDrag(true)}
            onLostPointerCapture={() => finishDrag(true)}
          >
            <svg width="100%" height="100%" viewBox={`0 0 ${props.params.source.width} ${props.params.source.height}`} preserveAspectRatio="xMidYMid meet" aria-hidden="true">
              <path fill="rgba(0,0,0,.58)" fillRule="evenodd" d={`M0 0H${props.params.source.width}V${props.params.source.height}H0Z M${displayedRect.x} ${displayedRect.y}h${displayedRect.width}v${displayedRect.height}h-${displayedRect.width}Z`} />
              <rect x={displayedRect.x} y={displayedRect.y} width={displayedRect.width} height={displayedRect.height} fill="transparent" stroke="white" strokeWidth="1.5" vectorEffect="non-scaling-stroke" />
              {[1, 2].map((i) => (
                <g key={i} stroke="rgba(255,255,255,.55)" strokeWidth="1" style={{pointerEvents: 'none'}}>
                  <line x1={displayedRect.x + displayedRect.width * i / 3} y1={displayedRect.y} x2={displayedRect.x + displayedRect.width * i / 3} y2={displayedRect.y + displayedRect.height} vectorEffect="non-scaling-stroke" />
                  <line x1={displayedRect.x} y1={displayedRect.y + displayedRect.height * i / 3} x2={displayedRect.x + displayedRect.width} y2={displayedRect.y + displayedRect.height * i / 3} vectorEffect="non-scaling-stroke" />
                </g>
              ))}
              {selectedMarker ? (
                <circle cx={selectedMarker.x + selectedMarker.width / 2} cy={selectedMarker.y + selectedMarker.height / 2} r="12" fill="#8ab4f8" stroke="#101214" strokeWidth="3" vectorEffect="non-scaling-stroke" onClick={() => props.onSelectMarker(props.selectedMarkerIndex)} />
              ) : null}
            </svg>
          </div>
        </div>
      </div>
      <div className="video-stage-panel">
        <div className="video-stage-panel-title">
          <span>Output</span>
          <span>{props.params.output.width} x {props.params.output.height}</span>
        </div>
        <div className="video-stage-output-frame" style={outputStyle}>
          <canvas ref={canvasRef} className="video-stage-output" />
          {previewError ? <span className="video-stage-preview-error">Preview unavailable</span> : null}
        </div>
      </div>
    </div>
  );
}
