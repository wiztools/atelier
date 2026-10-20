// Subtitle document model: the pure logic behind the subtitle editor. The
// editor holds one SubtitleDoc and derives everything from the ops below;
// undo/redo is full-doc snapshots, so every op returns a new doc. This module
// mirrors the Go rules (subtitle.go's parser semantics and
// subtitleCueWarnings) and is exercised by test/run-subtitle-model-tests.cjs
// like the other editor models.

export type SubtitleFormat = 'vtt' | 'srt';

export type SubtitleCue = {
  startSeconds: number;
  endSeconds: number;
  text: string;
};

export type SubtitleDoc = {
  format: SubtitleFormat;
  cues: SubtitleCue[];
};

export type SubtitleWarning = {
  index: number;
  message: string;
};

export function cloneSubtitleDoc(doc: SubtitleDoc): SubtitleDoc {
  return {
    format: doc.format,
    cues: doc.cues.map((cue) => ({...cue})),
  };
}

// sameSubtitleDoc is the dirty check: JSON equality over the cue set. Format
// participates — flipping the export target marks the doc dirty.
export function sameSubtitleDoc(a: SubtitleDoc, b: SubtitleDoc): boolean {
  return JSON.stringify(a) === JSON.stringify(b);
}

function withCues(doc: SubtitleDoc, cues: SubtitleCue[]): SubtitleDoc {
  return {format: doc.format, cues};
}

function withCue(doc: SubtitleDoc, index: number, cue: SubtitleCue): SubtitleDoc {
  const cues = doc.cues.map((current, i) => (i === index ? cue : {...current}));
  return withCues(doc, cues);
}

export function updateCueText(doc: SubtitleDoc, index: number, text: string): SubtitleDoc {
  const cue = doc.cues[index];
  if (!cue) return cloneSubtitleDoc(doc);
  return withCue(doc, index, {...cue, text});
}

export function updateCueTimes(doc: SubtitleDoc, index: number, startSeconds: number, endSeconds: number): SubtitleDoc {
  const cue = doc.cues[index];
  if (!cue) return cloneSubtitleDoc(doc);
  return withCue(doc, index, {startSeconds, endSeconds, text: cue.text});
}

// nudgeCueEdge moves one edge by delta. A start pushed past its end (or an
// end pulled before its start) clamps to a zero-length cue at the other edge
// rather than inverting.
export function nudgeCueEdge(doc: SubtitleDoc, index: number, edge: 'start' | 'end', delta: number): SubtitleDoc {
  const cue = doc.cues[index];
  if (!cue) return cloneSubtitleDoc(doc);
  if (edge === 'start') {
    const start = Math.max(0, cue.startSeconds + delta);
    return withCue(doc, index, {...cue, startSeconds: Math.min(start, cue.endSeconds)});
  }
  const end = Math.max(cue.startSeconds, cue.endSeconds + delta);
  return withCue(doc, index, {...cue, endSeconds: end});
}

// shiftAllCues moves every cue by offset (seconds; negative moves earlier).
// Starts clamp at zero preserving each cue's duration — shifting earlier than
// the first frame cannot produce negative times.
export function shiftAllCues(doc: SubtitleDoc, offset: number): SubtitleDoc {
  const cues = doc.cues.map((cue) => {
    if (offset >= 0) {
      return {...cue, startSeconds: cue.startSeconds + offset, endSeconds: cue.endSeconds + offset};
    }
    const start = Math.max(0, cue.startSeconds + offset);
    const duration = Math.max(0, cue.endSeconds - cue.startSeconds);
    return {...cue, startSeconds: start, endSeconds: start + duration};
  });
  return withCues(doc, cues);
}

// splitCueText cuts caption text at the word boundary nearest ratio (0..1) —
// a time-proportional split point. A single-word cue puts the whole text in
// the first half; the empty second half is the reviewer's cue to fill.
export function splitCueText(text: string, ratio: number): [string, string] {
  const words = text.trim().split(/\s+/).filter(Boolean);
  if (words.length <= 1) {
    return [text, ''];
  }
  const cut = Math.min(words.length - 1, Math.max(1, Math.round(ratio * words.length)));
  return [words.slice(0, cut).join(' '), words.slice(cut).join(' ')];
}

// splitSubtitleCue divides the cue at atSeconds (clamped inside it). Text
// splits at the word boundary nearest the time's proportional position.
export function splitSubtitleCue(doc: SubtitleDoc, index: number, atSeconds: number): SubtitleDoc {
  const cue = doc.cues[index];
  if (!cue) return cloneSubtitleDoc(doc);
  const at = Math.min(Math.max(atSeconds, cue.startSeconds), cue.endSeconds);
  const span = Math.max(cue.endSeconds - cue.startSeconds, 0);
  const ratio = span > 0 ? (at - cue.startSeconds) / span : 0.5;
  const [first, second] = splitCueText(cue.text, ratio);
  const cues = doc.cues.flatMap((current, i) =>
    i === index
      ? [
          {startSeconds: cue.startSeconds, endSeconds: at, text: first},
          {startSeconds: at, endSeconds: cue.endSeconds, text: second},
        ]
      : [{...current}],
  );
  return withCues(doc, cues);
}

// mergeCueWithNext folds cue index and its successor into one: the times span
// both, the texts join with a space. The last cue has no next and is a no-op.
export function mergeCueWithNext(doc: SubtitleDoc, index: number): SubtitleDoc {
  const cue = doc.cues[index];
  const next = doc.cues[index + 1];
  if (!cue || !next) return cloneSubtitleDoc(doc);
  const merged: SubtitleCue = {
    startSeconds: Math.min(cue.startSeconds, next.startSeconds),
    endSeconds: Math.max(cue.endSeconds, next.endSeconds),
    text: cue.text.trim() ? `${cue.text.replace(/\s+$/, '')} ${next.text.replace(/^\s+/, '')}` : next.text,
  };
  const cues = doc.cues.flatMap((current, i) => (i === index ? [merged] : i === index + 1 ? [] : [{...current}]));
  return withCues(doc, cues);
}

// insertSubtitleCueAfter adds a zero-length cue after index, starting where
// the previous one ends (or at 0 above the top / into an empty list) — the
// reviewer stretches it into place.
export function insertSubtitleCueAfter(doc: SubtitleDoc, index: number): SubtitleDoc {
  const cue = doc.cues[index];
  const start = cue ? cue.endSeconds : 0;
  const fresh: SubtitleCue = {startSeconds: start, endSeconds: start, text: ''};
  if (!cue) {
    return withCues(doc, [fresh, ...doc.cues.map((current) => ({...current}))]);
  }
  const cues = doc.cues.flatMap((current, i) => (i === index ? [{...current}, fresh] : [{...current}]));
  return withCues(doc, cues);
}

export function deleteSubtitleCue(doc: SubtitleDoc, index: number): SubtitleDoc {
  if (!doc.cues[index]) return cloneSubtitleDoc(doc);
  return withCues(
    doc,
    doc.cues.filter((_, i) => i !== index),
  );
}

// replaceAllInCues applies a literal (non-regex) replacement across every cue
// and reports how many substitutions landed — the count is the review
// affordance ("9 replaced") before committing the doc.
export function replaceAllInCues(doc: SubtitleDoc, find: string, replace: string): {doc: SubtitleDoc; count: number} {
  if (!find) {
    return {doc: cloneSubtitleDoc(doc), count: 0};
  }
  let count = 0;
  const cues = doc.cues.map((cue) => {
    const parts = cue.text.split(find);
    if (parts.length < 2) {
      return {...cue};
    }
    count += parts.length - 1;
    return {...cue, text: parts.join(replace)};
  });
  return {doc: withCues(doc, cues), count};
}

// subtitleWarnings is the review checklist — the frontend twin of
// subtitleCueWarnings in subtitle_editor.go. Order follows the cue list, and
// each warning carries its cue index so the UI can scroll to it.
export function subtitleWarnings(doc: SubtitleDoc): SubtitleWarning[] {
  const warnings: SubtitleWarning[] = [];
  doc.cues.forEach((cue, index) => {
    const label = `cue ${index + 1}`;
    if (cue.endSeconds < cue.startSeconds) {
      warnings.push({index, message: `${label}: end is before its start`});
      return;
    }
    if (cue.startSeconds < 0) {
      warnings.push({index, message: `${label}: negative start time`});
    }
    const duration = cue.endSeconds - cue.startSeconds;
    const chars = cue.text.trim().length;
    if (duration > 0 && chars / duration > 20) {
      warnings.push({index, message: `${label}: reading speed is high (${chars} chars in ${duration.toFixed(1)}s)`});
    }
    if (duration < 0.5) {
      warnings.push({index, message: `${label}: shorter than half a second`});
    }
    const previous = doc.cues[index - 1];
    if (previous && cue.startSeconds < previous.endSeconds) {
      warnings.push({index, message: `${label}: overlaps the previous cue`});
    }
  });
  return warnings;
}

// activeSubtitleIndex maps playback time onto the cue list. The session sync
// offset is applied here — mediaSeconds - offset lands in the cue's window —
// so the picker's ± control never mutates the document. -1 when nothing is
// active (a gap, or no media linked).
export function activeSubtitleIndex(doc: SubtitleDoc, mediaSeconds: number, offsetSeconds: number): number {
  const cueSeconds = mediaSeconds - offsetSeconds;
  for (let index = 0; index < doc.cues.length; index++) {
    const cue = doc.cues[index];
    if (cueSeconds >= cue.startSeconds && cueSeconds < cue.endSeconds) {
      return index;
    }
  }
  return -1;
}

// formatSubtitleClock renders a time the way the file format writes it —
// dot-decimal for VTT, comma for SRT. Display-only (the list's time fields);
// hours always present, matching both formats' canonical form.
export function formatSubtitleClock(seconds: number, format: SubtitleFormat): string {
  const safe = Number.isFinite(seconds) && seconds > 0 ? seconds : 0;
  const millis = Math.round(safe * 1000);
  const hh = String(Math.floor(millis / 3600000)).padStart(2, '0');
  const mm = String(Math.floor(millis / 60000) % 60).padStart(2, '0');
  const ss = String(Math.floor(millis / 1000) % 60).padStart(2, '0');
  const mmm = String(millis % 1000).padStart(3, '0');
  return `${hh}:${mm}:${ss}${format === 'srt' ? ',' : '.'}${mmm}`;
}

// The linked-media picker's row shape: only the fields the provenance label
// reads, so the model stays free of the wailsjs bindings (main.ConversationAsset
// satisfies it structurally).
export type LinkableMediaAsset = {
  conversationId: string;
  conversationTitle?: string;
};

// linkedMediaProvenance names one picker row's source chat. The edited
// conversation's own assets read "this chat" even when its title is blank —
// an untitled conversation must not look like foreign media; a different
// conversation's asset (the library fold) names that conversation, with
// "another chat" reserved for cross-conversation rows whose title is unknown.
export function linkedMediaProvenance(asset: LinkableMediaAsset, editedConversationID: string): string {
  if (asset.conversationId === editedConversationID) {
    return 'this chat';
  }
  return asset.conversationTitle || 'another chat';
}
