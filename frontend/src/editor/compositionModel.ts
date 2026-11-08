// Composition document model: the pure logic behind the video composer. The
// editor holds one CompositionDoc and derives everything from the ops below;
// undo/redo is full-doc snapshots, so every op returns a new doc and a no-op
// returns a clone. This module mirrors composition.go's rules (validation
// semantics, clamps, layout math) and stays free of React and the wailsjs
// bindings so test/run-composition-model-tests.cjs can compile it alone,
// like the other editor models.

export type CompositionMediaRefKind = 'video' | 'audio' | 'image';

export type CompositionMediaRef = {
  artifactId: string;
  conversationId?: string;
  path: string;
  mimeType?: string;
  kind: CompositionMediaRefKind;
  // Zero for a still: an image has no source duration — its clip's range
  // is its length.
  durationSeconds: number;
  // The display size — for a still, with any JPEG EXIF orientation applied.
  width?: number;
  height?: number;
  fps?: number;
  hasAudio?: boolean;
};

export type CompositionTransition = {
  kind: string;
  seconds: number;
  audioCrossfade: boolean;
};

export type CompositionOriginalAudio = {
  gainDb: number;
  muted: boolean;
  detached: boolean;
};

export type CompositionClip = {
  id: string;
  ref: CompositionMediaRef;
  inSeconds: number;
  outSeconds: number;
  transitionToNext: CompositionTransition | null;
  audio: CompositionOriginalAudio;
};

export type CompositionDuck = {
  startSeconds: number;
  endSeconds: number;
  gainDb: number;
};

export type CompositionSegment = {
  id: string;
  ref: CompositionMediaRef;
  inSeconds: number;
  outSeconds: number;
  startSeconds: number;
  gainDb: number;
  fadeInSeconds: number;
  fadeOutSeconds: number;
  ducks: CompositionDuck[];
  detachedFromClipId?: string;
};

export type CompositionLane = {
  id: string;
  name: string;
  gainDb: number;
  muted: boolean;
  segments: CompositionSegment[];
};

export type CompositionFit = 'pad' | 'crop' | 'blur';

export type CompositionOutput = {
  width: number;
  height: number;
  fps: number;
  fit: CompositionFit;
};

export type CompositionDoc = {
  schemaVersion: number;
  name: string;
  output: CompositionOutput;
  video: CompositionClip[];
  audioLanes: CompositionLane[];
};

// Mirrors composition.go's constants; keep both sides in step.
export const compositionMaxClips = 50;
export const compositionMaxAudioLanes = 4;
export const compositionEpsilon = 0.001;
export const compositionMinRangeSeconds = 0.01;
export const compositionTransitionMinSeconds = 0.1;
export const compositionTransitionMaxSeconds = 10.0;
export const compositionTransitionDefaultSeconds = 1.0;
export const compositionGainMinDb = -60.0;
export const compositionGainMaxDb = 12.0;
export const compositionMaxFPS = 60.0;
export const compositionDefaultFPS = 30.0;
export const compositionDefaultName = 'Untitled composition';
export const detachedAudioLaneName = 'Detached audio';
// A still lands at Final Cut Pro's default length, resizes freely down to the
// floor, and stops at a sanity ceiling (a still under a full podcast); a
// stills-only sequence's inherited frame fits within the long-edge cap.
export const compositionStillDefaultSeconds = 4.0;
export const compositionStillMinSeconds = 0.1;
export const compositionStillMaxSeconds = 3 * 60 * 60;
export const compositionStillFrameMaxEdge = 1920;

// compositionTransitions is the junction vocabulary — 'dissolve' (the
// composer's name for a plain cross-fade) plus the join tools' curated xfade
// enum minus 'fade' itself (ffmpeg also ships a pixel 'dissolve', a different
// effect, so the doc never stores that name). Mirrors composition.go.
export const compositionTransitions = [
  'dissolve', 'fadeblack', 'fadewhite', 'fadegrays',
  'wipeleft', 'wiperight', 'wipeup', 'wipedown',
  'slideleft', 'slideright', 'slideup', 'slidedown',
  'circlecrop', 'circleopen', 'circleclose', 'rectcrop', 'radial', 'distance',
  'smoothleft', 'smoothright', 'smoothup', 'smoothdown',
  'horzopen', 'horzclose', 'vertopen', 'vertclose',
  'diagtl', 'diagtr', 'diagbl', 'diagbr',
  'hlslice', 'hrslice', 'vuslice', 'vdslice',
  'wipetl', 'wipetr', 'wipebl', 'wipebr',
  'hblur', 'pixelize', 'squeezeh', 'squeezev',
];

// normalizeTransitionKind aliases the raw xfade 'fade' onto 'dissolve' and
// coerces anything foreign (a hand-edited or newer-version document) back to
// the default — the Go validator would refuse the save otherwise.
export function normalizeTransitionKind(kind: string): string {
  if (kind === 'fade') return 'dissolve';
  return compositionTransitions.includes(kind) ? kind : 'dissolve';
}

function finite(...values: number[]): boolean {
  return values.every((value) => Number.isFinite(value));
}

export function clampCompositionGain(gainDb: number): number {
  if (!finite(gainDb)) return 0;
  return Math.min(compositionGainMaxDb, Math.max(compositionGainMinDb, gainDb));
}

function evenDown(value: number): number {
  const whole = Math.floor(value);
  return whole % 2 === 0 ? whole : whole - 1;
}

// newCompositionID mirrors Go's randomID shape: <prefix>_<24 hex chars>.
export function newCompositionID(prefix: string): string {
  const bytes = new Uint8Array(12);
  if (typeof crypto !== 'undefined' && 'getRandomValues' in crypto) {
    crypto.getRandomValues(bytes);
  } else {
    for (let i = 0; i < bytes.length; i++) {
      bytes[i] = Math.floor(Math.random() * 256);
    }
  }
  const hex = Array.from(bytes, (b) => b.toString(16).padStart(2, '0')).join('');
  return `${prefix}_${hex}`;
}

// normalizeCompositionDoc adapts the Go wire shape (omitempty means absent)
// into the model's always-present form. Accepts the generated
// main.CompositionDoc instance or a plain object.
export function normalizeCompositionDoc(wire: any): CompositionDoc {
  const output = wire?.output ?? {};
  const doc: CompositionDoc = {
    schemaVersion: typeof wire?.schemaVersion === 'number' ? wire.schemaVersion : 1,
    name: typeof wire?.name === 'string' && wire.name.trim() ? wire.name : compositionDefaultName,
    output: {
      width: Math.max(0, Math.floor(output.width ?? 0)),
      height: Math.max(0, Math.floor(output.height ?? 0)),
      fps: finite(output.fps) ? output.fps : 0,
      fit: output.fit === 'crop' || output.fit === 'blur' ? output.fit : 'pad',
    },
    video: (Array.isArray(wire?.video) ? wire.video : []).map((clip: any) => ({
      id: clip?.id ?? '',
      ref: normalizeCompositionMediaRef(clip?.ref),
      inSeconds: finite(clip?.inSeconds) ? clip.inSeconds : 0,
      outSeconds: finite(clip?.outSeconds) ? clip.outSeconds : 0,
      transitionToNext: clip?.transitionToNext && clip.transitionToNext.kind !== 'cut'
        ? {
            kind: normalizeTransitionKind(String(clip.transitionToNext.kind)),
            seconds: finite(clip.transitionToNext.seconds) ? clip.transitionToNext.seconds : compositionTransitionDefaultSeconds,
            audioCrossfade: clip.transitionToNext.audioCrossfade !== false,
          }
        : null,
      audio: {
        gainDb: clampCompositionGain(clip?.audio?.gainDb ?? 0),
        muted: clip?.audio?.muted === true,
        detached: clip?.audio?.detached === true,
      },
    })),
    audioLanes: (Array.isArray(wire?.audioLanes) ? wire.audioLanes : []).map((lane: any) => ({
      id: lane?.id ?? '',
      name: typeof lane?.name === 'string' && lane.name.trim() ? lane.name : 'Audio',
      gainDb: clampCompositionGain(lane?.gainDb ?? 0),
      muted: lane?.muted === true,
      segments: (Array.isArray(lane?.segments) ? lane.segments : []).map((segment: any) => ({
        id: segment?.id ?? '',
        ref: normalizeCompositionMediaRef(segment?.ref),
        inSeconds: finite(segment?.inSeconds) ? segment.inSeconds : 0,
        outSeconds: finite(segment?.outSeconds) ? segment.outSeconds : 0,
        startSeconds: finite(segment?.startSeconds) ? segment.startSeconds : 0,
        gainDb: clampCompositionGain(segment?.gainDb ?? 0),
        fadeInSeconds: Math.max(0, finite(segment?.fadeInSeconds) ? segment.fadeInSeconds : 0),
        fadeOutSeconds: Math.max(0, finite(segment?.fadeOutSeconds) ? segment.fadeOutSeconds : 0),
        ducks: (Array.isArray(segment?.ducks) ? segment.ducks : []).map((duck: any) => ({
          startSeconds: finite(duck?.startSeconds) ? duck.startSeconds : 0,
          endSeconds: finite(duck?.endSeconds) ? duck.endSeconds : 0,
          gainDb: clampCompositionGain(duck?.gainDb ?? 0),
        })),
        detachedFromClipId: typeof segment?.detachedFromClipId === 'string' ? segment.detachedFromClipId : undefined,
      })),
    })),
  };
  return doc;
}

// normalizeCompositionMediaRef adapts one wire ref (ResolveCompositionMedia's
// return) into the model's form — the picker's add path.
export function normalizeCompositionMediaRef(ref: any): CompositionMediaRef {
  return {
    artifactId: typeof ref?.artifactId === 'string' ? ref.artifactId : '',
    conversationId: typeof ref?.conversationId === 'string' ? ref.conversationId : undefined,
    path: typeof ref?.path === 'string' ? ref.path : '',
    mimeType: typeof ref?.mimeType === 'string' ? ref.mimeType : undefined,
    kind: ref?.kind === 'audio' ? 'audio' : ref?.kind === 'image' ? 'image' : 'video',
    durationSeconds: finite(ref?.durationSeconds) ? ref.durationSeconds : 0,
    width: typeof ref?.width === 'number' ? ref.width : undefined,
    height: typeof ref?.height === 'number' ? ref.height : undefined,
    fps: typeof ref?.fps === 'number' ? ref.fps : undefined,
    hasAudio: ref?.hasAudio === true,
  };
}

// toWireDoc is the plain-object shape SaveCompositionEdit takes. The model's
// always-present form round-trips directly (a null transition is Go's nil).
export function toWireDoc(doc: CompositionDoc): any {
  return {
    schemaVersion: doc.schemaVersion,
    name: doc.name,
    output: {...doc.output},
    video: doc.video.map((clip) => ({
      id: clip.id,
      ref: {...clip.ref},
      inSeconds: clip.inSeconds,
      outSeconds: clip.outSeconds,
      transitionToNext: clip.transitionToNext
        ? {kind: clip.transitionToNext.kind, seconds: clip.transitionToNext.seconds, audioCrossfade: clip.transitionToNext.audioCrossfade}
        : null,
      audio: {...clip.audio},
    })),
    audioLanes: doc.audioLanes.map((lane) => ({
      id: lane.id,
      name: lane.name,
      gainDb: lane.gainDb,
      muted: lane.muted,
      segments: lane.segments.map((segment) => ({
        id: segment.id,
        ref: {...segment.ref},
        inSeconds: segment.inSeconds,
        outSeconds: segment.outSeconds,
        startSeconds: segment.startSeconds,
        gainDb: segment.gainDb,
        fadeInSeconds: segment.fadeInSeconds,
        fadeOutSeconds: segment.fadeOutSeconds,
        ducks: segment.ducks.map((duck) => ({...duck})),
        ...(segment.detachedFromClipId ? {detachedFromClipId: segment.detachedFromClipId} : {}),
      })),
    })),
  };
}

export function cloneCompositionDoc(doc: CompositionDoc): CompositionDoc {
  return normalizeCompositionDoc(toWireDoc(doc));
}

export function sameCompositionDoc(a: CompositionDoc, b: CompositionDoc): boolean {
  return JSON.stringify(toWireDoc(a)) === JSON.stringify(toWireDoc(b));
}

// ---- Preview freshness ----
// A preview is a finished render, so the stage can lag the timeline. Each
// render request carries the key of what it was asked to show — the
// document's render-relevant content, then the render options — and Go echoes
// it on every op transition (CompositionRenderOperation.previewKey, opaque
// there), so the stage judges the render it actually SHOWS against the
// timeline as it is now, even a render that outlived a closed editor.

export type CompositionPreviewOptions = {
  startSeconds?: number | null;
  endSeconds?: number | null;
  soloLaneId?: string;
};

// compositionPreviewRange is the range a render request resolves to — Go's
// normalizeCompositionRange over the same total (the sequence, or the audio
// extent for an audio-only document) — with one more equivalence for the
// key: a range covering the whole timeline renders the same file as no range.
// Null means the full timeline.
export function compositionPreviewRange(doc: CompositionDoc, startSeconds?: number | null, endSeconds?: number | null): {start: number; end: number} | null {
  const start = startSeconds !== undefined && startSeconds !== null && finite(startSeconds) ? startSeconds : null;
  const end = endSeconds !== undefined && endSeconds !== null && finite(endSeconds) ? endSeconds : null;
  if (start === null && end === null) return null;
  const total = compositionIsAudioOnly(doc) ? compositionAudioExtent(doc) : compositionSequenceDuration(doc);
  const from = start !== null && start > 0 ? start : 0;
  const to = end !== null && end < total ? end : total;
  if (from >= to - compositionMinRangeSeconds) return null;
  if (from <= 0 && to >= total) return null;
  return {start: from, end: to};
}

// previewFingerprint is cyrb53, a fast 53-bit string hash: the key rides
// every progress event, so it stays short, and a collision only costs one
// missed staleness note.
function previewFingerprint(text: string): string {
  let h1 = 0xdeadbeef;
  let h2 = 0x41c6ce57;
  for (let i = 0; i < text.length; i++) {
    const ch = text.charCodeAt(i);
    h1 = Math.imul(h1 ^ ch, 2654435761);
    h2 = Math.imul(h2 ^ ch, 1597334677);
  }
  h1 = Math.imul(h1 ^ (h1 >>> 16), 2246822507) ^ Math.imul(h2 ^ (h2 >>> 13), 3266489909);
  h2 = Math.imul(h2 ^ (h2 >>> 16), 2246822507) ^ Math.imul(h1 ^ (h1 >>> 13), 3266489909);
  return (4294967296 * (2097151 & h2) + (h1 >>> 0)).toString(36);
}

// compositionPreviewKey is "<timeline>.<options>". Names never reach the
// renderer, so the composition's and the lanes' names are blanked — a rename
// leaves the preview current. The options part is the resolved range (as
// compositionPreviewRange reads it) and the solo lane, which the caller
// passes only when that lane still exists.
export function compositionPreviewKey(doc: CompositionDoc, options: CompositionPreviewOptions = {}): string {
  const wire = toWireDoc(doc);
  wire.name = '';
  for (const lane of wire.audioLanes) lane.name = '';
  const range = compositionPreviewRange(doc, options.startSeconds, options.endSeconds);
  const settings = {range: range ? [range.start, range.end] : null, solo: options.soloLaneId ?? ''};
  return `${previewFingerprint(JSON.stringify(wire))}.${previewFingerprint(JSON.stringify(settings))}`;
}

// 'current': the render shows the timeline as it is. 'timeline': an edit
// landed after it. 'settings': only the range or solo lane differs.
// 'unknown': the render carries no key, so nothing can vouch for it.
export type CompositionPreviewFreshness = 'current' | 'timeline' | 'settings' | 'unknown';

export function compositionPreviewFreshness(renderedKey: string | null | undefined, currentKey: string): CompositionPreviewFreshness {
  if (!renderedKey) return 'unknown';
  if (renderedKey === currentKey) return 'current';
  return renderedKey.split('.')[0] === currentKey.split('.')[0] ? 'settings' : 'timeline';
}

export function clipDurationSeconds(clip: CompositionClip): number {
  return Math.max(0, clip.outSeconds - clip.inSeconds);
}

// isStillClip reports a video-lane entry holding a still image: no source to
// trim and no sound — its range is only ever [0, length].
export function isStillClip(clip: CompositionClip): boolean {
  return clip.ref.kind === 'image';
}

// clampStillLength pulls a still's length into [min, max] — Go's
// compositionNormalizeStill. Null for a non-finite request: there is no
// length to recover from NaN.
export function clampStillLength(seconds: number): number | null {
  if (!finite(seconds)) return null;
  return Math.min(compositionStillMaxSeconds, Math.max(compositionStillMinSeconds, seconds));
}

export function segmentDurationSeconds(segment: CompositionSegment): number {
  return Math.max(0, segment.outSeconds - segment.inSeconds);
}

// compositionClipLayout is the shared timeline math — Go's twin: each clip's
// timeline start plus the sequence duration, a dissolve overlapping its two
// sides by its length.
export function compositionClipLayout(doc: CompositionDoc): {starts: number[]; duration: number} {
  const starts: number[] = [];
  let elapsed = 0;
  doc.video.forEach((clip, index) => {
    starts.push(elapsed);
    elapsed += clipDurationSeconds(clip);
    if (clip.transitionToNext && index < doc.video.length - 1) {
      elapsed -= clip.transitionToNext.seconds;
    }
  });
  return {starts, duration: Math.max(0, elapsed)};
}

export function compositionSequenceDuration(doc: CompositionDoc): number {
  return compositionClipLayout(doc).duration;
}

// compositionIsAudioOnly mirrors the renderer's content rule (plan.AudioOnly
// = len(doc.Video) == 0, composition_render.go): a document with no video
// clips or stills renders as an M4A mix, so the editor presents it
// audio-first. A brand-new empty draft has no content to decide with, so the
// editor layers the opening intent (＋ Audio) on top of this.
export function compositionIsAudioOnly(doc: CompositionDoc): boolean {
  return doc.video.length === 0;
}

export function laneExtent(lane: CompositionLane): number {
  return lane.segments.reduce((end, segment) => Math.max(end, segment.startSeconds + segmentDurationSeconds(segment)), 0);
}

// compositionAudioExtent is where the last audio ends; audioOverhangSeconds
// is the part beyond the video sequence (AU-11's visibly-identified tail).
export function compositionAudioExtent(doc: CompositionDoc): number {
  return doc.audioLanes.reduce((end, lane) => Math.max(end, laneExtent(lane)), 0);
}

export function audioOverhangSeconds(doc: CompositionDoc): number {
  return Math.max(0, compositionAudioExtent(doc) - compositionSequenceDuration(doc));
}

// transitionBounds is what a junction's dissolve may be: at least the minimum,
// at most the shorter neighboring clip's half (capped by the global max).
// Null when there is no next clip or the neighbors cannot hold even the
// minimum — the TR-05 explanation the inspector renders.
export function transitionBounds(doc: CompositionDoc, clipIndex: number): {min: number; max: number} | null {
  const clip = doc.video[clipIndex];
  const next = doc.video[clipIndex + 1];
  if (!clip || !next) return null;
  const upper = Math.min(compositionTransitionMaxSeconds, Math.min(clipDurationSeconds(clip), clipDurationSeconds(next)) / 2);
  if (upper < compositionTransitionMinSeconds) return null;
  return {min: compositionTransitionMinSeconds, max: upper};
}

// clampTransition pulls a junction's transition into its bounds, in place on
// the cloned clip array. Returns the transition (or null for a cut).
function clampTransition(clip: CompositionClip, next: CompositionClip): CompositionTransition | null {
  if (!clip.transitionToNext) {
    return null;
  }
  const upper = Math.min(compositionTransitionMaxSeconds, Math.min(clipDurationSeconds(clip), clipDurationSeconds(next)) / 2);
  if (upper < compositionTransitionMinSeconds) {
    return null;
  }
  const seconds = Math.min(upper, Math.max(compositionTransitionMinSeconds, finite(clip.transitionToNext.seconds) && clip.transitionToNext.seconds > 0 ? clip.transitionToNext.seconds : compositionTransitionDefaultSeconds));
  return {kind: normalizeTransitionKind(clip.transitionToNext.kind), seconds, audioCrossfade: clip.transitionToNext.audioCrossfade};
}

// normalizeTransitions re-clamps every junction and drops the last clip's —
// the invariant trim/transition/move ops restore.
function normalizeTransitions(clips: CompositionClip[]): CompositionClip[] {
  return clips.map((clip, index) => ({
    ...clip,
    transitionToNext: index < clips.length - 1 ? clampTransition(clip, clips[index + 1]) : null,
  }));
}

// clampRange pulls a source range into [0, duration] with a usable length.
// Returns null when the duration cannot hold the minimum range.
function clampRange(inSeconds: number, outSeconds: number, duration: number): {inSeconds: number; outSeconds: number} | null {
  if (!finite(inSeconds, outSeconds, duration) || duration < compositionMinRangeSeconds) {
    return null;
  }
  const start = Math.min(Math.max(0, inSeconds), duration - compositionMinRangeSeconds);
  let end = Math.min(outSeconds, duration);
  if (end - start < compositionMinRangeSeconds) {
    end = Math.min(duration, start + compositionMinRangeSeconds);
    if (end - start < compositionMinRangeSeconds) return null;
  }
  return {inSeconds: start, outSeconds: end};
}

// clampSegmentFits re-fits a segment's fades and ducks into its (possibly
// new) span — the validateCompositionDoc rules.
function clampSegmentFits(segment: CompositionSegment): CompositionSegment {
  const span = segmentDurationSeconds(segment);
  const half = span / 2;
  const fadeIn = Math.min(half, Math.max(0, segment.fadeInSeconds));
  const fadeOut = Math.min(half, Math.max(0, segment.fadeOutSeconds));
  const ducks: CompositionDuck[] = [];
  for (const duck of [...segment.ducks].sort((a, b) => a.startSeconds - b.startSeconds)) {
    const start = Math.min(Math.max(0, duck.startSeconds), span - compositionEpsilon);
    const end = Math.min(span, Math.max(start + compositionEpsilon, duck.endSeconds));
    if (ducks.length && start < ducks[ducks.length - 1].endSeconds - compositionEpsilon) {
      continue; // an overlap introduced by clamping drops the later duck
    }
    ducks.push({startSeconds: start, endSeconds: end, gainDb: clampCompositionGain(duck.gainDb)});
  }
  return {...segment, fadeInSeconds: fadeIn, fadeOutSeconds: fadeOut, ducks};
}

// placeSegmentForward finds the first position at or after requestedStart
// where a segment of lengthSeconds fits on the lane without overlap — the
// snap rule adds and moves share (segments never overlap on a lane).
function placeSegmentForward(lane: CompositionLane, lengthSeconds: number, requestedStart: number, skipSegmentID?: string): number {
  let start = Math.max(0, finite(requestedStart) ? requestedStart : 0);
  const obstacles = lane.segments
    .filter((segment) => segment.id !== skipSegmentID)
    .sort((a, b) => a.startSeconds - b.startSeconds);
  for (const obstacle of obstacles) {
    const obstacleStart = obstacle.startSeconds;
    const obstacleEnd = obstacle.startSeconds + segmentDurationSeconds(obstacle);
    if (obstacleEnd <= start) continue;
    if (obstacleStart >= start + lengthSeconds) break;
    start = obstacleEnd;
  }
  return Math.round(start * 1000) / 1000;
}

function updateLane(doc: CompositionDoc, laneID: string, update: (lane: CompositionLane) => CompositionLane): CompositionDoc {
  return {...doc, audioLanes: doc.audioLanes.map((lane) => (lane.id === laneID ? update(lane) : lane))};
}

function updateSegment(doc: CompositionDoc, segmentID: string, update: (segment: CompositionSegment) => CompositionSegment): CompositionDoc {
  return {...doc, audioLanes: doc.audioLanes.map((lane) => ({...lane, segments: lane.segments.map((segment) => (segment.id === segmentID ? update(segment) : segment))}))};
}

function updateClip(doc: CompositionDoc, clipID: string, update: (clip: CompositionClip) => CompositionClip): CompositionDoc {
  return {...doc, video: doc.video.map((clip) => (clip.id === clipID ? update(clip) : clip))};
}

export function renameComposition(doc: CompositionDoc, name: string): CompositionDoc {
  const trimmed = name.trim();
  return {...doc, name: trimmed || compositionDefaultName};
}

// addClip appends (or inserts at index) a clip from a measured ref — the
// picker's add and the seed's landing share this shape: a video at its full
// range, an image as a still held for the default length. No-op at the clip
// limit, or for a ref the video lane cannot hold (audio, or an unmeasured
// source).
export function addClip(doc: CompositionDoc, ref: CompositionMediaRef, index?: number): CompositionDoc {
  const still = ref.kind === 'image';
  const measured = still ? (ref.width ?? 0) > 0 && (ref.height ?? 0) > 0 : ref.kind === 'video' && ref.durationSeconds > 0;
  if (!measured || doc.video.length >= compositionMaxClips) {
    return cloneCompositionDoc(doc);
  }
  const clip: CompositionClip = {
    id: newCompositionID('clp'),
    ref: {...ref},
    inSeconds: 0,
    outSeconds: still ? compositionStillDefaultSeconds : ref.durationSeconds,
    transitionToNext: null,
    audio: {gainDb: 0, muted: false, detached: false},
  };
  const at = index === undefined ? doc.video.length : Math.min(Math.max(0, index), doc.video.length);
  const video = [...doc.video.slice(0, at), clip, ...doc.video.slice(at)];
  return {...doc, video: normalizeTransitions(video)};
}

export function removeClip(doc: CompositionDoc, clipID: string): CompositionDoc {
  return {...doc, video: normalizeTransitions(doc.video.filter((clip) => clip.id !== clipID))};
}

// moveClip reorders. Transitions travel with their clip — the junction
// follows the clip, and a clip that lands last loses its transition
// (normalizeTransitions restores both rules).
export function moveClip(doc: CompositionDoc, clipID: string, toIndex: number): CompositionDoc {
  const from = doc.video.findIndex((clip) => clip.id === clipID);
  if (from < 0) return cloneCompositionDoc(doc);
  const to = Math.min(Math.max(0, Math.round(toIndex)), doc.video.length - 1);
  if (from === to) return cloneCompositionDoc(doc);
  const video = [...doc.video];
  const [clip] = video.splice(from, 1);
  video.splice(to, 0, clip);
  return {...doc, video: normalizeTransitions(video)};
}

// duplicateClip inserts a copy right after its source with a fresh id — the
// junction between them keeps the source's transition, which reads as
// "repeat this cut with the same look".
export function duplicateClip(doc: CompositionDoc, clipID: string): CompositionDoc {
  const index = doc.video.findIndex((clip) => clip.id === clipID);
  if (index < 0 || doc.video.length >= compositionMaxClips) {
    return cloneCompositionDoc(doc);
  }
  const source = doc.video[index];
  const copy: CompositionClip = {...source, id: newCompositionID('clp'), ref: {...source.ref}, audio: {...source.audio}};
  const video = [...doc.video.slice(0, index + 1), copy, ...doc.video.slice(index + 1)];
  return {...doc, video: normalizeTransitions(video)};
}

// setClipRange clamps the source in/out points and re-clamps the junctions
// the change touches (a shorter clip narrows what its dissolves may be). A
// still has no source to clamp into: its range only ever names a length, so
// the request resizes it (setStillDuration) and the range re-pins to [0, out].
export function setClipRange(doc: CompositionDoc, clipID: string, inSeconds: number, outSeconds: number): CompositionDoc {
  const clip = doc.video.find((candidate) => candidate.id === clipID);
  if (!clip) return cloneCompositionDoc(doc);
  if (isStillClip(clip)) return setStillDuration(doc, clipID, outSeconds - inSeconds);
  const clamped = clampRange(inSeconds, outSeconds, clip.ref.durationSeconds);
  if (!clamped) return cloneCompositionDoc(doc);
  const next = updateClip(doc, clipID, (current) => ({...current, inSeconds: clamped.inSeconds, outSeconds: clamped.outSeconds}));
  return {...next, video: normalizeTransitions(next.video)};
}

// setStillDuration resizes a still: the length clamps into its bounds, the
// range re-pins to [0, length], and the junctions it touches re-clamp. No-op
// on a video clip (its length is a source range — setClipRange) or a
// non-finite length.
export function setStillDuration(doc: CompositionDoc, clipID: string, seconds: number): CompositionDoc {
  const clip = doc.video.find((candidate) => candidate.id === clipID);
  const length = clampStillLength(seconds);
  if (!clip || !isStillClip(clip) || length === null) return cloneCompositionDoc(doc);
  const next = updateClip(doc, clipID, (current) => ({...current, inSeconds: 0, outSeconds: length}));
  return {...next, video: normalizeTransitions(next.video)};
}

// setClipTransition installs, resizes, or removes a junction's transition.
// A dissolve clamps into its bounds; a request Go would refuse (bounds null)
// is a no-op — the inspector renders those bounds, so the user never asks
// blind. Passing null is the cut; a kind outside the vocabulary normalizes.
export function setClipTransition(doc: CompositionDoc, clipID: string, transition: {kind?: string; seconds: number; audioCrossfade: boolean} | null): CompositionDoc {
  const index = doc.video.findIndex((clip) => clip.id === clipID);
  if (index < 0 || index >= doc.video.length - 1) return cloneCompositionDoc(doc);
  const next = updateClip(doc, clipID, (current) => ({
    ...current,
    transitionToNext: transition ? {kind: normalizeTransitionKind(transition.kind ?? 'dissolve'), seconds: transition.seconds, audioCrossfade: transition.audioCrossfade} : null,
  }));
  return {...next, video: normalizeTransitions(next.video)};
}

// setClipAudio patches a clip's original-audio mix. A still has no sound to
// mix, mute, or detach — the request is a no-op (Go normalizes stray state
// away, so building it here would only make a save that doesn't round-trip).
export function setClipAudio(doc: CompositionDoc, clipID: string, patch: Partial<CompositionOriginalAudio>): CompositionDoc {
  const target = doc.video.find((clip) => clip.id === clipID);
  if (target && isStillClip(target)) return cloneCompositionDoc(doc);
  return updateClip(doc, clipID, (clip) => ({
    ...clip,
    audio: {
      gainDb: clampCompositionGain(patch.gainDb !== undefined ? patch.gainDb : clip.audio.gainDb),
      muted: patch.muted !== undefined ? patch.muted : clip.audio.muted,
      detached: patch.detached !== undefined ? patch.detached : clip.audio.detached,
    },
  }));
}

// detachClipAudio moves a clip's original sound onto the Detached audio lane
// as a segment pinned to the clip's timeline position (snapping forward if
// a neighbour already holds that span), and marks the clip detached so the
// sound plays exactly once (AU-07). No-op on a silent or already-detached
// clip, or when no lane can take the segment.
export function detachClipAudio(doc: CompositionDoc, clipID: string): CompositionDoc {
  const index = doc.video.findIndex((clip) => clip.id === clipID);
  if (index < 0) return cloneCompositionDoc(doc);
  const clip = doc.video[index];
  if (clip.audio.detached || !clip.ref.hasAudio) return cloneCompositionDoc(doc);
  let lane = doc.audioLanes.find((candidate) => candidate.name === detachedAudioLaneName);
  let next: CompositionDoc;
  if (!lane) {
    if (doc.audioLanes.length >= compositionMaxAudioLanes) {
      lane = doc.audioLanes[doc.audioLanes.length - 1];
      next = {...doc};
    } else {
      lane = {id: newCompositionID('lane'), name: detachedAudioLaneName, gainDb: 0, muted: false, segments: []};
      next = {...doc, audioLanes: [...doc.audioLanes, lane]};
    }
  } else {
    next = {...doc};
  }
  const {starts} = compositionClipLayout(next);
  const start = placeSegmentForward(lane, clipDurationSeconds(clip), starts[index] ?? 0);
  const segment: CompositionSegment = {
    id: newCompositionID('seg'),
    ref: {...clip.ref},
    inSeconds: clip.inSeconds,
    outSeconds: clip.outSeconds,
    startSeconds: start,
    gainDb: 0,
    fadeInSeconds: 0,
    fadeOutSeconds: 0,
    ducks: [],
    detachedFromClipId: clip.id,
  };
  const withSegment = updateLane(next, lane.id, (current) => ({
    ...current,
    segments: [...current.segments, segment].sort((a, b) => a.startSeconds - b.startSeconds),
  }));
  return setClipAudio(withSegment, clipID, {detached: true});
}

// reattachClipAudio is detach's inverse: removes the segment(s) this clip's
// detach created and clears the marker, restoring the clip's own audio.
export function reattachClipAudio(doc: CompositionDoc, clipID: string): CompositionDoc {
  const clip = doc.video.find((candidate) => candidate.id === clipID);
  if (!clip || !clip.audio.detached) return cloneCompositionDoc(doc);
  const cleared: CompositionDoc = {
    ...doc,
    audioLanes: doc.audioLanes
      .map((lane) => ({...lane, segments: lane.segments.filter((segment) => segment.detachedFromClipId !== clipID)}))
      .filter((lane) => lane.segments.length > 0 || lane.name !== detachedAudioLaneName),
  };
  return setClipAudio(cleared, clipID, {detached: false});
}

export function addLane(doc: CompositionDoc, name?: string): CompositionDoc {
  if (doc.audioLanes.length >= compositionMaxAudioLanes) return cloneCompositionDoc(doc);
  const lane: CompositionLane = {
    id: newCompositionID('lane'),
    name: name?.trim() || `Audio ${doc.audioLanes.length + 1}`,
    gainDb: 0,
    muted: false,
    segments: [],
  };
  return {...doc, audioLanes: [...doc.audioLanes, lane]};
}

export function removeLane(doc: CompositionDoc, laneID: string): CompositionDoc {
  return {...doc, audioLanes: doc.audioLanes.filter((lane) => lane.id !== laneID)};
}

export function renameLane(doc: CompositionDoc, laneID: string, name: string): CompositionDoc {
  const trimmed = name.trim();
  return updateLane(doc, laneID, (lane) => ({...lane, name: trimmed || lane.name}));
}

export function setLaneMix(doc: CompositionDoc, laneID: string, patch: {gainDb?: number; muted?: boolean}): CompositionDoc {
  return updateLane(doc, laneID, (lane) => ({
    ...lane,
    gainDb: clampCompositionGain(patch.gainDb !== undefined ? patch.gainDb : lane.gainDb),
    muted: patch.muted !== undefined ? patch.muted : lane.muted,
  }));
}

// addSegmentToLane places an audio (or video-with-audio) ref on a lane at a
// requested timeline position, snapping forward to where it fits. No-op when
// the ref cannot live on a lane.
export function addSegmentToLane(doc: CompositionDoc, laneID: string, ref: CompositionMediaRef, startSeconds: number): CompositionDoc {
  if (ref.kind === 'audio' ? ref.durationSeconds <= 0 : !(ref.kind === 'video' && ref.hasAudio)) {
    return cloneCompositionDoc(doc);
  }
  const lane = doc.audioLanes.find((candidate) => candidate.id === laneID);
  if (!lane) return cloneCompositionDoc(doc);
  const start = placeSegmentForward(lane, ref.durationSeconds, startSeconds);
  const segment: CompositionSegment = {
    id: newCompositionID('seg'),
    ref: {...ref},
    inSeconds: 0,
    outSeconds: ref.durationSeconds,
    startSeconds: start,
    gainDb: 0,
    fadeInSeconds: 0,
    fadeOutSeconds: 0,
    ducks: [],
  };
  return updateLane(doc, laneID, (current) => ({...current, segments: [...current.segments, segment].sort((a, b) => a.startSeconds - b.startSeconds)}));
}

export function removeSegment(doc: CompositionDoc, segmentID: string): CompositionDoc {
  return {...doc, audioLanes: doc.audioLanes.map((lane) => ({...lane, segments: lane.segments.filter((segment) => segment.id !== segmentID)}))};
}

// duplicateSegment copies a segment immediately after itself (snapping
// forward if the copy would collide).
export function duplicateSegment(doc: CompositionDoc, segmentID: string): CompositionDoc {
  for (const lane of doc.audioLanes) {
    const segment = lane.segments.find((candidate) => candidate.id === segmentID);
    if (!segment) continue;
    const copy: CompositionSegment = {
      ...segment,
      id: newCompositionID('seg'),
      ref: {...segment.ref},
      startSeconds: segment.startSeconds + segmentDurationSeconds(segment),
      ducks: segment.ducks.map((duck) => ({...duck})),
      detachedFromClipId: undefined,
    };
    copy.startSeconds = placeSegmentForward(lane, segmentDurationSeconds(segment), copy.startSeconds, segmentID);
    return updateLane(doc, lane.id, (current) => ({...current, segments: [...current.segments, copy].sort((a, b) => a.startSeconds - b.startSeconds)}));
  }
  return cloneCompositionDoc(doc);
}

// moveSegment slides a segment to a new timeline start, snapping forward to
// where it fits on its lane (VS-07's "independently placed audio keeps its
// position unless the user moves it").
export function moveSegment(doc: CompositionDoc, segmentID: string, startSeconds: number): CompositionDoc {
  for (const lane of doc.audioLanes) {
    const segment = lane.segments.find((candidate) => candidate.id === segmentID);
    if (!segment) continue;
    const start = placeSegmentForward(lane, segmentDurationSeconds(segment), startSeconds, segmentID);
    return updateLane(doc, lane.id, (current) => ({
      ...current,
      segments: current.segments.map((candidate) => (candidate.id === segmentID ? {...candidate, startSeconds: start} : candidate)).sort((a, b) => a.startSeconds - b.startSeconds),
    }));
  }
  return cloneCompositionDoc(doc);
}

// setSegmentRange clamps the source in/out points; a longer outcome that
// would overrun the next segment on the lane clamps to fit before it.
export function setSegmentRange(doc: CompositionDoc, segmentID: string, inSeconds: number, outSeconds: number): CompositionDoc {
  for (const lane of doc.audioLanes) {
    const index = lane.segments.findIndex((candidate) => candidate.id === segmentID);
    if (index < 0) continue;
    const segment = lane.segments[index];
    const clamped = clampRange(inSeconds, outSeconds, segment.ref.durationSeconds);
    if (!clamped) continue;
    const next = lane.segments[index + 1];
    let outSeconds2 = clamped.outSeconds;
    if (next) {
      outSeconds2 = Math.min(outSeconds2, clamped.inSeconds + Math.max(compositionMinRangeSeconds, next.startSeconds - segment.startSeconds));
      if (outSeconds2 - clamped.inSeconds < compositionMinRangeSeconds) continue;
    }
    return updateSegment(doc, segmentID, (current) => clampSegmentFits({...current, inSeconds: clamped.inSeconds, outSeconds: outSeconds2}));
  }
  return cloneCompositionDoc(doc);
}

export function setSegmentMix(doc: CompositionDoc, segmentID: string, patch: {gainDb?: number; fadeInSeconds?: number; fadeOutSeconds?: number}): CompositionDoc {
  for (const lane of doc.audioLanes) {
    const segment = lane.segments.find((candidate) => candidate.id === segmentID);
    if (!segment) continue;
    const half = segmentDurationSeconds(segment) / 2;
    return updateSegment(doc, segmentID, (current) => ({
      ...current,
      gainDb: clampCompositionGain(patch.gainDb !== undefined ? patch.gainDb : current.gainDb),
      fadeInSeconds: Math.min(half, Math.max(0, patch.fadeInSeconds !== undefined ? patch.fadeInSeconds : current.fadeInSeconds)),
      fadeOutSeconds: Math.min(half, Math.max(0, patch.fadeOutSeconds !== undefined ? patch.fadeOutSeconds : current.fadeOutSeconds)),
    }));
  }
  return cloneCompositionDoc(doc);
}

// addDuck lowers a segment's gain over [start, end) — the step envelope the
// MVP's "lower the music during dialogue" runs on. Times are relative to the
// segment; an overlapping duck is refused (no-op).
export function addDuck(doc: CompositionDoc, segmentID: string, startSeconds: number, endSeconds: number, gainDb: number): CompositionDoc {
  for (const lane of doc.audioLanes) {
    const segment = lane.segments.find((candidate) => candidate.id === segmentID);
    if (!segment) continue;
    const span = segmentDurationSeconds(segment);
    const start = Math.min(Math.max(0, startSeconds), span - compositionEpsilon);
    const end = Math.min(span, Math.max(start + compositionEpsilon, endSeconds));
    if (segment.ducks.some((duck) => start < duck.endSeconds - compositionEpsilon && duck.startSeconds < end - compositionEpsilon)) {
      continue;
    }
    return updateSegment(doc, segmentID, (current) => ({
      ...current,
      ducks: [...current.ducks, {startSeconds: start, endSeconds: end, gainDb: clampCompositionGain(gainDb)}].sort((a, b) => a.startSeconds - b.startSeconds),
    }));
  }
  return cloneCompositionDoc(doc);
}

export function updateDuck(doc: CompositionDoc, segmentID: string, duckIndex: number, patch: Partial<CompositionDuck>): CompositionDoc {
  for (const lane of doc.audioLanes) {
    const segment = lane.segments.find((candidate) => candidate.id === segmentID);
    if (!segment || !segment.ducks[duckIndex]) continue;
    const span = segmentDurationSeconds(segment);
    const duck = segment.ducks[duckIndex];
    const previous = segment.ducks[duckIndex - 1];
    const nextDuck = segment.ducks[duckIndex + 1];
    let start = patch.startSeconds !== undefined ? Math.min(Math.max(0, patch.startSeconds), span - compositionEpsilon) : duck.startSeconds;
    let end = patch.endSeconds !== undefined ? Math.min(span, patch.endSeconds) : duck.endSeconds;
    if (end - start < compositionEpsilon) {
      end = Math.min(span, start + compositionEpsilon);
    }
    if (previous && start < previous.endSeconds - compositionEpsilon) {
      continue;
    }
    if (nextDuck && end > nextDuck.startSeconds + compositionEpsilon) {
      continue;
    }
    return updateSegment(doc, segmentID, (current) => ({
      ...current,
      ducks: current.ducks.map((candidate, index) => (index === duckIndex
        ? {startSeconds: start, endSeconds: end, gainDb: clampCompositionGain(patch.gainDb !== undefined ? patch.gainDb : candidate.gainDb)}
        : candidate)),
    }));
  }
  return cloneCompositionDoc(doc);
}

export function removeDuck(doc: CompositionDoc, segmentID: string, duckIndex: number): CompositionDoc {
  return updateSegment(doc, segmentID, (segment) => ({...segment, ducks: segment.ducks.filter((_, index) => index !== duckIndex)}));
}

export function setCompositionOutput(doc: CompositionDoc, patch: Partial<CompositionOutput>): CompositionDoc {
  const fit = patch.fit ?? doc.output.fit;
  return {
    ...doc,
    output: {
      width: evenDown(Math.max(0, patch.width !== undefined ? patch.width : doc.output.width)),
      height: evenDown(Math.max(0, patch.height !== undefined ? patch.height : doc.output.height)),
      fps: Math.min(compositionMaxFPS, Math.max(0, patch.fps !== undefined ? patch.fps : doc.output.fps)),
      fit: fit === 'crop' || fit === 'blur' ? fit : 'pad',
    },
  };
}

// resolveOutputFrame is the display answer for "what does this project render
// as": the explicit frame when set, else the inherited source's
// (outputFrameSource), else the 720p/30 fallback, with even dimensions —
// Go's compositionFrame.
export function resolveOutputFrame(doc: CompositionDoc): {width: number; height: number; fps: number} {
  const source = outputFrameSource(doc);
  const width = doc.output.width || source.width || 1280;
  const height = doc.output.height || source.height || 720;
  const fps = doc.output.fps || source.fps || compositionDefaultFPS;
  return {width: evenDown(width), height: evenDown(height), fps: Math.min(compositionMaxFPS, Math.max(1, fps))};
}

// outputFrameSource is the clip an unset output frame inherits from — Go's
// compositionFrameSource: the first VIDEO clip (a still opening the
// sequence, a title card, must not decide the format the footage after it
// renders in), else for a stills-only sequence the first still, fitted so a
// photo's native size never becomes the video frame. A still sets no fps.
export function outputFrameSource(doc: CompositionDoc): {width: number; height: number; fps: number} {
  const firstVideo = doc.video.find((clip) => !isStillClip(clip));
  if (firstVideo) {
    return {width: firstVideo.ref.width ?? 0, height: firstVideo.ref.height ?? 0, fps: firstVideo.ref.fps ?? 0};
  }
  const first = doc.video[0];
  if (!first) return {width: 0, height: 0, fps: 0};
  return {...fitStillFrame(first.ref.width ?? 0, first.ref.height ?? 0), fps: 0};
}

// fitStillFrame scales a still's display size to fit the long-edge cap,
// keeping its shape and never enlarging, even-dimensioned; zero when the
// still is too small to frame — Go's compositionFitStillFrame.
export function fitStillFrame(width: number, height: number): {width: number; height: number} {
  if (!(width > 0 && height > 0)) return {width: 0, height: 0};
  const long = Math.max(width, height);
  let fittedWidth = width;
  let fittedHeight = height;
  if (long > compositionStillFrameMaxEdge) {
    const scale = compositionStillFrameMaxEdge / long;
    fittedWidth = Math.round(width * scale);
    fittedHeight = Math.round(height * scale);
  }
  fittedWidth = evenDown(fittedWidth);
  fittedHeight = evenDown(fittedHeight);
  if (fittedWidth < 2 || fittedHeight < 2) return {width: 0, height: 0};
  return {width: fittedWidth, height: fittedHeight};
}

// compositionProblems mirrors validateCompositionDoc's refusals — the states
// the reducers refuse to build, so a hand-edited or foreign document still
// gets named reasons before Save lights up.
export function compositionProblems(doc: CompositionDoc): string[] {
  const problems: string[] = [];
  if (doc.video.length > compositionMaxClips) {
    problems.push(`a composition supports at most ${compositionMaxClips} clips`);
  }
  if (doc.audioLanes.length > compositionMaxAudioLanes) {
    problems.push(`a composition supports at most ${compositionMaxAudioLanes} audio lanes`);
  }
  const ids = new Set<string>();
  const claim = (id: string, label: string) => {
    if (!id.trim()) {
      problems.push(`${label} has an empty id`);
    } else if (ids.has(id)) {
      problems.push(`${label} reuses id ${id}`);
    }
    ids.add(id);
  };
  doc.video.forEach((clip, index) => {
    const label = `clip ${index + 1}`;
    claim(clip.id, label);
    if (isStillClip(clip)) {
      if (!((clip.ref.width ?? 0) > 0 && (clip.ref.height ?? 0) > 0)) {
        problems.push(`${label}'s still was never measured`);
      }
      if (!finite(clip.inSeconds, clip.outSeconds)) {
        problems.push(`${label} has a non-finite length`);
      }
    } else {
      if (clip.ref.kind !== 'video') {
        problems.push(`${label} references ${clip.ref.kind || 'unknown'} media — the video lane holds video clips and stills`);
      }
      if (clip.ref.durationSeconds <= 0) {
        problems.push(`${label}'s source was never measured`);
      }
      if (!finite(clip.inSeconds, clip.outSeconds) || clip.outSeconds - clip.inSeconds < compositionMinRangeSeconds) {
        problems.push(`${label}'s source range is too short`);
      }
    }
    if (clip.transitionToNext) {
      const kind = clip.transitionToNext.kind;
      if (kind !== 'dissolve' && kind !== 'fade' && !compositionTransitions.includes(kind)) {
        problems.push(`${label}'s transition kind must be "cut", "dissolve", or an xfade transition name`);
      }
    }
    if (clip.transitionToNext && index < doc.video.length - 1 && transitionBounds(doc, index) === null) {
      problems.push(`${label}'s transition does not fit: the adjacent clips are too short`);
    }
  });
  doc.audioLanes.forEach((lane, laneIndex) => {
    claim(lane.id, `audio lane ${laneIndex + 1}`);
    let laneEnd = 0;
    lane.segments.forEach((segment, segmentIndex) => {
      const label = `audio lane ${laneIndex + 1} segment ${segmentIndex + 1}`;
      claim(segment.id, label);
      if (segment.ref.kind !== 'audio' && !(segment.ref.kind === 'video' && segment.ref.hasAudio)) {
        problems.push(`${label} references ${segment.ref.kind || 'unknown'} media`);
      }
      if (segment.ref.durationSeconds <= 0) {
        problems.push(`${label}'s source was never measured`);
      }
      if (!finite(segment.inSeconds, segment.outSeconds) || segment.outSeconds - segment.inSeconds < compositionMinRangeSeconds) {
        problems.push(`${label}'s source range is too short`);
      }
      if (segment.startSeconds < 0) {
        problems.push(`${label} starts before the composition begins`);
      }
      if (segment.startSeconds < laneEnd - compositionEpsilon) {
        problems.push(`${label} overlaps the previous segment on its lane`);
      }
      laneEnd = segment.startSeconds + segmentDurationSeconds(segment);
      segment.ducks.forEach((duck, duckIndex) => {
        if (duck.endSeconds - duck.startSeconds < compositionEpsilon) {
          problems.push(`${label} duck ${duckIndex + 1} is too short to hear`);
        }
      });
      for (let d = 1; d < segment.ducks.length; d++) {
        if (segment.ducks[d].startSeconds < segment.ducks[d - 1].endSeconds - compositionEpsilon) {
          problems.push(`${label} duck ${d + 1} overlaps the previous duck`);
        }
      }
    });
  });
  return problems;
}

// formatCompositionClock renders m:ss.d — the timeline's display form
// (formatTime's shape, kept here so the model test covers it).
export function formatCompositionClock(seconds: number): string {
  const safe = Number.isFinite(seconds) && seconds > 0 ? seconds : 0;
  const minutes = Math.floor(safe / 60);
  const rest = safe - minutes * 60;
  const whole = Math.floor(rest);
  const tenth = Math.floor((rest - whole) * 10);
  return `${minutes}:${String(whole).padStart(2, '0')}.${tenth}`;
}
