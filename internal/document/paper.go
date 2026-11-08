package document

import (
	"context"
	"fmt"
	"math"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// The paper a document export prints on. Every sheet keeps the screenplay
// convention's margins — a 1.5" binding edge on the left, 1" everywhere
// else — and fills what's inside them on Courier 12's fixed grid: 10
// characters and 6 lines to the inch. The page box, the full-width column
// (action, scene headings, transitions, the markdown body) and the page's
// line count belong to the paper; the speech columns are measured from the
// text edge in characters and sit inside every sheet unchanged.
//
// US Letter is the US film and TV industry's page — the one the "page ≈ a
// minute" rule and contest page limits assume. A4 is the office paper nearly
// everywhere else: narrower and taller, so the same margins hold 57
// characters × 59 lines instead of 60 × 55, and a script runs a few pages
// shorter with its breaks in different places. Which page to print is the
// writer's call: the editor's Print block starts a screenplay on US Letter
// whatever the region (the industry page travels), a markdown document at the
// machine's region's paper (DefaultPaperSize), and remembers the last
// choice per dialect.

const (
	documentPaperLetter = "letter"
	documentPaperA4     = "a4"
)

// Paper is one sheet's geometry in points (1/72"): the page box and the
// text block its margins leave.
type Paper struct {
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

// pdfPaperLetter is US Letter (8.5" × 11"): 60 characters × 55 lines.
var pdfPaperLetter = Paper{name: documentPaperLetter, width: 612, height: 792, columns: 60, textRight: 540, topBaseline: 720, linesPerPage: 55}

// pdfPaperA4 is A4 (210 × 297 mm): 57 characters × 59 lines — the right
// margin lands at 1.07" (a 58th character would cross 1"), the last baseline
// at 1.03" above the bottom edge.
var pdfPaperA4 = Paper{name: documentPaperA4, width: 595.28, height: 841.89, columns: 57, textRight: 518.4, topBaseline: 769.89, linesPerPage: 59}

// PaperFor resolves a paper size name — "letter" or "a4", any case.
// Empty is US Letter, the export's page before the choice existed.
func PaperFor(name string) (Paper, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", documentPaperLetter:
		return pdfPaperLetter, nil
	case documentPaperA4:
		return pdfPaperA4, nil
	}
	return Paper{}, fmt.Errorf(`paper size must be %q or %q, got %q`, documentPaperLetter, documentPaperA4, name)
}

// titleTopRow is the row the title page's creative block starts on — a
// third of the way down the page.
func (p Paper) titleTopRow() int {
	return p.linesPerPage / 3
}

// columnsAt is how many characters of Courier at a point size fit the text
// block — the markdown layout's measure for its smaller code and table text
// and its larger headings. At 12pt it is the paper's own column.
func (p Paper) columnsAt(size float64) int {
	return int(math.Floor((p.textRight-pdfMarginLeft)/pdfCharWidthAt(size) + 1e-9))
}

// letterPaperRegions are the territories whose office paper is US Letter —
// CLDR's paperSize data (common/supplemental/supplementalData.xml); every
// other territory's is A4.
var letterPaperRegions = map[string]bool{
	"BZ": true, "CA": true, "CL": true, "CO": true, "CR": true, "GT": true, "MX": true,
	"NI": true, "PA": true, "PH": true, "PR": true, "SV": true, "US": true, "VE": true,
}

// SystemLocale reports the machine's locale. On macOS that is the
// AppleLocale default, which carries the Region setting even where it
// differs from the language — "en_US@rg=inzzzz" is US English formatted for
// India — and is read even for a Dock launch, which inherits no LANG. A
// terminal's LANG is no stand-in there: it often says en_US whatever the
// region. Elsewhere it is the POSIX locale variables in their precedence. A
// seam: the app's and this package's TestMain pin it so tests never read
// the machine's region.
var SystemLocale = func() string {
	if runtime.GOOS == "darwin" {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if out, err := exec.CommandContext(ctx, "/usr/bin/defaults", "read", "-g", "AppleLocale").Output(); err == nil {
			if id := strings.TrimSpace(string(out)); id != "" {
				return id
			}
		}
	}
	for _, name := range []string{"LC_ALL", "LC_PAPER", "LANG"} {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			return value
		}
	}
	return ""
}

// localeRegion extracts the two-letter region a locale identifier names: a
// Unicode region override (@rg=inzzzz, ICU's keyword form) wins; otherwise
// the first two-letter subtag after the language — en_IN, en-GB, zh-Hans_CN,
// de_DE.UTF-8. "" when the identifier names no country (en, C, POSIX, or
// es_419's multi-country area).
func localeRegion(identifier string) string {
	id := strings.TrimSpace(identifier)
	if at := strings.IndexByte(id, '@'); at >= 0 {
		for _, keyword := range strings.Split(id[at+1:], ";") {
			key, value, ok := strings.Cut(strings.TrimSpace(keyword), "=")
			if ok && strings.EqualFold(key, "rg") && len(value) >= 2 && isASCIILetters(value[:2]) {
				return strings.ToUpper(value[:2])
			}
		}
		id = id[:at]
	}
	if dot := strings.IndexByte(id, '.'); dot >= 0 {
		id = id[:dot] // a POSIX codeset: de_DE.UTF-8
	}
	parts := strings.FieldsFunc(id, func(r rune) bool { return r == '_' || r == '-' })
	for _, part := range parts[min(1, len(parts)):] {
		if len(part) == 2 && isASCIILetters(part) {
			return strings.ToUpper(part)
		}
	}
	return ""
}

func isASCIILetters(s string) bool {
	for _, r := range s {
		if !('a' <= r && r <= 'z' || 'A' <= r && r <= 'Z') {
			return false
		}
	}
	return s != ""
}

// DefaultPaperSize is a markdown document's starting paper in the
// Print block until the writer picks one: the region's office paper — US
// Letter where CLDR says so, A4 everywhere else — and US Letter, the export's
// historical page, when no region can be read. Screenplays don't ask: they
// start on US Letter everywhere.
func DefaultPaperSize() string {
	region := localeRegion(SystemLocale())
	if region == "" || letterPaperRegions[region] {
		return documentPaperLetter
	}
	return documentPaperA4
}
