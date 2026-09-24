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

// TestHarnessForceToolsVideoModeWhenTriageMisreadsToolset is the end-to-end
// regression for conv_50e4aaae3af7ef150ec5c48a. fal IS configured
// (generate_video sits in the registry), but triage mis-reads the toolset —
// "the specific tool for extending a video clip is not available" — and returns
// needsTools:false with responseMode:"video". Neither the unavailable-tool
// decline note nor the planner-exhausted guard fires on that combination, so
// the final model narrated a 2-second extension that never ran. The harness
// must cross-check the registry and force the planner on, so the extension
// actually happens and the turn persists a real video artifact.
func TestHarnessForceToolsVideoModeWhenTriageMisreadsToolset(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	keyring.MockInit()
	if err := saveFalAPIKey("fal-test-key"); err != nil {
		t.Fatalf("saveFalAPIKey: %v", err)
	}
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
	config.Providers.Fal.VideoModel = defaultFalVideoModel
	if err := writeAppConfig(config); err != nil {
		t.Fatalf("writeAppConfig: %v", err)
	}

	app := NewApp()
	falCalls := 0
	nonStreamCount := 0
	prepCalls := 0
	app.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if strings.HasPrefix(req.URL.Path, "/v1/models/pricing") {
			return falTestPricingResponse(), nil
		}
		if strings.Contains(req.URL.Path, "/api/openapi/") {
			return jsonResponse(`{"components":{"schemas":{"Input":{"type":"object","required":["prompt"],"properties":{"prompt":{"type":"string"},"duration":{"type":"string"},"aspect_ratio":{"type":"string"}}}}}}`), nil
		}
		if strings.Contains(req.URL.Host, "fal.run") {
			falCalls++
			if req.Method == http.MethodPost {
				return jsonResponse(`{"request_id":"req-extend-1"}`), nil
			}
			if strings.HasSuffix(req.URL.Path, "/status") {
				return jsonResponse(`{"status":"COMPLETED"}`), nil
			}
			if strings.HasSuffix(req.URL.Path, "/requests/req-extend-1") {
				return jsonResponse(`{"video":{"url":"https://queue.fal.run/extended.mp4","content_type":"video/mp4"}}`), nil
			}
			return &http.Response{StatusCode: 200, Status: "200 OK",
				Body:   io.NopCloser(strings.NewReader(string(tinyMP4()))),
				Header: http.Header{"Content-Type": []string{"video/mp4"}}}, nil
		}
		switch req.URL.Path {
		case "/api/show":
			return jsonResponse(`{"capabilities":[],"model_info":{},"details":{"family":"test","parameter_size":"1B"}}`), nil
		case "/api/chat":
			payload := chatPayload(t, req)
			if payload["stream"] == false {
				nonStreamCount++
				if nonStreamCount == 1 {
					// Triage: the exact failing decision from the conversation —
					// video intent preserved, tools declined on a false claim
					// about the toolset.
					decision := `{"needsTools":false,"responseMode":"video","toolTask":"","reason":"The user requested a video extension, but the specific tool for extending a video clip is not available in the current toolset.","mediaEdit":false,"imageEdit":false}`
					return chatCompletion("harness-model", decision), nil
				}
				prepCalls++
				body := `{"brief":"Extend the clip.","needsTools":true,"reason":"video","toolCalls":[{"name":"generate_video","content":"Extend the clip by 2 seconds, keeping the on-screen text unchanged"}]}`
				if prepCalls > 1 {
					body = `{"brief":"The extension was generated.","needsTools":false,"reason":"done","toolCalls":[]}`
				}
				return chatCompletion("harness-model", body), nil
			}
			// The final model's reply from the conversation — asserted harmless
			// only because the artifact now exists alongside it.
			body := "{\"model\":\"chat-box-model\",\"message\":{\"role\":\"assistant\",\"content\":\"The video has been extended by 2 seconds, and the existing text remains intact.\"},\"done\":false}\n" +
				"{\"model\":\"chat-box-model\",\"done\":true,\"done_reason\":\"stop\",\"eval_count\":3}\n"
			return &http.Response{StatusCode: 200, Status: "200 OK",
				Body:   io.NopCloser(strings.NewReader(body)),
				Header: http.Header{"Content-Type": []string{"application/x-ndjson"}}}, nil
		default:
			t.Fatalf("unexpected request %s %s", req.Method, req.URL)
			return nil, nil
		}
	})

	app.runChatStream(context.Background(), "request-force-video", ChatRequest{
		BaseURL: "http://ollama.test",
		Model:   "chat-box-model",
		Messages: []ChatMessage{
			{Role: "user", Content: "Extend this video by 2 seconds. The text needs to stay."},
		},
	})

	if falCalls == 0 {
		t.Fatal("fal was never called — the force-tooled planner did not run generate_video")
	}

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
	if detail.Conversation.Stats.ArtifactCount != 1 {
		t.Fatalf("artifactCount = %d, want 1 (the extension must actually render)", detail.Conversation.Stats.ArtifactCount)
	}
	assistant := detail.Turns[len(detail.Turns)-1]
	hasVideo := false
	for i := range assistant.Content {
		if assistant.Content[i].Type == "video" {
			hasVideo = true
		}
	}
	if !hasVideo {
		t.Fatalf("assistant turn has no video artifact: %+v", assistant.Content)
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

// TestTriagePromptRoutesSemanticImageEditsToImageMode pins the image sibling
// of the RESTYLING carve-out: the imageEdit sentence must carve content
// changes out — altering what an image shows (proportions, elements,
// appearance) is generation, and the local CLI image tools are no remedy for
// it — so a "make the head smaller" turn routes to image mode with
// generate_image and the attachment as reference instead of collapsing to
// text. conv_d53a86bd51bd5740ed30e683: the edit was routed to text on the
// theory that image tools only transform whole images, and the final model —
// which never saw the stripped attachment — asked for the image it had been
// sent.
func TestTriagePromptRoutesSemanticImageEditsToImageMode(t *testing.T) {
	registry := newHarnessToolRegistry([]HarnessToolDefinition{imageGenerationToolDefinition(AppConfig{})})
	prompt := triageSystemPrompt(registry, nil, "/tmp/ws")
	if !strings.Contains(prompt, "generate_image") {
		t.Fatalf("image-mode guidance should name generate_image:\n%s", prompt)
	}
	for _, want := range []string{
		"CONTENT CHANGES are not one of these",
		`never imageEdit`,
		"edit reference",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("imageEdit guidance missing %q:\n%s", want, prompt)
		}
	}
}
