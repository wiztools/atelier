package main

import (
	"context"
	"encoding/base64"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
)

// parseReplicateFixtureSchema builds a ModelInputSchema from a raw OpenAPI
// document shaped like Replicate's latest_version.openapi_schema — the same
// parse path the runtime uses.
func parseReplicateFixtureSchema(t *testing.T, doc string) *ModelInputSchema {
	t.Helper()
	schema, err := parseModelInputSchema([]byte(doc))
	if err != nil {
		t.Fatalf("parseModelInputSchema: %v", err)
	}
	return schema
}

const replicateEditModelSchema = `{"components":{"schemas":{"Input":{"type":"object","properties":{
	"prompt":{"type":"string","description":"the edit instruction"},
	"input_image":{"type":"string","description":"source frame"},
	"aspect_ratio":{"type":"string","enum":["match_input_image","1:1","16:9","9:16"]},
	"output_format":{"type":"string","default":"webp"},
	"num_outputs":{"type":"integer","default":1}
}}}}}`

const replicateVideoModelSchema = `{"components":{"schemas":{"Input":{"type":"object","properties":{
	"prompt":{"type":"string"},
	"duration":{"type":"number","description":"seconds"},
	"aspect_ratio":{"type":"string","enum":["16:9","9:16","1:1"]},
	"first_frame_image":{"type":"string"},
	"resolution":{"type":"string","enum":["480p","720p","1080p"]},
	"negative_prompt":{"type":"string"}
}}}}}`

func TestResolveReplicateImageInput(t *testing.T) {
	schema := parseReplicateFixtureSchema(t, replicateEditModelSchema)
	req := ImageGenerateRequest{
		Model:       "black-forest-labs/flux-kontext-pro",
		Prompt:      "make it snowy",
		Images:      []string{"data:image/png;base64," + tinyPNG},
		AspectRatio: "9:16",
	}
	input, notices, err := resolveReplicateImageInput(schema, req)
	if err != nil {
		t.Fatalf("resolveReplicateImageInput: %v", err)
	}
	if len(notices) != 0 {
		t.Fatalf("notices = %v, want none", notices)
	}
	if input["prompt"] != "make it snowy" {
		t.Fatalf("prompt = %v", input["prompt"])
	}
	// flux-kontext names its source frame input_image — the synonym table must
	// find it.
	if got, ok := input["input_image"].(string); !ok || !strings.HasPrefix(got, "data:image/png;base64,") {
		t.Fatalf("input_image = %v", input["input_image"])
	}
	if input["aspect_ratio"] != "9:16" {
		t.Fatalf("aspect_ratio = %v", input["aspect_ratio"])
	}
	if input["num_outputs"] != 1 {
		t.Fatalf("num_outputs = %v, want 1 (declared by the model)", input["num_outputs"])
	}
	// Speculative fal-side keys must never appear — Replicate rejects unknown
	// inputs.
	for _, key := range []string{"num_images", "image_size", "image_url"} {
		if _, exists := input[key]; exists {
			t.Fatalf("input carries speculative key %q: %v", key, input)
		}
	}
}

func TestResolveReplicateImageInputEnumAndDrops(t *testing.T) {
	schema := parseReplicateFixtureSchema(t, replicateEditModelSchema)
	input, notices, err := resolveReplicateImageInput(schema, ImageGenerateRequest{
		Model:       "owner/model",
		Prompt:      "x",
		AspectRatio: "4:3", // not in the model's enum
		Steps:       30,    // no num_inference_steps declared
	})
	if err != nil {
		t.Fatalf("resolveReplicateImageInput: %v", err)
	}
	if _, exists := input["aspect_ratio"]; exists {
		t.Fatal("an out-of-enum aspect ratio must not be sent")
	}
	if len(notices) != 1 || !strings.Contains(notices[0], `aspect ratio "4:3"`) {
		t.Fatalf("notices = %v, want the enum-drop notice", notices)
	}
	if _, exists := input["num_inference_steps"]; exists {
		t.Fatal("steps must be omitted on a model without the field")
	}
}

func TestResolveReplicateImageInputMultiImageScalarError(t *testing.T) {
	schema := parseReplicateFixtureSchema(t, replicateEditModelSchema)
	_, _, err := resolveReplicateImageInput(schema, ImageGenerateRequest{
		Model:  "owner/model",
		Prompt: "x",
		Images: []string{"data:image/png;base64," + tinyPNG, "data:image/png;base64," + tinyPNG},
	})
	if err == nil || !strings.Contains(err.Error(), "single image") {
		t.Fatalf("err = %v, want the multi-image-scalar refusal", err)
	}
}

func TestResolveReplicateImageInputArrayImages(t *testing.T) {
	doc := `{"components":{"schemas":{"Input":{"type":"object","properties":{
		"prompt":{"type":"string"},
		"image":{"type":"array","items":{"type":"string"},"maxItems":3}
	}}}}}`
	schema := parseReplicateFixtureSchema(t, doc)
	input, _, err := resolveReplicateImageInput(schema, ImageGenerateRequest{
		Model:  "google/nano-banana",
		Prompt: "x",
		Images: []string{"data:image/png;base64," + tinyPNG, "data:image/png;base64," + tinyPNG},
	})
	if err != nil {
		t.Fatalf("resolveReplicateImageInput: %v", err)
	}
	images, ok := input["image"].([]any)
	if !ok || len(images) != 2 {
		t.Fatalf("image = %v, want both entries", input["image"])
	}
}

func TestResolveReplicateImageInputNilSchemaFallback(t *testing.T) {
	input, notices, err := resolveReplicateImageInput(nil, ImageGenerateRequest{
		Model:  "owner/model",
		Prompt: "x",
		Images: []string{"data:image/png;base64," + tinyPNG},
	})
	if err != nil {
		t.Fatalf("resolveReplicateImageInput: %v", err)
	}
	if input["prompt"] != "x" || !strings.HasPrefix(input["image"].(string), "data:image/png") {
		t.Fatalf("minimal fallback input = %v", input)
	}
	if len(notices) != 1 {
		t.Fatalf("notices = %v, want the schema-unavailable notice", notices)
	}
}

func TestResolveReplicateVideoInput(t *testing.T) {
	schema := parseReplicateFixtureSchema(t, replicateVideoModelSchema)
	input, notices, err := resolveReplicateVideoInput(schema, VideoGenerateRequest{
		Model:               "wan-video/wan-2.5-i2v",
		Prompt:              "waves crash",
		Duration:            "5",
		AspectRatio:         "16:9",
		AspectRatioExplicit: true,
		Images:              []string{"data:image/png;base64," + tinyPNG},
		Resolution:          "720p",
	})
	if err != nil {
		t.Fatalf("resolveReplicateVideoInput: %v", err)
	}
	if len(notices) != 0 {
		t.Fatalf("notices = %v, want none", notices)
	}
	// The duration field is number-typed: "5" must coerce to the JSON number 5.
	if input["duration"] != float64(5) && input["duration"] != 5 {
		t.Fatalf("duration = %v (%T), want numeric 5", input["duration"], input["duration"])
	}
	if got, ok := input["first_frame_image"].(string); !ok || !strings.HasPrefix(got, "data:image/png;base64,") {
		t.Fatalf("first_frame_image = %v", input["first_frame_image"])
	}
	if input["aspect_ratio"] != "16:9" {
		t.Fatalf("aspect_ratio = %v (an explicit ratio is sent even with a source frame)", input["aspect_ratio"])
	}
	if input["resolution"] != "720p" {
		t.Fatalf("resolution = %v", input["resolution"])
	}
}

// replicateWan27VideoModelSchema mirrors wan-video/wan-2.7-i2v's declared
// inputs: the 2.7 revision renamed the source image from "image" to
// "first_frame" and added first_clip/last_frame for clip continuation and
// last-frame keyframes.
const replicateWan27VideoModelSchema = `{"components":{"schemas":{"Input":{"type":"object","properties":{
	"prompt":{"type":"string"},
	"duration":{"type":"integer","default":5,"minimum":2,"maximum":15},
	"first_frame":{"type":"string","format":"uri","nullable":true},
	"first_clip":{"type":"string","format":"uri","nullable":true},
	"last_frame":{"type":"string","format":"uri","nullable":true},
	"resolution":{"type":"string","enum":["480p","720p","1080p"],"default":"1080p"}
}}}}}`

// TestResolveReplicateVideoInputWan27FirstFrame pins the synonym for wan 2.7's
// renamed input: before it landed, the unmapped image was dropped and the
// prediction failed server-side demanding first_frame/first_clip
// (conv_d5f5177320da74a0ce3caebb).
func TestResolveReplicateVideoInputWan27FirstFrame(t *testing.T) {
	schema := parseReplicateFixtureSchema(t, replicateWan27VideoModelSchema)
	input, notices, err := resolveReplicateVideoInput(schema, VideoGenerateRequest{
		Model:  "wan-video/wan-2.7-i2v",
		Prompt: "folks with scissors for hands go about their day",
		Images: []string{"data:image/png;base64," + tinyPNG},
	})
	if err != nil {
		t.Fatalf("resolveReplicateVideoInput: %v", err)
	}
	if len(notices) != 0 {
		t.Fatalf("notices = %v, want none", notices)
	}
	if got, ok := input["first_frame"].(string); !ok || !strings.HasPrefix(got, "data:image/png;base64,") {
		t.Fatalf("first_frame = %v", input["first_frame"])
	}
}

// TestResolveReplicateVideoInputAutoDurationResolvesToModelDefault pins the
// Replicate side of the fal "auto"-duration fix: wan 2.7's duration is an
// enum-less integer (2–15, default 5), so the canonical "auto" must resolve to
// the model's own default — field omitted, notice naming the default — rather
// than being sent as a string the prediction would reject.
func TestResolveReplicateVideoInputAutoDurationResolvesToModelDefault(t *testing.T) {
	schema := parseReplicateFixtureSchema(t, replicateWan27VideoModelSchema)
	input, notices, err := resolveReplicateVideoInput(schema, VideoGenerateRequest{
		Model:  "wan-video/wan-2.7-i2v",
		Prompt: "folks with scissors for hands go about their day",
		Images: []string{"data:image/png;base64," + tinyPNG},
		// The configured global default can be "auto" (Seedance's value) even
		// while a Replicate model is routed.
		Duration: "auto",
	})
	if err != nil {
		t.Fatalf("resolveReplicateVideoInput: %v", err)
	}
	if _, exists := input["duration"]; exists {
		t.Fatalf("duration \"auto\" must resolve to the model's own default (field omitted), got %v", input["duration"])
	}
	if len(notices) != 1 {
		t.Fatalf("expected exactly the auto-duration notice, got %v", notices)
	}
	if !strings.Contains(notices[0], "no auto duration") || !strings.Contains(notices[0], "5 seconds") {
		t.Fatalf("notice = %q, want it to name the model's 5-second default", notices[0])
	}
}

func TestResolveReplicateVideoInputSourceRatioInheritance(t *testing.T) {
	schema := parseReplicateFixtureSchema(t, replicateVideoModelSchema)
	// A config-derived ratio (AspectRatioExplicit false) on an
	// image-to-video request is skipped so the frame's orientation wins.
	input, notices, err := resolveReplicateVideoInput(schema, VideoGenerateRequest{
		Model:       "wan-video/wan-2.5-i2v",
		Prompt:      "waves",
		AspectRatio: "16:9",
		Images:      []string{"data:image/png;base64," + tinyPNG},
	})
	if err != nil {
		t.Fatalf("resolveReplicateVideoInput: %v", err)
	}
	if _, exists := input["aspect_ratio"]; exists {
		t.Fatal("a non-explicit ratio must not be sent on a sourced request")
	}
	if len(notices) != 0 {
		t.Fatalf("notices = %v, want none (the skip is silent)", notices)
	}
}

func TestResolveReplicateVideoInputRefusals(t *testing.T) {
	schema := parseReplicateFixtureSchema(t, replicateVideoModelSchema)
	_, _, err := resolveReplicateVideoInput(schema, VideoGenerateRequest{
		Model:  "owner/model",
		Prompt: "extend",
		Videos: []string{"https://x.test/clip.mp4"},
	})
	if err != errReplicateVideoSourceUnsupported {
		t.Fatalf("video-source err = %v", err)
	}
	_, _, err = resolveReplicateVideoInput(schema, VideoGenerateRequest{
		Model:     "owner/model",
		Prompt:    "transition",
		Keyframes: true,
		Images:    []string{"https://x.test/a.png", "https://x.test/b.png"},
	})
	if err == nil || !strings.Contains(err.Error(), "keyframe") {
		t.Fatalf("keyframes err = %v", err)
	}
}

// TestResolveReplicateVideoInputExtendMapsGrokExtension pins the extend
// carve-out: a video-only ExtendSource request maps its clip onto the model's
// declared video input (grok-imagine-video-extension names it "video"),
// coerces the canonical duration string onto the number field, and skips the
// config-derived aspect ratio so the source clip's orientation governs — the
// fal-side inherit rule. A model without a video input refuses the extend the
// same way an image-to-video model refuses an unmapped image.
func TestResolveReplicateVideoInputExtendMapsGrokExtension(t *testing.T) {
	schema := parseReplicateFixtureSchema(t, `{"components":{"schemas":{"Input":{"type":"object","properties":{
		"prompt":{"type":"string"},
		"video":{"type":"string"},
		"duration":{"type":"number","default":6,"minimum":2,"maximum":10}
	}}}}}`)
	input, notices, err := resolveReplicateVideoInput(schema, VideoGenerateRequest{
		Model:        "xai/grok-imagine-video-extension",
		Prompt:       "the robot keeps walking",
		Duration:     "5",
		AspectRatio:  "16:9",
		Videos:       []string{"data:video/mp4;base64,QUJDRA=="},
		ExtendSource: true,
	})
	if err != nil {
		t.Fatalf("resolveReplicateVideoInput: %v", err)
	}
	if len(notices) != 0 {
		t.Fatalf("notices = %v, want none", notices)
	}
	if got, ok := input["video"].(string); !ok || !strings.HasPrefix(got, "data:video/mp4;base64,") {
		t.Fatalf("video = %v, want the source clip data URL", input["video"])
	}
	if input["duration"] != float64(5) {
		t.Fatalf("duration = %v (%T), want numeric 5", input["duration"], input["duration"])
	}
	if _, exists := input["aspect_ratio"]; exists {
		t.Fatal("a non-explicit aspect ratio must not be sent on an extend — the source clip's orientation governs")
	}

	// An extend onto a model with no video input is a hard refusal.
	noVideoSchema := parseReplicateFixtureSchema(t, `{"components":{"schemas":{"Input":{"type":"object","properties":{"prompt":{"type":"string"}}}}}}`)
	_, _, err = resolveReplicateVideoInput(noVideoSchema, VideoGenerateRequest{
		Model:        "owner/model",
		Prompt:       "extend",
		Videos:       []string{"data:video/mp4;base64,QUJDRA=="},
		ExtendSource: true,
	})
	if err == nil || !strings.Contains(err.Error(), "no source-video input") {
		t.Fatalf("err = %v, want the no-video-input refusal", err)
	}

	// The nil-schema fallback still carries the clip.
	input, _, err = resolveReplicateVideoInput(nil, VideoGenerateRequest{
		Model:        "owner/model",
		Prompt:       "extend",
		Videos:       []string{"data:video/mp4;base64,QUJDRA=="},
		ExtendSource: true,
	})
	if err != nil {
		t.Fatalf("nil-schema extend: %v", err)
	}
	if got, ok := input["video"].(string); !ok || !strings.HasPrefix(got, "data:video/mp4;base64,") {
		t.Fatalf("nil-schema video = %v", input["video"])
	}
}

// TestResolveReplicateVideoInputUnmappedSourceImageRefused pins the hard
// error for a source image the model's schema can't map: Replicate would
// accept the prediction (every input nullable) and the model would fail
// server-side, so the refusal must surface locally with the remedy.
func TestResolveReplicateVideoInputUnmappedSourceImageRefused(t *testing.T) {
	doc := `{"components":{"schemas":{"Input":{"type":"object","properties":{
		"prompt":{"type":"string"},
		"duration":{"type":"number"}
	}}}}}`
	schema := parseReplicateFixtureSchema(t, doc)
	_, _, err := resolveReplicateVideoInput(schema, VideoGenerateRequest{
		Model:  "owner/model",
		Prompt: "animate",
		Images: []string{"data:image/png;base64," + tinyPNG},
	})
	if err == nil || !strings.Contains(err.Error(), "no source-image input") {
		t.Fatalf("err = %v, want the unmapped-source-image refusal", err)
	}
}

func TestResolveReplicateVideoInputEnumDrops(t *testing.T) {
	schema := parseReplicateFixtureSchema(t, replicateVideoModelSchema)
	input, notices, err := resolveReplicateVideoInput(schema, VideoGenerateRequest{
		Model:      "owner/model",
		Prompt:     "x",
		Resolution: "4k", // not in the enum
		FPS:        "60", // no fps field declared
	})
	if err != nil {
		t.Fatalf("resolveReplicateVideoInput: %v", err)
	}
	if _, exists := input["resolution"]; exists {
		t.Fatal("an out-of-enum resolution must not be sent")
	}
	if _, exists := input["fps"]; exists {
		t.Fatal("fps must be omitted on a model without the field")
	}
	joined := strings.Join(notices, "\n")
	if !strings.Contains(joined, `resolution "4k"`) || !strings.Contains(joined, "frame-rate") {
		t.Fatalf("notices = %v, want both drop notices", notices)
	}
}

func TestResolveReplicateVideoInputNilSchemaFallback(t *testing.T) {
	input, notices, err := resolveReplicateVideoInput(nil, VideoGenerateRequest{
		Model:  "owner/model",
		Prompt: "x",
		Images: []string{"data:image/png;base64," + tinyPNG},
	})
	if err != nil {
		t.Fatalf("resolveReplicateVideoInput: %v", err)
	}
	if input["prompt"] != "x" || !strings.HasPrefix(input["image"].(string), "data:image/png") {
		t.Fatalf("minimal fallback input = %v", input)
	}
	if len(notices) != 1 {
		t.Fatalf("notices = %v, want the schema-unavailable notice", notices)
	}
}

// TestReplicateVideoDurationOptions exercises the duration picker lookup
// through the replicate schema cache against a mocked model fetch.
func TestReplicateVideoDurationOptions(t *testing.T) {
	doc := `{"latest_version":{"openapi_schema":{"components":{"schemas":{"Input":{"type":"object","properties":{
		"prompt":{"type":"string"},
		"duration":{"type":"string","enum":["5","10"]}
	}}}}}}}`
	httpClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/v1/models/owner/model" {
			t.Fatalf("unexpected request: %s %s", req.Method, req.URL.Path)
		}
		return jsonResp(doc), nil
	})}
	root := t.TempDir()
	// Seed the keychain so the cache's fetch path can load a token.
	keyring.MockInit()
	t.Cleanup(func() { _ = clearReplicateAPIKey() })
	if err := saveReplicateAPIKey("k"); err != nil {
		t.Fatalf("saveReplicateAPIKey: %v", err)
	}
	opts := replicateVideoDurationOptions(context.Background(), httpClient, root, "owner/model")
	if len(opts) != 2 || opts[0] != "5" || opts[1] != "10" {
		t.Fatalf("opts = %v, want [5 10]", opts)
	}
	// The cached schema lands in the replicate-namespaced directory.
	if _, err := os.Stat(filepath.Join(root, "schema-cache", "replicate", "owner_model.json")); err != nil {
		t.Fatalf("replicate cache file missing: %v", err)
	}
}

// TestReplicateVideoDurationOptionsIntegerRange: an enum-less integer duration
// with declared bounds synthesizes its picker range (wan 2.7's 2–15) instead of
// returning nil and dropping the Settings picker onto the generic fallback that
// lists "auto".
func TestReplicateVideoDurationOptionsIntegerRange(t *testing.T) {
	doc := `{"latest_version":{"openapi_schema":{"components":{"schemas":{"Input":{"type":"object","properties":{
		"prompt":{"type":"string"},
		"duration":{"type":"integer","default":5,"minimum":2,"maximum":15}
	}}}}}}}`
	httpClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/v1/models/owner/model" {
			t.Fatalf("unexpected request: %s %s", req.Method, req.URL.Path)
		}
		return jsonResp(doc), nil
	})}
	root := t.TempDir()
	keyring.MockInit()
	t.Cleanup(func() { _ = clearReplicateAPIKey() })
	if err := saveReplicateAPIKey("k"); err != nil {
		t.Fatalf("saveReplicateAPIKey: %v", err)
	}
	opts := replicateVideoDurationOptions(context.Background(), httpClient, root, "owner/model")
	if len(opts) != 14 || opts[0] != "2" || opts[13] != "15" {
		t.Fatalf("opts = %v, want 2..15 (14 values, no \"auto\")", opts)
	}
}

// TestConversationModelOverridesReplicateRouting pins the overlay routing for
// the second providers: video/image models land on the effective provider's
// slots.
func TestConversationModelOverridesReplicateRouting(t *testing.T) {
	config := defaultAppConfig()
	config.Providers.Fal.VideoModel = "fal-ai/kling-video/v2/master/text-to-video"
	config.Providers.Fal.VideoImageModel = "fal-ai/kling-video/v2/master/image-to-video"
	config.Providers.Fal.VideoExtendModel = "fal-ai/veo3.1/extend-video"
	config.Providers.Fal.ImageEditModel = "fal-ai/flux/dev/image-to-image"
	config.Providers.Fal.UpscaleModel = "fal-ai/esrgan"

	overlaid, _, err := overlayModelOverrides(config, ChatRequest{}, ConversationModelOverrides{
		VideoProvider:     "replicate",
		VideoModel:        "wan-video/wan-2.5-t2v",
		VideoImageModel:   "wan-video/wan-2.5-i2v",
		VideoExtendModel:  "xai/grok-imagine-video-extension",
		ImageProvider:     "replicate",
		ImageModel:        "black-forest-labs/flux-schnell",
		ImageEditModel:    "black-forest-labs/flux-kontext-pro",
		UpscaleModel:      "nightmareai/real-esrgan",
		VideoUpscaleModel: "topazlabs/video-upscale",
		VideoRestyleModel: "kwaivgi/kling-v3-omni-video",
		VideoReframeModel: "luma/reframe-video",
	})
	if err != nil {
		t.Fatalf("overlayModelOverrides: %v", err)
	}
	if overlaid.Models.VideoProvider != "replicate" || overlaid.Models.ImageProvider != "replicate" {
		t.Fatalf("providers = %q/%q", overlaid.Models.ImageProvider, overlaid.Models.VideoProvider)
	}
	if overlaid.Providers.Replicate.VideoModel != "wan-video/wan-2.5-t2v" ||
		overlaid.Providers.Replicate.VideoImageModel != "wan-video/wan-2.5-i2v" ||
		overlaid.Providers.Replicate.Model != "black-forest-labs/flux-schnell" ||
		overlaid.Providers.Replicate.ImageEditModel != "black-forest-labs/flux-kontext-pro" {
		t.Fatalf("replicate slots = %+v", overlaid.Providers.Replicate)
	}
	// The fal slots are untouched — the override routed, not clobbered.
	if overlaid.Providers.Fal.VideoModel != "fal-ai/kling-video/v2/master/text-to-video" {
		t.Fatalf("fal VideoModel = %q, want untouched", overlaid.Providers.Fal.VideoModel)
	}
	// The upscale override follows the effective image provider too.
	if overlaid.Providers.Replicate.UpscaleModel != "nightmareai/real-esrgan" {
		t.Fatalf("replicate UpscaleModel = %q", overlaid.Providers.Replicate.UpscaleModel)
	}
	if overlaid.Providers.Fal.UpscaleModel != "fal-ai/esrgan" {
		t.Fatalf("fal UpscaleModel = %q, want untouched", overlaid.Providers.Fal.UpscaleModel)
	}
	// The video-upscale override follows the effective video provider.
	if overlaid.Providers.Replicate.VideoUpscaleModel != "topazlabs/video-upscale" {
		t.Fatalf("replicate VideoUpscaleModel = %q", overlaid.Providers.Replicate.VideoUpscaleModel)
	}
	// So does the extend override, and the fal slot is untouched.
	if overlaid.Providers.Replicate.VideoExtendModel != "xai/grok-imagine-video-extension" {
		t.Fatalf("replicate VideoExtendModel = %q", overlaid.Providers.Replicate.VideoExtendModel)
	}
	if overlaid.Providers.Fal.VideoExtendModel != "fal-ai/veo3.1/extend-video" {
		t.Fatalf("fal VideoExtendModel = %q, want untouched", overlaid.Providers.Fal.VideoExtendModel)
	}
	// So do the restyle and reframe overrides.
	if overlaid.Providers.Replicate.VideoRestyleModel != "kwaivgi/kling-v3-omni-video" {
		t.Fatalf("replicate VideoRestyleModel = %q", overlaid.Providers.Replicate.VideoRestyleModel)
	}
	if overlaid.Providers.Replicate.VideoReframeModel != "luma/reframe-video" {
		t.Fatalf("replicate VideoReframeModel = %q", overlaid.Providers.Replicate.VideoReframeModel)
	}
	// A video model override with no provider pin inherits the config's
	// effective provider (replicate, set above).
	overlaid, _, _ = overlayModelOverrides(overlaid, ChatRequest{}, ConversationModelOverrides{VideoModel: "owner/another-t2v"})
	if overlaid.Providers.Replicate.VideoModel != "owner/another-t2v" {
		t.Fatalf("inherited-provider VideoModel = %q", overlaid.Providers.Replicate.VideoModel)
	}
	if err := validateConversationModelOverrides(ConversationModelOverrides{VideoProvider: "runware"}); err == nil {
		t.Fatal("an unknown video provider must be rejected")
	}
	if err := validateConversationModelOverrides(ConversationModelOverrides{ImageProvider: "replicate"}); err != nil {
		t.Fatalf("replicate must be a valid image provider override: %v", err)
	}
}

// TestReplicateMediaRoutingHelpers pins the replicate branches of the shared
// default-model resolvers.
func TestReplicateMediaRoutingHelpers(t *testing.T) {
	config := defaultAppConfig()
	config.Models.ImageProvider = "replicate"
	config.Providers.Replicate.Model = "owner/gen"
	config.Providers.Replicate.ImageEditModel = "owner/edit"
	if got := resolveDefaultImageModel(config); got != "owner/gen" {
		t.Fatalf("resolveDefaultImageModel = %q", got)
	}
	if got := resolveDefaultImageEditModel(config); got != "owner/edit" {
		t.Fatalf("resolveDefaultImageEditModel = %q", got)
	}
	// Defaults apply when unset.
	config.Providers.Replicate.Model = ""
	config.Providers.Replicate.ImageEditModel = ""
	if got := resolveDefaultImageModel(config); got != defaultReplicateImageModel {
		t.Fatalf("default image model = %q", got)
	}
	if got := resolveDefaultImageEditModel(config); got != defaultReplicateImageEditModel {
		t.Fatalf("default edit model = %q", got)
	}
	// A seeded model passes the image gate on the replicate path.
	config.Providers.Replicate.Model = defaultReplicateImageModel
	if !imageGenerationConfigured(config) {
		t.Fatal("replicate image generation should be configured with a model")
	}
	// A multi-image data URL still decodes through the shared normalizer.
	if decoded, err := base64.StdEncoding.DecodeString(tinyPNG); err != nil || len(decoded) == 0 {
		t.Fatalf("tinyPNG fixture did not decode: %v", err)
	}
}

func TestResolveReplicateUpscaleInput(t *testing.T) {
	schema := parseReplicateFixtureSchema(t, `{"components":{"schemas":{"Input":{"type":"object","properties":{
		"image":{"type":"string"},
		"scale":{"type":"number"}
	}}}}}`)
	input, notices, err := resolveReplicateUpscaleInput(schema, ImageUpscaleRequest{
		Model: "nightmareai/real-esrgan",
		Image: "data:image/png;base64," + tinyPNG,
		Scale: 4,
	})
	if err != nil {
		t.Fatalf("resolveReplicateUpscaleInput: %v", err)
	}
	if len(notices) != 0 {
		t.Fatalf("notices = %v", notices)
	}
	if !strings.HasPrefix(input["image"].(string), "data:image/png;base64,") {
		t.Fatalf("image = %v", input["image"])
	}
	if input["scale"] != float64(4) {
		t.Fatalf("scale = %v (%T), want numeric 4", input["scale"], input["scale"])
	}

	// Nil schema falls back to the minimal body every upscaler accepts.
	input, _, err = resolveReplicateUpscaleInput(nil, ImageUpscaleRequest{Model: "owner/model", Image: "data:image/png;base64," + tinyPNG})
	if err != nil {
		t.Fatalf("nil-schema fallback: %v", err)
	}
	if input["scale"] != float64(2) {
		t.Fatalf("default scale = %v (%T), want numeric 2", input["scale"], input["scale"])
	}

	// A model with no image input is a hard error — the source frame is the
	// tool's entire purpose.
	noImageSchema := parseReplicateFixtureSchema(t, `{"components":{"schemas":{"Input":{"type":"object","properties":{"prompt":{"type":"string"}}}}}}`)
	_, _, err = resolveReplicateUpscaleInput(noImageSchema, ImageUpscaleRequest{Model: "owner/model", Image: "data:image/png;base64," + tinyPNG})
	if err == nil || !strings.Contains(err.Error(), "no source-image input") {
		t.Fatalf("err = %v, want the no-image-input refusal", err)
	}

	// An enum-restricted scale factor is dropped with a notice rather than 422ing.
	enumSchema := parseReplicateFixtureSchema(t, `{"components":{"schemas":{"Input":{"type":"object","properties":{
		"image":{"type":"string"},
		"scale":{"type":"integer","enum":[1,2]}
	}}}}}`)
	input, notices, err = resolveReplicateUpscaleInput(enumSchema, ImageUpscaleRequest{Model: "owner/model", Image: "data:image/png;base64," + tinyPNG, Scale: 4})
	if err != nil {
		t.Fatalf("enum-guard case: %v", err)
	}
	if _, exists := input["scale"]; exists {
		t.Fatal("an out-of-enum scale must not be sent")
	}
	if len(notices) != 1 || !strings.Contains(notices[0], "does not accept scale 4") {
		t.Fatalf("notices = %v, want the enum-drop notice", notices)
	}
}

func TestVideoUpscaleTierHelpers(t *testing.T) {
	for _, tc := range []struct {
		member string
		want   int
		ok     bool
	}{
		{"1080p", 1080, true},
		{"480P", 480, true},
		{"4k", 2160, true},
		{"8k", 4320, true},
		{"auto", 0, false},
		{"", 0, false},
	} {
		if got, ok := videoUpscaleTierPixels(tc.member); ok != tc.ok || got != tc.want {
			t.Errorf("videoUpscaleTierPixels(%q) = %d,%v want %d,%v", tc.member, got, ok, tc.want, tc.ok)
		}
	}
	for _, tc := range []struct {
		name    string
		members []string
		target  float64
		want    string
		capped  bool
	}{
		{"snap up to nearest", []string{"720p", "1080p", "4k"}, 960, "1080p", false},
		{"exact match", []string{"720p", "1080p", "4k"}, 2160, "4k", false},
		{"below lowest stays lowest", []string{"720p", "1080p", "4k"}, 360, "720p", false},
		{"above all caps at top", []string{"720p", "1080p", "4k"}, 4320, "4k", true},
		{"unparseable members ignored", []string{"auto", "1080p", "4k"}, 800, "1080p", false},
		{"nothing parseable", []string{"auto", "match"}, 800, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, capped, ok := videoUpscaleTierForTarget(tc.members, tc.target)
			if tc.want == "" {
				if ok {
					t.Fatalf("ok = true, want false")
				}
				return
			}
			if !ok || got != tc.want || capped != tc.capped {
				t.Fatalf("videoUpscaleTierForTarget(%v, %v) = %q,%v,%v want %q,%v,true", tc.members, tc.target, got, capped, ok, tc.want, tc.capped)
			}
		})
	}
}

func TestResolveReplicateVideoUpscaleInput(t *testing.T) {
	// Factor shape: the schema declares a native factor field.
	factorSchema := parseReplicateFixtureSchema(t, `{"components":{"schemas":{"Input":{"type":"object","properties":{
		"video":{"type":"string"},
		"scale_factor":{"type":"number"}
	}}}}}`)
	input, sourceKey, notices, err := resolveReplicateVideoUpscaleInput(factorSchema, VideoUpscaleRequest{
		Model: "owner/factor-upscaler",
		Video: "data:video/mp4;base64,QUFB",
		Scale: 4,
	})
	if err != nil {
		t.Fatalf("factor shape: %v", err)
	}
	if len(notices) != 0 {
		t.Fatalf("notices = %v", notices)
	}
	if sourceKey != "video" || input["video"] != "data:video/mp4;base64,QUFB" {
		t.Fatalf("source = %q/%v", sourceKey, input["video"])
	}
	if input["scale_factor"] != float64(4) {
		t.Fatalf("scale_factor = %v (%T)", input["scale_factor"], input["scale_factor"])
	}

	// Tier shape: a probeable inline clip derives the tier from its own frame
	// size × factor (1920×1080 × 2 → 2160 → "4k").
	tierSchema := parseReplicateFixtureSchema(t, `{"components":{"schemas":{"Input":{"type":"object","properties":{
		"video":{"type":"string"},
		"target_resolution":{"type":"string","enum":["720p","1080p","4k"]}
	}}}}}`)
	inline1080p := "data:video/mp4;base64," + base64.StdEncoding.EncodeToString(mp4FixtureWithVideoTrack(1920, 1080))
	input, _, notices, err = resolveReplicateVideoUpscaleInput(tierSchema, VideoUpscaleRequest{
		Model: "topazlabs/video-upscale",
		Video: inline1080p,
		Scale: 2,
	})
	if err != nil {
		t.Fatalf("tier shape: %v", err)
	}
	if len(notices) != 0 {
		t.Fatalf("notices = %v, want none for an exact tier match", notices)
	}
	if input["target_resolution"] != "4k" {
		t.Fatalf("target_resolution = %v, want 4k", input["target_resolution"])
	}

	// A factor past the top tier caps with a notice.
	input, _, notices, err = resolveReplicateVideoUpscaleInput(tierSchema, VideoUpscaleRequest{
		Model: "topazlabs/video-upscale",
		Video: inline1080p,
		Scale: 4,
	})
	if err != nil {
		t.Fatalf("tier cap: %v", err)
	}
	if input["target_resolution"] != "4k" || len(notices) != 1 || !strings.Contains(notices[0], "capping") {
		t.Fatalf("input = %v, notices = %v, want the 4k cap notice", input, notices)
	}

	// A hosted-URL source can't be probed for the tier — degrade with a
	// notice, never guess.
	input, _, notices, err = resolveReplicateVideoUpscaleInput(tierSchema, VideoUpscaleRequest{
		Model: "topazlabs/video-upscale",
		Video: "https://cdn.example/clip.mp4",
		Scale: 2,
	})
	if err != nil {
		t.Fatalf("tier unprobeable: %v", err)
	}
	if _, exists := input["target_resolution"]; exists {
		t.Fatal("an underivable tier must not be sent")
	}
	if len(notices) != 1 || !strings.Contains(notices[0], "couldn't be read") {
		t.Fatalf("notices = %v, want the unprobeable notice", notices)
	}

	// Nil schema falls back to the minimal factor body.
	input, sourceKey, _, err = resolveReplicateVideoUpscaleInput(nil, VideoUpscaleRequest{Model: "owner/model", Video: "data:video/mp4;base64,QUFB"})
	if err != nil {
		t.Fatalf("nil schema: %v", err)
	}
	if sourceKey != "video" || input["scale"] != float64(2) {
		t.Fatalf("nil-schema fallback = %q/%v", sourceKey, input)
	}

	// A model with no video input is a hard error.
	noVideoSchema := parseReplicateFixtureSchema(t, `{"components":{"schemas":{"Input":{"type":"object","properties":{"prompt":{"type":"string"}}}}}}`)
	_, _, _, err = resolveReplicateVideoUpscaleInput(noVideoSchema, VideoUpscaleRequest{Model: "owner/model", Video: "data:video/mp4;base64,QUFB"})
	if err == nil || !strings.Contains(err.Error(), "no video input") {
		t.Fatalf("err = %v, want the no-video-input refusal", err)
	}

	// Neither a factor nor a tier field: degrade with a notice.
	neitherSchema := parseReplicateFixtureSchema(t, `{"components":{"schemas":{"Input":{"type":"object","properties":{"video":{"type":"string"}}}}}}`)
	_, _, notices, err = resolveReplicateVideoUpscaleInput(neitherSchema, VideoUpscaleRequest{Model: "owner/model", Video: "data:video/mp4;base64,QUFB"})
	if err != nil {
		t.Fatalf("neither shape: %v", err)
	}
	if len(notices) != 1 || !strings.Contains(notices[0], "no scale control") {
		t.Fatalf("notices = %v", notices)
	}
}

// TestReplicateVideoUpscaleListerFilter pins the ai-enhance-videos partition:
// upscaler ids stay, the collection's restoration/interpolation entries go.
func TestReplicateVideoUpscaleListerFilter(t *testing.T) {
	keep := []string{
		"topazlabs/video-upscale",
		"philz1337x/crystal-video-upscaler",
		"lucataco/real-esrgan-video",
		"tencentarc/animesr",
		"pollinations/real-basicvsr-video-superresolution",
	}
	for _, id := range keep {
		if !isReplicateVideoUpscaleModel(ReplicateModel{ID: id}) {
			t.Errorf("isReplicateVideoUpscaleModel(%q) = false, want true", id)
		}
	}
	for _, id := range []string{
		"pbarker/gfpgan-video",
		"arielreplicate/deoldify_video",
		"google/film-frame-interpolation",
		"xai/grok-imagine-video-extension",
	} {
		if isReplicateVideoUpscaleModel(ReplicateModel{ID: id}) {
			t.Errorf("isReplicateVideoUpscaleModel(%q) = true, want false", id)
		}
	}
}

func TestResolveReplicateVideoRestyleInput(t *testing.T) {
	schema := parseReplicateFixtureSchema(t, `{"components":{"schemas":{"Input":{"type":"object","properties":{
		"prompt":{"type":"string"},
		"video":{"type":"string"},
		"image_urls":{"type":"array","items":{"type":"string"},"maxItems":7},
		"negative_prompt":{"type":"string"},
		"resolution":{"type":"string","enum":["720p","1080p"]}
	}}}}}`)
	input, notices, err := resolveReplicateVideoRestyleInput(schema, VideoRestyleRequest{
		Model:          "kwaivgi/kling-v3-omni-video",
		Video:          "data:video/mp4;base64,QUFB",
		Prompt:         "anime style",
		Images:         []string{"data:image/png;base64," + tinyPNG, "data:image/png;base64," + tinyPNG},
		NegativePrompt: "blur",
		Resolution:     "1080p",
	})
	if err != nil {
		t.Fatalf("restyle: %v", err)
	}
	if len(notices) != 0 {
		t.Fatalf("notices = %v", notices)
	}
	if input["prompt"] != "anime style" || input["negative_prompt"] != "blur" || input["resolution"] != "1080p" {
		t.Fatalf("input = %v", input)
	}
	refs, ok := input["image_urls"].([]any)
	if !ok || len(refs) != 2 {
		t.Fatalf("image_urls = %v, want both references", input["image_urls"])
	}

	// A mode enum listing "reimagine" gets it by default (luma/modify-video).
	modeSchema := parseReplicateFixtureSchema(t, `{"components":{"schemas":{"Input":{"type":"object","properties":{
		"prompt":{"type":"string"},
		"video":{"type":"string"},
		"mode":{"type":"string","enum":["adhere","flex","reimagine"]}
	}}}}}`)
	input, _, err = resolveReplicateVideoRestyleInput(modeSchema, VideoRestyleRequest{
		Model:  "luma/modify-video",
		Video:  "data:video/mp4;base64,QUFB",
		Prompt: "claymation",
	})
	if err != nil {
		t.Fatalf("mode: %v", err)
	}
	if input["mode"] != "reimagine" {
		t.Fatalf("mode = %v, want the guided reimagine default", input["mode"])
	}

	// References on a model without an image input drop with a notice.
	noRefSchema := parseReplicateFixtureSchema(t, `{"components":{"schemas":{"Input":{"type":"object","properties":{
		"prompt":{"type":"string"},
		"video":{"type":"string"}
	}}}}}`)
	input, notices, err = resolveReplicateVideoRestyleInput(noRefSchema, VideoRestyleRequest{
		Model:  "owner/model",
		Video:  "data:video/mp4;base64,QUFB",
		Prompt: "x",
		Images: []string{"data:image/png;base64," + tinyPNG},
	})
	if err != nil {
		t.Fatalf("no-ref: %v", err)
	}
	if len(notices) != 1 || !strings.Contains(notices[0], "reference-image input") {
		t.Fatalf("notices = %v", notices)
	}

	// No video input is a hard error; nil schema falls back minimally.
	noVideoSchema := parseReplicateFixtureSchema(t, `{"components":{"schemas":{"Input":{"type":"object","properties":{"prompt":{"type":"string"}}}}}}`)
	if _, _, err := resolveReplicateVideoRestyleInput(noVideoSchema, VideoRestyleRequest{Model: "owner/model", Video: "data:video/mp4;base64,QUFB", Prompt: "x"}); err == nil || !strings.Contains(err.Error(), "no video input") {
		t.Fatalf("err = %v, want the no-video-input refusal", err)
	}
	input, _, err = resolveReplicateVideoRestyleInput(nil, VideoRestyleRequest{Model: "owner/model", Video: "data:video/mp4;base64,QUFB", Prompt: "x"})
	if err != nil || input["prompt"] != "x" || input["video"] != "data:video/mp4;base64,QUFB" {
		t.Fatalf("nil-schema fallback = %v, %v", input, err)
	}
}

func TestResolveReplicateVideoReframeInput(t *testing.T) {
	schema := parseReplicateFixtureSchema(t, `{"components":{"schemas":{"Input":{"type":"object","properties":{
		"video":{"type":"string"},
		"aspect_ratio":{"type":"string","enum":["16:9","9:16","1:1","4:5"]},
		"resolution":{"type":"string","enum":["720p"]}
	}}}}}`)
	input, notices, err := resolveReplicateVideoReframeInput(schema, VideoReframeRequest{
		Model:       "luma/reframe-video",
		Video:       "data:video/mp4;base64,QUFB",
		AspectRatio: "9:16",
		Resolution:  "720p",
	})
	if err != nil {
		t.Fatalf("reframe: %v", err)
	}
	if len(notices) != 0 || input["aspect_ratio"] != "9:16" || input["resolution"] != "720p" {
		t.Fatalf("input = %v, notices = %v", input, notices)
	}

	// Out-of-enum ratio drops with a notice.
	input, notices, err = resolveReplicateVideoReframeInput(schema, VideoReframeRequest{
		Model:       "luma/reframe-video",
		Video:       "data:video/mp4;base64,QUFB",
		AspectRatio: "21:9",
	})
	if err != nil {
		t.Fatalf("enum: %v", err)
	}
	if _, exists := input["aspect_ratio"]; exists || len(notices) != 1 {
		t.Fatalf("input = %v, notices = %v, want the drop", input, notices)
	}

	// No video input is a hard error; nil schema falls back minimally.
	noVideoSchema := parseReplicateFixtureSchema(t, `{"components":{"schemas":{"Input":{"type":"object","properties":{"prompt":{"type":"string"}}}}}}`)
	if _, _, err := resolveReplicateVideoReframeInput(noVideoSchema, VideoReframeRequest{Model: "owner/model", Video: "data:video/mp4;base64,QUFB"}); err == nil || !strings.Contains(err.Error(), "no video input") {
		t.Fatalf("err = %v, want the refusal", err)
	}
	input, _, err = resolveReplicateVideoReframeInput(nil, VideoReframeRequest{Model: "owner/model", Video: "data:video/mp4;base64,QUFB", AspectRatio: "9:16"})
	if err != nil || input["aspect_ratio"] != "9:16" {
		t.Fatalf("nil-schema fallback = %v, %v", input, err)
	}
}

// TestReplicateVideoEditingListerFilters pins the video-editing collection's
// two partitions.
func TestReplicateVideoEditingListerFilters(t *testing.T) {
	for _, id := range []string{
		"kwaivgi/kling-v3-omni-video",
		"wan-video/wan-2.7-videoedit",
		"luma/modify-video",
	} {
		if !isReplicateVideoRestyleModel(ReplicateModel{ID: id}) {
			t.Errorf("isReplicateVideoRestyleModel(%q) = false, want true", id)
		}
	}
	for _, id := range []string{
		"luma/reframe-video",
		"xai/grok-imagine-video-extension",
		"zsxkib/mmaudio",
		"heygen/lipsync-speed",
		"lucataco/trim-video",
		"kwaivgi/kling-v3-video",
	} {
		if isReplicateVideoRestyleModel(ReplicateModel{ID: id}) {
			t.Errorf("isReplicateVideoRestyleModel(%q) = true, want false", id)
		}
	}
	if !isReplicateVideoReframeModel(ReplicateModel{ID: "luma/reframe-video"}) {
		t.Error("isReplicateVideoReframeModel(luma/reframe-video) = false, want true")
	}
	if isReplicateVideoReframeModel(ReplicateModel{ID: "lucataco/trim-video"}) {
		t.Error("isReplicateVideoReframeModel(lucataco/trim-video) = true, want false")
	}
}
