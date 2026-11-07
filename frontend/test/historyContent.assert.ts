import type {main} from '../wailsjs/go/models';
import {
  historyAudios,
  historyComics,
  historyCompositions,
  historyImages,
  historyMarkdowns,
  historySubtitles,
  historyText,
  historyTranscripts,
  historyVideos,
  transcriptChipLabel,
} from '../src/historyContent';

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message);
}

function same(actual: unknown, expected: unknown, message: string): void {
  const got = JSON.stringify(actual);
  const want = JSON.stringify(expected);
  if (got !== want) throw new Error(`${message}: expected ${want}, got ${got}`);
}

// content builds one persisted turn part — the generated HistoryContent model
// is a plain-field class, so its wire shape is all a reader sees.
function content(type: string, fields: {text?: string; path?: string} = {}): main.HistoryContent {
  return {type, ...fields};
}

// Text parts join with a blank line; other kinds and empty parts drop out, and
// a turn without a content list reads as empty text.
const textTurn = [
  content('text', {text: 'first'}),
  content('image', {text: '/atelier-artifact/c/img_1.png'}),
  content('text', {text: ''}),
  content('text', {text: 'second'}),
  content('thinking', {text: 'pondering'}),
];
same(historyText(textTurn, 'text'), 'first\n\nsecond', 'text parts join with a blank line');
same(historyText(textTurn, 'thinking'), 'pondering', 'the type argument selects the part kind');
same(historyText(null, 'text'), '', 'a null content list reads as empty');
same(historyText(undefined, 'text'), '', 'an absent content list reads as empty');

// Media readers keep their own kind, prefer the hydrated URL (text) over the
// on-disk path, and skip parts that carry neither.
const mediaTurn = [
  content('image', {text: '/atelier-artifact/c/img_1.png', path: '/data/c/img_1.png'}),
  content('image', {path: '/data/c/img_2.png'}),
  content('image'),
  content('video', {text: '/atelier-artifact/c/vid_1.mp4'}),
  content('audio', {text: '/atelier-artifact/c/aud_1.m4a'}),
  content('transcript', {text: '/atelier-artifact/c/trc_1.vtt'}),
  content('transcript', {text: '/atelier-artifact/c/trc_2.txt'}),
];
same(historyImages(mediaTurn), ['/atelier-artifact/c/img_1.png', '/data/c/img_2.png'], 'images: URL first, path fallback, empties dropped');
same(historyVideos(mediaTurn), ['/atelier-artifact/c/vid_1.mp4'], 'videos keep their own kind');
same(historyAudios(mediaTurn), ['/atelier-artifact/c/aud_1.m4a'], 'audios keep their own kind');
same(historyTranscripts(mediaTurn), ['/atelier-artifact/c/trc_1.vtt', '/atelier-artifact/c/trc_2.txt'], 'transcripts include plain .txt');
same(historyImages(null), [], 'a null content list has no images');

// Subtitles are the .vtt/.srt transcripts only, in any case; a turn without
// one reads undefined (not []) so the hydrated entry omits the field.
same(historySubtitles([
  content('transcript', {text: '/atelier-artifact/c/trc_1.vtt'}),
  content('transcript', {path: '/data/c/trc_2.SRT'}),
  content('transcript', {text: '/atelier-artifact/c/trc_3.txt'}),
  content('image', {text: '/atelier-artifact/c/img_1.vtt'}),
]), ['/atelier-artifact/c/trc_1.vtt', '/data/c/trc_2.SRT'], 'subtitles are the vtt/srt transcripts');
assert(historySubtitles([content('transcript', {text: '/atelier-artifact/c/trc_3.txt'})]) === undefined, 'a plain transcript is not a subtitle');
assert(historySubtitles(null) === undefined, 'no content, no subtitles');

// Documents match both dialects by extension; compositions and comics are
// their .json revisions. Each reads undefined when the turn has none.
same(historyMarkdowns([
  content('markdown', {text: '/atelier-artifact/c/md_1.md'}),
  content('markdown', {text: '/atelier-artifact/c/md_2.Fountain'}),
  content('markdown', {text: '/atelier-artifact/c/notes.txt'}),
]), ['/atelier-artifact/c/md_1.md', '/atelier-artifact/c/md_2.Fountain'], 'markdown keeps .md and .fountain');
assert(historyMarkdowns([content('markdown', {text: '/atelier-artifact/c/notes.txt'})]) === undefined, 'other extensions are not documents');
same(historyCompositions([
  content('composition', {text: '/atelier-artifact/c/cmp_1.json'}),
  content('composition', {text: '/atelier-artifact/c/cmp_1.mp4'}),
]), ['/atelier-artifact/c/cmp_1.json'], 'compositions are their json revisions');
same(historyComics([content('comic', {path: '/data/c/cbc_1.JSON'})]), ['/data/c/cbc_1.JSON'], 'comics match .json in any case');
assert(historyComics([content('composition', {text: '/atelier-artifact/c/cmp_1.json'})]) === undefined, 'a composition is not a comic');

// The transcript chip names the artifact's kind from its extension.
same(transcriptChipLabel('/atelier-artifact/c/trc_1.vtt'), '📄 Timestamped transcript (.vtt)', 'vtt label');
same(transcriptChipLabel('/atelier-artifact/c/trc_1.SRT'), '📄 Subtitles (.srt)', 'srt label, any case');
same(transcriptChipLabel('/atelier-artifact/c/trc_1.txt'), '📄 Transcript (.txt)', 'plain transcripts');
