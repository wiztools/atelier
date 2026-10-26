package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
)

// TestSpeechGenerationToolGatingReplicateRoute pins the replicate half of the
// generate_speech gate: the route needs its own slot AND a Replicate token (a
// fal key lights up nothing), airplane mode drops it like every cloud route,
// and the registered description names the backend without the local-provider
// marker the triage airplane paragraph sniffs.
func TestSpeechGenerationToolGatingReplicateRoute(t *testing.T) {
	keyring.MockInit()
	t.Cleanup(func() {
		_ = clearFalAPIKey()
		_ = clearReplicateAPIKey()
	})

	replicate := defaultAppConfig()
	replicate.Models.SpeechProvider = "replicate"
	replicate = mergeAppConfig(replicate)
	if replicate.Providers.Replicate.SpeechModel == "" {
		t.Fatal("selecting the replicate speech provider must seed the slot")
	}
	if err := saveFalAPIKey("fal-test-key"); err != nil {
		t.Fatalf("saveFalAPIKey: %v", err)
	}
	if speechGenerationConfigured(replicate) {
		t.Fatal("generate_speech must stay unconfigured on the replicate route without a replicate key")
	}
	if _, ok := defaultHarnessToolRegistry(context.Background(), replicate, nil).Get("generate_speech"); ok {
		t.Fatal("generate_speech should be absent without a replicate key")
	}

	if err := saveReplicateAPIKey("replicate-test-key"); err != nil {
		t.Fatalf("saveReplicateAPIKey: %v", err)
	}
	if !speechGenerationConfigured(replicate) {
		t.Fatal("generate_speech should be configured on the replicate route with key and seeded slot")
	}
	registry := defaultHarnessToolRegistry(context.Background(), replicate, nil)
	def, ok := registry.Get("generate_speech")
	if !ok {
		t.Fatal("generate_speech should be registered on the replicate route")
	}
	if !strings.Contains(def.Description, "Replicate speech model") {
		t.Fatalf("the replicate description should name the backend, got %q", def.Description)
	}
	if strings.Contains(def.Description, speechLocalProviderMarker) || registry.SpeechServedLocally() {
		t.Fatal("the replicate description must not carry the local-provider marker")
	}
	if !strings.Contains(def.Description, "stay on fal.ai") {
		t.Fatalf("the replicate description should tell the planner cloning and design stay on fal, got %q", def.Description)
	}

	emptySlot := replicate
	emptySlot.Providers.Replicate.SpeechModel = ""
	if speechGenerationConfigured(emptySlot) {
		t.Fatal("generate_speech must stay unconfigured on the replicate route without a slot")
	}

	airplane := replicate
	airplane.AirplaneMode = true
	if speechGenerationConfigured(airplane) {
		t.Fatal("airplane mode must drop generate_speech on the replicate route")
	}

	// The fal route ignores the replicate key: a fal slot without a fal key
	// stays unconfigured even though a replicate token is around.
	if err := clearFalAPIKey(); err != nil {
		t.Fatalf("clearFalAPIKey: %v", err)
	}
	falRoute := defaultAppConfig()
	falRoute.Providers.Fal.AudioModel = "fal-ai/tts"
	if speechGenerationConfigured(falRoute) {
		t.Fatal("the fal route must still require the fal key")
	}
}

// TestGenerateSpeechReplicateRouteStandsDownCloning pins the executor's
// replicate contract: an attached voice reference never routes the call onto
// fal's clone endpoint — the request runs the replicate slot with no
// SourceAudio and the result carries a notice naming the provider switch — and
// the builder stamps the activity's provider and command so usage reports bill
// the call to Replicate, not the engine's fal default.
func TestGenerateSpeechReplicateRouteStandsDownCloning(t *testing.T) {
	config := defaultAppConfig()
	config.Models.SpeechProvider = "replicate"
	config.Providers.Replicate.SpeechModel = "minimax/speech-2.8-hd"
	config.Providers.Fal.AudioModel = "fal-ai/tts"
	config.Providers.Fal.AudioCloneModel = "fal-ai/clone/model"

	var captured AudioGenerateRequest
	tools := HarnessToolExecutionContext{
		Config:         config,
		VoiceReference: "data:audio/wav;base64,QUJD",
		GenerateAudio: func(ctx context.Context, req AudioGenerateRequest) (GeneratedAudio, error) {
			captured = req
			return GeneratedAudio{Data: []byte("x"), MimeType: "audio/mpeg"}, nil
		},
	}
	def := speechGenerationToolDefinition(false, "replicate")
	forced := true
	out, _, err := def.Execute(context.Background(), tools, HarnessToolCall{Content: "say hi in my voice", CloneVoice: &forced})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if captured.Model != "minimax/speech-2.8-hd" {
		t.Fatalf("model = %q, want the replicate slot", captured.Model)
	}
	if captured.SourceAudio != "" {
		t.Fatalf("SourceAudio must stand down on the replicate route even when cloning is forced, got %q", captured.SourceAudio)
	}
	if captured.Kind != audioKindSpeech {
		t.Fatalf("Kind = %q, want speech", captured.Kind)
	}
	audio, ok := out.(ToolAudioResult)
	if !ok {
		t.Fatalf("expected ToolAudioResult, got %T", out)
	}
	joined := strings.Join(audio.Notices, "\n")
	if !strings.Contains(joined, "Replicate speech route doesn't clone") || !strings.Contains(joined, "fal.ai") {
		t.Fatalf("expected the replicate cloning stand-down notice naming the switch, got %v", audio.Notices)
	}
	if strings.Contains(joined, "Ollama") {
		t.Fatalf("the replicate notice must not blame Ollama, got %v", audio.Notices)
	}

	// Voice, emotion, and speed pass through to the request untouched — the
	// resolver decides what the model's schema can take.
	captured = AudioGenerateRequest{}
	out, _, err = def.Execute(context.Background(), tools, HarnessToolCall{Content: "hello", Voice: "Wise_Woman", Emotion: "happy", Speed: 1.2})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if captured.Voice != "Wise_Woman" || captured.Emotion != "happy" || captured.Speed != 1.2 {
		t.Fatalf("speech controls should pass through, got %+v", captured)
	}
	for _, notice := range out.(ToolAudioResult).Notices {
		if strings.Contains(notice, "doesn't clone") {
			t.Fatalf("no stand-down notice expected when the named voice already outranked the clip, got %v", notice)
		}
	}

	activity := def.Activity(HarnessToolResult{Name: "generate_speech", Status: "completed", Result: ToolAudioResult{Model: "minimax/speech-2.8-hd"}})
	if activity.Provider != "replicate" {
		t.Fatalf("activity provider = %q, want replicate", activity.Provider)
	}
	if strings.Join(activity.Command, " ") != "replicate generate minimax/speech-2.8-hd" {
		t.Fatalf("activity command = %v, want replicate generate minimax/speech-2.8-hd", activity.Command)
	}
}

// replicateMiniMaxSpeechSchema mirrors minimax/speech-2.8-hd's published
// input shape: a required text, a free-string voice_id (catalog names or
// cloned ids), an emotion enum, and a bounded speed — flat, where fal's
// packaging nests the pair under voice_setting.
const replicateMiniMaxSpeechSchema = `{"components":{"schemas":{"Input":{"type":"object","required":["text"],"properties":{
	"text":{"type":"string"},
	"voice_id":{"type":"string","default":"Wise_Woman"},
	"emotion":{"type":"string","enum":["auto","happy","calm","sad","angry","fearful","disgusted","surprised"]},
	"speed":{"type":"number","minimum":0.5,"maximum":2},
	"pitch":{"type":"integer","minimum":-12,"maximum":12},
	"audio_format":{"type":"string","enum":["mp3","wav","flac","pcm"]}
}}}}}`

// TestResolveReplicateSpeechInput pins the canonical→native mapping for
// generate_speech on the replicate route: the spoken text is the hard
// requirement (an unmappable text refuses instead of speculating), voice/
// emotion/speed bind where the schema declares them — emotion enum-checked,
// speed clamped — and drop with notices where it doesn't, a voice reference
// never binds, the sound canonicals never fire, and a nil schema degrades to
// the collection's dominant {text} spelling.
func TestResolveReplicateSpeechInput(t *testing.T) {
	schema := parseReplicateFixtureSchema(t, replicateMiniMaxSpeechSchema)

	body, notices, err := resolveReplicateSpeechInput(schema, AudioGenerateRequest{
		Model:   "minimax/speech-2.8-hd",
		Kind:    audioKindSpeech,
		Prompt:  "Hello there.",
		Voice:   "English_Wiselady",
		Emotion: "happy",
		Speed:   1.3,
	})
	if err != nil {
		t.Fatalf("resolveReplicateSpeechInput: %v", err)
	}
	if got, ok := body["text"].(string); !ok || got != "Hello there." {
		t.Fatalf("text = %#v, want the spoken text", body["text"])
	}
	if _, speculative := body["prompt"]; speculative {
		t.Fatalf("the fal prompt+text double-write must not happen on Replicate, got %#v", body)
	}
	if got, ok := body["voice_id"].(string); !ok || got != "English_Wiselady" {
		t.Fatalf("voice_id = %#v, want the named voice", body["voice_id"])
	}
	if got, ok := body["emotion"].(string); !ok || got != "happy" {
		t.Fatalf("emotion = %#v, want happy", body["emotion"])
	}
	if got, ok := body["speed"].(float64); !ok || got != 1.3 {
		t.Fatalf("speed = %#v, want 1.3", body["speed"])
	}
	if len(notices) != 0 {
		t.Fatalf("a clean map should carry no notices, got %v", notices)
	}

	// An emotion outside the enum drops with a notice listing the valid ones;
	// an over-range speed clamps with a notice rather than dropping.
	body, notices, err = resolveReplicateSpeechInput(schema, AudioGenerateRequest{
		Model:   "minimax/speech-2.8-hd",
		Prompt:  "Hello",
		Emotion: "excited",
		Speed:   3,
	})
	if err != nil {
		t.Fatalf("resolveReplicateSpeechInput: %v", err)
	}
	if _, sent := body["emotion"]; sent {
		t.Fatalf("an unknown emotion must not be sent, got %#v", body["emotion"])
	}
	if got, ok := body["speed"].(float64); !ok || got != 2 {
		t.Fatalf("speed = %#v, want the clamped maximum 2", body["speed"])
	}
	joined := strings.Join(notices, "\n")
	if !strings.Contains(joined, "excited") || !strings.Contains(joined, "surprised") {
		t.Fatalf("the emotion drop should list the valid tones, got %v", notices)
	}
	if !strings.Contains(joined, "Clamped speed") {
		t.Fatalf("the speed clamp should ride a notice, got %v", notices)
	}

	// A chatterbox-shaped schema: the text lives in "prompt", and none of the
	// speech controls exist — each drops with its own notice, nothing is
	// guessed, and the reference-audio input is NOT bound (cloning stays on
	// fal; the executor stands the clip down and a direct caller's reference
	// drops with the same notice).
	chatterbox := parseReplicateFixtureSchema(t, `{"components":{"schemas":{"Input":{"type":"object","properties":{
		"prompt":{"type":"string"},
		"audio_prompt":{"type":"string","format":"uri"},
		"exaggeration":{"type":"number"},
		"seed":{"type":"integer"}
	}}}}}`)
	body, notices, err = resolveReplicateSpeechInput(chatterbox, AudioGenerateRequest{
		Model:       "resemble-ai/chatterbox",
		Prompt:      "Hello",
		Voice:       "Wise_Woman",
		Emotion:     "happy",
		Speed:       1.1,
		SourceAudio: "https://example.com/ref.wav",
	})
	if err != nil {
		t.Fatalf("resolveReplicateSpeechInput: %v", err)
	}
	if got, ok := body["prompt"].(string); !ok || got != "Hello" {
		t.Fatalf("prompt = %#v, want the spoken text bound to chatterbox's name", body["prompt"])
	}
	if len(body) != 1 {
		t.Fatalf("only the text may be sent to a schema without the controls, got %#v", body)
	}
	joined = strings.Join(notices, "\n")
	for _, want := range []string{"no voice control", "no emotion control", "no speed control", "doesn't clone"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("notices should include %q, got %v", want, notices)
		}
	}

	// A model with no text-shaped input at all refuses up front — Replicate
	// rejects unknown fields, so a guessed key would only 422 downstream.
	cloning := parseReplicateFixtureSchema(t, `{"components":{"schemas":{"Input":{"type":"object","properties":{
		"voice_file":{"type":"string","format":"uri"},
		"model":{"type":"string"}
	}}}}}`)
	if _, _, err := resolveReplicateSpeechInput(cloning, AudioGenerateRequest{Model: "minimax/voice-cloning", Prompt: "Hello"}); err == nil || !strings.Contains(err.Error(), "no text input") {
		t.Fatalf("an unmappable text must refuse naming the gap, got %v", err)
	}

	// A nil schema degrades to the minimal {text} body plus a notice.
	body, notices, err = resolveReplicateSpeechInput(nil, AudioGenerateRequest{Model: "acme/tts", Prompt: "Hello", Voice: "x"})
	if err != nil {
		t.Fatalf("resolveReplicateSpeechInput(nil): %v", err)
	}
	if len(body) != 1 || body["text"] != "Hello" {
		t.Fatalf("nil-schema body = %#v, want text only", body)
	}
	if len(notices) != 1 {
		t.Fatalf("nil schema should ride one notice, got %v", notices)
	}
}

// TestReplicateSpeechSchemaServes pins the picker's precise filter: a model
// needs a scalar-string text input through the replicate-speech synonyms, so
// the collection's voice-cloning utility (a voice_file in, an id out) and a
// nil schema drop out while the TTS shapes — text, prompt, gen_text — pass.
func TestReplicateSpeechSchemaServes(t *testing.T) {
	if !replicateSpeechSchemaServes(parseReplicateFixtureSchema(t, replicateMiniMaxSpeechSchema)) {
		t.Fatal("a MiniMax-style schema should serve speech")
	}
	chatterbox := parseReplicateFixtureSchema(t, `{"components":{"schemas":{"Input":{"type":"object","properties":{
		"prompt":{"type":"string"},
		"audio_prompt":{"type":"string","format":"uri"}
	}}}}}`)
	if !replicateSpeechSchemaServes(chatterbox) {
		t.Fatal("a prompt-named text input should serve speech")
	}
	cloning := parseReplicateFixtureSchema(t, `{"components":{"schemas":{"Input":{"type":"object","properties":{
		"voice_file":{"type":"string","format":"uri"},
		"model":{"type":"string"},
		"need_noise_reduction":{"type":"boolean"}
	}}}}}`)
	if replicateSpeechSchemaServes(cloning) {
		t.Fatal("the voice-cloning utility must drop out of the speech picker")
	}
	chatShaped := parseReplicateFixtureSchema(t, `{"components":{"schemas":{"Input":{"type":"object","properties":{
		"text":{"type":"array","items":{"type":"string"}}
	}}}}}`)
	if replicateSpeechSchemaServes(chatShaped) {
		t.Fatal("an array-shaped text input is a chat wrapper the request cannot fill")
	}
	if replicateSpeechSchemaServes(nil) {
		t.Fatal("a nil schema must not serve")
	}
}

// TestReplicateGatewaySpeechRouting drives the gateway's GenerateAudio speech
// branch end to end against a mocked Replicate transport: the token loads, the
// schema is fetched, the canonical request maps onto MiniMax's flat inputs
// (text/voice_id/emotion/speed — never the fal prompt spelling), the prediction
// lifecycle runs, the clip downloads, and the cost stays unset for the engine
// layer's CostUnknown stamp. The fal client is never contacted.
func TestReplicateGatewaySpeechRouting(t *testing.T) {
	keyring.MockInit()
	t.Cleanup(func() { _ = clearReplicateAPIKey() })
	if err := saveReplicateAPIKey("replicate-test-key"); err != nil {
		t.Fatalf("saveReplicateAPIKey: %v", err)
	}

	wav := buildTestWAV(uint32(36+16), 16, 8)
	var sentInput map[string]any
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch {
		case req.Method == http.MethodGet && req.URL.Path == "/v1/models/minimax/speech-2.8-hd":
			return jsonResp(`{"latest_version":{"openapi_schema":` + replicateMiniMaxSpeechSchema + `}}`), nil
		case req.Method == http.MethodPost && req.URL.Path == "/v1/models/minimax/speech-2.8-hd/predictions":
			var body struct {
				Input map[string]any `json:"input"`
			}
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				t.Fatalf("decode prediction body: %v", err)
			}
			sentInput = body.Input
			return jsonResp(`{"id":"pred-s1","status":"starting"}`), nil
		case req.Method == http.MethodGet && req.URL.Path == "/v1/predictions/pred-s1":
			return jsonResp(`{"id":"pred-s1","status":"succeeded","output":"https://replicate.delivery/out/speech.wav"}`), nil
		case req.Method == http.MethodGet && req.URL.Path == "/out/speech.wav":
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Body:       io.NopCloser(strings.NewReader(string(wav))),
				Header:     http.Header{"Content-Type": []string{"audio/wav"}},
			}, nil
		default:
			t.Fatalf("unexpected request: %s %s", req.Method, req.URL.Path)
			return nil, nil
		}
	})
	app := &App{client: &http.Client{Transport: transport}}
	config := defaultAppConfig()
	config.Storage.Root = t.TempDir()
	config.Models.SpeechProvider = "replicate"
	config.Providers.Replicate.SpeechModel = "minimax/speech-2.8-hd"
	// The fal slot stays configured to prove the routing ignores it.
	config.Providers.Fal.AudioModel = "fal-ai/tts"
	gateway := newToolGateway(app, config)
	generated, err := gateway.tools.GenerateAudio(context.Background(), AudioGenerateRequest{
		Model:   "minimax/speech-2.8-hd",
		Kind:    audioKindSpeech,
		Prompt:  "Welcome to Atelier.",
		Voice:   "Wise_Woman",
		Emotion: "calm",
		Speed:   0.9,
	})
	if err != nil {
		t.Fatalf("GenerateAudio: %v", err)
	}
	if len(generated.Data) == 0 || generated.MimeType != "audio/wav" {
		t.Fatalf("generated = %d bytes %q, want the downloaded WAV", len(generated.Data), generated.MimeType)
	}
	if generated.CostMicros != 0 {
		t.Fatalf("CostMicros = %d, want 0 (unknown, not free)", generated.CostMicros)
	}
	if sentInput["text"] != "Welcome to Atelier." || sentInput["voice_id"] != "Wise_Woman" || sentInput["emotion"] != "calm" {
		t.Fatalf("prediction input = %v, want MiniMax's flat text/voice_id/emotion", sentInput)
	}
	if speed, ok := sentInput["speed"].(float64); !ok || speed != 0.9 {
		t.Fatalf("prediction input speed = %v, want 0.9", sentInput["speed"])
	}
	if _, speculative := sentInput["prompt"]; speculative {
		t.Fatalf("replicate input must not carry the fal prompt spelling, got %v", sentInput)
	}
	for _, notice := range generated.Notices {
		if strings.Contains(notice, "no emotion control") || strings.Contains(notice, "no speed control") {
			t.Fatalf("MiniMax declares emotion and speed; no drop notice expected, got %v", generated.Notices)
		}
	}

	// Airplane mode refuses the cloud route before any request goes out.
	airplane := config
	airplane.AirplaneMode = true
	app.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		t.Fatalf("airplane mode must not contact Replicate, got %s %s", req.Method, req.URL.Path)
		return nil, nil
	})
	if _, err := newToolGateway(app, airplane).tools.GenerateAudio(context.Background(), AudioGenerateRequest{
		Model:  "minimax/speech-2.8-hd",
		Kind:   audioKindSpeech,
		Prompt: "Hello",
	}); err == nil {
		t.Fatal("airplane mode should refuse replicate speech")
	}
}

// TestMediaGenGapSpeechReplicate pins the speech half of the provider-aware
// audio remedy: with speech on Replicate and the sound tools still on fal, the
// gap note keeps the fal key remedy for sound and appends the Replicate token
// remedy for speech; the user-facing blockquote does the same, an answer that
// already names Replicate suppresses it, and video stays untouched.
func TestMediaGenGapSpeechReplicate(t *testing.T) {
	note := mediaGenUnavailableNote("audio", false, "replicate", false)
	if !strings.Contains(note, "fal.ai API key") || !strings.Contains(note, "Replicate API token") {
		t.Fatalf("mixed-route audio gap should name both remedies, got %q", note)
	}
	if strings.Contains(note, "Ollama") {
		t.Fatalf("the replicate speech route must not suggest Ollama, got %q", note)
	}
	if got := mediaGenUnavailableNote("video", false, "replicate", false); strings.Contains(got, "Speech") {
		t.Fatalf("video note must stay untouched by the speech route, got %q", got)
	}
	// With the sound tools on Replicate too, the single Replicate remedy
	// already covers speech — no second sentence.
	both := mediaGenUnavailableNote("audio", false, "replicate", true)
	if strings.Count(both, "Replicate API token") != 1 || strings.Contains(both, "fal.ai API key") {
		t.Fatalf("an all-replicate audio gap should name the token once, got %q", both)
	}

	notice := mediaGenFallbackNotice(true, "audio", "Sure, generating now.", false, "replicate", false)
	if !strings.Contains(notice, "fal.ai API key") || !strings.Contains(notice, "Speech is set to Replicate") {
		t.Fatalf("mixed-route fallback notice should name both remedies, got %q", notice)
	}
	if got := mediaGenFallbackNotice(true, "audio", "Add a Replicate token in Settings first.", false, "replicate", false); got != "" {
		t.Fatalf("an answer naming Replicate should suppress the notice on the replicate speech route, got %q", got)
	}
	if got := mediaGenFallbackNotice(true, "video", "Sure.", false, "replicate", false); strings.Contains(got, "Speech") {
		t.Fatalf("video notice must stay untouched by the speech route, got %q", got)
	}
}
