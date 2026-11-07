package main

import "math"

// The paper a document export prints on. Every sheet keeps the screenplay
// convention's margins — a 1.5" binding edge on the left, 1" everywhere
// else — and fills what's inside them on Courier 12's fixed grid: 10
// characters and 6 lines to the inch. The page box, the full-width column
// (action, scene headings, transitions, the markdown body) and the page's
// line count belong to the paper; the speech columns are measured from the
// text edge in characters and sit inside every sheet unchanged.

// pdfPaper is one sheet's geometry in points (1/72"): the page box and the
// text block its margins leave.
type pdfPaper struct {
	name   string
	width  float64
	height float64
	// columns is the full-width column in characters, and textRight its
	// right edge: the left margin plus that many Courier advances, as many as
	// a 1" right margin allows.
	columns   int
	textRight float64
	// topBaseline is the first row's baseline, 1" below the top edge. Rows
	// run down the 12pt grid from it — linesPerPage of them before a baseline
	// would cross the 1" bottom margin.
	topBaseline  float64
	linesPerPage int
}

// pdfPaperLetter is US Letter (8.5" × 11"), the US film and TV industry's
// page: 60 characters × 55 lines.
var pdfPaperLetter = pdfPaper{name: "letter", width: 612, height: 792, columns: 60, textRight: 540, topBaseline: 720, linesPerPage: 55}

// titleTopRow is the row the title page's creative block starts on — a
// third of the way down the page.
func (p pdfPaper) titleTopRow() int {
	return p.linesPerPage / 3
}

// columnsAt is how many characters of Courier at a point size fit the text
// block — the markdown layout's measure for its smaller code and table text
// and its larger headings. At 12pt it is the paper's own column.
func (p pdfPaper) columnsAt(size float64) int {
	return int(math.Floor((p.textRight-pdfMarginLeft)/pdfCharWidthAt(size) + 1e-9))
}
