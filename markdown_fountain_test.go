package main

import (
	"encoding/base64"
	"path/filepath"
	"strings"
	"testing"
)

func TestMarkdownFountainPersistence(t *testing.T) {
	config := defaultAppConfig()
	artifactsDir := filepath.Join(t.TempDir(), "artifacts")

	// A .fountain attachment persists with the fountain dialect's extension
	// and wire mime; a text/markdown one stays .md.
	fountain := "data:text/x-fountain;base64," + base64.StdEncoding.EncodeToString([]byte("INT. ROOM - DAY\n"))
	contents, _, err := historyContentForMessage(ChatMessage{Markdowns: []string{fountain, markdownDataURL(t, "# note\n")}}, artifactsDir, config)
	if err != nil {
		t.Fatalf("historyContentForMessage returned error: %v", err)
	}
	var documents []HistoryContent
	for _, content := range contents {
		if content.Type == "markdown" {
			documents = append(documents, content)
		}
	}
	if len(documents) != 2 || !strings.HasSuffix(documents[0].Path, ".fountain") || !strings.HasSuffix(documents[1].Path, ".md") {
		t.Fatalf("documents = %+v, want a .fountain and a .md artifact", documents)
	}
	if markdownArtifactExtension(documents[0]) != ".fountain" {
		t.Fatalf("fountain artifact extension = %q", markdownArtifactExtension(documents[0]))
	}
}

func TestSaveMarkdownEditFountainFormat(t *testing.T) {
	config := editTestHome(t)
	markdownConversationFixture(t, config.Storage)
	app := NewApp()

	script := "INT. ROOM - DAY\n\nMAYA\nHello.\n"
	result, err := app.SaveMarkdownEdit(SaveMarkdownEditRequest{
		ConversationID:   "conv_doc",
		SourceArtifactID: "md_old",
		Format:           "fountain",
		Text:             script,
	})
	if err != nil {
		t.Fatalf("SaveMarkdownEdit(fountain) returned error: %v", err)
	}
	if !strings.HasPrefix(result.ArtifactID, "md_") || result.Format != "fountain" {
		t.Fatalf("result = %+v", result)
	}
	if !strings.HasSuffix(result.URL, ".fountain") {
		t.Fatalf("url = %q, want a .fountain artifact", result.URL)
	}
	detail, err := getConversation(config.Storage, "conv_doc")
	if err != nil {
		t.Fatalf("getConversation returned error: %v", err)
	}
	var saved *HistoryContent
	for i := len(detail.Turns) - 1; i >= 0 && saved == nil; i-- {
		for j, content := range detail.Turns[i].Content {
			if content.ArtifactID == result.ArtifactID {
				saved = &detail.Turns[i].Content[j]
			}
		}
	}
	if saved == nil || saved.MimeType != "text/x-fountain" {
		t.Fatalf("saved entry = %+v", saved)
	}
	if !strings.Contains(detail.Turns[len(detail.Turns)-1].Content[0].Text, "saved as Fountain") {
		t.Fatalf("summary = %q", detail.Turns[len(detail.Turns)-1].Content[0].Text)
	}

	// The resolve hands the dialect back; an invalid format is refused.
	info, err := app.ResolveMarkdownEditSource("conv_doc", result.ArtifactID)
	if err != nil || info.Format != "fountain" {
		t.Fatalf("resolve format = %q, err %v", info.Format, err)
	}
	if _, err := app.SaveMarkdownEdit(SaveMarkdownEditRequest{ConversationID: "conv_doc", Format: "fdx", Text: script}); err == nil {
		t.Fatal("an unknown format must fail")
	}
}
