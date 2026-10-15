import type {MarkdownEditState} from '../src/editor/markdownModel';
import {
  fountainCharacterName,
  fountainContinueElementOnEnter,
  fountainCycleElementOnTab,
  fountainOutline,
  fountainParse,
  fountainStripEmphasis,
  fountainWarnings,
} from '../src/editor/fountainModel';

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message);
}

function state(text: string, selStart?: number, selEnd?: number): MarkdownEditState {
  return {text, selStart: selStart ?? text.length, selEnd: selEnd ?? selStart ?? text.length};
}

const SCRIPT = 'Title: The Draft\nAuthor: Subhash\n\nINT. WRITING ROOM - NIGHT\n\nA laptop glows.\n\nSUBHASH\nTyping furiously.\n(beat)\nStill typing.\n\nMEERA (V.O.)\nFrom the next room.\n\nCUT TO:\n';

// The parse mirrors fountain.go: elements, speech attribution, extensions.
const script = fountainParse(SCRIPT);
assert(script.titlePage['Title'] === 'The Draft' && script.titlePage['Author'] === 'Subhash', 'the title page parses from the leading key block');
const kinds = script.elements.map((element) => element.kind);
assert(
  JSON.stringify(kinds) === JSON.stringify([
    'Scene Heading', 'Action', 'Character', 'Dialogue', 'Parenthetical', 'Dialogue',
    'Character', 'Dialogue', 'Transition',
  ]),
  `kinds = ${kinds.join(',')}`,
);
assert(script.elements[3].character === 'SUBHASH', 'dialogue attributes its cue');
assert(script.elements[4].character === 'SUBHASH', 'a parenthetical stays in the speech block');
assert(script.elements[6].text === 'MEERA (V.O.)' && script.elements[7].character === 'MEERA', 'the cue keeps its extension; the attribution strips it');
assert(script.elements[0].line === 4, 'elements carry their 1-based source line (the title block consumes the blank line)');

// Pattern rules and their guards.
assert(fountainParse('INT. HOUSE\n').elements[0].kind === 'Scene Heading', 'INT. opens a scene');
assert(fountainParse('INTERRUPTED\n').elements[0].kind === 'Action', 'INT without a separator is action');
assert(fountainParse('CUT TO:\n').elements[0].kind === 'Transition', 'uppercase TO: is a transition');
assert(fountainParse('CUT TO the chase\n').elements[0].kind === 'Action', 'TO: must end the line');
assert(fountainParse('I’ll cut to:\n').elements[0].kind === 'Action', 'lowercase never transitions');
assert(fountainParse('WAIT\nWAIT\n').elements.length === 2 && fountainParse('WAIT\nWAIT\n').elements[1].kind === 'Dialogue', 'an uppercase shout inside a speech block stays dialogue');
assert(fountainParse('===\n').elements[0].kind === 'Page Break', 'three equals force a page break');
assert(fountainParse('= synopsis\n').elements[0].kind === 'Synopsis', 'one equal is a synopsis');
assert(fountainParse('# Act One\n').elements[0].kind === 'Section', 'a hash is a section');
assert(fountainParse('#tag\n').elements[0].kind === 'Action', 'a hash without a space is action');
assert(fountainParse('~la la\n').elements[0].kind === 'Lyric', 'a tilde is a lyric');
assert(fountainParse('> the end <\n').elements[0].kind === 'Centered' && fountainParse('> the end <\n').elements[0].text === 'the end', 'centered strips its markers');
assert(fountainParse('.FORCED SCENE\n').elements[0].kind === 'Scene Heading', 'a dot forces a scene heading');
assert(fountainParse('..not a scene\n').elements[0].kind === 'Action', 'a double dot is not a scene force');
assert(fountainParse('!Not a heading.\n').elements[0].kind === 'Action', 'a bang forces action');

// A script with no title page falls straight into the body.
const bare = fountainParse('Just prose.\n\nINT. LATER\n');
assert(Object.keys(bare.titlePage).length === 0 && bare.elements.length === 2 && bare.elements[0].kind === 'Action', 'no title page — the body parse starts at line one');

// Character names strip their extensions.
assert(fountainCharacterName('SUBHASH (CONT’D)') === 'SUBHASH', "CONT'D strips");
assert(fountainCharacterName('MEERA (V.O.)') === 'MEERA', 'V.O. strips');
assert(fountainCharacterName('MOM') === 'MOM', 'a bare cue is its own name');

// Outline: scenes at level 1, sections with their depth.
const outline = fountainOutline(SCRIPT + '# Act Two\n\nINT. LATER\n');
assert(outline.length === 3, 'the outline lists scenes and sections');
assert(outline[0].level === 1 && outline[0].title === 'INT. WRITING ROOM - NIGHT' && outline[0].line === 3, 'scenes sit at level 1 with a 0-based line');
assert(outline[1].level === 2 && outline[1].title === 'Act Two', 'sections indent by their depth');

// Warnings mirror fountain.go.
const dirty = fountainParse('@AMELIA\n@MAYA\nHello.\n\n@amelia\nIs here.\n');
const dirtyWarnings = fountainWarnings(dirty);
assert(dirtyWarnings.some((warning) => warning.message.includes('has no dialogue after it') && warning.line === 1), 'an empty cue warns at its line');
assert(dirtyWarnings.some((warning) => warning.message.includes('also spelled')), 'a case-variant spelling warns');
const headless = fountainWarnings(fountainParse('Just prose.\n'));
assert(headless.length === 1 && headless[0].message.includes('no scene headings'), 'a scene-less script warns');
assert(fountainWarnings(script).length === 0, 'a clean script has no warnings');

assert(fountainStripEmphasis('he *yells* and _points_') === 'he yells and points', 'emphasis strips for print');

// The Tab cycle re-types the current line through the force markers.
const cycled = fountainCycleElementOnTab(state('INT. ROOM\n\nAction line', 'Action line'.length + 13, 13 + 11))!;
assert(cycled.text === 'INT. ROOM\n\n@ACTION LINE', 'Tab on an action line makes it a character cue (uppercased)');
assert(fountainCycleElementOnTab(state('@CUE', 2, 2))!.text === '(CUE)', 'Tab on a cue makes it a parenthetical');
assert(fountainCycleElementOnTab(state('(beat)', 3, 3))!.text === '> BEAT', 'Tab on a parenthetical makes it a transition');
assert(fountainCycleElementOnTab(state('> TRANSITION', 4, 4))!.text === 'TRANSITION', 'Tab on a transition returns to action');
assert(fountainCycleElementOnTab(state('@CUE', 2, 2), true)!.text === 'CUE', 'Shift+Tab walks the cycle backwards');
assert(fountainCycleElementOnTab(state('INT. ROOM\n\nAction line', 20, 20))!.text === 'INT. ROOM\n\n@ACTION LINE', 'the cycle works from a caret anywhere on the line');

// The Enter continuation is element-driven (carets sit at the end of the
// element's own line, before its newline).
const cueState = state('MAYA\nHello.\n', 4, 4);
assert(fountainContinueElementOnEnter(cueState)!.text === 'MAYA\n\nHello.\n', 'Enter after a cue opens dialogue (a plain newline carries the context)');
const dialogueState = state('MAYA\nHello.\n', 11, 11);
assert(fountainContinueElementOnEnter(dialogueState)!.text === 'MAYA\nHello.\n\n\n', 'Enter after dialogue inserts the blank line that exits the speech block');
const sceneState = state('INT. ROOM - DAY\n', 15, 15);
assert(fountainContinueElementOnEnter(sceneState)!.text === 'INT. ROOM - DAY\n\n' && fountainContinueElementOnEnter(sceneState)!.selStart === 16, 'Enter after a scene heading opens action');
assert(fountainContinueElementOnEnter(state('Plain action.\n', 13, 13)) === null, 'other elements take the default newline');
