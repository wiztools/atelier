package main

import (
	"os"
	"path/filepath"
	"testing"
)

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
	if config.Prompts.System == "" {
		t.Fatal("system prompt should default")
	}
	if config.Generation.Image.Width != 768 || config.Generation.Image.Steps != 24 {
		t.Fatalf("image generation defaults = %+v", config.Generation.Image)
	}
}

func TestMergeAppConfigNormalizesOllamaEndpoint(t *testing.T) {
	config := mergeAppConfig(AppConfig{
		Providers: ConfigProviders{
			Ollama: ConfigOllama{
				BaseURL: "localhost:11434/",
				Models: ConfigOllamaModels{
					Chat:  "chat-model",
					Image: "image-model",
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
