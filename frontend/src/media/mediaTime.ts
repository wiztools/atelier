// Pure helpers the media players share: the poster-frame URL convention
// for persisted video artifacts and the precise m:ss.mmm time readout.

// posterURLForVideoSrc derives the poster-frame URL for a video src by the
// vid_<hex>_poster.jpg convention the Go writer produces beside every
// persisted video artifact (video_poster.go) — generateVideoPoster swaps the
// clip's extension for _poster.jpg in the same directory, so the swap here is
// its mirror. Only /atelier-artifact URLs carry the convention: a data URL
// (an in-flight attachment, not yet an artifact) or a foreign URL returns '',
// and a poster that 404s (history from before posters existed, or no
// extractor resolved at save time) renders as nothing — the video/placeholder
// shows exactly as it did without one.
function posterURLForVideoSrc(src: string): string {
  if (!src.startsWith('/atelier-artifact/')) return '';
  const slash = src.lastIndexOf('/');
  const dot = src.lastIndexOf('.');
  if (dot <= slash) return '';
  return src.slice(0, dot) + '_poster.jpg';
}

function preciseMediaTime(seconds: number): string {
  if (!Number.isFinite(seconds) || seconds < 0) return '0:00.000';
  const minutes = Math.floor(seconds / 60);
  const rest = seconds - minutes * 60;
  return `${minutes}:${rest.toFixed(3).padStart(6, '0')}`;
}

export {posterURLForVideoSrc, preciseMediaTime};
