import {
  CompositionDoc,
  CompositionMediaRef,
  addClip,
  addDuck,
  addLane,
  addSegmentToLane,
  audioOverhangSeconds,
  cloneCompositionDoc,
  compositionClipLayout,
  compositionProblems,
  compositionSequenceDuration,
  detachClipAudio,
  duplicateClip,
  duplicateSegment,
  formatCompositionClock,
  moveClip,
  moveSegment,
  newCompositionID,
  normalizeCompositionDoc,
  normalizeTransitionKind,
  reattachClipAudio,
  removeDuck,
  removeSegment,
  resolveOutputFrame,
  sameCompositionDoc,
  setClipAudio,
  setClipRange,
  setClipTransition,
  setCompositionOutput,
  setSegmentMix,
  setSegmentRange,
  toWireDoc,
  transitionBounds,
  updateDuck,
} from '../src/editor/compositionModel';

function assert(value: boolean, message: string): void {if (!value) throw new Error(message);}

function videoRef(id: string, seconds: number, hasAudio = true): CompositionMediaRef {
  return {artifactId: id, path: `/tmp/${id}.mp4`, kind: 'video', durationSeconds: seconds, width: 1280, height: 720, fps: 30, hasAudio};
}

function audioRef(id: string, seconds: number): CompositionMediaRef {
  return {artifactId: id, path: `/tmp/${id}.wav`, kind: 'audio', durationSeconds: seconds, hasAudio: true};
}

// A three-clip doc: 10s, 5s, 8s with a 1s dissolve after clip 1.
function threeClips(): CompositionDoc {
  let doc = normalizeCompositionDoc({schemaVersion: 1, name: 'Demo', video: [], audioLanes: []});
  doc = addClip(doc, videoRef('vid_1', 10));
  doc = addClip(doc, videoRef('vid_2', 5));
  doc = addClip(doc, videoRef('vid_3', 8));
  return setClipTransition(doc, doc.video[0].id, {seconds: 1, audioCrossfade: true});
}

// Wire normalization fills every optional field and defaults the fit.
const wire = normalizeCompositionDoc({
  schemaVersion: 1,
  name: '  ',
  video: [{id: 'c1', ref: {artifactId: 'vid_1', path: '/tmp/v.mp4', kind: 'video', durationSeconds: 4}, inSeconds: 0, outSeconds: 4,
    audio: {gainDb: -99, muted: false}}],
  audioLanes: [{id: 'l1', name: '  ', segments: [{id: 's1', ref: {artifactId: 'aud_1', path: '/tmp/a.wav', kind: 'audio', durationSeconds: 2},
    inSeconds: 0, outSeconds: 2, startSeconds: 0, ducks: [{startSeconds: 0, endSeconds: 1}]}]}],
});
assert(wire.name === 'Untitled composition', 'blank name defaults');
assert(wire.output.fit === 'pad', 'fit defaults to pad');
assert(wire.video[0].audio.gainDb === -60, 'clip gain clamps to the floor');
assert(wire.video[0].transitionToNext === null, 'absent transition reads as a cut');
assert(wire.audioLanes[0].name === 'Audio', 'blank lane name defaults');
assert(wire.audioLanes[0].segments[0].ducks[0].gainDb === 0, 'duck gain defaults to 0 dB');
assert(wire.audioLanes[0].segments[0].fadeInSeconds === 0, 'fades default to 0');
assert(normalizeCompositionDoc({}).video.length === 0 && normalizeCompositionDoc({}).audioLanes.length === 0, 'empty wire normalizes');

// Clone/same round-trips; toWireDoc omits nothing the Go side rejects.
const built = threeClips();
const clone = cloneCompositionDoc(built);
assert(sameCompositionDoc(clone, built), 'clone compares equal');
assert(!sameCompositionDoc(clone, setClipAudio(clone, clone.video[1].id, {muted: true})), 'a mix change is dirty');
assert(JSON.stringify(toWireDoc(built)).includes('"audioCrossfade":true'), 'wire carries the transition');
assert(newCompositionID('clp').startsWith('clp_') && newCompositionID('clp').length === 28, 'ids mirror Go randomID');

// Layout: dissolve overlaps the junction by its length.
const doc = threeClips();
const layout = compositionClipLayout(doc);
assert(layout.starts.join() === '0,9,14', `starts were ${layout.starts}`);
assert(compositionSequenceDuration(doc) === 22, `duration was ${compositionSequenceDuration(doc)}`);
assert(formatCompositionClock(83.46) === '1:23.4', 'clock renders m:ss.d');

// Transition bounds and clamps (TR-05): half the shorter neighbor, capped.
assert(transitionBounds(doc, 0)?.max === 2.5, 'bounds max is half the 5s neighbor');
assert(transitionBounds(doc, 2) === null, 'the last clip has no next');
const stretched = setClipTransition(doc, doc.video[0].id, {seconds: 9, audioCrossfade: false});
assert(stretched.video[0].transitionToNext?.seconds === 2.5, 'an oversized dissolve clamps to its bounds');
const cut = setClipTransition(doc, doc.video[0].id, null);
assert(cut.video[0].transitionToNext === null && compositionSequenceDuration(cut) === 23, 'a cut restores the full length');
const tight = setClipRange(doc, doc.video[1].id, 0, 0.15);
assert(tight.video[0].transitionToNext === null, 'a trimmed neighbor drops the dissolve that no longer fits');
assert(Math.abs((tight.video[1].outSeconds - tight.video[1].inSeconds) - 0.15) < 1e-9, 'range lands as requested');
const clampedRange = setClipRange(doc, doc.video[0].id, -5, 99);
assert(clampedRange.video[0].inSeconds === 0 && clampedRange.video[0].outSeconds === 10, 'range clamps to the source');

// Reordering: transitions travel with their clip; the last position drops one.
const moved = moveClip(doc, doc.video[1].id, 0);
assert(moved.video[0].ref.artifactId === 'vid_2' && moved.video[1].ref.artifactId === 'vid_1', 'moveClip reorders');
assert(moved.video[1].transitionToNext === null || moved.video[1].transitionToNext.kind === 'dissolve', 'moved doc keeps valid transitions');
assert(moved.video[2].transitionToNext === null, 'the last clip never keeps a transition');
const duplicated = duplicateClip(doc, doc.video[2].id);
assert(duplicated.video.length === 4 && duplicated.video[3].ref.artifactId === 'vid_3', 'duplicateClip inserts a copy after');

// The clip limit is a no-op wall, not a throw.
let full = normalizeCompositionDoc({video: [], audioLanes: []});
for (let i = 0; i < 60; i++) full = addClip(full, videoRef(`vid_x${i}`, 2));
assert(full.video.length === 50, `clip limit held at ${full.video.length}`);

// Lanes and segments: forward snapping keeps lanes non-overlapping.
let lanes = addLane(normalizeCompositionDoc({video: [], audioLanes: []}), 'Music');
const laneID = lanes.audioLanes[0].id;
lanes = addSegmentToLane(lanes, laneID, audioRef('aud_1', 6), 0);
lanes = addSegmentToLane(lanes, laneID, audioRef('aud_2', 4), 3);
assert(lanes.audioLanes[0].segments[1].startSeconds === 6, `segment snapped forward past the overlap (got ${lanes.audioLanes[0].segments[1].startSeconds})`);
assert(audioOverhangSeconds({...threeClips(), audioLanes: lanes.audioLanes}) === 0, 'music inside the sequence has no overhang');
const overhang = addSegmentToLane(lanes, laneID, audioRef('aud_3', 10), 20);
assert(audioOverhangSeconds({...threeClips(), audioLanes: overhang.audioLanes}) === 8, 'the audio tail past the video is measured');
const movedSeg = moveSegment(overhang, overhang.audioLanes[0].segments[2].id, 0);
assert(movedSeg.audioLanes[0].segments[2].startSeconds === 10, 'a move snaps forward to the free span');
const trimmed = setSegmentRange(overhang, overhang.audioLanes[0].segments[0].id, 0, 6);
assert(trimmed.audioLanes[0].segments[0].outSeconds === 6, 'segment range clamps to its source');
const dupSeg = duplicateSegment(trimmed, trimmed.audioLanes[0].segments[0].id);
assert(dupSeg.audioLanes[0].segments.length === 4 && dupSeg.audioLanes[0].segments[1].startSeconds === 6, 'duplicateSegment lands after itself');
const mixed = setSegmentMix(trimmed, trimmed.audioLanes[0].segments[0].id, {gainDb: -6, fadeInSeconds: 99});
assert(mixed.audioLanes[0].segments[0].fadeInSeconds === 3, 'fades clamp to half the segment');
assert(mixed.audioLanes[0].segments[0].gainDb === -6, 'segment gain lands');
let removed = removeSegment(mixed, mixed.audioLanes[0].segments[0].id);
assert(removed.audioLanes[0].segments.length === 2, 'removeSegment drops one');

// Ducks: the step envelope for lowering music under dialogue.
let ducked = addDuck(removed, removed.audioLanes[0].segments[0].id, 1, 2.5, -12);
assert(ducked.audioLanes[0].segments[0].ducks.length === 1, 'duck lands');
const overlapping = addDuck(ducked, ducked.audioLanes[0].segments[0].id, 2, 3, -9);
assert(overlapping.audioLanes[0].segments[0].ducks.length === 1, 'an overlapping duck is refused');
const nudged = updateDuck(ducked, ducked.audioLanes[0].segments[0].id, 0, {endSeconds: 3.5, gainDb: -30});
assert(nudged.audioLanes[0].segments[0].ducks[0].endSeconds === 3.5 && nudged.audioLanes[0].segments[0].ducks[0].gainDb === -30, 'duck updates land');
assert(removeDuck(nudged, nudged.audioLanes[0].segments[0].id, 0).audioLanes[0].segments[0].ducks.length === 0, 'duck removes');

// Detach / reattach (AU-07): the sound moves to the Detached lane at the
// clip's timeline position, plays once, and comes back.
const detachBase = threeClips();
const detached = detachClipAudio(detachBase, detachBase.video[1].id);
assert(detached.video[1].audio.detached === true, 'detach marks the clip');
assert(detached.audioLanes.length === 1 && detached.audioLanes[0].name === 'Detached audio', 'detach creates its lane');
assert(detached.audioLanes[0].segments[0].startSeconds === 9, `the detached segment pins to the clip start (got ${detached.audioLanes[0].segments[0].startSeconds})`);
assert(detached.audioLanes[0].segments[0].detachedFromClipId === detachBase.video[1].id, 'the segment links back to its clip');
const silentBase = threeClips();
const silent = detachClipAudio(silentBase, 'nope');
assert(sameCompositionDoc(silent, silentBase), 'detaching an unknown clip is a no-op');
const reattached = reattachClipAudio(detached, detached.video[1].id);
assert(reattached.video[1].audio.detached === false && reattached.audioLanes.length === 0, 'reattach removes the segment and the emptied lane');
const firstDetached = detachClipAudio(detachBase, detachBase.video[0].id);
const bothDetached = detachClipAudio(firstDetached, firstDetached.video[1].id);
assert(bothDetached.audioLanes[0].segments.length === 2, 'two detaches share the lane');
assert(bothDetached.audioLanes[0].segments[1].startSeconds === 10, 'the second detach pins after the first (no overlap, no double sound)');

// Output frame resolution and clamps (VS-06).
assert(resolveOutputFrame(threeClips()).width === 1280 && resolveOutputFrame(threeClips()).fps === 30, 'output inherits the first clip');
const sized = setCompositionOutput(threeClips(), {width: 1081, height: 1921, fit: 'blur'});
assert(sized.output.width === 1080 && sized.output.height === 1920 && sized.output.fit === 'blur', 'output clamps even and takes the fit');

// Junction vocabulary (the composer's xfade names).
assert(normalizeTransitionKind('fade') === 'dissolve' && normalizeTransitionKind('wipeleft') === 'wipeleft', "'fade' aliases to dissolve; curated names pass");
assert(normalizeTransitionKind('radialnonsense') === 'dissolve', 'a foreign kind coerces back to the default');
const wipeBase = threeClips();
const wipe = setClipTransition(wipeBase, wipeBase.video[1].id, {kind: 'wipeleft', seconds: 1, audioCrossfade: false});
assert(wipe.video[1].transitionToNext?.kind === 'wipeleft', 'a named kind survives the set');
assert(sameCompositionDoc(normalizeCompositionDoc(toWireDoc(wipe)), wipe), 'a named kind round-trips the wire');
assert(wipe.video[1].transitionToNext?.audioCrossfade === false, 'a named kind keeps its audio choice');

// Problems: the Go refusal mirror.
assert(compositionProblems(threeClips()).length === 0, 'a built doc has no problems');
const broken = cloneCompositionDoc(threeClips());
broken.video[1] = {...broken.video[1], ref: audioRef('aud_1', 5)};
const problems = compositionProblems(broken);
assert(problems.some((problem) => problem.includes('video lane holds video clips')), 'an audio ref on the video lane is named');
const overlapped = cloneCompositionDoc(lanes);
overlapped.audioLanes[0].segments[1] = {...overlapped.audioLanes[0].segments[1], startSeconds: 1};
assert(compositionProblems(overlapped).some((problem) => problem.includes('overlaps the previous segment')), 'a lane overlap is named');

console.log('composition model assertions passed');
