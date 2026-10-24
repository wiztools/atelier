package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
)

// Schemas shaped like Replicate's latest_version.openapi_schema for the
// speech-to-text collection's members, as published at implementation time:
// openai/whisper (audio URI, translate boolean, language enum, output-format
// enum), the gpt-4o-transcribe shape (audio_file, free-string language, no
// translate control), and a whisperx shape (audio_file, task enum, language
// string).
const replicateWhisperSchema = `{"components":{"schemas":{"Input":{"type":"object","properties":{
	"audio":{"type":"string","format":"uri","x-order":0},
	"transcription":{"type":"string","enum":["plain text","srt","vtt"],"default":"plain text","x-order":1},
	"translate":{"type":"boolean","default":false,"x-order":2},
	"language":{"type":"string","default":"auto","x-order":3,"enum":["auto","en","ja","fr","de"]}
}}}}}`

const replicateGPT4oTranscribeSchema = `{"components":{"schemas":{"Input":{"type":"object","properties":{
	"audio_file":{"type":"string","format":"uri"},
	"language":{"type":"string"},
	"prompt":{"type":"string"},
	"temperature":{"type":"number","default":0}
}}}}}`

const replicateWhisperXSchema = `{"components":{"schemas":{"Input":{"type":"object","properties":{
	"task":{"type":"string","enum":["transcribe","translate"],"default":"transcribe"},
	"audio_file":{"type":"string","format":"uri"},
	"language":{"type":"string"},
	"batch_size":{"type":"integer","default":64}
}}}}}`

const replicateSpeechLLMSchema = `{"components":{"schemas":{"Input":{"type":"object","properties":{
	"prompt":{"type":"string"},
	"messages":{"type":"array"},
	"audio":{"type":"array","items":{"type":"string","format":"uri"}},
	"temperature":{"type":"number"}
}}}}}`

func TestResolveReplicateTranscribeInputWhisper(t *testing.T) {
	schema := parseReplicateFixtureSchema(t, replicateWhisperSchema)
	req := TranscribeAudioRequest{
		Model:      "openai/whisper",
		Audio:      "data:audio/mpeg;base64,QUJD",
		Task:       "translate",
		Language:   "ja",
		Timestamps: "segments",
	}
	input, notices, err := resolveReplicateTranscribeInput(schema, req)
	if err != nil {
		t.Fatalf("resolveReplicateTranscribeInput: %v", err)
	}
	if len(notices) != 0 {
		t.Fatalf("expected no notices, got %v", notices)
	}
	if got := input["audio"]; got != req.Audio {
		t.Fatalf("audio = %v, want the clip", got)
	}
	// openai/whisper carries no task enum: translate maps onto the boolean.
	if got, ok := input["translate"].(bool); !ok || !got {
		t.Fatalf("translate = %v, want true", input["translate"])
	}
	if got := input["language"]; got != "ja" {
		t.Fatalf("language = %v, want the two-letter hint", got)
	}
	// A timestamped request asks the output-format enum for the vtt document.
	if got := input["transcription"]; got != "vtt" {
		t.Fatalf("transcription = %v, want %q", got, "vtt")
	}
}

func TestResolveReplicateTranscribeInputPlainRequestStaysPlain(t *testing.T) {
	schema := parseReplicateFixtureSchema(t, replicateWhisperSchema)
	input, _, err := resolveReplicateTranscribeInput(schema, TranscribeAudioRequest{
		Model: "openai/whisper",
		Audio: "data:audio/mpeg;base64,QUJD",
	})
	if err != nil {
		t.Fatalf("resolveReplicateTranscribeInput: %v", err)
	}
	if _, ok := input["transcription"]; ok {
		t.Fatalf("plain request must not pin the output format, got %v", input["transcription"])
	}
	if _, ok := input["translate"]; ok {
		t.Fatalf("transcribe task must not set translate, got %v", input["translate"])
	}
}

func TestResolveReplicateTranscribeInputTaskEnumAndUnknownLanguage(t *testing.T) {
	schema := parseReplicateFixtureSchema(t, replicateWhisperSchema)
	input, notices, err := resolveReplicateTranscribeInput(schema, TranscribeAudioRequest{
		Model:      "openai/whisper",
		Audio:      "data:audio/mpeg;base64,QUJD",
		Task:       "translate",
		Language:   "zz",
		Timestamps: "words",
	})
	if err != nil {
		t.Fatalf("resolveReplicateTranscribeInput: %v", err)
	}
	// openai/whisper carries no task enum: translate maps onto the boolean.
	if got, ok := input["translate"].(bool); !ok || !got {
		t.Fatalf("translate = %v, want true", input["translate"])
	}
	// A hint the enum doesn't list is dropped with a notice, never sent.
	if _, ok := input["language"]; ok {
		t.Fatalf("unknown language hint must be dropped, got %v", input["language"])
	}
	if len(notices) != 1 || !strings.Contains(notices[0], `does not list language "zz"`) {
		t.Fatalf("expected an unknown-language notice, got %v", notices)
	}
}

func TestResolveReplicateTranscribeInputDropsAndRequirements(t *testing.T) {
	t.Run("translate without a control degrades with a notice", func(t *testing.T) {
		schema := parseReplicateFixtureSchema(t, replicateGPT4oTranscribeSchema)
		input, notices, err := resolveReplicateTranscribeInput(schema, TranscribeAudioRequest{
			Model: "openai/gpt-4o-transcribe",
			Audio: "data:audio/mpeg;base64,QUJD",
			Task:  "translate",
		})
		if err != nil {
			t.Fatalf("resolveReplicateTranscribeInput: %v", err)
		}
		if _, ok := input["translate"]; ok {
			t.Fatalf("model without a translate control must not receive one, got %v", input)
		}
		if len(notices) != 1 || !strings.Contains(notices[0], "cannot translate") {
			t.Fatalf("expected a cannot-translate notice, got %v", notices)
		}
	})

	t.Run("audio input is required", func(t *testing.T) {
		schema := parseReplicateFixtureSchema(t, replicateSpeechLLMSchema)
		_, _, err := resolveReplicateTranscribeInput(schema, TranscribeAudioRequest{
			Model: "ibm-granite/granite-speech-4.1-2b",
			Audio: "data:audio/mpeg;base64,QUJD",
		})
		if err == nil || !strings.Contains(err.Error(), "no audio input") {
			t.Fatalf("expected a no-audio-input refusal, got %v", err)
		}
		if !strings.Contains(err.Error(), "fal.ai") {
			t.Fatalf("the refusal should name the provider-switch remedy, got %v", err)
		}
	})

	t.Run("missing clip fails before the schema walk", func(t *testing.T) {
		schema := parseReplicateFixtureSchema(t, replicateWhisperSchema)
		if _, _, err := resolveReplicateTranscribeInput(schema, TranscribeAudioRequest{Model: "openai/whisper"}); err == nil {
			t.Fatal("expected a missing-clip error")
		}
	})

	t.Run("nil schema sends the clip only", func(t *testing.T) {
		input, notices, err := resolveReplicateTranscribeInput(nil, TranscribeAudioRequest{
			Model: "openai/whisper",
			Audio: "data:audio/mpeg;base64,QUJD",
		})
		if err != nil {
			t.Fatalf("resolveReplicateTranscribeInput: %v", err)
		}
		if got := input["audio"]; got == "" {
			t.Fatalf("audio = %v, want the clip", got)
		}
		if len(input) != 1 {
			t.Fatalf("nil-schema body must carry the clip only, got %v", input)
		}
		if len(notices) != 1 || !strings.Contains(notices[0], "parameter schema") {
			t.Fatalf("expected a schema-unavailable notice, got %v", notices)
		}
	})
}

// whisperSegmentJSON is openai/whisper's output shape: transcript, optional
// English translation, and whisper segment objects.
const whisperSegmentJSON = `{"transcription":" Hello there","translation":" Hallo dort","segments":[
	{"start":0.0,"end":1.2,"text":" Hello"},
	{"start":1.2,"end":2.0,"text":" there"}
],"detected_language":"english"}`

// whisperXJSON is the whisperx shape: segments carrying nested word arrays.
const whisperXJSON = `{"language":"en","segments":[
	{"start":0.0,"end":1.2,"text":"Hello there","words":[
		{"start":0.0,"end":0.5,"word":"Hello","score":0.9},
		{"start":0.6,"end":1.2,"word":"there","score":0.8}
	]}
],"word_segments":[]}`

func TestReplicateTranscriptFromPrediction(t *testing.T) {
	mustPrediction := func(raw string) replicatePrediction {
		return replicatePrediction{Status: "succeeded", Output: []byte(raw)}
	}

	t.Run("plain request ignores volunteered segments", func(t *testing.T) {
		transcript, err := replicateTranscriptFromPrediction(mustPrediction(whisperSegmentJSON), TranscribeAudioRequest{
			Model: "openai/whisper",
		})
		if err != nil {
			t.Fatalf("replicateTranscriptFromPrediction: %v", err)
		}
		if transcript.Timestamps != "" || len(transcript.Chunks) != 0 {
			t.Fatalf("plain request must stay plain, got %q/%d chunks", transcript.Timestamps, len(transcript.Chunks))
		}
		if transcript.Text != "Hello there" {
			t.Fatalf("text = %q, want the trimmed transcript", transcript.Text)
		}
	})

	t.Run("segments request returns segment chunks", func(t *testing.T) {
		transcript, err := replicateTranscriptFromPrediction(mustPrediction(whisperSegmentJSON), TranscribeAudioRequest{
			Model:      "openai/whisper",
			Timestamps: "segments",
		})
		if err != nil {
			t.Fatalf("replicateTranscriptFromPrediction: %v", err)
		}
		if transcript.Timestamps != "segments" || len(transcript.Chunks) != 2 {
			t.Fatalf("timestamps = %q, chunks = %d, want 2 segments", transcript.Timestamps, len(transcript.Chunks))
		}
		if transcript.Chunks[0].Start != 0 || transcript.Chunks[0].End == nil || *transcript.Chunks[0].End != 1.2 {
			t.Fatalf("chunk 0 = %+v, want [0, 1.2]", transcript.Chunks[0])
		}
		if transcript.TimestampedText == "" {
			t.Fatal("timestamped rendering missing")
		}
	})

	t.Run("words request degrades to segments with a notice", func(t *testing.T) {
		transcript, err := replicateTranscriptFromPrediction(mustPrediction(whisperSegmentJSON), TranscribeAudioRequest{
			Model:      "openai/whisper",
			Timestamps: "words",
		})
		if err != nil {
			t.Fatalf("replicateTranscriptFromPrediction: %v", err)
		}
		if transcript.Timestamps != "segments" {
			t.Fatalf("timestamps = %q, want the segments degradation", transcript.Timestamps)
		}
		if len(transcript.Notices) != 1 || !strings.Contains(transcript.Notices[0], "segment-level timestamps only") {
			t.Fatalf("expected a degradation notice, got %v", transcript.Notices)
		}
	})

	t.Run("words request stays words when word chunks arrive", func(t *testing.T) {
		transcript, err := replicateTranscriptFromPrediction(mustPrediction(whisperXJSON), TranscribeAudioRequest{
			Model:      "victor-upmeet/whisperx",
			Timestamps: "words",
		})
		if err != nil {
			t.Fatalf("replicateTranscriptFromPrediction: %v", err)
		}
		if transcript.Timestamps != "words" || len(transcript.Chunks) != 2 {
			t.Fatalf("timestamps = %q with %d chunks, want 2 words", transcript.Timestamps, len(transcript.Chunks))
		}
		if transcript.Chunks[0].Text != "Hello" || transcript.Chunks[1].Text != "there" {
			t.Fatalf("chunks = %+v, want the flattened words", transcript.Chunks)
		}
	})

	t.Run("translate prefers the English field", func(t *testing.T) {
		transcript, err := replicateTranscriptFromPrediction(mustPrediction(whisperSegmentJSON), TranscribeAudioRequest{
			Model: "openai/whisper",
			Task:  "translate",
		})
		if err != nil {
			t.Fatalf("replicateTranscriptFromPrediction: %v", err)
		}
		if transcript.Text != "Hallo dort" {
			t.Fatalf("text = %q, want the translation", transcript.Text)
		}
	})

	t.Run("vtt document text parses into chunks", func(t *testing.T) {
		vtt := "WEBVTT\n\n00:00:00.000 --> 00:00:01.500\nFirst line\n\n00:00:01.500 --> 00:00:02.800\nSecond line\n"
		vttJSON, err := json.Marshal(vtt)
		if err != nil {
			t.Fatalf("marshal vtt: %v", err)
		}
		payload := `{"transcription":` + string(vttJSON) + `,"detected_language":"english"}`
		transcript, err := replicateTranscriptFromPrediction(mustPrediction(payload), TranscribeAudioRequest{
			Model:      "openai/whisper",
			Timestamps: "segments",
		})
		if err != nil {
			t.Fatalf("replicateTranscriptFromPrediction: %v", err)
		}
		if transcript.Timestamps != "segments" || len(transcript.Chunks) != 2 {
			t.Fatalf("timestamps = %q with %d chunks, want 2 parsed cues", transcript.Timestamps, len(transcript.Chunks))
		}
		if transcript.Text != "First line Second line" {
			t.Fatalf("text = %q, want the cue text joined", transcript.Text)
		}
	})

	t.Run("plain string output", func(t *testing.T) {
		transcript, err := replicateTranscriptFromPrediction(mustPrediction(`"just some words"`), TranscribeAudioRequest{
			Model:      "openai/gpt-4o-transcribe",
			Timestamps: "segments",
		})
		if err != nil {
			t.Fatalf("replicateTranscriptFromPrediction: %v", err)
		}
		if transcript.Text != "just some words" {
			t.Fatalf("text = %q", transcript.Text)
		}
		if transcript.Timestamps != "" || len(transcript.Chunks) != 0 {
			t.Fatalf("a chunkless result must report plain text, got %q/%d", transcript.Timestamps, len(transcript.Chunks))
		}
	})

	t.Run("empty output errors", func(t *testing.T) {
		if _, err := replicateTranscriptFromPrediction(mustPrediction(`{}`), TranscribeAudioRequest{Model: "openai/whisper"}); err == nil {
			t.Fatal("expected a no-text error")
		}
	})
}

func TestResolveTranscriptionProviderReplicate(t *testing.T) {
	keyring.MockInit()
	t.Cleanup(func() {
		_ = clearReplicateAPIKey()
		_ = clearFalAPIKey()
	})

	// Auto never resolves replicate: the historical fal-else-local chain stands.
	auto := defaultAppConfig()
	if got := resolveTranscriptionProvider(auto); got != transcriptionProviderFal {
		t.Fatalf("auto = %q, want fal", got)
	}

	// Pinned replicate routes there only with a token present.
	pinned := defaultAppConfig()
	pinned.Models.TranscriptionProvider = transcriptionProviderReplicate
	if got := resolveTranscriptionProvider(pinned); got != transcriptionProviderReplicate {
		t.Fatalf("pinned = %q, want replicate even without a token", got)
	}
	if transcribeAudioConfigured(pinned) {
		t.Fatal("replicate transcription must be unconfigured without a token")
	}
	if _, ok := defaultHarnessToolRegistry(context.Background(), pinned, nil).Get("transcribe_audio"); ok {
		t.Fatal("transcribe_audio must be absent when the pinned provider has no token")
	}

	if err := saveReplicateAPIKey("r8-test-token"); err != nil {
		t.Fatalf("saveReplicateAPIKey: %v", err)
	}
	if !transcribeAudioConfigured(pinned) {
		t.Fatal("replicate transcription should be configured with a token")
	}
	definition, ok := defaultHarnessToolRegistry(context.Background(), pinned, nil).Get("transcribe_audio")
	if !ok {
		t.Fatal("transcribe_audio should be registered for a pinned replicate provider")
	}
	if !strings.Contains(definition.Description, "Replicate speech-to-text model") {
		t.Fatalf("the planner description should name the replicate backend, got %q", definition.Description)
	}
	if got := resolveDefaultReplicateTranscribeModel(pinned); got != defaultReplicateTranscribeModel {
		t.Fatalf("default model = %q, want %q", got, defaultReplicateTranscribeModel)
	}
	pinned.Providers.Replicate.TranscribeModel = "openai/gpt-4o-transcribe"
	if got := resolveDefaultReplicateTranscribeModel(pinned); got != "openai/gpt-4o-transcribe" {
		t.Fatalf("configured override = %q, want it to win", got)
	}
}

func TestTranscriptionProviderReplicateOverrideOverlay(t *testing.T) {
	// The transcribe model override lands on the slot the effective
	// transcription provider reads — the ImageModel pair's contract.
	replicatePinned := defaultAppConfig()
	replicatePinned.Models.TranscriptionProvider = transcriptionProviderReplicate
	pinnedConfig, _, err := applyConversationModelOverrides(replicatePinned, ChatRequest{
		Model:          "primary",
		ModelOverrides: &ConversationModelOverrides{TranscribeModel: "openai/gpt-4o-transcribe"},
	})
	if err != nil {
		t.Fatalf("apply with overrides: %v", err)
	}
	if pinnedConfig.Providers.Replicate.TranscribeModel != "openai/gpt-4o-transcribe" {
		t.Fatalf("replicate slot = %q, want the override", pinnedConfig.Providers.Replicate.TranscribeModel)
	}

	falPinned := defaultAppConfig()
	falPinned.Models.TranscriptionProvider = transcriptionProviderFal
	falConfig, _, err := applyConversationModelOverrides(falPinned, ChatRequest{
		Model:          "primary",
		ModelOverrides: &ConversationModelOverrides{TranscribeModel: "fal-ai/other/whisper"},
	})
	if err != nil {
		t.Fatalf("apply with overrides: %v", err)
	}
	if falConfig.Providers.Fal.TranscribeModel != "fal-ai/other/whisper" {
		t.Fatalf("fal slot = %q, want the override", falConfig.Providers.Fal.TranscribeModel)
	}

	// A provider override rides the same overlay: the model lands on the
	// pinned provider's slot even when the global default reads fal.
	global := defaultAppConfig()
	global.Models.TranscriptionProvider = transcriptionProviderFal
	mixedConfig, _, err := applyConversationModelOverrides(global, ChatRequest{
		Model: "primary",
		ModelOverrides: &ConversationModelOverrides{
			TranscriptionProvider: transcriptionProviderReplicate,
			TranscribeModel:       "openai/whisper",
		},
	})
	if err != nil {
		t.Fatalf("apply with overrides: %v", err)
	}
	if mixedConfig.Providers.Replicate.TranscribeModel != "openai/whisper" {
		t.Fatalf("replicate slot = %q, want the override routed by the pinned provider", mixedConfig.Providers.Replicate.TranscribeModel)
	}
	if mixedConfig.Providers.Fal.TranscribeModel != "" {
		t.Fatalf("fal slot = %q, want untouched", mixedConfig.Providers.Fal.TranscribeModel)
	}
}

func TestTranscriptionProviderReplicateMergeNormalization(t *testing.T) {
	valid := defaultAppConfig()
	valid.Models.TranscriptionProvider = transcriptionProviderReplicate
	if got := mergeAppConfig(valid).Models.TranscriptionProvider; got != transcriptionProviderReplicate {
		t.Fatalf("merge normalized %q away, want replicate", got)
	}
	junk := defaultAppConfig()
	junk.Models.TranscriptionProvider = "together"
	if got := mergeAppConfig(junk).Models.TranscriptionProvider; got != "" {
		t.Fatalf("merge = %q, want junk normalized to empty", got)
	}
}

func TestReplicateTranscribeModelFilter(t *testing.T) {
	models := []ReplicateModel{
		{ID: "openai/whisper"},
		{ID: "openai/gpt-4o-transcribe"},
		{ID: "nvidia/parakeet-rnnt-1.1b"},
		{ID: "ibm-granite/granite-speech-4.1-2b"},
		{ID: "google/gemini-3-pro"},
	}
	kept := make([]string, 0, len(models))
	for _, model := range models {
		if isReplicateTranscribeModel(model) {
			kept = append(kept, model.ID)
		}
	}
	want := []string{"openai/whisper", "openai/gpt-4o-transcribe", "nvidia/parakeet-rnnt-1.1b"}
	if len(kept) != len(want) {
		t.Fatalf("kept %v, want %v", kept, want)
	}
	for i, id := range want {
		if kept[i] != id {
			t.Fatalf("kept[%d] = %q, want %q", i, kept[i], id)
		}
	}
}
