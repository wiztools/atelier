import {
  defaultVideoReframeParams, upsertVideoReframeMarker, moveVideoReframeMarkerTime,
  deleteVideoReframeMarker, outputForVideoPreset, changeVideoAspectWithOutput,
  updateVideoReframeMarker, sameVideoParams,
} from '../src/editor/videoTimelineModel';
import {validateVideoReframe} from '../src/editor/videoReframe';
import {mergeVideoEditOperations} from '../src/editor/videoEditState';

function assert(value: boolean, message: string): void {if (!value) throw new Error(message);}
const original = defaultVideoReframeParams({width: 1920, height: 1080, durationSeconds: 4});
const completed = {id: 'first-render', status: 'completed', resultUrl: 'saved.mp4'};
const stale = {id: 'first-render', status: 'running', resultUrl: ''};
const reconciled = mergeVideoEditOperations([stale], [completed, stale]);
assert(reconciled[0].status === 'completed' && reconciled[0].resultUrl === 'saved.mp4', 'terminal event must survive a delayed rendering snapshot');
assert(stale.status === 'running', 'event reconciliation must not mutate its input snapshot');
const added = upsertVideoReframeMarker(original, 2, {...original.markers[0], x: 100}, 'hold');
assert(original.markers.length === 1 && added.markers.length === 2, 'adding a marker must not mutate the original');
const replaced = upsertVideoReframeMarker(added, 2, {...added.markers[1], x: 200});
assert(replaced.markers.length === 2 && replaced.markers[1].x === 200, 'same-time edit must replace rather than duplicate');
const retimed = moveVideoReframeMarkerTime(replaced, 1, 4);
assert(retimed.markers[1].timeSeconds === 4, 'last marker can be placed at the clip end');
assert(sameVideoParams(deleteVideoReframeMarker(replaced, 0), replaced), 'start marker cannot be deleted');
assert(sameVideoParams(moveVideoReframeMarkerTime(replaced, 0, 3), replaced), 'start marker cannot be retimed');
const removed = deleteVideoReframeMarker(replaced, 1);
assert(removed.markers.length === 1 && removed.markers[0].timeSeconds === 0, 'deleting a later marker preserves the start');
const bounded = updateVideoReframeMarker(original, 0, {x: 99999, y: -10});
assert(bounded.markers[0].x === 1326 && bounded.markers[0].y === 0, 'position edits must clamp to source bounds');
for (const aspect of ['9:16', '16:9', '1:1'] as const) {
  const ratio = changeVideoAspectWithOutput(original, aspect, '720p');
  validateVideoReframe(ratio);
  const output = outputForVideoPreset(ratio, '720p');
  assert(Math.min(output.width, output.height) === 720, '720p presets must have a 720px short edge');
}
const small = defaultVideoReframeParams({width: 160, height: 96, durationSeconds: 0.0004});
const end = upsertVideoReframeMarker(small, 0.0004, small.markers[0]);
validateVideoReframe(end);
assert(end.markers[0].timeSeconds === 0, 'sub-millisecond quantization must preserve the start marker');
let full = original;
for (let i = 1; i < 100; i++) full = upsertVideoReframeMarker(full, i / 25, original.markers[0]);
assert(upsertVideoReframeMarker(full, 1, {...original.markers[0], x: 200}).markers.length === 100, 'existing marker remains editable at the cap');
let refused = false;
try {upsertVideoReframeMarker(full, 3.99, original.markers[0]);} catch {refused = true;}
assert(refused && full.markers.length === 100, 'adding beyond the cap must refuse without dropping another marker');
console.log('video timeline assertions passed');
