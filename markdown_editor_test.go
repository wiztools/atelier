package main

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMarkdownArtifactExtension(t *testing.T) {
	cases := []struct {
		content HistoryContent
		want    string
	}{
		{HistoryContent{Type: "markdown", Path: "artifacts/md_a.md", MimeType: "text/markdown"}, ".md"},
		{HistoryContent{Type: "markdown", MimeType: "text/markdown"}, ".md"},
		{HistoryContent{Type: "markdown", Path: "artifacts/md_b.txt", MimeType: "text/plain"}, ""},
		{HistoryContent{Type: "transcript", Path: "artifacts/trc_a.md", MimeType: "text/markdown"}, ""},
		{HistoryContent{Type: "image", Path: "artifacts/img_a.md"}, ""},
	}
	for _, tc := range cases {
		if got := markdownArtifactExtension(tc.content); got != tc.want {
			t.Fatalf("markdownArtifactExtension(%+v) = %q, want %q", tc.content, got, tc.want)
		}
	}
}

// markdownConversationFixture writes conv_doc with two markdown artifacts on
// assistant turns (newest-first walk material), plus a subtitle, an image,
// and conversation history around them.
func markdownConversationFixture(t *testing.T, storage ConfigStorage) {
	t.Helper()
	artifacts := filepath.Join(storage.History, "conversations", "2026/10/conv_doc", "artifacts")
	if err := os.MkdirAll(artifacts, 0755); err != nil {
		t.Fatalf("MkdirAll artifacts: %v", err)
	}
	if err := os.WriteFile(filepath.Join(artifacts, "md_old.md"), []byte("# Old draft\n\nFirst version.\n"), 0644); err != nil {
		t.Fatalf("write md artifact: %v", err)
	}
	if err := os.WriteFile(filepath.Join(artifacts, "md_new.md"), []byte("# New draft\n\nSecond version.\n"), 0644); err != nil {
		t.Fatalf("write md artifact: %v", err)
	}
	writeSearchConversation(t, storage, "2026/10/conv_doc",
		searchConversationFixture("conv_doc", "Documents", "2026-10-01T10:00:00Z"),
		searchTurnFixture("turn_000001", "assistant", "2026-10-01T10:00:00Z",
			HistoryContent{Type: "markdown", ArtifactID: "md_old", Path: "artifacts/md_old.md", MimeType: "text/markdown"},
			HistoryContent{Type: "transcript", ArtifactID: "trc_old", Path: "artifacts/trc_old.vtt", MimeType: "text/vtt"},
			HistoryContent{Type: "image", ArtifactID: "img_old", Path: "artifacts/img_old.png", MimeType: "image/png"}),
		searchTurnFixture("turn_000002", "assistant", "2026-10-01T10:05:00Z",
			HistoryContent{Type: "markdown", ArtifactID: "md_new", Path: "artifacts/md_new.md", MimeType: "text/markdown"}),
	)
}

// markdownUserTurnFixture writes a conversation whose NEWEST turn is a user
// turn carrying the given content — the record state while that turn's tools
// run, which is what the kindless explicit-first rule reads.
func markdownUserTurnFixture(t *testing.T, storage ConfigStorage, conversationID string, contents ...HistoryContent) {
	t.Helper()
	artifacts := filepath.Join(storage.History, "conversations", "2026/10/"+conversationID, "artifacts")
	if err := os.MkdirAll(artifacts, 0755); err != nil {
		t.Fatalf("MkdirAll artifacts: %v", err)
	}
	writeSearchConversation(t, storage, "2026/10/"+conversationID,
		searchConversationFixture(conversationID, "Attached", "2026-10-01T11:00:00Z"),
		searchTurnFixture("turn_000001", "assistant", "2026-10-01T10:00:00Z",
			HistoryContent{Type: "image", ArtifactID: "img_old", Path: "artifacts/img_old.png", MimeType: "image/png"}),
		searchTurnFixture("turn_000002", "user", "2026-10-01T11:00:00Z", contents...),
	)
}

func markdownDataURL(t *testing.T, text string) string {
	t.Helper()
	return "data:text/markdown;base64," + base64.StdEncoding.EncodeToString([]byte(text))
}

func TestHistoryContentForMessagePersistsMarkdowns(t *testing.T) {
	config := defaultAppConfig()
	artifactsDir := filepath.Join(t.TempDir(), "artifacts")
	message := ChatMessage{
		Role:      "user",
		Content:   "edit this please",
		Markdowns: []string{markdownDataURL(t, "# First\n\nHello\n"), markdownDataURL(t, "Second document\n")},
	}
	contents, _, err := historyContentForMessage(message, artifactsDir, config)
	if err != nil {
		t.Fatalf("historyContentForMessage returned error: %v", err)
	}
	var documents []HistoryContent
	for _, content := range contents {
		if content.Type == "markdown" {
			documents = append(documents, content)
		}
	}
	if len(documents) != 2 {
		t.Fatalf("persisted %d markdown entries, want 2: %+v", len(documents), contents)
	}
	for i, document := range documents {
		if !strings.HasPrefix(document.ArtifactID, "md_") {
			t.Fatalf("entry %d id = %q, want md_ prefix", i, document.ArtifactID)
		}
		// Every attachment normalizes to the .md spelling, whatever the
		// client's file extension was.
		if !strings.HasSuffix(document.Path, ".md") || document.MimeType != "text/markdown" {
			t.Fatalf("entry %d = %+v, want a .md text/markdown artifact", i, document)
		}
	}

	// Empty content fails the turn's persistence, not silently passes.
	if _, _, err := historyContentForMessage(ChatMessage{Markdowns: []string{markdownDataURL(t, "   \n")}}, artifactsDir, config); err == nil {
		t.Fatal("an empty document must fail persistence")
	}
	// Non-UTF-8 bytes fail too — markdown is text, not binary with a text mime.
	binary := "data:text/markdown;base64," + base64.StdEncoding.EncodeToString([]byte{0xff, 0xfe, 0x00, 0x01})
	if _, _, err := historyContentForMessage(ChatMessage{Markdowns: []string{binary}}, artifactsDir, config); err == nil {
		t.Fatal("non-UTF-8 content must fail persistence")
	}
}

func TestMarkdownAttachmentURLs(t *testing.T) {
	artifactsDir := t.TempDir()
	contents := []HistoryContent{
		{Type: "text", Text: "hi"},
		{Type: "markdown", ArtifactID: "md_a", Path: "artifacts/md_a.md", MimeType: "text/markdown"},
		{Type: "transcript", ArtifactID: "trc_a", Path: "artifacts/trc_a.vtt", MimeType: "text/vtt"},
		{Type: "image", ArtifactID: "img_a", Path: "artifacts/img_a.png", MimeType: "image/png"},
	}
	urls := markdownAttachmentURLs(contents, artifactsDir)
	if len(urls) != 1 || !strings.HasPrefix(urls[0], artifactPrefix) || !strings.HasSuffix(urls[0], "md_a.md") {
		t.Fatalf("urls = %v, want exactly the markdown artifact URL", urls)
	}
}

func TestConversationAssetsIncludeMarkdowns(t *testing.T) {
	detail := ConversationDetail{
		Conversation: HistoryConversation{ID: "conv_assets"},
		Turns: []HistoryTurn{{
			ID:   "turn_000001",
			Role: "assistant",
			Content: []HistoryContent{
				{Type: "markdown", ArtifactID: "md_doc", Path: "artifacts/md_doc.md", MimeType: "text/markdown"},
				{Type: "transcript", ArtifactID: "trc_txt", Path: "artifacts/trc_txt.txt", MimeType: "text/plain"},
				{Type: "image", ArtifactID: "img_a", Path: "artifacts/img_a.png", MimeType: "image/png"},
			},
		}},
	}
	assets := conversationAssetsFromDetail(detail)
	kinds := map[string]string{}
	for _, asset := range assets {
		kinds[asset.ID] = asset.Kind
	}
	if kinds["md_doc"] != "markdown" {
		t.Fatalf("markdown asset missing: %+v", kinds)
	}
	if kinds["img_a"] != "image" {
		t.Fatalf("image asset regressed: %+v", kinds)
	}

	// The mention index sees the same set.
	index := assetContentIndex(detail)
	if _, ok := index["md_doc"]; !ok {
		t.Fatal("asset index must resolve markdown artifacts")
	}
	if _, ok := index["trc_txt"]; ok {
		t.Fatal("asset index must not resolve plain transcripts")
	}
}

func TestResolveReferencedAssetsResolvesMarkdowns(t *testing.T) {
	storage := libraryTestStorage(t)
	markdownConversationFixture(t, storage)

	referenced := resolveReferencedAssets(storage, "conv_doc", "", []string{"md_old"})
	if len(referenced.entries) != 1 || referenced.entries[0].ArtifactID != "md_old" {
		t.Fatalf("entries = %+v, want the mentioned markdown artifact", referenced.entries)
	}
	// Documents fill no media slot: like subtitles, they are not model input.
	if len(referenced.images) != 0 || len(referenced.audios) != 0 || len(referenced.videos) != 0 {
		t.Fatalf("slots = %+v, want empty", referenced)
	}

	// A dangling id (artifact file gone) drops out entirely.
	if got := resolveReferencedAssets(storage, "conv_doc", "", []string{"md_missing"}); len(got.entries) != 0 {
		t.Fatalf("dangling id must resolve to nothing, got %+v", got.entries)
	}
}

func TestMessagesWithoutMediaStripsMarkdowns(t *testing.T) {
	messages := []ChatMessage{{Role: "user", Content: "hi", Markdowns: []string{"data:text/markdown;base64,IyBIZWxsTA=="}}}
	stripped := messagesWithoutMedia(messages)
	if len(stripped[0].Markdowns) != 0 {
		t.Fatal("markdowns must be stripped before triage like the other media bytes")
	}
	if messages[0].Markdowns == nil {
		t.Fatal("the original messages must be untouched (copy, not in-place)")
	}
}

func TestResolveMarkdownEditSource(t *testing.T) {
	config := editTestHome(t)
	markdownConversationFixture(t, config.Storage)
	app := NewApp()

	info, err := app.ResolveMarkdownEditSource("conv_doc", "md_new")
	if err != nil {
		t.Fatalf("ResolveMarkdownEditSource returned error: %v", err)
	}
	if info.ArtifactID != "md_new" || info.ConversationID != "conv_doc" || info.ConversationTitle != "Documents" {
		t.Fatalf("info identity = %+v", info)
	}
	if info.Text != "# New draft\n\nSecond version.\n" {
		t.Fatalf("text = %q", info.Text)
	}
	// "#", "New", "draft", "Second", "version." — the save path's summary
	// counts words the same way.
	if info.WordCount != 5 || info.MimeType != "text/markdown" {
		t.Fatalf("wordCount %d mime %q", info.WordCount, info.MimeType)
	}
	if !strings.HasPrefix(info.URL, artifactPrefix) || !strings.HasSuffix(info.URL, "md_new.md") {
		t.Fatalf("url = %q", info.URL)
	}

	// Non-markdown artifacts are a miss, not a fallback.
	if _, err := app.ResolveMarkdownEditSource("conv_doc", "trc_old"); err == nil {
		t.Fatal("a transcript id must not resolve as a markdown source")
	}
	if _, err := app.ResolveMarkdownEditSource("conv_doc", "img_old"); err == nil {
		t.Fatal("an image id must not resolve as a markdown source")
	}
	if _, err := app.ResolveMarkdownEditSource("conv_doc", "md_missing"); err == nil {
		t.Fatal("an unknown id must fail")
	}
	if _, err := app.ResolveMarkdownEditSource("", "md_new"); err == nil {
		t.Fatal("an empty conversation id must fail")
	}
}

func TestSaveMarkdownEdit(t *testing.T) {
	config := editTestHome(t)
	markdownConversationFixture(t, config.Storage)
	app := NewApp()

	text := "# Edited\n\nHello world.\n"
	result, err := app.SaveMarkdownEdit(SaveMarkdownEditRequest{
		ConversationID:   "conv_doc",
		SourceArtifactID: "md_old",
		Text:             text,
	})
	if err != nil {
		t.Fatalf("SaveMarkdownEdit returned error: %v", err)
	}
	if !strings.HasPrefix(result.ArtifactID, "md_") || result.WordCount != 4 {
		t.Fatalf("result = %+v", result)
	}
	if !strings.HasPrefix(result.URL, artifactPrefix) || !strings.HasSuffix(result.URL, ".md") {
		t.Fatalf("url = %q", result.URL)
	}

	// The saved artifact is the document on disk, and the summary turn records
	// the edit.
	detail, err := getConversation(config.Storage, "conv_doc")
	if err != nil {
		t.Fatalf("getConversation returned error: %v", err)
	}
	if detail.Conversation.Stats.TurnCount != 1 || detail.Conversation.Stats.ArtifactCount != 1 {
		t.Fatalf("stats = %+v, want the save's increments", detail.Conversation.Stats)
	}
	var saved *HistoryContent
	var saveTurn *HistoryTurn
	for i := len(detail.Turns) - 1; i >= 0; i-- {
		for j, content := range detail.Turns[i].Content {
			if content.ArtifactID == result.ArtifactID {
				saved = &detail.Turns[i].Content[j]
				saveTurn = &detail.Turns[i]
			}
		}
	}
	if saved == nil || saveTurn == nil {
		t.Fatal("the save turn with the new artifact is missing from history")
	}
	if saved.Type != "markdown" || !strings.HasSuffix(saved.Path, ".md") {
		t.Fatalf("saved entry = %+v, want a .md markdown artifact", saved)
	}
	if saveTurn.Role != "assistant" || len(saveTurn.Content) != 2 {
		t.Fatalf("save turn = role %q with %d entries, want an assistant turn with text + markdown", saveTurn.Role, len(saveTurn.Content))
	}
	if !strings.Contains(saveTurn.Content[0].Text, "md_old") || !strings.Contains(saveTurn.Content[0].Text, "4 words") {
		t.Fatalf("save summary = %q", saveTurn.Content[0].Text)
	}
	path, err := contentArtifactPath(config.Storage, "conv_doc", *saved)
	if err != nil {
		t.Fatalf("contentArtifactPath returned error: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read saved artifact: %v", err)
	}
	if string(data) != text {
		t.Fatalf("saved bytes differ from the edited document")
	}

	// Refusals: unknown conversation, an empty document, and a conversation
	// that is streaming.
	if _, err := app.SaveMarkdownEdit(SaveMarkdownEditRequest{ConversationID: "conv_missing", Text: text}); err == nil {
		t.Fatal("an unknown conversation must fail")
	}
	if _, err := app.SaveMarkdownEdit(SaveMarkdownEditRequest{ConversationID: "conv_doc", Text: "   "}); err == nil {
		t.Fatal("an empty document must fail")
	}
	app.streamsMu.Lock()
	app.streamConversations["req_live"] = "conv_doc"
	app.streamsMu.Unlock()
	defer func() {
		app.streamsMu.Lock()
		delete(app.streamConversations, "req_live")
		app.streamsMu.Unlock()
	}()
	if _, err := app.SaveMarkdownEdit(SaveMarkdownEditRequest{ConversationID: "conv_doc", Text: text}); err == nil {
		t.Fatal("a streaming conversation must refuse the save")
	}
}

func TestNewestEditorArtifactMarkdownKind(t *testing.T) {
	storage := libraryTestStorage(t)
	markdownConversationFixture(t, storage)

	// The markdown kind resolves the newest markdown artifact.
	kind, artifactID, err := newestEditorArtifact(storage, "conv_doc", "markdown")
	if err != nil {
		t.Fatalf("newestEditorArtifact(markdown) returned error: %v", err)
	}
	if kind != "markdown" || artifactID != "md_new" {
		t.Fatalf("got %s/%s, want markdown/md_new", kind, artifactID)
	}

	// Kindless on an assistant-latest conversation stays visual-media-only.
	kind, artifactID, err = newestEditorArtifact(storage, "conv_doc", "")
	if err != nil {
		t.Fatalf("kindless open returned error: %v", err)
	}
	if kind != "image" || artifactID != "img_old" {
		t.Fatalf("kindless got %s/%s, want image/img_old", kind, artifactID)
	}

	if _, _, err := newestEditorArtifact(storage, "conv_doc", "caption"); err == nil {
		t.Fatal("an unknown editor kind must fail")
	}
}

func TestNewestEditorArtifactPrefersUserTurnAttachments(t *testing.T) {
	storage := libraryTestStorage(t)

	// A user turn carrying one openable kind resolves within it, whatever the
	// kind — the attachment is the disambiguator.
	markdownUserTurnFixture(t, storage, "conv_md", HistoryContent{Type: "markdown", ArtifactID: "md_attached", Path: "artifacts/md_attached.md", MimeType: "text/markdown"})
	kind, artifactID, err := newestEditorArtifact(storage, "conv_md", "")
	if err != nil {
		t.Fatalf("kindless with an attached document returned error: %v", err)
	}
	if kind != "markdown" || artifactID != "md_attached" {
		t.Fatalf("got %s/%s, want markdown/md_attached", kind, artifactID)
	}

	// A subtitle attachment resolves the subtitle editor (the tier-1 gap the
	// explicit-first rule closes).
	markdownUserTurnFixture(t, storage, "conv_srt", HistoryContent{Type: "transcript", ArtifactID: "trc_attached", Path: "artifacts/trc_attached.vtt", MimeType: "text/vtt"})
	kind, artifactID, err = newestEditorArtifact(storage, "conv_srt", "")
	if err != nil {
		t.Fatalf("kindless with an attached subtitle returned error: %v", err)
	}
	if kind != "subtitle" || artifactID != "trc_attached" {
		t.Fatalf("got %s/%s, want subtitle/trc_attached", kind, artifactID)
	}

	// Two kinds in one turn are genuinely ambiguous: the call fails and names
	// them, so the planner re-asks with an explicit mode.
	markdownUserTurnFixture(t, storage, "conv_mixed",
		HistoryContent{Type: "image", ArtifactID: "img_attached", Path: "artifacts/img_attached.png", MimeType: "image/png"},
		HistoryContent{Type: "transcript", ArtifactID: "trc_mixed", Path: "artifacts/trc_mixed.vtt", MimeType: "text/vtt"})
	if _, _, err := newestEditorArtifact(storage, "conv_mixed", ""); err == nil || !strings.Contains(err.Error(), "several editor kinds") {
		t.Fatalf("mixed-kind turn error = %v, want the ambiguity refusal", err)
	}

	// A text-only user turn falls through to the legacy visual walk.
	markdownUserTurnFixture(t, storage, "conv_text", HistoryContent{Type: "text", Text: "no attachments here"})
	kind, artifactID, err = newestEditorArtifact(storage, "conv_text", "")
	if err != nil {
		t.Fatalf("kindless after a text-only user turn returned error: %v", err)
	}
	if kind != "image" || artifactID != "img_old" {
		t.Fatalf("got %s/%s, want the legacy image/img_old", kind, artifactID)
	}
}

func TestCreateEditorConversationMarkdownLaunch(t *testing.T) {
	config := newEditorLaunchTestConfig(t)
	app := NewApp()
	prompt := "Open this in the markdown editor"
	document := "# Draft\n\nBody text.\n"
	result, err := app.CreateEditorConversation(ChatRequest{
		Model: "chat-box-model",
		Messages: []ChatMessage{{
			Role:      "user",
			Content:   prompt,
			Markdowns: []string{markdownDataURL(t, document)},
		}},
	})
	if err != nil {
		t.Fatalf("CreateEditorConversation returned error: %v", err)
	}
	if len(result.MarkdownArtifactIDs) != 1 || !strings.HasPrefix(result.MarkdownArtifactIDs[0], "md_") {
		t.Fatalf("markdown artifact ids = %v, want one md_ id", result.MarkdownArtifactIDs)
	}

	content := launchTurnContent(t, config, result, result.MarkdownArtifactIDs[0])
	if content.Type != "markdown" || !strings.HasSuffix(content.Path, ".md") {
		t.Fatalf("artifact entry = %+v, want a .md markdown under artifacts/", content)
	}
	conversationPath, err := findConversationPath(config.Storage, result.ConversationID)
	if err != nil {
		t.Fatalf("findConversationPath returned error: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(conversationPath), filepath.FromSlash(content.Path)))
	if err != nil {
		t.Fatalf("read persisted artifact returned error: %v", err)
	}
	if string(data) != document {
		t.Fatalf("persisted markdown bytes differ from the attachment")
	}
}
