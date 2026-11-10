package document

// The report layout: a tabular summary — the usage report's PDF export —
// laid onto the same hand-rolled page writer the screenplay and markdown
// exports print through. Courier makes tables arithmetic: every column is a
// character count, so numbers right-align exactly without font metrics.
//
// A report is a title, gray subtitle lines, and sections. Each section is a
// bold underlined heading followed by any of: label/value facts (the summary
// stats), a table, and gray notes. Tables fit the text block at their own
// smaller size and span its width: when the natural widths overflow,
// left-aligned columns (names) narrow and their cells wrap onto continuation
// rows, the widest first, breaking long identifiers after a separator;
// right-aligned columns (figures) never wrap, and money aligns on its decimal
// point. A table that crosses a page repeats its heading, marked
// "(continued)", and its header row.

import (
	"strings"
	"unicode/utf8"
)

const (
	reportTitleSize   = 16.0
	reportHeadingSize = 12.0
	reportBodySize    = 10.0
	reportTableSize   = 9.0
	reportTableGutter = 2
	// A left-aligned column narrows to no less than this (or its header).
	reportMinFlexWidth = 8
)

// Report is one printable report.
type Report struct {
	Title    string
	Subtitle []string
	Sections []ReportSection
	// Footer prints gray after the last section.
	Footer []string
}

// ReportSection is a heading plus its body, printed in field order: facts,
// then the table, then notes. An empty heading prints the body alone.
type ReportSection struct {
	Heading string
	Facts   []ReportFact
	Table   *ReportTable
	Notes   []string
}

// ReportFact is one label/value summary line; values align in one column.
type ReportFact struct {
	Label string
	Value string
}

// ReportTable is a header row plus body rows; a row shorter than the columns
// prints blanks for the rest. A "\n" in a cell is a forced line break (a model
// name above its provider), each line wrapping to the column on its own.
type ReportTable struct {
	Columns []ReportColumn
	Rows    [][]string
}

// ReportColumn is one table column. Right right-aligns it (figures);
// Decimal right-aligns it on the decimal point (money), padding each figure
// so the points line up while cells without one ("--", "?") align right.
type ReportColumn struct {
	Header  string
	Right   bool
	Decimal bool
}

// reportBlock is one unit of the report's page flow: lines that stay
// together, the room they need before a break (keep — a heading asks for its
// body's first lines too), and the lines a page opened by this block reprints
// first (a continued table's heading and header row). A blank block is
// spacing, dropped at a page's top.
type reportBlock struct {
	lines  []fountainPDFLine
	keep   int
	repeat []fountainPDFLine
	blank  bool
}

// RenderReportPDF lays the report onto the paper and writes the PDF. Pages
// number from the second, like every rendered document.
func RenderReportPDF(report Report, paper Paper) []byte {
	return pdfWrite(paper, reportPaginate(paper, reportBlocks(report, paper)), false)
}

func reportBlocks(report Report, paper Paper) []reportBlock {
	blocks := []reportBlock{}
	blank := reportBlock{lines: []fountainPDFLine{{}}, blank: true}
	if title := pdfPrintable(strings.TrimSpace(report.Title)); title != "" {
		// Each 16pt line takes two slots of the 12pt grid: on one, a wrapped
		// title's descenders meet the next line's ascenders and the subtitle
		// crowds beneath it.
		lines := []fountainPDFLine{}
		for _, piece := range reportWrap(title, paper.columnsAt(reportTitleSize)) {
			lines = append(lines, fountainPDFLine{text: piece, font: "/F2", size: reportTitleSize}, fountainPDFLine{})
		}
		blocks = append(blocks, reportBlock{lines: lines})
	}
	for _, line := range report.Subtitle {
		blocks = append(blocks, reportGrayLines(line, paper)...)
	}
	for _, section := range report.Sections {
		blocks = append(blocks, blank)
		blocks = append(blocks, reportSectionBlocks(section, paper)...)
	}
	if len(report.Footer) > 0 {
		blocks = append(blocks, blank)
		for _, line := range report.Footer {
			blocks = append(blocks, reportGrayLines(line, paper)...)
		}
	}
	return blocks
}

// reportGrayLines wraps one gray body-size paragraph, a block per line so a
// long note may break anywhere.
func reportGrayLines(text string, paper Paper) []reportBlock {
	text = pdfPrintable(strings.TrimSpace(text))
	if text == "" {
		return nil
	}
	blocks := []reportBlock{}
	for _, piece := range reportWrap(text, paper.columnsAt(reportBodySize)) {
		blocks = append(blocks, reportBlock{lines: []fountainPDFLine{{text: piece, size: reportBodySize, gray: true}}})
	}
	return blocks
}

func reportHeadingLine(text string) fountainPDFLine {
	return fountainPDFLine{text: text, font: "/F2", size: reportHeadingSize, ruleBelow: true}
}

func reportSectionBlocks(section ReportSection, paper Paper) []reportBlock {
	body := []reportBlock{}
	if len(section.Facts) > 0 {
		body = append(body, reportFactBlocks(section.Facts, paper)...)
	}
	heading := pdfPrintable(strings.TrimSpace(section.Heading))
	if section.Table != nil && len(section.Table.Columns) > 0 {
		if len(body) > 0 {
			body = append(body, reportBlock{lines: []fountainPDFLine{{}}, blank: true})
		}
		var continued []fountainPDFLine
		if heading != "" {
			continued = []fountainPDFLine{reportHeadingLine(heading + " (continued)"), {}}
		}
		body = append(body, reportTableBlocks(*section.Table, paper, continued)...)
	}
	if len(section.Notes) > 0 {
		if len(body) > 0 {
			body = append(body, reportBlock{lines: []fountainPDFLine{{}}, blank: true})
		}
		for _, note := range section.Notes {
			body = append(body, reportGrayLines(note, paper)...)
		}
	}
	if heading == "" {
		return body
	}
	// The heading keeps its body's first block (a table's header and first
	// row ride one block) on its page.
	lead := 0
	if len(body) > 0 {
		lead = len(body[0].lines)
		if body[0].keep > lead {
			lead = body[0].keep
		}
	}
	headingBlock := reportBlock{
		lines: []fountainPDFLine{reportHeadingLine(heading), {}},
	}
	headingBlock.keep = len(headingBlock.lines) + lead
	return append([]reportBlock{headingBlock}, body...)
}

// reportFactBlocks prints label/value pairs with the values in one column —
// the label regular, the value bold.
func reportFactBlocks(facts []ReportFact, paper Paper) []reportBlock {
	labelWidth := 0
	for _, fact := range facts {
		if n := utf8.RuneCountInString(pdfPrintable(fact.Label)); n > labelWidth {
			labelWidth = n
		}
	}
	valueWidth := paper.columnsAt(reportBodySize) - labelWidth - reportTableGutter
	if valueWidth < reportMinFlexWidth {
		valueWidth = reportMinFlexWidth
	}
	blocks := []reportBlock{}
	for _, fact := range facts {
		label := pdfPrintable(fact.Label)
		pad := strings.Repeat(" ", labelWidth-utf8.RuneCountInString(label)+reportTableGutter)
		lines := []fountainPDFLine{}
		for i, piece := range reportWrap(pdfPrintable(fact.Value), valueWidth) {
			lead := label + pad
			if i > 0 {
				lead = strings.Repeat(" ", labelWidth+reportTableGutter)
			}
			lines = append(lines, fountainPDFLine{
				text:  lead + piece,
				size:  reportBodySize,
				spans: []pdfSpan{{text: lead}, {text: piece, bold: true}},
			})
		}
		blocks = append(blocks, reportBlock{lines: lines})
	}
	return blocks
}

// reportTableBlocks fits the table to the text block, then emits the header
// with the first row as one block (a header never strands at a page's foot)
// and every further row as its own block, each carrying the reprint a page it
// opens needs.
func reportTableBlocks(table ReportTable, paper Paper, continued []fountainPDFLine) []reportBlock {
	columns := len(table.Columns)
	headers := make([]string, columns)
	rows := make([][]string, len(table.Rows))
	for i, column := range table.Columns {
		headers[i] = pdfPrintable(column.Header)
	}
	for r, row := range table.Rows {
		rows[r] = make([]string, columns)
		for i := 0; i < columns && i < len(row); i++ {
			rows[r][i] = pdfPrintable(strings.TrimSpace(row[i]))
		}
	}
	// Decimal columns pad each figure on the right so the points line up
	// ("$1.20" over "$0.0015"); the padded figures are what the fit measures.
	for i, column := range table.Columns {
		if !column.Decimal {
			continue
		}
		places := 0
		for _, row := range rows {
			if n := reportDecimalPlaces(row[i]); n > places {
				places = n
			}
		}
		for _, row := range rows {
			if strings.Contains(row[i], ".") {
				row[i] += strings.Repeat(" ", places-reportDecimalPlaces(row[i]))
			}
		}
	}
	widths := reportFitColumns(table.Columns, headers, rows, paper.columnsAt(reportTableSize))

	headerLines := reportRowLines(table.Columns, headers, widths)
	for i := range headerLines {
		headerLines[i].font = "/F2"
	}
	headerLines[len(headerLines)-1].ruleBelow = true
	repeat := append(append([]fountainPDFLine{}, continued...), headerLines...)

	if len(rows) == 0 {
		return []reportBlock{{lines: headerLines}}
	}
	blocks := []reportBlock{}
	for r, row := range rows {
		lines := reportRowLines(table.Columns, row, widths)
		if r == 0 {
			lines = append(append([]fountainPDFLine{}, headerLines...), lines...)
			blocks = append(blocks, reportBlock{lines: lines})
			continue
		}
		blocks = append(blocks, reportBlock{lines: lines, repeat: repeat})
	}
	return blocks
}

// reportDecimalPlaces counts the characters after a figure's decimal point
// (0 without one).
func reportDecimalPlaces(text string) int {
	at := strings.LastIndex(text, ".")
	if at < 0 {
		return 0
	}
	return utf8.RuneCountInString(text[at+1:])
}

func (c ReportColumn) rightAligned() bool {
	return c.Right || c.Decimal
}

// reportFitColumns sizes each column to its widest cell line, then fits the
// row to the available characters: while it overflows, the widest
// left-aligned column still above its floor (the larger of its header and
// reportMinFlexWidth) narrows — right-aligned figures keep their natural
// width, and a table whose figures alone overflow prints past the margin
// rather than mangling a number; while it falls short, the first left-aligned
// column takes the slack, so every table spans the text block and their
// right-hand columns line up page-wide.
func reportFitColumns(columns []ReportColumn, headers []string, rows [][]string, available int) []int {
	widths := make([]int, len(columns))
	floors := make([]int, len(columns))
	for i := range columns {
		widths[i] = utf8.RuneCountInString(headers[i])
		floors[i] = widths[i]
		if floors[i] < reportMinFlexWidth {
			floors[i] = reportMinFlexWidth
		}
		for _, row := range rows {
			for _, line := range strings.Split(row[i], "\n") {
				if n := utf8.RuneCountInString(line); n > widths[i] {
					widths[i] = n
				}
			}
		}
		if widths[i] < 1 {
			widths[i] = 1
		}
	}
	total := reportTableGutter * (len(widths) - 1)
	for _, w := range widths {
		total += w
	}
	for total > available {
		widest := -1
		for i, column := range columns {
			if column.rightAligned() || widths[i] <= floors[i] {
				continue
			}
			if widest < 0 || widths[i] > widths[widest] {
				widest = i
			}
		}
		if widest < 0 {
			break
		}
		widths[widest]--
		total--
	}
	if total < available {
		for i, column := range columns {
			if !column.rightAligned() {
				widths[i] += available - total
				break
			}
		}
	}
	return widths
}

// reportCellLine is one printed line of a cell; secondary marks the lines
// after the cell's first forced break (a model's provider), which print gray.
type reportCellLine struct {
	text      string
	secondary bool
}

// reportCellLines splits a cell at its forced breaks and wraps each part to
// the column. A part that already fits keeps its spacing (a decimal figure's
// alignment padding).
func reportCellLines(cell string, width int) []reportCellLine {
	out := []reportCellLine{}
	for part, line := range strings.Split(cell, "\n") {
		pieces := []string{line}
		if utf8.RuneCountInString(line) > width {
			pieces = reportWrap(strings.TrimSpace(line), width)
		}
		for _, piece := range pieces {
			out = append(out, reportCellLine{text: piece, secondary: part > 0})
		}
	}
	return out
}

// reportRowLines renders one row as one or more printed lines: each cell
// wraps to its column, and the row is as tall as its tallest cell. A line
// carrying only secondary text prints gray.
func reportRowLines(columns []ReportColumn, cells []string, widths []int) []fountainPDFLine {
	wrapped := make([][]reportCellLine, len(columns))
	height := 1
	for i := range columns {
		wrapped[i] = reportCellLines(cells[i], widths[i])
		if len(wrapped[i]) > height {
			height = len(wrapped[i])
		}
	}
	lines := make([]fountainPDFLine, height)
	gutter := strings.Repeat(" ", reportTableGutter)
	for k := 0; k < height; k++ {
		var b strings.Builder
		primary, secondary := false, false
		for i, column := range columns {
			if i > 0 {
				b.WriteString(gutter)
			}
			piece := reportCellLine{}
			if k < len(wrapped[i]) {
				piece = wrapped[i][k]
			}
			if strings.TrimSpace(piece.text) != "" {
				if piece.secondary {
					secondary = true
				} else {
					primary = true
				}
			}
			pad := widths[i] - utf8.RuneCountInString(piece.text)
			if pad < 0 {
				pad = 0
			}
			if column.rightAligned() {
				b.WriteString(strings.Repeat(" ", pad))
				b.WriteString(piece.text)
			} else {
				b.WriteString(piece.text)
				b.WriteString(strings.Repeat(" ", pad))
			}
		}
		lines[k] = fountainPDFLine{text: strings.TrimRight(b.String(), " "), size: reportTableSize, gray: secondary && !primary}
	}
	return lines
}

// reportBreakAfter are the characters an identifier breaks after when it is
// too long for its column: path, word, and namespace separators — never "."
// (a version number like 4.5 must stay whole).
const reportBreakAfter = "/-_:"

// reportWrap breaks text at word boundaries to width characters, hard-
// breaking any word longer than the width (a long model id must not run into
// the next column) — after its last separator that fits, when that leaves at
// least a third of the width on the line, else at the width.
func reportWrap(text string, width int) []string {
	if width < 1 {
		width = 1
	}
	pieces := []string{}
	for _, line := range fountainWrapText(text, width) {
		for utf8.RuneCountInString(line) > width {
			runes := []rune(line)
			cut := width
			for at := width - 1; at+1 >= (width+2)/3 && at > 0; at-- {
				if strings.ContainsRune(reportBreakAfter, runes[at]) {
					cut = at + 1
					break
				}
			}
			pieces = append(pieces, string(runes[:cut]))
			line = string(runes[cut:])
		}
		pieces = append(pieces, line)
	}
	return pieces
}

// reportPaginate breaks the report's blocks onto pages: a block that would
// cross the foot (counting the room it keeps for what follows) starts a new
// page, a page never opens with spacing, a page a table row opens reprints
// the table's heading and header first, and a block taller than a page
// breaks wherever the page fills.
func reportPaginate(paper Paper, blocks []reportBlock) [][]fountainPDFLine {
	pages := [][]fountainPDFLine{}
	current := []fountainPDFLine{}
	flush := func() {
		pages = append(pages, current)
		current = []fountainPDFLine{}
	}
	for _, block := range blocks {
		need := block.keep
		if len(block.lines) > need {
			need = len(block.lines)
		}
		if len(current) > 0 && len(current)+need > paper.linesPerPage {
			flush()
		}
		if len(current) == 0 {
			if block.blank {
				continue
			}
			current = append(current, block.repeat...)
		}
		for _, line := range block.lines {
			if len(current) == paper.linesPerPage {
				flush()
			}
			current = append(current, line)
		}
	}
	if len(current) > 0 || len(pages) == 0 {
		pages = append(pages, current)
	}
	return pages
}
