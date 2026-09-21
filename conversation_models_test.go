package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestOverlayModelOverrides(t *testing.T) {
	baseConfig := func() AppConfig {
		config := defaultAppConfig()
		config.Models.PrimaryProvider = "ollama"
		config.Models.HarnessProvider = "ollama"
		config.Models.ImageProvider = "ollama"
		config.Providers.Ollama.Models.Primary = "global-primary"
		config.Providers.Ollama.Models.Harness = "global-harness"
		config.Providers.Ollama.Models.Image = "global-image"
		config.Providers.OpenRouter.Primary = "or-primary"
		config.Providers.OpenRouter.Harness = "or-harness"
		config.Providers.OpenAICompatible.Primary = "oai-primary"
		config.Providers.OpenAICompatible.Harness = "oai-harness"
		config.Providers.OpenAICompatible.Model = "oai-image"
		config.Providers.Fal.Model = "fal-image"
		config.Providers.Fal.VideoModel = "fal-video"
		config.Providers.Local.Whisper.Model = "whisper-small"
		return config
	}
	baseReq := func() ChatRequest {
		return ChatRequest{Provider: "ollama", Model: "global-primary", SelectedModel: "global-primary"}
	}

	cases := []struct {
		name      string
		overrides ConversationModelOverrides
		assert    func(t *testing.T, config AppConfig, req ChatRequest)
	}{
		{
			name:      "empty overrides change nothing",
			overrides: ConversationModelOverrides{},
			assert: func(t *testing.T, config AppConfig, req ChatRequest) {
				want := baseConfig()
				if !reflect.DeepEqual(config, want) {
					t.Fatalf("config changed under an empty override:\n%+v\nwant\n%+v", config, want)
				}
				if req.Model != "global-primary" || req.Provider != "ollama" {
					t.Fatalf("request rewritten under an empty override: %+v", req)
				}
			},
		},
		{
			name:      "primary pair rewrites request and config slots",
			overrides: ConversationModelOverrides{PrimaryProvider: "openrouter", PrimaryModel: "conv-primary"},
			assert: func(t *testing.T, config AppConfig, req ChatRequest) {
				if config.Models.PrimaryProvider != "openrouter" || config.Providers.OpenRouter.Primary != "conv-primary" {
					t.Fatalf("primary config = %q/%q", config.Models.PrimaryProvider, config.Providers.OpenRouter.Primary)
				}
				if req.Provider != "openrouter" || req.Model != "conv-primary" || req.SelectedModel != "conv-primary" {
					t.Fatalf("primary request = %+v", req)
				}
			},
		},
		{
			name:      "primary provider only inherits that provider's global model",
			overrides: ConversationModelOverrides{PrimaryProvider: "openai-compatible"},
			assert: func(t *testing.T, config AppConfig, req ChatRequest) {
				if req.Provider != "openai-compatible" || req.Model != "oai-primary" {
					t.Fatalf("primary request = %+v", req)
				}
				if config.Providers.OpenAICompatible.Primary != "oai-primary" {
					t.Fatalf("openai-compatible primary slot = %q", config.Providers.OpenAICompatible.Primary)
				}
			},
		},
		{
			name:      "primary model only keeps the global provider",
			overrides: ConversationModelOverrides{PrimaryModel: "conv-primary"},
			assert: func(t *testing.T, config AppConfig, req ChatRequest) {
				if config.Models.PrimaryProvider != "ollama" || config.Providers.Ollama.Models.Primary != "conv-primary" {
					t.Fatalf("primary config = %q/%q", config.Models.PrimaryProvider, config.Providers.Ollama.Models.Primary)
				}
				if req.Model != "conv-primary" {
					t.Fatalf("primary request model = %q", req.Model)
				}
			},
		},
		{
			name:      "harness pair rewrites harness routing",
			overrides: ConversationModelOverrides{HarnessProvider: "openrouter", HarnessModel: "conv-harness"},
			assert: func(t *testing.T, config AppConfig, req ChatRequest) {
				if config.Models.HarnessProvider != "openrouter" || config.Providers.OpenRouter.Harness != "conv-harness" {
					t.Fatalf("harness config = %q/%q", config.Models.HarnessProvider, config.Providers.OpenRouter.Harness)
				}
				if req.Model != "global-primary" {
					t.Fatalf("harness override must not touch the primary request model: %q", req.Model)
				}
			},
		},
		{
			name:      "image provider and model route to the fal slot",
			overrides: ConversationModelOverrides{ImageProvider: "fal", ImageModel: "fal-ai/conv-image"},
			assert: func(t *testing.T, config AppConfig, req ChatRequest) {
				if config.Models.ImageProvider != "fal" || config.Providers.Fal.Model != "fal-ai/conv-image" {
					t.Fatalf("image config = %q/%q", config.Models.ImageProvider, config.Providers.Fal.Model)
				}
			},
		},
		{
			name:      "image model only lands on the inherited provider's slot",
			overrides: ConversationModelOverrides{ImageModel: "conv-image"},
			assert: func(t *testing.T, config AppConfig, req ChatRequest) {
				if config.Providers.Ollama.Models.Image != "conv-image" {
					t.Fatalf("ollama image slot = %q", config.Providers.Ollama.Models.Image)
				}
			},
		},
		{
			name: "fal endpoints map one to one",
			overrides: ConversationModelOverrides{
				VideoModel:      "fal-ai/conv-video",
				AudioModel:      "fal-ai/conv-audio",
				TranscribeModel: "fal-ai/conv-transcribe",
			},
			assert: func(t *testing.T, config AppConfig, req ChatRequest) {
				if config.Providers.Fal.VideoModel != "fal-ai/conv-video" ||
					config.Providers.Fal.AudioModel != "fal-ai/conv-audio" ||
					config.Providers.Fal.TranscribeModel != "fal-ai/conv-transcribe" {
					t.Fatalf("fal slots = %+v", config.Providers.Fal)
				}
				if config.Providers.Fal.Model != "fal-image" {
					t.Fatalf("untouched fal slot changed: %+v", config.Providers.Fal)
				}
			},
		},
		{
			name:      "transcription provider, whisper model and binary",
			overrides: ConversationModelOverrides{TranscriptionProvider: "local-whisper", WhisperModel: "large-v3", WhisperBinary: "/opt/whisper-cli"},
			assert: func(t *testing.T, config AppConfig, req ChatRequest) {
				if config.Models.TranscriptionProvider != "local-whisper" || config.Providers.Local.Whisper.Model != "large-v3" || config.Providers.Local.Whisper.Binary != "/opt/whisper-cli" {
					t.Fatalf("transcription = %q/%q/%q", config.Models.TranscriptionProvider, config.Providers.Local.Whisper.Model, config.Providers.Local.Whisper.Binary)
				}
			},
		},
		{
			name: "generation defaults",
			overrides: ConversationModelOverrides{
				ImageAspectRatio: "16:9",
				ImageSizePreset:  "2k",
				ImageSteps:       12,
				VideoDuration:    "10",
				VideoAspectRatio: "9:16",
			},
			assert: func(t *testing.T, config AppConfig, req ChatRequest) {
				if config.Generation.Image.AspectRatio != "16:9" || config.Generation.Image.SizePreset != "2k" || config.Generation.Image.Steps != 12 {
					t.Fatalf("image generation defaults = %+v", config.Generation.Image)
				}
				if config.Generation.Video.Duration != "10" || config.Generation.Video.AspectRatio != "9:16" {
					t.Fatalf("video generation defaults = %+v", config.Generation.Video)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			config, req, err := overlayModelOverrides(baseConfig(), baseReq(), tc.overrides)
			if err != nil {
				t.Fatalf("overlayModelOverrides returned error: %v", err)
			}
			tc.assert(t, config, req)
		})
	}
}

func TestNormalizeAndValidateConversationModelOverrides(t *testing.T) {
	normalized := normalizeConversationModelOverrides(ConversationModelOverrides{
		PrimaryModel:  "  conv-primary  ",
		HarnessModel:  "   ",
		WhisperModel:  "large-v3 ",
		ImageSteps:    -3,
		VideoDuration: " 10 ",
	})
	if normalized.PrimaryModel != "conv-primary" || normalized.HarnessModel != "" || normalized.WhisperModel != "large-v3" {
		t.Fatalf("normalized = %+v", normalized)
	}
	if normalized.ImageSteps != 0 {
		t.Fatalf("negative ImageSteps must normalize to 0 (inherit), got %d", normalized.ImageSteps)
	}
	if normalized.VideoDuration != "10" {
		t.Fatalf("VideoDuration not trimmed: %q", normalized.VideoDuration)
	}
	if conversationModelOverridesActive(normalizeConversationModelOverrides(ConversationModelOverrides{PrimaryModel: "  "})) {
		t.Fatal("whitespace-only override must normalize to inactive")
	}
	if conversationModelOverridesActive(ConversationModelOverrides{}) {
		t.Fatal("empty override must be inactive")
	}

	invalid := []ConversationModelOverrides{
		{PrimaryProvider: "fal"},
		{HarnessProvider: "nope"},
		{ImageProvider: "openrouter"},
		{TranscriptionProvider: "ollama"},
	}
	for _, overrides := range invalid {
		if err := validateConversationModelOverrides(overrides); err == nil {
			t.Fatalf("override %+v must fail validation", overrides)
		}
	}
	if err := validateConversationModelOverrides(ConversationModelOverrides{
		PrimaryProvider:       "openrouter",
		HarnessProvider:       "openai-compatible",
		ImageProvider:         "fal",
		TranscriptionProvider: "local-whisper",
	}); err != nil {
		t.Fatalf("valid override failed validation: %v", err)
	}
}

func TestSetConversationModelOverrides(t *testing.T) {
	storage := libraryTestStorage(t)
	conversation := libraryConversationFixture("conv_models", "Models", "2026-09-01T10:00:00Z", "")
	writeSearchConversation(t, storage, "2026/09/conv_models", conversation)

	overrides := ConversationModelOverrides{PrimaryModel: "conv-primary", VideoModel: "fal-ai/conv-video"}
	summary, err := setConversationModelOverrides(storage, "conv_models", overrides)
	if err != nil {
		t.Fatalf("setConversationModelOverrides returned error: %v", err)
	}
	if summary.ModelOverrides == nil || summary.ModelOverrides.PrimaryModel != "conv-primary" {
		t.Fatalf("summary overrides = %+v", summary.ModelOverrides)
	}
	detail, err := getConversation(storage, "conv_models")
	if err != nil {
		t.Fatalf("getConversation returned error: %v", err)
	}
	if detail.Conversation.ModelOverrides == nil || detail.Conversation.ModelOverrides.VideoModel != "fal-ai/conv-video" {
		t.Fatalf("record overrides = %+v", detail.Conversation.ModelOverrides)
	}

	// An all-empty override clears (normalization already dropped the
	// whitespace, so this is the cleared shape the bound method produces).
	cleared, err := setConversationModelOverrides(storage, "conv_models", normalizeConversationModelOverrides(ConversationModelOverrides{PrimaryModel: " "}))
	if err != nil {
		t.Fatalf("clear returned error: %v", err)
	}
	if cleared.ModelOverrides != nil {
		t.Fatalf("cleared summary overrides = %+v, want nil", cleared.ModelOverrides)
	}
	detail, err = getConversation(storage, "conv_models")
	if err != nil {
		t.Fatalf("getConversation returned error: %v", err)
	}
	if detail.Conversation.ModelOverrides != nil {
		t.Fatalf("cleared record overrides = %+v, want nil", detail.Conversation.ModelOverrides)
	}

	if _, err := setConversationModelOverrides(storage, "conv_missing", overrides); err == nil {
		t.Fatal("setting overrides on an unknown conversation must fail")
	}

	deleted := libraryConversationFixture("conv_deleted", "Deleted", "2026-09-01T10:00:00Z", "")
	deleted.DeletedAt = "2026-09-01T11:00:00Z"
	writeSearchConversation(t, storage, "2026/09/conv_deleted", deleted)
	if _, err := setConversationModelOverrides(storage, "conv_deleted", overrides); err == nil {
		t.Fatal("setting overrides on a deleted conversation must fail")
	}
}

func TestSetConversationModelOverridesRefusedWhileStreaming(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	config := defaultAppConfig()
	config.Storage = ConfigStorage{
		Root:      filepath.Join(home, ".atelier"),
		History:   filepath.Join(home, ".atelier", "history"),
		Artifacts: filepath.Join(home, ".atelier", "history"),
	}
	if err := writeAppConfig(config); err != nil {
		t.Fatalf("writeAppConfig returned error: %v", err)
	}

	app := NewApp()
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	app.streamsMu.Lock()
	app.streams["request-streaming"] = cancel
	app.streamConversations["request-streaming"] = "conv_streaming"
	app.streamsMu.Unlock()

	if _, err := app.SetConversationModelOverrides("conv_streaming", ConversationModelOverrides{PrimaryModel: "conv-primary"}); err == nil ||
		!strings.Contains(err.Error(), "still running") {
		t.Fatalf("streaming refusal = %v, want still-running error", err)
	}
}

// TestStreamChatAppliesConversationModelOverrides drives the real StreamChat
// path against an existing conversation whose record carries a primary and a
// harness override, with the request still naming the global models: the
// record must win (workspace semantics) end to end — the wire requests and
// the persisted run steps all show the overridden models — and the append
// must preserve the override on the record.
func TestStreamChatAppliesConversationModelOverrides(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	config := defaultAppConfig()
	config.Storage = ConfigStorage{
		Root:      filepath.Join(home, ".atelier"),
		History:   filepath.Join(home, ".atelier", "history"),
		Artifacts: filepath.Join(home, ".atelier", "history"),
	}
	config.Providers.Ollama.BaseURL = "http://ollama.test"
	config.Providers.Ollama.Models.Primary = "global-primary"
	config.Providers.Ollama.Models.Harness = "global-harness"
	if err := writeAppConfig(config); err != nil {
		t.Fatalf("writeAppConfig returned error: %v", err)
	}

	workspace := t.TempDir()
	conversation := HistoryConversation{
		SchemaVersion: currentConversationSchemaVersion,
		ID:            "conv_models",
		Kind:          "chat",
		Title:         "Models",
		CreatedAt:     "2026-09-01T10:00:00Z",
		UpdatedAt:     "2026-09-01T10:00:00Z",
		Workspace:     workspace,
	}
	writeSearchConversation(t, config.Storage, "2026/09/conv_models", conversation)
	if _, err := setConversationModelOverrides(config.Storage, "conv_models", ConversationModelOverrides{
		PrimaryModel: "conv-primary",
		HarnessModel: "conv-harness",
	}); err != nil {
		t.Fatalf("setConversationModelOverrides returned error: %v", err)
	}

	app := NewApp()
	app.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/api/chat" {
			return notFoundResponse(), nil
		}
		var payload map[string]any
		data, _ := io.ReadAll(req.Body)
		if err := json.Unmarshal(data, &payload); err != nil {
			t.Fatalf("provider request body is not JSON: %v", err)
		}
		if payload["stream"] == false {
			if payload["model"] != "conv-harness" {
				t.Errorf("triage request model = %v, want conv-harness (the overridden harness)", payload["model"])
			}
			decision := `{"needsTools":false,"responseMode":"text","toolTask":"","reason":"General knowledge answer."}`
			body := `{"model":"conv-harness","message":{"role":"assistant","content":` + fmt.Sprintf("%q", decision) + `},"done":true}`
			return jsonResponse(body), nil
		}
		if payload["model"] != "conv-primary" {
			t.Errorf("streaming request model = %v, want conv-primary (the overridden primary)", payload["model"])
		}
		body := fmt.Sprintln(`{"model":"conv-primary","message":{"role":"assistant","content":"Later."},"done":false}`) +
			fmt.Sprintln(`{"model":"conv-primary","done":true,"done_reason":"stop","eval_count":1}`)
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     http.Header{"Content-Type": []string{"application/x-ndjson"}},
		}, nil
	})

	start, err := app.StreamChat(ChatRequest{
		RequestID:      "request-models",
		ConversationID: "conv_models",
		BaseURL:        "http://ollama.test",
		Model:          "global-primary",
		Messages: []ChatMessage{
			{Role: "user", Content: "Say hello"},
		},
	})
	if err != nil {
		t.Fatalf("StreamChat returned error: %v", err)
	}
	waitForStreamCleanup(t, app, start.RequestID)

	run := persistedHarnessRun(t, config, 1)
	steps, _ := run["steps"].([]any)
	if got := harnessStepByKind(t, steps, "triage")["model"]; got != "conv-harness" {
		t.Fatalf("triage step model = %v, want conv-harness", got)
	}
	if got := harnessStepByKind(t, steps, "streaming")["model"]; got != "conv-primary" {
		t.Fatalf("streaming step model = %v, want conv-primary", got)
	}

	// The append path rewrote conversation.json from its own load — the
	// override must survive that round-trip.
	detail, err := getConversation(config.Storage, "conv_models")
	if err != nil {
		t.Fatalf("getConversation returned error: %v", err)
	}
	if detail.Conversation.ModelOverrides == nil || detail.Conversation.ModelOverrides.PrimaryModel != "conv-primary" {
		t.Fatalf("record overrides after append = %+v", detail.Conversation.ModelOverrides)
	}
}

// TestConversationModelOverridesEveryFieldOverlays walks
// ConversationModelOverrides reflectively: each field, set alone to a probe
// value, must survive normalization (trim included) and change the overlaid
// config. A field added to the struct without its normalize entry or overlay
// line fails here instead of silently rendering as a no-op override in the
// conversation screen — the UI would show and accept the edit while the
// payload never carries it.
func TestConversationModelOverridesEveryFieldOverlays(t *testing.T) {
	baseConfig := func() AppConfig {
		config := defaultAppConfig()
		config.Models.PrimaryProvider = "ollama"
		config.Models.HarnessProvider = "ollama"
		config.Models.ImageProvider = "ollama"
		config.Providers.Ollama.Models.Primary = "global-primary"
		config.Providers.Ollama.Models.Harness = "global-harness"
		config.Providers.Ollama.Models.Image = "global-image"
		return config
	}
	req := ChatRequest{Provider: "ollama", Model: "global-primary", SelectedModel: "global-primary"}

	// Probes that must land somewhere the base config doesn't already match.
	// Provider fields need valid ids — the overlay normalizes unknowns away —
	// and the primary pair resolves its model from the request when the
	// provider's global slot is empty, so these can't ride the generic probe.
	probes := map[string]any{
		"PrimaryProvider":       "openrouter",
		"HarnessProvider":       "openrouter",
		"ImageProvider":         "fal",
		"TranscriptionProvider": "local-whisper",
		"ImageSteps":            7,
	}

	structType := reflect.TypeOf(ConversationModelOverrides{})
	fieldCount := 0
	for i := range structType.NumField() {
		field := structType.Field(i)
		fieldCount++
		probe, ok := probes[field.Name]
		if !ok {
			if field.Type.Kind() == reflect.Int {
				t.Fatalf("field %s needs an explicit int probe", field.Name)
			}
			// Whitespace wraps the probe so the same pass also proves the
			// field has an entry in normalizeConversationModelOverrides.
			probe = " probe/" + field.Name + " "
		}
		overrides := ConversationModelOverrides{}
		reflect.ValueOf(&overrides).Elem().Field(i).Set(reflect.ValueOf(probe))

		normalized := normalizeConversationModelOverrides(overrides)
		if !conversationModelOverridesActive(normalized) {
			t.Fatalf("%s: probe did not survive normalization", field.Name)
		}
		if s, isString := probe.(string); isString {
			if got := reflect.ValueOf(normalized).Field(i).String(); got != strings.TrimSpace(s) {
				t.Fatalf("%s: normalize did not trim the probe: %q", field.Name, got)
			}
		}

		overlaid, _, err := overlayModelOverrides(baseConfig(), req, normalized)
		if err != nil {
			t.Fatalf("%s: overlayModelOverrides returned error: %v", field.Name, err)
		}
		baseJSON, err := json.Marshal(baseConfig())
		if err != nil {
			t.Fatalf("marshal base config: %v", err)
		}
		overlaidJSON, err := json.Marshal(overlaid)
		if err != nil {
			t.Fatalf("marshal overlaid config: %v", err)
		}
		if string(baseJSON) == string(overlaidJSON) {
			t.Fatalf("%s: probe did not change the overlaid config — the overlay is missing its line", field.Name)
		}
	}
	// Guard the guard: a broken reflection walk must not pass vacuously.
	if fieldCount < 20 {
		t.Fatalf("reflected only %d fields — the walk is broken", fieldCount)
	}
}

// TestApplyConversationModelOverridesReadRules pins the source-of-truth rules:
// turn 1 without a request override is a no-op, a turn-1 request override is
// applied (the workspace/project lifecycle), an all-empty request override is
// dropped, and for an existing conversation the record wins — the request
// field never applies. An unknown conversation fails rather than silently
// running on global models.
func TestApplyConversationModelOverridesReadRules(t *testing.T) {
	config := defaultAppConfig()
	config.Providers.Ollama.Models.Primary = "global-primary"
	config.Providers.Ollama.Models.Harness = "global-harness"
	req := ChatRequest{Model: "global-primary", Messages: []ChatMessage{{Role: "user", Content: "hi"}}}

	sameConfig, sameReq, err := applyConversationModelOverrides(config, req)
	if err != nil {
		t.Fatalf("turn-1 apply without overrides returned error: %v", err)
	}
	if !reflect.DeepEqual(sameConfig, config) || !reflect.DeepEqual(sameReq, req) {
		t.Fatal("turn 1 without a request override must be a no-op")
	}

	// Turn 1 with a request override: applied like a record override, and
	// normalized by the pin helper the creation paths persist.
	pinnedConfig, pinnedReq, err := applyConversationModelOverrides(config, ChatRequest{
		Model:          "global-primary",
		ModelOverrides: &ConversationModelOverrides{PrimaryModel: " conv-primary ", HarnessModel: "conv-harness"},
	})
	if err != nil {
		t.Fatalf("turn-1 apply with overrides returned error: %v", err)
	}
	if pinnedReq.Model != "conv-primary" || pinnedReq.SelectedModel != "conv-primary" {
		t.Fatalf("turn-1 primary request = %+v", pinnedReq)
	}
	if pinnedConfig.Providers.Ollama.Models.Harness != "conv-harness" {
		t.Fatalf("turn-1 harness slot = %q", pinnedConfig.Providers.Ollama.Models.Harness)
	}
	pin := requestModelOverrides(ChatRequest{ModelOverrides: &ConversationModelOverrides{PrimaryModel: " conv-primary "}})
	if pin == nil || pin.PrimaryModel != "conv-primary" {
		t.Fatalf("requestModelOverrides = %+v, want trimmed pin", pin)
	}
	if requestModelOverrides(ChatRequest{ModelOverrides: &ConversationModelOverrides{PrimaryModel: "  "}}) != nil {
		t.Fatal("an all-empty request override must pin nil")
	}

	storage := libraryTestStorage(t)
	config.Storage = storage
	// Existing conversation without overrides: the request field is ignored.
	existing := libraryConversationFixture("conv_plain", "Plain", "2026-09-01T10:00:00Z", "")
	writeSearchConversation(t, storage, "2026/09/conv_plain", existing)
	plainConfig, plainReqOut, err := applyConversationModelOverrides(config, ChatRequest{
		ConversationID: "conv_plain",
		Model:          "global-primary",
		ModelOverrides: &ConversationModelOverrides{PrimaryModel: "must-not-apply"},
	})
	if err != nil {
		t.Fatalf("existing-conversation apply returned error: %v", err)
	}
	if plainReqOut.Model != "global-primary" || plainConfig.Providers.Ollama.Models.Primary != "global-primary" {
		t.Fatalf("request override leaked onto an existing conversation: %+v", plainReqOut)
	}

	req.ConversationID = "conv_missing"
	if _, _, err := applyConversationModelOverrides(config, req); err == nil {
		t.Fatal("applying overrides for an unknown conversation must fail")
	}
}

// TestStreamChatTurnOneCarriesRequestModelOverrides proves the turn-1 flow
// end to end: a first message whose request carries model overrides runs on
// them (wire requests + persisted steps), pins them onto the newly created
// record, and a second turn — with no request override at all — keeps running
// on them from the record.
func TestStreamChatTurnOneCarriesRequestModelOverrides(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	config := defaultAppConfig()
	config.Storage = ConfigStorage{
		Root:      filepath.Join(home, ".atelier"),
		History:   filepath.Join(home, ".atelier", "history"),
		Artifacts: filepath.Join(home, ".atelier", "history"),
	}
	config.Providers.Ollama.BaseURL = "http://ollama.test"
	config.Providers.Ollama.Models.Primary = "global-primary"
	config.Providers.Ollama.Models.Harness = "global-harness"
	if err := writeAppConfig(config); err != nil {
		t.Fatalf("writeAppConfig returned error: %v", err)
	}

	app := NewApp()
	sendTurn := func(requestID, conversationID string) string {
		start, err := app.StreamChat(ChatRequest{
			RequestID:      requestID,
			ConversationID: conversationID,
			BaseURL:        "http://ollama.test",
			Model:          "global-primary",
			Messages:       []ChatMessage{{Role: "user", Content: "Say hello"}},
			// Only the first send carries the override — the second turn must
			// pick it up from the record instead.
			ModelOverrides: &ConversationModelOverrides{PrimaryModel: "conv-primary", HarnessModel: "conv-harness"},
		})
		if err != nil {
			t.Fatalf("StreamChat (%s) returned error: %v", requestID, err)
		}
		waitForStreamCleanup(t, app, start.RequestID)
		return start.ConversationID
	}
	app.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/api/chat" {
			return notFoundResponse(), nil
		}
		var payload map[string]any
		data, _ := io.ReadAll(req.Body)
		if err := json.Unmarshal(data, &payload); err != nil {
			t.Fatalf("provider request body is not JSON: %v", err)
		}
		if payload["stream"] == false {
			if payload["model"] != "conv-harness" {
				t.Errorf("triage request model = %v, want conv-harness", payload["model"])
			}
			decision := `{"needsTools":false,"responseMode":"text","toolTask":"","reason":"General knowledge answer."}`
			body := `{"model":"conv-harness","message":{"role":"assistant","content":` + fmt.Sprintf("%q", decision) + `},"done":true}`
			return jsonResponse(body), nil
		}
		if payload["model"] != "conv-primary" {
			t.Errorf("streaming request model = %v, want conv-primary", payload["model"])
		}
		body := fmt.Sprintln(`{"model":"conv-primary","message":{"role":"assistant","content":"Later."},"done":false}`) +
			fmt.Sprintln(`{"model":"conv-primary","done":true,"done_reason":"stop","eval_count":1}`)
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     http.Header{"Content-Type": []string{"application/x-ndjson"}},
		}, nil
	})

	conversationID := sendTurn("request-turn-one", "")
	detail, err := getConversation(config.Storage, conversationID)
	if err != nil {
		t.Fatalf("getConversation returned error: %v", err)
	}
	if detail.Conversation.ModelOverrides == nil ||
		detail.Conversation.ModelOverrides.PrimaryModel != "conv-primary" ||
		detail.Conversation.ModelOverrides.HarnessModel != "conv-harness" {
		t.Fatalf("created record overrides = %+v", detail.Conversation.ModelOverrides)
	}

	// Second turn: no request override — the record carries it.
	sendTurn("request-turn-two", conversationID)
	run := persistedHarnessRun(t, config, 2)
	steps, _ := run["steps"].([]any)
	if got := harnessStepByKind(t, steps, "triage")["model"]; got != "conv-harness" {
		t.Fatalf("turn-2 triage step model = %v, want conv-harness", got)
	}
	if got := harnessStepByKind(t, steps, "streaming")["model"]; got != "conv-primary" {
		t.Fatalf("turn-2 streaming step model = %v, want conv-primary", got)
	}
}
