// Audio waveform peaks for the composition editor's audio segments. The
// pure math (compute/slice/path) compiles alone under
// test/run-audio-peaks-tests.cjs like the other editor models — no React, no
// wailsjs — while the loader half uses only DOM APIs (fetch/AudioContext),
// which tsc's default libraries already type. Decoding happens once per
// artifact URL and the per-bucket peaks are cached, so re-layouts and
// re-opens of the same doc never re-decode.

// audioPeaksResolution is the fixed bucket count computed per FILE. Segments
// slice whatever range they need out of these buckets, so segment width and
// trims never trigger a re-decode.
export const audioPeaksResolution = 4000;

// computeAudioPeaks folds one channel's samples into per-bucket max-abs
// peaks, accumulating into `peaks` so multi-channel files take the loudest
// channel per bucket (the display peak a meter would show). Buckets split
// the file evenly with no overlap; the last absorbs the rounding remainder,
// and a file shorter than the resolution keeps every bucket non-empty by
// sharing edge samples (harmless for a peak display).
export function computeAudioPeaks(peaks: Float32Array, samples: Float32Array): Float32Array {
  const buckets = peaks.length;
  if (buckets <= 0) return peaks;
  const total = samples.length;
  if (total === 0) return peaks;
  for (let bucket = 0; bucket < buckets; bucket++) {
    const start = Math.floor((bucket * total) / buckets);
    const rawEnd = bucket === buckets - 1 ? total : Math.floor(((bucket + 1) * total) / buckets);
    const end = Math.min(total, Math.max(rawEnd, start + 1));
    let peak = 0;
    for (let index = start; index < end; index++) {
      const magnitude = samples[index] < 0 ? -samples[index] : samples[index];
      if (magnitude > peak) peak = magnitude;
    }
    if (peak > peaks[bucket]) peaks[bucket] = peak;
  }
  return peaks;
}

// sliceAudioPeaks renders one source range [inSeconds, outSeconds) of a
// measured file as `bars` max-abs values (0..1) — a segment's waveform
// reads its own span, whatever the file's full length. Ranges outside the
// file clamp; a file without duration yields no bars.
export function sliceAudioPeaks(peaks: Float32Array, durationSeconds: number, inSeconds: number, outSeconds: number, bars: number): number[] {
  if (bars <= 0 || peaks.length === 0 || !(durationSeconds > 0)) return [];
  const startFraction = Math.min(1, Math.max(0, inSeconds / durationSeconds));
  const endFraction = Math.min(1, Math.max(startFraction, outSeconds / durationSeconds));
  const values: number[] = [];
  for (let bar = 0; bar < bars; bar++) {
    const barStart = startFraction + ((endFraction - startFraction) * bar) / bars;
    const barEnd = startFraction + ((endFraction - startFraction) * (bar + 1)) / bars;
    let firstBucket = Math.floor(barStart * peaks.length);
    let lastBucket = Math.ceil(barEnd * peaks.length);
    if (lastBucket <= firstBucket) lastBucket = firstBucket + 1;
    firstBucket = Math.max(0, firstBucket);
    lastBucket = Math.min(peaks.length, lastBucket);
    let peak = 0;
    for (let bucket = firstBucket; bucket < lastBucket; bucket++) {
      if (peaks[bucket] > peak) peak = peaks[bucket];
    }
    values.push(peak);
  }
  return values;
}

// waveformPath is the bars' SVG path in a `bars × 100` viewBox — each bar a
// vertical stroke mirrored around the middle line, stretched by
// preserveAspectRatio="none" to whatever width the segment renders at. A
// 2-unit floor keeps silence visible as a stub at the middle line.
export function waveformPath(bars: number[]): string {
  const parts: string[] = [];
  for (let bar = 0; bar < bars.length; bar++) {
    const height = Math.max(2, Math.min(1, Math.abs(bars[bar])) * 98);
    const half = height / 2;
    parts.push(`M${bar + 0.5} ${50 - half}V${50 + half}`);
  }
  return parts.join('');
}

export type AudioPeaksEntry = {
  peaks: Float32Array;
  durationSeconds: number;
};

// One shared AudioContext across every decode — constructing one per segment
// is wasteful, and WebKit caps live contexts.
let sharedContext: AudioContext | null = null;
function decodeContext(): AudioContext | null {
  if (sharedContext) return sharedContext;
  try {
    const constructor = window.AudioContext || (window as unknown as {webkitAudioContext?: typeof AudioContext}).webkitAudioContext;
    sharedContext = constructor ? new constructor() : null;
  } catch {
    sharedContext = null;
  }
  return sharedContext;
}

const peaksCache = new Map<string, AudioPeaksEntry>();
const peaksInFlight = new Map<string, Promise<AudioPeaksEntry | null>>();

// loadAudioPeaks fetches and decodes one artifact URL into cached peaks.
// Failures (missing file, an undecodable container) resolve null and are not
// cached — a re-mount retries, and the segment renders without a waveform
// either way.
export function loadAudioPeaks(url: string): Promise<AudioPeaksEntry | null> {
  if (!url) return Promise.resolve(null);
  const cached = peaksCache.get(url);
  if (cached) return Promise.resolve(cached);
  let inFlight = peaksInFlight.get(url);
  if (!inFlight) {
    const pending = (async () => {
      try {
        const response = await fetch(url);
        if (!response.ok) return null;
        const buffer = await response.arrayBuffer();
        const context = decodeContext();
        if (!context) return null;
        const decoded = await context.decodeAudioData(buffer);
        if (!(decoded.duration > 0)) return null;
        const peaks = new Float32Array(audioPeaksResolution);
        for (let channel = 0; channel < decoded.numberOfChannels; channel++) {
          computeAudioPeaks(peaks, decoded.getChannelData(channel));
        }
        const entry: AudioPeaksEntry = {peaks, durationSeconds: decoded.duration};
        peaksCache.set(url, entry);
        return entry;
      } catch {
        return null;
      }
    })();
    // The dedupe entry lives only while the decode runs; the settled value
    // is in the cache (or the failure wasn't), so late callers re-enter.
    void pending.then(() => {
      if (peaksInFlight.get(url) === pending) peaksInFlight.delete(url);
    });
    peaksInFlight.set(url, pending);
    inFlight = pending;
  }
  return inFlight;
}
