import {
  audioPeaksResolution,
  computeAudioPeaks,
  sliceAudioPeaks,
  waveformPath,
} from '../src/editor/audioPeaks';

function assert(value: boolean, message: string): void {if (!value) throw new Error(message);}

function assertNear(actual: number, expected: number, epsilon: number, message: string): void {
  assert(Math.abs(actual - expected) <= epsilon, `${message}: ${actual} vs ${expected}`);
}

// A deterministic 10-bucket signal: bucket i peaks at (i + 1) / 10.
function rampedPeaks(): Float32Array {
  const peaks = new Float32Array(10);
  for (let bucket = 0; bucket < 10; bucket++) peaks[bucket] = (bucket + 1) / 10;
  return peaks;
}

// computeAudioPeaks splits the file evenly (the last bucket absorbs the
// rounding remainder) and folds channels by keeping the loudest magnitude
// per bucket.
{
  const peaks = new Float32Array(4);
  computeAudioPeaks(peaks, new Float32Array([0.1, -0.5, 0.2, -0.8, 0.3]));
  // 5 samples over 4 buckets: [0.1] / [-0.5] / [0.2] / [-0.8, 0.3].
  assertNear(peaks[0], 0.1, 1e-6, 'bucket 0 holds the first sample alone');
  assertNear(peaks[1], 0.5, 1e-6, 'bucket 1');
  assertNear(peaks[2], 0.2, 1e-6, 'bucket 2');
  assertNear(peaks[3], 0.8, 1e-6, 'bucket 3 absorbs the remainder tail');
  assert(peaks.every((peak) => peak > 0), 'no bucket empties on a sub-sample-per-bucket file');
}
{
  // Channel folding: the second channel's louder samples win.
  const peaks = new Float32Array(2);
  computeAudioPeaks(peaks, new Float32Array([0.2, 0.2, 0.2, 0.2]));
  computeAudioPeaks(peaks, new Float32Array([0.1, 0.1, 0.9, 0.1]));
  assertNear(peaks[0], 0.2, 1e-6, 'channel 0 wins bucket 0');
  assertNear(peaks[1], 0.9, 1e-6, 'channel 1 wins bucket 1');
  assert(computeAudioPeaks(new Float32Array(0), new Float32Array([1])).length === 0, 'a zero-bucket peaks array is a no-op');
  assert(computeAudioPeaks(new Float32Array(3), new Float32Array(0)).every((peak) => peak === 0), 'an empty sample buffer leaves the peaks at zero');
}

// sliceAudioPeaks extracts the requested source range, clamped to the file.
{
  const peaks = rampedPeaks(); // 10 buckets over a nominal 10s file
  const whole = sliceAudioPeaks(peaks, 10, 0, 10, 10);
  assertNear(whole[0], 0.1, 0.2, 'the first bar reads the first bucket');
  assertNear(whole[9], 1.0, 0.2, 'the last bar reads the last bucket');
  assert(whole.length === 10, 'one bar per requested bar');

  const middle = sliceAudioPeaks(peaks, 10, 4, 6, 2);
  assertNear(middle[0], 0.5, 0.2, 'the 4–6s range starts at bucket 4');
  assertNear(middle[1], 0.6, 0.2, 'and its second bar covers bucket 5');

  // A range beyond the file clamps to the file's end — a ref whose measured
  // duration overshoots the decoded audio still renders.
  const clamped = sliceAudioPeaks(peaks, 10, 9, 20, 1);
  assertNear(clamped[0], 1.0, 0.2, 'an overhanging range clamps to the last bucket');

  // Aggregation is max, not mean — one loud sample marks its bar.
  const peaks2 = new Float32Array([0.01, 0.9]);
  assertNear(sliceAudioPeaks(peaks2, 2, 0, 2, 1)[0], 0.9, 1e-6, 'the bar peaks at its loudest bucket');
}
{
  assert(sliceAudioPeaks(new Float32Array(10), 0, 0, 1, 8).length === 0, 'no duration, no bars');
  assert(sliceAudioPeaks(new Float32Array(0), 10, 0, 10, 8).length === 0, 'no peaks, no bars');
  assert(sliceAudioPeaks(rampedPeaks(), 10, 0, 10, 0).length === 0, 'no bars requested, no bars');
  const reversed = sliceAudioPeaks(rampedPeaks(), 10, 7, 3, 4);
  assert(reversed.every((bar) => bar > 0), 'an inverted range still renders its clamped span');
}

// waveformPath mirrors each bar around the middle line with a floor so
// silence stays visible as a stub.
{
  const path = waveformPath([0, 1, 0.5]);
  assert(path === 'M0.5 49V51M1.5 1V99M2.5 25.5V74.5', `three bars, mirrored: ${path}`);
  assert(waveformPath([]) === '', 'no bars, no path');
  const overshoot = waveformPath([2]);
  assert(overshoot === 'M0.5 1V99', 'a bar above full scale clamps to full height');
}

// The resolution constant is the decode-side contract the loader relies on.
assert(audioPeaksResolution === 4000, 'the per-file bucket count stays at 4000');

console.log('audioPeaks assertions passed');
