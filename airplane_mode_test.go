package main

// Tests for airplane mode (airplane_mode.go): the chat-provider refusal and
// loopback enforcement, the tool-gate filtering, the forced local-whisper
// transcription backend, the editor inpaint refusal, the updater stand-down,
// and the up-front turn failure in RunChatStream. The disk-read seam
// (airplaneDiskConfig) is pinned OFF for the whole package in TestMain — the
// developer's own config.json must not decide what these tests see — and
// re-pinned per test via pinAirplaneDiskConfig.

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// pinAirplaneDiskConfig pins the airplane seam for one test: airplane mode on
// (with the given config as the disk read) or off.
func pinAirplaneDiskConfig(t *testing.T, config AppConfig, airplane bool) {
	t.Helper()
	previous := airplaneDiskConfig
	t.Cleanup(func() { airplaneDiskConfig = previous })
	airplaneDiskConfig = func() (AppConfig, bool) { return config, airplane }
}

// airplaneTestConfig is an isolated airplane-mode config: every role local,
// Ollama on loopback, storage under a temp HOME.
func airplaneTestConfig(t *testing.T) AppConfig {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	config := defaultAppConfig()
	config.AirplaneMode = true
	config.Storage = ConfigStorage{
		Root:      filepath.Join(home, ".atelier"),
		History:   filepath.Join(home, ".atelier", "history"),
		Artifacts: filepath.Join(home, ".atelier", "history"),
	}
	config.Providers.Ollama.BaseURL = "http://127.0.0.1:11434"
	config.Providers.OpenAICompatible.BaseURL = "http://127.0.0.1:8080"
	if err := writeAppConfig(config); err != nil {
		t.Fatalf("writeAppConfig returned error: %v", err)
	}
	return config
}

func TestAirplaneChatProviderError(t *testing.T) {
	on := defaultAppConfig()
	on.AirplaneMode = true
	on.Providers.OpenAICompatible.BaseURL = "http://127.0.0.1:8080"
	off := on
	off.AirplaneMode = false

	t.Run("mode off allows everything", func(t *testing.T) {
		for _, provider := range []string{"openrouter", "ollama", "openai-compatible"} {
			if err := airplaneChatProviderError(off, provider, "http://10.0.0.1:11434"); err != nil {
				t.Errorf("airplane off: %s should pass, got %v", provider, err)
			}
		}
	})

	t.Run("openrouter refused", func(t *testing.T) {
		err := airplaneChatProviderError(on, "openrouter", "")
		if !errors.Is(err, errAirplaneModeActive) {
			t.Fatalf("openrouter should be refused with the airplane sentinel, got %v", err)
		}
		if !strings.Contains(err.Error(), "Settings") {
			t.Errorf("refusal should name where to fix it, got %q", err.Error())
		}
	})

	t.Run("local providers on loopback allowed", func(t *testing.T) {
		if err := airplaneChatProviderError(on, "ollama", "http://127.0.0.1:11434"); err != nil {
			t.Errorf("loopback Ollama should pass, got %v", err)
		}
		if err := airplaneChatProviderError(on, "ollama", ""); err != nil {
			t.Errorf("empty base URL falls back to the configured endpoint and should pass, got %v", err)
		}
		if err := airplaneChatProviderError(on, "openai-compatible", ""); err != nil {
			t.Errorf("loopback OpenAI-compatible endpoint should pass, got %v", err)
		}
	})

	t.Run("non-local endpoints refused", func(t *testing.T) {
		if err := airplaneChatProviderError(on, "ollama", "http://192.0.2.10:11434"); !errors.Is(err, errAirplaneModeActive) {
			t.Errorf("remote Ollama endpoint should be refused, got %v", err)
		}
		remote := on
		remote.Providers.OpenAICompatible.BaseURL = "http://api.example.local:1234"
		if err := airplaneChatProviderError(remote, "openai-compatible", ""); !errors.Is(err, errAirplaneModeActive) {
			t.Errorf("remote OpenAI-compatible endpoint should be refused, got %v", err)
		}
	})
}

func TestIsLoopbackEndpoint(t *testing.T) {
	cases := []struct {
		url  string
		want bool
	}{
		{"", true}, // empty means the configured default, which is loopback
		{"http://127.0.0.1:11434", true},
		{"http://localhost:11434", true},
		{"http://LOCALHOST:11434", true},
		{"http://[::1]:11434", true},
		{"http://127.8.8.8:11434", true}, // the whole 127/8 is loopback
		{"http://192.0.2.1:11434", false},
		{"http://ollama.test:11434", false},
		{"http://0.0.0.0:11434", false},
		{"nonsense", false},
	}
	for _, tc := range cases {
		if got := isLoopbackEndpoint(tc.url); got != tc.want {
			t.Errorf("isLoopbackEndpoint(%q) = %v, want %v", tc.url, got, tc.want)
		}
	}
}

// TestAirplaneRegistryDropsCloudTools builds the turn-time tool catalog under
// airplane mode and asserts every cloud-only tool is gone while the local
// surface survives.
func TestAirplaneRegistryDropsCloudTools(t *testing.T) {
	config := defaultAppConfig()
	config.AirplaneMode = true
	// A fully configured fal setup: without airplane mode this registry would
	// carry every cloud tool.
	config.Providers.Fal.VideoModel = "some-video-model"
	config.Providers.Fal.AudioModel = "some-speech-model"
	config.Providers.Fal.SoundEffectsModel = "some-sound-model"
	config.Models.ImageProvider = "fal"
	config.Providers.Fal.Model = "some-image-model"

	registry := defaultHarnessToolRegistry(context.Background(), config, nil)
	for _, name := range []string{
		"generate_video", "generate_speech", "generate_sound", "extend_audio",
		"lip_sync", "upscale_image", "upscale_video", "reframe_video", "restyle_video",
	} {
		if _, ok := registry.Get(name); ok {
			t.Errorf("%s should not be offered in airplane mode", name)
		}
	}
	if _, ok := registry.Get("generate_image"); ok {
		t.Error("generate_image should not be offered in airplane mode when the routed image provider is fal")
	}
	// The local surface survives: workspace tools, open_editor, and (when a
	// whisper binary resolves) local transcription.
	if _, ok := registry.Get("open_editor"); !ok {
		t.Error("open_editor should stay offered in airplane mode")
	}
	for _, name := range []string{"list_files", "read_file", "write_file", "run_command"} {
		if _, ok := registry.Get(name); !ok {
			t.Errorf("%s should stay offered in airplane mode", name)
		}
	}

	// Transcription switches to the local whisper backend: refused without a
	// binary, offered once one resolves.
	stubLocalLookup(t, map[string]string{})
	if _, ok := defaultHarnessToolRegistry(context.Background(), config, nil).Get("transcribe_audio"); ok {
		t.Error("transcribe_audio should not be offered in airplane mode without a whisper binary")
	}
	stubLocalLookup(t, map[string]string{"whisper": "/usr/local/bin/whisper"})
	if _, ok := defaultHarnessToolRegistry(context.Background(), config, nil).Get("transcribe_audio"); !ok {
		t.Error("transcribe_audio should be offered in airplane mode once a whisper binary resolves")
	}
}

// TestAirplaneImageGenerationGateIsRouteAware asserts generate_image stays
// offered when the routed backend is local and drops only when it is cloud.
func TestAirplaneImageGenerationGateIsRouteAware(t *testing.T) {
	base := defaultAppConfig()
	base.AirplaneMode = true

	local := base
	local.Providers.Ollama.Models.Image = "local-vision"
	if !imageGenerationConfigured(local) {
		t.Error("ollama-routed generate_image should stay configured in airplane mode")
	}
	compatible := base
	compatible.Models.ImageProvider = "openai-compatible"
	compatible.Providers.OpenAICompatible.Model = "local-diffusion"
	if !imageGenerationConfigured(compatible) {
		t.Error("openai-compatible-routed generate_image should stay configured in airplane mode")
	}
	cloud := base
	cloud.Models.ImageProvider = "replicate"
	cloud.Providers.Replicate.Model = "cloud-model"
	if imageGenerationConfigured(cloud) {
		t.Error("replicate-routed generate_image should not be configured in airplane mode")
	}
}

// TestResolveTranscriptionProviderAirplaneForcesLocalWhisper pins the
// airplane variant of the auto-resolve: never fal, even with a fal key
// present and the fal backend pinned in config.
func TestResolveTranscriptionProviderAirplaneForcesLocalWhisper(t *testing.T) {
	config := defaultAppConfig()
	config.AirplaneMode = true
	config.Models.TranscriptionProvider = transcriptionProviderFal
	// localBinaryLookPath is pinned off by TestMain: no whisper binary.
	if got := resolveTranscriptionProvider(config); got != transcriptionProviderLocalWhisper {
		t.Fatalf("airplane mode must resolve transcription to local whisper, got %q", got)
	}
	if transcribeAudioConfigured(config) {
		t.Error("transcribe_audio should not be configured without a whisper binary, even with fal pinned")
	}
}

// TestResolveRefusesCloudProvidersInAirplaneMode drives ProviderRegistry
// .Resolve with the airplane seam pinned on — the outer belt behind the
// engine's up-front check.
func TestResolveRefusesCloudProvidersInAirplaneMode(t *testing.T) {
	config := airplaneTestConfig(t)
	pinAirplaneDiskConfig(t, config, true)
	app := NewApp()

	if _, err := newProviderRegistry(app).Resolve("openrouter", ""); !errors.Is(err, errAirplaneModeActive) {
		t.Fatalf("openrouter should be refused in airplane mode, got %v", err)
	}
	if _, err := newProviderRegistry(app).Resolve("ollama", "http://192.0.2.10:11434"); !errors.Is(err, errAirplaneModeActive) {
		t.Fatalf("a remote Ollama endpoint should be refused in airplane mode, got %v", err)
	}
	if _, err := newProviderRegistry(app).Resolve("ollama", "http://127.0.0.1:11434"); err != nil {
		t.Fatalf("a loopback Ollama endpoint should resolve in airplane mode, got %v", err)
	}
}

// TestAirplaneTurnFailsUpFront runs a full turn whose request names the
// OpenRouter provider while airplane mode is on: the turn must fail before
// any provider HTTP call, persist the failed run, and surface the remedy.
func TestAirplaneTurnFailsUpFront(t *testing.T) {
	config := airplaneTestConfig(t)

	app := NewApp()
	calls := 0
	app.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		return notFoundResponse(), nil
	})

	app.runChatStream(context.Background(), "request-airplane", ChatRequest{
		Provider: "openrouter",
		Model:    "some/cloud-model",
		Messages: []ChatMessage{{Role: "user", Content: "Hello"}},
	})

	if calls != 0 {
		t.Fatalf("airplane mode must fail the turn before any provider call, saw %d", calls)
	}
	turn := persistedAssistantTurn(t, config, 1)
	errText, _ := turn.ProviderResponse["error"].(string)
	if !strings.Contains(errText, "airplane mode") {
		t.Fatalf("failed turn providerResponse.error = %q, want the airplane remedy", errText)
	}
	run, ok := turn.ProviderResponse["harnessRun"].(map[string]any)
	if !ok {
		t.Fatalf("failed turn missing harness run: %+v", turn.ProviderResponse)
	}
	if run["status"] != "failed" {
		t.Fatalf("failed turn run status = %v, want failed", run["status"])
	}
	loop, _ := run["loop"].(map[string]any)
	if reason, _ := loop["stopReason"].(string); reason != "airplane_mode_blocked" {
		t.Fatalf("run loop stopReason = %v, want airplane_mode_blocked", loop["stopReason"])
	}
	// The refusal fires before triage, so the ledger carries no model-call
	// steps — nothing was burned on a turn policy had already decided.
	if steps, _ := run["steps"].([]any); len(steps) == 0 {
		t.Fatal("expected the queued step to persist")
	}
	for _, raw := range run["steps"].([]any) {
		step, _ := raw.(map[string]any)
		if kind, _ := step["kind"].(string); kind == "triage" || kind == "model_call" || kind == "planning" {
			t.Fatalf("no model call should run on an airplane-refused turn, found %q step", kind)
		}
	}
}

// TestAirplaneTitleGenerationFallsBack asserts the title call is skipped
// under airplane mode instead of reaching for a cloud provider.
func TestAirplaneTitleGenerationFallsBack(t *testing.T) {
	config := airplaneTestConfig(t)
	app := NewApp()
	app.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		t.Error("no HTTP call should be made for the title under airplane mode")
		return notFoundResponse(), nil
	})
	req := ChatRequest{
		Provider: "openrouter",
		Model:    "some/cloud-model",
		Messages: []ChatMessage{{Role: "user", Content: "Explain quaternion rotations in game engines"}},
	}
	title := app.generateConversationTitle(config, req, "assistant answer")
	if title == "" {
		t.Fatal("expected the prompt-derived fallback title, got empty")
	}
}

// TestCheckForUpdatesStandsDownInAirplaneMode asserts the shared check path
// returns the airplane error state without fetching the manifest, so both the
// manual button and the automatic scheduler stand down.
func TestCheckForUpdatesStandsDownInAirplaneMode(t *testing.T) {
	config := airplaneTestConfig(t)
	pinAirplaneDiskConfig(t, config, true)

	app := NewApp()
	app.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		t.Error("no manifest fetch should happen in airplane mode")
		return notFoundResponse(), nil
	})
	app.updateClient.Transport = app.client.Transport

	status := app.checkForUpdates(context.Background(), true)
	if status.State != updateStateError {
		t.Fatalf("status.State = %q, want %q", status.State, updateStateError)
	}
	if !strings.Contains(status.Error, "Airplane mode") {
		t.Errorf("status.Error should name airplane mode, got %q", status.Error)
	}
}

// TestAirplaneMediaAndInpaintErrors unit-drives the gateway-level refusals.
func TestAirplaneMediaAndInpaintErrors(t *testing.T) {
	on := defaultAppConfig()
	on.AirplaneMode = true
	off := on
	off.AirplaneMode = false

	if err := airplaneMediaError(off, "generate_video"); err != nil {
		t.Errorf("mode off should allow generate_video, got %v", err)
	}
	for _, tool := range []string{"generate_video", "lip_sync", "upscale_image", "extend_audio", "transcribe_audio"} {
		if err := airplaneMediaError(on, tool); !errors.Is(err, errAirplaneModeActive) {
			t.Errorf("%s should be refused in airplane mode, got %v", tool, err)
		}
	}
	// generate_image is route-aware: refused when the routed provider is a
	// cloud one, allowed when it is local. An unset ImageProvider routes to
	// Ollama (the default), so the refused case pins fal explicitly.
	falImage := on
	falImage.Models.ImageProvider = "fal"
	if err := airplaneMediaError(falImage, "generate_image"); !errors.Is(err, errAirplaneModeActive) {
		t.Errorf("fal-routed generate_image should be refused, got %v", err)
	} else if !strings.Contains(err.Error(), "generate_image") {
		t.Errorf("refusal should name the tool, got %v", err)
	}
	localImage := on
	localImage.Providers.Ollama.Models.Image = "local-vision"
	if err := airplaneMediaError(localImage, "generate_image"); err != nil {
		t.Errorf("ollama-routed generate_image should be allowed, got %v", err)
	}

	if err := airplaneInpaintError(on); !errors.Is(err, errAirplaneModeActive) {
		t.Errorf("inpaint should be refused in airplane mode, got %v", err)
	}
	if err := airplaneInpaintError(off); err != nil {
		t.Errorf("mode off should allow inpaint, got %v", err)
	}
}

// TestSubmitImageEditRefusesInpaintInAirplaneMode drives the editor's submit
// path: inpaint is refused before the provider pair resolves, crop is not.
func TestSubmitImageEditRefusesInpaintInAirplaneMode(t *testing.T) {
	config := airplaneTestConfig(t)
	app := NewApp()

	_, err := app.SubmitImageEdit(ImageEditSubmitRequest{Kind: editOperationKindInpaint})
	if !errors.Is(err, errAirplaneModeActive) {
		t.Fatalf("inpaint should be refused in airplane mode, got %v", err)
	}
	// Crop is local work: it must NOT trip the airplane refusal (it fails
	// later on its own validation instead).
	_, err = app.SubmitImageEdit(ImageEditSubmitRequest{Kind: editOperationKindCrop})
	if errors.Is(err, errAirplaneModeActive) {
		t.Fatalf("crop must not be blocked by airplane mode, got %v", err)
	}
	_ = config
}

// TestTriageSystemPromptCarriesAirplaneRouting asserts the airplane paragraph
// rides the triage prompt only while the mode is on.
func TestTriageSystemPromptCarriesAirplaneRouting(t *testing.T) {
	registry := newHarnessToolRegistry(nil)
	withAirplane := triageSystemPrompt(registry, nil, "/tmp/ws", true)
	if !strings.Contains(withAirplane, "Airplane mode is ON") {
		t.Error("airplane triage prompt should carry the airplane routing paragraph")
	}
	without := triageSystemPrompt(registry, nil, "/tmp/ws", false)
	if strings.Contains(without, "Airplane mode is ON") {
		t.Error("non-airplane triage prompt should not carry the airplane routing paragraph")
	}
}

// TestAirplaneConfigRoundTrips asserts the flag survives a config
// write/load/merge cycle untouched.
func TestAirplaneConfigRoundTrips(t *testing.T) {
	airplaneTestConfig(t) // writes an airplane-on config under the temp HOME
	loaded, err := loadAppConfig()
	if err != nil {
		t.Fatalf("loadAppConfig returned error: %v", err)
	}
	if !loaded.AirplaneMode {
		t.Fatal("airplane mode should survive write → load → merge")
	}
	off := loaded
	off.AirplaneMode = false
	path, err := configPath()
	if err != nil {
		t.Fatalf("configPath returned error: %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"version":1}`), 0o644); err != nil {
		t.Fatalf("failed to write a pre-airplane config: %v", err)
	}
	legacy, err := loadAppConfig()
	if err != nil {
		t.Fatalf("loadAppConfig returned error: %v", err)
	}
	if legacy.AirplaneMode {
		t.Error("a config written before airplane mode existed must load with the mode off")
	}
	_ = path
}
