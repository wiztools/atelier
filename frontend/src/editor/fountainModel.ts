// Fountain document model: the pure logic behind the markdown editor's
// screenplay dialect. The parse below mirrors fountain.go line for line —
// the TS side needs it LIVE (preview, outline, warnings, the Tab/Enter
// element model), the Go side only when an export renders, and the two must
// never disagree about what an element is. Like the Go parser it is
// generous: every line resolves to some element, nothing fails.
// Exercised by test/run-fountain-model-tests.cjs like the other editor
// models.

import type {MarkdownEditState} from './markdownModel';

export type FountainElementKind =
  | 'Scene Heading'
  | 'Action'
  | 'Character'
  | 'Parenthetical'
  | 'Dialogue'
  | 'Transition'
  | 'Lyric'
  | 'Section'
  | 'Synopsis'
  | 'Page Break'
  | 'Centered';

export type FountainElement = {
  kind: FountainElementKind;
  text: string;
  line: number; // 1-based
  character?: string;
};

export type FountainScript = {
  elements: FountainElement[];
  titlePage: Record<string, string>;
};

const SCENE_PREFIXES = ['INT.', 'EXT.', 'INT ', 'EXT ', 'EST ', 'INT./EXT.', 'EXT./INT.', 'INT/EXT', 'EXT/INT', 'I/E ', 'I/E.'];

function isUppercaseLine(line: string): boolean {
  let letters = false;
  for (const ch of line) {
    const lower = ch.toLowerCase();
    const upper = ch.toUpperCase();
    if (lower !== upper) {
      letters = true;
      if (ch !== upper) {
        return false;
      }
    }
  }
  return letters;
}

function isSceneHeading(line: string): boolean {
  const upper = line.toUpperCase();
  if (['INT', 'EXT', 'EST', 'I/E'].includes(upper)) {
    return true;
  }
  return SCENE_PREFIXES.some((prefix) => upper.startsWith(prefix));
}

function isTransitionCue(line: string): boolean {
  // "TO:" alone (length 3) is not a transition — mirror fountain.go's
  // minimum of four characters.
  return line.endsWith('TO:') && line.length >= 4 && isUppercaseLine(line.slice(0, -3));
}

function isSectionMarker(line: string): boolean {
  const hashes = line.match(/^#{1,6}/);
  if (!hashes || hashes[0].length >= line.length) {
    return false;
  }
  const next = line[hashes[0].length];
  return next === ' ' || next === '\t';
}

// The standard title-page keys — the guard that keeps "CUT TO:" from being
// eaten as a title page when it opens a script (mirrors fountain.go).
const TITLE_KEYS = new Set([
  'title', 'credit', 'author', 'authors', 'source', 'draft date', 'contact', 'copyright', 'notes',
]);

function titleKey(line: string): [string, string] | null {
  const trimmed = line.trim();
  const colon = trimmed.indexOf(':');
  if (colon <= 0) {
    return null;
  }
  const key = trimmed.slice(0, colon).trim();
  if (!/^[a-zA-Z0-9 ]+$/.test(key) || !TITLE_KEYS.has(key.toLowerCase())) {
    return null;
  }
  return [key, trimmed.slice(colon + 1).trim()];
}

// fountainCharacterName strips a cue's extension — (CONT'D), (V.O.) and
// friends — so "SUBHASH (V.O.)" and "SUBHASH" resolve to one character.
export function fountainCharacterName(cue: string): string {
  let name = cue.trim();
  for (;;) {
    const open = name.lastIndexOf('(');
    if (open < 0) {
      break;
    }
    const close = name.lastIndexOf(')');
    if (close < open) {
      break;
    }
    const extension = name.slice(open + 1, close).trim().toUpperCase().replace(/’/g, "'");
    if (["CONT'D", 'V.O.', 'O.S.', 'O.C.', 'PRE-LAP', 'FILTERED', 'ON SCREEN', 'OFF SCREEN'].includes(extension)) {
      name = name.slice(0, open).trim();
    } else {
      break;
    }
  }
  return name;
}

export function fountainParse(text: string): FountainScript {
  const lines = text.replace(/\r\n/g, '\n').split('\n');
  const elements: FountainElement[] = [];
  const titlePage: Record<string, string> = {};

  let i = 0;
  // Title page: `Key: value` lines from the very top, values continuing on
  // indented lines, ended by the first blank line.
  for (; i < lines.length; i++) {
    const line = lines[i];
    if (line.trim() === '') {
      i++;
      break;
    }
    const key = titleKey(line);
    if (!key) {
      break;
    }
    let value = key[1];
    i++;
    for (; i < lines.length; i++) {
      const cont = lines[i];
      if (cont.trim() === '' || (cont[0] !== ' ' && cont[0] !== '\t')) {
        break;
      }
      value += ' ' + cont.trim();
    }
    titlePage[key[0]] = value.trim();
    i--;
  }

  let expectSpeech = false;
  let speaker = '';
  for (; i < lines.length; i++) {
    const lineNo = i + 1;
    const trimmed = lines[i].trim();
    if (trimmed === '') {
      expectSpeech = false;
      continue;
    }
    let element: FountainElement;
    if (/^={3,}$/.test(trimmed)) {
      element = {kind: 'Page Break', text: trimmed, line: lineNo};
      expectSpeech = false;
    } else if (trimmed.startsWith('>') && trimmed.endsWith('<') && trimmed.length > 1) {
      element = {kind: 'Centered', text: trimmed.slice(1, -1).trim(), line: lineNo};
      expectSpeech = false;
    } else if (trimmed.startsWith('>')) {
      element = {kind: 'Transition', text: trimmed.slice(1).trim(), line: lineNo};
      expectSpeech = false;
    } else if (trimmed.startsWith('.') && trimmed.length > 1 && trimmed[1] !== '.') {
      element = {kind: 'Scene Heading', text: trimmed.slice(1).trim(), line: lineNo};
      expectSpeech = false;
    } else if (trimmed.startsWith('@')) {
      const cue = trimmed.slice(1).trim();
      element = {kind: 'Character', text: cue, line: lineNo};
      expectSpeech = true;
      speaker = fountainCharacterName(cue);
    } else if (trimmed.startsWith('!')) {
      element = {kind: 'Action', text: trimmed.slice(1).trim(), line: lineNo};
      expectSpeech = false;
    } else if (trimmed.startsWith('#') && isSectionMarker(trimmed)) {
      element = {kind: 'Section', text: trimmed.replace(/^#+\s*/, ''), line: lineNo};
      expectSpeech = false;
    } else if (trimmed.startsWith('=')) {
      element = {kind: 'Synopsis', text: trimmed.slice(1).trim(), line: lineNo};
      expectSpeech = false;
    } else if (trimmed.startsWith('~')) {
      element = {kind: 'Lyric', text: trimmed.slice(1).trim(), line: lineNo};
      expectSpeech = false;
    } else if (!expectSpeech && isSceneHeading(trimmed)) {
      element = {kind: 'Scene Heading', text: trimmed, line: lineNo};
    } else if (!expectSpeech && isTransitionCue(trimmed)) {
      element = {kind: 'Transition', text: trimmed, line: lineNo};
    } else if (!expectSpeech && i + 1 < lines.length && lines[i + 1].trim() !== '' && isUppercaseLine(trimmed)) {
      element = {kind: 'Character', text: trimmed, line: lineNo};
      expectSpeech = true;
      speaker = fountainCharacterName(trimmed);
    } else if (expectSpeech && trimmed.startsWith('(') && trimmed.endsWith(')') && trimmed.length > 1) {
      element = {kind: 'Parenthetical', text: trimmed, line: lineNo, character: speaker};
    } else if (expectSpeech) {
      element = {kind: 'Dialogue', text: trimmed, line: lineNo, character: speaker};
    } else {
      element = {kind: 'Action', text: trimmed, line: lineNo};
      expectSpeech = false;
    }
    elements.push(element);
  }
  return {elements, titlePage};
}

// fountainOutline is the sidebar's document map: scene headings at the top
// level, sections one step in (depth re-read from the raw line — the parse
// strips the markers).
export function fountainOutline(text: string): Array<{level: number; title: string; line: number}> {
  const rawLines = text.replace(/\r\n/g, '\n').split('\n');
  return fountainParse(text)
    .elements.filter((element) => element.kind === 'Scene Heading' || element.kind === 'Section')
    .map((element) => {
      if (element.kind === 'Scene Heading') {
        return {level: 1, title: element.text, line: element.line - 1};
      }
      const raw = rawLines[element.line - 1] ?? '';
      const hashes = raw.match(/^#+/);
      return {level: Math.min(6, Math.max(2, (hashes?.[0].length ?? 1) + 1)), title: element.text, line: element.line - 1};
    });
}

// fountainWarnings is the review checklist — the frontend twin of
// fountainWarnings in fountain.go: empty cues, duplicate spellings, and a
// script with no scenes.
export type FountainWarning = {
  line: number;
  message: string;
};

export function fountainWarnings(script: FountainScript): FountainWarning[] {
  const warnings: FountainWarning[] = [];
  const elements = script.elements;
  const names: Record<string, string> = {};
  const dupes: Record<string, boolean> = {};
  let scenes = 0;
  elements.forEach((element, index) => {
    if (element.kind === 'Scene Heading') {
      scenes++;
      return;
    }
    if (element.kind !== 'Character') {
      return;
    }
    const name = fountainCharacterName(element.text);
    const key = name.toLowerCase();
    if (names[key] && names[key] !== name && !dupes[key]) {
      warnings.push({line: element.line, message: `line ${element.line}: character “${name}” is also spelled “${names[key]}” — pick one spelling`});
      dupes[key] = true;
    } else if (!names[key]) {
      names[key] = name;
    }
    const next = elements[index + 1];
    if (next && next.kind === 'Character') {
      warnings.push({line: element.line, message: `line ${element.line}: the cue “${element.text}” has no dialogue after it`});
    }
  });
  if (scenes === 0 && elements.length > 0) {
    warnings.push({line: 1, message: 'no scene headings — an INT. or EXT. line marks where each scene starts'});
  }
  return warnings;
}

// fountainStripEmphasis removes the *bold* / _underline_ markers — the
// print path renders plain Courier.
export function fountainStripEmphasis(text: string): string {
  return text.replace(/[*_]/g, '');
}

// --- The keyboard model -------------------------------------------------

type CycleKind = 'action' | 'character' | 'parenthetical' | 'transition';
const CYCLE: CycleKind[] = ['action', 'character', 'parenthetical', 'transition'];

function classifyForCycle(line: string): CycleKind {
  if (line.startsWith('@')) return 'character';
  if (/^\(.*\)$/.test(line)) return 'parenthetical';
  if (line.startsWith('>')) return 'transition';
  return 'action';
}

function stripCycleMarker(line: string): string {
  if (line.startsWith('@')) return line.slice(1).trim();
  if (/^\(.*\)$/.test(line)) return line.slice(1, -1).trim();
  if (line.startsWith('>')) return line.slice(1).trim();
  return line;
}

function applyCycleMarker(kind: CycleKind, bare: string): string {
  switch (kind) {
    case 'character':
      return `@${bare.toUpperCase()}`;
    case 'parenthetical':
      return `(${bare})`;
    case 'transition':
      return `> ${bare.toUpperCase()}`;
    default:
      return bare;
  }
}

// fountainCycleElementOnTab is the screenplay Tab: it re-types the current
// line as the next element kind in action → character → parenthetical →
// transition (Shift+Tab walks backwards), applying Fountain's force markers
// (@ cue, (parenthetical), > transition). Dialogue is deliberately not in
// the cycle — it is context (an unmarked line under a cue), not a marker.
// Returns null when the caret is not on a line the op can rewrite.
export function fountainCycleElementOnTab(state: MarkdownEditState, back = false): MarkdownEditState | null {
  const lines = state.text.split('\n');
  const lineIndex = (state.text.slice(0, state.selStart).match(/\n/g) || []).length;
  const line = lines[lineIndex];
  if (line === undefined) {
    return null;
  }
  const current = classifyForCycle(line.trim());
  const bare = stripCycleMarker(line.trim());
  let at = CYCLE.indexOf(current);
  at = back ? (at + CYCLE.length - 1) % CYCLE.length : (at + 1) % CYCLE.length;
  const next = applyCycleMarker(CYCLE[at], bare);
  lines[lineIndex] = next;
  const text = lines.join('\n');
  const caret = state.text.slice(0, state.selStart).lastIndexOf('\n') + 1 + next.length;
  return {text, selStart: caret, selEnd: caret};
}

// fountainContinueElementOnEnter is the screenplay Enter, driven by the
// element under the caret: a cue continues into dialogue (plain newline —
// context does the formatting), dialogue and parentheticals exit the speech
// block with a blank line (so the next typed cue resolves as a cue), a scene
// heading continues into action, and anything else falls through to the
// default newline. Returns null when the op does not apply.
export function fountainContinueElementOnEnter(state: MarkdownEditState): MarkdownEditState | null {
  const caretLine = (state.text.slice(0, state.selStart).match(/\n/g) || []).length + 1;
  const at = fountainParse(state.text).elements.find((element) => element.line === caretLine);
  if (!at) {
    return null;
  }
  let insertion: string | null = null;
  if (at.kind === 'Character' && at.text.trim() !== '') {
    insertion = '\n';
  } else if (at.kind === 'Dialogue' || at.kind === 'Parenthetical') {
    insertion = '\n\n';
  } else if (at.kind === 'Scene Heading') {
    insertion = '\n';
  }
  if (insertion === null) {
    return null;
  }
  const text = state.text.slice(0, state.selStart) + insertion + state.text.slice(state.selEnd);
  const caret = state.selStart + insertion.length;
  return {text, selStart: caret, selEnd: caret};
}
