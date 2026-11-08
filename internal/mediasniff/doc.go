// Package mediasniff reads facts out of media bytes without decoding them:
// audio durations (WAV, MP3, AAC/ADTS, FLAC, OGG, M4A/MP4), MP4 video-track
// facts (duration, presentation and coded size, frame count, audio presence),
// image pixel sizes from magic bytes (PNG, GIF, WebP, JPEG), and the JPEG EXIF
// orientation. Everything is pure Go with no CLI or codec dependency, and every
// reader fails closed: on any structural doubt it reports !ok (or the neutral
// value) rather than a guess, so callers can fall back to ffprobe or a default.
package mediasniff
