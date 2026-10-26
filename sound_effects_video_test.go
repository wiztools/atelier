package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
)

// TestResolveSFXBodyMireloVideoToVideo verifies the default endpoint's mapping:
// the source clip rides video_url, the steering description text_prompt, and
// the per-sample billing dial num_samples is pinned to one — the endpoint's own
// default of 2 would double the bill for an output the tool never delivers.
func TestResolveSFXBodyMireloVideoToVideo(t *testing.T) {
	body, notices, err := resolveSFXBody(loadSchema(t, "mirelo-sfx2.0-video-to-video"),
		SFXGenerateRequest{
			Model:    defaultFalSoundEffectsVideoModel,
			Video:    "data:video/mp4;base64,CCC",
			Prompt:   "distant thunder, footsteps on gravel",
			Duration: 45,
		},
		builtinFalOverrides())
	if err != nil {
		t.Fatalf("resolveSFXBody: %v", err)
	}
	if got, ok := body["video_url"].(string); !ok || !strings.HasPrefix(got, "data:video/") {
		t.Fatalf("video_url = %v, want the source clip data URI", body["video_url"])
	}
	if got, ok := body["text_prompt"].(string); !ok || got != "distant thunder, footsteps on gravel" {
		t.Fatalf("text_prompt = %v, want the steering description", body["text_prompt"])
	}
	if got := body["num_samples"]; got != 1 {
		t.Fatalf("num_samples = %v, want 1 (per-sample billing)", got)
	}
	if got := body["duration"]; got != float64(45) {
		t.Fatalf("duration = %v, want 45", got)
	}
	if _, present := body["preserve_speech"]; present {
		t.Fatalf("preserve_speech must stay unset when the call gives no opinion, got %v", body["preserve_speech"])
	}
	if len(notices) != 0 {
		t.Fatalf("expected no notices on the default endpoint, got %v", notices)
	}
}

// TestResolveSFXBodyMMAudioV2 verifies the MMAudio dialect: the steering
// description lands on prompt (not text_prompt), and num_samples is never
// sent — the endpoint declares no such input, and the pin is schema-guarded.
func TestResolveSFXBodyMMAudioV2(t *testing.T) {
	body, notices, err := resolveSFXBody(loadSchema(t, "mmaudio-v2"),
		SFXGenerateRequest{
			Model:  "fal-ai/mmaudio-v2",
			Video:  "data:video/mp4;base64,CCC",
			Prompt: "waves crashing on rocks",
		},
		builtinFalOverrides())
	if err != nil {
		t.Fatalf("resolveSFXBody: %v", err)
	}
	if got, ok := body["prompt"].(string); !ok || got != "waves crashing on rocks" {
		t.Fatalf("prompt = %v, want the steering description", body["prompt"])
	}
	if _, present := body["text_prompt"]; present {
		t.Fatalf("text_prompt must not be sent to mmaudio, got %v", body["text_prompt"])
	}
	if _, present := body["num_samples"]; present {
		t.Fatalf("num_samples must not be sent to a model without the input, got %v", body["num_samples"])
	}
	if len(notices) != 0 {
		t.Fatalf("expected no notices, got %v", notices)
	}
}

// TestResolveSFXBodyVideoRequired is the tool's-purpose guard (the lipsync
// face-source rule): a model whose schema declares no video input is refused
// up front instead of 422ing downstream with a bare "field required".
func TestResolveSFXBodyVideoRequired(t *testing.T) {
	_, _, err := resolveSFXBody(loadSchema(t, "sfx-v2"),
		SFXGenerateRequest{
			Model:  "fal-ai/elevenlabs/sound-effects/v2",
			Video:  "data:video/mp4;base64,CCC",
			Prompt: "rain",
		},
		builtinFalOverrides())
	if err == nil {
		t.Fatal("expected a hard error for a model with no video input, got nil")
	}
	if !strings.Contains(err.Error(), "no video input") {
		t.Fatalf("error = %q, want it to name the missing video input", err.Error())
	}
}

// TestResolveSFXBodyNoSchema verifies the nil-schema generic fallback maps the
// source video plus the steering inputs' raw names, with the schema-unavailable
// notice.
func TestResolveSFXBodyNoSchema(t *testing.T) {
	preserve := false
	body, notices, err := resolveSFXBody(nil,
		SFXGenerateRequest{
			Model:          "mirelo-ai/sfx2.0/video-to-video",
			Video:          "data:video/mp4;base64,CCC",
			Prompt:         "rain",
			NegativePrompt: "music",
			Duration:       12,
			PreserveSpeech: &preserve,
		},
		builtinFalOverrides())
	if err != nil {
		t.Fatalf("resolveSFXBody: %v", err)
	}
	if len(notices) == 0 {
		t.Fatal("expected the schema-unavailable notice")
	}
	if got, ok := body["video_url"].(string); !ok || !strings.HasPrefix(got, "data:video/") {
		t.Fatalf("video_url = %v, want the source clip", body["video_url"])
	}
	if body["prompt"] != "rain" || body["negative_prompt"] != "music" || body["duration"] != float64(12) {
		t.Fatalf("generic body = %v, want the raw-name steering inputs", body)
	}
	if body["preserve_speech"] != false {
		t.Fatalf("preserve_speech = %v, want false", body["preserve_speech"])
	}
}

// TestResolveSFXBodyDurationClamped pins the clamp stance on mirelo's declared
// 1–600 bounds: an over-ask clamps down with a notice instead of 422ing, and a
// sub-second clip (whose probed length floors below the endpoint's minimum)
// clamps up.
func TestResolveSFXBodyDurationClamped(t *testing.T) {
	schema := loadSchema(t, "mirelo-sfx2.0-video-to-video")
	body, notices, err := resolveSFXBody(schema,
		SFXGenerateRequest{Model: defaultFalSoundEffectsVideoModel, Video: "data:video/mp4;base64,CCC", Prompt: "rain", Duration: 900},
		builtinFalOverrides())
	if err != nil {
		t.Fatalf("resolveSFXBody: %v", err)
	}
	if got := body["duration"]; got != float64(600) {
		t.Fatalf("duration = %v, want clamped to 600", got)
	}
	if len(notices) == 0 || !strings.Contains(notices[0], "maximum") {
		t.Fatalf("notices = %v, want the maximum clamp", notices)
	}

	body, notices, err = resolveSFXBody(schema,
		SFXGenerateRequest{Model: defaultFalSoundEffectsVideoModel, Video: "data:video/mp4;base64,CCC", Prompt: "rain", Duration: 0.4},
		builtinFalOverrides())
	if err != nil {
		t.Fatalf("resolveSFXBody: %v", err)
	}
	if got := body["duration"]; got != float64(1) {
		t.Fatalf("duration = %v, want clamped up to 1", got)
	}
	if len(notices) == 0 || !strings.Contains(notices[0], "minimum") {
		t.Fatalf("notices = %v, want the minimum clamp", notices)
	}
}

// TestResolveSFXBodyPreserveSpeech verifies the dialogue-preservation toggle:
// it maps onto preserve_speech when the model declares the control and drops
// with a notice when it doesn't (mmaudio).
func TestResolveSFXBodyPreserveSpeech(t *testing.T) {
	preserve := false
	body, notices, err := resolveSFXBody(loadSchema(t, "mirelo-sfx2.0-video-to-video"),
		SFXGenerateRequest{Model: defaultFalSoundEffectsVideoModel, Video: "data:video/mp4;base64,CCC", Prompt: "rain", PreserveSpeech: &preserve},
		builtinFalOverrides())
	if err != nil {
		t.Fatalf("resolveSFXBody: %v", err)
	}
	if body["preserve_speech"] != false {
		t.Fatalf("preserve_speech = %v, want false", body["preserve_speech"])
	}
	if len(notices) != 0 {
		t.Fatalf("expected no notices on the preserving endpoint, got %v", notices)
	}

	body, notices, err = resolveSFXBody(loadSchema(t, "mmaudio-v2"),
		SFXGenerateRequest{Model: "fal-ai/mmaudio-v2", Video: "data:video/mp4;base64,CCC", Prompt: "waves", PreserveSpeech: &preserve},
		builtinFalOverrides())
	if err != nil {
		t.Fatalf("resolveSFXBody: %v", err)
	}
	if _, present := body["preserve_speech"]; present {
		t.Fatalf("preserve_speech must be dropped on a model without the control, got %v", body["preserve_speech"])
	}
	if len(notices) == 0 || !strings.Contains(notices[0], "speech-preservation") {
		t.Fatalf("notices = %v, want the dropped-control notice", notices)
	}
}

// TestSoundEffectsVideoToolDeliversVideo pins the video-with-soundtrack path:
// the endpoint's video result stages as a ToolVideoResult, so the existing
// artifact/carry-forward pipeline picks it up unchanged.
func TestSoundEffectsVideoToolDeliversVideo(t *testing.T) {
	tools := HarnessToolExecutionContext{
		Config:         defaultAppConfig(),
		AttachedVideos: []string{"data:video/mp4;base64,QQ=="},
		GenerateSFX: func(ctx context.Context, req SFXGenerateRequest) (SFXGenerateResult, error) {
			return SFXGenerateResult{Video: GeneratedVideo{Data: tinyMP4(), MimeType: "video/mp4"}}, nil
		},
	}
	result, summary, err := soundEffectsVideoToolDefinition("fal").Execute(context.Background(), tools,
		HarnessToolCall{Name: "add_sound_effects", Content: "rain on a window"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	typed, ok := result.(ToolVideoResult)
	if !ok {
		t.Fatalf("result = %T, want ToolVideoResult", result)
	}
	if typed.Count != 1 || len(typed.Videos) != 1 {
		t.Fatalf("tool result = %+v, want one video", typed)
	}
	if !strings.Contains(summary, "added sound effects") {
		t.Fatalf("summary = %q, want it to name the delivered video", summary)
	}
}

// TestSoundEffectsVideoToolDeliversAudio pins the standalone-soundtrack path:
// video-to-audio endpoints stage as a ToolAudioResult (which carries forward
// to replace_audio) and the summary names that chain.
func TestSoundEffectsVideoToolDeliversAudio(t *testing.T) {
	tools := HarnessToolExecutionContext{
		Config:         defaultAppConfig(),
		AttachedVideos: []string{"data:video/mp4;base64,QQ=="},
		GenerateSFX: func(ctx context.Context, req SFXGenerateRequest) (SFXGenerateResult, error) {
			return SFXGenerateResult{Audio: GeneratedAudio{Data: []byte("fake-mp3"), MimeType: "audio/mpeg"}}, nil
		},
	}
	result, summary, err := soundEffectsVideoToolDefinition("fal").Execute(context.Background(), tools,
		HarnessToolCall{Name: "add_sound_effects", Content: "rain on a window"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	typed, ok := result.(ToolAudioResult)
	if !ok {
		t.Fatalf("result = %T, want ToolAudioResult", result)
	}
	if typed.Count != 1 || len(typed.Audios) != 1 {
		t.Fatalf("tool result = %+v, want one audio clip", typed)
	}
	if !strings.Contains(summary, "replace_audio") {
		t.Fatalf("summary = %q, want it to name the replace_audio chain", summary)
	}
}

// TestSoundEffectsVideoToolRequiresVideo mirrors lip_sync's attachment guard:
// a call with no attached video fails with the attach-first remedy, and the
// content requirement is the planner's correction message.
func TestSoundEffectsVideoToolRequiresVideo(t *testing.T) {
	tools := HarnessToolExecutionContext{
		Config: defaultAppConfig(),
		GenerateSFX: func(ctx context.Context, req SFXGenerateRequest) (SFXGenerateResult, error) {
			t.Fatal("GenerateSFX must not run without an attached video")
			return SFXGenerateResult{}, nil
		},
	}
	_, _, err := soundEffectsVideoToolDefinition("fal").Execute(context.Background(), tools,
		HarnessToolCall{Name: "add_sound_effects", Content: "rain"})
	if err == nil || !strings.Contains(err.Error(), "requires an attached video") {
		t.Fatalf("err = %v, want the attached-video guard", err)
	}
	if errs := soundEffectsVideoToolDefinition("fal").Validate("toolCalls[0]", HarnessToolCall{Name: "add_sound_effects"}); len(errs) != 1 || !strings.Contains(errs[0], ".content is required") {
		t.Fatalf("validate = %v, want the content requirement", errs)
	}
}

// TestSoundEffectsVideoToolGating confirms add_sound_effects is registered
// only when a fal video sound-effects model is configured AND a fal.ai key is
// present — the generate_sound gate on the video slot.
func TestSoundEffectsVideoToolGating(t *testing.T) {
	keyring.MockInit()
	t.Cleanup(func() { _ = clearFalAPIKey() })

	base := defaultAppConfig()
	base.Providers.Fal.SoundEffectsVideoModel = ""
	if soundEffectsVideoConfigured(base) {
		t.Fatal("video sound effects should not be configured without a model")
	}
	if _, ok := defaultHarnessToolRegistry(context.Background(), base, nil).Get("add_sound_effects"); ok {
		t.Fatal("add_sound_effects should be absent when unconfigured")
	}

	// Model set but no key → still not configured (would only fail at call time).
	noKey := defaultAppConfig()
	noKey.Providers.Fal.SoundEffectsVideoModel = "mirelo-ai/some/sfx"
	if soundEffectsVideoConfigured(noKey) {
		t.Fatal("video sound effects should not be configured without a fal key")
	}
	if _, ok := defaultHarnessToolRegistry(context.Background(), noKey, nil).Get("add_sound_effects"); ok {
		t.Fatal("add_sound_effects should be absent without a fal key")
	}

	if err := saveFalAPIKey("fal-test-key"); err != nil {
		t.Fatalf("saveFalAPIKey: %v", err)
	}
	configured := defaultAppConfig()
	configured.Providers.Fal.SoundEffectsVideoModel = "mirelo-ai/some/sfx"
	if !soundEffectsVideoConfigured(configured) {
		t.Fatal("video sound effects should be configured with a model and key")
	}
	if _, ok := defaultHarnessToolRegistry(context.Background(), configured, nil).Get("add_sound_effects"); !ok {
		t.Fatal("add_sound_effects should be registered when configured")
	}
	if got := resolveDefaultSoundEffectsVideoModel(configured); got != "mirelo-ai/some/sfx" {
		t.Fatalf("resolveDefaultSoundEffectsVideoModel = %q, want the configured model", got)
	}
	if got := resolveDefaultSoundEffectsVideoModel(base); got != defaultFalSoundEffectsVideoModel {
		t.Fatalf("resolveDefaultSoundEffectsVideoModel fallback = %q, want %q", got, defaultFalSoundEffectsVideoModel)
	}
}

// TestMergeAppConfigSeedsSoundEffectsVideoModel pins the audio-family seed:
// anyone with speech configured gets add_sound_effects without a Settings
// visit; a config with no audio model at all stays untouched (no new tools
// appear), and an explicit soundEffectsVideoModel always wins.
func TestMergeAppConfigSeedsSoundEffectsVideoModel(t *testing.T) {
	legacy := defaultAppConfig()
	legacy.Providers.Fal.AudioModel = "fal-ai/elevenlabs/tts/multilingual-v2"
	legacy.Providers.Fal.SoundEffectsVideoModel = ""
	merged := mergeAppConfig(legacy)
	if merged.Providers.Fal.SoundEffectsVideoModel != defaultFalSoundEffectsVideoModel {
		t.Fatalf("merged soundEffectsVideoModel = %q, want the default %q", merged.Providers.Fal.SoundEffectsVideoModel, defaultFalSoundEffectsVideoModel)
	}

	untouched := defaultAppConfig()
	untouched.Providers.Fal.AudioModel = ""
	untouched.Providers.Fal.SoundEffectsVideoModel = ""
	if merged := mergeAppConfig(untouched); merged.Providers.Fal.SoundEffectsVideoModel != "" {
		t.Fatalf("no audio model → no seed, got %q", merged.Providers.Fal.SoundEffectsVideoModel)
	}

	explicit := defaultAppConfig()
	explicit.Providers.Fal.AudioModel = "fal-ai/elevenlabs/tts/multilingual-v2"
	explicit.Providers.Fal.SoundEffectsVideoModel = "mirelo-ai/sfx2.0/video-to-audio"
	if merged := mergeAppConfig(explicit); merged.Providers.Fal.SoundEffectsVideoModel != "mirelo-ai/sfx2.0/video-to-audio" {
		t.Fatalf("explicit model must survive merge, got %q", merged.Providers.Fal.SoundEffectsVideoModel)
	}
}

// TestHarnessPlanSchemaAdmitsPreserveSpeech pins that the format-schema
// planner grammar frees add_sound_effects's preserveSpeech like the per-tool
// param schema does — the two are maintained separately, and a grammar without
// the property silently takes the dialogue-preservation control away from
// every non-native-tools planner.
func TestHarnessPlanSchemaAdmitsPreserveSpeech(t *testing.T) {
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
	if _, ok := properties["preserveSpeech"].(map[string]any); !ok {
		t.Errorf("plan grammar toolCalls properties missing preserveSpeech (has %v)", propertiesKeys(properties))
	}
}

// TestApplyKwargsMapsSFXPreserveSpeech pins the kwarg-dialect decode: a planner
// that emits its call Python-style still fills the preserveSpeech field the
// JSON plan path fills through the struct tag.
func TestApplyKwargsMapsSFXPreserveSpeech(t *testing.T) {
	var call HarnessToolCall
	applyKwargs(&call, "content='Thunder over gravel.', preserve_speech=false")
	if call.Content != "Thunder over gravel." {
		t.Fatalf("applyKwargs content = %q", call.Content)
	}
	if call.PreserveSpeech == nil || *call.PreserveSpeech {
		t.Fatalf("applyKwargs preserveSpeech = %v, want false", call.PreserveSpeech)
	}
}

// TestIsFalVideoSFXModel pins the picker predicate: the mirelo/mmaudio/
// cassetteai video endpoints match on id or tags, while the text-only SFX
// generators (generate_sound's domain) and unrelated transforms stay out.
func TestIsFalVideoSFXModel(t *testing.T) {
	for _, id := range []string{
		"mirelo-ai/sfx2.0/video-to-video",
		"mirelo-ai/sfx2.0/video-to-audio",
		"fal-ai/mmaudio-v2",
		"cassetteai/video-sound-effects-generator",
		"fal-ai/pixverse/sound-effects",
	} {
		if !isFalVideoSFXModel(FalModel{ID: id}) {
			t.Errorf("isFalVideoSFXModel(%q) = false, want true", id)
		}
	}
	if isFalVideoSFXModel(FalModel{ID: "fal-ai/kling-video/o3/pro/video-to-video/edit"}) {
		t.Error("plain video-to-video edit matched the sfx predicate")
	}
	if isFalVideoSFXModel(FalModel{ID: "fal-ai/wan/v2.2-a14b/text-to-video"}) {
		t.Error("text-to-video matched the sfx predicate")
	}
	// Tag matches for ids without the marker.
	if !isFalVideoSFXModel(FalModel{ID: "acme/soundtrack", Tags: []string{"sound-effects", "foley"}}) {
		t.Error("tagged sound-effects entry should match")
	}
}

// TestHarnessAddsSoundEffectsViaFal is the add_sound_effects happy path: a
// turn with an attached clip routes to audio mode, the planner emits the tool,
// the mirelo-shaped endpoint receives the hosted clip with the description and
// a pinned single sample, and the returned video-with-soundtrack lands in the
// conversation as a video artifact. The result JSON carries the ARRAY video
// shape (one File per sample) to exercise the tolerant decode.
func TestHarnessAddsSoundEffectsViaFal(t *testing.T) {
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
	config.Providers.Ollama.Models.Harness = "harness-model"
	config.Providers.Fal.SoundEffectsVideoModel = defaultFalSoundEffectsVideoModel
	if err := writeAppConfig(config); err != nil {
		t.Fatalf("writeAppConfig: %v", err)
	}

	clip := "data:video/mp4;base64," + base64.StdEncoding.EncodeToString(tinyMP4())
	const cdnSource = "https://v3.test.fal.media/files/abc/sfx-source.mp4"

	cdnUpload := false
	submittedModelPath := ""
	var submitted map[string]any
	app := NewApp()
	harnessCalls := 0
	app.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if strings.HasPrefix(req.URL.Path, "/v1/models/pricing") {
			return falTestPricingResponse(), nil
		}
		switch {
		case strings.Contains(req.URL.String(), "storage/auth/token"):
			return jsonResponse(`{"token":"cdn-test-token","token_type":"Bearer","base_url":"https://v3.test.fal.media"}`), nil
		case strings.HasSuffix(req.URL.Path, "/files/upload"):
			cdnUpload = true
			return jsonResponse(`{"access_url":"` + cdnSource + `","uploaded":true}`), nil
		case strings.Contains(req.URL.Path, "/api/openapi/"):
			// Minimized mirelo sfx2.0 video-to-video shape: video_url +
			// text_prompt + preserve_speech + the per-sample billing dial.
			return jsonResponse(`{"components":{"schemas":{"MireloSFX20VideoToVideoInput":{"type":"object","required":["video_url"],"properties":{"video_url":{"type":"string"},"text_prompt":{"type":"string"},"negative_prompt":{"type":"string"},"preserve_speech":{"type":"boolean","default":true},"duration":{"type":"number","minimum":1,"maximum":600,"default":10},"num_samples":{"type":"integer","minimum":1,"maximum":4,"default":2}}}}}}`), nil
		case strings.Contains(req.URL.Host, "fal.run"):
			if req.Method == http.MethodPost {
				submittedModelPath = req.URL.Path
				var body map[string]any
				if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
					t.Fatalf("decode fal submit body: %v", err)
				}
				submitted = body
				return jsonResponse(`{"request_id":"req-sfx-1"}`), nil
			}
			if strings.HasSuffix(req.URL.Path, "/status") {
				return jsonResponse(`{"status":"COMPLETED"}`), nil
			}
			if strings.Contains(req.URL.Path, "/requests/") {
				return jsonResponse(`{"video":[{"url":"https://queue.fal.run/sounded.mp4","content_type":"video/mp4"}]}`), nil
			}
			// Download of the generated clip.
			return &http.Response{StatusCode: 200, Status: "200 OK",
				Body:   io.NopCloser(bytes.NewReader(tinyMP4())),
				Header: http.Header{"Content-Type": []string{"video/mp4"}}}, nil
		}
		switch req.URL.Path {
		case "/api/show":
			return jsonResponse(`{"capabilities":[],"model_info":{},"details":{"family":"test","parameter_size":"1B"}}`), nil
		case "/api/chat":
			payload := chatPayload(t, req)
			if payload["stream"] == false {
				switch payload["model"] {
				case "harness-model":
					harnessCalls++
					switch harnessCalls {
					case 1: // triage
						return chatCompletion("harness-model", `{"needsTools":true,"responseMode":"audio","toolTask":"Add rain sound effects to the attached clip.","reason":"The user asked for sound effects on a video."}`), nil
					case 2: // plan round 1
						return chatCompletion("harness-model", `{"brief":"Soundtrack the clip.","needsTools":true,"reason":"sfx","toolCalls":[{"name":"add_sound_effects","content":"rain on a window, distant thunder","preserveSpeech":false}]}`), nil
					case 3: // plan round 2 — the evidence is in, wrap up
						return chatCompletion("harness-model", `{"brief":"Done.","needsTools":false,"reason":"done","toolCalls":[]}`), nil
					}
					t.Fatalf("unexpected harness call #%d", harnessCalls)
					return nil, nil
				default:
					// Title generation runs once, on the primary model, after
					// the turn — not part of the harness sequence.
					return chatCompletion("chat-box-model", `"Rain clip"`), nil
				}
			}
			body := fmt.Sprintln(`{"model":"chat-box-model","message":{"role":"assistant","content":"Here is your clip with rain."},"done":false}`) +
				fmt.Sprintln(`{"model":"chat-box-model","done":true,"done_reason":"stop","eval_count":3}`)
			return &http.Response{StatusCode: 200, Status: "200 OK",
				Body:   io.NopCloser(strings.NewReader(body)),
				Header: http.Header{"Content-Type": []string{"application/x-ndjson"}}}, nil
		default:
			t.Fatalf("unexpected request %s %s", req.Method, req.URL)
			return nil, nil
		}
	})

	app.runChatStream(context.Background(), "request-fal-sfx", ChatRequest{
		BaseURL: "http://ollama.test",
		Model:   "chat-box-model",
		Messages: []ChatMessage{{
			Role:    "user",
			Content: "Add rain sound effects to this clip.",
			Videos:  []string{clip},
		}},
	})

	if submitted == nil {
		t.Fatal("add_sound_effects never submitted to fal — the tool did not run")
	}
	if submittedModelPath != "/"+defaultFalSoundEffectsVideoModel {
		t.Fatalf("submitted to %q, want the video sound-effects model %q", submittedModelPath, "/"+defaultFalSoundEffectsVideoModel)
	}
	if !cdnUpload {
		t.Fatal("expected the source clip to upload to fal's CDN")
	}
	if submitted["video_url"] != cdnSource {
		t.Fatalf("submitted video_url = %v, want the CDN-hosted source", submitted["video_url"])
	}
	if submitted["text_prompt"] != "rain on a window, distant thunder" {
		t.Errorf("submitted text_prompt = %v", submitted["text_prompt"])
	}
	if submitted["preserve_speech"] != false {
		t.Errorf("submitted preserve_speech = %v, want false", submitted["preserve_speech"])
	}
	if submitted["num_samples"] != float64(1) {
		t.Errorf("submitted num_samples = %v, want the pinned single sample", submitted["num_samples"])
	}

	conversations, err := listConversations(config.Storage)
	if err != nil || len(conversations) == 0 {
		t.Fatalf("listConversations: %v (%d)", err, len(conversations))
	}
	detail, err := getConversation(config.Storage, conversations[0].ID)
	if err != nil {
		t.Fatalf("getConversation: %v", err)
	}
	assistant := detail.Turns[len(detail.Turns)-1]
	hasVideo := false
	for _, c := range assistant.Content {
		if c.Type == "video" {
			hasVideo = true
		}
	}
	if !hasVideo {
		t.Fatalf("assistant turn has no video artifact: %+v", assistant.Content)
	}
}
