package main

// The document exports: a saved .fountain artifact renders to a
// production-layout PDF or a Final Draft .fdx file. Both ride the
// save+download philosophy — they derive from a SAVED revision (the Edits
// panel's per-row actions), never from the editor's uncommitted draft. The
// PDF writer is hand-rolled on purpose: a screenplay page is the simplest
// PDF that exists — Courier text, one font family, no images — and this repo
// keeps its dependency list at wails + keyring. Scope notes: emphasis markers
// are stripped (styled in the preview, plain in print), sections and
// synopses are omitted (Fountain's own convention for canonically formatted
// output), and a short cue at a page's bottom moves to the next page rather
// than growing (MORE)/(CONT'D) treatment.

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// SaveDocumentExportRequest names a saved document artifact and the export
// rendering to write — "pdf" or "fdx".
type SaveDocumentExportRequest struct {
	Path          string `json:"path"`
	Format        string `json:"format"`
	SuggestedName string `json:"suggestedName,omitempty"`
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
	elements, titlePage := fountainParse(data)
	var rendered []byte
	extension, filterName, pattern := "", "", ""
	switch format {
	case "pdf":
		rendered, err = fountainPDF(elements, titlePage)
		extension, filterName, pattern = ".pdf", "PDF Files", "*.pdf"
	default:
		rendered, err = fountainFDX(elements, titlePage)
		extension, filterName, pattern = ".fdx", "Final Draft Files", "*.fdx"
	}
	if err != nil {
		return "", err
	}
	return a.saveArtifactDialog(rendered, req.SuggestedName, "atelier-screenplay", extension, "Export document", []runtime.FileFilter{
		{DisplayName: filterName, Pattern: pattern},
		{DisplayName: "All Files", Pattern: "*.*"},
	})
}

// --- PDF ---------------------------------------------------------------

// The industry page: US Letter, Courier 12pt (10 characters per inch, 6
// lines per inch), 1.5" left margin and 1" everywhere else — 60 characters
// and 55 lines of text per page. Element indents follow the convention
// measured from the text edge, in characters.
const (
	pdfPageWidth        = 612.0
	pdfPageHeight       = 792.0
	pdfMarginLeft       = 108.0
	pdfTextRight        = 540.0
	pdfTopBaseline      = 720.0
	pdfLineHeight       = 12.0
	pdfCharWidth        = 7.2
	pdfLinesPerPage     = 55
	fountainIndentChar  = 22 // character cue
	fountainIndentPar   = 16 // parenthetical
	fountainIndentDia   = 10 // dialogue
	fountainWidthAction = 60
	fountainWidthCue    = 35
	fountainWidthParen  = 30
	fountainWidthDia    = 35
)

// fountainPDFLine is one rendered row before pagination: wrapped text at a
// column, with the alignment and underline flags the title page needs.
type fountainPDFLine struct {
	text      string
	indent    int
	center    bool
	right     bool
	underline bool
}

// fountainLayoutLine converts one element into its rendered rows — wrapped
// at the element's width, emphasis markers stripped, alignment decided by
// kind. Returns nothing for elements the formatted output omits (sections,
// synopses, page breaks — the break is pagination's job).
func fountainLayoutLine(element FountainElement) []fountainPDFLine {
	width := fountainWidthAction
	indent := 0
	center, right := false, false
	switch element.Kind {
	case fountainScene, fountainAction, fountainSection, fountainSynopsis:
		if element.Kind == fountainSection || element.Kind == fountainSynopsis {
			return nil
		}
	case fountainTransition:
		right = true
	case fountainCharacter:
		width, indent = fountainWidthCue, fountainIndentChar
	case fountainParenthetical:
		width, indent = fountainWidthParen, fountainIndentPar
	case fountainDialogue:
		width, indent = fountainWidthDia, fountainIndentDia
	case fountainCentered:
		center = true
	}
	lines := fountainWrapText(fountainStripEmphasis(element.Text), width)
	rendered := make([]fountainPDFLine, len(lines))
	for i, line := range lines {
		rendered[i] = fountainPDFLine{text: line, indent: indent, center: center, right: right}
	}
	return rendered
}

// fountainWrapText breaks a line at word boundaries to the character width —
// Courier makes columns arithmetic, no font metrics needed. A word longer
// than the width stands alone on its line (it overflows the column rather
// than being split).
func fountainWrapText(text string, width int) []string {
	words := strings.Fields(text)
	if len(words) == 0 {
		return []string{""}
	}
	lines := []string{}
	current := words[0]
	for _, word := range words[1:] {
		if len(current)+1+len(word) <= width {
			current += " " + word
			continue
		}
		lines = append(lines, current)
		current = word
	}
	return append(lines, current)
}

// fountainStripEmphasis removes Fountain's *bold* / _underline_ markers —
// the print path renders plain Courier; the preview is where emphasis shows.
func fountainStripEmphasis(text string) string {
	replacer := strings.NewReplacer("*", "", "_", "")
	return replacer.Replace(text)
}

// fountainPDF renders the script. Returns the full PDF file bytes — objects,
// xref, trailer, %%EOF.
func fountainPDF(elements []FountainElement, titlePage map[string]string) ([]byte, error) {
	// Layout: optional title page, then the body, paginated so a scene
	// heading or a character cue (with room for one line of its speech)
	// never strands on a page's last line — the (MORE)/(CONT'D) machinery
	// this defers is the only loss.
	pages := [][]fountainPDFLine{}
	titleRow := -1
	if len(titlePage) > 0 {
		pages = append(pages, fountainTitlePageLines(titlePage))
		titleRow = fountainTitleRowIndex(titlePage)
	}
	type block []fountainPDFLine
	blocks := []block{}
	for _, element := range elements {
		lines := fountainLayoutLine(element)
		if len(lines) == 0 {
			continue
		}
		// A whole element keeps its own lines together when it matters
		// (scene heading, character cue); everything else paginates per row.
		if element.Kind == fountainScene || element.Kind == fountainCharacter {
			blocks = append(blocks, lines)
		} else {
			for _, line := range lines {
				blocks = append(blocks, block{line})
			}
		}
	}
	current := []fountainPDFLine{}
	lineAt := 0
	newPage := func() {
		pages = append(pages, current)
		current = []fountainPDFLine{}
		lineAt = 0
	}
	for _, blk := range blocks {
		if lineAt == pdfLinesPerPage {
			newPage()
		}
		needsRoom := len(blk)
		if needsRoom == 1 && blk[0].indent == fountainIndentChar {
			needsRoom = 2
		}
		if lineAt+needsRoom > pdfLinesPerPage {
			newPage()
		}
		current = append(current, blk...)
		lineAt += len(blk)
	}
	if len(current) > 0 || len(pages) == 0 {
		pages = append(pages, current)
	}

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
	pageCount := len(pages)
	kids := []string{}
	for i := range pages {
		kids = append(kids, fmt.Sprintf("%d 0 R", 5+2*i))
	}
	writeObj(1, "<< /Type /Catalog /Pages 2 0 R >>")
	writeObj(2, fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), pageCount))
	writeObj(3, "<< /Type /Font /Subtype /Type1 /BaseFont /Courier >>")
	writeObj(4, "<< /Type /Font /Subtype /Type1 /BaseFont /Courier-Bold >>")
	scriptPage := 0
	for i, pageLines := range pages {
		isTitle := len(titlePage) > 0 && i == 0
		if !isTitle {
			// The page number counts the script's own pages; the title page
			// is outside the numbering.
			scriptPage++
		}
		row := -1
		if isTitle {
			row = titleRow
		}
		pageID, contentID := 5+2*i, 6+2*i
		stream := fountainPageStream(pageLines, row, scriptPage)
		writeObj(pageID, fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %.0f %.0f] /Resources << /Font << /F1 3 0 R /F2 4 0 R >> >> /Contents %d 0 R >>", pdfPageWidth, pdfPageHeight, contentID))
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
	return w.bytes(), nil
}

// fountainPageStream renders one page's content stream: per-line BT/Tj/ET
// blocks at absolute positions, the underline stroke under the title row
// (title pages only), and the page number top-right from the script's second
// page — the title page is never numbered.
func fountainPageStream(lines []fountainPDFLine, underlineRow int, scriptPage int) string {
	var b bytes.Buffer
	rowY := func(row int) float64 {
		return pdfTopBaseline - float64(row)*pdfLineHeight
	}
	for row, line := range lines {
		text := pdfEscape(line.text)
		if text == "" {
			continue
		}
		width := float64(len(line.text)) * pdfCharWidth
		var x float64
		switch {
		case line.center:
			x = pdfMarginLeft + (pdfTextRight-pdfMarginLeft-width)/2
		case line.right:
			x = pdfTextRight - width
		default:
			x = pdfMarginLeft + float64(line.indent)*pdfCharWidth
		}
		font := "/F1"
		if line.underline {
			font = "/F2"
		}
		fmt.Fprintf(&b, "BT %s 12 Tf 1 0 0 1 %.2f %.2f Tm (%s) Tj ET\n", font, x, rowY(row), text)
		if row == underlineRow || line.underline {
			fmt.Fprintf(&b, "%.2f %.2f m %.2f %.2f l S\n", x, rowY(row)-2, x+width, rowY(row)-2)
		}
	}
	if scriptPage >= 2 && underlineRow < 0 {
		label := fmt.Sprintf("%d.", scriptPage)
		x := pdfTextRight - float64(len(label))*pdfCharWidth
		fmt.Fprintf(&b, "BT /F1 12 Tf 1 0 0 1 %.2f %.2f Tm (%s) Tj ET\n", x, pdfTopBaseline+pdfLineHeight, pdfEscape(label))
	}
	return b.String()
}

// fountainTitlePageLines lays out the Fountain title page: the creative
// block centered in the upper third, contact and draft info pinned to the
// bottom-left. Keys the renderers don't know are skipped.
func fountainTitlePageLines(titlePage map[string]string) []fountainPDFLine {
	blank := fountainPDFLine{}
	lines := []fountainPDFLine{}
	for i := 0; i < 18; i++ {
		lines = append(lines, blank)
	}
	center := func(value string, underline bool) fountainPDFLine {
		return fountainPDFLine{text: value, center: true, underline: underline}
	}
	if v := titlePage["Title"]; v != "" {
		lines = append(lines, center(v, true), blank)
	}
	if v := titlePage["Credit"]; v != "" {
		lines = append(lines, center(v, false), blank)
	}
	if v := titlePage["Author"]; v != "" {
		lines = append(lines, center(v, false), blank)
	}
	if v := titlePage["Authors"]; v != "" {
		lines = append(lines, center(v, false), blank)
	}
	if v := titlePage["Source"]; v != "" {
		lines = append(lines, blank, center(v, false))
	}
	for len(lines) < pdfLinesPerPage-6 {
		lines = append(lines, blank)
	}
	if v := titlePage["Contact"]; v != "" {
		for _, part := range strings.Split(v, " ") {
			// Contact values keep their line breaks as spaces; the bottom
			// block renders one fragment per row.
			lines = append(lines, fountainPDFLine{text: part})
		}
	}
	if v := titlePage["Draft date"]; v != "" {
		lines = append(lines, fountainPDFLine{text: v})
	}
	return lines[:pdfLinesPerPage]
}

// fountainTitleRowIndex finds the row the underline strokes go under —
// the row the title was laid out at (mirrors fountainTitlePageLines's walk).
func fountainTitleRowIndex(titlePage map[string]string) int {
	row := 18
	if titlePage["Title"] == "" {
		return -1
	}
	return row
}

// pdfBuf tracks object byte offsets for the xref table.
type pdfBuf struct {
	buf bytes.Buffer
}

func (w *pdfBuf) writeString(s string) { w.buf.WriteString(s) }
func (w *pdfBuf) raw() *bytes.Buffer   { return &w.buf }
func (w *pdfBuf) len() int             { return w.buf.Len() }
func (w *pdfBuf) bytes() []byte        { return w.buf.Bytes() }

// pdfEscape encodes a text string as a PDF literal string: backslash and
// parentheses escaped, everything outside latin-1 substituted (Courier's
// WinAnsi range; the preview is where full Unicode lives).
func pdfEscape(text string) string {
	var b strings.Builder
	for _, r := range text {
		switch r {
		case '\\', '(', ')':
			b.WriteByte('\\')
			b.WriteRune(r)
		case '’', '‘':
			b.WriteByte('\'')
		case '“', '”':
			b.WriteByte('"')
		case '—':
			b.WriteString("--")
		case '–':
			b.WriteByte('-')
		case '…':
			b.WriteString("...")
		default:
			if r < 256 {
				b.WriteRune(r)
			} else {
				b.WriteByte('?')
			}
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
		if value := titlePage[key]; value != "" {
			doc.TitlePage.Paragraphs = append(doc.TitlePage.Paragraphs, fdxTitleParagraph{Alignment: "Center", Text: value})
		}
	}
	rendered, err := xml.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	return append([]byte(xml.Header), append(rendered, '\n')...), nil
}
