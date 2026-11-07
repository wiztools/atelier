import {
  CompareDisplayItem,
  DiffHunk,
  DiffRow,
  compareMinFoldLines,
  compareSummary,
  foldRows,
  hunkAtRow,
  hunkSideRange,
  sectionLabel,
  stepHunk,
  unifiedItems,
} from '../src/editor/documentCompareModel';

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message);
}

function equal(n: number): DiffRow {
  return {kind: 'equal', before: {number: n, text: `line ${n}`}, after: {number: n, text: `line ${n}`}};
}

// rowsWithChangeAt builds `total` equal rows with a change row at each index
// in `changes` (line numbers simply follow the index).
function rowsWithChangeAt(total: number, changes: number[]): DiffRow[] {
  const rows: DiffRow[] = [];
  for (let index = 0; index < total; index++) {
    rows.push(changes.includes(index)
      ? {kind: 'change', before: {number: index + 1, text: `old ${index}`}, after: {number: index + 1, text: `new ${index}`}}
      : equal(index + 1));
  }
  return rows;
}

function describe(items: CompareDisplayItem[]): string {
  return items.map((item) => (item.type === 'row' ? String(item.index) : `[${item.start}-${item.end}]`)).join(' ');
}

// foldRows keeps `context` rows around each change and folds the rest.
{
  const rows = rowsWithChangeAt(30, [10]);
  const items = foldRows(rows, 3, new Set(), false);
  assert(describe(items) === '[0-6] 7 8 9 10 11 12 13 [14-29]', `context window around one change: ${describe(items)}`);
  const fold = items[0];
  assert(fold.type === 'fold' && fold.count === 7, 'a fold counts the rows it hides');
}

// Two changes whose windows touch merge into one visible stretch (and the
// two rows above the first window are too few to fold).
{
  const rows = rowsWithChangeAt(20, [5, 11]);
  const got = describe(foldRows(rows, 3, new Set(), false));
  assert(got === '0 1 2 3 4 5 6 7 8 9 10 11 12 13 14 [15-19]', `adjacent windows merge: ${got}`);
}

// A hidden run shorter than the fold minimum stays visible.
{
  const rows = rowsWithChangeAt(15, [3, 11]);
  // Rows 7 is the only row beyond both windows (0..6 and 8..14 are near) —
  // a one-row fold would be silly.
  const items = foldRows(rows, 3, new Set(), false);
  assert(items.every((item) => item.type === 'row'), `a short gap stays visible: ${describe(items)}`);
  assert(compareMinFoldLines > 1, 'the fold minimum is more than one row');
}

// An expanded fold shows its rows; showAll shows everything.
{
  const rows = rowsWithChangeAt(30, [10]);
  assert(describe(foldRows(rows, 3, new Set([14]), false)).endsWith('13 14 15 16 17 18 19 20 21 22 23 24 25 26 27 28 29'), 'an expanded fold opens');
  assert(foldRows(rows, 3, new Set(), true).length === 30, 'showAll draws every row');
}

// Identical documents show the whole document — nothing to fold toward.
{
  const rows = rowsWithChangeAt(25, []);
  assert(foldRows(rows, 3, new Set(), false).length === 25, 'no changes, no folds');
  assert(foldRows([], 3, new Set(), false).length === 0, 'an empty comparison has no items');
}

// unifiedItems lists a change block's before lines, then its after lines.
{
  const rows: DiffRow[] = [
    equal(1),
    {kind: 'change', before: {number: 2, text: 'I never said that.', segments: [{text: 'I never said '}, {text: 'that', changed: true}, {text: '.'}]}, after: {number: 2, text: 'I never said it.'}},
    {kind: 'delete', before: {number: 3, text: 'gone'}},
    equal(4),
    {kind: 'insert', after: {number: 5, text: 'added'}},
  ];
  const items = unifiedItems(rows, foldRows(rows, 3, new Set(), false));
  const shape = items.map((item) => (item.type === 'line' ? `${item.sign}${item.text}` : 'fold'));
  assert(JSON.stringify(shape) === JSON.stringify([' line 1', '-I never said that.', '-gone', '+I never said it.', ' line 4', '+added']), `unified order: ${JSON.stringify(shape)}`);
  const first = items[1];
  assert(first.type === 'line' && first.beforeNumber === 2 && first.afterNumber === undefined && first.row === 1, 'a removed line carries its before number and source row');
  assert(first.type === 'line' && (first.segments?.length ?? 0) === 3, 'segments ride the unified line');
  const context = items[0];
  assert(context.type === 'line' && context.beforeNumber === 1 && context.afterNumber === 1, 'an unchanged line carries both numbers');
}

// Folds pass through the unified layout untouched.
{
  const rows = rowsWithChangeAt(30, [10]);
  const items = unifiedItems(rows, foldRows(rows, 3, new Set(), false));
  assert(items[0].type === 'fold' && items[items.length - 1].type === 'fold', 'folds survive the unified reshape');
  assert(items.filter((item) => item.type === 'line' && item.sign === '-').length === 1, 'one removed line');
}

// Navigation clamps and starts from either end.
assert(stepHunk(-1, 1, 3) === 0, 'next from nothing lands on the first change');
assert(stepHunk(-1, -1, 3) === 2, 'previous from nothing lands on the last change');
assert(stepHunk(2, 1, 3) === 2 && stepHunk(0, -1, 3) === 0, 'navigation clamps at the ends');
assert(stepHunk(1, 1, 3) === 2 && stepHunk(1, -1, 3) === 0, 'navigation steps');
assert(stepHunk(-1, 1, 0) === -1, 'no changes, no cursor');

{
  const hunks: DiffHunk[] = [
    {firstRow: 2, lastRow: 3, beforeLine: 3, beforeCount: 2, afterLine: 3, afterCount: 1, added: 1, removed: 2},
    {firstRow: 9, lastRow: 9, beforeLine: 9, beforeCount: 0, afterLine: 9, afterCount: 1, added: 1, removed: 0, section: 'INT. KITCHEN - DAY'},
  ];
  assert(hunkAtRow(hunks, 3) === 0 && hunkAtRow(hunks, 9) === 1 && hunkAtRow(hunks, 5) === -1, 'rows map to their change');
}

assert(sectionLabel('INT. KITCHEN - DAY') === 'INT. KITCHEN - DAY', 'a section names itself');
assert(sectionLabel('') === 'Start of document' && sectionLabel(undefined) === 'Start of document', 'no section reads as the start');
assert(hunkSideRange(7, 1) === '7' && hunkSideRange(15, 4) === '15–18' && hunkSideRange(14, 0) === '—', 'side ranges mirror the Go excerpt');
assert(compareSummary({added: 5, removed: 1, changes: 2, identical: false}) === '+5 −1 · 2 changes', 'summary counts');
assert(compareSummary({added: 1, removed: 0, changes: 1, identical: false}) === '+1 −0 · 1 change', 'singular change');
assert(compareSummary({added: 0, removed: 0, changes: 0, identical: true}) === 'No differences', 'identical summary');

console.log('documentCompareModel assertions passed');
