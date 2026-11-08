// The compare workspace's view model — pure functions over the diff the Go
// side computed (document_diff.go). The frontend never re-diffs: it folds
// unchanged stretches, re-shapes rows for the unified layout, and steps
// between changes. No React and no bindings import, so the .cjs runner can
// compile it alone (test:document-compare-model). Types are structural
// mirrors of the wire shapes (document.DiffRow & co.) so the generated
// models satisfy them.

export type DiffSegment = {text: string; changed?: boolean};

export type DiffLine = {number: number; text: string; segments?: DiffSegment[]};

// kind is "equal" | "delete" | "insert" | "change" (a deleted and an inserted
// line paired by position inside a change block).
export type DiffRow = {kind: string; before?: DiffLine; after?: DiffLine};

export type DiffHunk = {
  firstRow: number;
  lastRow: number;
  beforeLine: number;
  beforeCount: number;
  afterLine: number;
  afterCount: number;
  added: number;
  removed: number;
  section?: string;
};

export type DiffStats = {added: number; removed: number; changes: number; identical: boolean};

// Unchanged lines kept visible on each side of a change.
export const compareContextLines = 3;

// A hidden run shorter than this stays visible — a "⋯ 2 unchanged lines"
// marker costs as much room as the lines it hides.
export const compareMinFoldLines = 4;

// A display item is a row to draw, or a fold standing in for a run of
// unchanged rows (start..end inclusive row indexes). Folds are keyed by their
// start row: the expanded set holds the starts the user opened.
export type CompareDisplayItem =
  | {type: 'row'; index: number}
  | {type: 'fold'; start: number; end: number; count: number};

// foldRows hides the unchanged stretches more than `context` rows from any
// change, unless showAll is set or the fold was expanded. Changed rows are
// never folded, so every hunk stays reachable. A comparison with no changes
// at all shows everything — there is nothing to focus on, and the document
// itself is the answer.
export function foldRows(
  rows: DiffRow[],
  context: number,
  expanded: ReadonlySet<number>,
  showAll: boolean,
): CompareDisplayItem[] {
  const items: CompareDisplayItem[] = [];
  const hasChanges = rows.some((row) => row.kind !== 'equal');
  if (showAll || !hasChanges) {
    rows.forEach((_, index) => items.push({type: 'row', index}));
    return items;
  }
  // Distance (in rows) to the nearest change on each side.
  const near: boolean[] = new Array(rows.length).fill(false);
  let since = Number.POSITIVE_INFINITY;
  for (let index = 0; index < rows.length; index++) {
    since = rows[index].kind === 'equal' ? since + 1 : 0;
    near[index] = since <= context;
  }
  let until = Number.POSITIVE_INFINITY;
  for (let index = rows.length - 1; index >= 0; index--) {
    until = rows[index].kind === 'equal' ? until + 1 : 0;
    near[index] = near[index] || until <= context;
  }
  for (let index = 0; index < rows.length; ) {
    if (near[index]) {
      items.push({type: 'row', index});
      index++;
      continue;
    }
    let end = index;
    while (end + 1 < rows.length && !near[end + 1]) end++;
    const count = end - index + 1;
    if (count < compareMinFoldLines || expanded.has(index)) {
      for (let row = index; row <= end; row++) items.push({type: 'row', index: row});
    } else {
      items.push({type: 'fold', start: index, end, count});
    }
    index = end + 1;
  }
  return items;
}

// A unified line: one side of the comparison in a single column — the sign
// says which ("-" before only, "+" after only, " " both), and row names the
// diff row it came from (scroll targets and keys).
export type UnifiedItem =
  | {type: 'line'; sign: ' ' | '-' | '+'; row: number; beforeNumber?: number; afterNumber?: number; text: string; segments?: DiffSegment[]}
  | {type: 'fold'; start: number; end: number; count: number};

// unifiedItems re-shapes the folded rows for the single-column layout: an
// unchanged row is one line carrying both numbers, and each change block
// (consecutive changed rows) lists all its before lines, then all its after
// lines — the unified-diff reading order. Folds pass through.
export function unifiedItems(rows: DiffRow[], items: CompareDisplayItem[]): UnifiedItem[] {
  const out: UnifiedItem[] = [];
  let block: number[] = [];
  const flush = () => {
    for (const index of block) {
      const line = rows[index].before;
      if (line) out.push({type: 'line', sign: '-', row: index, beforeNumber: line.number, text: line.text, segments: line.segments});
    }
    for (const index of block) {
      const line = rows[index].after;
      if (line) out.push({type: 'line', sign: '+', row: index, afterNumber: line.number, text: line.text, segments: line.segments});
    }
    block = [];
  };
  for (const item of items) {
    if (item.type === 'fold') {
      flush();
      out.push(item);
      continue;
    }
    const row = rows[item.index];
    if (row.kind !== 'equal') {
      block.push(item.index);
      continue;
    }
    flush();
    out.push({
      type: 'line',
      sign: ' ',
      row: item.index,
      beforeNumber: row.before?.number,
      afterNumber: row.after?.number,
      // Under ignore-whitespace an equal row's sides can differ in spacing;
      // the single column shows the after side's text.
      text: row.after?.text ?? row.before?.text ?? '',
    });
  }
  flush();
  return out;
}

// stepHunk moves the change cursor: next/previous, clamped to the ends. With
// nothing selected (-1), next lands on the first change and previous on the
// last.
export function stepHunk(current: number, delta: 1 | -1, count: number): number {
  if (count <= 0) return -1;
  if (current < 0) return delta > 0 ? 0 : count - 1;
  return Math.min(count - 1, Math.max(0, current + delta));
}

// hunkAtRow names the change a row belongs to — its index in hunks, or -1
// for an unchanged row.
export function hunkAtRow(hunks: DiffHunk[], row: number): number {
  return hunks.findIndex((hunk) => row >= hunk.firstRow && row <= hunk.lastRow);
}

// sectionLabel names a change's place for the change list: its scene or
// heading, or the document's start when it sits before the first one (the
// Go side's documentSectionLabel, capitalized for the UI).
export function sectionLabel(section?: string): string {
  return section && section.trim() ? section : 'Start of document';
}

// hunkSideRange renders one side's lines for the change list: "7", "15–18",
// or "—" when the side has no lines in the change (hunkSideRange in Go).
export function hunkSideRange(line: number, count: number): string {
  if (count === 0) return '—';
  if (count === 1) return String(line);
  return `${line}–${line + count - 1}`;
}

// compareSummary is the inspector's headline: the git-style counts, or the
// identical verdict.
export function compareSummary(stats: DiffStats): string {
  if (stats.identical) return 'No differences';
  const changes = `${stats.changes} change${stats.changes === 1 ? '' : 's'}`;
  return `+${stats.added} −${stats.removed} · ${changes}`;
}
