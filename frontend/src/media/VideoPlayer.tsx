import {useEffect, useRef, useState} from 'react';
import {videoThumbnailOnlySrc} from '../mediaIntake';
import {posterURLForVideoSrc, preciseMediaTime} from './mediaTime';

// The native <video controls> time readout belongs to WKWebView and can't be
// reformatted, so VideoPlayer floats its own precise m:ss.mmm readout under
// the frame. Milliseconds rather than microseconds: playback position is only
// frame-accurate, so finer digits would be noise. The readout is written
// straight to the DOM from a requestAnimationFrame loop while playing (React
// re-renders never touch the span because its JSX children stay static).
// onPlayingChange, when given, reports play/pause/end transitions so a lazy
// host (LazyPanelVideo) can keep a playing clip mounted while releasing
// paused ones.
//
// The poster is painted as an explicit affordance above the not-yet-playing
// video — the thumbnail plus a centered play glyph, the whole thing a button
// that starts playback — rather than trusted to the native poster attribute:
// WKWebView's attribute handling is state-dependent (a freshly-mounted
// preload="metadata" video shows it, an element whose src or poster lands
// after mount may not), and a bare image over the video reads as a still,
// not a clip. While the overlay is up the native controls are hidden — the
// overlay is the single play affordance; chrome and glyph together read as
// two play buttons. Click plays; the overlay drops on first play, on load
// error (pre-poster history keeps today's plain video), on a video error
// (controls need to be reachable), and resets when the derived URL changes
// (the upload swap re-renders the same mount from a data: URL to the
// artifact URL). In every dropped state the native controls are on.
function VideoPlayer({src, onPlayingChange}: {src: string; onPlayingChange?: (playing: boolean) => void}) {
  const videoRef = useRef<HTMLVideoElement | null>(null);
  const readoutRef = useRef<HTMLSpanElement | null>(null);
  const rafRef = useRef(0);
  const posterURL = posterURLForVideoSrc(src);
  const [posterHidden, setPosterHidden] = useState(false);
  const [stillFailed, setStillFailed] = useState(false);

  useEffect(() => {
    setPosterHidden(false);
    setStillFailed(false);
  }, [posterURL]);

  const syncReadout = () => {
    const video = videoRef.current;
    const readout = readoutRef.current;
    if (!video || !readout) return;
    readout.textContent = `${preciseMediaTime(video.currentTime)} / ${preciseMediaTime(video.duration)}`;
  };
  const tick = () => {
    syncReadout();
    rafRef.current = requestAnimationFrame(tick);
  };
  const startTicking = () => {
    cancelAnimationFrame(rafRef.current);
    rafRef.current = requestAnimationFrame(tick);
  };
  const stopTicking = () => {
    cancelAnimationFrame(rafRef.current);
    syncReadout();
  };

  useEffect(() => () => cancelAnimationFrame(rafRef.current), []);

  // FLV/AVI/TS (and MPG) have no WebKit demuxer: a <video> element would sit
  // black under a dead control bar, so these containers render the poster
  // still alone — no player, no readout, no play affordance
  // (videoThumbnailOnlySrc in mediaIntake.ts). The ffmpeg-backed tools read
  // them fine, so the clip remains a full attachment. A missing poster (no
  // extractor at save time) degrades to the quiet glyph.
  if (videoThumbnailOnlySrc(src)) {
    return (
      <div className="video-player">
        <div className="video-stage">
          {posterURL && !stillFailed ? (
            <img className="video-poster-still" src={posterURL} alt="" onError={() => setStillFailed(true)} />
          ) : (
            <span className="video-poster-still-empty" aria-hidden="true">
              <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.5">
                <circle cx="12" cy="12" r="9" />
                <path d="M10 9.2v5.6l4.8-2.8z" fill="currentColor" stroke="none" />
              </svg>
            </span>
          )}
        </div>
      </div>
    );
  }

  return (
    <div className="video-player">
      <div className="video-stage">
        <video
          ref={videoRef}
          src={src}
          // Native chrome is hidden while the poster overlay is up so the
          // overlay is the ONE play affordance — controls and an overlay
          // glyph together read as two play buttons. Controls take over the
          // moment the overlay drops (first play, or a failed/error state).
          controls={posterHidden || !posterURL}
          preload="metadata"
          poster={posterURL || undefined}
          onPlay={() => {
            startTicking();
            setPosterHidden(true);
            onPlayingChange?.(true);
          }}
          onPause={() => {
            stopTicking();
            onPlayingChange?.(false);
          }}
          onEnded={() => {
            stopTicking();
            onPlayingChange?.(false);
          }}
          onSeeked={() => {
            syncReadout();
            // A seek decodes a frame even before first play — the overlay
            // must step aside for it.
            setPosterHidden(true);
          }}
          onError={() => {
            // An undecodable clip must expose its controls/error state, not
            // a poster the click-through can never move.
            setPosterHidden(true);
          }}
          onTimeUpdate={syncReadout}
          onLoadedMetadata={syncReadout}
          onDurationChange={syncReadout}
        />
        {posterURL && !posterHidden && (
          <button
            type="button"
            className="video-poster-overlay"
            aria-label="Play video"
            onClick={() => void videoRef.current?.play().catch(() => {})}
          >
            <img
              src={posterURL}
              alt=""
              onError={() => setPosterHidden(true)}
            />
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.5" aria-hidden="true">
              <circle cx="12" cy="12" r="9" />
              <path d="M10 9.2v5.6l4.8-2.8z" fill="currentColor" stroke="none" />
            </svg>
          </button>
        )}
      </div>
      <span ref={readoutRef} className="video-time-readout">
        0:00.000 / 0:00.000
      </span>
    </div>
  );
}

export {VideoPlayer};
