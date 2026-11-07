package main

import (
	"encoding/xml"
	"fmt"
	"regexp"
	"strings"
	"testing"
)

// spanNotation renders spans compactly — face flags, a colon, the text —
// so the inline table below reads like the frontend model's mirror of it.
func spanNotation(spans []FountainSpan) string {
	parts := make([]string, len(spans))
	for i, span := range spans {
		flags := ""
		if span.Bold {
			flags += "b"
		}
		if span.Italic {
			flags += "i"
		}
		if span.Underline {
			flags += "u"
		}
		parts[i] = flags + ":" + span.Text
	}
	return strings.Join(parts, "|")
}

// The same table runs in frontend/test/fountainModel.assert.ts — the preview
// and the PDF must read every marker the same way.
func TestFountainInlineSpans(t *testing.T) {
	cases := []struct{ in, want string }{
		{"plain text", ":plain text"},
		{"a *quiet* word", ":a |i:quiet|: word"},
		{"a **loud** word", ":a |b:loud|: word"},
		{"***both***", "bi:both"},
		{"one _underlined_ word", ":one |u:underlined|: word"},
		{"**bold *and italic* text**", "b:bold |bi:and italic|b: text"},
		{"_*underlined italic*_", "iu:underlined italic"},
		{"un*believ*able", ":un|i:believ|:able"},
		{"snake_case_name", ":snake_case_name"},
		{"5 * 3 * 2", ":5 * 3 * 2"},
		{"He falls*", ":He falls*"},
		{`\*not italic\*`, ":*not italic*"},
		{"****", ":****"},
		{"**", ":**"},
		{"*_text*_", "i:_text|:_"},
		{"", ""},
	}
	for _, tc := range cases {
		if got := spanNotation(fountainInlineSpans(tc.in)); got != tc.want {
			t.Errorf("fountainInlineSpans(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// The same script runs in the frontend model's assertions: the preview's
// margins and the PDF's blank lines come from this one rule.
func TestFountainSpaceBefore(t *testing.T) {
	script := "INT. ROOM - DAY\n\nLine one.\nLine two.\n\nAnother paragraph.\nMAYA\n(beat)\nHello.\n~ la la\n\n> THE END <\n> REALLY <\n\nCUT TO:\n\nEXT. STREET - NIGHT\n"
	elements, _ := fountainParse([]byte(script))
	got := []string{}
	var prev *FountainElement
	for i := range elements {
		got = append(got, fmt.Sprintf("%s=%d", elements[i].Kind, fountainSpaceBefore(prev, elements[i])))
		prev = &elements[i]
	}
	want := []string{
		"Scene Heading=0", "Action=1", "Action=0", "Action=1", "Character=1", "Parenthetical=0",
		"Dialogue=0", "Lyric=0", "Centered=1", "Centered=0", "Transition=1", "Scene Heading=2",
	}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("spacing = %v\nwant      %v", got, want)
	}
}

func TestFountainTitlePageKeepsLineBreaks(t *testing.T) {
	script := "Title: The Long Night\nDraft date: November 7, 2026\nContact:\n    Subhash Chandran\n    42 Lantern Lane\n\nINT. ROOM - DAY\n"
	_, titlePage := fountainParse([]byte(script))
	if titlePage["Contact"] != "Subhash Chandran\n42 Lantern Lane" {
		t.Fatalf("contact = %q, want its two lines", titlePage["Contact"])
	}
	lines := fountainTitlePageLines(pdfPaperLetter, titlePage)
	if len(lines) != pdfPaperLetter.linesPerPage {
		t.Fatalf("title page rows = %d, want the full %d-row page (contact pinned to the bottom)", len(lines), pdfPaperLetter.linesPerPage)
	}
	if lines[pdfPaperLetter.linesPerPage-2].text != "Subhash Chandran" || lines[pdfPaperLetter.linesPerPage-1].text != "42 Lantern Lane" {
		t.Fatalf("bottom rows = %q / %q, want the contact block a line apiece", lines[pdfPaperLetter.linesPerPage-2].text, lines[pdfPaperLetter.linesPerPage-1].text)
	}
	title := lines[pdfPaperLetter.titleTopRow()]
	if title.text != "THE LONG NIGHT" || !title.center || len(title.spans) != 1 || !title.spans[0].bold || !title.spans[0].underline {
		t.Fatalf("title row = %+v, want it centered, uppercase, bold and underlined", title)
	}
	// The draft date sits centered under the title the way the preview shows
	// it — and is never cut off the page.
	if lines[pdfPaperLetter.titleTopRow()+2].text != "November 7, 2026" || !lines[pdfPaperLetter.titleTopRow()+2].center {
		t.Fatalf("draft date row = %+v", lines[pdfPaperLetter.titleTopRow()+2])
	}
}

// paginatedScript runs the body pipeline the export uses.
func paginatedScript(script string, includeNotes bool) [][]fountainPDFLine {
	elements, _ := fountainParse([]byte(script))
	return fountainPaginate(pdfPaperLetter, fountainBlocks(pdfPaperLetter, elements, includeNotes))
}

func rowTexts(page []fountainPDFLine) []string {
	texts := make([]string, len(page))
	for i, line := range page {
		texts[i] = line.text
	}
	return texts
}

// fillerParagraphs is n one-row action paragraphs: 2n-1 rows of a page.
func fillerParagraphs(n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "Filler paragraph %d.\n\n", i+1)
	}
	return b.String()
}

func TestFountainLayoutMirrorsThePreview(t *testing.T) {
	script := "INT. ROOM - DAY\n\nShe *whispers*, then **shouts**.\nThe _door_ slams.\n\n@maya (v.o.)\n(beat)\nWho is there?\n\n~ la la la\n\n> the end <\n\n> fade out.\n"
	pages := paginatedScript(script, false)
	if len(pages) != 1 {
		t.Fatalf("pages = %d, want 1", len(pages))
	}
	rows := pages[0]
	got := rowTexts(rows)
	want := []string{
		"INT. ROOM - DAY", "",
		"She whispers, then shouts.", "The door slams.", "",
		"MAYA (V.O.)", "(beat)", "Who is there?", "",
		"la la la", "",
		"the end", "",
		"FADE OUT.",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("rows =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if !rows[0].spans[0].bold {
		t.Fatal("a scene heading prints bold")
	}
	if spans := rows[2].spans; len(spans) != 5 || !spans[1].italic || !spans[3].bold {
		t.Fatalf("action emphasis = %+v, want italic and bold runs", spans)
	}
	if spans := rows[3].spans; len(spans) != 3 || !spans[1].underline || spans[1].text != "door" {
		t.Fatalf("underline run = %+v", spans)
	}
	if rows[5].indent != fountainIndentChar || rows[6].indent != fountainIndentPar || rows[7].indent != fountainIndentDia {
		t.Fatalf("speech columns = %d/%d/%d", rows[5].indent, rows[6].indent, rows[7].indent)
	}
	if rows[9].indent != fountainIndentChar || !rows[9].spans[0].italic {
		t.Fatalf("lyric row = %+v, want italic at the cue column", rows[9])
	}
	if !rows[11].center || !rows[13].right {
		t.Fatalf("centered/transition rows = %+v / %+v", rows[11], rows[13])
	}
}

func TestFountainPaginateForcedBreaks(t *testing.T) {
	// A break opening the script, doubled breaks, and a trailing break never
	// print blank pages; one between two scenes starts the second on a new page.
	pages := paginatedScript("===\n\nINT. ONE\n\nAction.\n\n===\n\n===\n\nINT. TWO\n\nMore.\n\n===\n", false)
	if len(pages) != 2 {
		t.Fatalf("pages = %d (%v), want 2", len(pages), pages)
	}
	if pages[0][0].text != "INT. ONE" || pages[1][0].text != "INT. TWO" {
		t.Fatalf("page tops = %q / %q", pages[0][0].text, pages[1][0].text)
	}
	if strings.Contains(strings.Join(rowTexts(pages[0]), "\n"), "===") {
		t.Fatal("a page break must never print as text")
	}
}

func TestFountainPaginateSceneHeadingKeepsWithNext(t *testing.T) {
	// 26 filler paragraphs fill rows 0-50; the heading's two blank lines and
	// its own row would fit, but its action could not follow on this page —
	// so the heading opens the next one.
	pages := paginatedScript(fillerParagraphs(26)+"INT. NEXT ROOM - DAY\n\nThe door opens.\n", false)
	if len(pages) != 2 {
		t.Fatalf("pages = %d, want 2", len(pages))
	}
	last := pages[0][len(pages[0])-1]
	if last.text != "Filler paragraph 26." {
		t.Fatalf("page one ends with %q, want the last filler (no stranded heading)", last.text)
	}
	if pages[1][0].text != "INT. NEXT ROOM - DAY" || pages[1][1].text != "" || pages[1][2].text != "The door opens." {
		t.Fatalf("page two = %q", rowTexts(pages[1]))
	}
}

func TestFountainPaginateCueNeverStrands(t *testing.T) {
	// Three rows of room: a four-row speech can't split (two rows of speech
	// on each page, plus its cue and (MORE)), so it moves whole.
	pages := paginatedScript(fillerParagraphs(26)+"MAYA\nLine one.\nLine two.\nLine three.\n", false)
	if len(pages) != 2 {
		t.Fatalf("pages = %d, want 2", len(pages))
	}
	if got := rowTexts(pages[1]); strings.Join(got, "|") != "MAYA|Line one.|Line two.|Line three." {
		t.Fatalf("page two = %q, want the whole speech", got)
	}
}

func TestFountainPaginateSpeechContinues(t *testing.T) {
	var speech strings.Builder
	for i := 0; i < 12; i++ {
		fmt.Fprintf(&speech, "This is sentence number %d of the speech. ", i+1)
	}
	pages := paginatedScript(fillerParagraphs(22)+"MAYA (V.O.)\n"+speech.String()+"\n", false)
	if len(pages) != 2 {
		t.Fatalf("pages = %d, want 2", len(pages))
	}
	first, second := pages[0], pages[1]
	more := first[len(first)-1]
	if more.text != "(MORE)" || more.indent != fountainIndentChar {
		t.Fatalf("page one ends %+v, want (MORE) at the cue column", more)
	}
	if !strings.HasSuffix(first[len(first)-2].text, "speech.") {
		t.Fatalf("the break must land on a sentence's end, got %q", first[len(first)-2].text)
	}
	if second[0].text != "MAYA (V.O.) (CONT'D)" || second[0].indent != fountainIndentChar {
		t.Fatalf("page two opens %+v, want the continued cue", second[0])
	}
	if !strings.HasPrefix(second[1].text, "This is sentence number") {
		t.Fatalf("the continued speech re-wraps from the sentence: %q", second[1].text)
	}
	// Nothing is lost or doubled across the break.
	var words []string
	for _, line := range append(append([]fountainPDFLine{}, first...), second...) {
		if line.indent == fountainIndentDia {
			words = append(words, strings.Fields(line.text)...)
		}
	}
	if strings.Join(words, " ") != strings.TrimSpace(speech.String()) {
		t.Fatal("the split speech must carry exactly the source's words")
	}
}

func TestFountainPaginateSpeechLongerThanAPage(t *testing.T) {
	var speech strings.Builder
	for i := 0; i < 200; i++ {
		fmt.Fprintf(&speech, "Sentence %d goes on. ", i+1)
	}
	pages := paginatedScript("MAYA\n"+speech.String()+"\n", false)
	if len(pages) < 2 {
		t.Fatalf("pages = %d, want the speech across several", len(pages))
	}
	for i, page := range pages {
		if len(page) > pdfPaperLetter.linesPerPage {
			t.Fatalf("page %d has %d rows", i+1, len(page))
		}
		if i > 0 && page[0].text != "MAYA (CONT'D)" {
			t.Fatalf("page %d opens %q, want one (CONT'D), never doubled", i+1, page[0].text)
		}
		if i < len(pages)-1 && page[len(page)-1].text != "(MORE)" {
			t.Fatalf("page %d ends %q, want (MORE)", i+1, page[len(page)-1].text)
		}
	}
}

func TestFountainPaginatePageTopsAreNeverBlank(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 30; i++ {
		fmt.Fprintf(&b, "INT. ROOM %d - DAY\n\nSomething happens in room %d, and it takes a while to describe properly.\n\nMAYA\nAnd then?\n\n", i, i)
	}
	pages := paginatedScript(b.String(), false)
	if len(pages) < 3 {
		t.Fatalf("pages = %d", len(pages))
	}
	for i, page := range pages {
		if len(page) == 0 || page[0].text == "" || page[len(page)-1].text == "" {
			t.Fatalf("page %d starts or ends blank: %q", i+1, rowTexts(page))
		}
		if len(page) > pdfPaperLetter.linesPerPage {
			t.Fatalf("page %d overflows: %d rows", i+1, len(page))
		}
		if page[len(page)-1].text == "MAYA" || strings.HasPrefix(page[len(page)-1].text, "INT.") {
			t.Fatalf("page %d strands %q at its foot", i+1, page[len(page)-1].text)
		}
	}
}

func TestFountainLongWordBreaksToTheColumn(t *testing.T) {
	long := strings.Repeat("x", 70)
	pages := paginatedScript("A link: "+long+" ends here.\n", false)
	for _, line := range pages[0] {
		if n := len([]rune(line.text)); n > pdfPaperLetter.columns {
			t.Fatalf("row %q is %d characters, wider than the column", line.text, n)
		}
	}
}

func TestFountainPDFNotesToggle(t *testing.T) {
	script := "# ACT ONE\n\n= The archive opens.\n\nINT. ROOM - DAY\n\nAction.\n"
	elements, titlePage := fountainParse([]byte(script))
	without, err := fountainPDF(pdfPaperLetter, elements, titlePage, false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(without), "ACT ONE") || strings.Contains(string(without), "archive opens") {
		t.Fatal("notes stay out of the page unless asked for")
	}
	with, err := fountainPDF(pdfPaperLetter, elements, titlePage, true)
	if err != nil {
		t.Fatal(err)
	}
	content := string(with)
	if !strings.Contains(content, "(ACT ONE) Tj") || !strings.Contains(content, "(The archive opens.) Tj") {
		t.Fatal("asked-for notes print")
	}
	if !strings.Contains(content, fmt.Sprintf("q %.2f g", pdfNoteGray)) {
		t.Fatal("notes print gray")
	}
}

func TestFountainPDFFacesAndEncoding(t *testing.T) {
	elements, titlePage := fountainParse([]byte("INT. CAFÉ - DAY\n\nJosé *waits*. The “end” — almost…\n"))
	pdf, err := fountainPDF(pdfPaperLetter, elements, titlePage, false)
	if err != nil {
		t.Fatal(err)
	}
	content := string(pdf)
	for _, face := range []string{"Courier", "Courier-Bold", "Courier-Oblique", "Courier-BoldOblique"} {
		if !strings.Contains(content, "/BaseFont /"+face+" /Encoding /WinAnsiEncoding") {
			t.Fatalf("font %s missing or not WinAnsi", face)
		}
	}
	if !strings.Contains(content, `/F2 12 Tf 1 0 0 1 108.00 720.00 Tm (INT. CAF\311 - DAY) Tj`) {
		t.Fatal("the scene heading prints bold and uppercase, its accent as a WinAnsi byte")
	}
	if !strings.Contains(content, "/F3 12 Tf") {
		t.Fatal("emphasis prints in the oblique face")
	}
	if !strings.Contains(content, `(. The "end" -- almost...) Tj`) {
		t.Fatal("typographic punctuation prints in its typewriter form")
	}
}

func TestPDFEscapeWinAnsi(t *testing.T) {
	cases := map[string]string{
		"plain":         "plain",
		"(paren) \\":    `\(paren\) \\`,
		"café":          `caf\351`,
		"“quoted” — it": `"quoted" -- it`,
		"日本":            "??",
		"tab\there":     "tab here",
	}
	for in, want := range cases {
		if got := pdfEscape(in); got != want {
			t.Errorf("pdfEscape(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFountainFDXTitlePageLines(t *testing.T) {
	_, titlePage := fountainParse([]byte("Title: Draft\nContact:\n    Line One\n    Line Two\n\nINT. ROOM\n"))
	fdx, err := fountainFDX(nil, titlePage)
	if err != nil {
		t.Fatal(err)
	}
	var doc fdxDocument
	if err := xml.Unmarshal(fdx, &doc); err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, paragraph := range doc.TitlePage.Paragraphs {
		got = append(got, paragraph.Text)
	}
	if strings.Join(got, "|") != "Draft|Line One|Line Two" {
		t.Fatalf("title paragraphs = %q, want a multi-line value one paragraph per line", got)
	}
}

// paginatedScriptOnPaper is paginatedScript on a chosen sheet.
func paginatedScriptOnPaper(paper pdfPaper, script string, includeNotes bool) [][]fountainPDFLine {
	elements, _ := fountainParse([]byte(script))
	return fountainPaginate(paper, fountainBlocks(paper, elements, includeNotes))
}

// renderedPageCount runs a script through the full PDF pipeline on a sheet
// and returns the page count — the end-to-end check for the paper's effect.
func renderedPageCount(t *testing.T, paper pdfPaper, script string) int {
	t.Helper()
	elements, titlePage := fountainParse([]byte(script))
	pdf, err := fountainPDF(paper, elements, titlePage, false)
	if err != nil {
		t.Fatal(err)
	}
	match := pdfCountRe.FindSubmatch(pdf)
	if match == nil {
		t.Fatalf("no /Count in the rendered PDF")
	}
	var count int
	fmt.Sscanf(string(match[1]), "%d", &count)
	return count
}

var pdfCountRe = regexp.MustCompile(`/Count (\d+)`)

// The same filler script on both sheets: each page fills to its own
// paper's line count — A4's taller text block carries more per page, so
// the geometry reaches the paginator and not just the page box.
func TestFountainA4CarriesMoreLinesPerPage(t *testing.T) {
	script := fillerParagraphs(30) // 59 rows: one page on A4 (59 lines), two on Letter (55)
	if got := renderedPageCount(t, pdfPaperLetter, script); got != 2 {
		t.Fatalf("Letter pages = %d, want 2", got)
	}
	if got := renderedPageCount(t, pdfPaperA4, script); got != 1 {
		t.Fatalf("A4 pages = %d, want 1 — the taller sheet fits all 59 rows", got)
	}
	pages := paginatedScriptOnPaper(pdfPaperA4, script, false)
	if len(pages[0]) != pdfPaperA4.linesPerPage {
		t.Fatalf("A4 page rows = %d, want a full %d-row page", len(pages[0]), pdfPaperA4.linesPerPage)
	}
}

// The A4 page box and grid land in the content: a metric sheet writes
// fractional MediaBox points, its first baseline sits 1" down the taller
// sheet, and no row outgrows the narrower column.
func TestFountainA4Geometry(t *testing.T) {
	script := "INT. ROOM - DAY\n\n" + strings.Repeat("Word ", 30) + "\n"
	elements, titlePage := fountainParse([]byte(script))
	pdf, err := fountainPDF(pdfPaperA4, elements, titlePage, false)
	if err != nil {
		t.Fatal(err)
	}
	content := string(pdf)
	if !strings.Contains(content, "/MediaBox [0 0 595.28 841.89]") {
		t.Fatal("the A4 page box must be 210 × 297 mm in points")
	}
	if !strings.Contains(content, "1 0 0 1 108.00 769.89 Tm (INT. ROOM - DAY) Tj") {
		t.Fatal("the first row must start at the left margin, 1 inch down the taller sheet")
	}
	for _, line := range paginatedScriptOnPaper(pdfPaperA4, script, false)[0] {
		if n := len([]rune(line.text)); n > pdfPaperA4.columns {
			t.Fatalf("row %q is %d characters, wider than the A4 column of %d", line.text, n, pdfPaperA4.columns)
		}
	}
}

// The same long script paginates shorter on A4 — the extra lines a page
// add up, and the breaks land in different places. That trade is what the
// Print block's hint tells the writer.
func TestFountainA4BreaksInDifferentPlaces(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 80; i++ {
		fmt.Fprintf(&b, "INT. ROOM %d - DAY\n\nSomething happens here and it takes a while to describe properly on the page.\n\nMAYA\nAnd then?\n\n", i)
	}
	letter, a4 := renderedPageCount(t, pdfPaperLetter, b.String()), renderedPageCount(t, pdfPaperA4, b.String())
	if a4 >= letter {
		t.Fatalf("A4 pages = %d, Letter = %d — the taller sheet must come out shorter", a4, letter)
	}
}

// The title page keeps its shape on A4: the creative block starts a third
// of the way down the taller sheet and the contact stays pinned to the
// bottom.
func TestFountainA4TitlePage(t *testing.T) {
	script := "Title: The Long Night\nDraft date: November 7, 2026\nContact:\n    Subhash Chandran\n    42 Lantern Lane\n\nINT. ROOM - DAY\n"
	_, titlePage := fountainParse([]byte(script))
	lines := fountainTitlePageLines(pdfPaperA4, titlePage)
	if len(lines) != pdfPaperA4.linesPerPage {
		t.Fatalf("A4 title page rows = %d, want the full %d-row page", len(lines), pdfPaperA4.linesPerPage)
	}
	if lines[pdfPaperA4.linesPerPage-1].text != "42 Lantern Lane" {
		t.Fatalf("bottom row = %q, want the contact pinned to the bottom", lines[pdfPaperA4.linesPerPage-1].text)
	}
	if lines[pdfPaperA4.titleTopRow()].text != "THE LONG NIGHT" {
		t.Fatalf("title row = %q, want it at row %d — a third down the sheet", lines[pdfPaperA4.titleTopRow()].text, pdfPaperA4.titleTopRow())
	}
}

// The paper reaches the export's front door: the request's paperSize picks
// the sheet, empty stays Letter, and a bad name fails naming the choices.
func TestSaveDocumentExportPaperSize(t *testing.T) {
	script := []byte(fillerParagraphs(40))
	for _, tc := range []struct {
		paperSize string
		wantBox   string
	}{
		{"", "/MediaBox [0 0 612 792]"},
		{"letter", "/MediaBox [0 0 612 792]"},
		{"A4", "/MediaBox [0 0 595.28 841.89]"},
	} {
		paper, err := documentPaperFor(tc.paperSize)
		if err != nil {
			t.Fatal(err)
		}
		data, _, _, _, err := renderDocumentExport(script, "/x/script.fountain", "pdf", false, paper)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), tc.wantBox) {
			t.Fatalf("paperSize %q: MediaBox missing %s", tc.paperSize, tc.wantBox)
		}
	}
	if _, err := documentPaperFor("legal"); err == nil || !strings.Contains(err.Error(), `"letter"`) {
		t.Fatalf("documentPaperFor(legal) = %v, want the choices named", err)
	}
}
