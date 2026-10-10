import {
  VideoTrimSegment,
  addCut, defaultVideoTrimDraft, deleteCut, draftFromSegments, keptSeconds, moveCut,
  regionIndexAt, sameTrimDraft, segmentsFromDraft, skipRangesForPlayback,
  toggleRegionRemoved, trimRegionBounds, validateVideoTrimParams,
} from '../src/editor/videoTrimModel';

function assert(value: boolean, message: string): void {if (!value) throw new Error(message);}
function segments(starts: number[], ends: number[]): VideoTrimSegment[] {
  return starts.map((start, index) => ({startSeconds: start, endSeconds: ends[index]}));
}
function rejects(fn: () => void, message: string): void {
  try {
    fn();
  } catch (error) {
    assert((error as Error).message.includes(message), `wrong error: ${(error as Error).message}`);
    return;
  }
  throw new Error(`expected refusal: ${message}`);
}

const duration = 8;

// The submission contract mirrors the Go validation.
validateVideoTrimParams({version: 1, source: {width: 1920, height: 1080, durationSeconds: duration}, segments: segments([0, 5], [2, 8])});
rejects(() => validateVideoTrimParams({version: 2, source: {width: 1920, height: 1080, durationSeconds: duration}, segments: segments([0], [1])}), 'version');
rejects(() => validateVideoTrimParams({version: 1, source: {width: 1920, height: 1080, durationSeconds: duration}, segments: []}), '1 to 50');
rejects(() => validateVideoTrimParams({version: 1, source: {width: 1920, height: 1080, durationSeconds: duration}, segments: segments([0, 2], [2.5, 8])}), 'overlap or touch');
rejects(() => validateVideoTrimParams({version: 1, source: {width: 1920, height: 1080, durationSeconds: duration}, segments: segments([0], [9])}), 'inside the source');
rejects(() => validateVideoTrimParams({version: 1, source: {width: 1920, height: 1080, durationSeconds: duration}, segments: segments([3], [2])}), 'non-empty');

// The empty draft keeps everything; segments round-trip through the draft.
const full = defaultVideoTrimDraft();
assert(segmentsFromDraft(full, duration).length === 1 && keptSeconds(full, duration) === duration, 'empty draft keeps the whole clip');
assert(skipRangesForPlayback(full, duration).length === 0, 'empty draft has no skip ranges');

// Trim tail: remove the region after a cut at 2.
const tail = toggleRegionRemoved(addCut(full, duration, 2), 1);
assert(sameTrimDraft(tail, draftFromSegments(validateVideoTrimParams({version: 1, source: {width: 1920, height: 1080, durationSeconds: duration}, segments: segments([0], [2])}).segments, duration)),
  'tail trim draft must round-trip through segments');
assert(keptSeconds(tail, duration) === 2, 'tail trim keeps 2s');

// Delete in between: tail-trim to 2, cut at 5 inside the removed range, then
// restore the [5,8] tail — kept [0,2] and [5,8].
const middle = toggleRegionRemoved(addCut(tail, duration, 5), 2);
assert(sameTrimDraft(middle, draftFromSegments(segments([0, 5], [2, 8]), duration)), 'mid delete draft must round-trip');
const middleSkip = skipRangesForPlayback(middle, duration);
assert(middleSkip.length === 1 && middleSkip[0].start === 2 && middleSkip[0].end === 5, 'removed range is the skip range');

// Deleting a cut merges the adjacent regions; removed footage wins, so a
// deleted boundary can never resurrect cut material.
const headGone = toggleRegionRemoved(middle, 0);
assert(headGone.headRemoved && headGone.cuts.length === 2, 'head removal rides the draft');
const merged = deleteCut(headGone, 0);
assert(merged.headRemoved && merged.cuts.length === 1 && !merged.cuts[0].removeNext, 'merging a removed head with a kept tail keeps only the tail');
assert(keptSeconds(merged, duration) === 3, 'merged draft keeps [5,8]');
const mergedFromKept = deleteCut(middle, 0);
assert(mergedFromKept.headRemoved, 'deleting the head cut keeps only footage kept on both sides');

// Cut insertion inherits the split region's removed state; a split never
// changes what renders.
const removedRegion = toggleRegionRemoved(addCut(full, duration, 2), 1);
const split = addCut(removedRegion, duration, 4);
const splitSegments = segmentsFromDraft(split, duration);
assert(splitSegments.length === 1 && splitSegments[0].endSeconds === 2, 'splitting a removed region adds no kept footage');

// Retime clamps between neighbours; region lookup follows the playhead.
const bounds = trimRegionBounds(middle, duration);
assert(bounds.length === 3 && bounds[1].start === 2 && bounds[1].end === 5 && bounds[1].removed, 'region bounds walk the cuts');
assert(regionIndexAt(middle, duration, 0) === 0 && regionIndexAt(middle, duration, 3) === 1 && regionIndexAt(middle, duration, 8) === 2, 'playhead maps onto its region');
const retimed = moveCut(middle, 0, 9);
assert(retimed.cuts[0].timeSeconds < retimed.cuts[1].timeSeconds, 'retiming clamps to the next cut');
assert(sameTrimDraft(moveCut(middle, -1, 3), middle), 'unknown cut index is a no-op');

// Trim-head draft round-trips too.
const headTrim = draftFromSegments(segments([3], [8]), duration);
assert(headTrim.headRemoved && headTrim.cuts.length === 1 && !headTrim.cuts[0].removeNext, 'head trim cuts at 3');
assert(keptSeconds(headTrim, duration) === 5, 'head trim keeps 5s');

// The 49-cut cap: one more cut must refuse without corrupting the draft.
let capped = defaultVideoTrimDraft();
for (let i = 1; i <= 49; i++) capped = addCut(capped, 60, i);
assert(capped.cuts.length === 49 && trimRegionBounds(capped, 60).length === 50, '49 cuts partition 50 regions');
rejects(() => addCut(capped, 60, 1.5), 'at most 50');
assert(capped.cuts.length === 49, 'a refused cut must not corrupt the draft');

console.log('video trim assertions passed');
