// Markdown document model: the pure logic behind the markdown editor. The
// document is plain text — there is no parse tree to hold — so the ops below
// transform {text, selection} tuples and the editor derives everything else
// (outline, stats, warnings) from the same text. Word counting mirrors the
// Go side's strings.Fields (markdown_editor.go) so the save summary and the
// editor's stats can never disagree. Exercised by
// test/run-markdown-model-tests.cjs like the other editor models.

export type MarkdownEditState = {
  text: string;
  selStart: number;
  selEnd: number;
};

export type MarkdownOutlineEntry = {
  level: number;
  title: string;
  line: number;
};

export type MarkdownStats = {
  words: number;
  chars: number;
  readingMinutes: number;
};

export type MarkdownWarning = {
  line: number;
  message: string;
};

// markdownWordCount mirrors strings.Fields: whitespace-delimited fields.
export function markdownWordCount(text: string): number {
  return text.split(/\s+/).filter(Boolean).length;
}

export function markdownStats(text: string): MarkdownStats {
  const words = markdownWordCount(text);
  return {
    words,
    chars: text.length,
    readingMinutes: words === 0 ? 0 : Math.max(1, Math.ceil(words / 200)),
  };
}

export function lineOfOffset(text: string, offset: number): number {
  return (text.slice(0, Math.min(offset, text.length)).match(/\n/g) || []).length;
}

export function offsetOfLine(text: string, line: number): number {
  const lines = text.split('\n');
  let offset = 0;
  for (let index = 0; index < Math.min(line, lines.length); index++) {
    offset += lines[index].length + 1;
  }
  return offset;
}

// lineRangeOfSelection maps a selection onto the [start, end] line indexes it
// touches — a selection ending at column 0 does not claim that line.
function lineRangeOfSelection(state: MarkdownEditState): [number, number] {
  const firstLine = lineOfOffset(state.text, state.selStart);
  let lastLine = firstLine;
  if (state.selEnd > state.selStart) {
    lastLine = lineOfOffset(state.text, state.selEnd);
    if (state.text[state.selEnd - 1] === '\n') {
      lastLine -= 1;
    }
  }
  return [firstLine, lastLine];
}

// mapSelectedLines rewrites every line the selection touches and keeps the
// selection pinned to those lines, stretched by whatever the rewrite added.
function mapSelectedLines(state: MarkdownEditState, transform: (line: string) => string): MarkdownEditState {
  const [firstLine, lastLine] = lineRangeOfSelection(state);
  const lines = state.text.split('\n');
  for (let index = firstLine; index <= lastLine; index++) {
    lines[index] = transform(lines[index]);
  }
  const text = lines.join('\n');
  const delta = text.length - state.text.length;
  return {text, selStart: state.selStart, selEnd: Math.max(state.selStart, state.selEnd + delta)};
}

const listMarkerRegex = /^(\s*)([-*+]|\d+[.)])\s+(\[[ xX]\]\s+)?/;

// wrapSelection toggles an inline marker around the selection. A selection
// already wrapped (outside or inside the markers) unwraps — a bare cursor
// between an empty pair unwraps too, cleaning the wrapper up. An empty
// selection with no wrapper inserts the markers around the placeholder and
// selects it, so typing replaces it.
export function wrapSelection(state: MarkdownEditState, marker: string, placeholder: string): MarkdownEditState {
  const {text, selStart, selEnd} = state;
  const before = text.slice(Math.max(0, selStart - marker.length), selStart);
  const after = text.slice(selEnd, selEnd + marker.length);
  if (before === marker && after === marker) {
    return {
      text: text.slice(0, selStart - marker.length) + text.slice(selStart, selEnd) + text.slice(selEnd + marker.length),
      selStart: selStart - marker.length,
      selEnd: selEnd - marker.length,
    };
  }
  const selected = text.slice(selStart, selEnd);
  const body = selected || placeholder;
  const next = text.slice(0, selStart) + marker + body + marker + text.slice(selEnd);
  if (selected) {
    return {text: next, selStart: selStart + marker.length, selEnd: selStart + marker.length + selected.length};
  }
  return {text: next, selStart: selStart + marker.length, selEnd: selStart + marker.length + placeholder.length};
}

// insertLink wraps the selection in [text](url) — or inserts the skeleton —
// and selects the URL placeholder so the next keystroke replaces it.
export function insertLink(state: MarkdownEditState): MarkdownEditState {
  const {text, selStart, selEnd} = state;
  const selected = text.slice(selStart, selEnd);
  const label = selected || 'link text';
  const snippet = `[${label}](url)`;
  const next = text.slice(0, selStart) + snippet + text.slice(selEnd);
  const urlStart = selStart + label.length + 3;
  return {text: next, selStart: urlStart, selEnd: urlStart + 3};
}

// cycleHeading rotates the selected lines through none → h1 → h2 → h3 → none.
// Deeper levels (h4-h6) are deliberate structure the cycle never touches — the
// anchor line's level decides the direction for the whole selection.
export function cycleHeading(state: MarkdownEditState): MarkdownEditState {
  const [firstLine] = lineRangeOfSelection(state);
  const anchor = state.text.split('\n')[firstLine];
  const current = anchor.match(/^(#{1,6})\s+/);
  const level = current ? current[1].length : 0;
  if (level > 3) {
    return state;
  }
  return mapSelectedLines(state, (line) => {
    const bare = line.replace(/^#{1,6}\s+/, '');
    if (level === 0) {
      return bare ? `# ${bare}` : line;
    }
    if (level >= 3) {
      return bare;
    }
    return `${'#'.repeat(level + 1)} ${bare}`;
  });
}

// toggleLineAffix adds a prefix to (or strips it from) every selected line —
// the blockquote and list toggles. Empty lines are left alone when adding:
// breaking a list with a blank line is the author's business.
export function toggleLineAffix(state: MarkdownEditState, prefix: string): MarkdownEditState {
  const lines = state.text.split('\n');
  const [firstLine, lastLine] = lineRangeOfSelection(state);
  let allAffixed = true;
  for (let index = firstLine; index <= lastLine; index++) {
    if (lines[index].trim() !== '' && !lines[index].startsWith(prefix)) {
      allAffixed = false;
      break;
    }
  }
  return mapSelectedLines(state, (line) => {
    if (allAffixed) {
      return line.startsWith(prefix) ? line.slice(prefix.length) : line;
    }
    return line.trim() === '' || line.startsWith(prefix) ? line : prefix + line;
  });
}

// continueListOnEnter handles Enter inside a list item: a carried marker
// continues the list (ordered numbers increment, tasks reset), while Enter on
// an empty item exits the list by stripping its marker. Returns null when the
// cursor is not in a list — the caller inserts a plain newline.
export function continueListOnEnter(state: MarkdownEditState): MarkdownEditState | null {
  const currentLine = lineOfOffset(state.text, state.selStart);
  const line = state.text.split('\n')[currentLine];
  const match = line.match(listMarkerRegex);
  if (!match) {
    return null;
  }
  const [, indent, marker, task] = match;
  const rest = line.slice(match[0].length);
  if (rest.trim() === '') {
    // Empty item: exit the list by deleting the bare marker.
    const lineStart = offsetOfLine(state.text, currentLine);
    const next = state.text.slice(0, lineStart) + indent + state.text.slice(lineStart + line.length);
    const cursor = lineStart + indent.length;
    return {text: next, selStart: cursor, selEnd: cursor};
  }
  let nextMarker = marker;
  const ordered = marker.match(/^(\d+)([.)])$/);
  if (ordered) {
    nextMarker = `${Number(ordered[1]) + 1}${ordered[2]}`;
  }
  const insertion = `\n${indent}${nextMarker} ${task ? '[ ] ' : ''}`;
  const next = state.text.slice(0, state.selStart) + insertion + state.text.slice(state.selEnd);
  const cursor = state.selStart + insertion.length;
  return {text: next, selStart: cursor, selEnd: cursor};
}

// indentSelection indents (or outdents) the selected lines by two spaces, but
// only when the selection sits inside a list or blockquote — outside one, Tab
// stays the browser's move-focus key and the caller does nothing. Returns
// null when the op does not apply.
export function indentSelection(state: MarkdownEditState, outdent: boolean): MarkdownEditState | null {
  const [firstLine] = lineRangeOfSelection(state);
  const first = state.text.split('\n')[firstLine];
  if (!listMarkerRegex.test(first) && !first.startsWith('>')) {
    return null;
  }
  return mapSelectedLines(state, (line) => {
    if (outdent) {
      return line.replace(/^ {1,2}/, '');
    }
    return line === '' ? line : `  ${line}`;
  });
}

// insertBlock wraps the cursor in a fenced code block and leaves it on the
// empty line inside, ready to type.
export function insertCodeFence(state: MarkdownEditState, language: string): MarkdownEditState {
  const fence = '```';
  const snippet = `\n${fence}${language}\n\n${fence}\n`;
  const next = state.text.slice(0, state.selStart) + snippet + state.text.slice(state.selEnd);
  const cursor = state.selStart + fence.length + language.length + 2;
  return {text: next, selStart: cursor, selEnd: cursor};
}

export function insertBlockSnippet(state: MarkdownEditState, snippet: string): MarkdownEditState {
  const next = state.text.slice(0, state.selStart) + snippet + state.text.slice(state.selEnd);
  return {text: next, selStart: state.selStart + snippet.length, selEnd: state.selStart + snippet.length};
}

export function insertTable(state: MarkdownEditState): MarkdownEditState {
  const table = '\n| Column | Column |\n| --- | --- |\n| | |\n';
  const next = state.text.slice(0, state.selStart) + table + state.text.slice(state.selEnd);
  const cursor = state.selStart + '\n| '.length;
  return {text: next, selStart: cursor, selEnd: cursor};
}

const fenceOpenRegex = /^\s{0,3}(```+|~~~+).*/;
const fenceCloseRegex = /^\s{0,3}(```+|~~~+)\s*$/;

// openFenceOn returns the fence marker a line opens (or null) — the marker's
// first character and length decide what closes it, per CommonMark.
function openFenceOn(line: string): {char: string; length: number} | null {
  const match = line.match(fenceOpenRegex);
  if (!match) {
    return null;
  }
  return {char: match[1][0], length: match[1].length};
}

function sameFence(fence: {char: string; length: number}, marker: {char: string; length: number}): boolean {
  return fence.char === marker.char && marker.length >= fence.length;
}

// markdownOutline lists the ATX headings outside code fences — the inspector's
// clickable document map.
export function markdownOutline(text: string): MarkdownOutlineEntry[] {
  const entries: MarkdownOutlineEntry[] = [];
  const lines = text.split('\n');
  let fence: {char: string; length: number} | null = null;
  lines.forEach((line, index) => {
    const marker = openFenceOn(line);
    if (fence) {
      if (marker && sameFence(fence, marker) && fenceCloseRegex.test(line)) {
        fence = null;
      }
      return;
    }
    if (marker) {
      fence = marker;
      return;
    }
    const heading = line.match(/^(#{1,6})\s+(.*)$/);
    if (heading) {
      entries.push({level: heading[1].length, title: heading[2].replace(/\s+#+\s*$/, '').trim(), line: index});
    }
  });
  return entries;
}

// markdownWarnings is the review checklist — conservative by design, so every
// entry is a real defect rather than a style opinion: an unclosed code fence
// (everything after it renders as code), a heading level that skips (h2
// followed by h4), and leftover TODO/FIXME markers.
export function markdownWarnings(text: string): MarkdownWarning[] {
  const warnings: MarkdownWarning[] = [];
  const lines = text.split('\n');
  let fence: {char: string; length: number} | null = null;
  let previousLevel = 0;
  lines.forEach((line, index) => {
    const number = index + 1;
    const marker = openFenceOn(line);
    if (fence) {
      if (marker && sameFence(fence, marker) && fenceCloseRegex.test(line)) {
        fence = null;
      }
      return;
    }
    if (marker) {
      fence = marker;
      return;
    }
    if (/\b(TODO|FIXME)\b/.test(line)) {
      warnings.push({line: number, message: `line ${number}: ${line.trim().match(/\b(?:TODO|FIXME)\b[^—.]{0,40}/)?.[0].trim() ?? 'TODO marker'} left in the document`});
    }
    const heading = line.match(/^(#{1,6})\s/);
    if (heading) {
      const level = heading[1].length;
      if (previousLevel > 0 && level > previousLevel + 1) {
        warnings.push({line: number, message: `line ${number}: heading level skips from h${previousLevel} to h${level}`});
      }
      previousLevel = level;
    }
  });
  if (fence) {
    warnings.push({line: lines.length, message: 'a code fence is never closed — everything after it renders as code'});
  }
  return warnings;
}

// findMatches returns the non-overlapping start offsets of every literal,
// case-sensitive occurrence of query — [] when the query is empty (the find
// field's no-op). Literal by contract, like replaceLiteral below: no regex
// surprises in a review tool.
export function findMatches(text: string, query: string): number[] {
  if (!query) {
    return [];
  }
  const matches: number[] = [];
  let at = text.indexOf(query);
  while (at >= 0) {
    matches.push(at);
    at = text.indexOf(query, at + query.length);
  }
  return matches;
}

// replaceLiteral applies a literal replacement across the whole document and
// reports how many substitutions landed — the count is the review affordance
// ("9 replaced") before committing.
export function replaceLiteral(text: string, find: string, replace: string): {text: string; count: number} {
  if (!find) {
    return {text, count: 0};
  }
  const parts = text.split(find);
  return {text: parts.join(replace), count: parts.length - 1};
}

// nextMatch picks the first match strictly after the caret, wrapping to the
// first — -1 when there are none. Strictly after, so Next from a match's own
// start advances instead of re-selecting it.
export function nextMatch(offsets: number[], caret: number): number {
  if (!offsets.length) {
    return -1;
  }
  for (const at of offsets) {
    if (at > caret) {
      return at;
    }
  }
  return offsets[0];
}

// previousMatch picks the last match strictly before the caret, wrapping to
// the last — -1 when there are none.
export function previousMatch(offsets: number[], caret: number): number {
  if (!offsets.length) {
    return -1;
  }
  for (let index = offsets.length - 1; index >= 0; index--) {
    if (offsets[index] < caret) {
      return offsets[index];
    }
  }
  return offsets[offsets.length - 1];
}
