package main

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newEditorLaunchTestConfig points the app at a temp HOME the same way the
// harness tests do, so loadReadyConfig reads the written config and every
// persistence path lands under t.TempDir().
func newEditorLaunchTestConfig(t *testing.T) AppConfig {
	t.Helper()
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
	config.Providers.Ollama.Models.Primary = "chat-box-model"
	if err := os.MkdirAll(config.Tools.Filesystem.Root, 0755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	if err := writeAppConfig(config); err != nil {
		t.Fatalf("writeAppConfig returned error: %v", err)
	}
	return config
}

var (
	// Minimal PNG header bytes — enough for decodeImagePayload's magic sniff.
	editorLaunchPNG = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0x00, 0x01}
	// Minimal MP4 "ftyp" box so isVideoBytes accepts the attachment.
	editorLaunchMP4 = []byte("\x00\x00\x00\x18ftypmp42\x00\x00\x00\x00mp42\x00\x00\x00\x00")
)

// launchTurnContent finds the persisted content entry for an artifact id on
// the launch conversation's single user turn.
func launchTurnContent(t *testing.T, config AppConfig, result EditorLaunchResult, artifactID string) HistoryContent {
	t.Helper()
	detail, err := getConversation(config.Storage, result.ConversationID)
	if err != nil {
		t.Fatalf("getConversation returned error: %v", err)
	}
	if detail.Conversation.Kind != "chat" {
		t.Fatalf("conversation kind = %q, want chat", detail.Conversation.Kind)
	}
	if detail.Conversation.Stats.TurnCount != 1 {
		t.Fatalf("turn count = %d, want 1 (the launch turn only)", detail.Conversation.Stats.TurnCount)
	}
	if len(detail.Turns) != 1 || detail.Turns[0].Role != "user" {
		t.Fatalf("turns = %+v, want a single user turn", detail.Turns)
	}
	for _, content := range detail.Turns[0].Content {
		if content.ArtifactID == artifactID {
			return content
		}
	}
	t.Fatalf("artifact %q missing from the persisted turn", artifactID)
	return HistoryContent{}
}

func TestCreateEditorConversationPersistsLaunchTurn(t *testing.T) {
	config := newEditorLaunchTestConfig(t)
	app := NewApp()
	prompt := "Open this image in image editor"
	result, err := app.CreateEditorConversation(ChatRequest{
		Model: "chat-box-model",
		Messages: []ChatMessage{{
			Role:    "user",
			Content: prompt,
			Images:  []string{"data:image/png;base64," + base64.StdEncoding.EncodeToString(editorLaunchPNG)},
		}},
	})
	if err != nil {
		t.Fatalf("CreateEditorConversation returned error: %v", err)
	}
	if !strings.HasPrefix(result.ConversationID, "conv_") {
		t.Fatalf("conversation id = %q, want conv_ prefix", result.ConversationID)
	}
	if len(result.ImageArtifactIDs) != 1 || !strings.HasPrefix(result.ImageArtifactIDs[0], "img_") {
		t.Fatalf("image artifact ids = %v, want one img_ id", result.ImageArtifactIDs)
	}
	if len(result.VideoArtifactIDs) != 0 {
		t.Fatalf("video artifact ids = %v, want none", result.VideoArtifactIDs)
	}

	detail, err := getConversation(config.Storage, result.ConversationID)
	if err != nil {
		t.Fatalf("getConversation returned error: %v", err)
	}
	if detail.Conversation.Title != prompt {
		t.Fatalf("conversation title = %q, want %q", detail.Conversation.Title, prompt)
	}
	if detail.Conversation.Workspace != config.Tools.Filesystem.Root {
		t.Fatalf("workspace = %q, want %q", detail.Conversation.Workspace, config.Tools.Filesystem.Root)
	}

	content := launchTurnContent(t, config, result, result.ImageArtifactIDs[0])
	if content.Type != "image" || !strings.HasPrefix(content.Path, "artifacts/") {
		t.Fatalf("artifact entry = %+v, want an image under artifacts/", content)
	}
	conversationPath, err := findConversationPath(config.Storage, result.ConversationID)
	if err != nil {
		t.Fatalf("findConversationPath returned error: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(conversationPath), filepath.FromSlash(content.Path)))
	if err != nil {
		t.Fatalf("read persisted artifact returned error: %v", err)
	}
	if string(data) != string(editorLaunchPNG) {
		t.Fatalf("persisted artifact bytes differ from the attachment")
	}
}

func TestCreateEditorConversationVideoLaunch(t *testing.T) {
	config := newEditorLaunchTestConfig(t)
	app := NewApp()
	result, err := app.CreateEditorConversation(ChatRequest{
		Messages: []ChatMessage{{
			Role:    "user",
			Content: "Open in video editor",
			Videos:  []string{"data:video/mp4;base64," + base64.StdEncoding.EncodeToString(editorLaunchMP4)},
		}},
	})
	if err != nil {
		t.Fatalf("CreateEditorConversation returned error: %v", err)
	}
	if len(result.VideoArtifactIDs) != 1 || !strings.HasPrefix(result.VideoArtifactIDs[0], "vid_") {
		t.Fatalf("video artifact ids = %v, want one vid_ id", result.VideoArtifactIDs)
	}
	if len(result.ImageArtifactIDs) != 0 {
		t.Fatalf("image artifact ids = %v, want none", result.ImageArtifactIDs)
	}
	content := launchTurnContent(t, config, result, result.VideoArtifactIDs[0])
	conversationPath, err := findConversationPath(config.Storage, result.ConversationID)
	if err != nil {
		t.Fatalf("findConversationPath returned error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(conversationPath), filepath.FromSlash(content.Path))); err != nil {
		t.Fatalf("persisted video artifact missing: %v", err)
	}
}

func TestCreateEditorConversationRequiresAttachment(t *testing.T) {
	config := newEditorLaunchTestConfig(t)
	app := NewApp()
	_, err := app.CreateEditorConversation(ChatRequest{
		Messages: []ChatMessage{{Role: "user", Content: "Open this image in image editor"}},
	})
	if err == nil {
		t.Fatalf("CreateEditorConversation without an attachment returned no error")
	}
	conversations, err := listConversations(config.Storage)
	if err != nil {
		t.Fatalf("listConversations returned error: %v", err)
	}
	if len(conversations) != 0 {
		t.Fatalf("a refused launch persisted %d conversations, want 0", len(conversations))
	}
}

func TestCreateEditorConversationRefusesUnknownProject(t *testing.T) {
	config := newEditorLaunchTestConfig(t)
	app := NewApp()
	_, err := app.CreateEditorConversation(ChatRequest{
		ProjectID: "proj_missing",
		Messages: []ChatMessage{{
			Role:    "user",
			Content: "Open this image in image editor",
			Images:  []string{"data:image/png;base64," + base64.StdEncoding.EncodeToString(editorLaunchPNG)},
		}},
	})
	if err == nil {
		t.Fatalf("CreateEditorConversation with an unknown project returned no error")
	}
	conversations, err := listConversations(config.Storage)
	if err != nil {
		t.Fatalf("listConversations returned error: %v", err)
	}
	if len(conversations) != 0 {
		t.Fatalf("a failed project validation persisted %d conversations, want 0", len(conversations))
	}
}

func TestCreateEditorConversationPinsModelOverrides(t *testing.T) {
	config := newEditorLaunchTestConfig(t)
	app := NewApp()
	result, err := app.CreateEditorConversation(ChatRequest{
		Model: "chat-box-model",
		ModelOverrides: &ConversationModelOverrides{
			PrimaryModel: "pinned-primary",
			ImageModel:   "pinned-image",
		},
		Messages: []ChatMessage{{
			Role:    "user",
			Content: "Open this image in image editor",
			Images:  []string{"data:image/png;base64," + base64.StdEncoding.EncodeToString(editorLaunchPNG)},
		}},
	})
	if err != nil {
		t.Fatalf("CreateEditorConversation returned error: %v", err)
	}
	detail, err := getConversation(config.Storage, result.ConversationID)
	if err != nil {
		t.Fatalf("getConversation returned error: %v", err)
	}
	if detail.Conversation.ModelOverrides == nil {
		t.Fatalf("model overrides not pinned onto the record")
	}
	if detail.Conversation.ModelOverrides.PrimaryModel != "pinned-primary" || detail.Conversation.ModelOverrides.ImageModel != "pinned-image" {
		t.Fatalf("model overrides = %+v, want the pinned selections", detail.Conversation.ModelOverrides)
	}
}

// openEditorLaunchConversation persists a one-image launch conversation and
// returns the tool execution context pointed at it, with the OpenEditor hook
// recording every fired (kind, conversationID, artifactID) triple.
func openEditorLaunchConversation(t *testing.T, config AppConfig, app *App) (HarnessToolExecutionContext, EditorLaunchResult, *[][]string) {
	t.Helper()
	result, err := app.CreateEditorConversation(ChatRequest{
		Model: "chat-box-model",
		Messages: []ChatMessage{{
			Role:    "user",
			Content: "Open this image in image editor",
			Images:  []string{"data:image/png;base64," + base64.StdEncoding.EncodeToString(editorLaunchPNG)},
		}},
	})
	if err != nil {
		t.Fatalf("CreateEditorConversation returned error: %v", err)
	}
	tools := newHarnessToolExecutionContext(config)
	tools.Storage = config.Storage
	tools.ConversationID = result.ConversationID
	fired := &[][]string{}
	tools.OpenEditor = func(kind, conversationID, artifactID string) error {
		*fired = append(*fired, []string{kind, conversationID, artifactID})
		return nil
	}
	return tools, result, fired
}

func openEditorDefinition(t *testing.T, config AppConfig) HarnessToolDefinition {
	t.Helper()
	definition, ok := defaultHarnessToolRegistry(context.Background(), config, nil).Get("open_editor")
	if !ok {
		t.Fatalf("open_editor missing from the default tool registry")
	}
	return definition
}

func TestOpenEditorToolResolvesAndEmits(t *testing.T) {
	config := newEditorLaunchTestConfig(t)
	app := NewApp()
	tools, result, fired := openEditorLaunchConversation(t, config, app)
	definition := openEditorDefinition(t, config)

	output, summary, err := definition.Execute(t.Context(), tools, HarnessToolCall{Name: "open_editor", Mode: "image"})
	if err != nil {
		t.Fatalf("open_editor returned error: %v", err)
	}
	typed, ok := output.(ToolOpenEditorResult)
	if !ok {
		t.Fatalf("open_editor output type = %T, want ToolOpenEditorResult", output)
	}
	if typed.Kind != "image" || typed.ConversationID != result.ConversationID || typed.ArtifactID != result.ImageArtifactIDs[0] {
		t.Fatalf("open_editor result = %+v, want the persisted image artifact", typed)
	}
	if len(*fired) != 1 || (*fired)[0][0] != "image" || (*fired)[0][1] != result.ConversationID || (*fired)[0][2] != result.ImageArtifactIDs[0] {
		t.Fatalf("launch hook fired %v, want one image launch for %s", *fired, result.ImageArtifactIDs[0])
	}
	if !strings.Contains(summary, result.ImageArtifactIDs[0]) || !strings.Contains(summary, "nothing was generated") {
		t.Fatalf("summary = %q, want the artifact id and the opened-as-is caveat", summary)
	}

	// An empty mode resolves the conversation's newest visual — the same image.
	if _, _, err := definition.Execute(t.Context(), tools, HarnessToolCall{Name: "open_editor"}); err != nil {
		t.Fatalf("open_editor with no mode returned error: %v", err)
	}
	if len(*fired) != 2 || (*fired)[1][0] != "image" {
		t.Fatalf("launch hook fired %v, want an image launch for the modeless call", *fired)
	}
}

func TestOpenEditorToolErrorsWithoutAMatchingAsset(t *testing.T) {
	config := newEditorLaunchTestConfig(t)
	app := NewApp()
	tools, _, fired := openEditorLaunchConversation(t, config, app)
	definition := openEditorDefinition(t, config)

	_, _, err := definition.Execute(t.Context(), tools, HarnessToolCall{Name: "open_editor", Mode: "video"})
	if err == nil || !strings.Contains(err.Error(), "no video") {
		t.Fatalf("open_editor with mode video returned (%v), want a no-video error", err)
	}
	if len(*fired) != 0 {
		t.Fatalf("launch hook fired %v for a failed resolution, want none", *fired)
	}
}

func TestOpenEditorToolRequiresConversationAndHook(t *testing.T) {
	config := newEditorLaunchTestConfig(t)
	definition := openEditorDefinition(t, config)

	tools := newHarnessToolExecutionContext(config)
	if _, _, err := definition.Execute(t.Context(), tools, HarnessToolCall{Name: "open_editor", Mode: "image"}); err == nil || !strings.Contains(err.Error(), "no conversation") {
		t.Fatalf("open_editor without a conversation returned (%v), want a conversation error", err)
	}

	tools.Storage = config.Storage
	tools.ConversationID = "conv_missing"
	if _, _, err := definition.Execute(t.Context(), tools, HarnessToolCall{Name: "open_editor", Mode: "image"}); err == nil {
		t.Fatalf("open_editor on a missing conversation returned no error")
	}

	tools.OpenEditor = func(kind, conversationID, artifactID string) error {
		return errors.New("delivery failed")
	}
	app := NewApp()
	result, err := app.CreateEditorConversation(ChatRequest{
		Model: "chat-box-model",
		Messages: []ChatMessage{{
			Role:    "user",
			Content: "Open this image in image editor",
			Images:  []string{"data:image/png;base64," + base64.StdEncoding.EncodeToString(editorLaunchPNG)},
		}},
	})
	if err != nil {
		t.Fatalf("CreateEditorConversation returned error: %v", err)
	}
	tools.ConversationID = result.ConversationID
	if _, _, err := definition.Execute(t.Context(), tools, HarnessToolCall{Name: "open_editor", Mode: "image"}); err == nil || !strings.Contains(err.Error(), "delivery failed") {
		t.Fatalf("open_editor with a failing hook returned (%v), want the hook's error", err)
	}
}

func TestOpenEditorToolValidatesMode(t *testing.T) {
	config := newEditorLaunchTestConfig(t)
	definition := openEditorDefinition(t, config)
	if errs := definition.Validate("toolCalls[0]", HarnessToolCall{Name: "open_editor", Mode: "audio"}); len(errs) == 0 {
		t.Fatalf("Validate accepted mode \"audio\"")
	}
	for _, mode := range []string{"", "image", "video"} {
		if errs := definition.Validate("toolCalls[0]", HarnessToolCall{Name: "open_editor", Mode: mode}); len(errs) != 0 {
			t.Fatalf("Validate rejected mode %q: %v", mode, errs)
		}
	}
	if definition.RequiresPermission() {
		t.Fatalf("open_editor must stay read-only (no permission gate)")
	}
}

// TestOpenEditorGatewayDeliveryFailsWithoutUI pins the fail-verbose delivery:
// the gateway's hook reports the undeliverable event instead of letting tool
// evidence claim an editor no UI ever showed (a.ctx is nil in tests).
func TestOpenEditorGatewayDeliveryFailsWithoutUI(t *testing.T) {
	config := newEditorLaunchTestConfig(t)
	app := NewApp()
	tools, result, _ := openEditorLaunchConversation(t, config, app)
	_ = tools
	gateway := newToolGateway(app, config, defaultHarnessToolRegistry(context.Background(), config, nil))
	gateway.tools.Storage = config.Storage
	gateway.tools.ConversationID = result.ConversationID
	outcome := gateway.Execute(t.Context(), ToolExecutionRequest{Name: "open_editor", Call: HarnessToolCall{Name: "open_editor", Mode: "image"}, ConversationID: result.ConversationID})
	if outcome.Status != "failed" || !strings.Contains(outcome.Error, "could not be delivered") {
		t.Fatalf("gateway open_editor outcome = %+v, want a delivery failure", outcome)
	}
}
