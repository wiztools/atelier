package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
)

// TestResolveAudioProvider pins the routing truth for the non-voice audio
// tools: Models.AudioProvider selects replicate, everything else (empty,
// junk) keeps fal — audio was fal-only before Replicate served it.
func TestResolveAudioProvider(t *testing.T) {
	fal := defaultAppConfig()
	if got := resolveAudioProvider(fal); got != "fal" {
		t.Fatalf("default audio provider = %q, want fal", got)
	}
	junk := defaultAppConfig()
	junk.Models.AudioProvider = "openrouter"
	if got := resolveAudioProvider(junk); got != "fal" {
		t.Fatalf("junk audio provider = %q, want fal", got)
	}
	replicate := defaultAppConfig()
	replicate.Models.AudioProvider = "replicate"
	if got := resolveAudioProvider(replicate); got != "replicate" {
		t.Fatalf("pinned audio provider = %q, want replicate", got)
	}
}

// TestSoundEffectsGatingFollowsAudioProvider confirms both non-voice gates
// check the ROUTED provider's slot and key: on the replicate route a fal key
// lights up nothing and a replicate key with the seeded slots does, and
// airplane mode drops the tools on both routes. Mirrors
// TestSoundEffectsGenerationToolGating's keyring-pin pattern.
func TestSoundEffectsGatingFollowsAudioProvider(t *testing.T) {
	keyring.MockInit()
	t.Cleanup(func() {
		_ = clearFalAPIKey()
		_ = clearReplicateAPIKey()
	})

	replicate := defaultAppConfig()
	replicate.Models.AudioProvider = "replicate"
	replicate = mergeAppConfig(replicate)
	if replicate.Providers.Replicate.SoundEffectsModel == "" || replicate.Providers.Replicate.SoundEffectsVideoModel == "" {
		t.Fatal("selecting the replicate audio provider must seed both sound slots")
	}
	// Provider selected but no replicate key → not configured (would only
	// fail at call time), even with a fal key around.
	if err := saveFalAPIKey("fal-test-key"); err != nil {
		t.Fatalf("saveFalAPIKey: %v", err)
	}
	if soundEffectsGenerationConfigured(replicate) {
		t.Fatal("generate_sound must stay unconfigured on the replicate route without a replicate key")
	}
	if soundEffectsVideoConfigured(replicate) {
		t.Fatal("add_sound_effects must stay unconfigured on the replicate route without a replicate key")
	}
	if _, ok := defaultHarnessToolRegistry(context.Background(), replicate, nil).Get("generate_sound"); ok {
		t.Fatal("generate_sound should be absent without a replicate key")
	}

	if err := saveReplicateAPIKey("replicate-test-key"); err != nil {
		t.Fatalf("saveReplicateAPIKey: %v", err)
	}
	if !soundEffectsGenerationConfigured(replicate) {
		t.Fatal("generate_sound should be configured on the replicate route with key and seeded slots")
	}
	if !soundEffectsVideoConfigured(replicate) {
		t.Fatal("add_sound_effects should be configured on the replicate route with key and seeded slots")
	}
	registry := defaultHarnessToolRegistry(context.Background(), replicate, nil)
	if _, ok := registry.Get("generate_sound"); !ok {
		t.Fatal("generate_sound should be registered on the replicate route")
	}
	if _, ok := registry.Get("add_sound_effects"); !ok {
		t.Fatal("add_sound_effects should be registered on the replicate route")
	}

	replicate.AirplaneMode = true
	if soundEffectsGenerationConfigured(replicate) || soundEffectsVideoConfigured(replicate) {
		t.Fatal("airplane mode must drop both non-voice audio tools on the replicate route")
	}

	// The replicate default resolvers route by the same flag: the seeded
	// slots win, then the built-in replicate constants.
	if got := resolveDefaultSoundEffectsModel(replicate); got != defaultReplicateSoundEffectsModel {
		t.Fatalf("replicate sound default = %q, want %q", got, defaultReplicateSoundEffectsModel)
	}
	if got := resolveDefaultSoundEffectsVideoModel(replicate); got != defaultReplicateSoundEffectsVideoModel {
		t.Fatalf("replicate video-sfx default = %q, want %q", got, defaultReplicateSoundEffectsVideoModel)
	}
	replicate.Providers.Replicate.SoundEffectsModel = "acme/music"
	replicate.AirplaneMode = false
	if got := resolveDefaultSoundEffectsModel(replicate); got != "acme/music" {
		t.Fatalf("configured replicate sound slot should win, got %q", got)
	}

	// The fal route is untouched by the replicate slots: without a fal key,
	// a configured fal slot stays unconfigured even though replicate is
	// selected elsewhere.
	if err := clearFalAPIKey(); err != nil {
		t.Fatalf("clearFalAPIKey: %v", err)
	}
	falRoute := defaultAppConfig()
	falRoute.Providers.Fal.SoundEffectsModel = "fal-ai/some/sound-model"
	if soundEffectsGenerationConfigured(falRoute) {
		t.Fatal("fal route must still require the fal key")
	}
}

// TestMergeAppConfigNormalizesAudioProvider pins the provider normalization
// (unknown/empty → fal) and the one-way seed rule: selecting replicate seeds
// the empty slots once, an explicit model always wins, and the fal slots stay
// untouched by a replicate selection.
func TestMergeAppConfigNormalizesAudioProvider(t *testing.T) {
	base := mergeAppConfig(defaultAppConfig())
	if base.Models.AudioProvider != "fal" {
		t.Fatalf("default audioProvider = %q, want fal", base.Models.AudioProvider)
	}
	junk := defaultAppConfig()
	junk.Models.AudioProvider = "ollama"
	if merged := mergeAppConfig(junk); merged.Models.AudioProvider != "fal" {
		t.Fatalf("junk audioProvider = %q, want fal", merged.Models.AudioProvider)
	}

	replicate := defaultAppConfig()
	replicate.Models.AudioProvider = "replicate"
	replicate.Providers.Replicate.SoundEffectsModel = "acme/chosen"
	merged := mergeAppConfig(replicate)
	if merged.Providers.Replicate.SoundEffectsModel != "acme/chosen" {
		t.Fatalf("explicit replicate slot must win, got %q", merged.Providers.Replicate.SoundEffectsModel)
	}
	if merged.Providers.Replicate.SoundEffectsVideoModel != defaultReplicateSoundEffectsVideoModel {
		t.Fatalf("empty replicate video-sfx slot should seed, got %q", merged.Providers.Replicate.SoundEffectsVideoModel)
	}
	if merged.Providers.Fal.SoundEffectsModel != "" {
		t.Fatalf("a replicate selection must not seed the fal slots, got %q", merged.Providers.Fal.SoundEffectsModel)
	}
}

// replicateAudioFixtureSchema builds an OpenAPI-shaped input schema like the
// replicate_params_test fixture pattern: a prompt and a bounded numeric
// duration, plus an optional negative_prompt.
const replicateAudioFixtureSchema = `{"components":{"schemas":{"Input":{"type":"object","properties":{
	"prompt":{"type":"string"},
	"duration":{"type":"number","minimum":1,"maximum":10},
	"negative_prompt":{"type":"string"}
}}}}}`

const replicateLyricsOnlySchema = `{"components":{"schemas":{"Input":{"type":"object","properties":{
	"lyrics":{"type":"string"}
}}}}}`

// TestResolveReplicateAudioInput pins the canonical→native mapping for
// generate_sound on the replicate route: prompt is the hard requirement (an
// unmappable prompt refuses instead of speculating), duration clamps to the
// declared bounds with a notice, unknown canonicals drop with notices, and a
// nil schema degrades to a minimal body.
func TestResolveReplicateAudioInput(t *testing.T) {
	schema := parseReplicateFixtureSchema(t, replicateAudioFixtureSchema)

	body, notices, err := resolveReplicateAudioInput(schema, AudioGenerateRequest{
		Model:          "acme/audio",
		Prompt:         "soft rain",
		Duration:       "30",
		NegativePrompt: "vocals",
	})
	if err != nil {
		t.Fatalf("resolveReplicateAudioInput: %v", err)
	}
	if got, ok := body["prompt"].(string); !ok || got != "soft rain" {
		t.Fatalf("prompt = %#v, want \"soft rain\"", body["prompt"])
	}
	if got, ok := body["duration"].(float64); !ok || got != 10 {
		t.Fatalf("duration = %#v, want the clamped 10", body["duration"])
	}
	if got, ok := body["negative_prompt"].(string); !ok || got != "vocals" {
		t.Fatalf("negative_prompt = %#v, want \"vocals\"", body["negative_prompt"])
	}
	if !strings.Contains(strings.Join(notices, "\n"), "maximum") {
		t.Fatalf("duration clamp should ride a notice, got %v", notices)
	}

	// No duration control → dropped with a notice, never a guessed field.
	bare := parseReplicateFixtureSchema(t, `{"components":{"schemas":{"Input":{"type":"object","properties":{
		"prompt":{"type":"string"}
	}}}}}`)
	body, notices, err = resolveReplicateAudioInput(bare, AudioGenerateRequest{Model: "acme/audio", Prompt: "rain", Duration: "10"})
	if err != nil {
		t.Fatalf("resolveReplicateAudioInput: %v", err)
	}
	if _, ok := body["duration"]; ok {
		t.Fatalf("duration must not be sent without a declared input, got %#v", body)
	}
	if len(notices) != 1 || !strings.Contains(notices[0], "sound length") {
		t.Fatalf("duration drop should ride a notice, got %v", notices)
	}

	// A lyrics-only music model binds its prompt-shaped input; the request
	// still works.
	lyrics := parseReplicateFixtureSchema(t, replicateLyricsOnlySchema)
	body, _, err = resolveReplicateAudioInput(lyrics, AudioGenerateRequest{Model: "acme/music", Prompt: "a verse about rain"})
	if err != nil {
		t.Fatalf("resolveReplicateAudioInput: %v", err)
	}
	if got, ok := body["lyrics"].(string); !ok || got != "a verse about rain" {
		t.Fatalf("lyrics binding = %#v, want the prompt", body["lyrics"])
	}

	// A model with no prompt-shaped input at all refuses up front —
	// Replicate rejects unknown fields, so the fal prompt+text double-write
	// would only 422 downstream.
	none := parseReplicateFixtureSchema(t, `{"components":{"schemas":{"Input":{"type":"object","properties":{
		"seed":{"type":"integer"}
	}}}}}`)
	if _, _, err := resolveReplicateAudioInput(none, AudioGenerateRequest{Model: "acme/audio", Prompt: "rain"}); err == nil {
		t.Fatal("an unmappable prompt must refuse instead of speculating")
	}

	// A nil schema degrades to the minimal body plus a notice.
	body, notices, err = resolveReplicateAudioInput(nil, AudioGenerateRequest{Model: "acme/audio", Prompt: "rain"})
	if err != nil {
		t.Fatalf("resolveReplicateAudioInput(nil): %v", err)
	}
	if len(body) != 1 || body["prompt"] != "rain" {
		t.Fatalf("nil-schema body = %#v, want prompt only", body)
	}
	if len(notices) != 1 {
		t.Fatalf("nil schema should ride a notice, got %v", notices)
	}
}

// TestResolveSFXBodyReplicateCategory pins the replicate branch of the shared
// SFX resolver: the nil-schema fallback writes Replicate's "video" input name
// (the fal spelling would 422 there), and the category table binds mmaudio's
// names.
func TestResolveSFXBodyReplicateCategory(t *testing.T) {
	body, notices, err := resolveSFXBodyForCategory(nil, SFXGenerateRequest{
		Model:  "acme/sfx",
		Video:  "data:video/mp4;base64,AAAA",
		Prompt: "thunder",
	}, Overrides{}, "replicate-sfx")
	if err != nil {
		t.Fatalf("resolveSFXBodyForCategory(nil): %v", err)
	}
	if _, ok := body["video"]; !ok {
		t.Fatalf("nil-schema replicate body = %#v, want the video key", body)
	}
	if _, ok := body["video_url"]; ok {
		t.Fatalf("nil-schema replicate body must not write the fal name, got %#v", body)
	}
	if len(notices) != 1 {
		t.Fatalf("nil schema should ride a notice, got %v", notices)
	}

	schema := parseReplicateFixtureSchema(t, `{"components":{"schemas":{"Input":{"type":"object","properties":{
		"video":{"type":"string","format":"uri"},
		"prompt":{"type":"string"},
		"duration":{"type":"number","maximum":30}
}}}}}`)
	body, notices, err = resolveSFXBodyForCategory(schema, SFXGenerateRequest{
		Model:    "acme/sfx",
		Video:    "https://example.com/clip.mp4",
		Prompt:   "thunder",
		Duration: 12,
	}, Overrides{}, "replicate-sfx")
	if err != nil {
		t.Fatalf("resolveSFXBodyForCategory: %v", err)
	}
	if _, ok := body["video"]; !ok {
		t.Fatalf("video input should bind, got %#v", body)
	}
	if got, ok := body["duration"].(float64); !ok || got != 12 {
		t.Fatalf("duration = %#v, want 12", body["duration"])
	}
	if len(notices) != 0 {
		t.Fatalf("a clean map should carry no notices, got %v", notices)
	}
}

// TestReplicateAudioURLShapes pins the output scavenge ladder: a URL string,
// a one-element array, and a nested object walk all resolve; a payload with
// no audio-shaped URL does not.
func TestReplicateAudioURLShapes(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"string", `"https://replicate.delivery/output.wav"`, "https://replicate.delivery/output.wav"},
		{"array", `["https://replicate.delivery/output.mp3"]`, "https://replicate.delivery/output.mp3"},
		{"nested", `{"audio":"https://replicate.delivery/output.flac"}`, "https://replicate.delivery/output.flac"},
		{"extension-less walk misses", `{"audio":"https://replicate.delivery/file"}`, ""},
		{"not audio", `{"video":"https://replicate.delivery/out.mp4"}`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := replicateAudioURL(json.RawMessage(tc.raw)); got != tc.want {
				t.Fatalf("replicateAudioURL = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestSoundToolActivitiesStampProvider pins the captured-config attribution:
// the replicate generate_sound builder stamps Provider replicate and the
// replicate command; the fal builder leaves the engine's fal default in
// place. The SFX video builder stamps both output kinds.
func TestSoundToolActivitiesStampProvider(t *testing.T) {
	tools := HarnessToolExecutionContext{
		Config: mergeAppConfig(defaultAppConfig()),
		GenerateAudio: func(ctx context.Context, req AudioGenerateRequest) (GeneratedAudio, error) {
			return GeneratedAudio{Data: []byte("x"), MimeType: "audio/mpeg"}, nil
		},
	}
	replicateDef := soundEffectsGenerationToolDefinition("replicate")
	out, _, err := replicateDef.Execute(context.Background(), tools, HarnessToolCall{Content: "rain", Model: "acme/music"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	activity := replicateDef.Activity(HarnessToolResult{Name: "generate_sound", Status: "completed", Result: out})
	if activity.Provider != "replicate" {
		t.Fatalf("replicate sound activity provider = %q, want replicate", activity.Provider)
	}
	if command := strings.Join(activity.Command, " "); command != "replicate generate acme/music" {
		t.Fatalf("replicate sound activity command = %q", command)
	}

	falDef := soundEffectsGenerationToolDefinition("fal")
	falOut, _, err := falDef.Execute(context.Background(), tools, HarnessToolCall{Content: "rain", Model: "fal-ai/m"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	falActivity := falDef.Activity(HarnessToolResult{Name: "generate_sound", Status: "completed", Result: falOut})
	if falActivity.Provider != "" {
		t.Fatalf("fal sound activity should leave the engine default, got %q", falActivity.Provider)
	}

	const clip = "data:video/mp4;base64,AAAA"
	sfxTools := HarnessToolExecutionContext{
		Config:         mergeAppConfig(defaultAppConfig()),
		AttachedVideos: []string{clip},
		GenerateSFX: func(ctx context.Context, req SFXGenerateRequest) (SFXGenerateResult, error) {
			return SFXGenerateResult{Audio: GeneratedAudio{Data: []byte("x"), MimeType: "audio/mpeg"}}, nil
		},
	}
	sfxDef := soundEffectsVideoToolDefinition("replicate")
	sfxOut, _, err := sfxDef.Execute(context.Background(), sfxTools, HarnessToolCall{Content: "thunder", Model: "acme/sfx"})
	if err != nil {
		t.Fatalf("SFX Execute: %v", err)
	}
	if _, ok := sfxOut.(ToolAudioResult); !ok {
		t.Fatalf("expected a standalone-soundtrack ToolAudioResult, got %T", sfxOut)
	}
	sfxActivity := sfxDef.Activity(HarnessToolResult{Name: "add_sound_effects", Status: "completed", Result: sfxOut})
	if sfxActivity.Provider != "replicate" {
		t.Fatalf("replicate sfx activity provider = %q, want replicate", sfxActivity.Provider)
	}
	if sfxActivity.MediaKind != "audio" {
		t.Fatalf("standalone soundtrack should report audio, got %q", sfxActivity.MediaKind)
	}
}

// TestConversationModelOverridesAudioProvider pins the override dimension's
// contract: validation whitelist, and the sound slots landing on the slot the
// effective provider reads (records written before the dimension existed keep
// landing on fal's).
func TestConversationModelOverridesAudioProvider(t *testing.T) {
	overrides := ConversationModelOverrides{AudioProvider: "openrouter"}
	if err := validateConversationModelOverrides(overrides); err == nil {
		t.Fatal("an unknown audio provider must be rejected")
	}
	overrides.AudioProvider = "replicate"
	if err := validateConversationModelOverrides(overrides); err != nil {
		t.Fatalf("validateConversationModelOverrides: %v", err)
	}

	config := mergeAppConfig(defaultAppConfig())
	var err error
	config, _, err = applyConversationModelOverrides(config, ChatRequest{ModelOverrides: &overrides})
	if err != nil {
		t.Fatalf("applyConversationModelOverrides: %v", err)
	}
	if config.Models.AudioProvider != "replicate" {
		t.Fatalf("audio provider overlay = %q, want replicate", config.Models.AudioProvider)
	}

	config = mergeAppConfig(defaultAppConfig())
	config, _, err = applyConversationModelOverrides(config, ChatRequest{ModelOverrides: &ConversationModelOverrides{
		AudioProvider:     "replicate",
		SoundEffectsModel: "acme/music",
	}})
	if err != nil {
		t.Fatalf("applyConversationModelOverrides: %v", err)
	}
	if got := config.Providers.Replicate.SoundEffectsModel; got != "acme/music" {
		t.Fatalf("replicate-pinned sound model = %q, want it on the replicate slot", got)
	}
	if got := config.Providers.Fal.SoundEffectsModel; got != "" {
		t.Fatalf("replicate-pinned sound model must not touch the fal slot, got %q", got)
	}

	// No provider override → the record keeps landing on fal's slot.
	config = mergeAppConfig(defaultAppConfig())
	config, _, err = applyConversationModelOverrides(config, ChatRequest{ModelOverrides: &ConversationModelOverrides{SoundEffectsModel: "fal-ai/kept"}})
	if err != nil {
		t.Fatalf("applyConversationModelOverrides: %v", err)
	}
	if got := config.Providers.Fal.SoundEffectsModel; got != "fal-ai/kept" {
		t.Fatalf("fal-legacy sound model = %q, want it on the fal slot", got)
	}
}

// TestMediaGenGapAudioReplicate pins the provider-aware audio remedy: a
// replicate-pinned setup learns about the Replicate token, the fal setup
// keeps the fal copy, and an answer naming replicate suppresses the replicate
// fallback notice.
func TestMediaGenGapAudioReplicate(t *testing.T) {
	note := mediaGenUnavailableNote("audio", false, false, true)
	if !strings.Contains(note, "Replicate API token") || strings.Contains(note, "fal.ai API key") {
		t.Fatalf("replicate-pinned audio gap should name the token, got %q", note)
	}
	falNote := mediaGenUnavailableNote("audio", false, false, false)
	if !strings.Contains(falNote, "fal.ai API key") {
		t.Fatalf("fal audio gap should keep the fal remedy, got %q", falNote)
	}
	if got := mediaGenFallbackNotice(true, "audio", "Add a Replicate token in Settings.", false, false, true); got != "" {
		t.Fatalf("a replicate-remedied answer should suppress the notice, got %q", got)
	}
	notice := mediaGenFallbackNotice(true, "audio", "Sure, generating now.", false, false, true)
	if !strings.Contains(notice, "Replicate API token") {
		t.Fatalf("replicate fallback notice should name the token, got %q", notice)
	}
}

// TestReplicateSoundPickerFilters pins the collection partitions: the music
// picker drops voice/tts ids, the video-sfx picker keeps only the
// audio-marked entries of the video-editing collection.
func TestReplicateSoundPickerFilters(t *testing.T) {
	if !isReplicateSoundModel(ReplicateModel{ID: "stability-ai/stable-audio-2.5"}) {
		t.Fatal("stable-audio should pass the sound filter")
	}
	if isReplicateSoundModel(ReplicateModel{ID: "acme/tts-pro"}) || isReplicateSoundModel(ReplicateModel{ID: "minimax/voice-clone"}) {
		t.Fatal("voice/tts ids must drop out of the sound picker")
	}
	if !isReplicateVideoSFXModel(ReplicateModel{ID: "zsxkib/mmaudio"}) {
		t.Fatal("mmaudio should pass the video-sfx filter")
	}
	if isReplicateVideoSFXModel(ReplicateModel{ID: "luma/reframe-video"}) || isReplicateVideoSFXModel(ReplicateModel{ID: "xai/grok-imagine-video-extension"}) {
		t.Fatal("reframe/extend ids must drop out of the video-sfx picker")
	}
	// Plain "audio" is not a generator marker: the collection's audio
	// utilities must never reach the video-sfx picker.
	if isReplicateVideoSFXModel(ReplicateModel{ID: "lucataco/extract-audio"}) || isReplicateVideoSFXModel(ReplicateModel{ID: "lucataco/video-audio-merge"}) {
		t.Fatal("audio-utility ids must drop out of the video-sfx picker")
	}
	if !isReplicateVideoSFXModel(ReplicateModel{ID: "zsxkib/mmaudio-t4"}) || !isReplicateVideoSFXModel(ReplicateModel{ID: "zsxkib/thinksound"}) {
		t.Fatal("the verified mmaudio/thinksound packagings should pass the pre-filter")
	}
}

// TestReplicateSFXSchemaServes pins the picker's precise filter: a model
// needs a scalar-string video input through the replicate-sfx synonyms.
func TestReplicateSFXSchemaServes(t *testing.T) {
	mmaudio := parseReplicateFixtureSchema(t, `{"components":{"schemas":{"Input":{"type":"object","properties":{
		"video":{"type":"string","format":"uri"},
		"prompt":{"type":"string"},
		"duration":{"type":"number"}
	}}}}}`)
	if !replicateSFXSchemaServes(mmaudio) {
		t.Fatal("an mmaudio-style schema should serve video sound effects")
	}
	utility := parseReplicateFixtureSchema(t, `{"components":{"schemas":{"Input":{"type":"object","properties":{
		"video_path":{"type":"string"},
		"audio_path":{"type":"string"}
	}}}}}`)
	if replicateSFXSchemaServes(utility) {
		t.Fatal("a schema without a source-video synonym must not serve")
	}
}

// TestResolveSFXBodyCaptionSteering pins ThinkSound's caption input binding:
// a model steering through "caption" alone receives the required sound
// description there.
func TestResolveSFXBodyCaptionSteering(t *testing.T) {
	schema := parseReplicateFixtureSchema(t, `{"components":{"schemas":{"Input":{"type":"object","properties":{
		"video":{"type":"string","format":"uri"},
		"caption":{"type":"string"},
		"cot":{"type":"boolean"}
	}}}}}`)
	body, notices, err := resolveSFXBodyForCategory(schema, SFXGenerateRequest{
		Model:  "zsxkib/thinksound",
		Video:  "https://example.com/clip.mp4",
		Prompt: "distant thunder, footsteps on gravel",
	}, Overrides{}, "replicate-sfx")
	if err != nil {
		t.Fatalf("resolveSFXBodyForCategory: %v", err)
	}
	if got, ok := body["caption"].(string); !ok || got != "distant thunder, footsteps on gravel" {
		t.Fatalf("caption = %#v, want the steering prompt", body["caption"])
	}
	for _, notice := range notices {
		if strings.Contains(notice, "takes no sound description") {
			t.Fatalf("caption must bind, got a drop notice: %v", notices)
		}
	}
}

const replicateContinuationSchema = `{"components":{"schemas":{"Input":{"type":"object","properties":{
	"prompt":{"type":"string"},
	"input_audio":{"type":"string","format":"uri"},
	"continuation":{"type":"boolean","default":false},
	"duration":{"type":"integer","default":8}
}}}}}`

// TestResolveReplicateAudioExtendInput pins the continuation mapping: source
// + flag + prompt are the operation (an unmappable one refuses), the canonical
// ADDED length becomes the model's TOTAL output clamped to the musicgen house
// cap, style folds into the prompt, "before" refuses naming fal, and "auto"
// sends no duration so the model default applies.
func TestResolveReplicateAudioExtendInput(t *testing.T) {
	schema := parseReplicateFixtureSchema(t, replicateContinuationSchema)

	body, notices, err := resolveReplicateAudioExtendInput(schema, AudioExtendRequest{
		Model:                 "meta/musicgen",
		Prompt:                "more rain",
		Duration:              "25",
		Style:                 "ambient",
		SourceAudio:           "https://example.com/clip.mp3",
		SourceDurationSeconds: 10,
	})
	if err != nil {
		t.Fatalf("resolveReplicateAudioExtendInput: %v", err)
	}
	if got, ok := body["input_audio"].(string); !ok || got != "https://example.com/clip.mp3" {
		t.Fatalf("input_audio = %#v", body["input_audio"])
	}
	if got, ok := body["continuation"].(bool); !ok || !got {
		t.Fatalf("continuation = %#v, want true", body["continuation"])
	}
	if got, ok := body["duration"].(int); !ok || got != 30 {
		t.Fatalf("duration = %#v, want the capped 30", body["duration"])
	}
	if got, ok := body["prompt"].(string); !ok || got != "more rain ambient" {
		t.Fatalf("prompt = %#v, want style folded in", body["prompt"])
	}
	if !strings.Contains(strings.Join(notices, "\n"), "output cap") {
		t.Fatalf("the clamp should ride a notice, got %v", notices)
	}

	// A source that already fills the cap refuses instead of re-rendering it.
	if _, _, err := resolveReplicateAudioExtendInput(schema, AudioExtendRequest{
		Model: "meta/musicgen", Prompt: "x", Duration: "5",
		SourceAudio: "https://example.com/long.mp3", SourceDurationSeconds: 30,
	}); err == nil || !strings.Contains(err.Error(), "fal.ai") {
		t.Fatalf("a cap-filled source must refuse with the fal remedy, got %v", err)
	}

	// "auto" sends no duration (the model default applies).
	body, _, err = resolveReplicateAudioExtendInput(schema, AudioExtendRequest{
		Model: "meta/musicgen", Prompt: "continue", SourceAudio: "https://example.com/clip.mp3",
	})
	if err != nil {
		t.Fatalf("auto extend: %v", err)
	}
	if _, ok := body["duration"]; ok {
		t.Fatalf("auto must not send a duration, got %#v", body)
	}

	// "before" refuses naming fal; a model without the continuation flag
	// refuses; a model without a source-audio input refuses.
	if _, _, err := resolveReplicateAudioExtendInput(schema, AudioExtendRequest{
		Model: "meta/musicgen", Prompt: "x", Direction: "before", SourceAudio: "https://example.com/c.mp3",
	}); err == nil || !strings.Contains(err.Error(), "fal.ai") {
		t.Fatalf("before must refuse with the fal remedy, got %v", err)
	}
	textOnly := parseReplicateFixtureSchema(t, `{"components":{"schemas":{"Input":{"type":"object","properties":{
		"prompt":{"type":"string"}
	}}}}}`)
	if _, _, err := resolveReplicateAudioExtendInput(textOnly, AudioExtendRequest{
		Model: "acme/t2m", Prompt: "x", SourceAudio: "https://example.com/c.mp3",
	}); err == nil || !strings.Contains(err.Error(), "continuation") {
		t.Fatalf("a model without a continuation flag must refuse, got %v", err)
	}
	uriOnly := parseReplicateFixtureSchema(t, `{"components":{"schemas":{"Input":{"type":"object","properties":{
		"continuation":{"type":"boolean"}
	}}}}}`)
	if _, _, err := resolveReplicateAudioExtendInput(uriOnly, AudioExtendRequest{
		Model: "acme/odd", Prompt: "x", SourceAudio: "https://example.com/c.mp3",
	}); err == nil || !strings.Contains(err.Error(), "source-audio") {
		t.Fatalf("a model without a source-audio input must refuse, got %v", err)
	}

	// A nil schema degrades to the minimal continuation body plus a notice.
	body, notices, err = resolveReplicateAudioExtendInput(nil, AudioExtendRequest{
		Model: "meta/musicgen", Prompt: "continue", SourceAudio: "https://example.com/c.mp3",
	})
	if err != nil {
		t.Fatalf("nil schema: %v", err)
	}
	if body["continuation"] != true || body["input_audio"] != "https://example.com/c.mp3" {
		t.Fatalf("nil-schema body = %#v", body)
	}
	if len(notices) != 1 {
		t.Fatalf("nil schema should ride a notice, got %v", notices)
	}
}

// TestReplicateContinuationSchemaServes pins the picker filter: a model needs
// BOTH a scalar-string source-audio input and a scalar-boolean continuation
// flag to serve extend_audio.
func TestReplicateContinuationSchemaServes(t *testing.T) {
	if !replicateContinuationSchemaServes(parseReplicateFixtureSchema(t, replicateContinuationSchema)) {
		t.Fatal("the musicgen-style schema should serve continuations")
	}
	textToMusic := parseReplicateFixtureSchema(t, `{"components":{"schemas":{"Input":{"type":"object","properties":{
		"prompt":{"type":"string"},
		"duration":{"type":"integer"}
	}}}}}`)
	if replicateContinuationSchemaServes(textToMusic) {
		t.Fatal("a text-to-music schema without continuation inputs must not serve")
	}
	arrayAudio := parseReplicateFixtureSchema(t, `{"components":{"schemas":{"Input":{"type":"object","properties":{
		"input_audio":{"type":"array","items":{"type":"string"}},
		"continuation":{"type":"boolean"}
	}}}}}`)
	if replicateContinuationSchemaServes(arrayAudio) {
		t.Fatal("an array-shaped audio input is a chat wrapper the canonical request cannot fill")
	}
}

// TestAudioExtendFollowsAudioProvider pins extend_audio's replicate route:
// the gate checks the routed provider's key, the default resolver routes, the
// merge seeds the slot, the override lands on the routed slot, the builder
// stamps the activity, and the description carries the continuation facts.
func TestAudioExtendFollowsAudioProvider(t *testing.T) {
	keyring.MockInit()
	t.Cleanup(func() {
		_ = clearFalAPIKey()
		_ = clearReplicateAPIKey()
	})

	replicate := mergeAppConfig(defaultAppConfig())
	replicate.Models.AudioProvider = "replicate"
	replicate = mergeAppConfig(replicate)
	if got := replicate.Providers.Replicate.AudioExtendModel; got != defaultReplicateAudioExtendModel {
		t.Fatalf("replicate extend slot should seed, got %q", got)
	}
	if audioExtendConfigured(replicate) {
		t.Fatal("the replicate extend route needs the replicate key")
	}
	if err := saveReplicateAPIKey("replicate-test-key"); err != nil {
		t.Fatalf("saveReplicateAPIKey: %v", err)
	}
	if !audioExtendConfigured(replicate) {
		t.Fatal("the replicate extend route should serve with a key")
	}
	if got := resolveDefaultAudioExtendModel(replicate); got != defaultReplicateAudioExtendModel {
		t.Fatalf("replicate extend default = %q, want %q", got, defaultReplicateAudioExtendModel)
	}
	replicate.Providers.Replicate.AudioExtendModel = "acme/continuer"
	if got := resolveDefaultAudioExtendModel(replicate); got != "acme/continuer" {
		t.Fatalf("configured replicate extend slot should win, got %q", got)
	}

	// The override lands on the slot the effective provider reads.
	config := mergeAppConfig(defaultAppConfig())
	var err error
	config, _, err = applyConversationModelOverrides(config, ChatRequest{ModelOverrides: &ConversationModelOverrides{
		AudioProvider:    "replicate",
		AudioExtendModel: "acme/overridden",
	}})
	if err != nil {
		t.Fatalf("applyConversationModelOverrides: %v", err)
	}
	if got := config.Providers.Replicate.AudioExtendModel; got != "acme/overridden" {
		t.Fatalf("replicate-pinned extend model = %q, want it on the replicate slot", got)
	}
	if got := config.Providers.Fal.AudioExtendModel; got != "" {
		t.Fatalf("replicate-pinned extend model must not touch the fal slot, got %q", got)
	}

	// The builder stamps the activity and the description names the shape.
	const clip = "data:audio/mpeg;base64,QUJD"
	tools := HarnessToolExecutionContext{
		Config:         replicate,
		AttachedAudios: []string{clip},
		GenerateAudioExtend: func(ctx context.Context, req AudioExtendRequest) (GeneratedAudio, error) {
			return GeneratedAudio{Data: []byte("x"), MimeType: "audio/mpeg"}, nil
		},
	}
	def := extendAudioToolDefinition("replicate")
	if !strings.Contains(def.Description, "continues the clip FORWARD") {
		t.Fatalf("replicate extend description should name the forward-only shape: %q", def.Description)
	}
	out, _, err := def.Execute(context.Background(), tools, HarnessToolCall{Content: "more", Model: "acme/continuer"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	activity := def.Activity(HarnessToolResult{Name: "extend_audio", Status: "completed", Result: out})
	if activity.Provider != "replicate" {
		t.Fatalf("replicate extend activity provider = %q", activity.Provider)
	}
	if command := strings.Join(activity.Command, " "); command != "replicate extend-audio acme/continuer" {
		t.Fatalf("replicate extend activity command = %q", command)
	}
}
