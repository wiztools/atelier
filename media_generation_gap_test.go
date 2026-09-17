package main

import (
	"context"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
)

// TestMediaGenUnavailableNoteForbidsFabrication asserts the model-facing note
// tells the final model the capability is absent and, crucially, not to claim
// the media was created or queued — the exact failure in
// conv_20a0df2b2db9b4e9ea5a1ad9, where a small model answered "Video Extension
// initiated … queued … rendering shortly" for a clip that never existed.
func TestMediaGenUnavailableNoteForbidsFabrication(t *testing.T) {
	note := mediaGenUnavailableNote("video")
	if note == "" {
		t.Fatal("expected a note for video mode, got empty")
	}
	lower := strings.ToLower(note)
	if !strings.Contains(lower, "video") {
		t.Errorf("note should name the media kind: %q", note)
	}
	if !strings.Contains(lower, "do not claim") {
		t.Errorf("note must forbid fabricating success: %q", note)
	}
	if !strings.Contains(note, "Settings → Providers") {
		t.Errorf("note must name the remedy (Settings → Providers): %q", note)
	}
	if mediaGenUnavailableNote("text") != "" {
		t.Errorf("non-generation mode should yield no note")
	}
}

// TestMediaGenFallbackNotice covers the deterministic user-facing blockquote:
// present when the capability is missing and the model's own answer didn't name
// the remedy, suppressed once the answer already covers it, and empty when the
// capability is available.
func TestMediaGenFallbackNotice(t *testing.T) {
	if got := mediaGenFallbackNotice(false, "video", "anything"); got != "" {
		t.Errorf("available capability should yield no notice, got %q", got)
	}
	notice := mediaGenFallbackNotice(true, "video", "Sure, I'll get right on that.")
	if notice == "" || !strings.Contains(notice, "Settings → Providers") {
		t.Errorf("missing capability with a silent answer should name the remedy, got %q", notice)
	}
	if got := mediaGenFallbackNotice(true, "video", "You need to add a fal.ai key first."); got != "" {
		t.Errorf("answer already naming the remedy should suppress the notice, got %q", got)
	}
}

// TestGenerationToolAvailableReflectsRegistry confirms the availability check
// tracks the live registry: absent without fal config, present once a video
// model and key are configured.
func TestGenerationToolAvailableReflectsRegistry(t *testing.T) {
	keyring.MockInit()
	t.Cleanup(func() { _ = clearFalAPIKey() })

	base := defaultAppConfig()
	base.Providers.Fal.VideoModel = ""
	if generationToolAvailable(defaultHarnessToolRegistry(context.Background(), base, nil), "video") {
		t.Fatal("video generation tool should be unavailable without config")
	}

	if err := saveFalAPIKey("fal-test-key"); err != nil {
		t.Fatalf("saveFalAPIKey: %v", err)
	}
	configured := defaultAppConfig()
	configured.Providers.Fal.VideoModel = "fal-ai/some/video-model"
	if !generationToolAvailable(defaultHarnessToolRegistry(context.Background(), configured, nil), "video") {
		t.Fatal("video generation tool should be available once configured")
	}
}

// TestHarnessDeclinesVideoExtensionWithoutTool is the end-to-end regression for
// conv_20a0df2b2db9b4e9ea5a1ad9. Triage routes an extend-the-clip request to
// video mode but finds no generate_video tool (no fal config), so it returns
// needsTools:false. Without the capability-gap guard the final model is free to
// hallucinate a "queued / rendering shortly" confirmation and it is saved
// verbatim. The turn must instead carry the deterministic decline notice and
// persist no video artifact.
func TestHarnessDeclinesVideoExtensionWithoutTool(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	keyring.MockInit()
	t.Cleanup(func() { _ = clearFalAPIKey() })

	config := defaultAppConfig()
	config.Storage = ConfigStorage{
		Root:      filepath.Join(home, ".atelier"),
		History:   filepath.Join(home, ".atelier", "history"),
		Artifacts: filepath.Join(home, ".atelier", "history"),
	}
	config.Providers.Ollama.BaseURL = "http://ollama.test"
	config.Providers.Ollama.Models.Primary = "chat-box-model"
	config.Providers.Ollama.Models.Harness = "chat-box-model"
	// No fal key and no fal video model: generate_video is absent from the registry.
	config.Providers.Fal.VideoModel = ""
	if err := writeAppConfig(config); err != nil {
		t.Fatalf("writeAppConfig: %v", err)
	}

	app := NewApp()
	app.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/api/show":
			return jsonResponse(`{"capabilities":[],"model_info":{},"details":{"family":"test","parameter_size":"1B"}}`), nil
		case "/api/chat":
			payload := chatPayload(t, req)
			if payload["stream"] == false {
				// Triage: the user wants a video, but no generate_video tool exists,
				// so it preserves the media intent yet declines tools.
				decision := `{"needsTools":false,"responseMode":"video","toolTask":"","reason":"no generate_video tool is available to extend the clip","mediaEdit":false,"imageEdit":false}`
				return chatCompletion("harness-model", decision), nil
			}
			// The final model hallucinates a successful queue — the buggy behavior.
			body := "{\"model\":\"chat-box-model\",\"message\":{\"role\":\"assistant\",\"content\":\"Action Confirmed: Video Extension initiated. The 3-second continuation has been queued and will render shortly.\"},\"done\":false}\n" +
				"{\"model\":\"chat-box-model\",\"done\":true,\"done_reason\":\"stop\",\"eval_count\":3}\n"
			return &http.Response{StatusCode: 200, Status: "200 OK",
				Body:   io.NopCloser(strings.NewReader(body)),
				Header: http.Header{"Content-Type": []string{"application/x-ndjson"}}}, nil
		default:
			t.Fatalf("unexpected request %s %s", req.Method, req.URL)
			return nil, nil
		}
	})

	app.runChatStream(context.Background(), "request-decline-video", ChatRequest{
		BaseURL: "http://ollama.test",
		Model:   "chat-box-model",
		Messages: []ChatMessage{
			{Role: "user", Content: "Extend this video by 3 more seconds of the girl running towards the city."},
		},
	})

	conversations, err := listConversations(config.Storage)
	if err != nil {
		t.Fatalf("listConversations: %v", err)
	}
	if len(conversations) != 1 {
		t.Fatalf("conversation count = %d, want 1", len(conversations))
	}
	detail, err := getConversation(config.Storage, conversations[0].ID)
	if err != nil {
		t.Fatalf("getConversation: %v", err)
	}
	if detail.Conversation.Stats.ArtifactCount != 0 {
		t.Fatalf("artifactCount = %d, want 0 (nothing was generated)", detail.Conversation.Stats.ArtifactCount)
	}
	if len(detail.Turns) != 2 {
		t.Fatalf("turn count = %d, want user + assistant", len(detail.Turns))
	}
	assistant := detail.Turns[1]
	for i := range assistant.Content {
		if assistant.Content[i].Type == "video" {
			t.Fatalf("assistant turn must not carry a video artifact: %+v", assistant.Content[i])
		}
	}
	var text string
	for i := range assistant.Content {
		if assistant.Content[i].Type == "text" {
			text += assistant.Content[i].Text
		}
	}
	if !strings.Contains(text, "Settings → Providers") {
		t.Fatalf("assistant reply must carry the deterministic decline notice naming the remedy; got:\n%s", text)
	}
}
