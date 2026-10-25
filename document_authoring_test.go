package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseAuthoredDocumentTaggedFences(t *testing.T) {
	doc, err := parseAuthoredDocument("Sure, here it is!\n\n```markdown\n# Report\n\nHello.\n```\n\nHope that helps.", "write a markdown report")
	if err != nil {
		t.Fatalf("parseAuthoredDocument returned error: %v", err)
	}
	if doc.Format != documentFormatMarkdown {
		t.Fatalf("format = %q, want markdown", doc.Format)
	}
	if doc.Body != "# Report\n\nHello." {
		t.Fatalf("body = %q", doc.Body)
	}
	if doc.Words != 3 {
		t.Fatalf("words = %d, want 3", doc.Words)
	}

	doc, err = parseAuthoredDocument("```fountain\nTitle: The Test\n\nINT. ROOM - DAY\n\nAction.\n```\n", "a fountain screenplay")
	if err != nil {
		t.Fatalf("parseAuthoredDocument(fountain) returned error: %v", err)
	}
	if doc.Format != documentFormatFountain {
		t.Fatalf("format = %q, want fountain", doc.Format)
	}
	if !strings.Contains(doc.Body, "INT. ROOM - DAY") {
		t.Fatalf("body = %q", doc.Body)
	}
}

func TestParseAuthoredDocumentInnerFencesKeepOuterClose(t *testing.T) {
	// A markdown document containing its own fenced code block: the inner
	// blocks pair up inside the outer fence, so the LAST bare ``` line is the
	// outer close — the body must keep everything up to it.
	content := "```markdown\n# Report\n\n```python\nprint('hi')\n```\n\nDone.\n```\n\nFooter prose the model added anyway."
	doc, err := parseAuthoredDocument(content, "")
	if err != nil {
		t.Fatalf("parseAuthoredDocument returned error: %v", err)
	}
	if !strings.Contains(doc.Body, "print('hi')") || !strings.Contains(doc.Body, "Done.") {
		t.Fatalf("body lost inner content: %q", doc.Body)
	}
	if strings.Contains(doc.Body, "Footer prose") {
		t.Fatalf("body swallowed post-fence prose: %q", doc.Body)
	}
}

func TestParseAuthoredDocumentBareFenceUsesTaskHint(t *testing.T) {
	doc, err := parseAuthoredDocument("```\nINT. ROOM - DAY\n\nAction.\n```", "author a fountain screenplay about a heist")
	if err != nil {
		t.Fatalf("parseAuthoredDocument(bare, screenplay hint) returned error: %v", err)
	}
	if doc.Format != documentFormatFountain {
		t.Fatalf("format = %q, want fountain from the task hint", doc.Format)
	}

	doc, err = parseAuthoredDocument("```\n# Notes\n```", "draft a project report")
	if err != nil {
		t.Fatalf("parseAuthoredDocument(bare, plain hint) returned error: %v", err)
	}
	if doc.Format != documentFormatMarkdown {
		t.Fatalf("format = %q, want markdown default", doc.Format)
	}
}

func TestParseAuthoredDocumentUnterminatedFenceDeliversBody(t *testing.T) {
	doc, err := parseAuthoredDocument("```markdown\n# Report\n\nTruncated mid-generation.", "")
	if err != nil {
		t.Fatalf("parseAuthoredDocument returned error: %v", err)
	}
	if doc.Body != "# Report\n\nTruncated mid-generation." {
		t.Fatalf("body = %q", doc.Body)
	}
}

func TestParseAuthoredDocumentRejectsUnparseable(t *testing.T) {
	if _, err := parseAuthoredDocument("Here is your document, no fence though.", ""); err == nil {
		t.Fatal("parseAuthoredDocument accepted a fence-less reply")
	}
	if _, err := parseAuthoredDocument("```markdown\n   \n```", ""); err == nil {
		t.Fatal("parseAuthoredDocument accepted an empty fenced block")
	}
}

func TestDecodeTriageDecisionAcceptsDocumentMode(t *testing.T) {
	decision, err := decodeTriageDecision(`{"needsTools":false,"responseMode":"document","toolTask":"author a fountain screenplay","reason":"document ask","mediaEdit":false,"imageEdit":false}`)
	if err != nil {
		t.Fatalf("decodeTriageDecision returned error: %v", err)
	}
	if decision.ResponseMode != "document" || decision.NeedsTools {
		t.Fatalf("decision = %+v, want document mode with needsTools false", decision)
	}

	// The truncation-salvage path must recover the document mode too.
	decision, err = decodeTriageDecision(`{"needsTools": false, "responseMode": "document", "toolTask": "wri`)
	if err != nil {
		t.Fatalf("decodeTriageDecision(salvage) returned error: %v", err)
	}
	if decision.ResponseMode != "document" || decision.NeedsTools {
		t.Fatalf("salvaged decision = %+v, want document mode with needsTools false", decision)
	}
}

func TestDocumentAuthoredEvidenceNote(t *testing.T) {
	if note := documentAuthoredEvidenceNote(nil); note != "" {
		t.Fatalf("nil document produced note %q", note)
	}
	note := documentAuthoredEvidenceNote(&authoredChatDocument{Format: documentFormatFountain, Words: 420})
	if !strings.Contains(note, "Fountain") || !strings.Contains(note, "420") || !strings.Contains(note, "Do not repeat") {
		t.Fatalf("note = %q", note)
	}
}

func TestDocumentDeliveredNotice(t *testing.T) {
	if got := documentDeliveredNotice(nil); got != "" {
		t.Fatalf("nil docs produced notice %q", got)
	}
	if got := documentDeliveredNotice([]chatTurnMarkdown{{data: []byte("x"), extension: ".md"}}); !strings.Contains(got, "delivered below the reply") {
		t.Fatalf("notice = %q", got)
	}
}

func TestDocumentAuthoringRequestStripsMediaAndCarriesTask(t *testing.T) {
	engine := newHarnessEngine(defaultAppConfig())
	req := ChatRequest{
		Messages: []ChatMessage{
			{Role: "user", Content: "write the doc", Images: []string{"data:image/png;base64,x"}, Markdowns: []string{"data:text/markdown;base64,x"}},
			{Role: "assistant", Content: "earlier"},
			{Role: "user", Content: "now the report"},
		},
		Options: map[string]any{"temperature": 0.5},
	}
	authorReq := engine.documentAuthoringRequest(req, "chat-model", "ollama", "author a markdown project report")
	if !strings.Contains(authorReq.System, "fenced code block") {
		t.Fatalf("system prompt missing the protocol: %q", authorReq.System)
	}
	if !strings.Contains(authorReq.System, "author a markdown project report") {
		t.Fatalf("system prompt missing the toolTask: %q", authorReq.System)
	}
	if authorReq.Model != "chat-model" || authorReq.Provider != "ollama" {
		t.Fatalf("model/provider = %q/%q, want chat-model/ollama", authorReq.Model, authorReq.Provider)
	}
	for _, message := range authorReq.Messages {
		if len(message.Images) != 0 || len(message.Markdowns) != 0 {
			t.Fatalf("media survived the strip: %+v", message)
		}
	}
	if authorReq.Options["num_ctx"] == nil {
		t.Fatalf("options missing num_ctx: %+v", authorReq.Options)
	}
}

func TestAuthorChatDocumentRetriesParseFailure(t *testing.T) {
	var calls int
	app := NewApp()
	app.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/api/chat" {
			t.Fatalf("unexpected request to %s", req.URL.Path)
		}
		calls++
		content := "I wrote it in prose, sorry."
		if calls >= 2 {
			content = "```markdown\n# Report\n\nHello.\n```"
		}
		body, _ := json.Marshal(map[string]any{
			"model":             "chat-model",
			"message":           map[string]any{"role": "assistant", "content": content},
			"done_reason":       "stop",
			"eval_count":        12,
			"prompt_eval_count": 7,
		})
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: io.NopCloser(strings.NewReader(string(body))), Header: http.Header{}}, nil
	})

	engine := newHarnessEngine(defaultAppConfig(), app)
	run := newHarnessRun("req-1", "conv-1")
	doc := engine.authorChatDocument(context.Background(), ChatRequest{
		BaseURL:  "http://ollama.test",
		Messages: []ChatMessage{{Role: "user", Content: "write a report"}},
	}, "chat-model", "ollama", "author a markdown report", &run)
	if doc == nil {
		t.Fatal("authorChatDocument returned nil, want the retried document")
	}
	if calls != 2 {
		t.Fatalf("provider calls = %d, want 2 (initial + correction retry)", calls)
	}
	if doc.Format != documentFormatMarkdown || doc.Body != "# Report\n\nHello." || doc.Model != "chat-model" {
		t.Fatalf("document = %+v", doc)
	}
	if len(run.Steps) != 2 {
		t.Fatalf("run steps = %d, want one per attempt", len(run.Steps))
	}
	for _, step := range run.Steps {
		if step.Kind != "document" || step.Model != "chat-model" || step.Provider != "ollama" {
			t.Fatalf("step = %+v", step)
		}
		if step.PromptTokens != 7 {
			t.Fatalf("step prompt tokens = %d, want 7", step.PromptTokens)
		}
	}
	if !strings.Contains(run.Steps[0].Summary, "correction requested") {
		t.Fatalf("first step summary = %q, want a correction note", run.Steps[0].Summary)
	}
	if run.Steps[1].Status != "completed" || !strings.Contains(run.Steps[1].Summary, "authored a markdown document") {
		t.Fatalf("second step = %+v", run.Steps[1])
	}
}

func TestAuthorChatDocumentProviderErrorIsNotRetried(t *testing.T) {
	var calls int
	app := NewApp()
	app.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: http.StatusInternalServerError, Status: "500 Internal Server Error", Body: io.NopCloser(strings.NewReader("boom")), Header: http.Header{}}, nil
	})

	engine := newHarnessEngine(defaultAppConfig(), app)
	run := newHarnessRun("req-1", "conv-1")
	doc := engine.authorChatDocument(context.Background(), ChatRequest{
		BaseURL:  "http://ollama.test",
		Messages: []ChatMessage{{Role: "user", Content: "write a report"}},
	}, "chat-model", "ollama", "", &run)
	if doc != nil {
		t.Fatalf("authorChatDocument returned %+v, want nil on a provider error", doc)
	}
	if calls != 1 {
		t.Fatalf("provider calls = %d, want 1 (provider errors are not retried)", calls)
	}
	if run.Steps[0].Status != "failed" {
		t.Fatalf("step status = %q, want failed", run.Steps[0].Status)
	}
}

func TestChatDocumentAssistantTurnPersistsMarkdownArtifacts(t *testing.T) {
	root := t.TempDir()
	storage := ConfigStorage{
		Root:      filepath.Join(root, ".atelier"),
		History:   filepath.Join(root, ".atelier", "history"),
		Artifacts: filepath.Join(root, ".atelier", "history"),
	}
	config := defaultAppConfig()
	config.Storage = storage
	if err := ensureStorageDirs(storage); err != nil {
		t.Fatalf("ensureStorageDirs returned error: %v", err)
	}

	conversationID, _, _, _, err := writePendingChatConversation(config, ChatRequest{
		Model: "chat-model",
		Messages: []ChatMessage{
			{Role: "user", Content: "Write a screenplay"},
		},
	})
	if err != nil {
		t.Fatalf("writePendingChatConversation returned error: %v", err)
	}

	urls, err := appendChatAssistantTurnWithTurnMedia(config, conversationID, "Wrote it.", "", "chat-model", "ollama", "stop", chatTurnMedia{
		markdowns: []chatTurnMarkdown{
			{data: []byte("# Report\n\nHello.\n"), extension: ".md"},
			{data: []byte("INT. ROOM - DAY\n\nAction.\n"), extension: ".fountain"},
		},
	}, fallbackHarnessRun("chat-model", "stop", 0))
	if err != nil {
		t.Fatalf("appendChatAssistantTurnWithTurnMedia returned error: %v", err)
	}
	if len(urls.markdowns) != 2 {
		t.Fatalf("markdown urls = %v, want two", urls.markdowns)
	}
	for _, url := range urls.markdowns {
		if !strings.HasPrefix(url, artifactPrefix+"/") {
			t.Fatalf("url = %q, want an %s link", url, artifactPrefix)
		}
	}

	detail, err := getConversation(storage, conversationID)
	if err != nil {
		t.Fatalf("getConversation returned error: %v", err)
	}
	if len(detail.Turns) != 2 || detail.Turns[1].Role != "assistant" {
		t.Fatalf("turns = %+v, want user and assistant", detail.Turns)
	}
	if detail.Conversation.Stats.ArtifactCount != 2 {
		t.Fatalf("artifact count = %d, want 2", detail.Conversation.Stats.ArtifactCount)
	}
	tool, _ := detail.Turns[1].ProviderResponse["tool"].(map[string]any)
	if tool["name"] != "document_authoring" || tool["documentCount"] != float64(2) {
		t.Fatalf("tool metadata = %+v, want document_authoring with two documents", tool)
	}

	// The persisted contents carry the markdown identity — the same one the
	// chip renderer, assets panel, and editors key on — and the artifact files
	// exist with the dialect extensions.
	var mdContents []HistoryContent
	for _, content := range detail.Turns[1].Content {
		if content.Type == "markdown" {
			mdContents = append(mdContents, content)
		}
	}
	if len(mdContents) != 2 {
		t.Fatalf("markdown contents = %d, want 2", len(mdContents))
	}
	if !strings.HasSuffix(mdContents[0].Path, ".md") || !strings.HasSuffix(mdContents[1].Path, ".fountain") {
		t.Fatalf("paths = %q, %q — extensions must follow the dialects", mdContents[0].Path, mdContents[1].Path)
	}
	// The artifacts live under <conversation dir>/artifacts; the URL carries
	// the absolute path, so derive the conversation dir from the first one.
	absArtifact := strings.TrimPrefix(urls.markdowns[0], artifactPrefix)
	conversationDir := filepath.Dir(filepath.Dir(absArtifact))
	for _, content := range mdContents {
		path := filepath.FromSlash(content.Path)
		if !filepath.IsAbs(path) {
			path = filepath.Join(conversationDir, path)
		}
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("artifact %q missing: %v", content.Path, err)
		}
	}

	// After hydration the frontend's historyMarkdowns walk finds the chips:
	// hydrated text is the /atelier-artifact URL ending in the dialect.
	hydrated := hydrateHistoryContent(conversationDir, detail.Turns[1].Content)
	seen := 0
	for _, content := range hydrated {
		if content.Type != "markdown" {
			continue
		}
		if !strings.HasPrefix(content.Text, artifactPrefix) || (!strings.HasSuffix(content.Text, ".md") && !strings.HasSuffix(content.Text, ".fountain")) {
			t.Fatalf("hydrated markdown text = %q, want an artifact URL with the dialect extension", content.Text)
		}
		seen++
	}
	if seen != 2 {
		t.Fatalf("hydrated markdown contents = %d, want 2", seen)
	}
}
