package main

import (
	"math"
	"strings"
	"testing"
)

// The papers' literal numbers are the margin rule worked out — this checks
// the arithmetic so a hand-edited profile can't drift from it: a 1.5" left
// margin and at least 1" everywhere else, with the column and the line count
// as large as those margins allow.
func TestPDFPaperMarginsHold(t *testing.T) {
	const inch, eps = 72.0, 1e-6
	for _, paper := range []pdfPaper{pdfPaperLetter, pdfPaperA4} {
		if got := pdfMarginLeft + float64(paper.columns)*pdfCharWidth; math.Abs(got-paper.textRight) > eps {
			t.Errorf("%s: textRight = %v, want the margin plus %d characters = %v", paper.name, paper.textRight, paper.columns, got)
		}
		if right := paper.width - paper.textRight; right < inch-eps || right-pdfCharWidth >= inch {
			t.Errorf("%s: right margin = %.2fpt, want the widest column a 1\" margin allows", paper.name, right)
		}
		if top := paper.height - paper.topBaseline; math.Abs(top-inch) > eps {
			t.Errorf("%s: first baseline %.2fpt below the top, want 1\"", paper.name, top)
		}
		last := paper.topBaseline - float64(paper.linesPerPage-1)*pdfLineHeight
		if last < inch-eps || last-pdfLineHeight >= inch {
			t.Errorf("%s: last baseline at %.2fpt, want the most lines a 1\" bottom margin allows", paper.name, last)
		}
		if paper.columnsAt(12) != paper.columns {
			t.Errorf("%s: columnsAt(12) = %d, want the paper's own %d", paper.name, paper.columnsAt(12), paper.columns)
		}
		// The speech columns are fixed in characters: every sheet must hold them.
		for _, column := range []int{fountainIndentChar + fountainWidthCue, fountainIndentPar + fountainWidthParen, fountainIndentDia + fountainWidthDia} {
			if column > paper.columns {
				t.Errorf("%s: a speech column ends at %d, past the %d-character text block", paper.name, column, paper.columns)
			}
		}
	}
	if pdfPaperA4.columns != 57 || pdfPaperA4.linesPerPage != 59 {
		t.Fatalf("A4 = %d × %d, want 57 characters × 59 lines", pdfPaperA4.columns, pdfPaperA4.linesPerPage)
	}
}

func TestDocumentPaperFor(t *testing.T) {
	for name, want := range map[string]string{"": "letter", "letter": "letter", "LETTER": "letter", " a4 ": "a4", "A4": "a4"} {
		paper, err := documentPaperFor(name)
		if err != nil || paper.name != want {
			t.Errorf("documentPaperFor(%q) = %q, %v; want %q", name, paper.name, err, want)
		}
	}
	if _, err := documentPaperFor("legal"); err == nil || !strings.Contains(err.Error(), `"letter" or "a4"`) {
		t.Fatalf("documentPaperFor(legal) = %v, want the choices named", err)
	}
}

func TestLocaleRegion(t *testing.T) {
	cases := map[string]string{
		"en_US@rg=inzzzz":                    "IN", // macOS: a US-English UI with the India region
		"en_GB@rg=uszzzz":                    "US", // the override wins either way
		"en_US@calendar=gregorian;rg=dezzzz": "DE",
		"en_IN":                              "IN",
		"en-GB":                              "GB",
		"fr_CA":                              "CA",
		"zh-Hans_CN":                         "CN", // a script subtag is skipped
		"sr_Latn_RS":                         "RS",
		"en_US_POSIX":                        "US",
		"de_DE.UTF-8":                        "DE", // a POSIX codeset
		"de_DE.UTF-8@euro":                   "DE", // and a POSIX modifier
		"es_419":                             "",   // a multi-country area names no paper
		"en":                                 "",
		"C":                                  "",
		"POSIX":                              "",
		"":                                   "",
	}
	for identifier, want := range cases {
		if got := localeRegion(identifier); got != want {
			t.Errorf("localeRegion(%q) = %q, want %q", identifier, got, want)
		}
	}
}

// The Print block's starting paper follows the region, never the language:
// the region override decides, Letter where CLDR says so, A4 elsewhere, and
// Letter when nothing can be read (TestMain's pinned state).
func TestDefaultDocumentPaperSizeFollowsTheRegion(t *testing.T) {
	if got := defaultDocumentPaperSize(); got != documentPaperLetter {
		t.Fatalf("with no locale = %q, want letter", got)
	}
	t.Cleanup(func() { systemLocaleIdentifier = func() string { return "" } })
	for identifier, want := range map[string]string{
		"en_US@rg=inzzzz": "a4",
		"en_IN":           "a4",
		"de_DE.UTF-8":     "a4",
		"en_GB":           "a4",
		"en_US":           "letter",
		"fr_CA":           "letter",
		"es_MX":           "letter",
		"en_GB@rg=uszzzz": "letter",
		"en":              "letter",
	} {
		systemLocaleIdentifier = func() string { return identifier }
		if got := NewApp().DefaultDocumentPaperSize(); got != want {
			t.Errorf("locale %q: default paper = %q, want %q", identifier, got, want)
		}
	}
}

// A bad paper name fails at the binding before anything is read or a
// dialog opens.
func TestSaveDocumentExportRejectsUnknownPaper(t *testing.T) {
	_, err := NewApp().SaveDocumentExport(SaveDocumentExportRequest{Path: "/nonexistent/script.fountain", Format: "pdf", PaperSize: "legal"})
	if err == nil || !strings.Contains(err.Error(), "paper size") {
		t.Fatalf("SaveDocumentExport(legal) = %v, want the paper size refused", err)
	}
}
