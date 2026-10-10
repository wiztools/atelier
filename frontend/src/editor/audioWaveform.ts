// Waveform support for the Audio Editor: peak extraction from decoded audio
// and canvas rendering. Peaks are computed once per source (the editor lifts
// them so the stage and the timeline strip share one decode) and normalized
// to 0..1 so a quiet recording still draws visible bars.

// audioWaveformMaxSeconds bounds the decode: decodeAudioData holds the whole
// clip as PCM (≈20MB per minute, stereo 44.1kHz), so hour-long recordings
// degrade to the striped placeholder instead of spiking memory.
export const audioWaveformMaxSeconds = 600;

// Peak resolution: enough to survive any panel width at 2x device pixels
// without re-decoding on resize.
const peakBucketCount = 1200;

// computeWaveformPeaks fetches and decodes one audio URL into normalized
// peaks (max absolute sample per bucket, mono mixdown). Failures (a container
// WebKit cannot decode, a fetch error, an over-budget clip) resolve to null —
// the UI draws its placeholder and playback is unaffected.
export async function computeWaveformPeaks(url: string, durationSeconds: number): Promise<number[] | null> {
  if (!Number.isFinite(durationSeconds) || durationSeconds <= 0 || durationSeconds > audioWaveformMaxSeconds) return null;
  const AudioContextCtor = window.AudioContext || (window as unknown as {webkitAudioContext?: typeof AudioContext}).webkitAudioContext;
  if (!AudioContextCtor) return null;
  let buffer: AudioBuffer | null = null;
  try {
    const response = await fetch(url);
    if (!response.ok) return null;
    const data = await response.arrayBuffer();
    const context = new AudioContextCtor();
    try {
      buffer = await context.decodeAudioData(data);
    } finally {
      void context.close().catch(() => {});
    }
  } catch {
    return null;
  }
  if (!buffer || buffer.length === 0) return null;
  const channels = Math.min(buffer.numberOfChannels, 2);
  const samples = buffer.getChannelData(0);
  const second = channels > 1 ? buffer.getChannelData(1) : null;
  const bucket = samples.length / peakBucketCount;
  const peaks = new Array<number>(peakBucketCount).fill(0);
  for (let index = 0; index < peakBucketCount; index++) {
    const start = Math.floor(index * bucket);
    const end = Math.min(samples.length, Math.floor((index + 1) * bucket));
    let peak = 0;
    for (let position = start; position < end; position++) {
      let value = Math.abs(samples[position]);
      if (second) value = Math.max(value, Math.abs(second[position]));
      if (value > peak) peak = value;
    }
    peaks[index] = peak;
  }
  let max = 0;
  for (const peak of peaks) max = Math.max(max, peak);
  if (max <= 0) return peaks;
  return peaks.map((peak) => peak / max);
}

// drawWaveformPeaks renders centered bars across the canvas, scaled to the
// device pixel ratio. The bar pitch follows the canvas width (≥3px per bar)
// so any source resolves to distinct bars; a null/empty peak list draws the
// striped placeholder.
export function drawWaveformPeaks(canvas: HTMLCanvasElement, peaks: number[] | null): void {
  const context = canvas.getContext('2d');
  if (!context) return;
  const ratio = window.devicePixelRatio || 1;
  const width = canvas.clientWidth;
  const height = canvas.clientHeight;
  canvas.width = Math.max(1, Math.floor(width * ratio));
  canvas.height = Math.max(1, Math.floor(height * ratio));
  context.setTransform(ratio, 0, 0, ratio, 0, 0);
  context.clearRect(0, 0, width, height);
  if (!peaks || peaks.length === 0 || width <= 0) {
    context.fillStyle = '#202326';
    for (let x = 0; x < width; x += 18) {
      context.fillRect(x, 0, 9, height);
    }
    return;
  }
  const middle = height / 2;
  const barCount = Math.max(1, Math.min(peaks.length, Math.floor(width / 3)));
  const barWidth = width / barCount;
  const stride = peaks.length / barCount;
  context.fillStyle = '#7d8a99';
  for (let index = 0; index < barCount; index++) {
    const start = Math.floor(index * stride);
    const end = Math.min(peaks.length, Math.max(start + 1, Math.floor((index + 1) * stride)));
    let peak = 0;
    for (let position = start; position < end; position++) {
      peak = Math.max(peak, peaks[position]);
    }
    const barHeight = Math.max(0.02, peak) * (height - 4);
    context.fillRect(index * barWidth, middle - barHeight / 2, Math.max(1, barWidth - 1), barHeight);
  }
}
