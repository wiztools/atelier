package document

import (
	"fmt"
	"math/rand"
	"reflect"
	"strings"
	"testing"
	"time"
)

// diffRowsText renders rows compactly for failure messages and exact
// expectations: " text" equal, "-text" delete, "+text" insert, "~a|b" change.
func diffRowsText(rows []DiffRow) []string {
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		switch row.Kind {
		case "equal":
			out = append(out, " "+row.Before.Text)
		case "delete":
			out = append(out, "-"+row.Before.Text)
		case "insert":
			out = append(out, "+"+row.After.Text)
		case "change":
			out = append(out, "~"+row.Before.Text+"|"+row.After.Text)
		default:
			out = append(out, "?"+row.Kind)
		}
	}
	return out
}

// assertDiffReconstructs checks the one property every diff must hold: the
// before side's lines, read down the rows, are exactly the before document
// (numbered 1..n in order), the same for the after side, and an equal row
// pairs lines that compare equal.
func assertDiffReconstructs(t *testing.T, diff Diff, before, after string, ignoreWhitespace bool) {
	t.Helper()
	wantBefore := SplitLines(before)
	wantAfter := SplitLines(after)
	gotBefore, gotAfter := []string{}, []string{}
	for index, row := range diff.Rows {
		if row.Before != nil {
			if row.Before.Number != len(gotBefore)+1 {
				t.Fatalf("row %d before line number = %d, want %d", index, row.Before.Number, len(gotBefore)+1)
			}
			gotBefore = append(gotBefore, row.Before.Text)
		}
		if row.After != nil {
			if row.After.Number != len(gotAfter)+1 {
				t.Fatalf("row %d after line number = %d, want %d", index, row.After.Number, len(gotAfter)+1)
			}
			gotAfter = append(gotAfter, row.After.Text)
		}
		if row.Kind == "equal" {
			left, right := row.Before.Text, row.After.Text
			if ignoreWhitespace {
				left, right = strings.Join(strings.Fields(left), " "), strings.Join(strings.Fields(right), " ")
			}
			if left != right {
				t.Fatalf("row %d is equal but pairs %q with %q", index, row.Before.Text, row.After.Text)
			}
		}
	}
	if !reflect.DeepEqual(gotBefore, nonNilLines(wantBefore)) {
		t.Fatalf("before side reads back %d lines, want %d", len(gotBefore), len(wantBefore))
	}
	if !reflect.DeepEqual(gotAfter, nonNilLines(wantAfter)) {
		t.Fatalf("after side reads back %d lines, want %d", len(gotAfter), len(wantAfter))
	}
}

func nonNilLines(lines []string) []string {
	if lines == nil {
		return []string{}
	}
	return lines
}

func TestSplitDocumentLines(t *testing.T) {
	cases := []struct {
		text string
		want []string
	}{
		{"", nil},
		{"a", []string{"a"}},
		{"a\n", []string{"a"}},
		{"a\n\n", []string{"a", ""}},
		{"a\r\nb\r\n", []string{"a", "b"}},
		{"\n", []string{""}},
	}
	for _, tc := range cases {
		if got := SplitLines(tc.text); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("SplitLines(%q) = %q, want %q", tc.text, got, tc.want)
		}
	}
}

func TestBuildDocumentDiffBasics(t *testing.T) {
	cases := []struct {
		name          string
		before, after string
		want          []string
		added         int
		removed       int
		changes       int
	}{
		{"identical", "a\nb\nc", "a\nb\nc", []string{" a", " b", " c"}, 0, 0, 0},
		{"both empty", "", "", []string{}, 0, 0, 0},
		{"before empty", "", "a\nb", []string{"+a", "+b"}, 2, 0, 1},
		{"after empty", "a\nb", "", []string{"-a", "-b"}, 0, 2, 1},
		{"trailing newline ignored", "a\nb\n", "a\nb", []string{" a", " b"}, 0, 0, 0},
		{"crlf normalized", "a\r\nb\r\n", "a\nb", []string{" a", " b"}, 0, 0, 0},
		{"insert middle", "a\nc", "a\nb\nc", []string{" a", "+b", " c"}, 1, 0, 1},
		{"delete middle", "a\nb\nc", "a\nc", []string{" a", "-b", " c"}, 0, 1, 1},
		{"replace one", "a\nb\nc", "a\nx\nc", []string{" a", "~b|x", " c"}, 1, 1, 1},
		{"two changes", "a\nb\nc\nd\ne", "a\nB\nc\nd\nE", []string{" a", "~b|B", " c", " d", "~e|E"}, 2, 2, 2},
		{"uneven block", "a\nb\nz", "a\nx\ny\nz", []string{" a", "~b|x", "+y", " z"}, 2, 1, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			diff := BuildDiff(tc.before, tc.after, FormatMarkdown, FormatMarkdown, false)
			if got := diffRowsText(diff.Rows); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("rows = %q, want %q", got, tc.want)
			}
			if diff.Stats.Added != tc.added || diff.Stats.Removed != tc.removed || diff.Stats.Changes != tc.changes {
				t.Fatalf("stats = %+v, want +%d -%d in %d changes", diff.Stats, tc.added, tc.removed, tc.changes)
			}
			if diff.Stats.Identical != (tc.changes == 0) {
				t.Fatalf("identical = %v with %d changes", diff.Stats.Identical, tc.changes)
			}
			if diff.Hunks == nil || diff.Rows == nil {
				t.Fatalf("rows and hunks must be non-nil for the frontend")
			}
			assertDiffReconstructs(t, diff, tc.before, tc.after, false)
		})
	}
}

// TestBuildDocumentDiffHunkLines pins the unified-diff line convention: a
// side with lines starts at its first line; a side without starts at the line
// just before the change point (0 at the top).
func TestBuildDocumentDiffHunkLines(t *testing.T) {
	cases := []struct {
		name                     string
		before, after            string
		beforeLine, beforeCount  int
		afterLine, afterCount    int
		firstRow, lastRow, added int
	}{
		{"insert middle", "a\nb", "a\nx\nb", 1, 0, 2, 1, 1, 1, 1},
		{"insert top", "a", "x\na", 0, 0, 1, 1, 0, 0, 1},
		{"delete top", "x\na", "a", 1, 1, 0, 0, 0, 0, 0},
		{"replace two", "a\nb\nc\nd", "a\nx\ny\nd", 2, 2, 2, 2, 1, 2, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			diff := BuildDiff(tc.before, tc.after, FormatMarkdown, FormatMarkdown, false)
			if len(diff.Hunks) != 1 {
				t.Fatalf("hunks = %+v, want one", diff.Hunks)
			}
			h := diff.Hunks[0]
			if h.BeforeLine != tc.beforeLine || h.BeforeCount != tc.beforeCount || h.AfterLine != tc.afterLine || h.AfterCount != tc.afterCount {
				t.Fatalf("hunk lines = before %d+%d after %d+%d, want before %d+%d after %d+%d", h.BeforeLine, h.BeforeCount, h.AfterLine, h.AfterCount, tc.beforeLine, tc.beforeCount, tc.afterLine, tc.afterCount)
			}
			if h.FirstRow != tc.firstRow || h.LastRow != tc.lastRow || h.Added != tc.added {
				t.Fatalf("hunk rows = %d..%d +%d, want %d..%d +%d", h.FirstRow, h.LastRow, h.Added, tc.firstRow, tc.lastRow, tc.added)
			}
		})
	}
}

func TestBuildDocumentDiffWordSegments(t *testing.T) {
	diff := BuildDiff("JOHN\nI never said that.", "JOHN\nI never said it.", FormatFountain, FormatFountain, false)
	if len(diff.Rows) != 2 || diff.Rows[1].Kind != "change" {
		t.Fatalf("rows = %q, want the dialogue line as a change", diffRowsText(diff.Rows))
	}
	wantBefore := []DiffSegment{{Text: "I never said "}, {Text: "that", Changed: true}, {Text: "."}}
	wantAfter := []DiffSegment{{Text: "I never said "}, {Text: "it", Changed: true}, {Text: "."}}
	if !reflect.DeepEqual(diff.Rows[1].Before.Segments, wantBefore) || !reflect.DeepEqual(diff.Rows[1].After.Segments, wantAfter) {
		t.Fatalf("segments = %+v / %+v, want %+v / %+v", diff.Rows[1].Before.Segments, diff.Rows[1].After.Segments, wantBefore, wantAfter)
	}

	// A rewritten phrase highlights as one span — the space between two
	// changed words joins them.
	before, after := wordDiffSegments("the quick brown fox", "the slow red fox", false)
	if !reflect.DeepEqual(before, []DiffSegment{{Text: "the "}, {Text: "quick brown", Changed: true}, {Text: " fox"}}) {
		t.Fatalf("before segments = %+v, want the rewritten phrase as one span", before)
	}
	if !reflect.DeepEqual(after, []DiffSegment{{Text: "the "}, {Text: "slow red", Changed: true}, {Text: " fox"}}) {
		t.Fatalf("after segments = %+v, want the rewritten phrase as one span", after)
	}

	// Too dissimilar to read as one edited line: no segments, the row tints
	// whole.
	if before, after := wordDiffSegments("The quick brown fox jumps.", "Completely unrelated words here!", false); before != nil || after != nil {
		t.Fatalf("dissimilar pair segments = %+v / %+v, want none", before, after)
	}
	diff = BuildDiff("a\nThe quick brown fox jumps.\nz", "a\nCompletely unrelated words here!\nz", FormatMarkdown, FormatMarkdown, false)
	if row := diff.Rows[1]; row.Kind != "change" || row.Before.Segments != nil || row.After.Segments != nil {
		t.Fatalf("dissimilar change row = %+v, want a change with no segments", row)
	}
}

func TestDiffTokenize(t *testing.T) {
	cases := []struct {
		text string
		want []string
	}{
		{"I never said that.", []string{"I", " ", "never", " ", "said", " ", "that", "."}},
		{"don't", []string{"don", "'", "t"}},
		{"  x", []string{"  ", "x"}},
		{"café, ok", []string{"café", ",", " ", "ok"}},
		{"(V.O.)", []string{"(", "V", ".", "O", ".", ")"}},
		{"snake_case 42", []string{"snake_case", " ", "42"}},
		{"", []string{}},
	}
	for _, tc := range cases {
		if got := diffTokenize(tc.text); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("diffTokenize(%q) = %q, want %q", tc.text, got, tc.want)
		}
	}
}

func TestBuildDocumentDiffIgnoreWhitespace(t *testing.T) {
	before := "JOHN  \nHello   world\n\tIndented"
	after := "JOHN\nHello world\nIndented"
	exact := BuildDiff(before, after, FormatMarkdown, FormatMarkdown, false)
	if exact.Stats.Identical || exact.Stats.Changes != 1 || exact.Stats.Removed != 3 {
		t.Fatalf("exact stats = %+v, want one change of three whitespace-edited lines", exact.Stats)
	}
	loose := BuildDiff(before, after, FormatMarkdown, FormatMarkdown, true)
	if !loose.Stats.Identical {
		t.Fatalf("ignore-whitespace stats = %+v, want identical", loose.Stats)
	}
	// Each side keeps its own text on an equal row.
	if loose.Rows[1].Before.Text != "Hello   world" || loose.Rows[1].After.Text != "Hello world" {
		t.Fatalf("equal row texts = %q / %q, want each side's own spacing", loose.Rows[1].Before.Text, loose.Rows[1].After.Text)
	}
	assertDiffReconstructs(t, loose, before, after, true)

	// Under ignore-whitespace a respaced, reworded line marks only its words.
	diff := BuildDiff("a\nthe  quick fox\nz", "a\nthe quick  dog\nz", FormatMarkdown, FormatMarkdown, true)
	before2 := diff.Rows[1].Before.Segments
	if len(before2) != 2 || before2[0].Changed || before2[0].Text != "the  quick " || !before2[1].Changed || before2[1].Text != "fox" {
		t.Fatalf("respaced segments = %+v, want only the reworded word marked", before2)
	}
}

// TestBuildDocumentDiffPatienceAnchors pins why the line diff anchors on
// unique lines: when a line of dialogue and blank separators trade places,
// two scripts are equally short — keep the dialogue matched and move the
// blanks, or keep a blank matched and move the dialogue. Minimal-edit Myers
// is indifferent and (in this implementation) keeps the blank; patience
// anchors on the dialogue, which occurs once on each side, so the writer
// sees their line unchanged and the spacing as the edit.
func TestBuildDocumentDiffPatienceAnchors(t *testing.T) {
	before := "JOHN\nI never said that.\n\n\nMARY"
	after := "JOHN\n\nI never said that.\nMARY"
	diff := BuildDiff(before, after, FormatFountain, FormatFountain, false)
	assertDiffReconstructs(t, diff, before, after, false)
	if got, want := diffRowsText(diff.Rows), []string{" JOHN", "+", " I never said that.", "-", "-", " MARY"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("rows = %q, want the dialogue kept and the blank lines moved (%q)", got, want)
	}

	// The contrast the anchoring exists for: plain Myers over the same
	// lines keeps a blank matched and reports the dialogue as moved.
	keys := map[string]int{}
	intern := func(lines []string) []int {
		out := make([]int, len(lines))
		for i, line := range lines {
			id, ok := keys[line]
			if !ok {
				id = len(keys)
				keys[line] = id
			}
			out[i] = id
		}
		return out
	}
	beforeLines, afterLines := SplitLines(before), SplitLines(after)
	for _, op := range diffKeys(intern(beforeLines), intern(afterLines), false) {
		if op.kind == diffEqual && beforeLines[op.before] == "I never said that." {
			t.Fatalf("plain Myers kept the dialogue matched too — the patience test no longer shows a difference; revisit its premise")
		}
	}
}

func TestLongestIncreasingAnchors(t *testing.T) {
	pairs := []diffAnchor{{0, 3}, {1, 0}, {2, 1}, {3, 4}, {4, 2}, {5, 5}}
	want := []diffAnchor{{1, 0}, {2, 1}, {4, 2}, {5, 5}}
	if got := longestIncreasingAnchors(pairs); !reflect.DeepEqual(got, want) {
		t.Fatalf("longestIncreasingAnchors = %v, want %v", got, want)
	}
	if got := longestIncreasingAnchors([]diffAnchor{{7, 9}}); !reflect.DeepEqual(got, []diffAnchor{{7, 9}}) {
		t.Fatalf("single pair = %v", got)
	}
}

// TestBuildDocumentDiffRepeatedLinesReconstruct fuzzes screenplay-shaped
// documents — few distinct lines, heavy repetition, exactly what stresses an
// aligner — through random edits and checks every result reconstructs both
// sides and keeps equal rows honest.
func TestBuildDocumentDiffRepeatedLinesReconstruct(t *testing.T) {
	vocabulary := []string{"JOHN", "MARY", "", "", "CUT TO:", "Yes.", "No.", "INT. HALL - DAY", "(beat)"}
	rng := rand.New(rand.NewSource(7))
	for trial := 0; trial < 300; trial++ {
		lines := make([]string, rng.Intn(40))
		for i := range lines {
			lines[i] = vocabulary[rng.Intn(len(vocabulary))]
		}
		edited := append([]string{}, lines...)
		for edits := rng.Intn(6); edits > 0; edits-- {
			at := 0
			if len(edited) > 0 {
				at = rng.Intn(len(edited) + 1)
			}
			switch rng.Intn(3) {
			case 0:
				edited = append(edited[:at], append([]string{vocabulary[rng.Intn(len(vocabulary))]}, edited[at:]...)...)
			case 1:
				if at < len(edited) {
					edited = append(edited[:at], edited[at+1:]...)
				}
			default:
				if at < len(edited) {
					edited[at] = fmt.Sprintf("line %d", rng.Intn(5))
				}
			}
		}
		before, after := strings.Join(lines, "\n"), strings.Join(edited, "\n")
		diff := BuildDiff(before, after, FormatFountain, FormatFountain, false)
		assertDiffReconstructs(t, diff, before, after, false)
		if want := reflect.DeepEqual(SplitLines(before), SplitLines(after)); diff.Stats.Identical != want {
			t.Fatalf("trial %d: identical = %v, want %v for before %q after %q", trial, diff.Stats.Identical, want, before, after)
		}
	}
}

func TestDocumentSectionIndexFountain(t *testing.T) {
	before := strings.Join([]string{
		"Title: Test",        // 1
		"Author: Me",         // 2
		"",                   // 3
		"FADE IN:",           // 4
		"",                   // 5
		"INT. KITCHEN - DAY", // 6
		"",                   // 7
		"John enters.",       // 8
		"",                   // 9
		".FLASHBACK",         // 10
		"",                   // 11
		"Mary waves.",        // 12
	}, "\n")
	sections := documentSectionIndex(before, FormatFountain)
	cases := map[int]string{1: "Title page", 2: "Title page", 4: "", 5: "", 6: "INT. KITCHEN - DAY", 8: "INT. KITCHEN - DAY", 10: "FLASHBACK", 12: "FLASHBACK"}
	for line, want := range cases {
		if got := sections.at(line); got != want {
			t.Errorf("section at line %d = %q, want %q", line, got, want)
		}
	}

	after := strings.Replace(strings.Replace(strings.Replace(before, "Author: Me", "Author: You", 1), "John enters.", "John runs in.", 1), "Mary waves.", "Mary waves back.", 1)
	diff := BuildDiff(before, after, FormatFountain, FormatFountain, false)
	got := []string{}
	for _, hunk := range diff.Hunks {
		got = append(got, hunk.Section)
	}
	if want := []string{"Title page", "INT. KITCHEN - DAY", "FLASHBACK"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("hunk sections = %q, want %q", got, want)
	}

	// A script without a title page has no title-page section, and a change
	// before the first scene sits in no section.
	plain := "FADE IN:\n\nINT. ROOF - NIGHT\n\nRain."
	if got := documentSectionIndex(plain, FormatFountain).at(1); got != "" {
		t.Fatalf("section before the first scene = %q, want none", got)
	}
}

func TestDocumentSectionIndexMarkdown(t *testing.T) {
	before := strings.Join([]string{
		"Preamble.",       // 1
		"# Intro",         // 2
		"text a",          // 3
		"```js",           // 4
		"# not a heading", // 5
		"code",            // 6
		"```",             // 7
		"## Usage ##",     // 8
		"text b",          // 9
		"####### seven",   // 10
		"#hashtag",        // 11
		"# C#",            // 12
	}, "\n")
	sections := documentSectionIndex(before, FormatMarkdown)
	cases := map[int]string{1: "", 2: "Intro", 3: "Intro", 5: "Intro", 6: "Intro", 9: "Usage", 10: "Usage", 11: "Usage", 12: "C#"}
	for line, want := range cases {
		if got := sections.at(line); got != want {
			t.Errorf("section at line %d = %q, want %q", line, got, want)
		}
	}

	// A deletion labels from the before side; an insertion from the after.
	after := strings.Replace(before, "text a\n", "", 1)
	after = strings.Replace(after, "text b", "text b\ntext c", 1)
	diff := BuildDiff(before, after, FormatMarkdown, FormatMarkdown, false)
	got := []string{}
	for _, hunk := range diff.Hunks {
		got = append(got, hunk.Section)
	}
	if want := []string{"Intro", "Usage"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("hunk sections = %q, want %q", got, want)
	}
}

func TestMarkdownHeadingTitle(t *testing.T) {
	cases := []struct {
		line  string
		title string
		ok    bool
	}{
		{"# Title", "Title", true},
		{"### Deep", "Deep", true},
		{"# A ##", "A", true},
		{"# A ## ", "A", true},
		{"# C#", "C#", true},
		{"# ##", "##", true},
		{"#   ", "", true},
		{"#", "", false},
		{"#nospace", "", false},
		{"####### seven", "", false},
		{"  # indented", "", false},
	}
	for _, tc := range cases {
		title, ok := markdownHeadingTitle(tc.line)
		if title != tc.title || ok != tc.ok {
			t.Errorf("markdownHeadingTitle(%q) = %q, %v; want %q, %v", tc.line, title, ok, tc.title, tc.ok)
		}
	}
}

// screenplayLines builds a deterministic screenplay-shaped document of n
// lines: unique scene headings and dialogue around heavily repeated cues and
// blanks.
func screenplayLines(rng *rand.Rand, n int) []string {
	lines := make([]string, 0, n)
	for scene := 0; len(lines) < n; scene++ {
		lines = append(lines, fmt.Sprintf("INT. LOCATION %d - DAY", scene), "")
		for speech := 0; speech < 4 && len(lines) < n; speech++ {
			lines = append(lines, []string{"JOHN", "MARY", "ALEX"}[rng.Intn(3)], fmt.Sprintf("Line %d of scene %d, take %d.", speech, scene, rng.Intn(1000)), "")
		}
	}
	return lines[:n]
}

func TestBuildDocumentDiffPerformance(t *testing.T) {
	if testing.Short() {
		t.Skip("performance bounds run in the full suite")
	}
	rng := rand.New(rand.NewSource(11))
	draft := screenplayLines(rng, 8000)
	revision := append([]string{}, draft...)
	for i := 0; i < len(revision); i += 50 {
		revision[i] = revision[i] + " (revised)"
	}
	revision = append(revision[:4000], append([]string{"INT. NEW SCENE - NIGHT", "", "JOHN", "Brand new."}, revision[4000:]...)...)
	start := time.Now()
	diff := BuildDiff(strings.Join(draft, "\n"), strings.Join(revision, "\n"), FormatFountain, FormatFountain, false)
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("8,000-line revision compared in %v, want well under a second", elapsed)
	}
	if diff.Stats.Changes < 100 {
		t.Fatalf("revision stats = %+v, want every scattered edit found", diff.Stats)
	}
	assertDiffReconstructs(t, diff, strings.Join(draft, "\n"), strings.Join(revision, "\n"), false)

	// Two unrelated documents: nothing unique in common, so the gaps go to
	// the capped Myers search.
	left := screenplayLines(rand.New(rand.NewSource(1)), 5000)
	right := screenplayLines(rand.New(rand.NewSource(2)), 5000)
	for i := range right {
		right[i] = strings.ReplaceAll(right[i], "LOCATION", "PLACE")
	}
	start = time.Now()
	unrelated := BuildDiff(strings.Join(left, "\n"), strings.Join(right, "\n"), FormatFountain, FormatFountain, false)
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("two unrelated 5,000-line documents compared in %v, want well under a second", elapsed)
	}
	assertDiffReconstructs(t, unrelated, strings.Join(left, "\n"), strings.Join(right, "\n"), false)
}
