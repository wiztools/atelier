package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"atelier/internal/document"
)

// SaveDocumentExportRequest names a saved document artifact and the export
// rendering to write — "pdf" or "fdx". IncludeNotes prints a screenplay's
// sections and synopses as gray notes, the way the preview shows them; left
// off they stay out of the page. PaperSize names the sheet the PDF prints
// on — "letter" (the default) or "a4"; the frontend remembers the last
// choice per dialect, starting a screenplay on US Letter and a markdown
// document at DefaultDocumentPaperSize (the region's office paper).
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
// exports. The artifact is parsed fresh from disk (the Fountain parser accepts any
// text, so an .md saved over as .fountain exports too), never regenerated
// from editor state.
func (a *App) SaveDocumentExport(req SaveDocumentExportRequest) (string, error) {
	format := strings.ToLower(strings.TrimSpace(req.Format))
	if format != "pdf" && format != "fdx" {
		return "", fmt.Errorf(`document export format must be "pdf" or "fdx", got %q`, req.Format)
	}
	paper, err := document.PaperFor(req.PaperSize)
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
	export, err := document.RenderExport(data, sourcePath, format, req.IncludeNotes, paper)
	if err != nil {
		return "", err
	}
	return a.saveArtifactDialog(export.Data, req.SuggestedName, "atelier-screenplay", export.Extension, "Export document", []runtime.FileFilter{
		{DisplayName: export.FilterName, Pattern: export.Pattern},
		{DisplayName: "All Files", Pattern: "*.*"},
	})
}

// DefaultDocumentPaperSize is the Print block's starting choice for a
// markdown document before the writer has made one ("letter" or "a4").
func (a *App) DefaultDocumentPaperSize() string {
	return document.DefaultPaperSize()
}
