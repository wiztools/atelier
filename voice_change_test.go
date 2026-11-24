package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"atelier/internal/mediasniff/mediasnifftest"

	"github.com/zalando/go-keyring"
)

// voiceChangeTestConfig returns a config with the change_voice model slot
// pinned, so the gate opens as soon as a fal.ai key exists (the audio provider
// is fal by default).
func voiceChangeTestConfig() AppConfig {
	config := defaultAppConfig()
	config.Providers.Fal.VoiceChangerModel = defaultFalVoiceChangerModel
	return config
}

// TestVoiceChangeToolGating confirms change_voice is registered only on the
// fal audio route, with a model AND a fal.ai key — and that any other audio
// provider, airplane mode, or an empty slot keeps it out of the registry.
func TestVoiceChangeToolGating(t *testing.T) {
	keyring.MockInit()
	t.Cleanup(func() { _ = clearFalAPIKey() })
	ctx := context.Background()

	if _, ok := defaultHarnessToolRegistry(ctx, voiceChangeTestConfig(), nil).Get(voiceChangerToolName); ok {
		t.Fatal("change_voice should be absent without a fal.ai key")
	}
	if err := saveFalAPIKey("fal-test-key"); err != nil {
		t.Fatalf("saveFalAPIKey: %v", err)
	}
	if !voiceChangeConfigured(voiceChangeTestConfig()) {
		t.Fatal("voice changing should be configured with the fal provider, a model, and a key")
	}
	if _, ok := defaultHarnessToolRegistry(ctx, voiceChangeTestConfig(), nil).Get(voiceChangerToolName); !ok {
		t.Fatal("change_voice should be registered when configured")
	}

	closed := []struct {
		name   string
		mutate func(*AppConfig)
	}{
		{"empty model slot", func(c *AppConfig) { c.Providers.Fal.VoiceChangerModel = "" }},
		{"replicate audio provider", func(c *AppConfig) { c.Models.AudioProvider = "replicate" }},
		{"runware audio provider", func(c *AppConfig) { c.Models.AudioProvider = "runware" }},
		{"airplane mode", func(c *AppConfig) { c.AirplaneMode = true }},
	}
	for _, tc := range closed {
		config := voiceChangeTestConfig()
		tc.mutate(&config)
		if voiceChangeConfigured(config) {
			t.Errorf("%s: voiceChangeConfigured = true, want false", tc.name)
		}
		if _, ok := defaultHarnessToolRegistry(ctx, config, nil).Get(voiceChangerToolName); ok {
			t.Errorf("%s: change_voice registered, want absent", tc.name)
		}
	}

	if got := resolveDefaultVoiceChangerModel(voiceChangeTestConfig()); got != defaultFalVoiceChangerModel {
		t.Fatalf("resolveDefaultVoiceChangerModel = %q, want the configured model", got)
	}
	cleared := voiceChangeTestConfig()
	cleared.Providers.Fal.VoiceChangerModel = ""
	if got := resolveDefaultVoiceChangerModel(cleared); got != defaultFalVoiceChangerModel {
		t.Fatalf("resolveDefaultVoiceChangerModel fallback = %q, want %q", got, defaultFalVoiceChangerModel)
	}
}

// TestMergeAppConfigSeedsVoiceChangerModel pins the provider-selection seed:
// choosing fal.ai as the audio provider (or leaving it unset) enables
// change_voice without a Settings visit, while another provider leaves the
// slot empty and an explicit model always survives the merge.
func TestMergeAppConfigSeedsVoiceChangerModel(t *testing.T) {
	// An unset or unknown provider normalizes to fal, so it seeds too.
	for name, provider := range map[string]string{"fal": "fal", "unset": "", "unknown": "acme"} {
		config := defaultAppConfig()
		config.Models.AudioProvider = provider
		config.Providers.Fal.VoiceChangerModel = ""
		if merged := mergeAppConfig(config); merged.Providers.Fal.VoiceChangerModel != defaultFalVoiceChangerModel {
			t.Errorf("%s audio provider: merged voiceChangerModel = %q, want the default %q", name, merged.Providers.Fal.VoiceChangerModel, defaultFalVoiceChangerModel)
		}
	}
	for _, provider := range []string{"replicate", "runware"} {
		config := defaultAppConfig()
		config.Models.AudioProvider = provider
		config.Providers.Fal.VoiceChangerModel = ""
		if merged := mergeAppConfig(config); merged.Providers.Fal.VoiceChangerModel != "" {
			t.Errorf("%s audio provider: voiceChangerModel seeded to %q, want empty (fal-only tool)", provider, merged.Providers.Fal.VoiceChangerModel)
		}
	}
	explicit := defaultAppConfig()
	explicit.Providers.Fal.VoiceChangerModel = "acme/voice-changer"
	if merged := mergeAppConfig(explicit); merged.Providers.Fal.VoiceChangerModel != "acme/voice-changer" {
		t.Fatalf("explicit model must survive merge, got %q", merged.Providers.Fal.VoiceChangerModel)
	}
}

// TestVoiceChangerVoicesFollowTheEndpointSchema pins the voice list's source of
// truth: the endpoint's own schema. Its voice examples come back in published
// order with the default appended, and the tool description lists them —
// described where the built-in table knows the name, bare otherwise.
func TestVoiceChangerVoicesFollowTheEndpointSchema(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	keyring.MockInit()
	if err := saveFalAPIKey("fal-test-key"); err != nil {
		t.Fatalf("saveFalAPIKey: %v", err)
	}
	t.Cleanup(func() { _ = clearFalAPIKey() })

	raw, err := os.ReadFile(filepath.Join("testdata", "fal-schemas", "elevenlabs-voice-changer.json"))
	if err != nil {
		t.Fatal(err)
	}
	app := NewApp()
	app.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method == http.MethodGet && req.URL.Query().Get("endpoint_id") == defaultFalVoiceChangerModel {
			return jsonResponse(string(raw)), nil
		}
		t.Fatalf("unexpected request %s %s", req.Method, req.URL)
		return nil, nil
	})
	config := voiceChangeTestConfig()
	config.Storage.Root = filepath.Join(home, ".atelier")

	voices, fromSchema := voiceChangerVoices(context.Background(), config, app)
	if !fromSchema {
		t.Fatal("voices should come from the endpoint's schema")
	}
	schema := loadSchema(t, "elevenlabs-voice-changer")
	voice, ok := schema.property("voice")
	if !ok || len(voice.Examples) == 0 {
		t.Fatalf("pinned schema carries no voice examples: %+v", voice)
	}
	// The default is a valid target fal lists beside its examples, so it rides last.
	want := append(append([]string{}, voice.Examples...), "Rachel")
	if !reflect.DeepEqual(voices, want) {
		t.Fatalf("voices = %v, want the schema's examples plus the default %v", voices, want)
	}

	description := voiceChangeToolDefinition(context.Background(), config, app).Description
	for _, phrase := range []string{
		"takes NO prompt",
		"Aria (female, expressive, narration and audiobooks)",
		"Daniel (male, deep, compelling narration)",
		"Rachel (female, American, calm)",
	} {
		if !strings.Contains(description, phrase) {
			t.Errorf("description missing %q:\n%s", phrase, description)
		}
	}
}

// TestVoiceChangerVoicesFallBackWithoutASchema covers the offline side: with
// no App, an unreachable schema, or a schema whose voice property lists no
// examples, the built-in table stands in — and a schema-listed name the table
// doesn't describe still appears, bare.
func TestVoiceChangerVoicesFallBackWithoutASchema(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	keyring.MockInit()
	if err := saveFalAPIKey("fal-test-key"); err != nil {
		t.Fatalf("saveFalAPIKey: %v", err)
	}
	t.Cleanup(func() { _ = clearFalAPIKey() })

	var builtIn []string
	for _, entry := range elevenLabsVoiceCatalog {
		builtIn = append(builtIn, entry.Name)
	}
	config := voiceChangeTestConfig()
	config.Storage.Root = filepath.Join(home, ".atelier")

	if voices, fromSchema := voiceChangerVoices(context.Background(), config, nil); fromSchema || !reflect.DeepEqual(voices, builtIn) {
		t.Fatalf("no app: voices = %v (schema %v), want the built-in table", voices, fromSchema)
	}

	unreachable := NewApp()
	unreachable.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return nil, errors.New("offline")
	})
	if voices, fromSchema := voiceChangerVoices(context.Background(), config, unreachable); fromSchema || !reflect.DeepEqual(voices, builtIn) {
		t.Fatalf("unreachable schema: voices = %v (schema %v), want the built-in table", voices, fromSchema)
	}

	// A catalog fal republishes with names the built-in table never heard of:
	// the schema still wins, and each new name is listed bare.
	const republished = `{"components":{"schemas":{"AcmeVoiceChangerInput":{"type":"object","required":["audio_url"],
		"properties":{"audio_url":{"type":"string"},"voice":{"type":"string","default":"Darian","examples":["Talia","Eldrin"]}}}}}}`
	rePublished := NewApp()
	rePublished.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return jsonResponse(republished), nil
	})
	if voices, fromSchema := voiceChangerVoices(context.Background(), config, rePublished); !fromSchema || !reflect.DeepEqual(voices, []string{"Talia", "Eldrin", "Darian"}) {
		t.Fatalf("republished schema: voices = %v (schema %v), want [Talia Eldrin Darian]", voices, fromSchema)
	}
	description := voiceChangeDescription([]string{"Talia", "Eldrin", "Daniel"})
	if !strings.Contains(description, "Talia, Eldrin, Daniel (male, deep, compelling narration)") {
		t.Fatalf("an undescribed name should list bare beside a described one:\n%s", description)
	}
}

// TestElevenLabsVoiceCatalogMatchesPinnedSchema is the drift alarm. The
// built-in table is the offline fallback for a list fal publishes as schema
// examples, so the two must agree: a table name the pinned schema doesn't
// list means fal's catalog moved (the default voices retire on 2026-12-31),
// and a schema name the table doesn't describe means a planner will meet it
// with no gender or delivery to match a request against.
func TestElevenLabsVoiceCatalogMatchesPinnedSchema(t *testing.T) {
	schema := loadSchema(t, "elevenlabs-voice-changer")
	voice, ok := schema.property("voice")
	if !ok || len(voice.Examples) == 0 {
		t.Fatalf("pinned schema carries no voice examples: %+v", voice)
	}
	listed := map[string]bool{}
	for _, name := range voice.Examples {
		listed[strings.ToLower(name)] = true
	}
	def, _ := voice.Default.(string)
	for _, entry := range elevenLabsVoiceCatalog {
		if !listed[strings.ToLower(entry.Name)] && !strings.EqualFold(def, entry.Name) {
			t.Errorf("built-in voice %q is neither in the pinned schema's examples nor its default %q — fal's catalog moved; refresh the table", entry.Name, def)
		}
	}
	described := map[string]bool{}
	for _, entry := range elevenLabsVoiceCatalog {
		described[strings.ToLower(entry.Name)] = true
	}
	for _, name := range voice.Examples {
		if !described[strings.ToLower(name)] {
			t.Errorf("pinned schema lists voice %q the built-in table doesn't describe — add a descriptor", name)
		}
	}
	// The endpoint contract the resolver and the description rely on.
	if _, ok := schema.property("audio_url"); !ok {
		t.Error("pinned schema has no audio_url input")
	}
	if len(voice.Enum) != 0 {
		t.Errorf("voice = enum %v, want a plain string (examples are advice, not a closed set)", voice.Enum)
	}
}

// TestResolveVoiceChangeBody pins the canonical-to-native mapping against the
// real endpoint's schema, and the refusal/degradation rules for a model pinned
// into the slot that doesn't share its shape.
func TestResolveVoiceChangeBody(t *testing.T) {
	schema := loadSchema(t, "elevenlabs-voice-changer")
	req := VoiceChangeRequest{
		Model:       defaultFalVoiceChangerModel,
		SourceAudio: "data:audio/wav;base64,UklGRg==",
		Voice:       "Daniel",
	}
	body, notices, err := resolveVoiceChangeBody(schema, req, builtinFalOverrides())
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	// Exactly the two inputs the tool owns: the tuning dials and output format
	// stay at the endpoint's own defaults rather than being written blind.
	want := map[string]any{"audio_url": req.SourceAudio, "voice": "Daniel"}
	if !reflect.DeepEqual(body, want) {
		t.Fatalf("body = %v, want %v", body, want)
	}
	if len(notices) != 0 {
		t.Fatalf("notices = %v, want none", notices)
	}

	// No schema: the documented names go out under a notice.
	body, notices, err = resolveVoiceChangeBody(nil, req, builtinFalOverrides())
	if err != nil || body["audio_url"] != req.SourceAudio || body["voice"] != "Daniel" {
		t.Fatalf("nil schema: body = %v, err = %v", body, err)
	}
	if len(notices) != 1 || !strings.Contains(notices[0], "Couldn't load the model's parameter schema") {
		t.Fatalf("nil schema notices = %v, want the schema notice", notices)
	}

	// A model with no audio input cannot re-voice anything: fatal, naming the remedy.
	const noAudio = `{"components":{"schemas":{"AcmeInput":{"type":"object","properties":{"prompt":{"type":"string"}}}}}}`
	noAudioSchema, err := parseModelInputSchema([]byte(noAudio))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := resolveVoiceChangeBody(noAudioSchema, req, builtinFalOverrides()); err == nil || !strings.Contains(err.Error(), "has no audio input") {
		t.Fatalf("no-audio model: err = %v, want the fatal refusal", err)
	}

	// A model with no voice control still re-voices the clip — a notice, not a failure.
	const noVoice = `{"components":{"schemas":{"AcmeInput":{"type":"object","required":["audio_url"],"properties":{"audio_url":{"type":"string"}}}}}}`
	noVoiceSchema, err := parseModelInputSchema([]byte(noVoice))
	if err != nil {
		t.Fatal(err)
	}
	body, notices, err = resolveVoiceChangeBody(noVoiceSchema, req, builtinFalOverrides())
	if err != nil {
		t.Fatalf("no-voice model: err = %v", err)
	}
	if body["audio_url"] != req.SourceAudio || len(body) != 1 {
		t.Fatalf("no-voice model: body = %v, want the audio input alone", body)
	}
	if len(notices) != 1 || !strings.Contains(notices[0], "has no voice input") {
		t.Fatalf("no-voice model notices = %v, want the dropped-voice notice", notices)
	}

	// A required input the tool cannot fill refuses before any money moves.
	// (A required voice_id would NOT refuse — the voice canonical maps onto
	// it — so the stand-in is an input no canonical reaches.)
	const demanding = `{"components":{"schemas":{"AcmeInput":{"type":"object","required":["audio_url","target_language"],"properties":{"audio_url":{"type":"string"},"target_language":{"type":"string"}}}}}}`
	demandingSchema, err := parseModelInputSchema([]byte(demanding))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := resolveVoiceChangeBody(demandingSchema, req, builtinFalOverrides()); err == nil || !strings.Contains(err.Error(), "target_language") {
		t.Fatalf("demanding model: err = %v, want the refusal naming target_language", err)
	}

	// The voice synonyms reach the other spellings a pinned model may use.
	const voiceID = `{"components":{"schemas":{"AcmeInput":{"type":"object","required":["audio_url","voice_id"],"properties":{"audio_url":{"type":"string"},"voice_id":{"type":"string"}}}}}}`
	voiceIDSchema, err := parseModelInputSchema([]byte(voiceID))
	if err != nil {
		t.Fatal(err)
	}
	body, _, err = resolveVoiceChangeBody(voiceIDSchema, req, builtinFalOverrides())
	if err != nil || body["voice_id"] != "Daniel" {
		t.Fatalf("voice_id model: body = %v err = %v, want the voice on voice_id", body, err)
	}

	// An empty voice sends nothing and takes the model's own default.
	body, _, err = resolveVoiceChangeBody(schema, VoiceChangeRequest{Model: req.Model, SourceAudio: req.SourceAudio}, builtinFalOverrides())
	if err != nil || len(body) != 1 {
		t.Fatalf("empty voice: body = %v, err = %v, want the audio input alone", body, err)
	}

	// fal-overrides.json can retire the voice control for a model whose
	// published schema disagrees with its runtime.
	ov := Overrides{byCategory: map[string]map[string]map[string]overrideEntry{
		"voice-change": {defaultFalVoiceChangerModel: {"voice": {Path: ""}}},
	}}
	body, notices, err = resolveVoiceChangeBody(schema, req, ov)
	if err != nil || body["voice"] != nil || len(notices) != 1 {
		t.Fatalf("voice override off: body = %v notices = %v err = %v", body, notices, err)
	}
}

// TestVoiceChangeToolValidate pins the planner corrections: the voice is
// required and must be a NAME, a description in the voice field is corrected
// here instead of costing a planner round on the endpoint's rejection, and a
// single word off the list rides for the endpoint to judge.
func TestVoiceChangeToolValidate(t *testing.T) {
	definition := voiceChangeToolDefinition(context.Background(), voiceChangeTestConfig(), nil)
	corrected := []struct {
		name string
		call HarnessToolCall
		want string
	}{
		{"missing voice", HarnessToolCall{}, ".voice is required"},
		{"a description in content", HarnessToolCall{Content: "a deep male voice"}, "no content or prompt"},
		{"a description as the voice", HarnessToolCall{Voice: "a deep male voice"}, "is a description, not a voice name"},
		{"two names", HarnessToolCall{Voice: "Daniel,Brian"}, "is a description, not a voice name"},
	}
	for _, tc := range corrected {
		errs := definition.Validate("toolCalls[0]", tc.call)
		if len(errs) != 1 || !strings.Contains(errs[0], tc.want) {
			t.Errorf("%s: validate = %v, want one correction containing %q", tc.name, errs, tc.want)
		}
	}
	// The correction hands the planner the catalog to pick from.
	errs := definition.Validate("toolCalls[0]", HarnessToolCall{Voice: "a deep male voice"})
	if !strings.Contains(errs[0], "one of Alice, Aria") || !strings.Contains(errs[0], "Daniel") {
		t.Errorf("description correction should list the catalog: %v", errs)
	}
	// Catalog names in any casing pass, and so does a single word the list
	// doesn't show — fal publishes examples, not an enum.
	for _, voice := range []string{"Daniel", "daniel", "DANIEL", "Rachel", "Harry"} {
		if errs := definition.Validate("toolCalls[0]", HarnessToolCall{Voice: voice}); len(errs) != 0 {
			t.Errorf("voice %q: validate = %v, want no correction", voice, errs)
		}
	}
}

// TestVoiceChangeToolExecute pins the canonical request the executor builds and
// the staged result: the attached clip as the source, the voice normalized to
// the catalog's spelling, the default model, and a summary naming the voice —
// plus the guards that keep a sourceless or voiceless call off the network.
func TestVoiceChangeToolExecute(t *testing.T) {
	const source = "data:audio/wav;base64,UklGRg=="
	var got VoiceChangeRequest
	tools := HarnessToolExecutionContext{
		Config:         voiceChangeTestConfig(),
		AttachedAudios: []string{source},
		ChangeVoice: func(ctx context.Context, req VoiceChangeRequest) (GeneratedAudio, error) {
			got = req
			return GeneratedAudio{Data: tinyMP3(), MimeType: "audio/mpeg", CostMicros: 40000, Notices: []string{"resolver note"}}, nil
		},
	}
	definition := voiceChangeToolDefinition(context.Background(), voiceChangeTestConfig(), nil)
	result, summary, err := definition.Execute(context.Background(), tools,
		HarnessToolCall{Name: voiceChangerToolName, Voice: " daniel "})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got.Model != defaultFalVoiceChangerModel || got.SourceAudio != source || got.Voice != "Daniel" {
		t.Fatalf("request = %+v, want the default model, the attached clip, and the catalog spelling", got)
	}
	typed, ok := result.(ToolAudioResult)
	if !ok {
		t.Fatalf("result = %T, want ToolAudioResult", result)
	}
	if typed.Count != 1 || len(typed.Audios) != 1 || typed.Model != defaultFalVoiceChangerModel {
		t.Fatalf("tool result = %+v, want one staged clip on the default model", typed)
	}
	if typed.Prompt != "voice: Daniel" {
		t.Errorf("staged prompt = %q, want it to name the target voice", typed.Prompt)
	}
	if typed.CostMicros != 40000 || len(typed.Notices) != 1 {
		t.Errorf("staged cost/notices = %d/%v, want the cost and the resolver notice carried through", typed.CostMicros, typed.Notices)
	}
	if summary != "changed the voice to Daniel with "+defaultFalVoiceChangerModel {
		t.Errorf("summary = %q, want it to name the voice and the model", summary)
	}
	activity := definition.Activity(HarnessToolResult{Name: voiceChangerToolName, Status: "completed", Result: result})
	wantCommand := []string{"fal", "change-voice", defaultFalVoiceChangerModel}
	if !reflect.DeepEqual(activity.Command, wantCommand) || activity.MediaKind != "audio" || activity.CostMicros != 40000 {
		t.Fatalf("activity = %+v, want command %v on the audio kind with the cost", activity, wantCommand)
	}

	// An off-list single word is sent exactly as given.
	if _, _, err := definition.Execute(context.Background(), tools, HarnessToolCall{Name: voiceChangerToolName, Voice: "Harry"}); err != nil || got.Voice != "Harry" {
		t.Fatalf("off-list name: voice = %q err = %v, want it passed through", got.Voice, err)
	}

	guards := []struct {
		name  string
		tools HarnessToolExecutionContext
		call  HarnessToolCall
		want  string
	}{
		{"no source", HarnessToolExecutionContext{
			Config: voiceChangeTestConfig(),
			ChangeVoice: func(ctx context.Context, req VoiceChangeRequest) (GeneratedAudio, error) {
				t.Fatal("ChangeVoice must not run without an audio clip")
				return GeneratedAudio{}, nil
			},
		}, HarnessToolCall{Name: voiceChangerToolName, Voice: "Daniel"}, "needs an audio clip"},
		{"no voice", HarnessToolExecutionContext{
			Config:         voiceChangeTestConfig(),
			AttachedAudios: []string{source},
			ChangeVoice: func(ctx context.Context, req VoiceChangeRequest) (GeneratedAudio, error) {
				t.Fatal("ChangeVoice must not run without a target voice")
				return GeneratedAudio{}, nil
			},
		}, HarnessToolCall{Name: voiceChangerToolName}, "needs a target voice"},
		{"no hook", HarnessToolExecutionContext{
			Config:         voiceChangeTestConfig(),
			AttachedAudios: []string{source},
		}, HarnessToolCall{Name: voiceChangerToolName, Voice: "Daniel"}, "not available in this context"},
	}
	for _, tc := range guards {
		if _, _, err := definition.Execute(context.Background(), tc.tools, tc.call); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want it to contain %q", tc.name, err, tc.want)
		}
	}
}

// TestHarnessPlanSchemaAdmitsVoice pins that the format-schema planner grammar
// frees change_voice's voice like the per-tool param schema does. The grammar
// never carried the key — only the call struct did — so a format-schema
// planner could not name a voice at all, for this tool or generate_speech.
func TestHarnessPlanSchemaAdmitsVoice(t *testing.T) {
	registry := newHarnessToolRegistry([]HarnessToolDefinition{voiceChangeToolDefinition(context.Background(), voiceChangeTestConfig(), nil)})
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
	if _, ok := properties["voice"].(map[string]any); !ok {
		t.Errorf("plan grammar toolCalls properties missing voice (has %v)", propertiesKeys(properties))
	}
	// The kwarg dialect fills the same field.
	var call HarnessToolCall
	applyKwargs(&call, "voice='Daniel'")
	if call.Voice != "Daniel" {
		t.Fatalf("applyKwargs voice = %q, want Daniel", call.Voice)
	}
}

// TestTriagePromptRoutesVoiceChange pins the routing rules: a voice change is
// audio mode naming change_voice (not a mediaEdit, not voice design), the ask
// names the tool in toolTask whether or not it is listed — the contract word
// the harness's absent-tool check reads — and airplane mode lists it with the
// cloud-only tools. The existing five-tool sentence stays byte-identical.
func TestTriagePromptRoutesVoiceChange(t *testing.T) {
	registry := newHarnessToolRegistry([]HarnessToolDefinition{voiceChangeToolDefinition(context.Background(), voiceChangeTestConfig(), nil)})
	catalog := registry.PromptCatalog()
	for _, want := range []string{"change_voice", "takes NO prompt", "Daniel (male, deep, compelling narration)"} {
		if !strings.Contains(catalog, want) {
			t.Errorf("tool catalog missing %q", want)
		}
	}
	prompt := triageSystemPrompt(registry, nil, "/tmp/ws", false)
	for _, want := range []string{
		"change the voice of an existing recording",
		"CHANGING THE VOICE is not one of these either",
		`choose responseMode "audio" and name change_voice in toolTask`,
		"change_voice can re-voice an attached audio clip",
		"always write change_voice in toolTask",
		"when it is not listed, set needsTools false",
		// Untouched: the pinned five-tool list and the retired name's absence.
		"generate_speech, generate_sound, generate_voice, extend_audio, or add_sound_effects tool",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("triage prompt missing %q", want)
		}
	}
	if strings.Contains(prompt, "generate_audio") {
		t.Error("the retired generate_audio tool name must not appear in the triage prompt")
	}
	if airplane := triageSystemPrompt(registry, nil, "/tmp/ws", true); !strings.Contains(airplane, "add_sound_effects, change_voice, lip_sync") {
		t.Error("airplane paragraph should list change_voice among the cloud-only tools")
	}
}

// TestVoiceChangeUnavailableNoteAndFallback pins the absent-tool reply's two
// voices: the note tells the final model not to claim a conversion and names
// the one remedy that covers every reason the tool can be missing, and the
// blockquote lands only when the model's answer didn't already point there.
func TestVoiceChangeUnavailableNoteAndFallback(t *testing.T) {
	if voiceChangeFallbackNotice(false, false, "anything at all") != "" {
		t.Fatal("no blockquote when the voice changer is available")
	}
	note := voiceChangeUnavailableNote(false)
	for _, want := range []string{
		"Do not claim the voice was changed",
		"set Audio Provider to fal.ai and pick a Voice Changer model in Settings → Models",
		"add a fal.ai API key in Settings → Providers",
	} {
		if !strings.Contains(note, want) {
			t.Errorf("note missing %q:\n%s", want, note)
		}
	}
	airplaneNote := voiceChangeUnavailableNote(true)
	if !strings.Contains(airplaneNote, "airplane mode") || strings.Contains(airplaneNote, "API key") {
		t.Errorf("airplane note should name the mode and never suggest a key:\n%s", airplaneNote)
	}
	fallbacks := []struct {
		name, answer, want string
	}{
		{"bare answer", "Sorry, I can't do that here.", "Voice changing isn't set up yet"},
		{"answer names fal.ai", "Voice changing needs fal.ai.", ""},
		{"airplane bare", "", "unavailable while airplane mode is on"},
	}
	for _, tc := range fallbacks {
		airplane := tc.name == "airplane bare"
		got := voiceChangeFallbackNotice(true, airplane, tc.answer)
		if tc.want == "" {
			if got != "" {
				t.Errorf("%s: fallback = %q, want none (the answer covered it)", tc.name, got)
			}
		} else if !strings.Contains(got, tc.want) {
			t.Errorf("%s: fallback = %q, want it to contain %q", tc.name, got, tc.want)
		}
	}
	if voiceChangeFallbackNotice(true, true, "Airplane mode is on, so this is blocked.") != "" {
		t.Error("an answer naming airplane mode needs no blockquote")
	}
}

// persistedToolActivity returns the single tool activity of the conversation's
// sole assistant turn — the harness run's one tool_call step.
func persistedToolActivity(t *testing.T, config AppConfig) map[string]any {
	t.Helper()
	run := persistedHarnessRun(t, config, 1)
	for _, raw := range run["steps"].([]any) {
		step := raw.(map[string]any)
		if step["kind"] != "tool_call" {
			continue
		}
		tools := step["tools"].([]any)
		if len(tools) != 1 {
			t.Fatalf("tool activities = %+v, want exactly one", tools)
		}
		return tools[0].(map[string]any)
	}
	t.Fatalf("persisted run has no tool_call step: %+v", run)
	return nil
}

// assistantText joins the text parts of the conversation's sole assistant turn.
func assistantText(t *testing.T, config AppConfig) string {
	t.Helper()
	var text []string
	for _, content := range persistedAssistantTurn(t, config, 1).Content {
		if content.Type == "text" {
			text = append(text, content.Text)
		}
	}
	return strings.Join(text, "\n")
}

// TestHarnessChangesVoiceViaFal is the change_voice happy path: a turn with an
// attached recording routes to audio mode, the planner names a catalog voice in
// lowercase, the endpoint receives the hosted clip under the voice's catalog
// spelling and nothing else, and the re-voiced clip lands in the conversation
// as an audio artifact priced off the source's own length.
func TestHarnessChangesVoiceViaFal(t *testing.T) {
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
	config.Providers.Fal.VoiceChangerModel = defaultFalVoiceChangerModel
	if err := writeAppConfig(config); err != nil {
		t.Fatalf("writeAppConfig: %v", err)
	}

	// A 2-second WAV (16000 payload bytes at the fixture's 8 kHz byte rate):
	// the per-minute estimate reads the source's own length before upload.
	source := "data:audio/wav;base64," + base64.StdEncoding.EncodeToString(mediasnifftest.WAV(16000))
	const cdnSource = "https://v3.test.fal.media/files/abc/voice-source.wav"

	var submitted map[string]any
	submittedPath := ""
	uploaded := false
	app := NewApp()
	harnessCalls := 0
	app.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if resp, ok := falTestPlatformResponse(req); ok {
			return resp, nil
		}
		switch {
		case strings.Contains(req.URL.String(), "storage/auth/token"):
			return jsonResponse(`{"token":"cdn-test-token","token_type":"Bearer","base_url":"https://v3.test.fal.media"}`), nil
		case strings.HasSuffix(req.URL.Path, "/files/upload"):
			uploaded = true
			return jsonResponse(`{"access_url":"` + cdnSource + `","uploaded":true}`), nil
		case strings.Contains(req.URL.Host, "fal.run"):
			if req.Method == http.MethodPost {
				submittedPath = req.URL.Path
				if err := json.NewDecoder(req.Body).Decode(&submitted); err != nil {
					t.Fatalf("decode fal submit body: %v", err)
				}
				return jsonResponse(`{"request_id":"req-vc-1"}`), nil
			}
			if strings.HasSuffix(req.URL.Path, "/status") {
				return jsonResponse(`{"status":"COMPLETED"}`), nil
			}
			if strings.HasSuffix(req.URL.Path, "/requests/req-vc-1") {
				return jsonResponse(`{"audio":{"url":"https://queue.fal.run/revoiced.mp3","content_type":"audio/mpeg"},"seed":1902083897}`), nil
			}
			// Download of the generated clip.
			return audioResp(tinyMP3(), "audio/mpeg"), nil
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
						return chatCompletion("harness-model", `{"needsTools":true,"responseMode":"audio","toolTask":"Change the voice of the attached clip to a deep male voice using change_voice.","reason":"The user asked to re-voice a recording."}`), nil
					case 2: // plan round 1
						return chatCompletion("harness-model", `{"brief":"Re-voice as Daniel.","needsTools":true,"reason":"voice change","toolCalls":[{"name":"change_voice","voice":"daniel"}]}`), nil
					case 3: // plan round 2 — the evidence is in, wrap up
						return chatCompletion("harness-model", `{"brief":"Done.","needsTools":false,"reason":"done","toolCalls":[]}`), nil
					}
					t.Fatalf("unexpected harness call #%d", harnessCalls)
					return nil, nil
				default:
					// Title generation runs once, on the primary model, after
					// the turn — not part of the harness sequence.
					return chatCompletion("chat-box-model", `"Re-voiced clip"`), nil
				}
			}
			body := fmt.Sprintln(`{"model":"chat-box-model","message":{"role":"assistant","content":"Here is your clip in Daniel's voice."},"done":false}`) +
				fmt.Sprintln(`{"model":"chat-box-model","done":true,"done_reason":"stop","eval_count":3}`)
			return &http.Response{StatusCode: 200, Status: "200 OK",
				Body:   io.NopCloser(strings.NewReader(body)),
				Header: http.Header{"Content-Type": []string{"application/x-ndjson"}}}, nil
		default:
			t.Fatalf("unexpected request %s %s", req.Method, req.URL)
			return nil, nil
		}
	})

	app.runChatStream(context.Background(), "request-fal-voice-change", ChatRequest{
		BaseURL: "http://ollama.test",
		Model:   "chat-box-model",
		Messages: []ChatMessage{{
			Role:    "user",
			Content: "Change the voice in this clip to a deep male voice.",
			Audios:  []string{source},
		}},
	})

	if submitted == nil {
		t.Fatal("change_voice never submitted to fal — the tool did not run")
	}
	if submittedPath != "/"+defaultFalVoiceChangerModel {
		t.Fatalf("submitted to %q, want the voice changer %q", submittedPath, "/"+defaultFalVoiceChangerModel)
	}
	if !uploaded {
		t.Error("expected the source clip to upload to fal's CDN")
	}
	want := map[string]any{"audio_url": cdnSource, "voice": "Daniel"}
	if !reflect.DeepEqual(submitted, want) {
		t.Fatalf("submitted body = %v, want %v (the hosted clip and the catalog spelling, nothing else)", submitted, want)
	}

	assistant := persistedAssistantTurn(t, config, 1)
	hasAudio := false
	for _, content := range assistant.Content {
		if content.Type == "audio" {
			hasAudio = true
		}
	}
	if !hasAudio {
		t.Fatalf("assistant turn has no audio artifact: %+v", assistant.Content)
	}

	// The ledger: fal attribution, the change-voice command, and the per-minute
	// estimate over the source's own 2 seconds ($0.3/min → $0.01).
	activity := persistedToolActivity(t, config)
	if activity["name"] != voiceChangerToolName || activity["provider"] != "fal" || activity["mediaKind"] != "audio" {
		t.Fatalf("activity = %+v, want change_voice on fal producing audio", activity)
	}
	wantCommand := []any{"fal", "change-voice", defaultFalVoiceChangerModel}
	if !reflect.DeepEqual(activity["command"], wantCommand) {
		t.Errorf("activity command = %v, want %v", activity["command"], wantCommand)
	}
	if activity["costMicros"] != float64(10000) || activity["costUnknown"] == true {
		t.Errorf("activity cost = %v (unknown %v), want 10000 micros from 2s at $0.3 per minute", activity["costMicros"], activity["costUnknown"])
	}
}

// TestHarnessVoiceChangeAbsentSkipsPlanner covers the turn where triage routes
// a voice change but change_voice is not registered (here: another audio
// provider, with fal speech still configured so the audio-mode force-tool
// fallback WOULD have a tool to name). Either needsTools spelling converges:
// the planner never runs, no request reaches fal, the final model is told why
// and what to advise, and the reply carries the blockquote.
func TestHarnessVoiceChangeAbsentSkipsPlanner(t *testing.T) {
	for _, tc := range []struct {
		name       string
		needsTools string
	}{
		{"triage set needsTools true", "true"},
		{"triage left tools off", "false"},
	} {
		t.Run(tc.name, func(t *testing.T) {
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
			// The fal key is present and the slot is seeded; only the audio
			// provider keeps change_voice out of the registry.
			config.Models.AudioProvider = "replicate"
			config.Providers.Fal.AudioModel = defaultFalAudioModel
			if err := writeAppConfig(config); err != nil {
				t.Fatalf("writeAppConfig: %v", err)
			}

			harnessCalls := 0
			plannerRan := false
			falReached := false
			finalSawNote := false
			app := NewApp()
			app.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if strings.Contains(req.URL.Host, "fal.run") {
					falReached = true
				}
				switch req.URL.Path {
				case "/api/show":
					return jsonResponse(`{"capabilities":[],"model_info":{},"details":{"family":"test","parameter_size":"1B"}}`), nil
				case "/api/chat":
					payload := chatPayload(t, req)
					if payload["stream"] == false {
						harnessCalls++
						if harnessCalls > 1 {
							plannerRan = true
							return chatCompletion("harness-model", `{"brief":"never","needsTools":true,"reason":"x","toolCalls":[]}`), nil
						}
						return chatCompletion("harness-model", `{"needsTools":`+tc.needsTools+`,"responseMode":"audio","toolTask":"Change the voice of the attached clip using change_voice.","reason":"The user asked to re-voice a recording."}`), nil
					}
					messages, _ := json.Marshal(payload["messages"])
					if strings.Contains(string(messages), "the voice changer is not enabled on this machine") {
						finalSawNote = true
					}
					body := fmt.Sprintln(`{"model":"chat-box-model","message":{"role":"assistant","content":"Sorry, I can't change a voice in this chat."},"done":false}`) +
						fmt.Sprintln(`{"model":"chat-box-model","done":true,"done_reason":"stop","eval_count":3}`)
					return &http.Response{StatusCode: 200, Status: "200 OK",
						Body:   io.NopCloser(strings.NewReader(body)),
						Header: http.Header{"Content-Type": []string{"application/x-ndjson"}}}, nil
				default:
					t.Fatalf("unexpected request %s %s", req.Method, req.URL)
					return nil, nil
				}
			})

			app.runChatStream(context.Background(), "request-voice-change-absent", ChatRequest{
				BaseURL: "http://ollama.test",
				Model:   "chat-box-model",
				Messages: []ChatMessage{{
					Role:    "user",
					Content: "Change the voice in this clip.",
				}},
			})

			if plannerRan {
				t.Error("the planner ran although change_voice is not registered")
			}
			if falReached {
				t.Error("a request reached fal although change_voice is not registered")
			}
			if !finalSawNote {
				t.Error("the final model was not told why the voice change cannot run")
			}
			if text := assistantText(t, config); !strings.Contains(text, "Voice changing isn't set up yet") {
				t.Errorf("reply lacks the fallback blockquote:\n%s", text)
			}
		})
	}
}

// TestListFalVoiceChangerModels pins the picker: the static default always
// leads, the id/tag "voice-changer" marker admits both spellings from either
// voice category while design and extend endpoints stay out, and a failed
// catalog keeps the default alone.
func TestListFalVoiceChangerModels(t *testing.T) {
	app := &App{falCategoryFetch: func(_ context.Context, category string) ([]FalModel, error) {
		switch category {
		case falAudioToAudioCategory:
			return []FalModel{
				{ID: "fal-ai/stable-audio-25/inpaint"},
				{ID: "fal-ai/elevenlabs/voice-changer/pro", Tags: []string{"audio-to-audio"}},
				{ID: "fal-ai/kling/create-voice"},
			}, nil
		case falTextToSpeechCategory:
			return []FalModel{
				{ID: "fal-ai/minimax/voice-design"},
				{ID: "acme/voice_changer"},
				{ID: "acme/revoice", Tags: []string{"Voice Changer"}},
			}, nil
		}
		return nil, errors.New("unexpected category " + category)
	}}
	got, err := app.ListFalVoiceChangerModels()
	if err != nil {
		t.Fatalf("ListFalVoiceChangerModels: %v", err)
	}
	want := []string{defaultFalVoiceChangerModel, "fal-ai/elevenlabs/voice-changer/pro", "acme/voice_changer", "acme/revoice"}
	if len(got) != len(want) {
		t.Fatalf("voice changer list = %+v, want %v", got, want)
	}
	for i, id := range want {
		if got[i].ID != id {
			t.Errorf("voice changer list[%d] = %q, want %q", i, got[i].ID, id)
		}
	}

	failing := &App{falCategoryFetch: func(_ context.Context, category string) ([]FalModel, error) {
		return nil, errors.New("fal rate limited: Rate limit exceeded")
	}}
	got, err = failing.ListFalVoiceChangerModels()
	if err != nil || len(got) != 1 || got[0].ID != defaultFalVoiceChangerModel {
		t.Fatalf("catalog failure: list = %+v err = %v, want the static default alone", got, err)
	}
}

// TestConversationModelOverridesVoiceChangerModel pins the per-conversation
// override: a pinned voice changer lands on the fal slot (the tool is
// fal-only, so the audio provider doesn't reroute it), and an unset field
// inherits the global slot.
func TestConversationModelOverridesVoiceChangerModel(t *testing.T) {
	if got := normalizeConversationModelOverrides(ConversationModelOverrides{VoiceChangerModel: "  acme/voice-changer "}); got.VoiceChangerModel != "acme/voice-changer" {
		t.Fatalf("normalized voiceChangerModel = %q, want it trimmed", got.VoiceChangerModel)
	}
	for _, provider := range []string{"fal", "replicate"} {
		config := voiceChangeTestConfig()
		config.Models.AudioProvider = provider
		overlaid, _, err := overlayModelOverrides(config, ChatRequest{}, ConversationModelOverrides{VoiceChangerModel: "acme/voice-changer"})
		if err != nil {
			t.Fatalf("overlay: %v", err)
		}
		if overlaid.Providers.Fal.VoiceChangerModel != "acme/voice-changer" {
			t.Errorf("%s provider: overlaid voiceChangerModel = %q, want the pinned override on the fal slot", provider, overlaid.Providers.Fal.VoiceChangerModel)
		}
	}
	overlaid, _, err := overlayModelOverrides(voiceChangeTestConfig(), ChatRequest{}, ConversationModelOverrides{})
	if err != nil {
		t.Fatalf("overlay: %v", err)
	}
	if overlaid.Providers.Fal.VoiceChangerModel != defaultFalVoiceChangerModel {
		t.Fatalf("unset override: voiceChangerModel = %q, want the global slot inherited", overlaid.Providers.Fal.VoiceChangerModel)
	}
}

// TestVoiceChangeBillingAndPricingBatch pins the money side: a per-minute quote
// prices off the source's seconds while a flat quote prices as one call, an
// unmeasurable source prices nothing (never a guess), and the endpoint joins
// the shared pricing batch only on the fal route.
func TestVoiceChangeBillingAndPricingBatch(t *testing.T) {
	hints := falBillingHints{Requests: 1, Seconds: 2}
	if quantity, ok := hints.quantityForUnit("minutes"); !ok || math.Abs(quantity-2.0/60) > 1e-9 {
		t.Fatalf("minutes quantity = (%v, %v), want 2s as 1/30 of a minute", quantity, ok)
	}
	if quantity, ok := hints.quantityForUnit("request"); !ok || quantity != 1 {
		t.Fatalf("request quantity = (%v, %v), want one call", quantity, ok)
	}
	if _, ok := (falBillingHints{Requests: 1}).quantityForUnit("minutes"); ok {
		t.Fatal("an unmeasurable source must not price a per-minute quote")
	}

	if got := dataURLMediaType("data:audio/wav;codec=pcm;base64,UklGRg=="); got != "audio/wav" {
		t.Errorf("dataURLMediaType = %q, want audio/wav", got)
	}
	if got := dataURLMediaType("https://v3.fal.media/files/x.wav"); got != "" {
		t.Errorf("dataURLMediaType of a hosted URL = %q, want empty", got)
	}

	for name, provider := range map[string]string{"fal": "fal", "replicate": "replicate", "runware": "runware"} {
		config := defaultAppConfig()
		config.Models.AudioProvider = provider
		found := false
		for _, id := range configuredFalEndpointIDs(config) {
			if id == defaultFalVoiceChangerModel {
				found = true
			}
		}
		if found != (provider == "fal") {
			t.Errorf("%s audio provider: voice changer in the pricing batch = %v", name, found)
		}
	}
}
