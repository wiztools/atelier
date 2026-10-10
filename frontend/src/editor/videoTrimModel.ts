import {VideoReframeSource} from './videoReframe';

// The trim tool's data model. The SUBMISSION contract mirrors the Go side:
// kept segments on the source timeline. The EDITOR works with cut markers —
// the reframe idiom — so the draft below is a pure editing affordance:
// interior cut times, each carrying the kept/removed state of the region that
// FOLLOWS it (the head region's state rides on the draft itself). Converting
// in both directions is lossless, so a restored session round-trips.

export type VideoTrimSegment = {startSeconds: number; endSeconds: number};

export type VideoTrimParams = {
  version: 1;
  source: VideoReframeSource;
  segments: VideoTrimSegment[];
};

export type VideoTrimCut = {
  timeSeconds: number;
  removeNext: boolean;
};

export type VideoTrimDraft = {
  headRemoved: boolean;
  cuts: VideoTrimCut[];
};

const timePrecision = 1000;
const cutEpsilon = 1 / timePrecision;
export const maxVideoTrimRegions = 50;

function clamp(value: number, min: number, max: number): number {
  if (max < min) return min;
  return Math.min(max, Math.max(min, value));
}

function roundTime(value: number): number {
  return Math.round(value * timePrecision) / timePrecision;
}

function fail(message: string): never {
  throw new Error(`Invalid video trim params: ${message}`);
}

function finiteNumber(value: unknown, name: string): number {
  if (typeof value !== 'number' || !Number.isFinite(value)) fail(`${name} must be finite`);
  return value;
}

function source(sourceLike: unknown): VideoReframeSource {
  if (!sourceLike || typeof sourceLike !== 'object') fail('source must be an object');
  const record = sourceLike as Record<string, unknown>;
  const width = finiteNumber(record.width, 'source.width');
  const height = finiteNumber(record.height, 'source.height');
  const durationSeconds = finiteNumber(record.durationSeconds, 'source.durationSeconds');
  if (width < 2 || height < 2) fail('source dimensions must be at least 2');
  if (durationSeconds <= 0) fail('source.durationSeconds must be positive');
  return {width, height, durationSeconds};
}

// validateVideoTrimParams is the frontend mirror of Go's validateVideoTrim:
// chronological non-overlapping segments inside the source duration.
export function validateVideoTrimParams(params: unknown): VideoTrimParams {
  if (!params || typeof params !== 'object') fail('params must be an object');
  const record = params as Record<string, unknown>;
  if (record.version !== 1) fail('version must be 1');
  const validSource = source(record.source);
  if (!Array.isArray(record.segments) || record.segments.length < 1 || record.segments.length > maxVideoTrimRegions) {
    fail(`segments must contain 1 to ${maxVideoTrimRegions} entries`);
  }
  const parsed = record.segments.map((value, index) => {
    if (!value || typeof value !== 'object') fail(`segments[${index}] must be an object`);
    const segment = value as Record<string, unknown>;
    return {
      startSeconds: finiteNumber(segment.startSeconds, `segments[${index}].startSeconds`),
      endSeconds: finiteNumber(segment.endSeconds, `segments[${index}].endSeconds`),
    };
  }).sort((a, b) => a.startSeconds - b.startSeconds);
  parsed.forEach((segment, index) => {
    if (segment.startSeconds < 0 || segment.endSeconds > validSource.durationSeconds || segment.startSeconds >= segment.endSeconds) {
      fail(`segments[${index}] must be a non-empty range inside the source duration`);
    }
    if (index > 0 && segment.startSeconds - parsed[index - 1].endSeconds < cutEpsilon) {
      fail('segments must not overlap or touch');
    }
  });
  return {version: 1, source: validSource, segments: parsed};
}

export function defaultVideoTrimDraft(): VideoTrimDraft {
  return {headRemoved: false, cuts: []};
}

export function cloneDraft(draft: VideoTrimDraft): VideoTrimDraft {
  return {
    headRemoved: draft.headRemoved,
    cuts: draft.cuts.map((cut) => ({timeSeconds: cut.timeSeconds, removeNext: cut.removeNext})),
  };
}

// regionsOf lays the timeline out: one entry per region, head first.
export function trimRegionBounds(draft: VideoTrimDraft, duration: number): {start: number; end: number; removed: boolean}[] {
  const bounds: {start: number; end: number; removed: boolean}[] = [];
  let previous = 0;
  let removed = draft.headRemoved;
  for (const cut of draft.cuts) {
    bounds.push({start: previous, end: cut.timeSeconds, removed});
    previous = cut.timeSeconds;
    removed = cut.removeNext;
  }
  bounds.push({start: previous, end: duration, removed});
  return bounds;
}

// segmentsFromDraft is the submission view: the kept regions. Adjacent kept
// regions (a cut whose two sides are both kept — possible after toggles)
// merge, since the contract refuses touching segments.
export function segmentsFromDraft(draft: VideoTrimDraft, duration: number): VideoTrimSegment[] {
  const segments: VideoTrimSegment[] = [];
  for (const region of trimRegionBounds(draft, duration)) {
    if (region.removed || region.end - region.start <= 0) continue;
    const previous = segments[segments.length - 1];
    if (previous && Math.abs(previous.endSeconds - region.start) < cutEpsilon) {
      previous.endSeconds = region.end;
    } else {
      segments.push({startSeconds: region.start, endSeconds: region.end});
    }
  }
  return segments;
}

export function draftFromSegments(segments: VideoTrimSegment[], duration: number): VideoTrimDraft {
  const draft = defaultVideoTrimDraft();
  // Walking the kept segments: a segment not starting at zero means removed
  // footage before it (the cut closing that gap opens the kept range, so it
  // carries removeNext=false), and a segment not reaching the end means
  // removed footage after it (its closing cut carries removeNext=true).
  draft.headRemoved = !(segments.length > 0 && segments[0].startSeconds <= cutEpsilon);
  const cuts: VideoTrimCut[] = [];
  for (const segment of segments) {
    if (segment.startSeconds > cutEpsilon) cuts.push({timeSeconds: segment.startSeconds, removeNext: false});
    if (segment.endSeconds < duration - cutEpsilon) cuts.push({timeSeconds: segment.endSeconds, removeNext: true});
  }
  draft.cuts = cuts;
  return normalizeDraft(draft, duration);
}

// normalizeDraft sorts the cuts, drops degenerate ones, and clamps to the
// clip's interior.
function normalizeDraft(draft: VideoTrimDraft, duration: number): VideoTrimDraft {
  const cuts = draft.cuts
    .map((cut) => ({timeSeconds: roundTime(clamp(cut.timeSeconds, 0, duration)), removeNext: cut.removeNext}))
    .filter((cut) => cut.timeSeconds > cutEpsilon && cut.timeSeconds < duration - cutEpsilon)
    .sort((a, b) => a.timeSeconds - b.timeSeconds);
  const merged: VideoTrimCut[] = [];
  for (const cut of cuts) {
    const previous = merged[merged.length - 1];
    if (previous && cut.timeSeconds - previous.timeSeconds < cutEpsilon) {
      previous.removeNext = previous.removeNext || cut.removeNext;
      continue;
    }
    merged.push({...cut});
  }
  return {headRemoved: draft.headRemoved, cuts: merged};
}

export function sameTrimDraft(a: VideoTrimDraft, b: VideoTrimDraft): boolean {
  return JSON.stringify(a) === JSON.stringify(b);
}

// addCut splits the region containing timeSeconds; the new cut inherits that
// region's removed state so a split never changes what renders.
export function addCut(draft: VideoTrimDraft, duration: number, timeSeconds: number): VideoTrimDraft {
  const time = roundTime(clamp(timeSeconds, 0, duration));
  if (time <= cutEpsilon || time >= duration - cutEpsilon) {
    throw new Error('a cut must sit strictly inside the clip');
  }
  if (draft.cuts.length >= maxVideoTrimRegions - 1) {
    throw new Error(`Video trim supports at most ${maxVideoTrimRegions} regions`);
  }
  const next = cloneDraft(draft);
  if (next.cuts.some((cut) => Math.abs(cut.timeSeconds - time) < cutEpsilon)) {
    throw new Error('there is already a cut at this time');
  }
  const region = trimRegionBounds(draft, duration).find((bounds) => time > bounds.start + cutEpsilon && time < bounds.end - cutEpsilon);
  if (!region) throw new Error('a cut must sit strictly inside a region');
  next.cuts.push({timeSeconds: time, removeNext: region.removed});
  return normalizeDraft(next, duration);
}

export function deleteCut(draft: VideoTrimDraft, index: number): VideoTrimDraft {
  if (index < 0 || index >= draft.cuts.length) return draft;
  const next = cloneDraft(draft);
  const deleted = next.cuts[index];
  const before = index > 0 ? next.cuts[index - 1] : null;
  // Merging two regions keeps footage if either half was kept: removing must
  // never silently widen into kept material.
  if (before) before.removeNext = before.removeNext || deleted.removeNext;
  else next.headRemoved = next.headRemoved || deleted.removeNext;
  next.cuts.splice(index, 1);
  // Cut times are already valid; only the sort/merge pass matters here.
  return normalizeDraft(next, Number.POSITIVE_INFINITY);
}

// moveCut retimes one cut between its neighbours.
export function moveCut(draft: VideoTrimDraft, index: number, timeSeconds: number): VideoTrimDraft {
  if (index < 0 || index >= draft.cuts.length) return draft;
  const next = cloneDraft(draft);
  const previous = index > 0 ? next.cuts[index - 1].timeSeconds : 0;
  const following = index < next.cuts.length - 1 ? next.cuts[index + 1].timeSeconds : Number.POSITIVE_INFINITY;
  next.cuts[index].timeSeconds = roundTime(clamp(timeSeconds, previous + cutEpsilon, following - cutEpsilon));
  return normalizeDraft(next, Number.POSITIVE_INFINITY);
}

export function toggleRegionRemoved(draft: VideoTrimDraft, index: number): VideoTrimDraft {
  const next = cloneDraft(draft);
  if (index === 0) next.headRemoved = !next.headRemoved;
  else if (index > 0 && index <= next.cuts.length) next.cuts[index - 1].removeNext = !next.cuts[index - 1].removeNext;
  return next;
}

// regionIndexAt maps a playhead position onto its region.
export function regionIndexAt(draft: VideoTrimDraft, duration: number, timeSeconds: number): number {
  const time = clamp(timeSeconds, 0, duration);
  const bounds = trimRegionBounds(draft, duration);
  for (let index = 0; index < bounds.length; index++) {
    if (time < bounds[index].end - cutEpsilon || index === bounds.length - 1) return index;
  }
  return bounds.length - 1;
}

export function keptSeconds(draft: VideoTrimDraft, duration: number): number {
  return segmentsFromDraft(draft, duration).reduce((total, segment) => total + segment.endSeconds - segment.startSeconds, 0);
}

// skipRangesForPlayback is the preview's jump table: the removed regions.
export function skipRangesForPlayback(draft: VideoTrimDraft, duration: number): {start: number; end: number}[] {
  return trimRegionBounds(draft, duration)
    .filter((region) => region.removed && region.end - region.start > 0)
    .map((region) => ({start: region.start, end: region.end}));
}
