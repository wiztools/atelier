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
  const overlayCanvasRef = useRef<HTMLCanvasElement | null>(null);
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
        // While paused the playhead state is authoritative — echoing the
        // frame-snapped media time back would cancel programmatic seeks
        // (frame-step, marker hops) that land inside the current frame.
        if (props.playing && !dragRef.current) props.onTimeChange(time);
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
  }, [props.params, props.sourceUrl, dragRect, props.playing]);

  useEffect(() => {
    draw();
  }, [props.params, dragRect, props.currentTime, props.sourceUrl]);

  // The crop overlay draws on a canvas instead of SVG: WKWebView leaves stale
  // semi-transparent stroke pixels behind when the SVG is mutated rapidly above
  // the video surface (e.g. marker hops). clearRect + full redraw of one bitmap
  // has no incremental invalidation to get wrong.
  const drawOverlay = () => {
    const canvas = overlayCanvasRef.current;
    const overlay = overlayRef.current;
    if (!canvas || !overlay) return;
    const bounds = overlay.getBoundingClientRect();
    const dpr = window.devicePixelRatio || 1;
    const pixelWidth = Math.max(1, Math.round(bounds.width * dpr));
    const pixelHeight = Math.max(1, Math.round(bounds.height * dpr));
    if (canvas.width !== pixelWidth || canvas.height !== pixelHeight) {
      canvas.width = pixelWidth;
      canvas.height = pixelHeight;
    }
    const context = canvas.getContext('2d');
    if (!context) return;
    context.setTransform(1, 0, 0, 1, 0, 0);
    context.clearRect(0, 0, canvas.width, canvas.height);
    if (bounds.width <= 0 || bounds.height <= 0) return;
    const scale = Math.min(bounds.width / props.params.source.width, bounds.height / props.params.source.height);
    context.setTransform(
      dpr * scale, 0, 0, dpr * scale,
      dpr * (bounds.width - props.params.source.width * scale) / 2,
      dpr * (bounds.height - props.params.source.height * scale) / 2,
    );
    const rect = displayedRect;
    context.beginPath();
    context.rect(0, 0, props.params.source.width, props.params.source.height);
    context.rect(rect.x, rect.y, rect.width, rect.height);
    context.fillStyle = 'rgba(0,0,0,.58)';
    context.fill('evenodd');
    context.strokeStyle = '#ffffff';
    context.lineWidth = 1.5 / scale;
    context.strokeRect(rect.x, rect.y, rect.width, rect.height);
    context.strokeStyle = 'rgba(255,255,255,.55)';
    context.lineWidth = 1 / scale;
    context.beginPath();
    for (const i of [1, 2]) {
      context.moveTo(rect.x + rect.width * i / 3, rect.y);
      context.lineTo(rect.x + rect.width * i / 3, rect.y + rect.height);
      context.moveTo(rect.x, rect.y + rect.height * i / 3);
      context.lineTo(rect.x + rect.width, rect.y + rect.height * i / 3);
    }
    context.stroke();
    const selected = props.params.markers[props.selectedMarkerIndex];
    if (selected) {
      context.beginPath();
      context.arc(selected.x + selected.width / 2, selected.y + selected.height / 2, 12 / scale, 0, Math.PI * 2);
      context.fillStyle = '#8ab4f8';
      context.fill();
      context.strokeStyle = '#101214';
      context.lineWidth = 3 / scale;
      context.stroke();
    }
  };

  const drawOverlayRef = useRef(() => {});
  drawOverlayRef.current = drawOverlay;

  useEffect(() => {
    drawOverlay();
  }, [props.params, dragRect, props.currentTime, props.sourceUrl, props.selectedMarkerIndex]);

  useEffect(() => {
    const overlay = overlayRef.current;
    if (!overlay || typeof ResizeObserver === 'undefined') return;
    const observer = new ResizeObserver(() => drawOverlayRef.current());
    observer.observe(overlay);
    return () => observer.disconnect();
  }, []);

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
            <canvas
              ref={overlayCanvasRef}
              className="video-stage-overlay-canvas"
              onClick={() => props.onSelectMarker(props.selectedMarkerIndex)}
            />
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
