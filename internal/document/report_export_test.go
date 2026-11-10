package document

import (
	"fmt"
	"strings"
	"testing"
)

func sampleReport(rows int) Report {
	table := &ReportTable{
		Columns: []ReportColumn{{Header: "Model"}, {Header: "Calls", Right: true}, {Header: "Cost", Right: true}},
	}
	for i := 0; i < rows; i++ {
		table.Rows = append(table.Rows, []string{fmt.Sprintf("model-%d", i), fmt.Sprintf("%d", i), "$0.010"})
	}
	return Report{
		Title:    "Project report — Atelier",
		Subtitle: []string{"Generated 2026-11-10"},
		Sections: []ReportSection{
			{Heading: "Summary", Facts: []ReportFact{{Label: "Cost", Value: "$1.23"}, {Label: "Model calls", Value: "42"}}},
			{Heading: "Cost by model", Table: table, Notes: []string{"Costs are estimates."}},
		},
		Footer: []string{"Exported from Atelier."},
	}
}

func TestReportPDFShape(t *testing.T) {
	data := RenderReportPDF(sampleReport(3), pdfPaperLetter)
	content := string(data)
	if !strings.HasPrefix(content, "%PDF-1.4\n") || !strings.Contains(content, "%%EOF") {
		t.Fatal("the PDF must be a complete file")
	}
	// The em dash prints in its typewriter form; the title is bold at 16pt.
	if !strings.Contains(content, "/F2 16 Tf") || !strings.Contains(content, "(Project report -- Atelier) Tj") {
		t.Fatal("the title must render bold at the large size with typewriter punctuation")
	}
	// The fact's value prints bold beside its padded label.
	if !strings.Contains(content, "(42) Tj") || !strings.Contains(content, "(Model calls  ) Tj") {
		t.Fatalf("the facts must print label and value runs:\n%s", content)
	}
	// Section headings and the table header carry a rule.
	if strings.Count(content, " l S\n") < 3 {
		t.Fatal("headings and the table header must be underlined")
	}
	if strings.Contains(content, "(2.) Tj") {
		t.Fatal("a one-page report must not carry a page number")
	}
}

// Figures right-align in their columns, and every row ends inside the text
// block on both sheets — the name column wraps rather than pushing figures
// past the margin.
func TestReportTableAlignsAndFits(t *testing.T) {
	long := strings.Repeat("very-long-model-identifier/", 6)
	report := Report{Sections: []ReportSection{{
		Heading: "Models",
		Table: &ReportTable{
			Columns: []ReportColumn{{Header: "Model"}, {Header: "Calls", Right: true}, {Header: "Cost", Right: true}},
			Rows:    [][]string{{"short", "7", "$0.01"}, {long, "12345", "$123.45"}},
		},
	}}}
	for _, paper := range []Paper{pdfPaperLetter, pdfPaperA4} {
		pages := reportPaginate(paper, reportBlocks(report, paper))
		var tableRows []fountainPDFLine
		for _, line := range pages[0] {
			if line.pdfSize() == reportTableSize {
				tableRows = append(tableRows, line)
			}
		}
		if len(tableRows) < 4 {
			t.Fatalf("%s: the long model name must wrap onto continuation rows, got %d table rows", paper.name, len(tableRows))
		}
		width := len([]rune(tableRows[0].text))
		for _, line := range tableRows {
			charWidth := pdfCharWidthAt(line.pdfSize())
			right := pdfMarginLeft + float64(line.indent+len([]rune(line.text)))*charWidth
			if right > paper.textRight+1e-6 {
				t.Errorf("%s: row %q ends at %.1fpt, past the text block's %.1fpt", paper.name, line.text, right, paper.textRight)
			}
		}
		// The header and both first-lines of the rows end at the same column:
		// the Cost figures right-align.
		if !strings.HasSuffix(tableRows[0].text, "Cost") || !strings.HasSuffix(tableRows[1].text, "$0.01") {
			t.Fatalf("%s: unexpected rows %q / %q", paper.name, tableRows[0].text, tableRows[1].text)
		}
		if got := len([]rune(tableRows[1].text)); got != width {
			t.Errorf("%s: short row is %d wide, header %d — figures must right-align", paper.name, got, width)
		}
		if !tableRows[0].ruleBelow || tableRows[0].pdfFont() != "/F2" {
			t.Errorf("%s: the header row must print bold with a rule below", paper.name)
		}
	}
}

// A table that crosses a page reprints its heading (continued) and header
// row at the next page's top; no page opens with spacing.
func TestReportTableRepeatsHeaderAcrossPages(t *testing.T) {
	pages := reportPaginate(pdfPaperLetter, reportBlocks(sampleReport(120), pdfPaperLetter))
	if len(pages) < 2 {
		t.Fatalf("120 rows must cross a page, got %d pages", len(pages))
	}
	for i, page := range pages {
		if len(page) > pdfPaperLetter.linesPerPage {
			t.Fatalf("page %d carries %d lines, more than the paper's %d", i+1, len(page), pdfPaperLetter.linesPerPage)
		}
		if len(page) == 0 || strings.TrimSpace(page[0].text) == "" {
			t.Fatalf("page %d opens blank", i+1)
		}
		if i == 0 {
			continue
		}
		texts := rowTexts(page)
		if i < len(pages)-1 || strings.HasPrefix(texts[2], "Model") {
			if texts[0] != "Cost by model (continued)" || !strings.HasPrefix(texts[2], "Model") {
				t.Fatalf("page %d must reprint the heading and header, got %q", i+1, texts[:3])
			}
		}
	}
	if !strings.Contains(string(RenderReportPDF(sampleReport(120), pdfPaperLetter)), "(2.) Tj") {
		t.Fatal("the second page must carry its number")
	}
}

// A section heading never strands at a page's foot: it moves on with the
// table header and first row.
func TestReportHeadingKeepsWithTable(t *testing.T) {
	paper := pdfPaperLetter
	report := Report{Sections: []ReportSection{
		{Heading: "Filler", Notes: []string{}},
		{Heading: "Table", Table: &ReportTable{Columns: []ReportColumn{{Header: "A"}}, Rows: [][]string{{"x"}, {"y"}}}},
	}}
	// Pad the first section to leave exactly two lines at the page's foot.
	for i := 0; i < paper.linesPerPage-2-2; i++ {
		report.Sections[0].Notes = append(report.Sections[0].Notes, "note")
	}
	pages := reportPaginate(paper, reportBlocks(report, paper))
	if len(pages) != 2 {
		t.Fatalf("got %d pages, want 2", len(pages))
	}
	last := pages[0][len(pages[0])-1]
	if last.text == "Table" {
		t.Fatal("the heading stranded at the foot of page 1")
	}
	if pages[1][0].text != "Table" {
		t.Fatalf("page 2 must open with the heading, got %q", pages[1][0].text)
	}
}

func TestReportWrapHardBreaksLongWords(t *testing.T) {
	got := reportWrap("abcdefghij klm", 4)
	want := []string{"abcd", "efgh", "ij", "klm"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("reportWrap = %q, want %q", got, want)
	}
	// An identifier breaks after a separator, never inside a version number.
	got = reportWrap("fal-ai/kling-video/v2.5-turbo", 12)
	want = []string{"fal-ai/", "kling-video/", "v2.5-turbo"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("reportWrap = %q, want %q", got, want)
	}
	got = reportWrap("anthropic/claude-sonnet-4.5", 26)
	want = []string{"anthropic/claude-sonnet-", "4.5"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("reportWrap = %q, want %q", got, want)
	}
}

// A "\n" in a cell is a forced break: the model name above its provider, the
// column sized to the longer line, figures on the first line only.
func TestReportCellForcedBreak(t *testing.T) {
	columns := []ReportColumn{{Header: "Model"}, {Header: "Calls", Right: true}}
	cells := []string{"claude-sonnet\nopenrouter", "3"}
	widths := reportFitColumns(columns, []string{"Model", "Calls"}, [][]string{cells}, 20)
	if widths[0] != len("claude-sonnet") {
		t.Fatalf("the name column must size to its longest line, got %d", widths[0])
	}
	lines := reportRowLines(columns, cells, widths)
	if len(lines) != 2 || lines[0].text != "claude-sonnet      3" || lines[1].text != "openrouter" {
		t.Fatalf("rows = %q", rowTexts(lines))
	}
	// The provider prints gray so it can't read as more of the name; the
	// name's own wrapped lines stay black.
	if lines[0].gray || !lines[1].gray {
		t.Fatalf("gray = %v/%v, want the provider line alone gray", lines[0].gray, lines[1].gray)
	}
	wrapped := reportRowLines(columns, []string{"fal-ai/kling-video\nfal", "3"}, []int{10, 5})
	if len(wrapped) != 4 || wrapped[1].gray || wrapped[2].gray || !wrapped[3].gray {
		t.Fatalf("a wrapped name's continuation must stay black: %q", rowTexts(wrapped))
	}
}

// Every table spans the text block — slack goes to the name column — so the
// right-hand columns of different tables line up.
func TestReportTablesSpanTheTextBlock(t *testing.T) {
	columns := []ReportColumn{{Header: "Tool"}, {Header: "Calls", Right: true}}
	widths := reportFitColumns(columns, []string{"Tool", "Calls"}, [][]string{{"x", "1"}}, 80)
	if widths[0]+reportTableGutter+widths[1] != 80 {
		t.Fatalf("widths %v must fill 80 characters", widths)
	}
}

// Money aligns on the decimal point: "$1.20" and "$0.0015" share the point's
// column, and a figure without one aligns right.
func TestReportDecimalColumnAlignsPoints(t *testing.T) {
	report := Report{Sections: []ReportSection{{Table: &ReportTable{
		Columns: []ReportColumn{{Header: "Model"}, {Header: "Cost", Decimal: true}},
		Rows:    [][]string{{"a", "$1.20"}, {"b", "$0.0015"}, {"c", "~$30.80"}, {"d", "--"}},
	}}}}
	pages := reportPaginate(pdfPaperLetter, reportBlocks(report, pdfPaperLetter))
	points := map[int]bool{}
	var dash string
	for _, line := range pages[0] {
		if line.pdfSize() != reportTableSize || line.pdfFont() == "/F2" {
			continue
		}
		if at := strings.LastIndex(line.text, "."); at >= 0 {
			points[at] = true
		} else {
			dash = line.text
		}
	}
	if len(points) != 1 {
		t.Fatalf("decimal points sit in %d columns, want 1", len(points))
	}
	if len(dash) != pdfPaperLetter.columnsAt(reportTableSize) {
		t.Fatalf("a figure without a point must align right, got %q", dash)
	}
}
