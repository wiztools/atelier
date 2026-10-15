package main

import (
	"strings"
	"testing"
)

func TestParseMarkdownBlocks(t *testing.T) {
	doc := "# Title\n\nA paragraph with **bold** and a [link](https://example.com).\nContinuation line.\n\n## Sub\n\n- first item\n- second item\n  wrapped continuation\n\n1. one\n2. two\n\n> quoted line\n> more quote\n\n```go\ncode := true\n```\n\n---\n\n| A | B |\n| --- | --- |\n| 1 | 2 |\n"
	blocks := parseMarkdownBlocks([]byte(doc))

	got := make([]string, len(blocks))
	for i, block := range blocks {
		got[i] = block.kind
	}
	want := []string{
		markdownBlockHeading,
		markdownBlockParagraph,
		markdownBlockHeading,
		markdownBlockBullet,
		markdownBlockBullet,
		markdownBlockOrdered,
		markdownBlockOrdered,
		markdownBlockQuote,
		markdownBlockCode,
		markdownBlockRule,
		markdownBlockTable,
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("kinds = %v, want %v", got, want)
	}
	if blocks[0].level != 1 || blocks[2].level != 2 {
		t.Fatalf("heading levels = %d/%d, want 1/2", blocks[0].level, blocks[2].level)
	}
	// The parse keeps the paragraph's raw lines; the layout merges them
	// into flowing text (markdownPDFLines joins with spaces).
	if strings.Join(blocks[1].lines, "\n") != "A paragraph with **bold** and a [link](https://example.com).\nContinuation line." {
		t.Fatalf("paragraph merge = %q", blocks[1].lines)
	}
	if blocks[4].lines[0] != "second item" || len(blocks[4].lines) != 2 {
		t.Fatalf("list continuation = %v", blocks[4].lines)
	}
	if blocks[5].marker != "1." || blocks[6].marker != "2." {
		t.Fatalf("ordered markers = %q/%q", blocks[5].marker, blocks[6].marker)
	}
	if blocks[8].lines[0] != "code := true" {
		t.Fatalf("code verbatim = %v", blocks[8].lines)
	}
	if blocks[10].lines[0] != "| A | B |" || len(blocks[10].lines) != 2 {
		t.Fatalf("table rows = %v (the |---| separator must drop)", blocks[10].lines)
	}
}

func TestParseMarkdownBlocksStopsParagraphAtBlocks(t *testing.T) {
	doc := "text then\n# Heading\n- item\n"
	blocks := parseMarkdownBlocks([]byte(doc))
	got := make([]string, len(blocks))
	for i, block := range blocks {
		got[i] = block.kind
	}
	if strings.Join(got, "|") != "paragraph|heading|bullet" {
		t.Fatalf("kinds = %v", got)
	}
}

func TestMarkdownPlainInline(t *testing.T) {
	assertions := []struct{ in, want string }{
		{"**bold** and *italic*", "bold and italic"},
		{"`code` span", "code span"},
		{"[site](https://example.com)", "site (https://example.com)"},
		{"[docs](docs)", "docs"},
		{"![photo of a cat](cat.png)", "[image: photo of a cat]"},
		{"![](x.png)", "[image]"},
		{"~~struck~~ _under_", "struck under"},
	}
	for _, tc := range assertions {
		if got := markdownPlainInline(tc.in); got != tc.want {
			t.Fatalf("markdownPlainInline(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestMarkdownPDFShape(t *testing.T) {
	doc := "# The Document\n\nIntro paragraph with enough words to need a wrap at the sixty character column width of the page.\n\n- a bullet\n\n```\ncode line\n```\n"
	blocks := parseMarkdownBlocks([]byte(doc))
	pdf, err := markdownPDF(blocks)
	if err != nil {
		t.Fatalf("markdownPDF returned error: %v", err)
	}
	content := string(pdf)
	if !strings.HasPrefix(content, "%PDF-1.4\n") || !strings.Contains(content, "%%EOF") {
		t.Fatal("the PDF must be a complete file")
	}
	// The heading renders bold at 16pt; the body at 12.
	if !strings.Contains(content, "/F2 16 Tf") || !strings.Contains(content, "(The Document) Tj") {
		t.Fatal("the heading must render bold at the large size")
	}
	if !strings.Contains(content, "(code line) Tj") {
		t.Fatal("the code line must render verbatim")
	}
	// No title page: page 1 stays unnumbered, and a one-page doc never shows a number.
	if strings.Contains(content, "(2.) Tj") {
		t.Fatal("a one-page document must not carry a page number")
	}

	var long strings.Builder
	long.WriteString("# Long\n\n")
	for i := 0; i < 200; i++ {
		long.WriteString("Body line of the long document to force pagination past one page.\n\n")
	}
	pdf2, err := markdownPDF(parseMarkdownBlocks([]byte(long.String())))
	if err != nil {
		t.Fatalf("markdownPDF(long) returned error: %v", err)
	}
	if !strings.Contains(string(pdf2), "(2.) Tj") {
		t.Fatal("the second page must carry the 2. page number")
	}
}

func TestSaveDocumentExportMarkdown(t *testing.T) {
	config := editTestHome(t)
	markdownConversationFixture(t, config.Storage)
	app := NewApp()

	result, err := app.SaveMarkdownEdit(SaveMarkdownEditRequest{
		ConversationID:   "conv_doc",
		SourceArtifactID: "md_old",
		Format:           "markdown",
		Text:             "# Saved doc\n\nBody text.\n",
	})
	if err != nil {
		t.Fatalf("SaveMarkdownEdit returned error: %v", err)
	}
	data, extension, _, _, err := renderDocumentExport([]byte("# Saved doc\n\nBody text.\n"), result.URL, "pdf")
	if err != nil {
		t.Fatalf("renderDocumentExport(pdf) returned error: %v", err)
	}
	if extension != ".pdf" || !strings.HasPrefix(string(data), "%PDF-1.4\n") || !strings.Contains(string(data), "(Saved doc) Tj") {
		t.Fatalf("the markdown render must be the document's PDF (ext %q, %d bytes)", extension, len(data))
	}

	// FDX renders screenplays: a markdown artifact refuses with the fix.
	if _, _, _, _, err := renderDocumentExport([]byte("# doc"), result.URL, "fdx"); err == nil || !strings.Contains(err.Error(), ".fountain") {
		t.Fatalf("fdx on a markdown artifact = %v, want the .fountain guidance", err)
	}

	// The fountain path still works end to end.
	fdx, extension, _, _, err := renderDocumentExport([]byte("INT. ROOM - DAY\n\nMAYA\nHello.\n"), scriptArtifactURL(t, config), "fdx")
	if err != nil || extension != ".fdx" || !strings.Contains(string(fdx), "FinalDraft") {
		t.Fatalf("fdx render = %v, ext %q", err, extension)
	}
}

// scriptArtifactURL saves a fountain document through the editor's door and
// returns its artifact URL — the dispatch keys on the URL's extension.
func scriptArtifactURL(t *testing.T, config AppConfig) string {
	t.Helper()
	app := NewApp()
	result, err := app.SaveMarkdownEdit(SaveMarkdownEditRequest{
		ConversationID:   "conv_doc",
		SourceArtifactID: "md_old",
		Format:           "fountain",
		Text:             "INT. ROOM - DAY\n\nMAYA\nHello.\n",
	})
	if err != nil {
		t.Fatalf("SaveMarkdownEdit(fountain) returned error: %v", err)
	}
	return result.URL
}
