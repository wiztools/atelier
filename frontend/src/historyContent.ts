// Persisted-turn readers: pick a hydrated turn's text and its artifact URLs
// out of its HistoryContent list by kind — what the transcript renders when
// a conversation reopens — plus the transcript chip's label.

import type {main} from '../wailsjs/go/models';
import {asArray} from './utils';

function historyText(contents: main.HistoryContent[] | null | undefined, type: string): string {
  return asArray(contents)
    .filter((content) => content.type === type)
    .map((content) => content.text ?? '')
    .filter(Boolean)
    .join('\n\n');
}

function historyImages(contents: main.HistoryContent[] | null | undefined): string[] {
  return asArray(contents)
    .filter((content) => content.type === 'image')
    .map((content) => content.text || content.path || '')
    .filter(Boolean);
}

function historyVideos(contents: main.HistoryContent[] | null | undefined): string[] {
  return asArray(contents)
    .filter((content) => content.type === 'video')
    .map((content) => content.text || content.path || '')
    .filter(Boolean);
}

function historyAudios(contents: main.HistoryContent[] | null | undefined): string[] {
  return asArray(contents)
    .filter((content) => content.type === 'audio')
    .map((content) => content.text || content.path || '')
    .filter(Boolean);
}

function historyTranscripts(contents: main.HistoryContent[] | null | undefined): string[] {
  return asArray(contents)
    .filter((content) => content.type === 'transcript')
    .map((content) => content.text || content.path || '')
    .filter(Boolean);
}

// historySubtitles picks the SUBTITLE artifacts (.vtt/.srt) off a user turn's
// content — the hydrated /atelier-artifact URLs attached subtitle files
// persist as. Plain .txt transcripts are not subtitles and never match, and
// assistant-produced transcript artifacts stay in `transcripts` (they render
// there), which is why the caller guards this to user turns.
function historySubtitles(contents: main.HistoryContent[] | null | undefined): string[] | undefined {
  const urls = asArray(contents)
    .filter((content) => {
      if (content.type !== 'transcript') return false;
      const path = content.text || content.path || '';
      return path.toLowerCase().endsWith('.vtt') || path.toLowerCase().endsWith('.srt');
    })
    .map((content) => content.text || content.path || '')
    .filter(Boolean);
  return urls.length ? urls : undefined;
}

// historyMarkdowns picks the markdown artifacts off a turn's content — the
// hydrated /atelier-artifact URLs md_ documents persist as, on either side of
// the conversation (attached by the user, saved by the markdown editor). Both
// dialects match — .md and .fountain — the same extension set
// markdownArtifactIDFromURL gates the chip's Edit button on.
function historyMarkdowns(contents: main.HistoryContent[] | null | undefined): string[] | undefined {
  const urls = asArray(contents)
    .filter((content) => content.type === 'markdown')
    .map((content) => content.text || content.path || '')
    .filter((path) => /\.(md|fountain)$/i.test(path));
  return urls.length ? urls : undefined;
}

// historyCompositions picks the composition project documents off a turn's
// content — the hydrated /atelier-artifact URLs cmp_ revisions persist as,
// one entry per save. The chip's Edit door reopens that revision in the
// video composer.
function historyCompositions(contents: main.HistoryContent[] | null | undefined): string[] | undefined {
  const urls = asArray(contents)
    .filter((content) => content.type === 'composition')
    .map((content) => content.text || content.path || '')
    .filter((path) => /\.json$/i.test(path));
  return urls.length ? urls : undefined;
}

// historyComics picks the comic book documents off a turn's content — the
// hydrated /atelier-artifact URLs cbc_ revisions persist as, one entry per
// save. The chip's Edit door reopens that revision in the comic editor.
function historyComics(contents: main.HistoryContent[] | null | undefined): string[] | undefined {
  const urls = asArray(contents)
    .filter((content) => content.type === 'comic')
    .map((content) => content.text || content.path || '')
    .filter((path) => /\.json$/i.test(path));
  return urls.length ? urls : undefined;
}

// Transcript artifacts come in three kinds — the WebVTT rendering of a
// timestamped transcript (.vtt), its converted SubRip form (.srt, from
// convert_subtitles), and an oversized plain transcript staged as text (.txt).
// The URL's extension says which; the chip label follows it.
function transcriptChipLabel(url: string): string {
  const lower = url.toLowerCase();
  if (lower.endsWith('.vtt')) return '📄 Timestamped transcript (.vtt)';
  if (lower.endsWith('.srt')) return '📄 Subtitles (.srt)';
  return '📄 Transcript (.txt)';
}

export {
  historyText,
  historyImages,
  historyVideos,
  historyAudios,
  historyTranscripts,
  historySubtitles,
  historyMarkdowns,
  historyCompositions,
  historyComics,
  transcriptChipLabel,
};
