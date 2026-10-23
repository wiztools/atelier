package main

import (
	"context"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
)

// TestVoiceGenerationToolGating confirms generate_voice is registered only
// when a fal voice-design model is configured AND a fal.ai key is present —
// the speech/sound gate on the voiceDesignModel field.
func TestVoiceGenerationToolGating(t *testing.T) {
	keyring.MockInit()
	t.Cleanup(func() { _ = clearFalAPIKey() })

	base := defaultAppConfig()
	base.Providers.Fal.VoiceDesignModel = ""
	if voiceDesignConfigured(base) {
		t.Fatal("voice design should not be configured without a fal voice-design model")
	}
	if _, ok := defaultHarnessToolRegistry(context.Background(), base, nil).Get("generate_voice"); ok {
		t.Fatal("generate_voice should be absent when unconfigured")
	}

	// Model set but no key → still not configured (would only fail at call time).
	noKey := defaultAppConfig()
	noKey.Providers.Fal.VoiceDesignModel = "fal-ai/some/voice-design"
	if voiceDesignConfigured(noKey) {
		t.Fatal("voice design should not be configured without a fal key")
	}
	if _, ok := defaultHarnessToolRegistry(context.Background(), noKey, nil).Get("generate_voice"); ok {
		t.Fatal("generate_voice should be absent without a fal key")
	}

	if err := saveFalAPIKey("fal-test-key"); err != nil {
		t.Fatalf("saveFalAPIKey: %v", err)
	}
	configured := defaultAppConfig()
	configured.Providers.Fal.VoiceDesignModel = "fal-ai/some/voice-design"
	if !voiceDesignConfigured(configured) {
		t.Fatal("voice design should be configured with a fal voice-design model and key")
	}
	if _, ok := defaultHarnessToolRegistry(context.Background(), configured, nil).Get("generate_voice"); !ok {
		t.Fatal("generate_voice should be registered when configured")
	}
	if got := resolveDefaultVoiceDesignModel(configured); got != "fal-ai/some/voice-design" {
		t.Fatalf("resolveDefaultVoiceDesignModel = %q, want the configured model", got)
	}
	if got := resolveDefaultVoiceDesignModel(base); got != defaultFalVoiceDesignModel {
		t.Fatalf("resolveDefaultVoiceDesignModel fallback = %q, want %q", got, defaultFalVoiceDesignModel)
	}
}

// TestVoiceDesignExecuteDefaultsAndClamps covers the canonical-request shaping:
// the default preview line when the planner passes none, the rune clamps at the
// fal endpoint's documented limits, the notices that explain both, and the
// voice-id surfacing in notice + summary.
func TestVoiceDesignExecuteDefaultsAndClamps(t *testing.T) {
	config := defaultAppConfig()
	config.Providers.Fal.VoiceDesignModel = "fal-ai/some/voice-design"

	newTools := func(got *[]VoiceDesignRequest) HarnessToolExecutionContext {
		tools := newHarnessToolExecutionContext(config)
		tools.GenerateVoiceDesign = func(_ context.Context, req VoiceDesignRequest) (GeneratedAudio, error) {
			*got = append(*got, req)
			return GeneratedAudio{
				Data:     []byte("audio"),
				MimeType: "audio/mpeg",
				VoiceID:  "minimax_VoiceId123",
			}, nil
		}
		return tools
	}

	// No previewText: the default preview line applies under a notice, and the
	// designed voice id rides both the notice and the planner summary.
	var requests []VoiceDesignRequest
	tools := newTools(&requests)
	out, summary, err := voiceDesignExecute(context.Background(), tools, VoiceDesignRequest{
		Description: "A sweet, natural voice of an 8-year-old girl",
	})
	if err != nil {
		t.Fatalf("voiceDesignExecute: %v", err)
	}
	if len(requests) != 1 {
		t.Fatalf("got %d backend requests, want 1", len(requests))
	}
	if requests[0].PreviewText != defaultVoiceDesignPreviewText {
		t.Fatalf("preview text = %q, want the default line", requests[0].PreviewText)
	}
	if requests[0].Model != "fal-ai/some/voice-design" {
		t.Fatalf("model = %q, want the configured default", requests[0].Model)
	}
	typed, ok := out.(ToolAudioResult)
	if !ok {
		t.Fatalf("result type = %T, want ToolAudioResult", out)
	}
	joined := strings.Join(typed.Notices, "\n")
	if !strings.Contains(joined, "default line") {
		t.Errorf("notices should explain the default preview line: %v", typed.Notices)
	}
	if !strings.Contains(joined, "minimax_VoiceId123") {
		t.Errorf("notices should surface the designed voice id: %v", typed.Notices)
	}
	if !strings.Contains(summary, "minimax_VoiceId123") {
		t.Errorf("summary should carry the voice id: %q", summary)
	}

	// Over-limit fields clamp to the endpoint's documented character ceilings
	// with a notice each.
	requests = nil
	tools = newTools(&requests)
	longDescription := strings.Repeat("a", 2500)
	longPreview := strings.Repeat("b", 600)
	_, _, err = voiceDesignExecute(context.Background(), tools, VoiceDesignRequest{
		Description: longDescription,
		PreviewText: longPreview,
	})
	if err != nil {
		t.Fatalf("voiceDesignExecute (clamps): %v", err)
	}
	if len([]rune(requests[0].Description)) != 2000 {
		t.Errorf("description = %d runes, want clamped to 2000", len([]rune(requests[0].Description)))
	}
	if len([]rune(requests[0].PreviewText)) != 500 {
		t.Errorf("preview text = %d runes, want clamped to 500", len([]rune(requests[0].PreviewText)))
	}

	// A backend that reports no voice id (the Qwen voice-design family) still
	// summarizes the design instead of claiming a generated clip.
	requests = nil
	tools = newTools(&requests)
	tools.GenerateVoiceDesign = func(_ context.Context, req VoiceDesignRequest) (GeneratedAudio, error) {
		return GeneratedAudio{Data: []byte("audio"), MimeType: "audio/mpeg"}, nil
	}
	_, summary, err = voiceDesignExecute(context.Background(), tools, VoiceDesignRequest{Description: "warm narrator"})
	if err != nil {
		t.Fatalf("voiceDesignExecute (no voice id): %v", err)
	}
	if !strings.Contains(summary, "designed a voice with fal-ai/some/voice-design") {
		t.Errorf("summary = %q, want the voice-design wording", summary)
	}

	// Missing content fails at validation, not at execution.
	if errs := voiceGenerationToolDefinition().Validate("call", HarnessToolCall{}); len(errs) == 0 {
		t.Fatal("expected a validation error for an empty description")
	}
}

// TestFalCustomVoiceIDParses covers the voice-design result extraction: the id
// at the payload's top level, nested under "data" (fal nests some outputs
// there), or absent entirely.
func TestFalCustomVoiceIDParses(t *testing.T) {
	if got := falCustomVoiceID([]byte(`{"custom_voice_id":"minimax_VoiceId123","audio":{"url":"https://x/y.mp3"}}`)); got != "minimax_VoiceId123" {
		t.Fatalf("top-level id = %q", got)
	}
	if got := falCustomVoiceID([]byte(`{"data":{"custom_voice_id":"nested_id","audio":{"url":"https://x/y.mp3"}}}`)); got != "nested_id" {
		t.Fatalf("nested id = %q", got)
	}
	if got := falCustomVoiceID([]byte(`{"audio":{"url":"https://x/y.mp3"}}`)); got != "" {
		t.Fatalf("absent id = %q, want empty", got)
	}
}

// TestHarnessPlanSchemaAdmitsPreviewText pins that the format-schema planner
// grammar frees generate_voice's previewText like the per-tool param schema
// does — the two are maintained separately, and a grammar without the property
// silently takes the preview-text control away from every Ollama-planned turn.
func TestHarnessPlanSchemaAdmitsPreviewText(t *testing.T) {
	registry := defaultHarnessToolRegistry(context.Background(), defaultAppConfig(), nil)
	schema := harnessToolPlanSchema(registry)
	toolCalls, ok := schema["properties"].(map[string]any)["toolCalls"].(map[string]any)
	if !ok {
		t.Fatalf("plan schema = %#v, want a toolCalls property", schema)
	}
	items, ok := toolCalls["items"].(map[string]any)
	if !ok {
		t.Fatalf("toolCalls schema = %#v, want items", toolCalls)
	}
	properties, ok := items["properties"].(map[string]any)
	if !ok {
		t.Fatalf("toolCalls items = %#v, want properties", items)
	}
	if _, ok := properties["previewText"].(map[string]any); !ok {
		t.Errorf("plan grammar toolCalls properties missing previewText (has %v)", propertiesKeys(properties))
	}
}

// TestMediaGenAudioGapListIncludesVoiceDesign pins generate_voice into the
// audio-mode capability lists: the gap check counts a configured voice-design
// endpoint as audio generation being available, and the force tool task names
// the tool so the planner's plan emits the call.
func TestMediaGenAudioGapListIncludesVoiceDesign(t *testing.T) {
	names := generationToolNamesByMode("audio")
	found := false
	for _, name := range names {
		if name == "generate_voice" {
			found = true
		}
	}
	if !found {
		t.Fatalf("generationToolNamesByMode(audio) = %v, want generate_voice listed", names)
	}
	task := generationForceToolTask("audio")
	if !strings.Contains(task, "generate_voice") {
		t.Fatalf("generationForceToolTask(audio) = %q, want generate_voice named", task)
	}
}
