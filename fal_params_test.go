package main

import (
	"encoding/json"
	"fmt"
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
	if got, ok := ov.lookup("audio", "fal-ai/minimax/speech-02-hd", "voice"); !ok || got.Path != "" {
		t.Fatalf("expected explicit-unsupported voice override, got %+v ok=%v", got, ok)
	}
	if got, _ := ov.lookup("audio", "acme/tts", "voice"); got.Path != "speaker_id" {
		t.Fatalf("expected user voice remap, got %+v", got)
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

// TestLoadFalOverridesAcceptsObjectEntry verifies the object-valued override
// form: {"path","kind","items","maxItems"}. This is the shape that lets an
// override force array semantics for a model whose published schema disagrees
// with its runtime (the escape hatch added for conv_3232dd3836e50aa6402d8f51).
// The legacy bare-string form is covered by TestLoadFalOverridesMergesUserOverBuiltin
// above.
func TestLoadFalOverridesAcceptsObjectEntry(t *testing.T) {
	dir := t.TempDir()
	user := `{"video":{"acme/drift":{"sourceImage":{"path":"image_urls","kind":"array","items":"string","maxItems":9}}}}`
	if err := os.WriteFile(filepath.Join(dir, "fal-overrides.json"), []byte(user), 0o644); err != nil {
		t.Fatal(err)
	}
	ov := loadFalOverrides(dir)
	got, ok := ov.lookup("video", "acme/drift", "sourceImage")
	if !ok {
		t.Fatal("expected object override to be present")
	}
	if got.Path != "image_urls" {
		t.Fatalf("Path = %q, want \"image_urls\"", got.Path)
	}
	if got.Kind != "array" {
		t.Fatalf("Kind = %q, want \"array\"", got.Kind)
	}
	if got.Items != "string" {
		t.Fatalf("Items = %q, want \"string\"", got.Items)
	}
	if got.MaxItems != 9 {
		t.Fatalf("MaxItems = %d, want 9", got.MaxItems)
	}
}

// TestLoadFalOverridesBackwardCompatibleWithStringEntry pins the contract that
// an existing fal-overrides.json written in the legacy bare-string form (the
// only form before the object form was added) still parses unchanged into an
// overrideEntry whose Path is the string and Kind is empty (i.e. infer from
// schema). A user's deployed config must not break across this change.
func TestLoadFalOverridesBackwardCompatibleWithStringEntry(t *testing.T) {
	dir := t.TempDir()
	user := `{"audio":{"acme/tts":{"voice":"speaker_id"},"acme/skip":{"voice":""}}}`
	if err := os.WriteFile(filepath.Join(dir, "fal-overrides.json"), []byte(user), 0o644); err != nil {
		t.Fatal(err)
	}
	ov := loadFalOverrides(dir)
	remap, ok := ov.lookup("audio", "acme/tts", "voice")
	if !ok {
		t.Fatal("expected legacy string override to parse")
	}
	if remap.Path != "speaker_id" || remap.Kind != "" {
		t.Fatalf("legacy string override = %+v, want {Path:\"speaker_id\" Kind:\"\"}", remap)
	}
	disabled, ok := ov.lookup("audio", "acme/skip", "voice")
	if !ok || disabled.Path != "" {
		t.Fatalf("legacy empty-string disable = %+v, want {Path:\"\"}", disabled)
	}
}

// TestFindNativeOverrideForcesArrayKind is the unit test for the fabrication
// branch in findNative: when an override names a field NOT present in the
// schema but carries Kind:"array", the returned SchemaProperty must be
// schemaArray so the existing array coercion and multi-image guardrails apply
// unchanged. coerceImages is then exercised to prove the end-to-end shape.
func TestFindNativeOverrideForcesArrayKind(t *testing.T) {
	// Schema declares only a scalar image_url — no image_urls field at all.
	schema := &ModelInputSchema{
		Properties: map[string]SchemaProperty{
			"prompt":    {Name: "prompt", Kind: schemaScalar},
			"image_url": {Name: "image_url", Kind: schemaScalar, Type: "string"},
		},
		order: []string{"prompt", "image_url"},
	}
	ov := Overrides{byCategory: map[string]map[string]map[string]overrideEntry{
		"video": {
			"acme/drift": {
				"sourceImage": {Path: "image_urls", Kind: "array", Items: "string"},
			},
		},
	}}

	path, prop, ok := findNative(schema, ov, "video", "acme/drift", "sourceImage")
	if !ok {
		t.Fatal("expected findNative to honor the override")
	}
	if path != "image_urls" {
		t.Fatalf("path = %q, want \"image_urls\"", path)
	}
	if prop.Kind != schemaArray {
		t.Fatalf("prop.Kind = %v, want schemaArray (forced by override)", prop.Kind)
	}
	if prop.Items == nil || prop.Items.Type != "string" {
		t.Fatalf("prop.Items = %+v, want string element type", prop.Items)
	}
	// The downstream coercion already gates on Kind == schemaArray — prove the
	// fabricated property produces the one-element slice the runtime requires.
	got := coerceImages(prop, []string{"data:image/png;base64,ABC"})
	slice, ok := got.([]any)
	if !ok {
		t.Fatalf("coerceImages = %+v (%T), want []any", got, got)
	}
	if len(slice) != 1 || slice[0] != "data:image/png;base64,ABC" {
		t.Fatalf("slice = %+v, want single-element slice with the data URI", slice)
	}
}

// TestFindNativeOverrideScalarFabricationDefault confirms an override WITHOUT a
// Kind hint still fabricates a scalar property (the pre-override behavior). This
// guards against a regression where every fabricated override accidentally
// becomes an array.
func TestFindNativeOverrideScalarFabricationDefault(t *testing.T) {
	schema := &ModelInputSchema{
		Properties: map[string]SchemaProperty{
			"prompt": {Name: "prompt", Kind: schemaScalar},
		},
		order: []string{"prompt"},
	}
	ov := Overrides{byCategory: map[string]map[string]map[string]overrideEntry{
		"video": {"acme/custom": {"sourceImage": {Path: "image_url"}}},
	}}
	_, prop, ok := findNative(schema, ov, "video", "acme/custom", "sourceImage")
	if !ok {
		t.Fatal("expected findNative to honor the override")
	}
	if prop.Kind != schemaScalar {
		t.Fatalf("prop.Kind = %v, want schemaScalar when no Kind hint is given", prop.Kind)
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

// TestResolveImageBodyFlux verifies the image-to-image resolver maps a canonical
// request onto flux/dev/image-to-image's scalar image_url field. This is the
// classic image-to-image shape and the prior hardcoded behavior; the resolver
// must reproduce it exactly.
func TestResolveImageBodyFlux(t *testing.T) {
	body, notices, _ := resolveImageBody(loadSchema(t, "flux-dev-image-to-image"),
		ImageGenerateRequest{
			Model:  "fal-ai/flux/dev/image-to-image",
			Prompt: "cartoon character reference sheet",
			Steps:  24,
			Images: []string{"data:image/png;base64,ABC"},
		},
		builtinFalOverrides())
	if body["prompt"] != "cartoon character reference sheet" {
		t.Fatalf("prompt = %v, want the request prompt", body["prompt"])
	}
	// image_url is a SCALAR on flux → forwarded as a string, not wrapped.
	if got, ok := body["image_url"].(string); !ok || got != "data:image/png;base64,ABC" {
		t.Fatalf("image_url = %+v, want the source string", body["image_url"])
	}
	if _, present := body["image_urls"]; present {
		t.Fatalf("image_urls must not be set on flux; got %v", body["image_urls"])
	}
	if _, present := body["image_size"]; present {
		t.Fatalf("image_size must be omitted for image-to-image; got %v", body["image_size"])
	}
	if body["num_inference_steps"] != 24 {
		t.Fatalf("num_inference_steps = %v, want 24", body["num_inference_steps"])
	}
	if body["num_images"] != 1 {
		t.Fatalf("num_images = %v, want 1", body["num_images"])
	}
	if len(notices) != 0 {
		t.Fatalf("expected no notices for flux image-to-image, got %v", notices)
	}
}

// TestResolveImageBodyNanoBananaEdit verifies the resolver wraps a scalar source
// image into a slice when the model's schema declares the field as an array —
// fal-ai/nano-banana/edit's image_urls is `array of string`. This is the case
// that produced the 422 in the wild: sending image_url (scalar) to an endpoint
// that requires image_urls (array).
func TestResolveImageBodyNanoBananaEdit(t *testing.T) {
	body, notices, _ := resolveImageBody(loadSchema(t, "nano-banana-edit"),
		ImageGenerateRequest{
			Model:  "fal-ai/nano-banana/edit",
			Prompt: "cartoon character reference sheet",
			Images: []string{"data:image/png;base64,ABC"},
		},
		builtinFalOverrides())
	urls, ok := body["image_urls"].([]any)
	if !ok {
		t.Fatalf("image_urls = %+v (%T), want []any slice", body["image_urls"], body["image_urls"])
	}
	if len(urls) != 1 || urls[0] != "data:image/png;base64,ABC" {
		t.Fatalf("image_urls = %+v, want single-element slice with the source", urls)
	}
	if _, present := body["image_url"]; present {
		t.Fatalf("image_url must not be set on nano-banana/edit; got %v", body["image_url"])
	}
	if body["num_images"] != 1 {
		t.Fatalf("num_images = %v, want 1", body["num_images"])
	}
	if len(notices) != 0 {
		t.Fatalf("expected no notices for nano-banana/edit, got %v", notices)
	}
}

// TestResolveImageBodyNoSourceImageInput verifies the resolver degrades cleanly
// when a model has NO source-image field at all (e.g. fal-ai/nano-banana-pro,
// which is text-to-image only). Rather than fabricate an image_url/image_urls
// that fal will reject with a 422, the resolver emits a text-to-image body and
// surfaces a notice so the user understands their attachment was ignored.
func TestResolveImageBodyNoSourceImageInput(t *testing.T) {
	body, notices, _ := resolveImageBody(loadSchema(t, "nano-banana-pro"),
		ImageGenerateRequest{
			Model:  "fal-ai/nano-banana-pro",
			Prompt: "cartoon character reference sheet",
			Images: []string{"data:image/png;base64,ABC"},
		},
		builtinFalOverrides())
	if _, present := body["image_url"]; present {
		t.Fatalf("image_url must not be set when the model has no source-image field; got %v", body["image_url"])
	}
	if _, present := body["image_urls"]; present {
		t.Fatalf("image_urls must not be set when the model has no source-image field; got %v", body["image_urls"])
	}
	if body["prompt"] != "cartoon character reference sheet" {
		t.Fatalf("prompt = %v, want the request prompt", body["prompt"])
	}
	if body["num_images"] != 1 {
		t.Fatalf("num_images = %v, want 1", body["num_images"])
	}
	if len(notices) != 1 {
		t.Fatalf("expected one notice about the ignored attachment, got %v", notices)
	}
	if !strings.Contains(notices[0], "no source-image input") {
		t.Fatalf("notice = %q, want it to mention the missing source-image input", notices[0])
	}
}

// TestResolveImageBodyTextToImage verifies the text-to-image path: no source
// image attached → image_size is set from the configured dimensions and no
// source-image field appears in the body.
func TestResolveImageBodyTextToImage(t *testing.T) {
	body, notices, _ := resolveImageBody(loadSchema(t, "flux-dev-image-to-image"),
		ImageGenerateRequest{
			Model:  "fal-ai/flux/schnell",
			Prompt: "a lighthouse at dusk",
			Width:  768,
			Height: 768,
		},
		builtinFalOverrides())
	if _, present := body["image_url"]; present {
		t.Fatalf("image_url must not be set on text-to-image; got %v", body["image_url"])
	}
	size, ok := body["image_size"].(map[string]any)
	if !ok {
		t.Fatalf("image_size = %+v (%T), want {width,height} map", body["image_size"], body["image_size"])
	}
	if size["width"] != 768 || size["height"] != 768 {
		t.Fatalf("image_size = %+v, want 768x768", size)
	}
	if len(notices) != 0 {
		t.Fatalf("expected no notices for text-to-image, got %v", notices)
	}
}

// TestResolveImageBodySendsAspectPresetEnum is the fix for conv_b02dc16f:
// seedream ignores a {width,height} object below its minimum pixel area and
// falls back to a default landscape size, so a 9:16 request produced a landscape
// image. When the requested ratio maps to a fal preset, the enum string is sent
// instead — both on the schema path and the no-schema legacy path.
func TestResolveImageBodySendsAspectPresetEnum(t *testing.T) {
	t.Run("no schema sends preset string", func(t *testing.T) {
		body, _, _ := resolveImageBody(nil, ImageGenerateRequest{
			Model:  "bytedance/seedream/v5/pro/text-to-image",
			Prompt: "x", Width: 576, Height: 1024, AspectRatio: "9:16",
		}, builtinFalOverrides())
		if got, ok := body["image_size"].(string); !ok || got != "portrait_16_9" {
			t.Fatalf("image_size = %+v, want portrait_16_9 preset string", body["image_size"])
		}
	})
	t.Run("no schema with unmapped ratio sends pixels", func(t *testing.T) {
		body, _, _ := resolveImageBody(nil, ImageGenerateRequest{
			Model: "m", Prompt: "x", Width: 1000, Height: 500, AspectRatio: "21:9",
		}, builtinFalOverrides())
		size, ok := body["image_size"].(map[string]any)
		if !ok {
			t.Fatalf("image_size = %+v, want {width,height} for an unmapped ratio", body["image_size"])
		}
		if size["width"] != 1000 || size["height"] != 500 {
			t.Fatalf("image_size = %+v, want the pixel object", size)
		}
	})
	t.Run("schema with empty enum sends preset string", func(t *testing.T) {
		// seedream's anyOf leaves Enum empty in our parsed view; the preset is
		// still safe to send because there is no enum constraint to violate.
		schema := &ModelInputSchema{
			Properties: map[string]SchemaProperty{"image_size": {Name: "image_size", Kind: schemaScalar}},
			order:      []string{"image_size"},
		}
		body, _, _ := resolveImageBody(schema, ImageGenerateRequest{
			Model:  "bytedance/seedream/v5/pro/text-to-image",
			Prompt: "x", Width: 1024, Height: 576, AspectRatio: "16:9",
		}, builtinFalOverrides())
		if got := body["image_size"]; got != "landscape_16_9" {
			t.Fatalf("image_size = %+v, want landscape_16_9 preset string", got)
		}
	})
	t.Run("schema enum rejecting preset falls back to pixels", func(t *testing.T) {
		// A model whose image_size enum does NOT include the preset must fall
		// back to the pixel object rather than sending a value fal would reject.
		schema := &ModelInputSchema{
			Properties: map[string]SchemaProperty{"image_size": {Name: "image_size", Kind: schemaScalar, Enum: []string{"only_this_one"}}},
			order:      []string{"image_size"},
		}
		body, _, _ := resolveImageBody(schema, ImageGenerateRequest{
			Model: "m", Prompt: "x", Width: 1024, Height: 576, AspectRatio: "16:9",
		}, builtinFalOverrides())
		size, ok := body["image_size"].(map[string]any)
		if !ok {
			t.Fatalf("image_size = %+v, want pixel fallback when enum excludes the preset", body["image_size"])
		}
		if size["width"] != 1024 || size["height"] != 576 {
			t.Fatalf("image_size = %+v, want the requested pixel object", size)
		}
	})
	// conv_711ebd5f: an image-to-image edit (source image attached) with a
	// requested aspect ratio must still send the preset. Without this, fal
	// inherited the source's landscape orientation and a "9:16" request on a
	// landscape source produced a landscape output.
	t.Run("edit with source image sends preset", func(t *testing.T) {
		schema := &ModelInputSchema{
			Properties: map[string]SchemaProperty{
				"image_urls": {Name: "image_urls", Kind: schemaArray},
				"image_size": {Name: "image_size", Kind: schemaScalar},
			},
			order: []string{"image_urls", "image_size"},
		}
		body, _, _ := resolveImageBody(schema, ImageGenerateRequest{
			Model:  "bytedance/seedream/v5/pro/edit",
			Prompt: "zoom out, more sky", Width: 576, Height: 1024, AspectRatio: "9:16",
			Images: []string{"data:image/png;base64,iVBORw0KGgo="},
		}, builtinFalOverrides())
		if got := body["image_size"]; got != "portrait_16_9" {
			t.Fatalf("image_size = %+v, want portrait_16_9 preset on the edit path", got)
		}
		if _, ok := body["image_urls"]; !ok {
			t.Fatalf("image_urls must still be set alongside the preset: %+v", body)
		}
	})
	t.Run("edit model with no ratio input omits both", func(t *testing.T) {
		// A model that derives dims purely from the source (neither image_size
		// nor aspect_ratio) must not get either field — they would be unknown
		// fields. Only the preset is ever sent on image_size, and only the raw
		// ratio on aspect_ratio, and only when the schema declares the field.
		schema := &ModelInputSchema{
			Properties: map[string]SchemaProperty{
				"image_url": {Name: "image_url", Kind: schemaScalar},
			},
			order: []string{"image_url"},
		}
		body, _, _ := resolveImageBody(schema, ImageGenerateRequest{
			Model:  "some/source-only-edit",
			Prompt: "zoom out", Width: 576, Height: 1024, AspectRatio: "9:16",
			Images: []string{"data:image/png;base64,iVBORw0KGgo="},
		}, builtinFalOverrides())
		if _, present := body["image_size"]; present {
			t.Fatalf("image_size must be omitted when the model has no image_size input: %+v", body)
		}
		if _, present := body["aspect_ratio"]; present {
			t.Fatalf("aspect_ratio must be omitted when the model has no aspect_ratio input: %+v", body)
		}
	})
}

// TestResolveImageBodySendsAspectRatio is the regression for
// conv_369b3099eed8483b7b6a14bf: a "create a 9:16 image" request on
// fal-ai/nano-banana-2/edit (a sourced image edit) came back 1365x768 landscape.
// The planner emitted aspectRatio:"9:16" correctly, but the resolver could only
// forward the ratio through image_size — which that model doesn't have — so the
// ratio was dropped and fal fell back to its "auto" default, inheriting the
// source's landscape orientation. The model exposes aspect_ratio as a string
// enum (which lists "9:16"); the fix sends the raw ratio there. This is the
// image sibling of how the video resolver already handles aspect_ratio.
func TestResolveImageBodySendsAspectRatio(t *testing.T) {
	const barePNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="

	t.Run("nano-banana-2/edit sourced edit sends raw ratio", func(t *testing.T) {
		// The exact conv_369b3099 scenario: landscape source + explicit 9:16.
		body, _, err := resolveImageBody(loadSchema(t, "nano-banana-2-edit"),
			ImageGenerateRequest{
				Model:       "fal-ai/nano-banana-2/edit",
				Prompt:      "The statue from the given image, set in a sea environment.",
				AspectRatio: "9:16",
				Images:      []string{"data:image/png;base64," + barePNG},
			},
			builtinFalOverrides())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got := body["aspect_ratio"]; got != "9:16" {
			t.Fatalf("aspect_ratio = %+v, want the raw \"9:16\" string the model's enum accepts", body["aspect_ratio"])
		}
		// image_size must NOT be sent — the model has no such field, and the
		// preset string ("portrait_16_9") isn't in its aspect_ratio enum anyway.
		if _, present := body["image_size"]; present {
			t.Fatalf("image_size must be omitted on a model with aspect_ratio: %+v", body)
		}
		// The source frame still rides on the model's image_urls array input.
		urls, ok := body["image_urls"].([]any)
		if !ok || len(urls) != 1 {
			t.Fatalf("image_urls = %+v, want single-element slice alongside the ratio", body["image_urls"])
		}
	})

	t.Run("nano-banana/edit (plain string field) sends raw ratio", func(t *testing.T) {
		// The older nano-banana/edit fixture's aspect_ratio is a bare string
		// with no enum, so valueAllowedByEnum short-circuits true (no
		// constraint). The raw ratio is still placed.
		body, _, err := resolveImageBody(loadSchema(t, "nano-banana-edit"),
			ImageGenerateRequest{
				Model:       "fal-ai/nano-banana/edit",
				Prompt:      "transform this",
				AspectRatio: "9:16",
				Images:      []string{"data:image/png;base64," + barePNG},
			},
			builtinFalOverrides())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got := body["aspect_ratio"]; got != "9:16" {
			t.Fatalf("aspect_ratio = %+v, want \"9:16\" on the no-enum string field", body["aspect_ratio"])
		}
		if _, present := body["image_size"]; present {
			t.Fatalf("image_size must be omitted when aspect_ratio was placed: %+v", body)
		}
	})

	t.Run("text-to-image on aspect_ratio model sends raw ratio", func(t *testing.T) {
		// No Images → text-to-image path through sendImageSize, which must also
		// prefer aspect_ratio over image_size when the model has the former.
		body, _, err := resolveImageBody(loadSchema(t, "nano-banana-2-edit"),
			ImageGenerateRequest{
				Model:       "fal-ai/nano-banana-2/edit",
				Prompt:      "a tall portrait",
				AspectRatio: "16:9",
				Width:       1024,
				Height:      576,
			},
			builtinFalOverrides())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got := body["aspect_ratio"]; got != "16:9" {
			t.Fatalf("aspect_ratio = %+v, want \"16:9\" on the text-to-image path", body["aspect_ratio"])
		}
		if _, present := body["image_size"]; present {
			t.Fatalf("image_size must be omitted when aspect_ratio was placed: %+v", body)
		}
	})

	t.Run("ratio outside the enum is dropped, falls back to image_size preset", func(t *testing.T) {
		// A model that declares BOTH aspect_ratio (with an enum excluding the
		// requested ratio) and image_size (preset enum) must fall through to
		// the image_size preset when the ratio isn't enum-allowed — mirroring
		// the duration/resolution/fps discipline on the video path. Request 9:16
		// (excluded from this aspect_ratio enum) but mapped to the
		// portrait_16_9 preset that image_size's enum accepts.
		schema := &ModelInputSchema{
			Properties: map[string]SchemaProperty{
				"aspect_ratio": {Name: "aspect_ratio", Kind: schemaScalar, Enum: []string{"1:1", "16:9"}},
				"image_size":   {Name: "image_size", Kind: schemaScalar, Enum: []string{"square_hd", "landscape_16_9", "portrait_16_9"}},
			},
			order: []string{"aspect_ratio", "image_size"},
		}
		body, _, err := resolveImageBody(schema, ImageGenerateRequest{
			Model:       "hybrid/edit",
			Prompt:      "x",
			AspectRatio: "9:16", // not in aspect_ratio enum
			Width:       576,
			Height:      1024,
		}, builtinFalOverrides())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if _, present := body["aspect_ratio"]; present {
			t.Fatalf("aspect_ratio must be dropped when the ratio is outside its enum: %+v", body)
		}
		if got := body["image_size"]; got != "portrait_16_9" {
			t.Fatalf("image_size = %+v, want the portrait_16_9 preset fallback", got)
		}
	})
}

func TestFalImageSizePreset(t *testing.T) {
	cases := map[string]string{
		"1:1":  "square_hd",
		"16:9": "landscape_16_9",
		"9:16": "portrait_16_9",
		"4:3":  "landscape_4_3",
		"3:4":  "portrait_4_3",
		"21:9": "", // no preset; callers fall back to pixels
		"":     "",
	}
	for ratio, want := range cases {
		if got := falImageSizePreset(ratio); got != want {
			t.Errorf("falImageSizePreset(%q) = %q, want %q", ratio, got, want)
		}
	}
}

// TestResolveImageBodyNormalizesBareBase64 pins the regression from
// conv_e8ea99de04b547a516394be1: the resolver must wrap bare base64 (the shape
// AttachedImage arrives in — the frontend strips the data: prefix for Ollama)
// into a data URI before sending it to fal. fal rejects bare base64 with a 422.
// Both the scalar (image_url) and array (image_urls) paths must normalize.
func TestResolveImageBodyNormalizesBareBase64(t *testing.T) {
	// A real 1x1 PNG: base64 starts with iVBORw0KGgo, no data: prefix.
	const barePNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="

	t.Run("scalar image_url", func(t *testing.T) {
		body, _, _ := resolveImageBody(loadSchema(t, "flux-dev-image-to-image"),
			ImageGenerateRequest{
				Model:  "fal-ai/flux/dev/image-to-image",
				Prompt: "transform this",
				Images: []string{barePNG},
			},
			builtinFalOverrides())
		got, ok := body["image_url"].(string)
		if !ok {
			t.Fatalf("image_url = %+v (%T), want string", body["image_url"], body["image_url"])
		}
		if !strings.HasPrefix(got, "data:image/png;base64,") {
			t.Fatalf("image_url = %q, want a data:image/png;base64, URI (bare base64 is rejected by fal)", got[:min(40, len(got))])
		}
		if !strings.HasSuffix(got, barePNG) {
			t.Fatalf("image_url payload changed during normalization: %q", got)
		}
	})

	t.Run("array image_urls", func(t *testing.T) {
		body, _, _ := resolveImageBody(loadSchema(t, "nano-banana-edit"),
			ImageGenerateRequest{
				Model:  "fal-ai/nano-banana/edit",
				Prompt: "transform this",
				Images: []string{barePNG},
			},
			builtinFalOverrides())
		urls, ok := body["image_urls"].([]any)
		if !ok || len(urls) != 1 {
			t.Fatalf("image_urls = %+v, want single-element slice", body["image_urls"])
		}
		got, ok := urls[0].(string)
		if !ok {
			t.Fatalf("image_urls[0] = %+v (%T), want string", urls[0], urls[0])
		}
		if !strings.HasPrefix(got, "data:image/png;base64,") {
			t.Fatalf("image_urls[0] = %q, want a data:image/png;base64, URI", got[:min(40, len(got))])
		}
	})
}

// TestResolveImageBodyNoSchema verifies the nil-schema fallback: when the fal
// OpenAPI doc can't be fetched (offline, unknown endpoint), the resolver emits
// the legacy hardcoded body (prompt, num_images, image_url|image_size,
// num_inference_steps) and a single notice. This preserves today's behavior so a
// schema outage never breaks image generation outright.
func TestResolveImageBodyNoSchema(t *testing.T) {
	body, notices, _ := resolveImageBody(nil,
		ImageGenerateRequest{
			Model:  "fal-ai/flux/dev/image-to-image",
			Prompt: "an impressionist painting",
			Steps:  4,
			Images: []string{"data:image/png;base64,ABC"},
		},
		builtinFalOverrides())
	if body["prompt"] != "an impressionist painting" {
		t.Fatalf("prompt = %v", body["prompt"])
	}
	if body["image_url"] != "data:image/png;base64,ABC" {
		t.Fatalf("image_url = %v, want the source string (legacy fallback)", body["image_url"])
	}
	if body["num_inference_steps"] != 4 {
		t.Fatalf("num_inference_steps = %v, want 4", body["num_inference_steps"])
	}
	if body["num_images"] != 1 {
		t.Fatalf("num_images = %v, want 1", body["num_images"])
	}
	if len(notices) != 1 {
		t.Fatalf("expected one schema-unavailable notice, got %v", notices)
	}
	if !strings.Contains(notices[0], "Couldn't load") {
		t.Fatalf("notice = %q, want it to mention the unavailable schema", notices[0])
	}
}

// TestResolveVideoBodyVeoTextToVideo maps the canonical text-to-video params onto
// Veo 3.1's schema fields. duration and aspect_ratio are enums on Veo; the
// resolver passes the caller's string through unchanged.
func TestResolveVideoBodyVeoTextToVideo(t *testing.T) {
	audio := true
	body, notices, _ := resolveVideoBody(loadSchema(t, "veo3.1"),
		VideoGenerateRequest{
			Model:          "fal-ai/veo3.1",
			Prompt:         "a drone shot over a misty pine forest at sunrise",
			Duration:       "8s",
			AspectRatio:    "16:9",
			NegativePrompt: "blurry, text",
			GenerateAudio:  &audio,
		},
		builtinFalOverrides())
	if body["prompt"] != "a drone shot over a misty pine forest at sunrise" {
		t.Fatalf("prompt = %v", body["prompt"])
	}
	if body["duration"] != "8s" {
		t.Fatalf("duration = %v, want 8s", body["duration"])
	}
	if body["aspect_ratio"] != "16:9" {
		t.Fatalf("aspect_ratio = %v, want 16:9", body["aspect_ratio"])
	}
	if body["negative_prompt"] != "blurry, text" {
		t.Fatalf("negative_prompt = %v", body["negative_prompt"])
	}
	if body["generate_audio"] != true {
		t.Fatalf("generate_audio = %v, want true", body["generate_audio"])
	}
	// No source media attached — neither video_url nor image_url should appear.
	if _, present := body["video_url"]; present {
		t.Fatalf("video_url must not be set for text-to-video; got %v", body["video_url"])
	}
	if _, present := body["image_url"]; present {
		t.Fatalf("image_url must not be set for text-to-video; got %v", body["image_url"])
	}
	if len(notices) != 0 {
		t.Fatalf("expected no notices for Veo text-to-video, got %v", notices)
	}
}

// TestResolveVideoBodySilentRequestOnModelWithoutToggle reproduces the
// conv_e30f67cc834d4e98e1a49631 regression: a user asks for "no audio"
// (generateAudio:false) against a model that emits synchronized audio by
// default yet exposes no generate_audio input — alibaba/happy-horse is the
// real-world case. The resolver must not silently drop the flag (that lets
// audio through after the user explicitly declined it); it emits a notice so
// the failure is visible, and leaves generate_audio out of the body since the
// endpoint has no such field to set.
func TestResolveVideoBodySilentRequestOnModelWithoutToggle(t *testing.T) {
	silent := false
	body, notices, _ := resolveVideoBody(loadSchema(t, "happy-horse-image-to-video"),
		VideoGenerateRequest{
			Model:         "alibaba/happy-horse/v1.1/image-to-video",
			Prompt:        "a parent stands still while a child walks toward the light",
			GenerateAudio: &silent,
			Image:         "data:image/png;base64,ABC",
		},
		builtinFalOverrides())
	if _, present := body["generate_audio"]; present {
		t.Fatalf("generate_audio must not be set; the model has no such field. got %v", body["generate_audio"])
	}
	if len(notices) != 1 {
		t.Fatalf("expected one notice (silent request cannot be honored), got %v", notices)
	}
	if !strings.Contains(notices[0], "audio by default") || !strings.Contains(notices[0], "cannot be honored") {
		t.Fatalf("notice = %q, want it to say audio is on by default and the silent request cannot be honored", notices[0])
	}
}

// A nil GenerateAudio (let the model default) must stay notice-free even when
// the model has no generate_audio toggle: there is no user request being
// violated, so the default-audio behavior is not a failure to surface.
func TestResolveVideoBodyDefaultAudioOnModelWithoutToggleNoNotice(t *testing.T) {
	body, notices, _ := resolveVideoBody(loadSchema(t, "happy-horse-image-to-video"),
		VideoGenerateRequest{
			Model:  "alibaba/happy-horse/v1.1/image-to-video",
			Prompt: "a calm lake at dawn",
			Image:  "data:image/png;base64,ABC",
		},
		builtinFalOverrides())
	if _, present := body["generate_audio"]; present {
		t.Fatalf("generate_audio must not be set; the model has no such field. got %v", body["generate_audio"])
	}
	if len(notices) != 0 {
		t.Fatalf("expected no notices for an unspecified audio flag, got %v", notices)
	}
}

// TestResolveVideoBodyVeoExtend verifies the extend path: an attached video maps
// onto the model's video_url field, and an attached image is ignored in favor of
// the video (extend takes precedence over image-to-video).
func TestResolveVideoBodyVeoExtend(t *testing.T) {
	body, notices, _ := resolveVideoBody(loadSchema(t, "veo3.1-extend-video"),
		VideoGenerateRequest{
			Model:  "fal-ai/veo3.1/extend-video",
			Prompt: "the camera continues panning across the valley",
			Video:  "data:video/mp4;base64,AAA",
			Image:  "data:image/png;base64,BBB",
		},
		builtinFalOverrides())
	if body["prompt"] != "the camera continues panning across the valley" {
		t.Fatalf("prompt = %v", body["prompt"])
	}
	if got, ok := body["video_url"].(string); !ok || !strings.HasPrefix(got, "data:video/") {
		t.Fatalf("video_url = %v, want the attached video data URI", body["video_url"])
	}
	// image_url must NOT appear: extend wins over image-to-video when both are set.
	if _, present := body["image_url"]; present {
		t.Fatalf("image_url must not be set when extending a video; got %v", body["image_url"])
	}
	if len(notices) != 0 {
		t.Fatalf("expected no notices for Veo extend, got %v", notices)
	}
}

// extendVideoNoAspectSchema is a synthesized schema with a scalar video_url
// field and no aspect_ratio input — the shape of an extend endpoint that
// derives the output ratio from the source clip (e.g.
// xai/grok-imagine-video/extend-video). Built inline because no captured
// OpenAPI doc for such a model exists in the testdata tree yet.
func extendVideoNoAspectSchema() *ModelInputSchema {
	return &ModelInputSchema{
		Properties: map[string]SchemaProperty{
			"prompt":    {Name: "prompt", Kind: schemaScalar},
			"video_url": {Name: "video_url", Kind: schemaScalar},
		},
		order: []string{"prompt", "video_url"},
	}
}

// extendVideoWithDurationSchema is a synthesized schema with a scalar video_url
// and an integer duration field — the shape of grok-imagine-video/extend-video,
// whose duration is documented as "length of the extension in seconds"
// (default 6, range 2-10). Built inline because no captured OpenAPI doc for the
// grok extend endpoint exists in the testdata tree yet.
func extendVideoWithDurationSchema() *ModelInputSchema {
	return &ModelInputSchema{
		Properties: map[string]SchemaProperty{
			"prompt":    {Name: "prompt", Kind: schemaScalar},
			"video_url": {Name: "video_url", Kind: schemaScalar},
			"duration":  {Name: "duration", Kind: schemaScalar, Type: "integer"},
		},
		order: []string{"prompt", "video_url", "duration"},
	}
}

// TestResolveVideoBodyExtendDurationSentNoSemanticsNotice reproduces
// conv_484449cf8fe4a13c1ffa6bb4: the planner had no duration field for video, so
// "extend another 5s" sent no duration and fal applied its default. Now the
// video tool exposes duration, and against an extend model with an integer-
// typed duration input the value is forwarded as an integer. The extension-
// length semantics (duration = length appended, not total clip) are documented
// in the param schema rather than surfaced as a runtime notice, so a successful
// send must produce NO notice.
func TestResolveVideoBodyExtendDurationSentNoSemanticsNotice(t *testing.T) {
	body, notices, _ := resolveVideoBody(extendVideoWithDurationSchema(),
		VideoGenerateRequest{
			Model:    "xai/grok-imagine-video/extend-video",
			Prompt:   "continue the flight over the canyon",
			Video:    "data:video/mp4;base64,AAA",
			Duration: "5",
		},
		builtinFalOverrides())
	// duration is sent as an integer (the schema types it integer), not "5".
	if got, ok := body["duration"]; !ok {
		t.Fatalf("duration must be sent; the model has a duration input. got body %v", body)
	} else if got != 5 {
		t.Fatalf("duration = %v (%T), want integer 5 (schema types it integer)", got, got)
	}
	if got, ok := body["video_url"].(string); !ok || !strings.HasPrefix(got, "data:video/") {
		t.Fatalf("video_url = %v, want the attached video data URI", body["video_url"])
	}
	if len(notices) != 0 {
		t.Fatalf("a successful duration send must not emit a notice; semantics live in the param description. got %v", notices)
	}
}

// TestResolveVideoBodyExtendDurationOutOfEnumDropped verifies an out-of-enum
// duration on an extend is dropped with only the duration-dropped notice (no
// extension-length notice piles on). Uses an enum-constrained duration that
// rejects "5".
func TestResolveVideoBodyExtendDurationOutOfEnumDropped(t *testing.T) {
	schema := &ModelInputSchema{
		Properties: map[string]SchemaProperty{
			"prompt":    {Name: "prompt", Kind: schemaScalar},
			"video_url": {Name: "video_url", Kind: schemaScalar},
			"duration":  {Name: "duration", Kind: schemaScalar, Enum: []string{"6", "8", "10"}},
		},
		order: []string{"prompt", "video_url", "duration"},
	}
	body, notices, _ := resolveVideoBody(schema,
		VideoGenerateRequest{
			Model:    "xai/grok-imagine-video/extend-video",
			Prompt:   "continue the flight",
			Video:    "data:video/mp4;base64,AAA",
			Duration: "5", // not in the enum
		},
		builtinFalOverrides())
	if _, present := body["duration"]; present {
		t.Fatalf("duration must be dropped (out of enum), got %v", body["duration"])
	}
	if len(notices) != 1 {
		t.Fatalf("expected only the duration-dropped notice, got %v", notices)
	}
	if !strings.Contains(notices[0], "does not accept duration") {
		t.Fatalf("notice = %q, want the duration-dropped notice", notices[0])
	}
}

// TestResolveVideoBodyExtendExplicitRatioNoToggle reproduces
// conv_484449cf8fe4a13c1ffa6bb4: an explicit aspect ratio on an extend model
// that has no aspect_ratio input (xai/grok-imagine-video/extend-video). The
// model derives orientation from the source clip, so the text-to-video notice
// ("has no aspect-ratio control; ignoring the requested aspect ratio") was
// false — extend inherits the source video's ratio, exactly like image-to-
// video inherits the source frame. The resolver now says honestly that the
// ratio is only honored if the source video already matches, and that the
// video was not reshaped. aspect_ratio must not appear in the body (the model
// has no such field) and video_url is still sent.
func TestResolveVideoBodyExtendExplicitRatioNoToggle(t *testing.T) {
	body, notices, _ := resolveVideoBody(extendVideoNoAspectSchema(),
		VideoGenerateRequest{
			Model:               "xai/grok-imagine-video/extend-video",
			Prompt:              "continue the flight over the canyon",
			Video:               "data:video/mp4;base64,AAA",
			AspectRatio:         "9:16",
			AspectRatioExplicit: true,
		},
		builtinFalOverrides())
	if _, present := body["aspect_ratio"]; present {
		t.Fatalf("aspect_ratio must not be set; the model has no such field. got %v", body["aspect_ratio"])
	}
	if got, ok := body["video_url"].(string); !ok || !strings.HasPrefix(got, "data:video/") {
		t.Fatalf("video_url = %v, want the attached video data URI", body["video_url"])
	}
	if len(notices) != 1 {
		t.Fatalf("expected one notice (explicit ratio not honored via the source clip), got %v", notices)
	}
	if strings.Contains(notices[0], "no aspect-ratio control") {
		t.Fatalf("notice must not claim 'no aspect-ratio control' for an extend model. got %q", notices[0])
	}
	if !strings.Contains(notices[0], "derives the output aspect ratio from the source video") ||
		!strings.Contains(notices[0], "did not reshape the video") ||
		!strings.Contains(notices[0], "9:16") {
		t.Fatalf("notice = %q, want it to say the model derives ratio from the source video, mention 9:16, and that the video was not reshaped", notices[0])
	}
}

// TestResolveVideoBodyExtendDropsDefaultRatio is the extend counterpart to
// TestResolveVideoBodyImageToVideoDropsDefaultRatio: an attached video
// without an explicitly requested ratio must NOT send aspect_ratio, so the
// model inherits the source clip's orientation instead of getting a config
// default stamped onto it. Verifies the gate treats sourceVideo the same as
// sourceImages.
func TestResolveVideoBodyExtendDropsDefaultRatio(t *testing.T) {
	schema := &ModelInputSchema{
		Properties: map[string]SchemaProperty{
			"prompt":       {Name: "prompt", Kind: schemaScalar},
			"video_url":    {Name: "video_url", Kind: schemaScalar},
			"aspect_ratio": {Name: "aspect_ratio", Kind: schemaScalar},
		},
		order: []string{"prompt", "video_url", "aspect_ratio"},
	}
	body, notices, _ := resolveVideoBody(schema,
		VideoGenerateRequest{
			Model:       "fal-ai/veo3.1/extend-video",
			Prompt:      "extend the shot",
			Video:       "data:video/mp4;base64,AAA",
			AspectRatio: "16:9", // AspectRatioExplicit is false → config default
		},
		builtinFalOverrides())
	if _, present := body["aspect_ratio"]; present {
		t.Fatalf("aspect_ratio must not be sent for an extend with an inherited ratio; got %v", body["aspect_ratio"])
	}
	if got, ok := body["video_url"].(string); !ok || !strings.HasPrefix(got, "data:video/") {
		t.Fatalf("video_url = %v, want the attached video data URI", body["video_url"])
	}
	if len(notices) != 0 {
		t.Fatalf("expected no notices for extend with default ratio, got %v", notices)
	}
}

// TestResolveVideoBodyKlingImageToVideo verifies the builtin override that
// compensates for fal's schema/runtime drift on Kling image-to-video: the
// published schema declares a scalar image_url, but the runtime rejects it with
// 422 demanding image_urls (a list). The builtin override forces the source
// image onto image_urls as a one-element slice, so an image-to-video turn
// succeeds instead of 422ing. See conv_3232dd3836e50aa6402d8f51.
func TestResolveVideoBodyKlingImageToVideo(t *testing.T) {
	body, notices, _ := resolveVideoBody(loadSchema(t, "kling-image-to-video"),
		VideoGenerateRequest{
			Model:  "fal-ai/kling-video/v2/master/image-to-video",
			Prompt: "make the character walk forward",
			Image:  "data:image/png;base64,ABC",
		},
		builtinFalOverrides())
	if body["prompt"] != "make the character walk forward" {
		t.Fatalf("prompt = %v", body["prompt"])
	}
	urls, ok := body["image_urls"].([]any)
	if !ok {
		t.Fatalf("image_urls = %+v (%T), want a one-element []any slice (runtime requires a list)", body["image_urls"], body["image_urls"])
	}
	if len(urls) != 1 || !strings.HasPrefix(fmt.Sprint(urls[0]), "data:image/") {
		t.Fatalf("image_urls = %+v, want single-element slice with the attached image data URI", urls)
	}
	if _, present := body["image_url"]; present {
		t.Fatalf("image_url must not be set; the override routes the source onto image_urls. got %v", body["image_url"])
	}
	if _, present := body["video_url"]; present {
		t.Fatalf("video_url must not be set for image-to-video; got %v", body["video_url"])
	}
	if len(notices) != 0 {
		t.Fatalf("expected no notices for Kling image-to-video, got %v", notices)
	}
}

// TestResolveVideoBodyKlingO3ProImageUrlsArray is the end-to-end regression for
// conv_9bbf4d6894859debe3430fdb: the Kling o3/pro reference-to-video model
// declares image_urls as anyOf:[{type:array, items:{...}}, {type:null}], which
// the schema parser used to read as an empty scalar — so resolveVideoBody
// concluded "has no source-image input" and silently dropped the user's image,
// producing a text-only video. Now that the parser unwraps the nullable array
// union, image_urls is schemaArray and the source lands as a one-element list.
// Skips when the live schema cache is absent (CI without a fal key).
func TestResolveVideoBodyKlingO3ProImageUrlsArray(t *testing.T) {
	path := filepath.Join(os.Getenv("HOME"), ".atelier", "schema-cache", "fal-ai_kling-video_o3_pro_reference-to-video.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("live schema cache not present at %s; skipping end-to-end resolver test", path)
	}
	var cached struct {
		Raw json.RawMessage `json:"raw"`
	}
	if err := json.Unmarshal(raw, &cached); err != nil {
		t.Fatalf("decode cache wrapper: %v", err)
	}
	schema, err := parseModelInputSchema(cached.Raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	body, notices, err := resolveVideoBody(schema,
		VideoGenerateRequest{
			Model:  "fal-ai/kling-video/o3/pro/reference-to-video",
			Prompt: "A statue standing still as gentle waves hit it.",
			Images: []string{"data:image/png;base64,iVBORw0KGgo="},
		},
		builtinFalOverrides())
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	urls, ok := body["image_urls"].([]any)
	if !ok {
		t.Fatalf("image_urls = %+v (%T), want a one-element []any list (the runtime requires a list)", body["image_urls"], body["image_urls"])
	}
	if len(urls) != 1 || !strings.HasPrefix(fmt.Sprint(urls[0]), "data:image/") {
		t.Fatalf("image_urls = %+v, want single-element slice with the attached image data URI", urls)
	}
	if _, present := body["image_url"]; present {
		t.Fatalf("image_url (scalar) must not be set; the source routes onto image_urls. got %v", body["image_url"])
	}
	for _, n := range notices {
		if strings.Contains(n, "no source-image input") {
			t.Fatalf("must not report 'no source-image input' now that image_urls parses as an array; got notice %q", n)
		}
	}
}

// TestResolveVideoBodyImageToVideoExplicitRatioNoToggle reproduces
// conv_e30f67cc834d4e98e1a49631: an explicit aspect ratio on an image-to-video
// model that has no aspect_ratio input (alibaba/happy-horse). The model derives
// orientation from the source frame, so the old notice ("has no aspect-ratio
// control; ignoring the requested aspect ratio") was false on both counts. The
// resolver now says honestly that the ratio is only honored if the source image
// already matches, and that the image was not reshaped. aspect_ratio must not
// appear in the body (the model has no such field) and image_url is still sent.
func TestResolveVideoBodyImageToVideoExplicitRatioNoToggle(t *testing.T) {
	body, notices, _ := resolveVideoBody(loadSchema(t, "happy-horse-image-to-video"),
		VideoGenerateRequest{
			Model:               "alibaba/happy-horse/v1.1/image-to-video",
			Prompt:              "a parent stands still while a child walks toward the light",
			Image:               "data:image/png;base64,ABC",
			AspectRatio:         "9:16",
			AspectRatioExplicit: true,
		},
		builtinFalOverrides())
	if _, present := body["aspect_ratio"]; present {
		t.Fatalf("aspect_ratio must not be set; the model has no such field. got %v", body["aspect_ratio"])
	}
	if got, ok := body["image_url"].(string); !ok || !strings.HasPrefix(got, "data:image/") {
		t.Fatalf("image_url = %v, want the attached image data URI", body["image_url"])
	}
	if len(notices) != 1 {
		t.Fatalf("expected one notice (explicit ratio not honored via the frame), got %v", notices)
	}
	if strings.Contains(notices[0], "no aspect-ratio control") {
		t.Fatalf("notice must not claim 'no aspect-ratio control' for an image-to-video model. got %q", notices[0])
	}
	if !strings.Contains(notices[0], "derives the output aspect ratio from the source image") ||
		!strings.Contains(notices[0], "did not reshape the image") ||
		!strings.Contains(notices[0], "9:16") {
		t.Fatalf("notice = %q, want it to say the model derives ratio from the source image, mention 9:16, and that the image was not reshaped", notices[0])
	}
}

// TestResolveVideoBodyTextToVideoNoAspectRatioKeepsOldNotice confirms the
// text-to-video path still uses the accurate "no aspect-ratio control" notice
// when there is no source image to inherit from — there the ratio genuinely
// can't be carried, unlike the image-to-video case above.
func TestResolveVideoBodyTextToVideoNoAspectRatioKeepsOldNotice(t *testing.T) {
	body, notices, _ := resolveVideoBody(loadSchema(t, "happy-horse-image-to-video"),
		VideoGenerateRequest{
			Model:       "alibaba/happy-horse/v1.1/image-to-video",
			Prompt:      "a calm ocean at dawn",
			AspectRatio: "16:9", // no Image attached → text-to-video path
		},
		builtinFalOverrides())
	if _, present := body["aspect_ratio"]; present {
		t.Fatalf("aspect_ratio must not be set; the model has no such field. got %v", body["aspect_ratio"])
	}
	if len(notices) != 1 {
		t.Fatalf("expected one notice, got %v", notices)
	}
	if !strings.Contains(notices[0], "no aspect-ratio control") {
		t.Fatalf("text-to-video notice should still say 'no aspect-ratio control'. got %q", notices[0])
	}
}

// imageVideoWithAspectSchema is like scalarImageVideoSchema but also declares an
// aspect_ratio field, so the image-to-video aspect-ratio gate can be exercised
// against the schema-driven branch (findNative "video" "aspectRatio").
func imageVideoWithAspectSchema() *ModelInputSchema {
	return &ModelInputSchema{
		Properties: map[string]SchemaProperty{
			"prompt":       {Name: "prompt", Kind: schemaScalar},
			"image_url":    {Name: "image_url", Kind: schemaScalar},
			"aspect_ratio": {Name: "aspect_ratio", Kind: schemaScalar},
		},
		order: []string{"prompt", "image_url", "aspect_ratio"},
	}
}

// TestResolveVideoBodyImageToVideoDropsDefaultRatio verifies the fix for
// conv_26cc3f515d6d645b316763cb: an attached image without an explicitly
// requested ratio must NOT send aspect_ratio, so the model inherits the source
// frame's orientation instead of getting a config default (e.g. "16:9") stamped
// onto a portrait image. Both the schema-driven and legacy nil-schema paths
// follow the same gate.
func TestResolveVideoBodyImageToVideoDropsDefaultRatio(t *testing.T) {
	t.Run("schema-driven", func(t *testing.T) {
		body, _, _ := resolveVideoBody(imageVideoWithAspectSchema(),
			VideoGenerateRequest{
				Model:       "fal-ai/kling-video/v2/master/image-to-video",
				Prompt:      "animate",
				Image:       "data:image/png;base64,ABC",
				AspectRatio: "16:9", // config-filled, NOT explicit
			},
			builtinFalOverrides())
		if _, present := body["aspect_ratio"]; present {
			t.Fatalf("aspect_ratio must be dropped for image-to-video without an explicit ratio; got %v", body["aspect_ratio"])
		}
		// The Kling model id triggers the builtin image_urls override (the schema
		// declares scalar image_url but the runtime demands a list), so the source
		// lands on image_urls, not image_url. The aspect-ratio gate under test is
		// independent of which field carries the image.
		if _, present := body["image_urls"]; !present {
			t.Fatalf("image_urls must still be sent for image-to-video (builtin override routes the source onto the list field)")
		}
	})
	t.Run("legacy nil-schema", func(t *testing.T) {
		body, _, _ := resolveVideoBody(nil,
			VideoGenerateRequest{
				Model:       "fal-ai/kling-video/v2/master/image-to-video",
				Prompt:      "animate",
				Image:       "data:image/png;base64,ABC",
				AspectRatio: "16:9", // config-filled, NOT explicit
			},
			builtinFalOverrides())
		if _, present := body["aspect_ratio"]; present {
			t.Fatalf("aspect_ratio must be dropped for legacy image-to-video without an explicit ratio; got %v", body["aspect_ratio"])
		}
	})
}

// TestResolveVideoBodyImageToVideoExplicitRatio verifies that an explicit
// (planner-set) aspect ratio overrides the source frame and IS sent for
// image-to-video. This is the "make this 16:9 even though the image is 9:16"
// case: the user asked, so we honor it.
func TestResolveVideoBodyImageToVideoExplicitRatio(t *testing.T) {
	t.Run("schema-driven", func(t *testing.T) {
		body, _, _ := resolveVideoBody(imageVideoWithAspectSchema(),
			VideoGenerateRequest{
				Model:               "fal-ai/kling-video/v2/master/image-to-video",
				Prompt:              "animate",
				Image:               "data:image/png;base64,ABC",
				AspectRatio:         "16:9",
				AspectRatioExplicit: true,
			},
			builtinFalOverrides())
		if body["aspect_ratio"] != "16:9" {
			t.Fatalf("explicit aspect_ratio must be sent for image-to-video; got %v", body["aspect_ratio"])
		}
	})
	t.Run("legacy nil-schema", func(t *testing.T) {
		body, _, _ := resolveVideoBody(nil,
			VideoGenerateRequest{
				Model:               "fal-ai/kling-video/v2/master/image-to-video",
				Prompt:              "animate",
				Image:               "data:image/png;base64,ABC",
				AspectRatio:         "16:9",
				AspectRatioExplicit: true,
			},
			builtinFalOverrides())
		if body["aspect_ratio"] != "16:9" {
			t.Fatalf("explicit aspect_ratio must be sent for legacy image-to-video; got %v", body["aspect_ratio"])
		}
	})
}

// TestResolveVideoBodyNoSchema verifies the nil-schema legacy fallback reproduces
// the body GenerateVideo used to build itself, plus a schema-unavailable notice.
func TestResolveVideoBodyNoSchema(t *testing.T) {
	silent := false
	body, notices, _ := resolveVideoBody(nil,
		VideoGenerateRequest{
			Model:          "fal-ai/kling-video/v2/master/text-to-video",
			Prompt:         "a calm ocean at dawn",
			Duration:       "5",
			AspectRatio:    "16:9",
			NegativePrompt: "text",
			GenerateAudio:  &silent,
			Image:          "data:image/png;base64,ABC",
		},
		builtinFalOverrides())
	if body["prompt"] != "a calm ocean at dawn" {
		t.Fatalf("prompt = %v", body["prompt"])
	}
	if body["duration"] != "5" {
		t.Fatalf("duration = %v, want 5 (legacy fallback)", body["duration"])
	}
	// This is image-to-video (an Image is attached) without an explicit ratio,
	// so aspect_ratio is dropped and the model inherits the frame's orientation
	// — the legacy fallback follows the same gate as the schema-driven branch.
	if _, present := body["aspect_ratio"]; present {
		t.Fatalf("aspect_ratio must be dropped for image-to-video without an explicit ratio (legacy fallback); got %v", body["aspect_ratio"])
	}
	if body["negative_prompt"] != "text" {
		t.Fatalf("negative_prompt = %v (legacy fallback)", body["negative_prompt"])
	}
	if body["generate_audio"] != false {
		t.Fatalf("generate_audio = %v, want false (legacy fallback)", body["generate_audio"])
	}
	if got, ok := body["image_url"].(string); !ok || !strings.HasPrefix(got, "data:image/") {
		t.Fatalf("image_url = %v, want the source string (legacy fallback)", body["image_url"])
	}
	if len(notices) != 1 {
		t.Fatalf("expected one schema-unavailable notice, got %v", notices)
	}
	if !strings.Contains(notices[0], "Couldn't load") {
		t.Fatalf("notice = %q, want it to mention the unavailable schema", notices[0])
	}
}

// TestResolveVideoBodyNoSourceInput verifies that a model lacking a source-video
// field degrades cleanly with a notice when the user attached a video to extend.
func TestResolveVideoBodyNoSourceInput(t *testing.T) {
	body, notices, _ := resolveVideoBody(loadSchema(t, "veo3.1"),
		VideoGenerateRequest{
			Model:  "fal-ai/veo3.1",
			Prompt: "extend this clip",
			Video:  "data:video/mp4;base64,AAA",
		},
		builtinFalOverrides())
	// veo3.1 (text-to-video) has no video_url field — the video is dropped.
	if _, present := body["video_url"]; present {
		t.Fatalf("video_url must not be set on a model with no source-video input; got %v", body["video_url"])
	}
	if len(notices) != 1 {
		t.Fatalf("expected one source-video-ignored notice, got %v", notices)
	}
	if !strings.Contains(notices[0], "source-video") {
		t.Fatalf("notice = %q, want it to mention the ignored source video", notices[0])
	}
}

// multiImageVideoSchema is a synthesized schema with an array-typed image_urls
// source field (the shape of seedance reference-to-video). It stands in for a
// real fal fixture: no captured reference-to-video OpenAPI doc exists in the
// testdata tree yet, so build the schema inline the way the music-length and
// nano-banana tests do.
func multiImageVideoSchema(maxItems int) *ModelInputSchema {
	prop := SchemaProperty{Name: "image_urls", Kind: schemaArray, MaxItems: maxItems, Items: &SchemaProperty{Name: "image_urls", Kind: schemaScalar}}
	return &ModelInputSchema{
		Properties: map[string]SchemaProperty{
			"prompt":     {Name: "prompt", Kind: schemaScalar},
			"image_urls": prop,
		},
		order: []string{"prompt", "image_urls"},
	}
}

// scalarImageVideoSchema is a synthesized schema with a scalar image_url field
// (the shape of a single-frame image-to-video endpoint like seedance
// image-to-video). Multiple attached images against it must hard-error.
func scalarImageVideoSchema() *ModelInputSchema {
	return &ModelInputSchema{
		Properties: map[string]SchemaProperty{
			"prompt":    {Name: "prompt", Kind: schemaScalar},
			"image_url": {Name: "image_url", Kind: schemaScalar},
		},
		order: []string{"prompt", "image_url"},
	}
}

// TestResolveVideoBodyMultiImageArray verifies that multiple attached images
// fan out onto an array-typed image_urls field for a reference-to-video model.
// All three URLs must reach the body in the user's attach order.
func TestResolveVideoBodyMultiImageArray(t *testing.T) {
	body, notices, err := resolveVideoBody(multiImageVideoSchema(0),
		VideoGenerateRequest{
			Model:  "bytedance/seedance-2.0/reference-to-video",
			Prompt: "blend @Image1 and @Image2",
			Images: []string{"data:image/png;base64,AAA", "data:image/png;base64,BBB", "data:image/png;base64,CCC"},
		},
		builtinFalOverrides())
	if err != nil {
		t.Fatalf("expected no error for array multi-image, got %v", err)
	}
	urls, ok := body["image_urls"].([]any)
	if !ok {
		t.Fatalf("image_urls = %v (type %T), want []any", body["image_urls"], body["image_urls"])
	}
	if len(urls) != 3 {
		t.Fatalf("expected 3 image_urls, got %d (%v)", len(urls), urls)
	}
	// Order preserved.
	for i, want := range []string{"data:image/png;base64,AAA", "data:image/png;base64,BBB", "data:image/png;base64,CCC"} {
		if urls[i] != want {
			t.Errorf("image_urls[%d] = %v, want %q", i, urls[i], want)
		}
	}
	if len(notices) != 0 {
		t.Fatalf("expected no notices for multi-image array path, got %v", notices)
	}
}

// TestResolveVideoBodySingleImageArrayStillWorks is a regression guard: a single
// image into an array field still produces a one-element slice (the original
// behavior of coerceVideoValue wrapping a scalar), now via coerceImages.
func TestResolveVideoBodySingleImageArrayStillWorks(t *testing.T) {
	body, _, err := resolveVideoBody(multiImageVideoSchema(0),
		VideoGenerateRequest{
			Model:  "bytedance/seedance-2.0/reference-to-video",
			Prompt: "animate this",
			Images: []string{"data:image/png;base64,AAA"},
		},
		builtinFalOverrides())
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	urls, ok := body["image_urls"].([]any)
	if !ok || len(urls) != 1 || urls[0] != "data:image/png;base64,AAA" {
		t.Fatalf("image_urls = %v, want a one-element slice with the data URI", body["image_urls"])
	}
}

// TestResolveVideoBodyOverrideArrayAcceptsMultipleImages is the multi-image
// counterpart to the Kling builtin override: when an override forces Kind:"array"
// for a model whose schema declares a scalar image_url, multiple attached images
// must fan out onto the override's image_urls field rather than tripping the
// scalar multi-image guardrail. This proves the fabricated schemaArray property
// flows through the same code path as a real schema-declared array.
func TestResolveVideoBodyOverrideArrayAcceptsMultipleImages(t *testing.T) {
	// Schema declares a scalar image_url (the drift shape); the override routes
	// sourceImage onto image_urls as an array.
	schema := &ModelInputSchema{
		Properties: map[string]SchemaProperty{
			"prompt":    {Name: "prompt", Kind: schemaScalar},
			"image_url": {Name: "image_url", Kind: schemaScalar, Type: "string"},
		},
		order: []string{"prompt", "image_url"},
	}
	ov := Overrides{byCategory: map[string]map[string]map[string]overrideEntry{
		"video": {
			"acme/drift": {
				"sourceImage": {Path: "image_urls", Kind: "array", Items: "string"},
			},
		},
	}}
	body, _, err := resolveVideoBody(schema,
		VideoGenerateRequest{
			Model:  "acme/drift",
			Prompt: "blend two references",
			Images: []string{"data:image/png;base64,AAA", "data:image/png;base64,BBB"},
		},
		ov)
	if err != nil {
		t.Fatalf("expected multi-image into an override-forced array to succeed, got %v", err)
	}
	urls, ok := body["image_urls"].([]any)
	if !ok {
		t.Fatalf("image_urls = %v (%T), want []any", body["image_urls"], body["image_urls"])
	}
	if len(urls) != 2 || urls[0] != "data:image/png;base64,AAA" || urls[1] != "data:image/png;base64,BBB" {
		t.Fatalf("image_urls = %+v, want both images in attach order", urls)
	}
	if _, present := body["image_url"]; present {
		t.Fatalf("image_url must not be set; the override routes onto image_urls. got %v", body["image_url"])
	}
}

// TestResolveVideoBodyScalarRejectsMultipleImages verifies the hard-error
// guardrail: a scalar-image model (e.g. seedance image-to-video) with multiple
// attached images must fail the call rather than silently drop the extras. The
// error must name the model and the attached count so the user knows what to fix.
func TestResolveVideoBodyScalarRejectsMultipleImages(t *testing.T) {
	_, _, err := resolveVideoBody(scalarImageVideoSchema(),
		VideoGenerateRequest{
			Model:  "bytedance/seedance-2.0/image-to-video",
			Prompt: "blend these",
			Images: []string{"data:image/png;base64,AAA", "data:image/png;base64,BBB", "data:image/png;base64,CCC"},
		},
		builtinFalOverrides())
	if err == nil {
		t.Fatal("expected a hard error for multi-image into a scalar model, got nil")
	}
	if !strings.Contains(err.Error(), "single image") {
		t.Errorf("error = %q, want it to mention 'single image'", err.Error())
	}
	if !strings.Contains(err.Error(), "bytedance/seedance-2.0/image-to-video") {
		t.Errorf("error = %q, want it to name the model", err.Error())
	}
	if !strings.Contains(err.Error(), "3 were attached") {
		t.Errorf("error = %q, want it to name the attached count", err.Error())
	}
}

// TestResolveVideoBodyMaxItemsExceeded verifies the maxItems guardrail: an array
// model declaring maxItems:2 must reject 3 attached images. The cap value must
// appear in the error so the user knows the limit.
func TestResolveVideoBodyMaxItemsExceeded(t *testing.T) {
	_, _, err := resolveVideoBody(multiImageVideoSchema(2),
		VideoGenerateRequest{
			Model:  "bytedance/seedance-2.0/reference-to-video",
			Prompt: "blend these",
			Images: []string{"data:image/png;base64,AAA", "data:image/png;base64,BBB", "data:image/png;base64,CCC"},
		},
		builtinFalOverrides())
	if err == nil {
		t.Fatal("expected a hard error for exceeding maxItems, got nil")
	}
	if !strings.Contains(err.Error(), "at most 2") {
		t.Errorf("error = %q, want it to name the maxItems cap (2)", err.Error())
	}
	if !strings.Contains(err.Error(), "3 were attached") {
		t.Errorf("error = %q, want it to name the attached count", err.Error())
	}
}

// TestResolveVideoBodyNoSchemaRejectsMultipleImages verifies the nil-schema
// legacy fallback also rejects multi-image — it only knows a scalar image_url,
// so silently dropping extras there would hide the capability mismatch too.
func TestResolveVideoBodyNoSchemaRejectsMultipleImages(t *testing.T) {
	_, _, err := resolveVideoBody(nil,
		VideoGenerateRequest{
			Model:  "unknown-model",
			Prompt: "blend these",
			Images: []string{"data:image/png;base64,AAA", "data:image/png;base64,BBB"},
		},
		builtinFalOverrides())
	if err == nil {
		t.Fatal("expected a hard error for multi-image with no schema, got nil")
	}
	if !strings.Contains(err.Error(), "at most one image") || !strings.Contains(err.Error(), "2 were attached") {
		t.Errorf("error = %q, want it to mention 'at most one image' and the count", err.Error())
	}
}

// TestResolveImageBodyMultiImageArray is the image-edit sibling of
// TestResolveVideoBodyMultiImageArray: multiple attached images fan out onto an
// array-typed image_urls field (e.g. nano-banana/edit multi-reference edit).
func TestResolveImageBodyMultiImageArray(t *testing.T) {
	body, _, err := resolveImageBody(loadSchema(t, "nano-banana-edit"),
		ImageGenerateRequest{
			Model:  "fal-ai/nano-banana/edit",
			Prompt: "blend @Image1 and @Image2",
			Images: []string{"data:image/png;base64,AAA", "data:image/png;base64,BBB"},
		},
		builtinFalOverrides())
	if err != nil {
		t.Fatalf("expected no error for array multi-image edit, got %v", err)
	}
	urls, ok := body["image_urls"].([]any)
	if !ok || len(urls) != 2 {
		t.Fatalf("image_urls = %v, want a 2-element slice", body["image_urls"])
	}
	// Order preserved.
	for i, want := range []string{"data:image/png;base64,AAA", "data:image/png;base64,BBB"} {
		if urls[i] != want {
			t.Errorf("image_urls[%d] = %v, want %q", i, urls[i], want)
		}
	}
}

// TestResolveImageBodyScalarRejectsMultipleImages verifies the image-edit
// hard-error guardrail: a scalar-image edit model with multiple attached images
// fails the call. Uses the flux-dev image-to-image schema whose source field is
// a scalar image_url.
func TestResolveImageBodyScalarRejectsMultipleImages(t *testing.T) {
	_, _, err := resolveImageBody(loadSchema(t, "flux-dev-image-to-image"),
		ImageGenerateRequest{
			Model:  "fal-ai/flux/dev/image-to-image",
			Prompt: "blend these",
			Images: []string{"data:image/png;base64,AAA", "data:image/png;base64,BBB"},
		},
		builtinFalOverrides())
	if err == nil {
		t.Fatal("expected a hard error for multi-image into a scalar edit model, got nil")
	}
	if !strings.Contains(err.Error(), "single image") || !strings.Contains(err.Error(), "2 were attached") {
		t.Errorf("error = %q, want it to mention single image and the count", err.Error())
	}
}

// TestResolveLipsyncBodyAudioToVideo verifies the audio-to-video path: the
// driving audio maps onto audio_url and the face image onto image_url. Uses the
// real image-capable endpoint sync-lipsync/v3/image-to-video (required:
// image_url, audio_url). The Kling lipsync/audio-to-video endpoint is NOT
// image-capable despite its name — see TestResolveLipsyncBodyImageOnVideoOnlyModel.
func TestResolveLipsyncBodyAudioToVideo(t *testing.T) {
	body, notices, err := resolveLipsyncBody(loadSchema(t, "sync-lipsync-v3-image-to-video"),
		LipsyncGenerateRequest{
			Model: "fal-ai/sync-lipsync/v3/image-to-video",
			Audio: "data:audio/mpeg;base64,AAA",
			Image: "data:image/png;base64,BBB",
		},
		builtinFalOverrides())
	if err != nil {
		t.Fatalf("resolveLipsyncBody returned error for image-capable model: %v", err)
	}
	if got, ok := body["audio_url"].(string); !ok || !strings.HasPrefix(got, "data:audio/") {
		t.Fatalf("audio_url = %v, want the driving audio data URI", body["audio_url"])
	}
	if got, ok := body["image_url"].(string); !ok || !strings.HasPrefix(got, "data:image/") {
		t.Fatalf("image_url = %v, want the face image data URI", body["image_url"])
	}
	if _, present := body["video_url"]; present {
		t.Fatalf("video_url must not be set for audio-to-video; got %v", body["video_url"])
	}
	if len(notices) != 0 {
		t.Fatalf("expected no notices for image-capable audio-to-video, got %v", notices)
	}
}

// TestResolveLipsyncBodyImageOnVideoOnlyModel is the regression test for
// conv_ff1caffa123d39a9fd98f2ac: an audio+image turn was routed to Kling's
// lipsync/audio-to-video endpoint, which despite its name is video-only
// (required: video_url, audio_url — no image input). Previously the image was
// dropped with a notice and the request 422'd downstream with a confusing
// "video_url: Field required". Now the unmapped face source is a hard error
// with an actionable message naming the mismatch.
func TestResolveLipsyncBodyImageOnVideoOnlyModel(t *testing.T) {
	_, _, err := resolveLipsyncBody(loadSchema(t, "kling-lipsync-audio-to-video"),
		LipsyncGenerateRequest{
			Model: "fal-ai/kling-video/lipsync/audio-to-video",
			Audio: "data:audio/mpeg;base64,AAA",
			Image: "data:image/png;base64,BBB",
		},
		builtinFalOverrides())
	if err == nil {
		t.Fatalf("expected a hard error when an image is sent to a video-only lip sync model, got nil")
	}
	if !strings.Contains(err.Error(), "no image input") {
		t.Fatalf("error = %q, want it to explain the model has no image input", err.Error())
	}
}

// TestResolveLipsyncBodyVideoOnImageOnlyModel is the symmetric guard: sending a
// video face source to an image-only model is likewise a hard error.
func TestResolveLipsyncBodyVideoOnImageOnlyModel(t *testing.T) {
	_, _, err := resolveLipsyncBody(loadSchema(t, "sync-lipsync-v3-image-to-video"),
		LipsyncGenerateRequest{
			Model: "fal-ai/sync-lipsync/v3/image-to-video",
			Audio: "data:audio/mpeg;base64,AAA",
			Video: "data:video/mp4;base64,CCC",
		},
		builtinFalOverrides())
	if err == nil {
		t.Fatalf("expected a hard error when a video is sent to an image-only lip sync model, got nil")
	}
	if !strings.Contains(err.Error(), "no video input") {
		t.Fatalf("error = %q, want it to explain the model has no video input", err.Error())
	}
}

// TestResolveLipsyncBodyVideoToVideo verifies the video-to-video path: the
// driving audio maps onto audio_url and the face video onto video_url.
func TestResolveLipsyncBodyVideoToVideo(t *testing.T) {
	body, notices, err := resolveLipsyncBody(loadSchema(t, "sync-lipsync-v2-pro"),
		LipsyncGenerateRequest{
			Model: "fal-ai/sync-lipsync/v2/pro",
			Audio: "data:audio/mpeg;base64,AAA",
			Video: "data:video/mp4;base64,CCC",
		},
		builtinFalOverrides())
	if err != nil {
		t.Fatalf("resolveLipsyncBody returned error for video-capable model: %v", err)
	}
	if got, ok := body["audio_url"].(string); !ok || !strings.HasPrefix(got, "data:audio/") {
		t.Fatalf("audio_url = %v, want the driving audio data URI", body["audio_url"])
	}
	if got, ok := body["video_url"].(string); !ok || !strings.HasPrefix(got, "data:video/") {
		t.Fatalf("video_url = %v, want the face video data URI", body["video_url"])
	}
	if _, present := body["image_url"]; present {
		t.Fatalf("image_url must not be set for video-to-video; got %v", body["image_url"])
	}
	if len(notices) != 0 {
		t.Fatalf("expected no notices for sync-lipsync video-to-video, got %v", notices)
	}
}

// TestResolveLipsyncBodyNoSchema verifies the nil-schema generic fallback maps
// the audio + whichever face source is present, plus a schema-unavailable
// notice.
func TestResolveLipsyncBodyNoSchema(t *testing.T) {
	body, notices, err := resolveLipsyncBody(nil,
		LipsyncGenerateRequest{
			Model: "fal-ai/sync-lipsync/v2/pro",
			Audio: "data:audio/mpeg;base64,AAA",
			Video: "data:video/mp4;base64,CCC",
		},
		builtinFalOverrides())
	if err != nil {
		t.Fatalf("nil-schema fallback returned error: %v", err)
	}
	if got, ok := body["audio_url"].(string); !ok || !strings.HasPrefix(got, "data:audio/") {
		t.Fatalf("audio_url = %v, want the driving audio data URI", body["audio_url"])
	}
	if got, ok := body["video_url"].(string); !ok || !strings.HasPrefix(got, "data:video/") {
		t.Fatalf("video_url = %v, want the face video data URI", body["video_url"])
	}
	if len(notices) != 1 {
		t.Fatalf("expected one schema-unavailable notice, got %v", notices)
	}
	if !strings.Contains(notices[0], "Couldn't load") {
		t.Fatalf("notice = %q, want it to mention the unavailable schema", notices[0])
	}
}
