package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/zalando/go-keyring"
)

// TestLiveOllamaSpeechEndToEnd exercises the real /v1/audio/speech transport
// against a running Ollama when ATELIER_LIVE_OLLAMA_SPEECH names a pulled
// speech model (e.g. "phonon") — the updater_e2e_test gating pattern, skipped
// otherwise. It is the one check that depends on the server's actual bytes:
// the streaming WAV header must come back rewritten so the strict pure-Go
// sniffer measures the clip the way ffprobe would.
func TestLiveOllamaSpeechEndToEnd(t *testing.T) {
	model := strings.TrimSpace(os.Getenv("ATELIER_LIVE_OLLAMA_SPEECH"))
	if model == "" {
		t.Skip("set ATELIER_LIVE_OLLAMA_SPEECH=phonon to run against a live Ollama")
	}
	client := newOllamaClient(&http.Client{Timeout: 2 * time.Minute}, defaultOllamaBaseURL)
	generated, err := client.GenerateSpeech(context.Background(), model, "Zoey", "Hello from Atelier's speech provider test.")
	if err != nil {
		t.Fatalf("GenerateSpeech: %v", err)
	}
	if generated.MimeType != "audio/wav" {
		t.Fatalf("mime = %q, want audio/wav", generated.MimeType)
	}
	if got := binary.LittleEndian.Uint32(generated.Data[4:8]); got != uint32(len(generated.Data)-8) {
		t.Fatalf("RIFF size = %d, want %d (header not rewritten)", got, len(generated.Data)-8)
	}
	seconds, ok := probeAudioDuration(generated.Data)
	if !ok || seconds < 0.5 || seconds > 30 {
		t.Fatalf("probeAudioDuration = %.2fs ok=%v, want a measurable short clip", seconds, ok)
	}
	t.Logf("live %s: %d bytes, %.2fs", model, len(generated.Data), seconds)
}

// buildTestWAV assembles a minimal PCM WAV (RIFF + fmt + data + payload) with
// the given chunk-size fields, so tests can pin both well-formed and
// streaming-placeholder headers.
func buildTestWAV(riffSize, dataSize uint32, samples int) []byte {
	var out []byte
	out = append(out, []byte("RIFF")...)
	var riff [4]byte
	binary.LittleEndian.PutUint32(riff[:], riffSize)
	out = append(out, riff[:]...)
	out = append(out, []byte("WAVE")...)
	out = append(out, []byte("fmt ")...)
	var fmtSize [4]byte
	binary.LittleEndian.PutUint32(fmtSize[:], 16)
	out = append(out, fmtSize[:]...)
	fmtChunk := make([]byte, 16)
	binary.LittleEndian.PutUint16(fmtChunk[0:], 1)     // PCM
	binary.LittleEndian.PutUint16(fmtChunk[2:], 1)     // mono
	binary.LittleEndian.PutUint32(fmtChunk[4:], 24000) // sample rate
	binary.LittleEndian.PutUint32(fmtChunk[8:], 48000) // byte rate
	binary.LittleEndian.PutUint16(fmtChunk[12:], 2)    // block align
	binary.LittleEndian.PutUint16(fmtChunk[14:], 16)   // bits
	out = append(out, fmtChunk...)
	out = append(out, []byte("data")...)
	var data [4]byte
	binary.LittleEndian.PutUint32(data[:], dataSize)
	out = append(out, data[:]...)
	payload := make([]byte, samples*2)
	for i := range payload {
		payload[i] = byte(i % 251)
	}
	return append(out, payload...)
}

// TestFixStreamingWAVHeader pins the streaming-placeholder rewrite: a WAV
// emitted with 0xFFFFFFFF RIFF and data sizes (Ollama's /v1/audio/speech does
// exactly this) comes out with honest sizes measurable by the strict pure-Go
// sniffer, a well-formed file passes through untouched, and a non-WAV buffer
// is returned unchanged.
func TestFixStreamingWAVHeader(t *testing.T) {
	if !bytes.Equal(fixStreamingWAVHeader([]byte("not a wav")), []byte("not a wav")) {
		t.Fatal("non-WAV input must pass through unchanged")
	}
	wellFormed := buildTestWAV(uint32(36+8), 8, 4)
	fixed := fixStreamingWAVHeader(append([]byte{}, wellFormed...))
	if !bytes.Equal(fixed, wellFormed) {
		t.Fatal("a well-formed WAV header must not be rewritten")
	}
	streaming := buildTestWAV(0xFFFFFFFF, 0xFFFFFFFF, 4)
	fixed = fixStreamingWAVHeader(append([]byte(nil), streaming...))
	if got := binary.LittleEndian.Uint32(fixed[4:8]); got != uint32(len(fixed)-8) {
		t.Fatalf("RIFF size = %d, want %d", got, len(fixed)-8)
	}
	if got := binary.LittleEndian.Uint32(fixed[40:44]); got != 8 {
		t.Fatalf("data size = %d, want 8 (4 samples x 2 bytes)", got)
	}
	if seconds, ok := wavDuration(fixed); !ok || seconds < 0.0001 || seconds > 0.001 {
		t.Fatalf("wavDuration after fix = %.6f (ok=%v), want ~0.00033s", seconds, ok)
	}
	// The unpatched original stays unmeasurable — the reason the fix exists.
	if _, ok := wavDuration(streaming); ok {
		t.Fatal("the streaming header must stay unmeasurable to the strict sniffer")
	}
}

// TestOllamaGenerateSpeechTransport drives the /v1/audio/speech client against
// an httptest server: the WAV payload comes back with its placeholder header
// rewritten, the voice field is sent only when set, and the OpenAI-shaped and
// plain Ollama error bodies surface their message (a 404 names the version
// floor, since a pre-speech Ollama has no such route).
func TestOllamaGenerateSpeechTransport(t *testing.T) {
	streamingWAV := buildTestWAV(0xFFFFFFFF, 0xFFFFFFFF, 8)
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/audio/speech" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		gotBody = map[string]any{}
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "audio/wav")
		_, _ = w.Write(streamingWAV)
	}))
	defer server.Close()
	client := newOllamaClient(&http.Client{}, server.URL)

	generated, err := client.GenerateSpeech(context.Background(), "phonon", "Zoey", "hello")
	if err != nil {
		t.Fatalf("GenerateSpeech: %v", err)
	}
	if gotBody["model"] != "phonon" || gotBody["input"] != "hello" || gotBody["response_format"] != "wav" {
		t.Fatalf("request body = %v", gotBody)
	}
	if gotBody["voice"] != "Zoey" {
		t.Fatalf("voice should ride the body, got %v", gotBody["voice"])
	}
	if generated.MimeType != "audio/wav" {
		t.Fatalf("mime = %q", generated.MimeType)
	}
	if binary.LittleEndian.Uint32(generated.Data[40:44]) != 16 {
		t.Fatalf("data size not rewritten: %d", binary.LittleEndian.Uint32(generated.Data[40:44]))
	}

	if _, err := client.GenerateSpeech(context.Background(), "phonon", " ", "hello"); err != nil {
		t.Fatalf("GenerateSpeech: %v", err)
	}
	if _, hasVoice := gotBody["voice"]; hasVoice {
		t.Fatalf("a blank voice must be omitted, got %v", gotBody["voice"])
	}

	errorServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
			Voice string `json:"voice"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		switch {
		case body.Voice == "Nope":
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"voice \"Nope\" not found; available voices: Zoey","type":"invalid_request_error"}}`))
		case body.Model == "qwen3":
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"qwen3 does not support speech"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"message":"route not found"}}`))
		}
	}))
	defer errorServer.Close()
	errorClient := newOllamaClient(&http.Client{}, errorServer.URL)

	_, err = errorClient.GenerateSpeech(context.Background(), "phonon", "Nope", "hi")
	if err == nil || !strings.Contains(err.Error(), `voice "Nope" not found`) {
		t.Fatalf("OpenAI-shaped error body should surface its message, got %v", err)
	}
	_, err = errorClient.GenerateSpeech(context.Background(), "qwen3", "", "hi")
	if err == nil || !strings.Contains(err.Error(), "does not support speech") {
		t.Fatalf("plain error body should surface its message, got %v", err)
	}
	_, err = errorClient.GenerateSpeech(context.Background(), "phonon", "", "hi")
	if err == nil || !strings.Contains(err.Error(), "0.40.11") {
		t.Fatalf("404 should name the version floor, got %v", err)
	}
}

// TestSpeechProviderRouting pins the routing truth and both resolvers: unset
// or junk keeps the fal route (speech was fal-only before Ollama speech
// existed), "ollama" routes local and reads its own slot with the phonon
// belt, and mergeAppConfig normalizes like the other provider dimensions.
func TestSpeechProviderRouting(t *testing.T) {
	fal := defaultAppConfig()
	if got := resolveSpeechProvider(fal); got != "fal" {
		t.Fatalf("resolveSpeechProvider(default) = %q, want fal", got)
	}
	local := defaultAppConfig()
	local.Models.SpeechProvider = "ollama"
	local.Providers.Ollama.Models.Speech = "phonon:40m-de"
	if got := resolveSpeechProvider(local); got != "ollama" {
		t.Fatalf("resolveSpeechProvider(ollama) = %q, want ollama", got)
	}
	if got := resolveDefaultSpeechModel(local); got != "phonon:40m-de" {
		t.Fatalf("resolveDefaultSpeechModel = %q, want the ollama slot", got)
	}
	emptySlot := defaultAppConfig()
	emptySlot.Models.SpeechProvider = "ollama"
	if got := resolveDefaultSpeechModel(emptySlot); got != defaultOllamaSpeechModel {
		t.Fatalf("resolveDefaultSpeechModel(empty ollama slot) = %q, want %q", got, defaultOllamaSpeechModel)
	}
	falModel := defaultAppConfig()
	falModel.Providers.Fal.AudioModel = "fal-ai/tts"
	if got := resolveDefaultSpeechModel(falModel); got != "fal-ai/tts" {
		t.Fatalf("resolveDefaultSpeechModel(fal) = %q, want the fal slot", got)
	}

	merged := defaultAppConfig()
	merged.Models.SpeechProvider = "  ollama "
	if got := mergeAppConfig(merged).Models.SpeechProvider; got != "ollama" {
		t.Fatalf("mergeAppConfig should keep ollama, got %q", got)
	}
	junk := defaultAppConfig()
	junk.Models.SpeechProvider = "elevenlabs"
	if got := mergeAppConfig(junk).Models.SpeechProvider; got != "fal" {
		t.Fatalf("mergeAppConfig should normalize junk to fal, got %q", got)
	}
}

// TestSpeechGenerationToolGatingOllamaRoute extends the fal gate test: the
// local route needs only its model slot — no key, and airplane mode never
// drops it — and the registered description carries the local-provider marker
// the triage airplane paragraph sniffs.
func TestSpeechGenerationToolGatingOllamaRoute(t *testing.T) {
	keyring.MockInit()
	t.Cleanup(func() { _ = clearFalAPIKey() })

	local := defaultAppConfig()
	local.Models.SpeechProvider = "ollama"
	local.Providers.Ollama.Models.Speech = "phonon"
	if !speechGenerationConfigured(local) {
		t.Fatal("speech should be configured on the ollama route with just a model slot")
	}
	registry := defaultHarnessToolRegistry(context.Background(), local, nil)
	def, ok := registry.Get("generate_speech")
	if !ok {
		t.Fatal("generate_speech should be registered on the ollama route")
	}
	if !registry.SpeechServedLocally() {
		t.Fatal("the ollama-routed description should carry the local-provider marker")
	}
	if !strings.Contains(def.Description, "airplane mode") {
		t.Fatal("the local description should state airplane-mode availability")
	}

	local.Models.SpeechProvider = "ollama"
	local.Providers.Ollama.Models.Speech = ""
	if speechGenerationConfigured(local) {
		t.Fatal("speech should not be configured on the ollama route without a model slot")
	}
	if _, ok := defaultHarnessToolRegistry(context.Background(), local, nil).Get("generate_speech"); ok {
		t.Fatal("generate_speech should be absent without an ollama slot")
	}

	airplane := local
	airplane.Providers.Ollama.Models.Speech = "phonon"
	airplane.AirplaneMode = true
	if !speechGenerationConfigured(airplane) {
		t.Fatal("the ollama route must survive airplane mode")
	}
	falAirplane := defaultAppConfig()
	falAirplane.Providers.Fal.AudioModel = "fal-ai/tts"
	falAirplane.AirplaneMode = true
	if speechGenerationConfigured(falAirplane) {
		t.Fatal("the fal route must stay dropped under airplane mode")
	}
	falRegistry := defaultHarnessToolRegistry(context.Background(), defaultAppConfig(), nil)
	if falRegistry.SpeechServedLocally() {
		t.Fatal("an unregistered tool must not report local speech")
	}
	falRouted := defaultAppConfig()
	falRouted.Providers.Fal.AudioModel = "fal-ai/tts"
	if err := saveFalAPIKey("fal-test-key"); err != nil {
		t.Fatalf("saveFalAPIKey: %v", err)
	}
	if def, ok := defaultHarnessToolRegistry(context.Background(), falRouted, nil).Get("generate_speech"); !ok || strings.Contains(def.Description, speechLocalProviderMarker) {
		t.Fatal("the fal description must not carry the local-provider marker")
	}
}

// TestGenerateSpeechOllamaRouteStandsDownCloning pins the executor's local
// contract: an attached voice reference does not route the call onto fal's
// clone endpoint — the request runs the ollama slot with no SourceAudio, and
// the result carries a notice saying the clip was ignored.
func TestGenerateSpeechOllamaRouteStandsDownCloning(t *testing.T) {
	local := defaultAppConfig()
	local.Models.SpeechProvider = "ollama"
	local.Providers.Ollama.Models.Speech = "phonon"
	local.Providers.Fal.AudioModel = "fal-ai/tts"
	local.Providers.Fal.AudioCloneModel = "fal-ai/clone/model"

	var captured AudioGenerateRequest
	tools := HarnessToolExecutionContext{
		Config:         local,
		VoiceReference: "data:audio/wav;base64,QUJD",
		GenerateAudio: func(ctx context.Context, req AudioGenerateRequest) (GeneratedAudio, error) {
			captured = req
			return GeneratedAudio{Data: []byte("x"), MimeType: "audio/wav"}, nil
		},
	}
	def := speechGenerationToolDefinition(false, "ollama")
	out, _, err := def.Execute(context.Background(), tools, HarnessToolCall{Content: "say hi in my voice"})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if captured.Model != "phonon" {
		t.Fatalf("model = %q, want the ollama slot", captured.Model)
	}
	if captured.SourceAudio != "" {
		t.Fatalf("SourceAudio must stand down on the ollama route, got %q", captured.SourceAudio)
	}
	if captured.Kind != audioKindSpeech {
		t.Fatalf("Kind = %q, want speech", captured.Kind)
	}
	audio, ok := out.(ToolAudioResult)
	if !ok {
		t.Fatalf("expected ToolAudioResult, got %T", out)
	}
	found := false
	for _, notice := range audio.Notices {
		if strings.Contains(notice, "can't clone voices") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a cloning stand-down notice, got %v", audio.Notices)
	}

	// A named voice alone routes plainly, no notice.
	captured = AudioGenerateRequest{}
	out, _, err = def.Execute(context.Background(), tools, HarnessToolCall{Content: "hello", Voice: "Zoey"})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if captured.Voice != "Zoey" || captured.Model != "phonon" {
		t.Fatalf("named voice should pass through to the ollama slot, got %+v", captured)
	}
	audio = out.(ToolAudioResult)
	for _, notice := range audio.Notices {
		if strings.Contains(notice, "can't clone voices") {
			t.Fatalf("no stand-down notice expected without a reference, got %v", audio.Notices)
		}
	}

	// Activity attribution: the builder stamps ollama so usage reports don't
	// bill the local call to fal; the fal route leaves the engine's default.
	activity := def.Activity(HarnessToolResult{Result: ToolAudioResult{Model: "phonon"}})
	if activity.Provider != "ollama" {
		t.Fatalf("activity provider = %q, want ollama", activity.Provider)
	}
	if strings.Join(activity.Command, " ") != "ollama run phonon" {
		t.Fatalf("activity command = %v, want ollama run phonon", activity.Command)
	}
	falDef := speechGenerationToolDefinition(false, "fal")
	falActivity := falDef.Activity(HarnessToolResult{Result: ToolAudioResult{Model: "fal-ai/tts"}})
	if falActivity.Provider != "" {
		t.Fatalf("fal-routed activity should leave the engine's fal default, got %q", falActivity.Provider)
	}
}

// TestConversationModelOverridesSpeechRouting pins the override pair:
// SpeechProvider routes and AudioModel lands on the slot the effective
// provider reads — an ollama-pinned conversation stores on the Ollama Speech
// slot, a fal one on fal's AudioModel, and an unknown provider is rejected.
func TestConversationModelOverridesSpeechRouting(t *testing.T) {
	base := defaultAppConfig()
	base.Providers.Ollama.Models.Speech = "global-phonon"
	base.Providers.Fal.AudioModel = "global-fal-tts"

	localPinned, _, err := overlayModelOverrides(base, ChatRequest{}, ConversationModelOverrides{
		SpeechProvider: "ollama",
		AudioModel:     "pinned-phonon",
	})
	if err != nil {
		t.Fatalf("overlay: %v", err)
	}
	if localPinned.Providers.Ollama.Models.Speech != "pinned-phonon" {
		t.Fatalf("ollama slot = %q, want the override", localPinned.Providers.Ollama.Models.Speech)
	}
	if localPinned.Providers.Fal.AudioModel != "global-fal-tts" {
		t.Fatalf("fal slot must stay untouched, got %q", localPinned.Providers.Fal.AudioModel)
	}

	falPinned, _, err := overlayModelOverrides(base, ChatRequest{}, ConversationModelOverrides{
		SpeechProvider: "fal",
		AudioModel:     "pinned-fal-tts",
	})
	if err != nil {
		t.Fatalf("overlay: %v", err)
	}
	if falPinned.Providers.Fal.AudioModel != "pinned-fal-tts" {
		t.Fatalf("fal slot = %q, want the override", falPinned.Providers.Fal.AudioModel)
	}
	if falPinned.Providers.Ollama.Models.Speech != "global-phonon" {
		t.Fatalf("ollama slot must stay untouched, got %q", falPinned.Providers.Ollama.Models.Speech)
	}

	// Model-only override follows the conversation's global provider.
	globalLocal := defaultAppConfig()
	globalLocal.Models.SpeechProvider = "ollama"
	overlaid, _, err := overlayModelOverrides(globalLocal, ChatRequest{}, ConversationModelOverrides{AudioModel: "local-model"})
	if err != nil {
		t.Fatalf("overlay: %v", err)
	}
	if overlaid.Providers.Ollama.Models.Speech != "local-model" {
		t.Fatalf("model-only override should follow the global ollama route, got %q", overlaid.Providers.Ollama.Models.Speech)
	}

	if err := validateConversationModelOverrides(normalizeConversationModelOverrides(ConversationModelOverrides{SpeechProvider: "replicate"})); err == nil {
		t.Fatal("an unknown speech provider must be rejected")
	}
	if err := validateConversationModelOverrides(normalizeConversationModelOverrides(ConversationModelOverrides{SpeechProvider: "ollama"})); err != nil {
		t.Fatalf("ollama speech provider must validate: %v", err)
	}
}
