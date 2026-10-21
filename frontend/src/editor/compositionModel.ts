// Composition document model: the pure logic behind the video composer. The
// editor holds one CompositionDoc and derives everything from the ops below;
// undo/redo is full-doc snapshots, so every op returns a new doc and a no-op
// returns a clone. This module mirrors composition.go's rules (validation
// semantics, clamps, layout math) and stays free of React and the wailsjs
// bindings so test/run-composition-model-tests.cjs can compile it alone,
// like the other editor models.

export type CompositionMediaRefKind = 'video' | 'audio';

export type CompositionMediaRef = {
  artifactId: string;
  conversationId?: string;
  path: string;
  mimeType?: string;
  kind: CompositionMediaRefKind;
  durationSeconds: number;
  width?: number;
  height?: number;
  fps?: number;
  hasAudio?: boolean;
};

export type CompositionTransition = {
  kind: 'dissolve';
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
      transitionToNext: clip?.transitionToNext?.kind === 'dissolve'
        ? {
            kind: 'dissolve' as const,
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
    kind: ref?.kind === 'audio' ? 'audio' : 'video',
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

export function clipDurationSeconds(clip: CompositionClip): number {
  return Math.max(0, clip.outSeconds - clip.inSeconds);
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

// clampTransition pulls a junction's dissolve into its bounds, in place on
// the cloned clip array. Returns the transition (or null for a cut).
function clampTransition(clip: CompositionClip, next: CompositionClip): CompositionTransition | null {
  if (!clip.transitionToNext || clip.transitionToNext.kind !== 'dissolve') {
    return null;
  }
  const upper = Math.min(compositionTransitionMaxSeconds, Math.min(clipDurationSeconds(clip), clipDurationSeconds(next)) / 2);
  if (upper < compositionTransitionMinSeconds) {
    return null;
  }
  const seconds = Math.min(upper, Math.max(compositionTransitionMinSeconds, finite(clip.transitionToNext.seconds) && clip.transitionToNext.seconds > 0 ? clip.transitionToNext.seconds : compositionTransitionDefaultSeconds));
  return {kind: 'dissolve', seconds, audioCrossfade: clip.transitionToNext.audioCrossfade};
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

// addClip appends (or inserts at index) a full-range clip from a measured
// ref — the picker's add and the seed's landing share this shape. No-op at
// the clip limit.
export function addClip(doc: CompositionDoc, ref: CompositionMediaRef, index?: number): CompositionDoc {
  if (ref.kind !== 'video' || ref.durationSeconds <= 0 || doc.video.length >= compositionMaxClips) {
    return cloneCompositionDoc(doc);
  }
  const clip: CompositionClip = {
    id: newCompositionID('clp'),
    ref: {...ref},
    inSeconds: 0,
    outSeconds: ref.durationSeconds,
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
// the change touches (a shorter clip narrows what its dissolves may be).
export function setClipRange(doc: CompositionDoc, clipID: string, inSeconds: number, outSeconds: number): CompositionDoc {
  const clip = doc.video.find((candidate) => candidate.id === clipID);
  if (!clip) return cloneCompositionDoc(doc);
  const clamped = clampRange(inSeconds, outSeconds, clip.ref.durationSeconds);
  if (!clamped) return cloneCompositionDoc(doc);
  const next = updateClip(doc, clipID, (current) => ({...current, inSeconds: clamped.inSeconds, outSeconds: clamped.outSeconds}));
  return {...next, video: normalizeTransitions(next.video)};
}

// setClipTransition installs, resizes, or removes a junction's transition.
// A dissolve clamps into its bounds; a request Go would refuse (bounds null)
// is a no-op — the inspector renders those bounds, so the user never asks
// blind. Passing null is the cut.
export function setClipTransition(doc: CompositionDoc, clipID: string, transition: {seconds: number; audioCrossfade: boolean} | null): CompositionDoc {
  const index = doc.video.findIndex((clip) => clip.id === clipID);
  if (index < 0 || index >= doc.video.length - 1) return cloneCompositionDoc(doc);
  const next = updateClip(doc, clipID, (current) => ({
    ...current,
    transitionToNext: transition ? {kind: 'dissolve', seconds: transition.seconds, audioCrossfade: transition.audioCrossfade} : null,
  }));
  return {...next, video: normalizeTransitions(next.video)};
}

export function setClipAudio(doc: CompositionDoc, clipID: string, patch: Partial<CompositionOriginalAudio>): CompositionDoc {
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
// as": the explicit frame when set, else the first clip's display size and
// fps (else the 720p/30 fallback), with even dimensions.
export function resolveOutputFrame(doc: CompositionDoc): {width: number; height: number; fps: number} {
  const first = doc.video[0];
  const width = doc.output.width || (first?.ref.width ?? 0) || 1280;
  const height = doc.output.height || (first?.ref.height ?? 0) || 720;
  const fps = doc.output.fps || (first?.ref.fps ?? 0) || compositionDefaultFPS;
  return {width: evenDown(width), height: evenDown(height), fps: Math.min(compositionMaxFPS, Math.max(1, fps))};
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
    if (clip.ref.kind !== 'video') {
      problems.push(`${label} references ${clip.ref.kind || 'unknown'} media — the video lane holds video clips`);
    }
    if (clip.ref.durationSeconds <= 0) {
      problems.push(`${label}'s source was never measured`);
    }
    if (!finite(clip.inSeconds, clip.outSeconds) || clip.outSeconds - clip.inSeconds < compositionMinRangeSeconds) {
      problems.push(`${label}'s source range is too short`);
    }
    if (clip.transitionToNext && clip.transitionToNext.kind !== 'dissolve') {
      problems.push(`${label}'s transition kind must be "cut" or "dissolve"`);
    }
    if (clip.transitionToNext && index < doc.video.length - 1 && transitionBounds(doc, index) === null) {
      problems.push(`${label}'s dissolve does not fit: the adjacent clips are too short`);
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
