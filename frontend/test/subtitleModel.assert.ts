import {
  SubtitleDoc, activeSubtitleIndex, deleteSubtitleCue, formatSubtitleClock, insertSubtitleCueAfter,
  linkedMediaProvenance, mergeCueWithNext, nudgeCueEdge, replaceAllInCues, sameSubtitleDoc, shiftAllCues,
  splitSubtitleCue, subtitleWarnings, updateCueText, updateCueTimes,
} from '../src/editor/subtitleModel';

function assert(value: boolean, message: string): void {if (!value) throw new Error(message);}

function doc(cues: [number, number, string][]): SubtitleDoc {
  return {format: 'srt', cues: cues.map(([start, end, text]) => ({startSeconds: start, endSeconds: end, text}))};
}

// Dirty tracking: JSON equality, format participates.
const base = doc([[0, 2, 'One'], [2.5, 4, 'Two']]);
assert(sameSubtitleDoc(base, doc([[0, 2, 'One'], [2.5, 4, 'Two']])), 'equal docs must match');
assert(!sameSubtitleDoc(base, doc([[0, 2, 'One'], [2.5, 4, 'Two ']])), 'text change must be dirty');
assert(!sameSubtitleDoc(base, {...base, format: 'vtt'}), 'format change must be dirty');

// Text and time edits are targeted.
assert(updateCueText(base, 1, 'Changed').cues[1].text === 'Changed', 'updateCueText edits one cue');
assert(updateCueText(base, 99, 'x').cues.length === 2, 'out-of-range edit is a no-op copy');
assert(updateCueTimes(base, 0, 0.5, 2).cues[0].startSeconds === 0.5, 'updateCueTimes sets both edges');

// Edge nudges clamp rather than invert.
assert(nudgeCueEdge(base, 0, 'start', -1).cues[0].startSeconds === 0, 'start clamps at zero');
assert(nudgeCueEdge(base, 0, 'start', 5).cues[0].startSeconds === 2, 'start clamps at the end');
assert(nudgeCueEdge(base, 0, 'end', -5).cues[0].endSeconds === 0, 'end clamps at the start');

// Shift-all clamps negative shifts at zero preserving duration.
const shifted = shiftAllCues(doc([[0, 2, 'A'], [3, 5, 'B']]), -1.5);
assert(shifted.cues[0].startSeconds === 0 && shifted.cues[0].endSeconds === 2, 'first cue clamps at zero and keeps duration');
assert(shifted.cues[1].startSeconds === 1.5 && shifted.cues[1].endSeconds === 3.5, 'later cue shifts fully');
const later = shiftAllCues(base, 2);
assert(later.cues[0].startSeconds === 2 && later.cues[0].endSeconds === 4, 'positive shift moves everything');

// Split: time-proportional word boundary, clamped inside the cue.
const split = splitSubtitleCue(doc([[0, 4, 'one two three four']]), 0, 1);
assert(split.cues.length === 2, 'split produces two cues');
assert(split.cues[0].endSeconds === 1 && split.cues[1].startSeconds === 1, 'split shares the boundary time');
assert(split.cues[0].text === 'one' && split.cues[1].text === 'two three four', 'text splits near the ratio');
const clamped = splitSubtitleCue(doc([[0, 4, 'one two']]), 0, 99);
assert(clamped.cues[0].endSeconds === 4 && clamped.cues[1].startSeconds === 4, 'split point clamps into the cue');

// Merge spans times and joins text; the last cue cannot merge.
const merged = mergeCueWithNext(doc([[0, 2, 'One'], [2.5, 4, 'Two'], [5, 6, 'Three']]), 0);
assert(merged.cues.length === 2, 'merge removes the next cue');
assert(merged.cues[0].text === 'One Two', 'merge joins with a space');
assert(merged.cues[0].startSeconds === 0 && merged.cues[0].endSeconds === 4, 'merge spans the times');
assert(mergeCueWithNext(base, 1).cues.length === 2, 'merging the last cue is a no-op');

// Insert-after seeds a zero-length cue at the boundary.
const inserted = insertSubtitleCueAfter(doc([[0, 2, 'One']]), 0);
assert(inserted.cues.length === 2 && inserted.cues[1].startSeconds === 2 && inserted.cues[1].endSeconds === 2, 'insert seeds at the end');
const insertedTop = insertSubtitleCueAfter(doc([[0, 2, 'One']]), -1);
assert(insertedTop.cues.length === 2 && insertedTop.cues[0].startSeconds === 0, 'insert above the top seeds at zero');

// Delete removes exactly the named cue.
assert(deleteSubtitleCue(base, 0).cues.length === 1 && deleteSubtitleCue(base, 0).cues[0].text === 'Two', 'delete removes the cue');

// Replace-all is literal and counted.
const replaced = replaceAllInCues(doc([[0, 1, 'cat cat'], [2, 3, 'cats']]), 'cat', 'dog');
assert(replaced.count === 3, `replace-all counts every occurrence, got ${replaced.count}`);
assert(replaced.doc.cues[0].text === 'dog dog' && replaced.doc.cues[1].text === 'dogs', 'replace-all substitutes literally');
assert(replaceAllInCues(base, '', 'x').count === 0, 'empty find is a no-op');

// Warnings mirror subtitleCueWarnings (subtitle_editor.go).
const clean = subtitleWarnings(doc([[0, 2, 'Fine'], [2.5, 4.5, 'Also fine']]));
assert(clean.length === 0, `clean doc must not warn: ${JSON.stringify(clean)}`);
const warned = subtitleWarnings(doc([[-1, 0.2, 'hi'], [0.1, 0.3, 'overlap'], [1, 1.2, 'x'.repeat(40)], [5, 4, 'backwards']]));
const messages = warned.map((warning) => warning.message).join('\n');
for (const want of ['negative start', 'shorter than half a second', 'overlaps the previous cue', 'reading speed', 'end is before its start']) {
  assert(messages.includes(want), `warnings missing ${want}: ${messages}`);
}
assert(subtitleWarnings(doc([[0, 2, 'x'.repeat(50)]])).length === 1, 'high reading speed alone warns once');

// Active-cue mapping applies the session offset and treats the end as open.
const cueDoc = doc([[1, 2, 'One'], [3, 4, 'Two']]);
assert(activeSubtitleIndex(cueDoc, 1.5, 0) === 0, 'inside the window selects the cue');
assert(activeSubtitleIndex(cueDoc, 2, 0) === -1, 'the end boundary is open');
assert(activeSubtitleIndex(cueDoc, 2.5, -0.5) === 1, 'a negative offset maps media onto later cues (cue time runs ahead)');
assert(activeSubtitleIndex(cueDoc, 4.5, 1.5) === 1, 'a positive offset maps media onto earlier cue time');
assert(activeSubtitleIndex(cueDoc, 9, 0) === -1, 'a gap selects nothing');

// Clock formatting follows the format's decimal separator.
assert(formatSubtitleClock(3671.5, 'srt') === '01:01:11,500', 'srt clock uses a comma');
assert(formatSubtitleClock(3671.5, 'vtt') === '01:01:11.500', 'vtt clock uses a dot');
assert(formatSubtitleClock(-3, 'srt') === '00:00:00,000', 'negative times clamp at zero for display');

// Linked-media provenance: the edited conversation's own assets never read as
// foreign media, even when its title is blank; a cross-conversation row (the
// library fold) names its chat.
assert(linkedMediaProvenance({conversationId: 'conv_a'}, 'conv_a') === 'this chat', 'own asset reads this chat');
assert(linkedMediaProvenance({conversationId: 'conv_a', conversationTitle: ''}, 'conv_a') === 'this chat', 'untitled own asset still reads this chat');
assert(linkedMediaProvenance({conversationId: 'conv_b', conversationTitle: 'Studio session'}, 'conv_a') === 'Studio session', 'cross-conversation asset names its chat');
assert(linkedMediaProvenance({conversationId: 'conv_b'}, 'conv_a') === 'another chat', 'untitled cross-conversation asset falls back to another chat');

console.log('subtitle model assertions passed');
