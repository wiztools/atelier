import type {main} from '../wailsjs/go/models';
import {
  MentionCandidate,
  assetMentionLabel,
  assetTurnLabel,
  detectMentionAt,
  mentionMatches,
  parseComposerMentions,
} from '../src/composerMentions';

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message);
}

function same(actual: unknown, expected: unknown, message: string): void {
  const got = JSON.stringify(actual);
  const want = JSON.stringify(expected);
  if (got !== want) throw new Error(`${message}: expected ${want}, got ${got}`);
}

// An open token is '@' at the start of text or after whitespace, running to
// the caret with no whitespace inside it.
same(detectMentionAt('@', 1), {at: 0, query: ''}, 'a bare @ opens an empty query');
same(detectMentionAt('look at @img', 12), {at: 8, query: 'img'}, 'the token runs from @ to the caret');
same(detectMentionAt('look at @img', 10), {at: 8, query: 'i'}, 'the query ends at the caret');
same(detectMentionAt('a\n@clip', 7), {at: 2, query: 'clip'}, 'a newline counts as whitespace');
assert(detectMentionAt('mail me@host', 12) === null, 'an @ inside a word is not a mention');
assert(detectMentionAt('look at @img now', 16) === null, 'whitespace after the token closes it');
assert(detectMentionAt('no mention here', 15) === null, 'text without @ has no token');
assert(detectMentionAt('@img', 0) === null, 'a caret at the start has nothing behind it');
assert(detectMentionAt('', 0) === null, 'an empty composer has no token');
// Re-entering the first word of an inserted @"quoted name" re-opens the menu;
// the quote stays in the query so the accepted replacement consumes it.
same(detectMentionAt('see @"sunset beach.png"', 9), {at: 4, query: '"sun'}, 'a quoted token re-opens inside its first word');

// The pool is this turn's attachments first, then the conversation's assets
// (whose labels are id prefixes and carry the id).
const pool = [
  {label: 'sunset beach.png'},
  {label: 'img_ab12cd34', assetID: 'img_ab12cd34ef56'},
];
same(parseComposerMentions('use @img_ab12cd34 here', pool), [
  {text: 'use '},
  {text: '@img_ab12cd34', mention: {source: '@img_ab12cd34', label: 'img_ab12cd34', assetID: 'img_ab12cd34ef56'}},
  {text: ' here'},
], 'a bare asset token resolves with its asset id');
same(parseComposerMentions('put @"sunset beach.png" first', pool), [
  {text: 'put '},
  {text: '@"sunset beach.png"', mention: {source: '@"sunset beach.png"', label: 'sunset beach.png'}},
  {text: ' first'},
], 'a quoted token binds a multi-word attachment name and carries no asset id');
same(parseComposerMentions('@IMG_AB12CD34', pool), [
  {text: '@IMG_AB12CD34', mention: {source: '@IMG_AB12CD34', label: 'img_ab12cd34', assetID: 'img_ab12cd34ef56'}},
], 'matching is case-insensitive and reports the pool label');
same(parseComposerMentions('@clip.mp4', [{label: 'clip.mp4'}, {label: 'CLIP.mp4', assetID: 'vid_1'}]), [
  {text: '@clip.mp4', mention: {source: '@clip.mp4', label: 'clip.mp4'}},
], 'an attachment outranks a same-named asset (first wins)');
// Only references that will carry media are marked: prose like "ping @john"
// and an @ inside a word stay plain.
same(parseComposerMentions('ping @john and a@img_ab12cd34', pool), [
  {text: 'ping @john and a@img_ab12cd34'},
], 'unresolved tokens and mid-word @ stay plain text');
same(parseComposerMentions('x @"open @img_ab12cd34', pool), [
  {text: 'x @"open @img_ab12cd34'},
], 'an unclosed quote takes the rest as plain text');
same(parseComposerMentions('@ alone', pool), [{text: '@ alone'}], 'a bare @ binds nothing');
same(parseComposerMentions('', pool), [], 'empty text has no segments');
same(
  parseComposerMentions('@img_ab12cd34 @"sunset beach.png"', pool).map((segment) => segment.mention?.label ?? segment.text),
  ['img_ab12cd34', ' ', 'sunset beach.png'],
  'adjacent mentions keep the space between them',
);

// The menu filters candidates by case-insensitive substring, keeping order; an
// empty query lists everything (the menu is most useful right after '@').
function candidate(name: string): MentionCandidate {
  return {name, src: '', payload: '', kind: 'image'};
}
const items = [candidate('Sunset Beach.png'), candidate('clip.mp4'), candidate('img_ab12cd34')];
assert(mentionMatches('', items) === items, 'an empty query lists every candidate');
assert(mentionMatches('   ', items) === items, 'a blank query lists every candidate');
same(mentionMatches('beach', items).map((item) => item.name), ['Sunset Beach.png'], 'substring match, case-insensitive');
same(mentionMatches('i', items).map((item) => item.name), ['clip.mp4', 'img_ab12cd34'], 'matches keep candidate order');
same(mentionMatches('"sun', items).map((item) => item.name), ['Sunset Beach.png'], 'a re-opened quoted token matches without its quote');
same(mentionMatches(' CLIP ', items).map((item) => item.name), ['clip.mp4'], 'the query is trimmed');
same(mentionMatches('zzz', items), [], 'no match, no candidates');

// Asset tokens are self-describing id prefixes: the kind plus enough hex to be
// unique in practice, with no spaces to break token detection.
same(assetMentionLabel({id: 'img_0123456789abcdef'} as main.ConversationAsset), 'img_01234567', 'the token is the first 12 id characters');

// Origin turn ids read as human turn numbers; anything else passes through.
same(assetTurnLabel('turn_000003'), 'turn 3', 'origin turns read as human numbers');
same(assetTurnLabel('turn_000120'), 'turn 120', 'leading zeros drop');
same(assetTurnLabel('turn_000000'), 'turn_000000', 'turn zero is not a human turn number');
same(assetTurnLabel('import'), 'import', 'unparsable ids pass through');
