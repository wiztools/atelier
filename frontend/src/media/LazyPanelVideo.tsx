import {useEffect, useRef, useState} from 'react';
import {posterURLForVideoSrc} from './mediaTime';
import {VideoPlayer} from './VideoPlayer';

// Panel video cards lazy-mount their player. An eager panel instantiates
// every clip's media pipeline at once — twenty-plus AVPlayer-backed <video>
// elements in one WKWebView page — and a burst of concurrent media loads can
// wedge the page's whole media stack: every later load, chat transcripts
// included, then stalls at networkState 2 without ever reaching the network,
// and only a webview restart clears it. Mounting just the cards near the
// list's scroll viewport bounds that burst, and unmounting paused players
// scrolled well past the band releases their resources in long sessions. A
// clip that is playing is never unmounted — its audio keeps rolling while the
// user browses other assets — and is released on its first pause once out of
// the band. The observed root is the .assets-list scroller (via closest) so
// rootMargin extends into the scrolled content, giving a mount lookahead
// band the viewport root's clipping would not. Panel playback is also
// exclusive: a clip that starts pauses every other mounted video in the
// panel, so starting one never layers a second clip's audio over the first.
function LazyPanelVideo({src}: {src: string}) {
  const slotRef = useRef<HTMLDivElement | null>(null);
  const [mounted, setMounted] = useState(false);
  const [posterFailed, setPosterFailed] = useState(false);
  const visibleRef = useRef(false);
  const playingRef = useRef(false);
  // The unmounted card has no <video> to paint a frame, so the poster image
  // carries the thumbnail (posterURLForVideoSrc) with the play glyph on top.
  // A 404 (pre-poster history, extractor absent at save time) falls back to
  // the bare glyph via onError — cheaper than pre-checking existence.
  const posterURL = posterFailed ? '' : posterURLForVideoSrc(src);

  useEffect(() => {
    const slot = slotRef.current;
    if (!slot) return;
    const observer = new IntersectionObserver((entries) => {
      for (const entry of entries) {
        visibleRef.current = entry.isIntersecting;
        if (entry.isIntersecting) {
          setMounted(true);
        } else if (!playingRef.current) {
          setMounted(false);
        }
      }
    }, {root: slot.closest('.assets-list') ?? undefined, rootMargin: '600px 0px'});
    observer.observe(slot);
    return () => observer.disconnect();
  }, []);

  const handlePlayingChange = (playing: boolean) => {
    playingRef.current = playing;
    // Exclusive playback: a clip that begins pauses every other mounted
    // video in the panel before either audibly advances. Walking the DOM
    // (rather than keeping a registry) scopes the policy to this panel —
    // transcript players are untouched — and sees exactly the videos the
    // lazy mount has instantiated right now.
    if (playing) {
      const panel = slotRef.current?.closest('.assets-panel');
      panel?.querySelectorAll<HTMLVideoElement>('video').forEach((video) => {
        if (!video.paused && !slotRef.current?.contains(video)) video.pause();
      });
    }
    // A pause while already outside the band gets no later observer callback,
    // so release the player here rather than waiting for the next scroll.
    if (!playing && !visibleRef.current) {
      setMounted(false);
    }
  };

  return (
    <div className="panel-video-slot" ref={slotRef}>
      {mounted ? (
        <VideoPlayer src={src} onPlayingChange={handlePlayingChange} />
      ) : (
        <div className="panel-video-placeholder" aria-hidden="true">
          {posterURL && (
            <img
              className="panel-video-poster"
              src={posterURL}
              alt=""
              onError={() => setPosterFailed(true)}
            />
          )}
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.5">
            <circle cx="12" cy="12" r="9" />
            <path d="M10 9.2v5.6l4.8-2.8z" fill="currentColor" stroke="none" />
          </svg>
        </div>
      )}
    </div>
  );
}

export {LazyPanelVideo};
