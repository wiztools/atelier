package main

import (
	"strings"
	"testing"

	"atelier/internal/document"
)

// A bad paper name fails at the binding before anything is read or a
// dialog opens.
func TestSaveDocumentExportRejectsUnknownPaper(t *testing.T) {
	_, err := NewApp().SaveDocumentExport(SaveDocumentExportRequest{Path: "/nonexistent/script.fountain", Format: "pdf", PaperSize: "legal"})
	if err == nil || !strings.Contains(err.Error(), "paper size") {
		t.Fatalf("SaveDocumentExport(legal) = %v, want the paper size refused", err)
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
	letter, err := document.PaperFor("letter")
	if err != nil {
		t.Fatal(err)
	}
	export, err := document.RenderExport([]byte("# Saved doc\n\nBody text.\n"), result.URL, "pdf", false, letter)
	if err != nil {
		t.Fatalf("document.RenderExport(pdf) returned error: %v", err)
	}
	if export.Extension != ".pdf" || !strings.HasPrefix(string(export.Data), "%PDF-1.4\n") || !strings.Contains(string(export.Data), "(Saved doc) Tj") {
		t.Fatalf("the markdown render must be the document's PDF (ext %q, %d bytes)", export.Extension, len(export.Data))
	}

	// FDX renders screenplays: a markdown artifact refuses with the fix.
	if _, err := document.RenderExport([]byte("# doc"), result.URL, "fdx", false, letter); err == nil || !strings.Contains(err.Error(), ".fountain") {
		t.Fatalf("fdx on a markdown artifact = %v, want the .fountain guidance", err)
	}

	// The fountain path still works end to end.
	fdx, err := document.RenderExport([]byte("INT. ROOM - DAY\n\nMAYA\nHello.\n"), scriptArtifactURL(t, config), "fdx", false, letter)
	if err != nil || fdx.Extension != ".fdx" || !strings.Contains(string(fdx.Data), "FinalDraft") {
		t.Fatalf("fdx render = %v, ext %q", err, fdx.Extension)
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
