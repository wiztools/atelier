import {
  MarkdownEditState,
  continueListOnEnter,
  cycleHeading,
  indentSelection,
  insertBlockSnippet,
  insertCodeFence,
  insertLink,
  insertTable,
  lineOfOffset,
  markdownOutline,
  markdownStats,
  markdownWarnings,
  markdownWordCount,
  offsetOfLine,
  toggleLineAffix,
  wrapSelection,
} from '../src/editor/markdownModel';

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message);
}

function state(text: string, selStart?: number, selEnd?: number): MarkdownEditState {
  return {text, selStart: selStart ?? text.length, selEnd: selEnd ?? selStart ?? text.length};
}

// markdownWordCount mirrors Go's strings.Fields — the save summary's count.
assert(markdownWordCount('') === 0, 'empty text counts zero words');
assert(markdownWordCount('   \n\t ') === 0, 'whitespace-only text counts zero words');
assert(markdownWordCount('# Heading\n\nSome body text.') === 5, 'fields split on any run of whitespace');
assert(markdownWordCount(' spaced out') === 2, 'leading/trailing whitespace does not create words');

assert(markdownStats('one two three').words === 3, 'stats carry the word count');
assert(markdownStats('one two three').readingMinutes === 1, 'short docs read in a minute');
assert(markdownStats(new Array(601).fill('word').join(' ')).readingMinutes === 4, 'reading time rounds up at 200 wpm');
assert(markdownStats('').readingMinutes === 0, 'empty doc reads in no minutes');

assert(lineOfOffset('ab\ncd', 0) === 0 && lineOfOffset('ab\ncd', 3) === 1 && lineOfOffset('ab\ncd', 5) === 1, 'lineOfOffset maps offsets onto lines');
assert(offsetOfLine('ab\ncd', 1) === 3 && offsetOfLine('ab\ncd', 0) === 0, 'offsetOfLine maps lines onto offsets');
assert(offsetOfLine('ab\ncd', 99) === 6, 'offsetOfLine clamps past the end');

// wrapSelection: wrap, unwrap, and type-through.
const wrapped = wrapSelection(state('hello world', 6, 11), '**', 'bold');
assert(wrapped.text === 'hello **world**' && wrapped.selStart === 8 && wrapped.selEnd === 13, 'wrapSelection wraps the selection');
const unwrapped = wrapSelection(state('hello **world**', 8, 13), '**', 'bold');
assert(unwrapped.text === 'hello world' && unwrapped.selStart === 6 && unwrapped.selEnd === 11, 'wrapSelection unwraps a wrapped selection');
const cursorInside = wrapSelection(state('hello **** world', 8, 8), '**', 'bold');
assert(cursorInside.text === 'hello  world' && cursorInside.selStart === 6, 'a cursor between empty markers unwraps the pair');
const empty = wrapSelection(state('hello ', 6, 6), '*', 'italic');
assert(empty.text === 'hello *italic*' && empty.selStart === 7 && empty.selEnd === 13, 'an empty selection inserts the placeholder and selects it');

const linked = insertLink(state('see this', 4, 8));
assert(linked.text === 'see [this](url)' && linked.text.slice(linked.selStart, linked.selEnd) === 'url', 'insertLink selects the URL placeholder');
const linkedEmpty = insertLink(state('see ', 4, 4));
assert(linkedEmpty.text === 'see [link text](url)', 'insertLink on empty selection inserts the skeleton');

// cycleHeading: none → h1 → h2 → h3 → none, keyed off the first selected line.
assert(cycleHeading(state('Title', 0, 5)).text === '# Title', 'cycleHeading promotes plain text to h1');
assert(cycleHeading(state('# Title', 0, 7)).text === '## Title', 'cycleHeading steps h1 to h2');
assert(cycleHeading(state('## Title', 0, 8)).text === '### Title', 'cycleHeading steps h2 to h3');
assert(cycleHeading(state('### Title', 0, 9)).text === 'Title', 'cycleHeading strips at h3');
assert(cycleHeading(state('#### Deep', 0, 9)).text === '#### Deep', 'cycleHeading leaves deeper levels alone (cycle keyed on h1-h3)');
const multiHeading = cycleHeading(state('## One\ntwo\n## Three', 0, 19));
assert(multiHeading.text === '### One\n### two\n### Three', 'cycleHeading maps every selected line');

assert(toggleLineAffix(state('a\n\nb', 0, 4), '- ').text === '- a\n\n- b', 'toggleLineAffix skips empty lines when adding');
assert(toggleLineAffix(state('- a\n- b', 0, 7), '- ').text === 'a\nb', 'toggleLineAffix strips when every line carries the prefix');
assert(toggleLineAffix(state('a\n- b', 0, 5), '- ').text === '- a\n- b', 'toggleLineAffix adds when any line lacks the prefix');

// continueListOnEnter.
const bullet = continueListOnEnter(state('- first item', 12, 12))!;
assert(bullet.text === '- first item\n- ' && bullet.selStart === 15, 'bullet lists continue with the same marker');
const ordered = continueListOnEnter(state('1. first', 8, 8))!;
assert(ordered.text === '1. first\n2. ', 'ordered lists increment');
const orderedParen = continueListOnEnter(state('2) second', 9, 9))!;
assert(orderedParen.text === '2) second\n3) ', 'ordered lists keep their delimiter');
const task = continueListOnEnter(state('- [x] done', 10, 10))!;
assert(task.text === '- [x] done\n- [ ] ', 'task lists reset the checkbox');
const nestedIndent = continueListOnEnter(state('  - nested', 10, 10))!;
assert(nestedIndent.text === '  - nested\n  - ', 'continuation preserves indentation');
const exit = continueListOnEnter(state('- ', 2, 2))!;
assert(exit.text === '' && exit.selStart === 0, 'enter on an empty item exits the list');
assert(continueListOnEnter(state('plain text', 5, 5)) === null, 'plain text gets a default newline');
const midList = continueListOnEnter(state('- one two', 5, 5))!;
assert(midList.text === '- one\n-  two' && midList.selStart === 8, 'splitting an item inserts the marker at the caret');

// indentSelection applies only inside lists and blockquotes.
assert(indentSelection(state('plain text', 0, 5), false) === null, 'tab outside a list keeps the browser default');
assert(indentSelection(state('- a\n- b', 0, 7), false)!.text === '  - a\n  - b', 'tab indents list lines');
assert(indentSelection(state('  - a\n  - b', 0, 11), true)!.text === '- a\n- b', 'shift-tab outdents list lines');
assert(indentSelection(state('> quote', 0, 7), false)!.text === '  > quote', 'tab indents blockquotes');

const fenced = insertCodeFence(state('before', 6, 6), 'ts');
assert(fenced.text === 'before\n```ts\n\n```\n', 'insertCodeFence wraps the cursor in a fence');
assert(fenced.selStart === 13 && fenced.text.slice(12, 13) === '\n', 'insertCodeFence leaves the cursor on the empty line inside');
assert(insertBlockSnippet(state('a', 1, 1), '\n\n---\n').text === 'a\n\n---\n', 'insertBlockSnippet appends the snippet at the caret');
const tabled = insertTable(state('', 0, 0));
assert(tabled.text.startsWith('\n| Column | Column |'), 'insertTable inserts the skeleton');

// markdownOutline skips fences and strips closing hashes.
const outline = markdownOutline('# Top\n\n```ts\n# not a heading\n```\n\n## Nested # \n');
assert(outline.length === 2, 'outline lists two headings');
assert(outline[0].level === 1 && outline[0].line === 0 && outline[0].title === 'Top', 'outline carries level, line, and title');
assert(outline[1].level === 2 && outline[1].line === 6 && outline[1].title === 'Nested', 'outline skips fenced headings and strips closing hashes');
assert(markdownOutline('no heading\n#tag neither').length === 0, 'atx headings need the space');

// markdownWarnings: the conservative checklist.
const clean = markdownWarnings('# Title\n\n## Sub\n\nBody.\n');
assert(clean.length === 0, 'a clean document has no warnings');
const skips = markdownWarnings('# Title\n\n### Skipped\n');
assert(skips.length === 1 && skips[0].line === 3 && skips[0].message.includes('h1 to h3'), 'heading level skips warn with the line');
const unclosed = markdownWarnings('# Title\n\n```ts\nconst x = 1;\n');
assert(unclosed.length === 1 && unclosed[0].line === 5 && unclosed[0].message.includes('never closed'), 'an unclosed fence warns at the end');
const todos = markdownWarnings('TODO fix this\n\n- done\n');
assert(todos.length === 1 && todos[0].line === 1 && todos[0].message.includes('TODO'), 'TODO markers warn with the line');
const fencedTodo = markdownWarnings('```ts\n// TODO inside code\n```\n');
assert(fencedTodo.length === 0, 'markers inside code fences are code, not warnings');
assert(markdownWarnings('# Title\n\n## Sub ###\n').length === 0, 'closing hashes do not break the heading read');

import {findMatches, nextMatch, previousMatch, replaceLiteral} from '../src/editor/markdownModel';

// findMatches / nextMatch / previousMatch / replaceLiteral: literal,
// case-sensitive, wrap-around navigation — the inspector's find bar contract.
assert(findMatches('ab cd ab', 'ab').length === 2 && findMatches('ab cd ab', 'ab')[0] === 0 && findMatches('ab cd ab', 'ab')[1] === 6, 'findMatches returns every non-overlapping offset');
assert(findMatches('aB ab', 'ab').length === 1, 'findMatches is case-sensitive');
assert(findMatches('aaa', 'aa').length === 1, 'findMatches never overlaps matches');
assert(findMatches('anything', '').length === 0, 'an empty query matches nothing (the no-op guard)');
assert(nextMatch([3, 7, 12], 5) === 7 && nextMatch([3, 7, 12], 12) === 3, 'nextMatch advances and wraps');
assert(previousMatch([3, 7, 12], 10) === 7 && previousMatch([3, 7, 12], 3) === 12, 'previousMatch rewinds and wraps');
assert(nextMatch([], 0) === -1 && previousMatch([], 0) === -1, 'no matches navigates nowhere');
const replaced = replaceLiteral('one two one two', 'one', '1');
assert(replaced.text === '1 two 1 two' && replaced.count === 2, 'replaceLiteral swaps every occurrence and counts');
assert(replaceLiteral('text', '', 'x').count === 0, 'replaceLiteral with an empty find is a no-op');
