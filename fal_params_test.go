package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func loadSchema(t *testing.T, name string) *ModelInputSchema {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "fal-schemas", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	s, err := parseModelInputSchema(raw)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestLoadFalOverridesMergesUserOverBuiltin(t *testing.T) {
	dir := t.TempDir()
	user := `{"audio":{"fal-ai/minimax/speech-02-hd":{"voice":""},"acme/tts":{"voice":"speaker_id"}}}`
	if err := os.WriteFile(filepath.Join(dir, "fal-overrides.json"), []byte(user), 0o644); err != nil {
		t.Fatal(err)
	}
	ov := loadFalOverrides(dir)
	if got, ok := ov.lookup("audio", "fal-ai/minimax/speech-02-hd", "voice"); !ok || got != "" {
		t.Fatalf("expected explicit-unsupported voice override, got %q ok=%v", got, ok)
	}
	if got, _ := ov.lookup("audio", "acme/tts", "voice"); got != "speaker_id" {
		t.Fatalf("expected user voice remap, got %q", got)
	}
}

func TestLoadFalOverridesMalformedIgnored(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "fal-overrides.json"), []byte("{ not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	ov := loadFalOverrides(dir) // must not panic; returns built-in defaults
	if ov.byCategory == nil {
		t.Fatal("expected non-nil overrides even on malformed file")
	}
}

func TestResolveSFXLoopAndDuration(t *testing.T) {
	body, notices := resolveAudioBody(loadSchema(t, "sfx-v2"),
		AudioGenerateRequest{Model: "fal-ai/elevenlabs/sound-effects/v2", Prompt: "rain", Duration: "10", Loop: true},
		builtinFalOverrides())
	if body["text"] != "rain" {
		t.Fatalf("expected text=rain, got %v", body["text"])
	}
	if body["duration_seconds"] != 10.0 {
		t.Fatalf("expected duration_seconds=10, got %v", body["duration_seconds"])
	}
	if body["loop"] != true {
		t.Fatalf("expected loop=true, got %v", body["loop"])
	}
	if len(notices) != 0 {
		t.Fatalf("expected no notices, got %v", notices)
	}
}

func TestResolveVoiceNestedMerge(t *testing.T) {
	body, notices := resolveAudioBody(loadSchema(t, "minimax-speech-02-hd"),
		AudioGenerateRequest{Model: "fal-ai/minimax/speech-02-hd", Prompt: "hello", Voice: "Grandma"},
		builtinFalOverrides())
	vs, ok := body["voice_setting"].(map[string]any)
	if !ok {
		t.Fatalf("expected voice_setting object, got %T", body["voice_setting"])
	}
	if vs["voice_id"] != "Grandma" {
		t.Fatalf("expected voice_id=Grandma, got %v", vs["voice_id"])
	}
	if vs["speed"] != 1.0 { // sibling default preserved by merge
		t.Fatalf("expected merged default speed=1, got %v", vs["speed"])
	}
	if len(notices) != 0 {
		t.Fatalf("unexpected notices: %v", notices)
	}
}

func TestResolveDropsUnsupportedLoop(t *testing.T) {
	_, notices := resolveAudioBody(loadSchema(t, "elevenlabs-tts-ml-v2"),
		AudioGenerateRequest{Model: "fal-ai/elevenlabs/tts/multilingual-v2", Prompt: "hi", Loop: true},
		builtinFalOverrides())
	if len(notices) != 1 || !strings.Contains(notices[0], "loop") {
		t.Fatalf("expected one loop-drop notice, got %v", notices)
	}
}

func TestResolveVoiceOnSFXDropped(t *testing.T) {
	_, notices := resolveAudioBody(loadSchema(t, "sfx-v2"),
		AudioGenerateRequest{Model: "sfx", Prompt: "wind", Voice: "Rachel"},
		builtinFalOverrides())
	if len(notices) != 1 || !strings.Contains(notices[0], "voice") {
		t.Fatalf("expected voice-drop notice, got %v", notices)
	}
}

func TestResolveVoiceUnsupportedViaOverride(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "fal-overrides.json"),
		[]byte(`{"audio":{"fal-ai/elevenlabs/tts/multilingual-v2":{"voice":""}}}`), 0o644)
	ov := loadFalOverrides(dir)
	_, notices := resolveAudioBody(loadSchema(t, "elevenlabs-tts-ml-v2"),
		AudioGenerateRequest{Model: "fal-ai/elevenlabs/tts/multilingual-v2", Prompt: "hi", Voice: "Rachel"}, ov)
	if len(notices) != 1 || !strings.Contains(notices[0], "voice") {
		t.Fatalf("expected voice dropped by override, got %v", notices)
	}
}

func TestResolveMusicLengthMsTransform(t *testing.T) {
	// Synthesize a schema whose only duration-ish field is music_length_ms.
	schema := &ModelInputSchema{
		Properties: map[string]SchemaProperty{
			"prompt":          {Name: "prompt", Kind: schemaScalar},
			"music_length_ms": {Name: "music_length_ms", Kind: schemaScalar},
		},
		order: []string{"prompt", "music_length_ms"},
	}
	body, notices := resolveAudioBody(schema,
		AudioGenerateRequest{Model: "fal-ai/elevenlabs/music", Prompt: "jazz", Duration: "10"},
		builtinFalOverrides())
	if body["music_length_ms"] != 10000.0 {
		t.Fatalf("expected 10000ms, got %v", body["music_length_ms"])
	}
	if len(notices) != 0 {
		t.Fatalf("unexpected notices: %v", notices)
	}
}

func TestResolveSchemaUnavailableGeneric(t *testing.T) {
	body, notices := resolveAudioBody(nil,
		AudioGenerateRequest{Model: "x", Prompt: "hi", Loop: true, Voice: "Rachel"},
		builtinFalOverrides())
	if body["prompt"] != "hi" || body["text"] != "hi" {
		t.Fatalf("expected generic prompt+text body, got %v", body)
	}
	if len(notices) != 1 || !strings.Contains(notices[0], "schema") {
		t.Fatalf("expected schema-unavailable notice, got %v", notices)
	}
}

// TestResolveAgainstRealSFXSchema resolves against the actual captured
// fal-ai/elevenlabs/sound-effects/v2 OpenAPI schema (committed fixture), which
// declares duration_seconds as anyOf[number,null] rather than a plain number —
// exercising the real shape the app fetches at runtime.
func TestResolveAgainstRealSFXSchema(t *testing.T) {
	body, notices := resolveAudioBody(loadSchema(t, "sfx-v2-real"),
		AudioGenerateRequest{Model: "fal-ai/elevenlabs/sound-effects/v2", Prompt: "soft wind moving desert sand", Duration: "12", Loop: true},
		builtinFalOverrides())
	if body["text"] != "soft wind moving desert sand" {
		t.Fatalf("expected text mapped, got %v", body["text"])
	}
	if body["loop"] != true {
		t.Fatalf("expected loop=true from real schema, got %v", body["loop"])
	}
	if body["duration_seconds"] != 12.0 {
		t.Fatalf("expected duration_seconds=12 from anyOf field, got %v", body["duration_seconds"])
	}
	if len(notices) != 0 {
		t.Fatalf("expected no notices for a fully-supported request, got %v", notices)
	}
}
