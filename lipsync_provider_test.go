package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// loadReplicateSchema reads a captured lipsync collection model's OpenAPI
// schema — the same bytes GetModelInputSchema returns for the model — from
// testdata/replicate-schemas. The fixtures are real scraped schemas, so the
// synonym table and the picker predicate are pinned against the dialect the
// collection actually speaks, not an invented one.
func loadReplicateSchema(t *testing.T, name string) *ModelInputSchema {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "replicate-schemas", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	s, err := parseModelInputSchema(raw)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestMergeAppConfigNormalizesLipsyncProvider(t *testing.T) {
	cases := []struct {
		name  string
		value string
		want  string
	}{
		{"empty inherits fal", "", "fal"},
		{"unknown falls back to fal", "openrouter", "fal"},
		{"whitespace pinned replicate is kept", " replicate ", "replicate"},
		{"explicit fal", "fal", "fal"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			config := defaultAppConfig()
			config.Models.LipsyncProvider = tc.value
			merged := mergeAppConfig(config)
			if merged.Models.LipsyncProvider != tc.want {
				t.Fatalf("LipsyncProvider = %q, want %q", merged.Models.LipsyncProvider, tc.want)
			}
		})
	}
}

func TestLipsyncModelSlotsRouteByProvider(t *testing.T) {
	config := defaultAppConfig()
	config.Providers.Fal.LipsyncImageModel = "fal-ai/sync-lipsync/v3/image-to-video"
	config.Providers.Fal.LipsyncVideoModel = "fal-ai/kling-video/lipsync/audio-to-video"
	config.Providers.Replicate.LipsyncImageModel = "veed/fabric-1.0"
	config.Providers.Replicate.LipsyncVideoModel = "sync/lipsync-2"

	if got := resolveDefaultLipsyncImageModel(config, "fal"); got != "fal-ai/sync-lipsync/v3/image-to-video" {
		t.Fatalf("fal image slot = %q", got)
	}
	if got := resolveDefaultLipsyncVideoModel(config, "fal"); got != "fal-ai/kling-video/lipsync/audio-to-video" {
		t.Fatalf("fal video slot = %q", got)
	}
	if got := resolveDefaultLipsyncImageModel(config, "replicate"); got != "veed/fabric-1.0" {
		t.Fatalf("replicate image slot = %q", got)
	}
	if got := resolveDefaultLipsyncVideoModel(config, "replicate"); got != "sync/lipsync-2" {
		t.Fatalf("replicate video slot = %q", got)
	}

	// Empty slots fall back to each provider's known-good defaults — no
	// model needs configuring before lip_sync is offered, the transcription
	// slots' contract.
	empty := defaultAppConfig()
	if got := resolveDefaultLipsyncImageModel(empty, "replicate"); got != defaultReplicateLipsyncImageModel {
		t.Fatalf("empty replicate image slot = %q, want default %q", got, defaultReplicateLipsyncImageModel)
	}
	if got := resolveDefaultLipsyncVideoModel(empty, "replicate"); got != defaultReplicateLipsyncVideoModel {
		t.Fatalf("empty replicate video slot = %q, want default %q", got, defaultReplicateLipsyncVideoModel)
	}
	if got := resolveDefaultLipsyncImageModel(empty, "fal"); got != defaultFalLipsyncImageModel {
		t.Fatalf("empty fal image slot = %q, want default %q", got, defaultFalLipsyncImageModel)
	}
}

// TestReplicateLipsyncSchemaServes pins the picker predicate against the
// collection's real schemas: which models serve each face kind, and which
// the canonical request cannot drive (text-driven speech, a required prompt,
// a voice-script avatar) drop out entirely.
func TestReplicateLipsyncSchemaServes(t *testing.T) {
	cases := []struct {
		fixture       string
		wantImageFace bool
		wantVideoFace bool
	}{
		{"sync-lipsync-2", false, true},
		{"sync-lipsync-2-pro", false, true},
		{"heygen-lipsync-speed", false, true},
		{"pixverse-lipsync", false, true},
		{"veed-fabric-1.0", true, false},
		{"bytedance-omni-human", true, false},
		// Text-driven contracts the canonical request cannot fill: kling's
		// text + voice_id, wan's required prompt, p-video-avatar's
		// voice_script.
		{"kling-lip-sync", false, false},
		{"wan-2.2-s2v", false, false},
		{"p-video-avatar", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.fixture, func(t *testing.T) {
			schema := loadReplicateSchema(t, tc.fixture)
			if got := replicateLipsyncSchemaServes(schema, "sourceImage"); got != tc.wantImageFace {
				t.Fatalf("sourceImage serves = %v, want %v", got, tc.wantImageFace)
			}
			if got := replicateLipsyncSchemaServes(schema, "sourceVideo"); got != tc.wantVideoFace {
				t.Fatalf("sourceVideo serves = %v, want %v", got, tc.wantVideoFace)
			}
		})
	}
	if replicateLipsyncSchemaServes(nil, "sourceVideo") {
		t.Fatal("nil schema must not serve")
	}
}

// TestResolveLipsyncBodyReplicateDialect pins the canonical request onto the
// Replicate dialect through the shared body resolver: the audio and the face
// land on the model's own key names, and a face kind the model cannot accept
// is the fatal face-source error, not a silent drop.
func TestResolveLipsyncBodyReplicateDialect(t *testing.T) {
	t.Run("video face on sync/lipsync-2", func(t *testing.T) {
		body, notices, err := resolveLipsyncBody(loadReplicateSchema(t, "sync-lipsync-2"),
			LipsyncGenerateRequest{
				Model: "sync/lipsync-2",
				Audio: "data:audio/mpeg;base64,AAA",
				Video: "data:video/mp4;base64,BBB",
			},
			Overrides{}, "replicate-lipsync")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(notices) != 0 {
			t.Fatalf("unexpected notices: %v", notices)
		}
		if body["audio"] != "data:audio/mpeg;base64,AAA" || body["video"] != "data:video/mp4;base64,BBB" {
			t.Fatalf("body = %v, want the audio and video keys set", body)
		}
		if _, ok := body["image"]; ok {
			t.Fatalf("body must not carry an image key: %v", body)
		}
	})

	t.Run("image face on veed/fabric-1.0", func(t *testing.T) {
		body, _, err := resolveLipsyncBody(loadReplicateSchema(t, "veed-fabric-1.0"),
			LipsyncGenerateRequest{
				Model: "veed/fabric-1.0",
				Audio: "data:audio/mpeg;base64,AAA",
				Image: "data:image/png;base64,CCC",
			},
			Overrides{}, "replicate-lipsync")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if body["image"] != "data:image/png;base64,CCC" || body["audio"] != "data:audio/mpeg;base64,AAA" {
			t.Fatalf("body = %v, want the image and audio keys set", body)
		}
	})

	t.Run("video face against an image-face model is fatal", func(t *testing.T) {
		_, _, err := resolveLipsyncBody(loadReplicateSchema(t, "veed-fabric-1.0"),
			LipsyncGenerateRequest{
				Model: "veed/fabric-1.0",
				Audio: "data:audio/mpeg;base64,AAA",
				Video: "data:video/mp4;base64,BBB",
			},
			Overrides{}, "replicate-lipsync")
		if err == nil || !strings.Contains(err.Error(), "no video input") {
			t.Fatalf("error = %v, want the no-video-input face-source failure", err)
		}
	})

	t.Run("image face against a video-face model is fatal", func(t *testing.T) {
		_, _, err := resolveLipsyncBody(loadReplicateSchema(t, "sync-lipsync-2"),
			LipsyncGenerateRequest{
				Model: "sync/lipsync-2",
				Audio: "data:audio/mpeg;base64,AAA",
				Image: "data:image/png;base64,CCC",
			},
			Overrides{}, "replicate-lipsync")
		if err == nil || !strings.Contains(err.Error(), "no image input") {
			t.Fatalf("error = %v, want the no-image-input face-source failure", err)
		}
	})

	t.Run("video-retalking's face and input_audio ride the synonyms", func(t *testing.T) {
		// video-retalking names its inputs face/input_audio — the non-obvious
		// pairs the synonym table exists for. The schema here is the
		// captured shape minus every unrelated control.
		raw := map[string]any{
			"components": map[string]any{
				"schemas": map[string]any{
					"Input": map[string]any{
						"properties": map[string]any{
							"face":        map[string]any{"type": "string", "format": "uri"},
							"input_audio": map[string]any{"type": "string", "format": "uri"},
						},
						"required": []any{"face", "input_audio"},
					},
				},
			},
		}
		doc, err := json.Marshal(raw)
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := parseModelInputSchema(doc)
		if err != nil {
			t.Fatal(err)
		}
		body, _, err := resolveLipsyncBody(parsed,
			LipsyncGenerateRequest{
				Model: "chenxwh/video-retalking",
				Audio: "data:audio/mpeg;base64,AAA",
				Video: "data:video/mp4;base64,BBB",
			},
			Overrides{}, "replicate-lipsync")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if body["input_audio"] != "data:audio/mpeg;base64,AAA" || body["face"] != "data:video/mp4;base64,BBB" {
			t.Fatalf("body = %v, want face and input_audio keys set", body)
		}
	})
}

// TestConversationModelOverridesLipsyncRouting pins the per-conversation
// provider dimension: the provider override routes, and the model slots land
// on the ones the effective provider reads.
func TestConversationModelOverridesLipsyncRouting(t *testing.T) {
	base := func() AppConfig {
		config := defaultAppConfig()
		config.Providers.Fal.LipsyncImageModel = "fal-image"
		config.Providers.Fal.LipsyncVideoModel = "fal-video"
		return config
	}
	req := ChatRequest{Provider: "ollama", Model: "primary", SelectedModel: "primary"}

	t.Run("provider and slots route to replicate", func(t *testing.T) {
		config, _, err := overlayModelOverrides(base(), req, ConversationModelOverrides{
			LipsyncProvider:   "replicate",
			LipsyncImageModel: "rep-image",
			LipsyncVideoModel: "rep-video",
		})
		if err != nil {
			t.Fatal(err)
		}
		if config.Models.LipsyncProvider != "replicate" {
			t.Fatalf("LipsyncProvider = %q, want replicate", config.Models.LipsyncProvider)
		}
		if config.Providers.Replicate.LipsyncImageModel != "rep-image" || config.Providers.Replicate.LipsyncVideoModel != "rep-video" {
			t.Fatalf("replicate slots = %q/%q, want rep-image/rep-video",
				config.Providers.Replicate.LipsyncImageModel, config.Providers.Replicate.LipsyncVideoModel)
		}
		if config.Providers.Fal.LipsyncImageModel != "fal-image" || config.Providers.Fal.LipsyncVideoModel != "fal-video" {
			t.Fatalf("fal slots must stay untouched, got %q/%q",
				config.Providers.Fal.LipsyncImageModel, config.Providers.Fal.LipsyncVideoModel)
		}
	})

	t.Run("model-only override follows the global provider", func(t *testing.T) {
		globalReplicate := base()
		globalReplicate.Models.LipsyncProvider = "replicate"
		config, _, err := overlayModelOverrides(globalReplicate, req, ConversationModelOverrides{LipsyncImageModel: "rep-image"})
		if err != nil {
			t.Fatal(err)
		}
		if config.Providers.Replicate.LipsyncImageModel != "rep-image" {
			t.Fatalf("replicate image slot = %q, want rep-image", config.Providers.Replicate.LipsyncImageModel)
		}
	})

	t.Run("model-only override with a global fal stays fal", func(t *testing.T) {
		config, _, err := overlayModelOverrides(base(), req, ConversationModelOverrides{LipsyncImageModel: "rep-image"})
		if err != nil {
			t.Fatal(err)
		}
		if config.Providers.Fal.LipsyncImageModel != "rep-image" {
			t.Fatalf("fal image slot = %q, want rep-image", config.Providers.Fal.LipsyncImageModel)
		}
	})
}

// TestConversationModelOverridesRejectsUnknownLipsyncProvider pins the
// mutator-time validation against the same whitelist mergeAppConfig
// enforces.
func TestConversationModelOverridesRejectsUnknownLipsyncProvider(t *testing.T) {
	err := validateConversationModelOverrides(ConversationModelOverrides{LipsyncProvider: "openrouter"})
	if err == nil || !strings.Contains(err.Error(), "unknown lipsync provider") {
		t.Fatalf("error = %v, want the unknown lipsync provider rejection", err)
	}
	for _, ok := range []string{"", "fal", "replicate"} {
		if err := validateConversationModelOverrides(ConversationModelOverrides{LipsyncProvider: ok}); err != nil {
			t.Fatalf("lipsyncProvider %q: unexpected error %v", ok, err)
		}
	}
}

// TestConversationModelOverridesRejectsUnknownInpaintProvider pins the
// validation map entry for the inpaint provider — the case existed but no
// map entry ever reached it, so an invalid value slipped through mutator
// validation.
func TestConversationModelOverridesRejectsUnknownInpaintProvider(t *testing.T) {
	err := validateConversationModelOverrides(ConversationModelOverrides{InpaintProvider: "openrouter"})
	if err == nil || !strings.Contains(err.Error(), "unknown inpaint provider") {
		t.Fatalf("error = %v, want the unknown inpaint provider rejection", err)
	}
	for _, ok := range []string{"", "fal", "replicate"} {
		if err := validateConversationModelOverrides(ConversationModelOverrides{InpaintProvider: ok}); err != nil {
			t.Fatalf("inpaintProvider %q: unexpected error %v", ok, err)
		}
	}
}
