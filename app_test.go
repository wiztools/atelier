package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func waitForStreamCleanup(t *testing.T, app *App, requestID string) {
	t.Helper()
	for range 100 {
		app.streamsMu.Lock()
		_, exists := app.streams[requestID]
		app.streamsMu.Unlock()
		if !exists {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("stream %q did not clean up", requestID)
}

func harnessStepByKind(t *testing.T, steps []any, kind string) map[string]any {
	t.Helper()
	for _, step := range steps {
		typed, ok := step.(map[string]any)
		if ok && typed["kind"] == kind {
			return typed
		}
	}
	t.Fatalf("harness steps missing %q: %+v", kind, steps)
	return nil
}

func historyImagesForTest(contents []HistoryContent) []HistoryContent {
	images := []HistoryContent{}
	for _, content := range contents {
		if content.Type == "image" {
			images = append(images, content)
		}
	}
	return images
}

func historyTextForTest(contents []HistoryContent, contentType string) string {
	for _, content := range contents {
		if content.Type == contentType {
			return content.Text
		}
	}
	return ""
}

func TestNormalizeBaseURL(t *testing.T) {
	got, err := normalizeBaseURL("localhost:11434/")
	if err != nil {
		t.Fatalf("normalizeBaseURL returned error: %v", err)
	}
	if got != "http://localhost:11434" {
		t.Fatalf("normalizeBaseURL = %q, want %q", got, "http://localhost:11434")
	}
}

func TestNormalizeImagePayload(t *testing.T) {
	payload := normalizeImagePayload("iVBORw0KGgo=")
	if payload != "data:image/png;base64,iVBORw0KGgo=" {
		t.Fatalf("normalizeImagePayload = %q", payload)
	}

	dataURL := "data:image/png;base64,iVBORw0KGgo="
	if normalizeImagePayload(dataURL) != dataURL {
		t.Fatal("data URL should pass through unchanged")
	}
}

func TestCollectImagesFromSingularImageField(t *testing.T) {
	raw := []byte(`{"model":"x/z-image-turbo:latest","response":"","done_reason":"stop","image":"iVBORw0KGgo="}`)
	images := collectImagesFromJSON(raw)
	if len(images) != 1 {
		t.Fatalf("collectImagesFromJSON returned %d images, want 1", len(images))
	}
	if images[0] != "data:image/png;base64,iVBORw0KGgo=" {
		t.Fatalf("image = %q", images[0])
	}
}

func TestCompactRawResponseRedactsImageData(t *testing.T) {
	raw := []byte(`{"image":"iVBORw0KGgo="}`)
	compact := compactRawResponse(raw)
	if compact == `{"image":"iVBORw0KGgo="}` {
		t.Fatal("compact raw response should redact image data")
	}
}

func TestDecodeImagePayload(t *testing.T) {
	data, extension, err := decodeImagePayload("data:image/png;base64,iVBORw0KGgo=")
	if err != nil {
		t.Fatalf("decodeImagePayload returned error: %v", err)
	}
	if len(data) != 8 {
		t.Fatalf("decoded data length = %d, want 8", len(data))
	}
	if extension != ".png" {
		t.Fatalf("extension = %q, want .png", extension)
	}
}

func TestNormalizeImagePayloadRejectsNonImageBase64(t *testing.T) {
	if normalizeImagePayload("stop") != "" {
		t.Fatal("non-image base64 should not be treated as a renderable image")
	}
}

func TestSanitizeFilename(t *testing.T) {
	got := sanitizeFilename(`bad/name:image?.png`)
	if got != "bad-name-image-.png" {
		t.Fatalf("sanitizeFilename = %q", got)
	}
}

func TestMergeAppConfigFillsDefaults(t *testing.T) {
	config := mergeAppConfig(AppConfig{})
	if config.Version != 1 {
		t.Fatalf("version = %d, want 1", config.Version)
	}
	if config.Providers.Ollama.BaseURL != defaultOllamaBaseURL {
		t.Fatalf("baseURL = %q", config.Providers.Ollama.BaseURL)
	}
	if config.Storage.History == "" {
		t.Fatal("history storage should default")
	}
	if config.Providers.Ollama.Models.Harness != config.Providers.Ollama.Models.Chat {
		t.Fatalf("harness model = %q, want chat default %q", config.Providers.Ollama.Models.Harness, config.Providers.Ollama.Models.Chat)
	}
	if config.Prompts.System == "" {
		t.Fatal("system prompt should default")
	}
	if config.Generation.Image.Width != 768 || config.Generation.Image.Steps != 24 {
		t.Fatalf("image generation defaults = %+v", config.Generation.Image)
	}
	if config.Tools.Filesystem.Root == "" {
		t.Fatal("filesystem tool root should default")
	}
	if config.Tools.Filesystem.MaxOutputBytes <= 0 {
		t.Fatal("filesystem tool output limit should default")
	}
	if config.Tools.Filesystem.TimeoutMS <= 0 {
		t.Fatal("filesystem tool timeout should default")
	}
}

func TestMergeAppConfigNormalizesOllamaEndpoint(t *testing.T) {
	config := mergeAppConfig(AppConfig{
		Providers: ConfigProviders{
			Ollama: ConfigOllama{
				BaseURL: "localhost:11434/",
				Models: ConfigOllamaModels{
					Chat:    "chat-model",
					Harness: "harness-model",
					Image:   "image-model",
				},
			},
		},
		Prompts: ConfigPrompts{
			System: "custom",
		},
		Generation: ConfigGeneration{
			Image: ConfigImageGeneration{Width: 512, Height: 512, Steps: 8},
		},
		UI: ConfigUI{Mode: "image"},
	})
	if config.Providers.Ollama.BaseURL != "http://localhost:11434" {
		t.Fatalf("baseURL = %q", config.Providers.Ollama.BaseURL)
	}
	if config.UI.Mode != "image" {
		t.Fatalf("mode = %q", config.UI.Mode)
	}
	if config.Providers.Ollama.Models.Harness != "harness-model" {
		t.Fatalf("harness model = %q", config.Providers.Ollama.Models.Harness)
	}
}

func TestMergeStorageConfigExpandsHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	storage := mergeStorageConfig(ConfigStorage{Root: "~/.atelier"}, defaultAppConfig().Storage)
	if storage.Root != filepath.Join(home, ".atelier") {
		t.Fatalf("root = %q", storage.Root)
	}
	if storage.History != filepath.Join(home, ".atelier", "history") {
		t.Fatalf("history = %q", storage.History)
	}
}

func TestDefaultDocumentsRootUsesHomeDocuments(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", "")
	t.Setenv("XDG_DOCUMENTS_DIR", "")

	if got := defaultDocumentsRoot(); got != filepath.Join(home, "Documents") {
		t.Fatalf("defaultDocumentsRoot = %q, want home Documents", got)
	}
	config := mergeAppConfig(AppConfig{})
	if config.Tools.Filesystem.Root != filepath.Join(home, "Documents") {
		t.Fatalf("filesystem root = %q, want Documents default", config.Tools.Filesystem.Root)
	}
}

func TestDefaultDocumentsRootUsesXDGDocumentsDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DOCUMENTS_DIR", "$HOME/Docs")

	if got := defaultDocumentsRoot(); got != filepath.Join(home, "Docs") {
		t.Fatalf("defaultDocumentsRoot = %q, want XDG documents dir", got)
	}
}

func TestFilesystemToolRunCommandCapturesOutput(t *testing.T) {
	root := t.TempDir()
	tool := newFilesystemToolLayer(ConfigFilesystemTool{
		Root:           root,
		MaxOutputBytes: 1024,
		TimeoutMS:      1000,
	})

	result, err := tool.RunCommand(context.Background(), ToolCommandRequest{
		Command: "/bin/echo",
		Args:    []string{"hello atelier"},
	})
	if err != nil {
		t.Fatalf("RunCommand returned error: %v", err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("exit code = %d, stderr = %q", result.ExitCode, result.Stderr)
	}
	if strings.TrimSpace(result.Stdout) != "hello atelier" {
		t.Fatalf("stdout = %q", result.Stdout)
	}
	if result.Cwd != root {
		t.Fatalf("cwd = %q, want %q", result.Cwd, root)
	}
}

func TestFilesystemToolRejectsCwdOutsideRoot(t *testing.T) {
	tool := newFilesystemToolLayer(ConfigFilesystemTool{Root: t.TempDir()})
	_, err := tool.RunCommand(context.Background(), ToolCommandRequest{
		Command: "/bin/echo",
		Cwd:     "/",
	})
	if err == nil {
		t.Fatal("RunCommand should reject cwd outside root")
	}
}

func TestFilesystemToolTruncatesCommandOutput(t *testing.T) {
	root := t.TempDir()
	tool := newFilesystemToolLayer(ConfigFilesystemTool{
		Root:           root,
		MaxOutputBytes: 4,
		TimeoutMS:      1000,
	})

	result, err := tool.RunCommand(context.Background(), ToolCommandRequest{
		Command: "/bin/echo",
		Args:    []string{"abcdef"},
	})
	if err != nil {
		t.Fatalf("RunCommand returned error: %v", err)
	}
	if !result.Truncated {
		t.Fatal("result should be marked truncated")
	}
	if result.Stdout != "abcd" {
		t.Fatalf("stdout = %q, want truncated output", result.Stdout)
	}
}

func TestFilesystemToolRejectsRecursiveDelete(t *testing.T) {
	tool := newFilesystemToolLayer(ConfigFilesystemTool{Root: t.TempDir()})
	_, err := tool.RunCommand(context.Background(), ToolCommandRequest{
		Command: "rm",
		Args:    []string{"-rf", "anything"},
	})
	if err == nil {
		t.Fatal("RunCommand should reject recursive delete")
	}
}

func TestFilesystemToolReadWriteAndListFiles(t *testing.T) {
	root := t.TempDir()
	tool := newFilesystemToolLayer(ConfigFilesystemTool{Root: root})

	writeResult, err := tool.WriteFile(ToolFileWriteRequest{
		Path:    "notes/todo.txt",
		Content: "ship tools",
	})
	if err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	if writeResult.Bytes != len("ship tools") {
		t.Fatalf("written bytes = %d", writeResult.Bytes)
	}

	readResult, err := tool.ReadFile(ToolFileReadRequest{Path: "notes/todo.txt"})
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	if readResult.Content != "ship tools" {
		t.Fatalf("content = %q", readResult.Content)
	}

	listResult, err := tool.ListFiles(ToolFileListRequest{Path: "notes"})
	if err != nil {
		t.Fatalf("ListFiles returned error: %v", err)
	}
	if len(listResult.Entries) != 1 || listResult.Entries[0].Name != "todo.txt" {
		t.Fatalf("entries = %+v", listResult.Entries)
	}
}

func TestFilesystemToolRejectsFileOutsideRoot(t *testing.T) {
	tool := newFilesystemToolLayer(ConfigFilesystemTool{Root: t.TempDir()})
	_, err := tool.WriteFile(ToolFileWriteRequest{
		Path:    "../outside.txt",
		Content: "nope",
	})
	if err == nil {
		t.Fatal("WriteFile should reject paths outside root")
	}
}

func TestResolveToolPermissionSignalsDecision(t *testing.T) {
	app := NewApp()
	response := make(chan bool, 1)
	app.permissionsMu.Lock()
	app.permissions["permission-1"] = response
	app.permissionsMu.Unlock()

	if err := app.ResolveToolPermission("permission-1", true); err != nil {
		t.Fatalf("ResolveToolPermission returned error: %v", err)
	}
	select {
	case approved := <-response:
		if !approved {
			t.Fatal("permission decision = false, want true")
		}
	case <-time.After(time.Second):
		t.Fatal("permission decision was not signaled")
	}
	app.permissionsMu.Lock()
	_, exists := app.permissions["permission-1"]
	app.permissionsMu.Unlock()
	if exists {
		t.Fatal("permission should be removed after decision")
	}
}

func TestResolveToolPermissionRejectsMissingRequest(t *testing.T) {
	app := NewApp()
	if err := app.ResolveToolPermission("missing", true); err == nil {
		t.Fatal("ResolveToolPermission should reject missing request")
	}
}

func TestParseHarnessToolPlanAcceptsNoToolDecision(t *testing.T) {
	plan, errors := parseHarnessToolPlan("```json\n{\"brief\":\"Answer from conversation.\",\"needsTools\":false,\"reason\":\"No filesystem context is needed.\",\"toolCalls\":[]}\n```")
	if len(errors) > 0 {
		t.Fatalf("validation errors = %+v", errors)
	}
	if plan.NeedsTools {
		t.Fatal("needsTools = true, want false")
	}
	if len(plan.ToolCalls) != 0 {
		t.Fatalf("toolCalls = %+v, want empty", plan.ToolCalls)
	}
}

func TestParseHarnessToolPlanAcceptsToolDecision(t *testing.T) {
	plan, errors := parseHarnessToolPlan("```json\n{\"brief\":\"Read status.\",\"needsTools\":true,\"reason\":\"Need current file contents.\",\"toolCalls\":[{\"name\":\"read_file\",\"path\":\"status.txt\",\"maxBytes\":20000}]}\n```")
	if len(errors) > 0 {
		t.Fatalf("validation errors = %+v", errors)
	}
	if !plan.NeedsTools || len(plan.ToolCalls) != 1 {
		t.Fatalf("plan = %+v, want one tool call", plan)
	}
	if plan.ToolCalls[0].Name != "read_file" || plan.ToolCalls[0].Path != "status.txt" {
		t.Fatalf("tool call = %+v", plan.ToolCalls[0])
	}
}

func TestParseHarnessToolPlanRejectsInvalidToolName(t *testing.T) {
	_, errors := parseHarnessToolPlan("```json\n{\"brief\":\"Do it.\",\"needsTools\":true,\"reason\":\"Need a tool.\",\"toolCalls\":[{\"name\":\"delete_all\",\"path\":\".\"}]}\n```")
	if !containsSubstring(errors, "name must be one of") {
		t.Fatalf("validation errors = %+v, want invalid tool name", errors)
	}
}

func TestParseHarnessToolPlanRejectsMissingRequiredFields(t *testing.T) {
	_, errors := parseHarnessToolPlan("```json\n{\"brief\":\"Read it.\",\"needsTools\":true,\"reason\":\"Need file.\",\"toolCalls\":[{\"name\":\"read_file\"}]}\n```")
	if !containsSubstring(errors, "path is required") {
		t.Fatalf("validation errors = %+v, want missing path", errors)
	}
}

func TestParseHarnessToolPlanRejectsInconsistentToolDecision(t *testing.T) {
	_, errors := parseHarnessToolPlan("```json\n{\"brief\":\"No tools.\",\"needsTools\":false,\"reason\":\"No tools needed.\",\"toolCalls\":[{\"name\":\"list_files\",\"path\":\".\"}]}\n```")
	if !containsSubstring(errors, "toolCalls must be empty") {
		t.Fatalf("validation errors = %+v, want inconsistent needsTools error", errors)
	}
}

func TestFilesystemToolRegistryProjectsPromptAndValidationNames(t *testing.T) {
	registry := filesystemToolRegistry()
	catalog := registry.PromptCatalog()
	names := registry.NamesCSV()
	for _, definition := range registry.definitions {
		if !strings.Contains(catalog, definition.Name) {
			t.Fatalf("prompt catalog = %q, want tool name %q", catalog, definition.Name)
		}
		if !strings.Contains(catalog, definition.Example) {
			t.Fatalf("prompt catalog = %q, want example %q", catalog, definition.Example)
		}
		if !strings.Contains(names, definition.Name) {
			t.Fatalf("names = %q, want tool name %q", names, definition.Name)
		}
		if definition.Execute == nil {
			t.Fatalf("tool %q missing executor", definition.Name)
		}
		if definition.RequiresPermission() && definition.Permission == nil {
			t.Fatalf("tool %q requires permission but has no permission presenter", definition.Name)
		}
		if definition.Activity == nil {
			t.Fatalf("tool %q missing activity projector", definition.Name)
		}
	}
	_, errors := parseHarnessToolPlan("```json\n{\"brief\":\"Do it.\",\"needsTools\":true,\"reason\":\"Need a tool.\",\"toolCalls\":[{\"name\":\"delete_all\",\"path\":\".\"}]}\n```")
	if !containsSubstring(errors, "name must be one of "+names) {
		t.Fatalf("validation errors = %+v, want registry names %q", errors, names)
	}
}

func containsSubstring(values []string, substring string) bool {
	for _, value := range values {
		if strings.Contains(value, substring) {
			return true
		}
	}
	return false
}

func TestOllamaClientChecksStatusAndListsModels(t *testing.T) {
	client := newOllamaClient(&http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			switch req.URL.Path {
			case "/api/version":
				return &http.Response{
					StatusCode: http.StatusOK,
					Status:     "200 OK",
					Body:       io.NopCloser(strings.NewReader(`{"version":"1.2.3"}`)),
					Header:     http.Header{"Content-Type": []string{"application/json"}},
				}, nil
			case "/api/tags":
				return &http.Response{
					StatusCode: http.StatusOK,
					Status:     "200 OK",
					Body: io.NopCloser(strings.NewReader(`{"models":[{
						"name":"llava:7b",
						"modified_at":"2026-01-01T00:00:00Z",
						"size":123,
						"details":{"family":"llama","parameter_size":"7B"}
					},{
						"name":"x/flux2-klein:4b",
						"modified_at":"2026-01-02T00:00:00Z",
						"size":456,
						"details":{"family":"flux","parameter_size":"4B"}
					}]}`)),
					Header: http.Header{"Content-Type": []string{"application/json"}},
				}, nil
			case "/api/show":
				data, _ := io.ReadAll(req.Body)
				if strings.Contains(string(data), "x/flux2-klein:4b") {
					return &http.Response{
						StatusCode: http.StatusOK,
						Status:     "200 OK",
						Body:       io.NopCloser(strings.NewReader(`{"capabilities":["completion","image-generation"],"model_info":{"architecture":"diffusion"}}`)),
						Header:     http.Header{"Content-Type": []string{"application/json"}},
					}, nil
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Status:     "200 OK",
					Body:       io.NopCloser(strings.NewReader(`{"capabilities":["completion","vision"],"model_info":{"gemma3.mm.tokens_per_image":256}}`)),
					Header:     http.Header{"Content-Type": []string{"application/json"}},
				}, nil
			default:
				return &http.Response{
					StatusCode: http.StatusNotFound,
					Status:     "404 Not Found",
					Body:       io.NopCloser(strings.NewReader("not found")),
					Header:     http.Header{},
				}, nil
			}
		}),
	}, "http://ollama.test")

	status := client.Check(context.Background())
	if !status.Online || status.Version != "1.2.3" || status.BaseURL != "http://ollama.test" {
		t.Fatalf("status = %+v, want online 1.2.3 at test endpoint", status)
	}

	models, err := client.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels returned error: %v", err)
	}
	if len(models) != 2 || models[0].Name != "llava:7b" || models[0].Family != "llama" || models[0].Parameter != "7B" {
		t.Fatalf("models = %+v, want parsed ollama model details", models)
	}
	if models[0].ImageGeneration {
		t.Fatalf("vision model should not be marked as image generation: %+v", models[0])
	}
	if !models[1].ImageGeneration {
		t.Fatalf("image-generation model should be marked: %+v", models[1])
	}
}

func TestEnsureStorageDirs(t *testing.T) {
	root := t.TempDir()
	storage := ConfigStorage{
		Root:      filepath.Join(root, ".atelier"),
		History:   filepath.Join(root, ".atelier", "history"),
		Artifacts: filepath.Join(root, ".atelier", "history"),
	}
	if err := ensureStorageDirs(storage); err != nil {
		t.Fatalf("ensureStorageDirs returned error: %v", err)
	}
	for _, path := range []string{
		storage.Root,
		filepath.Join(storage.History, "conversations"),
		filepath.Join(storage.History, "indexes"),
	} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("expected %s to exist: %v", path, err)
		}
		if !info.IsDir() {
			t.Fatalf("%s is not a directory", path)
		}
	}
}

func TestImageGenerationConversationLifecycle(t *testing.T) {
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

	conversationID, err := writeImageGenerationConversation(
		config,
		ImageGenerateRequest{Model: "image-model", Prompt: "Paint a small house", Width: 64, Height: 64, Steps: 2},
		ollamaGenerateResponse{Model: "image-model", Done: true},
		[]string{"data:image/png;base64,iVBORw0KGgo="},
		"{}",
	)
	if err != nil {
		t.Fatalf("writeImageGenerationConversation returned error: %v", err)
	}

	conversations, err := listConversations(storage)
	if err != nil {
		t.Fatalf("listConversations returned error: %v", err)
	}
	if len(conversations) != 1 {
		t.Fatalf("conversation count = %d, want 1", len(conversations))
	}
	if conversations[0].ID != conversationID {
		t.Fatalf("conversation id = %q, want %q", conversations[0].ID, conversationID)
	}

	if err := deleteConversation(storage, conversationID); err != nil {
		t.Fatalf("deleteConversation returned error: %v", err)
	}
	conversations, err = listConversations(storage)
	if err != nil {
		t.Fatalf("listConversations after delete returned error: %v", err)
	}
	if len(conversations) != 0 {
		t.Fatalf("conversation count after delete = %d, want 0", len(conversations))
	}
}

func TestImageGenerationPendingConversationLifecycle(t *testing.T) {
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

	req := ImageGenerateRequest{Model: "image-model", Prompt: "Paint early", Width: 64, Height: 64, Steps: 2}
	conversationID, err := writePendingImageGenerationConversation(config, req)
	if err != nil {
		t.Fatalf("writePendingImageGenerationConversation returned error: %v", err)
	}
	conversations, err := listConversations(storage)
	if err != nil {
		t.Fatalf("listConversations returned error: %v", err)
	}
	if len(conversations) != 1 || conversations[0].ID != conversationID {
		t.Fatalf("conversations = %+v, want pending image conversation %s", conversations, conversationID)
	}
	detail, err := getConversation(storage, conversationID)
	if err != nil {
		t.Fatalf("getConversation returned error: %v", err)
	}
	if len(detail.Turns) != 1 || detail.Turns[0].Role != "user" {
		t.Fatalf("turns after pending write = %+v, want one user turn", detail.Turns)
	}

	err = appendImageGenerationResult(
		config,
		conversationID,
		req,
		ollamaGenerateResponse{Model: "image-model", Done: true},
		[]string{"data:image/png;base64,iVBORw0KGgo="},
		"{}",
	)
	if err != nil {
		t.Fatalf("appendImageGenerationResult returned error: %v", err)
	}
	detail, err = getConversation(storage, conversationID)
	if err != nil {
		t.Fatalf("getConversation after result returned error: %v", err)
	}
	if len(detail.Turns) != 2 || detail.Turns[1].Role != "assistant" {
		t.Fatalf("turns after result = %+v, want user and assistant", detail.Turns)
	}
	if detail.Conversation.Stats.ArtifactCount != 1 {
		t.Fatalf("artifact count = %d, want 1", detail.Conversation.Stats.ArtifactCount)
	}
}

func TestHistoryAppendRejectsWrongConversationKind(t *testing.T) {
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

	imageConversationID, err := writePendingImageGenerationConversation(
		config,
		ImageGenerateRequest{Model: "image-model", Prompt: "Paint early", Width: 64, Height: 64, Steps: 2},
	)
	if err != nil {
		t.Fatalf("writePendingImageGenerationConversation returned error: %v", err)
	}
	if _, err := appendChatUserTurn(config, ChatRequest{
		ConversationID: imageConversationID,
		Model:          "chat-model",
		Messages: []ChatMessage{
			{Role: "user", Content: "This should stay chat-only"},
		},
	}); err == nil || !strings.Contains(err.Error(), "not a chat conversation") {
		t.Fatalf("appendChatUserTurn error = %v, want wrong-kind chat error", err)
	}

	chatConversationID, err := writePendingChatConversation(config, ChatRequest{
		Model: "chat-model",
		Messages: []ChatMessage{
			{Role: "user", Content: "Start a chat"},
		},
	})
	if err != nil {
		t.Fatalf("writePendingChatConversation returned error: %v", err)
	}
	err = appendImageGenerationResult(
		config,
		chatConversationID,
		ImageGenerateRequest{Model: "image-model", Prompt: "Paint late", Width: 64, Height: 64, Steps: 2},
		ollamaGenerateResponse{Model: "image-model", Done: true},
		[]string{"data:image/png;base64,iVBORw0KGgo="},
		"{}",
	)
	if err == nil || !strings.Contains(err.Error(), "not an image conversation") {
		t.Fatalf("appendImageGenerationResult error = %v, want wrong-kind image error", err)
	}
}

func TestPurgeArchivedConversationsRemovesOnlySoftDeletedFolders(t *testing.T) {
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

	archivedID, err := writeImageGenerationConversation(
		config,
		ImageGenerateRequest{Model: "image-model", Prompt: "Archive me", Width: 64, Height: 64, Steps: 2},
		ollamaGenerateResponse{Model: "image-model", Done: true},
		[]string{"data:image/png;base64,iVBORw0KGgo="},
		"{}",
	)
	if err != nil {
		t.Fatalf("write archived conversation returned error: %v", err)
	}
	activeID, err := writeImageGenerationConversation(
		config,
		ImageGenerateRequest{Model: "image-model", Prompt: "Keep me", Width: 64, Height: 64, Steps: 2},
		ollamaGenerateResponse{Model: "image-model", Done: true},
		[]string{"data:image/png;base64,iVBORw0KGgo="},
		"{}",
	)
	if err != nil {
		t.Fatalf("write active conversation returned error: %v", err)
	}
	archivedPath, err := findConversationPath(storage, archivedID)
	if err != nil {
		t.Fatalf("find archived conversation returned error: %v", err)
	}
	activePath, err := findConversationPath(storage, activeID)
	if err != nil {
		t.Fatalf("find active conversation returned error: %v", err)
	}
	archivedDir := filepath.Dir(archivedPath)
	activeDir := filepath.Dir(activePath)

	if err := deleteConversation(storage, archivedID); err != nil {
		t.Fatalf("deleteConversation returned error: %v", err)
	}
	result, err := purgeArchivedConversations(storage)
	if err != nil {
		t.Fatalf("purgeArchivedConversations returned error: %v", err)
	}
	if result.DeletedConversations != 1 {
		t.Fatalf("deleted conversations = %d, want 1", result.DeletedConversations)
	}
	if result.DeletedAssets != 1 {
		t.Fatalf("deleted assets = %d, want 1", result.DeletedAssets)
	}
	if _, err := os.Stat(archivedDir); !os.IsNotExist(err) {
		t.Fatalf("archived dir still exists or stat failed differently: %v", err)
	}
	if _, err := os.Stat(activeDir); err != nil {
		t.Fatalf("active dir stat returned error: %v", err)
	}
}

func TestChatConversationLifecycle(t *testing.T) {
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

	conversationID, err := writeChatConversation(
		config,
		ChatRequest{
			Model:  "chat-model",
			System: "Be useful.",
			Messages: []ChatMessage{
				{Role: "user", Content: "Explain markdown tables"},
			},
		},
		"Here is a table.",
		"",
		"chat-model",
		"stop",
		12,
		"Markdown Tables",
	)
	if err != nil {
		t.Fatalf("writeChatConversation returned error: %v", err)
	}

	conversations, err := listConversations(storage)
	if err != nil {
		t.Fatalf("listConversations returned error: %v", err)
	}
	if len(conversations) != 1 {
		t.Fatalf("conversation count = %d, want 1", len(conversations))
	}
	if conversations[0].ID != conversationID {
		t.Fatalf("conversation id = %q, want %q", conversations[0].ID, conversationID)
	}
	if conversations[0].Kind != "chat" {
		t.Fatalf("conversation kind = %q, want chat", conversations[0].Kind)
	}
	if conversations[0].Title != "Markdown Tables" {
		t.Fatalf("conversation title = %q, want Markdown Tables", conversations[0].Title)
	}

	updated, err := updateConversationTitle(storage, conversationID, "Edited Title")
	if err != nil {
		t.Fatalf("updateConversationTitle returned error: %v", err)
	}
	if updated.Title != "Edited Title" {
		t.Fatalf("updated title = %q, want Edited Title", updated.Title)
	}

	detail, err := getConversation(storage, conversationID)
	if err != nil {
		t.Fatalf("getConversation returned error: %v", err)
	}
	if detail.Conversation.ID != conversationID {
		t.Fatalf("detail conversation id = %q, want %q", detail.Conversation.ID, conversationID)
	}
	if len(detail.Turns) != 2 {
		t.Fatalf("detail turn count = %d, want 2", len(detail.Turns))
	}
	if detail.Turns[0].Role != "user" || detail.Turns[1].Role != "assistant" {
		t.Fatalf("turn roles = %q/%q, want user/assistant", detail.Turns[0].Role, detail.Turns[1].Role)
	}
	harnessRun, ok := detail.Turns[1].ProviderResponse["harnessRun"].(map[string]any)
	if !ok {
		t.Fatalf("assistant provider response missing harness run: %+v", detail.Turns[1].ProviderResponse)
	}
	if harnessRun["mode"] != "chat" || harnessRun["status"] != "completed" {
		t.Fatalf("harness run = %+v, want completed chat run", harnessRun)
	}
	loop, ok := harnessRun["loop"].(map[string]any)
	if !ok {
		t.Fatalf("harness run missing loop metadata: %+v", harnessRun)
	}
	if loop["maxSteps"] != float64(3) || loop["iterations"] != float64(1) || loop["stopReason"] != "final" {
		t.Fatalf("harness loop = %+v, want final single-iteration loop", loop)
	}
	steps, ok := harnessRun["steps"].([]any)
	if !ok || len(steps) != 6 {
		t.Fatalf("harness steps = %+v, want full lifecycle timeline", harnessRun["steps"])
	}
	evaluation := harnessStepByKind(t, steps, "evaluation")
	if evaluation["decision"] != "final" {
		t.Fatalf("evaluation step = %+v, want final evaluation", evaluation)
	}
	if len(detail.Turns[0].Content) != 1 || detail.Turns[0].Content[0].Text != "Explain markdown tables" {
		t.Fatalf("initial user content = %+v, want text prompt", detail.Turns[0].Content)
	}

	appendedID, err := appendChatConversation(
		config,
		ChatRequest{
			ConversationID: conversationID,
			Model:          "chat-model",
			Messages: []ChatMessage{
				{Role: "user", Content: "Add one more example"},
			},
		},
		"Here is another example.",
		"",
		"chat-model",
		"stop",
		8,
	)
	if err != nil {
		t.Fatalf("appendChatConversation returned error: %v", err)
	}
	if appendedID != conversationID {
		t.Fatalf("appended id = %q, want %q", appendedID, conversationID)
	}
	detail, err = getConversation(storage, conversationID)
	if err != nil {
		t.Fatalf("getConversation after append returned error: %v", err)
	}
	if len(detail.Turns) != 4 {
		t.Fatalf("detail turn count after append = %d, want 4", len(detail.Turns))
	}
	if detail.Conversation.Stats.TurnCount != 4 {
		t.Fatalf("conversation turn count after append = %d, want 4", detail.Conversation.Stats.TurnCount)
	}
}

func TestHarnessRunChatStreamRecordsHistory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	config := defaultAppConfig()
	config.Storage = ConfigStorage{
		Root:      filepath.Join(home, ".atelier"),
		History:   filepath.Join(home, ".atelier", "history"),
		Artifacts: filepath.Join(home, ".atelier", "history"),
	}
	config.Providers.Ollama.BaseURL = "http://ollama.test"
	config.Providers.Ollama.Models.Chat = "chat-box-model"
	config.Providers.Ollama.Models.Harness = "harness-model"
	if err := writeAppConfig(config); err != nil {
		t.Fatalf("writeAppConfig returned error: %v", err)
	}

	var requestedModels []string
	app := NewApp()
	app.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/api/chat" {
			return &http.Response{
				StatusCode: http.StatusNotFound,
				Status:     "404 Not Found",
				Body:       io.NopCloser(strings.NewReader("not found")),
				Header:     http.Header{},
			}, nil
		}
		var payload map[string]any
		data, _ := io.ReadAll(req.Body)
		if err := json.Unmarshal(data, &payload); err != nil {
			t.Fatalf("provider request body is not JSON: %v", err)
		}
		requestedModel, _ := payload["model"].(string)
		requestedModels = append(requestedModels, requestedModel)
		if payload["stream"] == false {
			if requestedModel != "harness-model" {
				t.Fatalf("harness prep model = %q, want harness-model", requestedModel)
			}
			body := `{"model":"harness-model","message":{"role":"assistant","content":"Answer directly and warmly."},"done":true,"done_reason":"stop","eval_count":2}`
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Body:       io.NopCloser(strings.NewReader(body)),
				Header:     http.Header{"Content-Type": []string{"application/json"}},
			}, nil
		}
		if requestedModel != "chat-box-model" {
			t.Fatalf("response stream model = %q, want chat-box-model", requestedModel)
		}
		body := fmt.Sprintln(`{"model":"chat-box-model","message":{"role":"assistant","content":"Hello from selected chat model.","thinking":"Final model thought."},"done":false}`) +
			fmt.Sprintln(`{"model":"chat-box-model","done":true,"done_reason":"stop","eval_count":3}`)
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     http.Header{"Content-Type": []string{"application/x-ndjson"}},
		}, nil
	})
	app.runChatStream(context.Background(), "request-1", ChatRequest{
		BaseURL: "http://ollama.test",
		Model:   "chat-box-model",
		Messages: []ChatMessage{
			{Role: "user", Content: "Say hello"},
		},
	})
	if strings.Join(requestedModels, ",") != "harness-model,chat-box-model" {
		t.Fatalf("provider request models = %v, want harness then selected chat model", requestedModels)
	}

	conversations, err := listConversations(config.Storage)
	if err != nil {
		t.Fatalf("listConversations returned error: %v", err)
	}
	if len(conversations) != 1 {
		t.Fatalf("conversation count = %d, want 1", len(conversations))
	}
	detail, err := getConversation(config.Storage, conversations[0].ID)
	if err != nil {
		t.Fatalf("getConversation returned error: %v", err)
	}
	if got := detail.Turns[1].Content[0].Text; got != "Hello from selected chat model." {
		t.Fatalf("assistant content = %q, want streamed content", got)
	}
	if got := detail.Turns[0].Request["model"]; got != "harness-model" {
		t.Fatalf("user turn request model = %q, want harness-model", got)
	}
	if got := detail.Turns[0].Request["selectedModel"]; got != "chat-box-model" {
		t.Fatalf("user turn selected model = %q, want chat-box-model", got)
	}
	if got := detail.Turns[1].Model; got != "chat-box-model" {
		t.Fatalf("assistant turn model = %q, want chat-box-model", got)
	}
	if thinking := historyTextForTest(detail.Turns[1].Content, "thinking"); !strings.Contains(thinking, "Harness preparation") || !strings.Contains(thinking, "Final model thought.") {
		t.Fatalf("assistant thinking = %q, want harness prep and final model thinking", thinking)
	}
	harnessRun, ok := detail.Turns[1].ProviderResponse["harnessRun"].(map[string]any)
	if !ok {
		t.Fatalf("assistant provider response missing harness run: %+v", detail.Turns[1].ProviderResponse)
	}
	steps, ok := harnessRun["steps"].([]any)
	if !ok || len(steps) != 6 {
		t.Fatalf("harness steps = %+v, want full lifecycle timeline", harnessRun["steps"])
	}
	streaming := harnessStepByKind(t, steps, "streaming")
	if streaming["status"] != "completed" || streaming["tokens"] != float64(3) {
		t.Fatalf("streaming step = %+v, want completed streaming metadata", streaming)
	}
}

func TestHarnessExecutesFilesystemToolBeforeSelectedModel(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	config := defaultAppConfig()
	config.Storage = ConfigStorage{
		Root:      filepath.Join(home, ".atelier"),
		History:   filepath.Join(home, ".atelier", "history"),
		Artifacts: filepath.Join(home, ".atelier", "history"),
	}
	config.Tools.Filesystem.Root = filepath.Join(home, "tool-root")
	config.Providers.Ollama.BaseURL = "http://ollama.test"
	config.Providers.Ollama.Models.Chat = "chat-box-model"
	config.Providers.Ollama.Models.Harness = "harness-model"
	if err := os.MkdirAll(config.Tools.Filesystem.Root, 0755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(config.Tools.Filesystem.Root, "status.txt"), []byte("Project status: green"), 0644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	if err := writeAppConfig(config); err != nil {
		t.Fatalf("writeAppConfig returned error: %v", err)
	}

	app := NewApp()
	var responseSystem string
	prepCalls := 0
	app.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/api/chat" {
			return &http.Response{
				StatusCode: http.StatusNotFound,
				Status:     "404 Not Found",
				Body:       io.NopCloser(strings.NewReader("not found")),
				Header:     http.Header{},
			}, nil
		}
		var payload map[string]any
		data, _ := io.ReadAll(req.Body)
		if err := json.Unmarshal(data, &payload); err != nil {
			t.Fatalf("provider request body is not JSON: %v", err)
		}
		if payload["stream"] == false {
			prepCalls++
			body := "{\"model\":\"harness-model\",\"message\":{\"role\":\"assistant\",\"content\":\"```json\\n{\\\"brief\\\":\\\"Use the status file to answer.\\\",\\\"needsTools\\\":true,\\\"reason\\\":\\\"The user asks for project status from the workspace.\\\",\\\"toolCalls\\\":[{\\\"name\\\":\\\"read_file\\\",\\\"path\\\":\\\"status.txt\\\"}]}\\n```\"},\"done\":true,\"done_reason\":\"stop\",\"eval_count\":2}"
			if prepCalls > 1 {
				body = "{\"model\":\"harness-model\",\"message\":{\"role\":\"assistant\",\"content\":\"```json\\n{\\\"brief\\\":\\\"Answer that the project status is green based on the status file.\\\",\\\"needsTools\\\":false,\\\"reason\\\":\\\"The status file provided enough context.\\\",\\\"toolCalls\\\":[]}\\n```\"},\"done\":true,\"done_reason\":\"stop\",\"eval_count\":2}"
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Body:       io.NopCloser(strings.NewReader(body)),
				Header:     http.Header{"Content-Type": []string{"application/json"}},
			}, nil
		}
		messages, ok := payload["messages"].([]any)
		if !ok || len(messages) == 0 {
			t.Fatalf("stream request messages = %+v, want system handoff", payload["messages"])
		}
		systemMessage, ok := messages[0].(map[string]any)
		if !ok || systemMessage["role"] != "system" {
			t.Fatalf("first message = %+v, want system handoff", messages[0])
		}
		responseSystem, _ = systemMessage["content"].(string)
		body := fmt.Sprintln(`{"model":"chat-box-model","message":{"role":"assistant","content":"The project is green."},"done":false}`) +
			fmt.Sprintln(`{"model":"chat-box-model","done":true,"done_reason":"stop","eval_count":3}`)
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     http.Header{"Content-Type": []string{"application/x-ndjson"}},
		}, nil
	})

	app.runChatStream(context.Background(), "request-tools", ChatRequest{
		BaseURL: "http://ollama.test",
		Model:   "chat-box-model",
		Messages: []ChatMessage{
			{Role: "user", Content: "What is the project status?"},
		},
	})
	if !strings.Contains(responseSystem, "Filesystem tool observations") || !strings.Contains(responseSystem, "Project status: green") {
		t.Fatalf("response system handoff = %q, want filesystem observation", responseSystem)
	}
	if prepCalls != 2 {
		t.Fatalf("harness prep calls = %d, want initial plan plus inspection", prepCalls)
	}

	conversations, err := listConversations(config.Storage)
	if err != nil {
		t.Fatalf("listConversations returned error: %v", err)
	}
	detail, err := getConversation(config.Storage, conversations[0].ID)
	if err != nil {
		t.Fatalf("getConversation returned error: %v", err)
	}
	if got := detail.Turns[1].Content[0].Text; got != "The project is green." {
		t.Fatalf("assistant content = %q, want final model answer", got)
	}
	if thinking := historyTextForTest(detail.Turns[1].Content, "thinking"); !strings.Contains(thinking, "Tool results") || !strings.Contains(thinking, "Project status: green") {
		t.Fatalf("assistant thinking = %q, want tool results", thinking)
	}
	harnessRun, ok := detail.Turns[1].ProviderResponse["harnessRun"].(map[string]any)
	if !ok {
		t.Fatalf("assistant provider response missing harness run: %+v", detail.Turns[1].ProviderResponse)
	}
	steps, ok := harnessRun["steps"].([]any)
	if !ok {
		t.Fatalf("harness steps = %+v, want timeline", harnessRun["steps"])
	}
	toolStep := harnessStepByKind(t, steps, "tool_call")
	if toolStep["status"] != "completed" || toolStep["provider"] != "filesystem" {
		t.Fatalf("tool step = %+v, want completed filesystem call", toolStep)
	}
	activities, ok := toolStep["tools"].([]any)
	if !ok || len(activities) != 1 {
		t.Fatalf("tool activities = %+v, want one activity", toolStep["tools"])
	}
	activity, ok := activities[0].(map[string]any)
	if !ok || activity["name"] != "read_file" || activity["status"] != "completed" {
		t.Fatalf("tool activity = %+v, want completed read_file", activities[0])
	}
	if path, _ := activity["path"].(string); !strings.HasSuffix(path, "status.txt") {
		t.Fatalf("tool activity path = %q, want status.txt", path)
	}
}

func TestHarnessCanRequestSecondToolRound(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	config := defaultAppConfig()
	config.Storage = ConfigStorage{
		Root:      filepath.Join(home, ".atelier"),
		History:   filepath.Join(home, ".atelier", "history"),
		Artifacts: filepath.Join(home, ".atelier", "history"),
	}
	config.Tools.Filesystem.Root = filepath.Join(home, "tool-root")
	config.Providers.Ollama.BaseURL = "http://ollama.test"
	config.Providers.Ollama.Models.Chat = "chat-box-model"
	config.Providers.Ollama.Models.Harness = "harness-model"
	if err := os.MkdirAll(config.Tools.Filesystem.Root, 0755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(config.Tools.Filesystem.Root, "notes.txt"), []byte("Second round found this."), 0644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	if err := writeAppConfig(config); err != nil {
		t.Fatalf("writeAppConfig returned error: %v", err)
	}

	app := NewApp()
	prepCalls := 0
	var responseSystem string
	app.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/api/chat" {
			return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader("not found"))}, nil
		}
		var payload map[string]any
		data, _ := io.ReadAll(req.Body)
		if err := json.Unmarshal(data, &payload); err != nil {
			t.Fatalf("provider request body is not JSON: %v", err)
		}
		if payload["stream"] == false {
			prepCalls++
			body := "{\"model\":\"harness-model\",\"message\":{\"role\":\"assistant\",\"content\":\"```json\\n{\\\"brief\\\":\\\"List workspace files first.\\\",\\\"needsTools\\\":true,\\\"reason\\\":\\\"Need to discover file names.\\\",\\\"toolCalls\\\":[{\\\"name\\\":\\\"list_files\\\",\\\"path\\\":\\\".\\\"}]}\\n```\"},\"done\":true,\"done_reason\":\"stop\",\"eval_count\":2}"
			if prepCalls == 2 {
				body = "{\"model\":\"harness-model\",\"message\":{\"role\":\"assistant\",\"content\":\"```json\\n{\\\"brief\\\":\\\"Read notes.txt before answering.\\\",\\\"needsTools\\\":true,\\\"reason\\\":\\\"The file list revealed notes.txt.\\\",\\\"toolCalls\\\":[{\\\"name\\\":\\\"read_file\\\",\\\"path\\\":\\\"notes.txt\\\"}]}\\n```\"},\"done\":true,\"done_reason\":\"stop\",\"eval_count\":2}"
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Body:       io.NopCloser(strings.NewReader(body)),
				Header:     http.Header{"Content-Type": []string{"application/json"}},
			}, nil
		}
		messages := payload["messages"].([]any)
		systemMessage := messages[0].(map[string]any)
		responseSystem, _ = systemMessage["content"].(string)
		body := fmt.Sprintln(`{"model":"chat-box-model","message":{"role":"assistant","content":"Second round answer."},"done":false}`) +
			fmt.Sprintln(`{"model":"chat-box-model","done":true,"done_reason":"stop","eval_count":3}`)
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     http.Header{"Content-Type": []string{"application/x-ndjson"}},
		}, nil
	})

	app.runChatStream(context.Background(), "request-second-round", ChatRequest{
		BaseURL: "http://ollama.test",
		Model:   "chat-box-model",
		Messages: []ChatMessage{
			{Role: "user", Content: "Use the workspace notes"},
		},
	})
	if prepCalls != 2 {
		t.Fatalf("harness prep calls = %d, want capped two iterations", prepCalls)
	}
	if !strings.Contains(responseSystem, "Second round found this.") {
		t.Fatalf("response system handoff = %q, want second round file content", responseSystem)
	}
	conversations, err := listConversations(config.Storage)
	if err != nil {
		t.Fatalf("listConversations returned error: %v", err)
	}
	detail, err := getConversation(config.Storage, conversations[0].ID)
	if err != nil {
		t.Fatalf("getConversation returned error: %v", err)
	}
	harnessRun := detail.Turns[1].ProviderResponse["harnessRun"].(map[string]any)
	loop := harnessRun["loop"].(map[string]any)
	if loop["iterations"] != float64(2) {
		t.Fatalf("loop = %+v, want 2 iterations", loop)
	}
	toolStep := harnessStepByKind(t, harnessRun["steps"].([]any), "tool_call")
	activities := toolStep["tools"].([]any)
	if len(activities) != 2 {
		t.Fatalf("tool activities = %+v, want list and read", activities)
	}
}

func TestHarnessForcesWorkspaceListingWhenModelSkipsTools(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	config := defaultAppConfig()
	config.Storage = ConfigStorage{
		Root:      filepath.Join(home, ".atelier"),
		History:   filepath.Join(home, ".atelier", "history"),
		Artifacts: filepath.Join(home, ".atelier", "history"),
	}
	config.Tools.Filesystem.Root = filepath.Join(home, "tool-root")
	config.Providers.Ollama.BaseURL = "http://ollama.test"
	config.Providers.Ollama.Models.Chat = "chat-box-model"
	config.Providers.Ollama.Models.Harness = "harness-model"
	if err := os.MkdirAll(filepath.Join(config.Tools.Filesystem.Root, "actual-dir"), 0755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(config.Tools.Filesystem.Root, "actual.txt"), []byte("real workspace file"), 0644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	if err := writeAppConfig(config); err != nil {
		t.Fatalf("writeAppConfig returned error: %v", err)
	}

	app := NewApp()
	prepCalls := 0
	var responseSystem string
	app.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/api/chat" {
			return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader("not found"))}, nil
		}
		var payload map[string]any
		data, _ := io.ReadAll(req.Body)
		if err := json.Unmarshal(data, &payload); err != nil {
			t.Fatalf("provider request body is not JSON: %v", err)
		}
		if payload["stream"] == false {
			prepCalls++
			body := "{\"model\":\"harness-model\",\"message\":{\"role\":\"assistant\",\"content\":\"```json\\n{\\\"brief\\\":\\\"Answer from general project context.\\\",\\\"needsTools\\\":false,\\\"reason\\\":\\\"No tools are needed.\\\",\\\"toolCalls\\\":[]}\\n```\"},\"done\":true,\"done_reason\":\"stop\",\"eval_count\":2}"
			if prepCalls == 2 {
				body = "{\"model\":\"harness-model\",\"message\":{\"role\":\"assistant\",\"content\":\"```json\\n{\\\"brief\\\":\\\"Answer only from the listed workspace entries.\\\",\\\"needsTools\\\":false,\\\"reason\\\":\\\"The workspace listing is sufficient.\\\",\\\"toolCalls\\\":[]}\\n```\"},\"done\":true,\"done_reason\":\"stop\",\"eval_count\":2}"
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Body:       io.NopCloser(strings.NewReader(body)),
				Header:     http.Header{"Content-Type": []string{"application/json"}},
			}, nil
		}
		messages := payload["messages"].([]any)
		systemMessage := messages[0].(map[string]any)
		responseSystem, _ = systemMessage["content"].(string)
		body := fmt.Sprintln(`{"model":"chat-box-model","message":{"role":"assistant","content":"actual.txt and actual-dir are present."},"done":false}`) +
			fmt.Sprintln(`{"model":"chat-box-model","done":true,"done_reason":"stop","eval_count":3}`)
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     http.Header{"Content-Type": []string{"application/x-ndjson"}},
		}, nil
	})

	app.runChatStream(context.Background(), "request-workspace-list", ChatRequest{
		BaseURL: "http://ollama.test",
		Model:   "chat-box-model",
		Messages: []ChatMessage{
			{Role: "user", Content: "What is present in the workspace?"},
		},
	})
	if !strings.Contains(responseSystem, "actual.txt") || !strings.Contains(responseSystem, "actual-dir") {
		t.Fatalf("response system handoff = %q, want real workspace entries", responseSystem)
	}
	conversations, err := listConversations(config.Storage)
	if err != nil {
		t.Fatalf("listConversations returned error: %v", err)
	}
	detail, err := getConversation(config.Storage, conversations[0].ID)
	if err != nil {
		t.Fatalf("getConversation returned error: %v", err)
	}
	harnessRun := detail.Turns[1].ProviderResponse["harnessRun"].(map[string]any)
	toolStep := harnessStepByKind(t, harnessRun["steps"].([]any), "tool_call")
	activities := toolStep["tools"].([]any)
	activity := activities[0].(map[string]any)
	if activity["name"] != "list_files" || activity["status"] != "completed" {
		t.Fatalf("tool activity = %+v, want completed list_files fallback", activity)
	}
}

func TestHarnessForcesWriteFileWhenModelEmitsInvalidWritePlan(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	config := defaultAppConfig()
	config.Storage = ConfigStorage{
		Root:      filepath.Join(home, ".atelier"),
		History:   filepath.Join(home, ".atelier", "history"),
		Artifacts: filepath.Join(home, ".atelier", "history"),
	}
	config.Tools.Filesystem.Root = filepath.Join(home, "tool-root")
	config.Providers.Ollama.BaseURL = "http://ollama.test"
	config.Providers.Ollama.Models.Chat = "chat-box-model"
	config.Providers.Ollama.Models.Harness = "harness-model"
	if err := writeAppConfig(config); err != nil {
		t.Fatalf("writeAppConfig returned error: %v", err)
	}

	app := NewApp()
	app.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/api/chat" {
			return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader("not found"))}, nil
		}
		var payload map[string]any
		data, _ := io.ReadAll(req.Body)
		if err := json.Unmarshal(data, &payload); err != nil {
			t.Fatalf("provider request body is not JSON: %v", err)
		}
		if payload["stream"] == false {
			body := "{\"model\":\"harness-model\",\"message\":{\"role\":\"assistant\",\"content\":\"```json\\n{\\\"brief\\\":\\\"Create hello.txt.\\\",\\\"needsTools\\\":true,\\\"reason\\\":\\\"File creation requires filesystem manipulation.\\\",\\\"toolCalls\\\":{\\\"name\\\":\\\"write_file\\\",\\\"filename\\\":\\\"hello.txt\\\",\\\"text\\\":\\\"hello from Atelier\\\"}}\\n```\"},\"done\":true,\"done_reason\":\"stop\",\"eval_count\":2}"
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Body:       io.NopCloser(strings.NewReader(body)),
				Header:     http.Header{"Content-Type": []string{"application/json"}},
			}, nil
		}
		body := fmt.Sprintln(`{"model":"chat-box-model","message":{"role":"assistant","content":"Created hello.txt."},"done":false}`) +
			fmt.Sprintln(`{"model":"chat-box-model","done":true,"done_reason":"stop","eval_count":3}`)
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     http.Header{"Content-Type": []string{"application/x-ndjson"}},
		}, nil
	})

	app.runChatStream(context.Background(), "request-force-write", ChatRequest{
		BaseURL: "http://ollama.test",
		Model:   "chat-box-model",
		Messages: []ChatMessage{
			{Role: "user", Content: "Create a file named hello.txt in the workspace that says hello from Atelier."},
		},
	})
	content, err := os.ReadFile(filepath.Join(config.Tools.Filesystem.Root, "hello.txt"))
	if err != nil {
		t.Fatalf("expected hello.txt to be written: %v", err)
	}
	if string(content) != "hello from Atelier" {
		t.Fatalf("hello.txt = %q, want requested content", string(content))
	}
	conversations, err := listConversations(config.Storage)
	if err != nil {
		t.Fatalf("listConversations returned error: %v", err)
	}
	detail, err := getConversation(config.Storage, conversations[0].ID)
	if err != nil {
		t.Fatalf("getConversation returned error: %v", err)
	}
	harnessRun := detail.Turns[1].ProviderResponse["harnessRun"].(map[string]any)
	toolStep := harnessStepByKind(t, harnessRun["steps"].([]any), "tool_call")
	activity := toolStep["tools"].([]any)[0].(map[string]any)
	if activity["name"] != "write_file" || activity["status"] != "completed" {
		t.Fatalf("tool activity = %+v, want completed write_file", activity)
	}
}

func TestHarnessRoutesChatImageRequestToImageTool(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	config := defaultAppConfig()
	config.Storage = ConfigStorage{
		Root:      filepath.Join(home, ".atelier"),
		History:   filepath.Join(home, ".atelier", "history"),
		Artifacts: filepath.Join(home, ".atelier", "history"),
	}
	config.Providers.Ollama.BaseURL = "http://ollama.test"
	config.Providers.Ollama.Models.Chat = "chat-model"
	config.Providers.Ollama.Models.Harness = "chat-model"
	config.Providers.Ollama.Models.Image = "image-model"
	if err := writeAppConfig(config); err != nil {
		t.Fatalf("writeAppConfig returned error: %v", err)
	}

	app := NewApp()
	app.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/api/show":
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Body:       io.NopCloser(strings.NewReader(`{"capabilities":["completion"]}`)),
				Header:     http.Header{"Content-Type": []string{"application/json"}},
			}, nil
		case "/api/generate":
			var payload map[string]any
			data, _ := io.ReadAll(req.Body)
			if err := json.Unmarshal(data, &payload); err != nil {
				t.Fatalf("image request body is not JSON: %v", err)
			}
			if payload["model"] != "image-model" {
				t.Fatalf("image request model = %q, want settings fallback image-model", payload["model"])
			}
			body := `{"model":"image-model","image":"iVBORw0KGgo=","done":true}`
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Body:       io.NopCloser(strings.NewReader(body)),
				Header:     http.Header{"Content-Type": []string{"application/json"}},
			}, nil
		default:
			t.Fatalf("unexpected provider path %q, want /api/generate", req.URL.Path)
		}
		return nil, nil
	})
	app.runChatStream(t.Context(), "request-image-tool", ChatRequest{
		BaseURL:       "http://ollama.test",
		Model:         "chat-model",
		SelectedModel: "chat-model",
		Messages: []ChatMessage{
			{Role: "user", Content: "Create an image of a small house"},
		},
	})

	conversations, err := listConversations(config.Storage)
	if err != nil {
		t.Fatalf("listConversations returned error: %v", err)
	}
	if len(conversations) != 1 {
		t.Fatalf("conversation count = %d, want 1", len(conversations))
	}
	detail, err := getConversation(config.Storage, conversations[0].ID)
	if err != nil {
		t.Fatalf("getConversation returned error: %v", err)
	}
	if len(detail.Turns) != 2 {
		t.Fatalf("turn count = %d, want user and assistant", len(detail.Turns))
	}
	images := historyImagesForTest(detail.Turns[1].Content)
	if len(images) != 1 {
		t.Fatalf("assistant image content = %+v, want one image", detail.Turns[1].Content)
	}
	tool, ok := detail.Turns[1].ProviderResponse["tool"].(map[string]any)
	if !ok || tool["name"] != "image_generation" {
		t.Fatalf("assistant provider tool = %+v, want image_generation", detail.Turns[1].ProviderResponse["tool"])
	}
	harnessRun, ok := detail.Turns[1].ProviderResponse["harnessRun"].(map[string]any)
	if !ok {
		t.Fatalf("assistant provider response missing harness run: %+v", detail.Turns[1].ProviderResponse)
	}
	steps, ok := harnessRun["steps"].([]any)
	if !ok {
		t.Fatalf("harness steps = %+v, want timeline", harnessRun["steps"])
	}
	toolStep := harnessStepByKind(t, steps, "tool_call")
	if toolStep["status"] != "completed" || toolStep["model"] != "image-model" {
		t.Fatalf("tool step = %+v, want completed image-model call", toolStep)
	}
}

func TestHarnessRoutesChatImageRequestToSelectedImageCapableModel(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	config := defaultAppConfig()
	config.Storage = ConfigStorage{
		Root:      filepath.Join(home, ".atelier"),
		History:   filepath.Join(home, ".atelier", "history"),
		Artifacts: filepath.Join(home, ".atelier", "history"),
	}
	config.Providers.Ollama.BaseURL = "http://ollama.test"
	config.Providers.Ollama.Models.Chat = "chat-model"
	config.Providers.Ollama.Models.Harness = "harness-model"
	config.Providers.Ollama.Models.Image = "settings-image-model"
	if err := writeAppConfig(config); err != nil {
		t.Fatalf("writeAppConfig returned error: %v", err)
	}

	app := NewApp()
	app.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/api/show":
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Body:       io.NopCloser(strings.NewReader(`{"capabilities":["completion","image-generation"]}`)),
				Header:     http.Header{"Content-Type": []string{"application/json"}},
			}, nil
		case "/api/generate":
			var payload map[string]any
			data, _ := io.ReadAll(req.Body)
			if err := json.Unmarshal(data, &payload); err != nil {
				t.Fatalf("image request body is not JSON: %v", err)
			}
			if payload["model"] != "x/flux2-klein:4b" {
				t.Fatalf("image request model = %q, want selected image-capable model", payload["model"])
			}
			body := `{"model":"x/flux2-klein:4b","image":"iVBORw0KGgo=","done":true}`
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Body:       io.NopCloser(strings.NewReader(body)),
				Header:     http.Header{"Content-Type": []string{"application/json"}},
			}, nil
		default:
			t.Fatalf("unexpected provider path %q", req.URL.Path)
		}
		return nil, nil
	})
	app.runChatStream(t.Context(), "request-selected-image-tool", ChatRequest{
		BaseURL:       "http://ollama.test",
		Model:         "harness-model",
		SelectedModel: "x/flux2-klein:4b",
		Messages: []ChatMessage{
			{Role: "user", Content: "Create an image of a small house"},
		},
	})

	conversations, err := listConversations(config.Storage)
	if err != nil {
		t.Fatalf("listConversations returned error: %v", err)
	}
	detail, err := getConversation(config.Storage, conversations[0].ID)
	if err != nil {
		t.Fatalf("getConversation returned error: %v", err)
	}
	harnessRun, ok := detail.Turns[1].ProviderResponse["harnessRun"].(map[string]any)
	if !ok {
		t.Fatalf("assistant provider response missing harness run: %+v", detail.Turns[1].ProviderResponse)
	}
	toolStep := harnessStepByKind(t, harnessRun["steps"].([]any), "tool_call")
	if toolStep["model"] != "x/flux2-klein:4b" {
		t.Fatalf("tool step = %+v, want selected image-capable model", toolStep)
	}
}

func TestStreamChatReturnsConversationAfterPendingTurn(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	config := defaultAppConfig()
	config.Storage = ConfigStorage{
		Root:      filepath.Join(home, ".atelier"),
		History:   filepath.Join(home, ".atelier", "history"),
		Artifacts: filepath.Join(home, ".atelier", "history"),
	}
	config.Providers.Ollama.BaseURL = "http://ollama.test"
	config.Providers.Ollama.Models.Chat = "chat-model"
	config.Providers.Ollama.Models.Harness = "chat-model"
	if err := writeAppConfig(config); err != nil {
		t.Fatalf("writeAppConfig returned error: %v", err)
	}

	app := NewApp()
	app.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		var payload map[string]any
		data, _ := io.ReadAll(req.Body)
		if err := json.Unmarshal(data, &payload); err != nil {
			t.Fatalf("provider request body is not JSON: %v", err)
		}
		if payload["stream"] == false {
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Body:       io.NopCloser(strings.NewReader(`{"model":"chat-model","message":{"role":"assistant","content":"Proceed normally."},"done":true}`)),
				Header:     http.Header{"Content-Type": []string{"application/json"}},
			}, nil
		}
		body := fmt.Sprintln(`{"model":"chat-model","message":{"role":"assistant","content":"Later."},"done":false}`) +
			fmt.Sprintln(`{"model":"chat-model","done":true,"done_reason":"stop","eval_count":1}`)
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     http.Header{"Content-Type": []string{"application/x-ndjson"}},
		}, nil
	})

	start, err := app.StreamChat(ChatRequest{
		RequestID: "request-immediate",
		BaseURL:   "http://ollama.test",
		Model:     "chat-model",
		Messages: []ChatMessage{
			{Role: "user", Content: "Start now"},
		},
	})
	if err != nil {
		t.Fatalf("StreamChat returned error: %v", err)
	}
	if start.RequestID != "request-immediate" {
		t.Fatalf("request id = %q, want request-immediate", start.RequestID)
	}
	if strings.TrimSpace(start.ConversationID) == "" {
		t.Fatal("StreamChat returned empty conversation id")
	}

	detail, err := getConversation(config.Storage, start.ConversationID)
	if err != nil {
		t.Fatalf("getConversation returned error: %v", err)
	}
	if len(detail.Turns) == 0 {
		t.Fatal("conversation has no pending user turn")
	}
	if got := detail.Turns[0].Content[0].Text; got != "Start now" {
		t.Fatalf("pending user text = %q, want Start now", got)
	}
	waitForStreamCleanup(t, app, start.RequestID)
}

func TestHarnessStartChatTurnRecordsUserBeforeAssistant(t *testing.T) {
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

	engine := newHarnessEngine(config)
	req := ChatRequest{
		Model: "chat-model",
		Messages: []ChatMessage{
			{Role: "user", Content: "Start immediately"},
		},
	}
	conversationID, err := engine.StartChatTurn(req)
	if err != nil {
		t.Fatalf("StartChatTurn returned error: %v", err)
	}
	conversations, err := listConversations(storage)
	if err != nil {
		t.Fatalf("listConversations returned error: %v", err)
	}
	if len(conversations) != 1 || conversations[0].ID != conversationID {
		t.Fatalf("conversations = %+v, want started conversation %s", conversations, conversationID)
	}
	detail, err := getConversation(storage, conversationID)
	if err != nil {
		t.Fatalf("getConversation returned error: %v", err)
	}
	if len(detail.Turns) != 1 || detail.Turns[0].Role != "user" {
		t.Fatalf("turns after start = %+v, want one user turn", detail.Turns)
	}

	run := newChatHarnessRun("chat-model", "stop", 2)
	if err := engine.SaveAssistantTurn(conversationID, "Done later.", "", "chat-model", "stop", 2, run); err != nil {
		t.Fatalf("SaveAssistantTurn returned error: %v", err)
	}
	detail, err = getConversation(storage, conversationID)
	if err != nil {
		t.Fatalf("getConversation after assistant returned error: %v", err)
	}
	if len(detail.Turns) != 2 || detail.Turns[1].Role != "assistant" {
		t.Fatalf("turns after assistant = %+v, want user and assistant", detail.Turns)
	}
}

func TestWriteChatConversationPersistsInputImages(t *testing.T) {
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

	const pngDataURL = "data:image/png;base64,iVBORw0KGgo="
	conversationID, err := writeChatConversation(
		config,
		ChatRequest{
			Model: "chat-model",
			Messages: []ChatMessage{
				{Role: "user", Content: "Describe this image", Images: []string{pngDataURL}},
			},
		},
		"It is a tiny png.",
		"",
		"chat-model",
		"stop",
		4,
		"Image Description",
	)
	if err != nil {
		t.Fatalf("writeChatConversation returned error: %v", err)
	}

	detail, err := getConversation(storage, conversationID)
	if err != nil {
		t.Fatalf("getConversation returned error: %v", err)
	}
	if detail.Conversation.Stats.ArtifactCount != 1 {
		t.Fatalf("artifact count = %d, want 1", detail.Conversation.Stats.ArtifactCount)
	}
	if len(detail.Turns) != 2 {
		t.Fatalf("turn count = %d, want 2", len(detail.Turns))
	}
	userContent := detail.Turns[0].Content
	if len(userContent) != 2 {
		t.Fatalf("user content count = %d, want text and image", len(userContent))
	}
	imageContent := userContent[1]
	if imageContent.Type != "image" {
		t.Fatalf("image content type = %q, want image", imageContent.Type)
	}
	if imageContent.Path != "artifacts/input_000001_000001.png" {
		t.Fatalf("image path = %q, want artifact path", imageContent.Path)
	}
	if imageContent.MimeType != "image/png" {
		t.Fatalf("image mime type = %q, want image/png", imageContent.MimeType)
	}
	if imageContent.Text != pngDataURL {
		t.Fatalf("hydrated image text = %q, want data URL", imageContent.Text)
	}
}
