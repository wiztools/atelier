import {useEffect, useRef} from 'react';
import {preciseMediaTime} from './mediaTime';

// The audio sibling of VideoPlayer's time readout: WKWebView's native
// <audio controls> readout can't be reformatted either, so the same precise
// m:ss.mmm current/total line rides beneath the control bar. Like the video
// readout it is written straight to the DOM from a requestAnimationFrame
// loop while playing — onTimeUpdate alone fires far too coarsely for the
// millisecond digits — and stays selectable so a position can be copied.
function AudioPlayer({src}: {src: string}) {
  const audioRef = useRef<HTMLAudioElement | null>(null);
  const readoutRef = useRef<HTMLSpanElement | null>(null);
  const rafRef = useRef(0);

  const syncReadout = () => {
    const audio = audioRef.current;
    const readout = readoutRef.current;
    if (!audio || !readout) return;
    readout.textContent = `${preciseMediaTime(audio.currentTime)} / ${preciseMediaTime(audio.duration)}`;
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

  return (
    <div className="audio-player">
      <audio
        ref={audioRef}
        src={src}
        controls
        preload="metadata"
        onPlay={startTicking}
        onPause={stopTicking}
        onEnded={stopTicking}
        onSeeked={syncReadout}
        onTimeUpdate={syncReadout}
        onLoadedMetadata={syncReadout}
        onDurationChange={syncReadout}
      />
      <span ref={readoutRef} className="audio-time-readout">
        0:00.000 / 0:00.000
      </span>
    </div>
  );
}

export {AudioPlayer};
