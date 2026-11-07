package main

// The document exports: a saved .fountain artifact renders to a print-layout
// PDF or a Final Draft .fdx file. Both ride the save+download philosophy —
// they derive from a SAVED revision (the editor's Export PDF door and the
// Edits panel's per-row actions), never from the editor's uncommitted draft.
//
// The PDF prints the script the way the preview shows it, on the industry
// page: Courier 12pt columns, scene headings bold and uppercase, cues and
// transitions uppercase, lyrics italic, Fountain's *italic* / **bold** /
// _underline_ in Courier's own faces (fountainInlineSpans — the preview's
// reading), and the preview's paragraph rhythm as real blank lines
// (fountainSpaceBefore — one rule for both). What the screen never shows, the
// page adds: `===` starts a new page, a scene heading never strands at a
// page's foot, a cue always keeps dialogue beneath it, and a speech that
// crosses a page breaks — at a sentence's end when one is close — with
// (MORE) below and the cue's (CONT'D) above. Sections and synopses print as
// gray notes when the export asks for them (the preview's view) and stay out
// otherwise (Fountain's convention for formatted output, and a page count
// true to the script).
//
// The PDF writer is hand-rolled on purpose: a screenplay page is about the
// simplest PDF there is — Courier's four standard faces, nothing embedded, no
// images — and this repo keeps its dependency list at wails + keyring.

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// SaveDocumentExportRequest names a saved document artifact and the export
// rendering to write — "pdf" or "fdx". IncludeNotes prints a screenplay's
// sections and synopses as gray notes, the way the preview shows them; left
// off they stay out of the page. PaperSize names the sheet the PDF prints
// on — "letter" (the default) or "a4"; the frontend remembers the last
// choice and starts at DefaultDocumentPaperSize (the region's office paper).
// FDX ignores it: Final Draft paginates with its own page setup.
type SaveDocumentExportRequest struct {
	Path          string `json:"path"`
	Format        string `json:"format"`
	SuggestedName string `json:"suggestedName,omitempty"`
	IncludeNotes  bool   `json:"includeNotes,omitempty"`
	PaperSize     string `json:"paperSize,omitempty"`
}

// SaveDocumentExport renders a saved document artifact to the requested
// format and opens the save dialog — the SaveMarkdown twin for the derived
// exports. The artifact is parsed fresh from disk (fountainParse accepts any
// text, so an .md saved over as .fountain exports too), never regenerated
// from editor state.
func (a *App) SaveDocumentExport(req SaveDocumentExportRequest) (string, error) {
	format := strings.ToLower(strings.TrimSpace(req.Format))
	if format != "pdf" && format != "fdx" {
		return "", fmt.Errorf(`document export format must be "pdf" or "fdx", got %q`, req.Format)
	}
	paper, err := documentPaperFor(req.PaperSize)
	if err != nil {
		return "", err
	}
	sourcePath := strings.TrimSpace(req.Path)
	sourcePath = strings.TrimPrefix(sourcePath, artifactPrefix)
	if sourcePath == "" {
		return "", errors.New("document path is empty")
	}
	data, err := os.ReadFile(sourcePath)
	if err != nil {
		return "", err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return "", errors.New("document artifact is empty")
	}
	rendered, extension, filterName, pattern, err := renderDocumentExport(data, sourcePath, format, req.IncludeNotes, paper)
	if err != nil {
		return "", err
	}
	return a.saveArtifactDialog(rendered, req.SuggestedName, "atelier-screenplay", extension, "Export document", []runtime.FileFilter{
		{DisplayName: filterName, Pattern: pattern},
		{DisplayName: "All Files", Pattern: "*.*"},
	})
}

// renderDocumentExport is the save dialog's testable half: the artifact's own
// extension picks the layout — .fountain renders as a screenplay (PDF) or
// exports to Final Draft; everything else renders as a document, the markdown
// print layout. FDX has no meaning for a markdown document, so it refuses
// with the fix. includeNotes reaches only the screenplay PDF; the paper, both
// PDFs (Final Draft paginates an .fdx with its own page setup).
func renderDocumentExport(data []byte, sourcePath, format string, includeNotes bool, paper pdfPaper) ([]byte, string, string, string, error) {
	isFountain := strings.EqualFold(filepath.Ext(sourcePath), ".fountain")
	switch format {
	case "pdf":
		if isFountain {
			elements, titlePage := fountainParse(data)
			rendered, err := fountainPDF(paper, elements, titlePage, includeNotes)
			return rendered, ".pdf", "PDF Files", "*.pdf", err
		}
		rendered, err := markdownPDF(paper, parseMarkdownBlocks(data))
		return rendered, ".pdf", "PDF Files", "*.pdf", err
	default:
		if !isFountain {
			return nil, "", "", "", errors.New("FDX export renders Fountain screenplays — save the document as .fountain first")
		}
		elements, titlePage := fountainParse(data)
		rendered, err := fountainFDX(elements, titlePage)
		return rendered, ".fdx", "Final Draft Files", "*.fdx", err
	}
}

// --- PDF ---------------------------------------------------------------

// The page grid every paper shares: Courier 12pt (10 characters per inch, 6
// lines per inch) behind a 1.5" left margin. The paper (pdfPaper) sets the
// rest — the page box, the full-width column, the line count. Element
// indents follow the convention measured from the text edge, in characters.
const (
	pdfMarginLeft      = 108.0
	pdfLineHeight      = 12.0
	pdfCharWidth       = 7.2
	pdfNoteGray        = 0.45 // a printed note's fill: legible, plainly not script
	pdfUnderlineDrop   = 1.5  // Courier's underline position below the baseline
	pdfUnderlineWidth  = 0.6  // and its thickness
	fountainIndentChar = 22   // character cue
	fountainIndentPar  = 16   // parenthetical
	fountainIndentDia  = 10   // dialogue
	fountainWidthCue   = 35
	fountainWidthParen = 30
	fountainWidthDia   = 35
	// A break inside a flow leaves at least this many of its rows on each
	// page — the widow/orphan rule, and for a speech "a cue never ends a page
	// without its dialogue".
	fountainMinSplitRows = 2
	// A break at a sentence's end wins over a fuller mid-sentence one when
	// it gives up no more than this many rows of the page.
	fountainSentenceBreakSlack = 5
)

// pdfSpan is one styled run of a printed row: Courier's four faces plus an
// underline rule.
type pdfSpan struct {
	text      string
	bold      bool
	italic    bool
	underline bool
}

// pdfFont names the run's face among the page's font resources.
func (s pdfSpan) pdfFont() string {
	switch {
	case s.bold && s.italic:
		return "/F4"
	case s.bold:
		return "/F2"
	case s.italic:
		return "/F3"
	}
	return "/F1"
}

func (s pdfSpan) sameFace(other pdfSpan) bool {
	return s.bold == other.bold && s.italic == other.italic && s.underline == other.underline
}

// fountainPDFLine is one rendered row before pagination: text at a column,
// with its alignment. A screenplay row carries styled spans (text is then
// their concatenation — the row's measure) and gray marks a printed note; a
// row without spans prints its text in one face.
type fountainPDFLine struct {
	text   string
	indent int
	center bool
	right  bool
	spans  []pdfSpan
	gray   bool
	// font and size carry the markdown layout's typographic steps (bold
	// headings at larger sizes); zero values mean the screenplay defaults —
	// regular Courier at 12pt. Pagination stays on the fixed 12pt grid for
	// both dialects: larger glyphs draw on their own grid slot, with blank
	// lines the layout inserts around headings keeping them clear.
	font string
	size float64
}

// pdfFont returns the line's font resource name, defaulting to regular Courier.
func (l fountainPDFLine) pdfFont() string {
	if l.font != "" {
		return l.font
	}
	return "/F1"
}

// pdfSize returns the line's point size, defaulting to 12.
func (l fountainPDFLine) pdfSize() float64 {
	if l.size > 0 {
		return l.size
	}
	return 12
}

// pdfCharWidthAt is the Courier advance width at a size: 0.6em.
func pdfCharWidthAt(size float64) float64 {
	return size * 0.6
}

// fountainWrapText breaks a line at word boundaries to the character width —
// the markdown layout's wrapper (the screenplay wraps styled words, see
// fountainWrapWords). Courier makes columns arithmetic, no font metrics
// needed. A word longer than the width stands alone on its line (it
// overflows the column rather than being split).
func fountainWrapText(text string, width int) []string {
	words := strings.Fields(text)
	if len(words) == 0 {
		return []string{""}
	}
	lines := []string{}
	current := words[0]
	for _, word := range words[1:] {
		if utf8.RuneCountInString(current)+1+utf8.RuneCountInString(word) <= width {
			current += " " + word
			continue
		}
		lines = append(lines, current)
		current = word
	}
	return append(lines, current)
}

// pdfPaginate breaks laid-out rows into pages on the paper's fixed line grid,
// honoring a per-block keep-with-next room requirement — the markdown
// layout's widow rule (a heading needs a body line below it). The screenplay
// has its own page flow (fountainPaginate).
func pdfPaginate(paper pdfPaper, blocks [][]fountainPDFLine, keepRoom func([]fountainPDFLine) int) [][]fountainPDFLine {
	pages := [][]fountainPDFLine{}
	current := []fountainPDFLine{}
	lineAt := 0
	newPage := func() {
		pages = append(pages, current)
		current = []fountainPDFLine{}
		lineAt = 0
	}
	for _, blk := range blocks {
		if lineAt == paper.linesPerPage {
			newPage()
		}
		needsRoom := len(blk)
		if keepRoom != nil {
			needsRoom = keepRoom(blk)
		}
		if lineAt+needsRoom > paper.linesPerPage {
			newPage()
		}
		current = append(current, blk...)
		lineAt += len(blk)
	}
	if len(current) > 0 || len(pages) == 0 {
		pages = append(pages, current)
	}
	return pages
}

// pdfWrite assembles the PDF file — objects, xref, trailer, %%EOF — from
// laid-out pages on the paper's page box. A title page, when present, is page
// one and stands outside the numbering: the script's own pages count from the
// next one, and the number prints from the second of them ("2." top-right),
// the industry habit.
func pdfWrite(paper pdfPaper, pages [][]fountainPDFLine, titlePage bool) []byte {
	w := &pdfBuf{}
	w.writeString("%PDF-1.4\n")
	type objRef struct {
		id   int
		head int
	}
	objects := []objRef{}
	record := func(id int) { objects = append(objects, objRef{id: id, head: w.len()}) }
	writeObj := func(id int, body string) {
		record(id)
		fmt.Fprintf(w.raw(), "%d 0 obj\n%s\nendobj\n", id, body)
	}
	// Courier's four faces, F1-F4 (pdfSpan.pdfFont), in WinAnsi — the
	// latin-1 repertoire pdfEscape writes.
	faces := []string{"Courier", "Courier-Bold", "Courier-Oblique", "Courier-BoldOblique"}
	firstPage := 3 + len(faces)
	kids := []string{}
	for i := range pages {
		kids = append(kids, fmt.Sprintf("%d 0 R", firstPage+2*i))
	}
	writeObj(1, "<< /Type /Catalog /Pages 2 0 R >>")
	writeObj(2, fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), len(pages)))
	fontRefs := []string{}
	for i, face := range faces {
		writeObj(3+i, fmt.Sprintf("<< /Type /Font /Subtype /Type1 /BaseFont /%s /Encoding /WinAnsiEncoding >>", face))
		fontRefs = append(fontRefs, fmt.Sprintf("/F%d %d 0 R", i+1, 3+i))
	}
	scriptPage := 0
	// The page box in points, written as briefly as it reads: "612 792" for
	// Letter, the fractional points a metric sheet needs where it has them.
	mediaBox := strconv.FormatFloat(paper.width, 'f', -1, 64) + " " + strconv.FormatFloat(paper.height, 'f', -1, 64)
	for i, pageLines := range pages {
		number := 0
		if !titlePage || i > 0 {
			scriptPage++
			number = scriptPage
		}
		pageID, contentID := firstPage+2*i, firstPage+1+2*i
		stream := fountainPageStream(paper, pageLines, number)

		writeObj(pageID, fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %s] /Resources << /Font << %s >> >> /Contents %d 0 R >>", mediaBox, strings.Join(fontRefs, " "), contentID))
		record(contentID)
		fmt.Fprintf(w.raw(), "%d 0 obj\n<< /Length %d >>\nstream\n%s\nendstream\nendobj\n", contentID, len(stream), stream)
	}

	xrefHead := w.len()
	fmt.Fprintf(w.raw(), "xref\n0 %d\n", len(objects)+1)
	w.writeString("0000000000 65535 f \n")
	for _, object := range objects {
		fmt.Fprintf(w.raw(), "%010d 00000 n \n", object.head)
	}
	fmt.Fprintf(w.raw(), "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, xrefHead)
	return w.bytes()
}

// fountainPDF renders the script on the paper: the title page when the
// script opens with one, then the body grouped into flows and paginated with
// the screenplay page-flow rules, then the shared object assembly.
func fountainPDF(paper pdfPaper, elements []FountainElement, titlePage map[string]string, includeNotes bool) ([]byte, error) {
	pages := [][]fountainPDFLine{}
	hasTitle := len(titlePage) > 0
	if hasTitle {
		pages = append(pages, fountainTitlePageLines(paper, titlePage))
	}
	body := fountainPaginate(paper, fountainBlocks(paper, elements, includeNotes))
	if hasTitle && len(body) == 1 && len(body[0]) == 0 {
		// A title page and nothing after it: no blank script page.
		body = nil
	}
	pages = append(pages, body...)
	return pdfWrite(paper, pages, hasTitle), nil
}

// fountainPageStream renders one page's content stream: each row's runs as
// BT/Tj/ET blocks at absolute positions in their own faces, underline rules
// under underlined runs, notes in gray, and the page number top-right on
// script pages from the second (number 0 — a title page — prints none).
func fountainPageStream(paper pdfPaper, lines []fountainPDFLine, number int) string {
	var b bytes.Buffer
	for row, line := range lines {
		if strings.TrimSpace(line.text) == "" {
			continue
		}
		size := line.pdfSize()
		charWidth := pdfCharWidthAt(size)
		width := float64(utf8.RuneCountInString(line.text)) * charWidth
		var x float64
		switch {
		case line.center:
			x = pdfMarginLeft + (paper.textRight-pdfMarginLeft-width)/2
		case line.right:
			x = paper.textRight - width
		default:
			x = pdfMarginLeft + float64(line.indent)*charWidth
		}
		y := paper.topBaseline - float64(row)*pdfLineHeight
		if line.gray {
			fmt.Fprintf(&b, "q %.2f g %.2f G\n", pdfNoteGray, pdfNoteGray)
		}
		if len(line.spans) == 0 {
			fmt.Fprintf(&b, "BT %s %.0f Tf 1 0 0 1 %.2f %.2f Tm (%s) Tj ET\n", line.pdfFont(), size, x, y, pdfEscape(line.text))
		}
		for _, span := range line.spans {
			spanWidth := float64(utf8.RuneCountInString(span.text)) * charWidth
			if strings.TrimSpace(span.text) != "" {
				fmt.Fprintf(&b, "BT %s %.0f Tf 1 0 0 1 %.2f %.2f Tm (%s) Tj ET\n", span.pdfFont(), size, x, y, pdfEscape(span.text))
			}
			if span.underline {
				fmt.Fprintf(&b, "%.1f w %.2f %.2f m %.2f %.2f l S\n", pdfUnderlineWidth, x, y-pdfUnderlineDrop, x+spanWidth, y-pdfUnderlineDrop)
			}
			x += spanWidth
		}
		if line.gray {
			b.WriteString("Q\n")
		}
	}
	if number >= 2 {
		label := fmt.Sprintf("%d.", number)
		x := paper.textRight - float64(len(label))*pdfCharWidth
		fmt.Fprintf(&b, "BT /F1 12 Tf 1 0 0 1 %.2f %.2f Tm (%s) Tj ET\n", x, paper.topBaseline+pdfLineHeight, pdfEscape(label))
	}
	return b.String()
}

// --- Screenplay layout -------------------------------------------------

// fountainPrintStyle is how one element kind prints: its column (indent and
// width in characters from the text edge), its alignment, and the case and
// face the preview gives it.
type fountainPrintStyle struct {
	indent int
	width  int
	center bool
	right  bool
	upper  bool
	bold   bool
	italic bool
	gray   bool
}

// fountainPrintStyleFor maps an element kind onto its column and face — the
// industry indents the preview draws, its uppercase scene headings, cues
// and transitions, its bold sluglines and italic lyrics, its gray notes.
// columns is the paper's full-width column; the speech columns are fixed.
func fountainPrintStyleFor(kind string, columns int) fountainPrintStyle {
	switch kind {
	case fountainScene:
		return fountainPrintStyle{width: columns, upper: true, bold: true}
	case fountainCharacter:
		return fountainPrintStyle{indent: fountainIndentChar, width: fountainWidthCue, upper: true}
	case fountainParenthetical:
		return fountainPrintStyle{indent: fountainIndentPar, width: fountainWidthParen}
	case fountainDialogue:
		return fountainPrintStyle{indent: fountainIndentDia, width: fountainWidthDia}
	case fountainTransition:
		return fountainPrintStyle{width: columns, right: true, upper: true}
	case fountainCentered:
		return fountainPrintStyle{width: columns, center: true}
	case fountainLyric:
		return fountainPrintStyle{indent: fountainIndentChar, width: columns - fountainIndentChar, italic: true}
	case fountainSection:
		return fountainPrintStyle{width: columns, bold: true, italic: true, gray: true}
	case fountainSynopsis:
		return fountainPrintStyle{width: columns, italic: true, gray: true}
	}
	return fountainPrintStyle{width: columns}
}

// fountainWord is one printable word: its styled runs (a word can change
// face mid-way — un*believ*able) and its width in characters, plus whether
// the space after it is underlined, so an underlined phrase prints one
// unbroken rule.
type fountainWord struct {
	spans          []pdfSpan
	width          int
	underlineAfter bool
}

func (w fountainWord) plain() string {
	var b strings.Builder
	for _, span := range w.spans {
		b.WriteString(span.text)
	}
	return b.String()
}

// fountainWordsFor turns element text into printable words: inline emphasis
// parsed the preview's way (fountainInlineSpans), the kind's case and face
// applied, and every run normalized to the Courier repertoire before it is
// measured, so widths count the glyphs the page carries.
func fountainWordsFor(text string, style fountainPrintStyle) []fountainWord {
	words := []fountainWord{}
	var current *fountainWord
	for _, span := range fountainInlineSpans(text) {
		face := pdfSpan{bold: span.Bold || style.bold, italic: span.Italic || style.italic, underline: span.Underline}
		chunk := span.Text
		if style.upper {
			chunk = strings.ToUpper(chunk)
		}
		for _, r := range pdfPrintable(chunk) {
			if unicode.IsSpace(r) {
				if current != nil {
					words = append(words, *current)
					current = nil
				}
				if n := len(words); n > 0 && face.underline {
					words[n-1].underlineAfter = true
				}
				continue
			}
			if current == nil {
				current = &fountainWord{}
			}
			current.width++
			if n := len(current.spans); n > 0 && current.spans[n-1].sameFace(face) {
				current.spans[n-1].text += string(r)
				continue
			}
			run := face
			run.text = string(r)
			current.spans = append(current.spans, run)
		}
	}
	if current != nil {
		words = append(words, *current)
	}
	return words
}

// fountainBreakLongWords hard-breaks any word wider than the column into
// column-wide pieces — a long URL would otherwise run off the page.
func fountainBreakLongWords(words []fountainWord, width int) []fountainWord {
	if width <= 0 {
		return words
	}
	out := make([]fountainWord, 0, len(words))
	for _, word := range words {
		for word.width > width {
			head, tail := word.cut(width)
			out = append(out, head)
			word = tail
		}
		out = append(out, word)
	}
	return out
}

// cut splits a word after at characters, its runs split where they straddle.
func (w fountainWord) cut(at int) (fountainWord, fountainWord) {
	head := fountainWord{width: at}
	tail := fountainWord{width: w.width - at, underlineAfter: w.underlineAfter}
	taken := 0
	for _, span := range w.spans {
		runes := []rune(span.text)
		switch {
		case taken >= at:
			tail.spans = append(tail.spans, span)
		case taken+len(runes) <= at:
			head.spans = append(head.spans, span)
		default:
			first, rest := span, span
			first.text = string(runes[:at-taken])
			rest.text = string(runes[at-taken:])
			head.spans = append(head.spans, first)
			tail.spans = append(tail.spans, rest)
		}
		taken += len(runes)
	}
	return head, tail
}

// fountainWrapWords breaks words into rows at the column width — greedy, one
// space between words (fountainBreakLongWords already fit the wide ones).
func fountainWrapWords(words []fountainWord, width int) [][]fountainWord {
	rows := [][]fountainWord{}
	row := []fountainWord{}
	used := 0
	for _, word := range words {
		if len(row) > 0 && used+1+word.width > width {
			rows = append(rows, row)
			row = []fountainWord{}
			used = 0
		}
		if len(row) > 0 {
			used++
		}
		row = append(row, word)
		used += word.width
	}
	if len(row) > 0 {
		rows = append(rows, row)
	}
	return rows
}

// fountainRowLine lays one wrapped row out as a printed line: the words'
// runs joined by single spaces (underlined where the phrase is), adjacent
// runs of one face merged.
func fountainRowLine(row []fountainWord, style fountainPrintStyle) fountainPDFLine {
	line := fountainPDFLine{indent: style.indent, center: style.center, right: style.right, gray: style.gray}
	var text strings.Builder
	add := func(span pdfSpan) {
		text.WriteString(span.text)
		if n := len(line.spans); n > 0 && line.spans[n-1].sameFace(span) {
			line.spans[n-1].text += span.text
			return
		}
		line.spans = append(line.spans, span)
	}
	for i, word := range row {
		if i > 0 {
			previous := row[i-1]
			space := previous.spans[len(previous.spans)-1]
			space.text = " "
			space.underline = previous.underlineAfter
			add(space)
		}
		for _, span := range word.spans {
			add(span)
		}
	}
	line.text = text.String()
	return line
}

// fountainEndsSentence reports a word that closes a sentence — a final . ! ?
// or a "--" cut-off, closing quotes and brackets allowed after it; an
// honorific's period is not an end.
func fountainEndsSentence(word fountainWord) bool {
	text := strings.TrimRight(word.plain(), "\"')]")
	switch strings.ToUpper(text) {
	case "MR.", "MRS.", "MS.", "DR.", "ST.", "JR.", "SR.", "VS.", "PROF.":
		return false
	}
	return strings.HasSuffix(text, ".") || strings.HasSuffix(text, "!") || strings.HasSuffix(text, "?") || strings.HasSuffix(text, "--")
}

// fountainPart is one element inside a flow, kept as printable words.
type fountainPart struct {
	kind  string
	style fountainPrintStyle
	words []fountainWord
}

func fountainPartFor(element FountainElement, columns int) fountainPart {
	style := fountainPrintStyleFor(element.Kind, columns)
	return fountainPart{
		kind:  element.Kind,
		style: style,
		words: fountainBreakLongWords(fountainWordsFor(element.Text, style), style.width),
	}
}

func (p fountainPart) rows() []fountainPDFLine {
	wrapped := fountainWrapWords(p.words, p.style.width)
	lines := make([]fountainPDFLine, len(wrapped))
	for i, row := range wrapped {
		lines[i] = fountainRowLine(row, p.style)
	}
	return lines
}

// fountainFlow is one block of the printed script: a scene heading, an
// action paragraph (consecutive action lines), a speech (a cue plus its
// parentheticals, dialogue and lyrics), a run of lyric or centered lines, a
// transition, or a note. It stays as words rather than rows so a page break
// can land inside it and re-wrap both halves.
type fountainFlow struct {
	kind  string        // the lead element's kind
	cue   *fountainPart // a speech's character cue
	parts []fountainPart
}

func (f *fountainFlow) render() []fountainPDFLine {
	lines := []fountainPDFLine{}
	if f.cue != nil {
		lines = append(lines, f.cue.rows()...)
	}
	for _, part := range f.parts {
		lines = append(lines, part.rows()...)
	}
	return lines
}

// fountainSplit is one way to break a flow across a page: the rows that end
// this page — a speech's closing with (MORE) — and the flow that opens the
// next, a speech reopening under its (CONT'D) cue.
type fountainSplit struct {
	head     []fountainPDFLine
	tail     *fountainFlow
	sentence bool // the break falls at a sentence's end or between elements
}

// splits lists every legal break inside the flow: between two of its
// elements (never right after a parenthetical — it belongs to the speech
// below it), at each wrapped row's end, and at each sentence's end inside an
// element (both halves re-wrapped). A legal break leaves at least
// fountainMinSplitRows of the flow's content on each page. Scene headings
// and transitions print whole.
func (f *fountainFlow) splits() []fountainSplit {
	if f.kind == fountainScene || f.kind == fountainTransition {
		return nil
	}
	splits := []fountainSplit{}
	add := func(p, w int, sentence bool) {
		headParts := append([]fountainPart{}, f.parts[:p]...)
		tailParts := []fountainPart{}
		if w > 0 {
			head, tail := f.parts[p], f.parts[p]
			head.words = f.parts[p].words[:w]
			tail.words = f.parts[p].words[w:]
			headParts = append(headParts, head)
			tailParts = append(tailParts, tail)
		} else {
			tailParts = append(tailParts, f.parts[p])
		}
		tailParts = append(tailParts, f.parts[p+1:]...)
		headContent := (&fountainFlow{kind: f.kind, parts: headParts}).render()
		tail := &fountainFlow{kind: f.kind, parts: tailParts}
		if len(headContent) < fountainMinSplitRows || len(tail.render()) < fountainMinSplitRows {
			return
		}
		head := []fountainPDFLine{}
		if f.cue != nil {
			head = append(head, f.cue.rows()...)
		}
		head = append(head, headContent...)
		if f.cue != nil {
			head = append(head, fountainPDFLine{text: "(MORE)", indent: fountainIndentChar})
			continued := fountainContinuedCue(*f.cue)
			tail.cue = &continued
		}
		splits = append(splits, fountainSplit{head: head, tail: tail, sentence: sentence})
	}
	for p, part := range f.parts {
		if p > 0 && !(f.cue != nil && f.parts[p-1].kind == fountainParenthetical) {
			add(p, 0, true)
		}
		if part.kind == fountainParenthetical {
			continue
		}
		rowEnds := map[int]bool{}
		at := 0
		wrapped := fountainWrapWords(part.words, part.style.width)
		for r := 0; r < len(wrapped)-1; r++ {
			at += len(wrapped[r])
			rowEnds[at] = true
			add(p, at, fountainEndsSentence(part.words[at-1]))
		}
		for w := 1; w < len(part.words); w++ {
			if !rowEnds[w] && fountainEndsSentence(part.words[w-1]) {
				add(p, w, true)
			}
		}
	}
	return splits
}

// split picks the break for a page with room rows left: the fullest legal
// head that fits, except that a sentence-end break wins when it gives up no
// more than fountainSentenceBreakSlack rows to the fullest one.
func (f *fountainFlow) split(room int) (fountainSplit, bool) {
	splits := f.splits()
	fullest, sentence := -1, -1
	for i, s := range splits {
		if len(s.head) > room {
			continue
		}
		if fullest < 0 || len(s.head) > len(splits[fullest].head) {
			fullest = i
		}
		if s.sentence && (sentence < 0 || len(s.head) > len(splits[sentence].head)) {
			sentence = i
		}
	}
	if fullest < 0 {
		return fountainSplit{}, false
	}
	if sentence >= 0 && len(splits[fullest].head)-len(splits[sentence].head) <= fountainSentenceBreakSlack {
		return splits[sentence], true
	}
	return splits[fullest], true
}

// minLead is the fewest rows the flow needs at a page's foot to start
// there: its shortest legal head, or the whole flow when nothing splits.
func (f *fountainFlow) minLead() int {
	least := len(f.render())
	for _, s := range f.splits() {
		if len(s.head) < least {
			least = len(s.head)
		}
	}
	return least
}

// fountainContinuedCue is the cue a speech reopens under on the next page:
// its own extensions kept, (CONT'D) added once.
func fountainContinuedCue(cue fountainPart) fountainPart {
	plain := []string{}
	for _, word := range cue.words {
		plain = append(plain, word.plain())
	}
	if strings.Contains(strings.ToUpper(strings.Join(plain, " ")), "(CONT'D)") {
		return cue
	}
	continued := cue
	continued.words = append(append([]fountainWord{}, cue.words...), fountainWordsFor("(CONT'D)", cue.style)...)
	return continued
}

// fountainBlock is one unit of the paginated script: a flow with the blank
// lines that open it, or a forced page break.
type fountainBlock struct {
	spaceBefore  int
	keepWithNext bool // a scene heading or a note: never the last thing on a page
	pageBreak    bool
	flow         *fountainFlow
}

// fountainBlocks groups the parsed script into flows: an element and the
// ones the spacing rule keeps on its following lines (fountainSpaceBefore is
// zero) print as one block — an action paragraph, a speech, a run of lyric or
// centered lines. Notes print only when asked for, and spacing then measures
// from the previous printed element. Words wrap to the paper's columns.
func fountainBlocks(paper pdfPaper, elements []FountainElement, includeNotes bool) []fountainBlock {
	blocks := []fountainBlock{}
	var prev *FountainElement
	for i := 0; i < len(elements); i++ {
		element := elements[i]
		switch element.Kind {
		case fountainSection, fountainSynopsis:
			if !includeNotes {
				continue
			}
		case fountainPageBreak:
			blocks = append(blocks, fountainBlock{pageBreak: true})
			prev = &elements[i]
			continue
		}
		block := fountainBlock{
			spaceBefore:  fountainSpaceBefore(prev, element),
			keepWithNext: element.Kind == fountainScene || element.Kind == fountainSection || element.Kind == fountainSynopsis,
			flow:         &fountainFlow{kind: element.Kind},
		}
		part := fountainPartFor(element, paper.columns)
		if element.Kind == fountainCharacter {
			block.flow.cue = &part
		} else {
			block.flow.parts = append(block.flow.parts, part)
		}
		for i+1 < len(elements) && elements[i+1].Kind != fountainPageBreak && fountainSpaceBefore(&elements[i], elements[i+1]) == 0 {
			i++
			block.flow.parts = append(block.flow.parts, fountainPartFor(elements[i], paper.columns))
		}
		blocks = append(blocks, block)
		prev = &elements[i]
	}
	return blocks
}

// fountainLeadRoom is what must fit at a page's foot for the block at index
// j to start there: its blank lines plus its shortest legal head — or, for
// a block that keeps with its own next, all of it and that next's lead.
func fountainLeadRoom(blocks []fountainBlock, j int) int {
	if j >= len(blocks) || blocks[j].pageBreak || blocks[j].flow == nil {
		return 0
	}
	next := blocks[j]
	if next.keepWithNext {
		return next.spaceBefore + len(next.flow.render()) + fountainLeadRoom(blocks, j+1)
	}
	return next.spaceBefore + next.flow.minLead()
}

// fountainPaginate lays the blocks onto the paper's pages: blank lines open a
// block except at a page's top, a forced break starts a new page, a scene
// heading (or note) moves on when what follows it can't start beneath it,
// and a block that doesn't fit splits at its best legal break — or moves to
// the next page whole when it can't split. Only a block taller than a whole
// page with no legal break is cut where the page ends.
func fountainPaginate(paper pdfPaper, blocks []fountainBlock) [][]fountainPDFLine {
	pages := [][]fountainPDFLine{}
	page := []fountainPDFLine{}
	flush := func() {
		pages = append(pages, page)
		page = []fountainPDFLine{}
	}
	place := func(gap int, rows []fountainPDFLine) {
		if len(page) > 0 {
			for k := 0; k < gap; k++ {
				page = append(page, fountainPDFLine{})
			}
		}
		page = append(page, rows...)
	}
	for i, block := range blocks {
		if block.pageBreak {
			if len(page) > 0 {
				flush()
			}
			continue
		}
		flow, spaceBefore := block.flow, block.spaceBefore
		for flow != nil {
			rows := flow.render()
			if len(rows) == 0 {
				break
			}
			gap := spaceBefore
			if len(page) == 0 {
				gap = 0
			}
			room := paper.linesPerPage - len(page) - gap
			if len(rows) <= room {
				if block.keepWithNext && len(page) > 0 && len(rows)+fountainLeadRoom(blocks, i+1) > room {
					flush()
					continue
				}
				place(gap, rows)
				break
			}
			if s, ok := flow.split(room); ok {
				place(gap, s.head)
				flush()
				flow, spaceBefore = s.tail, 0
				continue
			}
			if len(page) > 0 {
				flush()
				continue
			}
			for len(rows) > paper.linesPerPage {
				page = append(page, rows[:paper.linesPerPage]...)
				flush()
				rows = rows[paper.linesPerPage:]
			}
			place(0, rows)
			break
		}
	}
	if len(page) > 0 || len(pages) == 0 {
		pages = append(pages, page)
	}
	return pages
}

// fountainTitlePageLines lays out the title page the way the preview orders
// it: the title (bold, underlined, uppercase), credit, author(s), source and
// draft date centered from a third of the way down, each value keeping its
// own line breaks; then the print-only keys — the contact block, copyright,
// notes — pinned to the bottom-left.
func fountainTitlePageLines(paper pdfPaper, titlePage map[string]string) []fountainPDFLine {
	topRow := paper.titleTopRow()
	lines := make([]fountainPDFLine, topRow)
	valueRows := func(value string, style fountainPrintStyle, underline bool) []fountainPDFLine {
		rows := []fountainPDFLine{}
		for _, valueLine := range strings.Split(value, "\n") {
			words := fountainBreakLongWords(fountainWordsFor(valueLine, style), style.width)
			if underline {
				for w := range words {
					words[w].underlineAfter = true
					for s := range words[w].spans {
						words[w].spans[s].underline = true
					}
				}
			}
			for _, row := range fountainWrapWords(words, style.width) {
				rows = append(rows, fountainRowLine(row, style))
			}
		}
		return rows
	}
	for _, key := range []string{"Title", "Credit", "Author", "Authors", "Source", "Draft date"} {
		value := strings.TrimSpace(titlePage[key])
		if value == "" {
			continue
		}
		if len(lines) > topRow {
			lines = append(lines, fountainPDFLine{})
		}
		style := fountainPrintStyle{width: paper.columns, center: true}
		if key == "Title" {
			style.upper, style.bold = true, true
		}
		lines = append(lines, valueRows(value, style, key == "Title")...)
	}
	bottom := []fountainPDFLine{}
	for _, key := range []string{"Contact", "Copyright", "Notes"} {
		value := strings.TrimSpace(titlePage[key])
		if value == "" {
			continue
		}
		if len(bottom) > 0 {
			bottom = append(bottom, fountainPDFLine{})
		}
		bottom = append(bottom, valueRows(value, fountainPrintStyle{width: paper.columns}, false)...)
	}
	if len(bottom) > 0 {
		start := paper.linesPerPage - len(bottom)
		if start < len(lines)+1 {
			start = len(lines) + 1
		}
		for len(lines) < start {
			lines = append(lines, fountainPDFLine{})
		}
		lines = append(lines, bottom...)
	}
	if len(lines) > paper.linesPerPage {
		lines = lines[:paper.linesPerPage]
	}
	return lines
}

// pdfBuf tracks object byte offsets for the xref table.
type pdfBuf struct {
	buf bytes.Buffer
}

func (w *pdfBuf) writeString(s string) { w.buf.WriteString(s) }
func (w *pdfBuf) raw() *bytes.Buffer   { return &w.buf }
func (w *pdfBuf) len() int             { return w.buf.Len() }
func (w *pdfBuf) bytes() []byte        { return w.buf.Bytes() }

// pdfTypography maps the typographic punctuation an editor types onto
// Courier's typewriter forms — straight quotes, "--" for the dash — and tabs
// onto spaces.
var pdfTypography = strings.NewReplacer("’", "'", "‘", "'", "“", "\"", "”", "\"", "—", "--", "–", "-", "…", "...", "\t", " ")

// pdfPrintable normalizes text to what the WinAnsi-encoded Courier faces
// print: typographic punctuation in its typewriter form, latin-1 kept, and
// everything else — C1 controls, characters past latin-1 — substituted with
// "?" (the preview is where full Unicode lives). Layout measures the result,
// so a row's width is exactly the glyphs it carries.
func pdfPrintable(text string) string {
	text = pdfTypography.Replace(text)
	var b strings.Builder
	for _, r := range text {
		if r == 0x7f || (r >= 0x80 && r < 0xa0) || r > 0xff {
			b.WriteByte('?')
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// pdfEscape encodes text as a PDF literal string in WinAnsi: printable ASCII
// as itself (backslash and parentheses escaped), the latin-1 upper half as
// octal byte escapes — the content stream stays ASCII — and control
// characters as spaces.
func pdfEscape(text string) string {
	var b strings.Builder
	for _, r := range pdfPrintable(text) {
		switch {
		case r == '\\' || r == '(' || r == ')':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r < 0x20:
			b.WriteByte(' ')
		case r < 0x7f:
			b.WriteRune(r)
		default:
			fmt.Fprintf(&b, "\\%03o", r)
		}
	}
	return b.String()
}

// --- Final Draft -------------------------------------------------------

// The .fdx shape: Final Draft's XML with its paragraph type vocabulary —
// which fountainElementKind already speaks verbatim.
type fdxDocument struct {
	XMLName      xml.Name     `xml:"FinalDraft"`
	DocumentType string       `xml:"DocumentType,attr"`
	Template     string       `xml:"Template,attr"`
	Version      string       `xml:"Version,attr"`
	Content      fdxContent   `xml:"Content"`
	TitlePage    fdxTitlePage `xml:"TitlePage"`
}

type fdxContent struct {
	Paragraphs []fdxParagraph `xml:"Paragraph"`
}

type fdxParagraph struct {
	Type string `xml:"Type,attr"`
	Text string `xml:"Text"`
}

type fdxTitlePage struct {
	Paragraphs []fdxTitleParagraph `xml:"Paragraph"`
}

type fdxTitleParagraph struct {
	Alignment string `xml:"Alignment,attr"`
	Text      string `xml:"Text"`
}

func fountainFDX(elements []FountainElement, titlePage map[string]string) ([]byte, error) {
	doc := fdxDocument{DocumentType: "Script", Template: "No", Version: "1"}
	for _, element := range elements {
		switch element.Kind {
		case fountainSection, fountainSynopsis, fountainPageBreak:
			// Never part of the script body Final Draft shows.
			continue
		default:
			doc.Content.Paragraphs = append(doc.Content.Paragraphs, fdxParagraph{Type: element.Kind, Text: element.Text})
		}
	}
	for _, key := range []string{"Title", "Credit", "Author", "Authors", "Source", "Draft date", "Contact", "Copyright"} {
		// A multi-line value (a contact block) is one paragraph per line.
		for _, line := range strings.Split(titlePage[key], "\n") {
			if line = strings.TrimSpace(line); line != "" {
				doc.TitlePage.Paragraphs = append(doc.TitlePage.Paragraphs, fdxTitleParagraph{Alignment: "Center", Text: line})
			}
		}
	}
	rendered, err := xml.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	return append([]byte(xml.Header), append(rendered, '\n')...), nil
}
