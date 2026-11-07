package main

// The document diff behind the compare workspace and the compare_documents
// tool — pure functions over two texts, no storage, no UI. Lines are the unit
// (a document's revisions differ by lines the writer touched), with a
// word-level refinement on the lines a change block pairs up so a one-word
// dialogue tweak highlights the word, not the whole line.
//
// The line diff is patience-anchored with a Myers fallback. Lines that occur
// exactly once on BOTH sides anchor the alignment (their longest increasing
// run, in order), and the gaps between anchors recurse; a gap with no unique
// lines falls back to Myers' O(ND) shortest edit script (the linear-space
// bisect). Minimal-edit Myers alone is indifferent between equally short
// scripts, and screenplays are full of short lines that repeat everywhere —
// character cues ("JOHN"), "CUT TO:", blank separators — so when a line of
// content and that filler trade places it as often keeps a blank line
// matched and reports the dialogue as moved. Anchoring on unique lines (scene
// headings, dialogue) keeps the content matched and reports the filler as
// the edit — the reading a writer expects.
//
// The Myers search is cost-capped: a gap whose edit distance runs past
// lineDiffMaxCost lands as a whole-block replace instead of an exhaustive
// search, so two unrelated long documents still compare in well under a
// second — and for documents that different, "this all changed" is the
// honest rendering anyway.

import (
	"sort"
	"strings"
	"unicode"
)

// lineDiffMaxCost caps the Myers bisect's edit-distance search (the number of
// d-steps from each end). Past it the gap renders as a whole-block replace.
const lineDiffMaxCost = 10000

// wordDiffMinSimilarity is the token similarity (2·matched / total, over
// non-whitespace tokens) a paired change row needs before its words are
// highlighted. Below it the two lines share too little for word marks to
// read as an edit — the whole line is tinted instead of confetti.
const wordDiffMinSimilarity = 0.5

type diffOpKind byte

const (
	diffEqual diffOpKind = iota
	diffDelete
	diffInsert
)

// diffOp is one step of an edit script: an equal pair, a deleted before-line,
// or an inserted after-line. Indexes are 0-based; the absent side is -1.
type diffOp struct {
	kind   diffOpKind
	before int
	after  int
}

// lineDiffer computes an edit script over two sequences of interned keys
// (equal keys ⇔ equal lines or tokens), appending ops in document order.
type lineDiffer struct {
	a, b []int
	ops  []diffOp
}

func (d *lineDiffer) equal(ai, bi int) { d.ops = append(d.ops, diffOp{diffEqual, ai, bi}) }
func (d *lineDiffer) del(ai int)       { d.ops = append(d.ops, diffOp{diffDelete, ai, -1}) }
func (d *lineDiffer) ins(bi int)       { d.ops = append(d.ops, diffOp{diffInsert, -1, bi}) }

// diffKeys diffs two key sequences. patience selects the unique-line
// anchoring (lines); tokens run plain Myers — a line's tokens are few, and
// unique-token anchoring buys nothing there.
func diffKeys(a, b []int, patience bool) []diffOp {
	d := &lineDiffer{a: a, b: b, ops: make([]diffOp, 0, max(len(a), len(b)))}
	d.diffRange(0, len(a), 0, len(b), patience)
	return d.ops
}

// diffRange diffs a[aLo:aHi] against b[bLo:bHi]: the common prefix and suffix
// are equal outright, an empty side is a pure insert or delete, and anything
// else anchors (patience) or bisects (Myers).
func (d *lineDiffer) diffRange(aLo, aHi, bLo, bHi int, patience bool) {
	for aLo < aHi && bLo < bHi && d.a[aLo] == d.b[bLo] {
		d.equal(aLo, bLo)
		aLo++
		bLo++
	}
	suffix := 0
	for aHi-suffix > aLo && bHi-suffix > bLo && d.a[aHi-suffix-1] == d.b[bHi-suffix-1] {
		suffix++
	}
	aHi -= suffix
	bHi -= suffix
	switch {
	case aLo == aHi:
		for j := bLo; j < bHi; j++ {
			d.ins(j)
		}
	case bLo == bHi:
		for i := aLo; i < aHi; i++ {
			d.del(i)
		}
	case patience && d.patience(aLo, aHi, bLo, bHi):
	default:
		d.myers(aLo, aHi, bLo, bHi)
	}
	for i := 0; i < suffix; i++ {
		d.equal(aHi+i, bHi+i)
	}
}

type diffAnchor struct{ a, b int }

// patience anchors the range on keys that occur exactly once on each side —
// uniqueness counted within the range, so a gap between anchors finds its own
// anchors on recursion — keeping the longest run that stays in order on both
// sides. Reports false when the range has no unique common key, leaving the
// range to Myers.
func (d *lineDiffer) patience(aLo, aHi, bLo, bHi int) bool {
	type occurrence struct{ inA, inB, posA, posB int }
	seen := make(map[int]*occurrence, aHi-aLo)
	for i := aLo; i < aHi; i++ {
		o := seen[d.a[i]]
		if o == nil {
			o = &occurrence{}
			seen[d.a[i]] = o
		}
		o.inA++
		o.posA = i
	}
	for j := bLo; j < bHi; j++ {
		if o := seen[d.b[j]]; o != nil {
			o.inB++
			o.posB = j
		}
	}
	pairs := []diffAnchor{}
	for i := aLo; i < aHi; i++ {
		if o := seen[d.a[i]]; o.inA == 1 && o.inB == 1 {
			pairs = append(pairs, diffAnchor{a: i, b: o.posB})
		}
	}
	if len(pairs) == 0 {
		return false
	}
	prevA, prevB := aLo, bLo
	for _, anchor := range longestIncreasingAnchors(pairs) {
		d.diffRange(prevA, anchor.a, prevB, anchor.b, true)
		d.equal(anchor.a, anchor.b)
		prevA, prevB = anchor.a+1, anchor.b+1
	}
	d.diffRange(prevA, aHi, prevB, bHi, true)
	return true
}

// longestIncreasingAnchors keeps the longest subsequence of pairs (already in
// before-side order) whose after-side positions also increase — patience
// sorting with predecessor links, O(k log k).
func longestIncreasingAnchors(pairs []diffAnchor) []diffAnchor {
	tails := []int{}
	prev := make([]int, len(pairs))
	for i, pair := range pairs {
		at := sort.Search(len(tails), func(k int) bool { return pairs[tails[k]].b >= pair.b })
		prev[i] = -1
		if at > 0 {
			prev[i] = tails[at-1]
		}
		if at == len(tails) {
			tails = append(tails, i)
		} else {
			tails[at] = i
		}
	}
	out := make([]diffAnchor, len(tails))
	for i, k := len(tails)-1, tails[len(tails)-1]; i >= 0; i, k = i-1, prev[k] {
		out[i] = pairs[k]
	}
	return out
}

// myers splits the range at the middle of its shortest edit script and
// recurses on both halves; a search that runs past the cost cap lands the
// whole range as delete-then-insert. The caller trimmed the common prefix and
// suffix and both sides are non-empty, so the edit distance is at least 2 and
// the split point is strictly inside — every recursion shrinks.
func (d *lineDiffer) myers(aLo, aHi, bLo, bHi int) {
	x, y, ok := bisectKeys(d.a[aLo:aHi], d.b[bLo:bHi])
	if !ok {
		for i := aLo; i < aHi; i++ {
			d.del(i)
		}
		for j := bLo; j < bHi; j++ {
			d.ins(j)
		}
		return
	}
	d.diffRange(aLo, aLo+x, bLo, bLo+y, false)
	d.diffRange(aLo+x, aHi, bLo+y, bHi, false)
}

// bisectKeys finds the middle snake of the shortest edit script between a and
// b — Myers' linear-space search, run from both ends until the forward and
// reverse paths overlap (the diff-match-patch formulation). Reports false when
// no overlap appears within lineDiffMaxCost steps, or when a and b share
// nothing at all (whose only script is replace-everything).
func bisectKeys(a, b []int) (int, int, bool) {
	n, m := len(a), len(b)
	maxD := min((n+m+1)/2, lineDiffMaxCost)
	vOffset := maxD
	vLength := 2*maxD + 2
	v1 := make([]int, vLength)
	v2 := make([]int, vLength)
	for i := range v1 {
		v1[i] = -1
		v2[i] = -1
	}
	v1[vOffset+1] = 0
	v2[vOffset+1] = 0
	delta := n - m
	// An odd delta means the forward pass detects the overlap; even, the
	// reverse pass.
	front := delta%2 != 0
	k1start, k1end, k2start, k2end := 0, 0, 0, 0
	for step := 0; step < maxD; step++ {
		for k1 := -step + k1start; k1 <= step-k1end; k1 += 2 {
			k1Offset := vOffset + k1
			var x1 int
			if k1 == -step || (k1 != step && v1[k1Offset-1] < v1[k1Offset+1]) {
				x1 = v1[k1Offset+1]
			} else {
				x1 = v1[k1Offset-1] + 1
			}
			y1 := x1 - k1
			for x1 < n && y1 < m && a[x1] == b[y1] {
				x1++
				y1++
			}
			v1[k1Offset] = x1
			switch {
			case x1 > n:
				k1end += 2 // ran off the right edge
			case y1 > m:
				k1start += 2 // ran off the bottom edge
			case front:
				k2Offset := vOffset + delta - k1
				if k2Offset >= 0 && k2Offset < vLength && v2[k2Offset] != -1 {
					if x1 >= n-v2[k2Offset] {
						return x1, y1, true
					}
				}
			}
		}
		for k2 := -step + k2start; k2 <= step-k2end; k2 += 2 {
			k2Offset := vOffset + k2
			var x2 int
			if k2 == -step || (k2 != step && v2[k2Offset-1] < v2[k2Offset+1]) {
				x2 = v2[k2Offset+1]
			} else {
				x2 = v2[k2Offset-1] + 1
			}
			y2 := x2 - k2
			for x2 < n && y2 < m && a[n-x2-1] == b[m-y2-1] {
				x2++
				y2++
			}
			v2[k2Offset] = x2
			switch {
			case x2 > n:
				k2end += 2
			case y2 > m:
				k2start += 2
			case !front:
				k1Offset := vOffset + delta - k2
				if k1Offset >= 0 && k1Offset < vLength && v1[k1Offset] != -1 {
					x1 := v1[k1Offset]
					y1 := vOffset + x1 - k1Offset
					// The forward store keeps points that ran off an edge;
					// only an in-grid point can split the range.
					if x1 <= n && y1 >= 0 && y1 <= m && x1 >= n-x2 {
						return x1, y1, true
					}
				}
			}
		}
	}
	return 0, 0, false
}

// DocumentDiff is one comparison, ready to render: the aligned rows the
// workspace draws, the hunks its change list navigates, and the stats the
// summary states. The frontend renders it as-is — it never re-diffs.
type DocumentDiff struct {
	Rows  []DocumentDiffRow  `json:"rows"`
	Hunks []DocumentDiffHunk `json:"hunks"`
	Stats DocumentDiffStats  `json:"stats"`
}

// DocumentDiffRow is one aligned row. Kind is "equal" (both sides), "delete"
// (before only), "insert" (after only), or "change" (a deleted line and an
// inserted one paired by position inside a change block). Under
// ignore-whitespace an equal row's two texts may differ in spacing — each
// side keeps its own text.
type DocumentDiffRow struct {
	Kind   string            `json:"kind"`
	Before *DocumentDiffLine `json:"before,omitempty"`
	After  *DocumentDiffLine `json:"after,omitempty"`
}

// DocumentDiffLine is one side of a row: the 1-based line number and text,
// plus word segments when the row is a change similar enough to mark.
type DocumentDiffLine struct {
	Number   int                   `json:"number"`
	Text     string                `json:"text"`
	Segments []DocumentDiffSegment `json:"segments,omitempty"`
}

// DocumentDiffSegment is a run of a changed line's text; Changed marks the
// words this side deleted (before) or inserted (after).
type DocumentDiffSegment struct {
	Text    string `json:"text"`
	Changed bool   `json:"changed,omitempty"`
}

// DocumentDiffHunk is one change: a maximal run of non-equal rows
// (FirstRow..LastRow, inclusive row indexes). Line/Count follow the unified
// diff convention per side — Line is the hunk's first line on that side, or,
// when the side has none (a pure insertion or deletion), the line just before
// the change point (0 at the document's top). Section names where the change
// sits: the scene heading in effect (Fountain) or the nearest heading above
// (Markdown) at the change's first non-blank line, read from the after side
// when it has lines, else the before side; empty before the first scene or
// heading.
type DocumentDiffHunk struct {
	FirstRow    int    `json:"firstRow"`
	LastRow     int    `json:"lastRow"`
	BeforeLine  int    `json:"beforeLine"`
	BeforeCount int    `json:"beforeCount"`
	AfterLine   int    `json:"afterLine"`
	AfterCount  int    `json:"afterCount"`
	Added       int    `json:"added"`
	Removed     int    `json:"removed"`
	Section     string `json:"section,omitempty"`
}

// DocumentDiffStats counts lines git-style: a changed line is one removal
// plus one addition. Changes is the hunk count.
type DocumentDiffStats struct {
	Added     int  `json:"added"`
	Removed   int  `json:"removed"`
	Changes   int  `json:"changes"`
	Identical bool `json:"identical"`
}

// splitDocumentLines splits a document into lines: CRLF normalizes, and a
// final newline ends the last line rather than opening an empty one — so
// "text" and "text\n" compare identical.
func splitDocumentLines(text string) []string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	if text == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(text, "\n"), "\n")
}

// buildDocumentDiff compares two document texts. The formats ("markdown" |
// "fountain") pick each side's section labels; ignoreWhitespace compares
// lines with whitespace runs collapsed and the ends trimmed (the rows still
// show each side's own text).
func buildDocumentDiff(beforeText, afterText, beforeFormat, afterFormat string, ignoreWhitespace bool) DocumentDiff {
	before := splitDocumentLines(beforeText)
	after := splitDocumentLines(afterText)
	keys := map[string]int{}
	intern := func(lines []string) []int {
		out := make([]int, len(lines))
		for i, line := range lines {
			if ignoreWhitespace {
				line = strings.Join(strings.Fields(line), " ")
			}
			id, ok := keys[line]
			if !ok {
				id = len(keys)
				keys[line] = id
			}
			out[i] = id
		}
		return out
	}
	ops := diffKeys(intern(before), intern(after), true)

	diff := DocumentDiff{Rows: make([]DocumentDiffRow, 0, len(ops)), Hunks: []DocumentDiffHunk{}}
	beforeSections := documentSectionIndex(beforeText, beforeFormat)
	afterSections := documentSectionIndex(afterText, afterFormat)
	for i := 0; i < len(ops); {
		if ops[i].kind == diffEqual {
			diff.Rows = append(diff.Rows, DocumentDiffRow{
				Kind:   "equal",
				Before: &DocumentDiffLine{Number: ops[i].before + 1, Text: before[ops[i].before]},
				After:  &DocumentDiffLine{Number: ops[i].after + 1, Text: after[ops[i].after]},
			})
			i++
			continue
		}
		// A change block: every op up to the next equal. Within it the
		// deletes and inserts pair by position — the script's own
		// interleaving carries no meaning for display.
		dels, inss := []int{}, []int{}
		for ; i < len(ops) && ops[i].kind != diffEqual; i++ {
			if ops[i].kind == diffDelete {
				dels = append(dels, ops[i].before)
			} else {
				inss = append(inss, ops[i].after)
			}
		}
		hunk := DocumentDiffHunk{FirstRow: len(diff.Rows), BeforeCount: len(dels), AfterCount: len(inss), Added: len(inss), Removed: len(dels)}
		hunk.BeforeLine = changePointLine(dels, diff.Rows, true)
		hunk.AfterLine = changePointLine(inss, diff.Rows, false)
		for k := 0; k < max(len(dels), len(inss)); k++ {
			row := DocumentDiffRow{}
			if k < len(dels) {
				row.Before = &DocumentDiffLine{Number: dels[k] + 1, Text: before[dels[k]]}
			}
			if k < len(inss) {
				row.After = &DocumentDiffLine{Number: inss[k] + 1, Text: after[inss[k]]}
			}
			switch {
			case row.Before != nil && row.After != nil:
				row.Kind = "change"
				row.Before.Segments, row.After.Segments = wordDiffSegments(row.Before.Text, row.After.Text, ignoreWhitespace)
			case row.Before != nil:
				row.Kind = "delete"
			default:
				row.Kind = "insert"
			}
			diff.Rows = append(diff.Rows, row)
		}
		hunk.LastRow = len(diff.Rows) - 1
		if hunk.AfterCount > 0 {
			hunk.Section = afterSections.at(sectionLine(inss, after))
		} else {
			hunk.Section = beforeSections.at(sectionLine(dels, before))
		}
		diff.Hunks = append(diff.Hunks, hunk)
		diff.Stats.Added += hunk.Added
		diff.Stats.Removed += hunk.Removed
	}
	diff.Stats.Changes = len(diff.Hunks)
	diff.Stats.Identical = len(diff.Hunks) == 0
	return diff
}

// changePointLine is a hunk side's unified-diff line: its first line when the
// side has lines, else the same side's line on the row just above the change
// (0 at the document's top).
func changePointLine(indexes []int, rows []DocumentDiffRow, beforeSide bool) int {
	if len(indexes) > 0 {
		return indexes[0] + 1
	}
	for r := len(rows) - 1; r >= 0; r-- {
		line := rows[r].After
		if beforeSide {
			line = rows[r].Before
		}
		if line != nil {
			return line.Number
		}
	}
	return 0
}

// sectionLine picks the 1-based line a hunk's section is read at: its first
// non-blank changed line. A new scene usually arrives with the blank line
// that separates it from the previous one, and that blank still sits in the
// previous scene — labelling by it would file "INT. WAREHOUSE" under the
// scene before it. An all-blank hunk reads at its first line.
func sectionLine(indexes []int, lines []string) int {
	for _, index := range indexes {
		if strings.TrimSpace(lines[index]) != "" {
			return index + 1
		}
	}
	return indexes[0] + 1
}

// diffTokenize splits a line into word runs (letters, digits, underscore),
// whitespace runs, and single other characters — the units a word diff marks.
func diffTokenize(text string) []string {
	tokens := []string{}
	class := func(r rune) int {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_':
			return 1
		case unicode.IsSpace(r):
			return 2
		default:
			return 3
		}
	}
	start, prevClass := 0, 0
	for i, r := range text {
		c := class(r)
		if i > start && (c != prevClass || c == 3) {
			tokens = append(tokens, text[start:i])
			start = i
		}
		prevClass = c
	}
	if start < len(text) {
		tokens = append(tokens, text[start:])
	}
	return tokens
}

// wordDiffSegments marks the words a changed line pair differs by, or returns
// nil for both sides when the pair is too dissimilar to read as one edited
// line (wordDiffMinSimilarity) — the row is then tinted whole. Under
// ignoreWhitespace every whitespace run matches every other, so a respaced
// phrase marks only its words.
func wordDiffSegments(beforeText, afterText string, ignoreWhitespace bool) ([]DocumentDiffSegment, []DocumentDiffSegment) {
	beforeTokens := diffTokenize(beforeText)
	afterTokens := diffTokenize(afterText)
	keys := map[string]int{}
	intern := func(tokens []string) []int {
		out := make([]int, len(tokens))
		for i, token := range tokens {
			key := token
			if ignoreWhitespace && strings.TrimSpace(token) == "" {
				key = " "
			}
			id, ok := keys[key]
			if !ok {
				id = len(keys)
				keys[key] = id
			}
			out[i] = id
		}
		return out
	}
	ops := diffKeys(intern(beforeTokens), intern(afterTokens), false)
	beforeChanged := make([]bool, len(beforeTokens))
	afterChanged := make([]bool, len(afterTokens))
	matched, total := 0, 0
	for _, op := range ops {
		switch op.kind {
		case diffEqual:
			if strings.TrimSpace(beforeTokens[op.before]) != "" {
				matched++
				total += 2
			}
		case diffDelete:
			beforeChanged[op.before] = true
			if strings.TrimSpace(beforeTokens[op.before]) != "" {
				total++
			}
		case diffInsert:
			afterChanged[op.after] = true
			if strings.TrimSpace(afterTokens[op.after]) != "" {
				total++
			}
		}
	}
	if total > 0 && float64(2*matched)/float64(total) < wordDiffMinSimilarity {
		return nil, nil
	}
	return mergeDiffSegments(beforeTokens, beforeChanged), mergeDiffSegments(afterTokens, afterChanged)
}

// mergeDiffSegments joins a side's tokens into runs of equal changed-ness. A
// whitespace token sitting between two changed tokens is marked changed too,
// so a rewritten phrase highlights as one span ("quick brown"), not words
// with gaps.
func mergeDiffSegments(tokens []string, changed []bool) []DocumentDiffSegment {
	for i := 1; i+1 < len(tokens); i++ {
		if !changed[i] && changed[i-1] && changed[i+1] && strings.TrimSpace(tokens[i]) == "" {
			changed[i] = true
		}
	}
	segments := []DocumentDiffSegment{}
	for i, token := range tokens {
		if n := len(segments); n > 0 && segments[n-1].Changed == changed[i] {
			segments[n-1].Text += token
			continue
		}
		segments = append(segments, DocumentDiffSegment{Text: token, Changed: changed[i]})
	}
	return segments
}

// documentSection is one place a hunk can sit: the 1-based line a section
// starts on and its label ("" resets — the stretch has no section).
type documentSection struct {
	line  int
	title string
}

type documentSectionList []documentSection

// at names the section in effect at a 1-based line: the last entry starting
// on or before it.
func (sections documentSectionList) at(line int) string {
	k := sort.Search(len(sections), func(i int) bool { return sections[i].line > line })
	if k == 0 {
		return ""
	}
	return sections[k-1].title
}

// documentSectionIndex lists a document's sections in line order. Fountain:
// the title page (when the script opens with one, up to its first body
// element) and every scene heading — the same parse the editor's outline and
// the PDF export read. Markdown: the ATX headings outside code fences, the
// rules of the editor's outline (markdownOutline in markdownModel.ts).
func documentSectionIndex(text, format string) documentSectionList {
	sections := documentSectionList{}
	if format == documentFormatFountain {
		elements, titlePage := fountainParse([]byte(text))
		if len(titlePage) > 0 {
			sections = append(sections, documentSection{line: 1, title: "Title page"})
			bodyStart := len(splitDocumentLines(text)) + 1
			if len(elements) > 0 {
				bodyStart = elements[0].Line
			}
			sections = append(sections, documentSection{line: bodyStart})
		}
		for _, element := range elements {
			if element.Kind == fountainScene {
				sections = append(sections, documentSection{line: element.Line, title: element.Text})
			}
		}
		return sections
	}
	var fenceChar byte
	fenceLength := 0
	for index, line := range splitDocumentLines(text) {
		markerChar, markerLength, rest := markdownFenceMarker(line)
		if fenceLength > 0 {
			if markerChar == fenceChar && markerLength >= fenceLength && strings.TrimSpace(rest) == "" {
				fenceLength = 0
			}
			continue
		}
		if markerLength > 0 {
			fenceChar, fenceLength = markerChar, markerLength
			continue
		}
		if title, ok := markdownHeadingTitle(line); ok {
			if title == "" {
				title = "Untitled heading"
			}
			sections = append(sections, documentSection{line: index + 1, title: title})
		}
	}
	return sections
}

// markdownFenceMarker reports the code fence a line opens or closes — up to
// three leading whitespace characters, then a run of 3+ backticks or tildes —
// with the text after the run (a closing fence carries nothing there). The
// marker's character and length decide what closes it, per CommonMark; the
// rules are fenceOpenRegex/fenceCloseRegex in markdownModel.ts.
func markdownFenceMarker(line string) (byte, int, string) {
	indent := 0
	for indent < len(line) && (line[indent] == ' ' || line[indent] == '\t') {
		indent++
	}
	if indent > 3 || indent >= len(line) || (line[indent] != '`' && line[indent] != '~') {
		return 0, 0, ""
	}
	char := line[indent]
	run := 0
	for indent+run < len(line) && line[indent+run] == char {
		run++
	}
	if run < 3 {
		return 0, 0, ""
	}
	return char, run, line[indent+run:]
}

// markdownHeadingTitle reads an ATX heading — 1 to 6 #s then whitespace — the
// way markdownOutline does: the title is the rest of the line, with a closing
// run of #s stripped only when whitespace precedes it ("# A ##" → "A", but
// "# C#" keeps its #).
func markdownHeadingTitle(line string) (string, bool) {
	hashes := 0
	for hashes < len(line) && line[hashes] == '#' {
		hashes++
	}
	if hashes == 0 || hashes > 6 || hashes == len(line) || (line[hashes] != ' ' && line[hashes] != '\t') {
		return "", false
	}
	title := strings.TrimRight(strings.TrimLeft(line[hashes:], " \t"), " \t")
	if body := strings.TrimRight(title, "#"); body != title && body != strings.TrimRight(body, " \t") {
		title = body
	}
	return strings.TrimSpace(title), true
}
