package main

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
)

// TestUpscaleVideoToolRequiresAttachedVideo verifies the tool errors when no
// source video is attached, since there is nothing to upscale.
func TestUpscaleVideoToolRequiresAttachedVideo(t *testing.T) {
	tools := HarnessToolExecutionContext{
		Config: AppConfig{Models: ConfigModels{ImageProvider: "fal"}},
		UpscaleVideo: func(context.Context, VideoUpscaleRequest) (GeneratedVideo, error) {
			t.Fatal("UpscaleVideo must not be called without an attached video")
			return GeneratedVideo{}, nil
		},
	}
	def := videoUpscaleToolDefinition()
	_, _, err := def.Execute(t.Context(), tools, HarnessToolCall{Scale: "2x"})
	if err == nil || !strings.Contains(err.Error(), "attached video") {
		t.Fatalf("err = %v, want an error mentioning an attached video is required", err)
	}
}

// TestUpscaleVideoToolDefaultsAndScaleMapping checks the default model, the 2x
// default, the 4x override, the forwarded source video, and the result shape
// (a ToolVideoResult carrying one temp-file video, like generate_video).
func TestUpscaleVideoToolDefaultsAndScaleMapping(t *testing.T) {
	tests := []struct {
		name       string
		scale      string
		wantScale  float64
		wantSuffix string
	}{
		{"default is 2x", "", 2.0, "2x with"},
		{"explicit 2x", "2x", 2.0, "2x with"},
		{"explicit 4x", "4x", 4.0, "4x with"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var captured VideoUpscaleRequest
			tools := HarnessToolExecutionContext{
				Config:        AppConfig{Models: ConfigModels{ImageProvider: "fal"}},
				AttachedVideo: "data:video/mp4;base64,AAA",
				UpscaleVideo: func(_ context.Context, req VideoUpscaleRequest) (GeneratedVideo, error) {
					captured = req
					return GeneratedVideo{Data: []byte("fake-mp4"), MimeType: "video/mp4", SourceURL: "https://fal.example/v.mp4"}, nil
				},
			}
			def := videoUpscaleToolDefinition()
			result, summary, err := def.Execute(t.Context(), tools, HarnessToolCall{Scale: tc.scale})
			if err != nil {
				t.Fatalf("Execute returned error: %v", err)
			}
			if captured.Video != "data:video/mp4;base64,AAA" {
				t.Errorf("captured video = %q, want the attached source forwarded", captured.Video)
			}
			if captured.Model != defaultFalVideoUpscaleModel {
				t.Errorf("model = %q, want video upscale default %q", captured.Model, defaultFalVideoUpscaleModel)
			}
			if captured.Scale != tc.wantScale {
				t.Errorf("scale = %v, want %v", captured.Scale, tc.wantScale)
			}
			if !strings.Contains(summary, tc.wantSuffix) {
				t.Errorf("summary = %q, want it to mention %q", summary, tc.wantSuffix)
			}
			typed, ok := result.(ToolVideoResult)
			if !ok || typed.Count != 1 || len(typed.Videos) != 1 {
				t.Fatalf("result = %+v, want a ToolVideoResult with one video", result)
			}
			if typed.Videos[0].TempPath == "" || typed.Videos[0].MimeType != "video/mp4" {
				t.Errorf("video file = %+v, want a staged temp file with the mime type", typed.Videos[0])
			}
			t.Cleanup(func() { _ = os.Remove(typed.Videos[0].TempPath) })
		})
	}
}

// TestUpscaleVideoToolHonorsModelOverride verifies precedence: a call-supplied
// model wins over the configured VideoUpscaleModel, which wins over the const
// default.
func TestUpscaleVideoToolHonorsModelOverride(t *testing.T) {
	var captured VideoUpscaleRequest
	tools := HarnessToolExecutionContext{
		Config:        AppConfig{Providers: ConfigProviders{Fal: ConfigFal{VideoUpscaleModel: "fal-ai/topaz/upscale/video"}}},
		AttachedVideo: "data:video/mp4;base64,AAA",
		UpscaleVideo: func(_ context.Context, req VideoUpscaleRequest) (GeneratedVideo, error) {
			captured = req
			return GeneratedVideo{Data: []byte("fake-mp4"), MimeType: "video/mp4"}, nil
		},
	}
	def := videoUpscaleToolDefinition()
	result, _, err := def.Execute(t.Context(), tools, HarnessToolCall{Model: "fal-ai/seedvr/upscale/video"})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if captured.Model != "fal-ai/seedvr/upscale/video" {
		t.Errorf("model = %q, want the call override", captured.Model)
	}
	if typed, ok := result.(ToolVideoResult); ok {
		t.Cleanup(func() { _ = os.Remove(typed.Videos[0].TempPath) })
	}
	// The configured model must also win when the call doesn't override it.
	captured = VideoUpscaleRequest{}
	if _, _, err := def.Execute(t.Context(), tools, HarnessToolCall{}); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if captured.Model != "fal-ai/topaz/upscale/video" {
		t.Errorf("model = %q, want the configured VideoUpscaleModel", captured.Model)
	}
}

// TestVideoUpscaleConfiguredAndResolver covers the gating (available whenever a
// fal.ai key is configured, like transcribe_audio and lip_sync — the default
// endpoint always applies) and the resolver's configured→default fallback.
func TestVideoUpscaleConfiguredAndResolver(t *testing.T) {
	keyring.MockInit()
	if err := saveFalAPIKey("fal-test-key"); err != nil {
		t.Fatalf("saveFalAPIKey: %v", err)
	}
	t.Cleanup(func() { _ = clearFalAPIKey() })

	if !videoUpscaleConfigured(AppConfig{}) {
		t.Error("videoUpscaleConfigured(with key) = false, want true")
	}
	configured := resolveDefaultVideoUpscaleModel(AppConfig{
		Providers: ConfigProviders{Fal: ConfigFal{VideoUpscaleModel: "clarityai/crystal-video-upscaler"}},
	})
	if configured != "clarityai/crystal-video-upscaler" {
		t.Errorf("resolveDefaultVideoUpscaleModel(configured) = %q, want the configured endpoint", configured)
	}
	if fallback := resolveDefaultVideoUpscaleModel(AppConfig{}); fallback != defaultFalVideoUpscaleModel {
		t.Errorf("resolveDefaultVideoUpscaleModel(default) = %q, want %q", fallback, defaultFalVideoUpscaleModel)
	}

	// With the key cleared the tool is not offered.
	if err := clearFalAPIKey(); err != nil {
		t.Fatalf("clearFalAPIKey: %v", err)
	}
	if videoUpscaleConfigured(AppConfig{}) {
		t.Error("videoUpscaleConfigured(no key) = true, want false")
	}
}

// videoUpscaleSchema builds a synthetic upscaler schema declaring video_url plus
// one factor field, exercising the synonym table's per-endpoint names: fal's
// video-upscaler declares "scale", crystal "scale_factor", topaz
// "upscale_factor". factorField is the native name; factorType is its schema
// type (topaz declares number; the others float too — coerceVideoValue must
// emit a JSON number either way).
func videoUpscaleSchema(factorField, factorType string) *ModelInputSchema {
	return &ModelInputSchema{
		Properties: map[string]SchemaProperty{
			"video_url": {Name: "video_url", Kind: schemaScalar},
			factorField: {Name: factorField, Kind: schemaScalar, Type: factorType},
		},
		order: []string{"video_url", factorField},
	}
}

// TestResolveVideoUpscaleBody covers the synonym mapping (scale / scale_factor /
// upscale_factor all receive the canonical factor), the missing-scale notice,
// the missing-video-input hard error, and the nil-schema legacy fallback.
func TestResolveVideoUpscaleBody(t *testing.T) {
	t.Run("maps each endpoint's native factor name", func(t *testing.T) {
		for _, tc := range []struct{ field, typ string }{
			{"scale", "number"},
			{"scale_factor", "number"},
			{"upscale_factor", "number"},
		} {
			body, notices, err := resolveVideoUpscaleBody(videoUpscaleSchema(tc.field, tc.typ),
				VideoUpscaleRequest{Model: "acme/upscaler", Video: "data:video/mp4;base64,AAA", Scale: 2},
				builtinFalOverrides())
			if err != nil {
				t.Fatalf("%s: resolveVideoUpscaleBody error: %v", tc.field, err)
			}
			if got, ok := body[tc.field].(float64); !ok || got != 2 {
				t.Errorf("%s: body[%q] = %v (%T), want the number 2", tc.field, tc.field, body[tc.field], body[tc.field])
			}
			if body["video_url"] != "data:video/mp4;base64,AAA" {
				t.Errorf("%s: video_url = %v, want the attached clip", tc.field, body["video_url"])
			}
			if len(notices) != 0 {
				t.Errorf("%s: notices = %v, want none", tc.field, notices)
			}
		}
	})
	t.Run("integer-typed factor is coerced", func(t *testing.T) {
		body, _, err := resolveVideoUpscaleBody(videoUpscaleSchema("scale", "integer"),
			VideoUpscaleRequest{Model: "acme/upscaler", Video: "https://example.com/v.mp4", Scale: 4},
			builtinFalOverrides())
		if err != nil {
			t.Fatalf("resolveVideoUpscaleBody error: %v", err)
		}
		if got, ok := body["scale"].(int); !ok || got != 4 {
			t.Errorf("body[scale] = %v (%T), want the integer 4", body["scale"], body["scale"])
		}
	})
	t.Run("missing scale input degrades with a notice", func(t *testing.T) {
		schema := &ModelInputSchema{
			Properties: map[string]SchemaProperty{
				"video_url": {Name: "video_url", Kind: schemaScalar},
			},
			order: []string{"video_url"},
		}
		body, notices, err := resolveVideoUpscaleBody(schema,
			VideoUpscaleRequest{Model: "acme/upscaler", Video: "https://example.com/v.mp4", Scale: 2},
			builtinFalOverrides())
		if err != nil {
			t.Fatalf("resolveVideoUpscaleBody error: %v", err)
		}
		if _, present := body["scale"]; present {
			t.Errorf("body[scale] = %v, want it omitted when the model declares no factor input", body["scale"])
		}
		if len(notices) != 1 || !strings.Contains(notices[0], "no scale control") {
			t.Fatalf("notices = %v, want one \"no scale control\" notice", notices)
		}
	})
	t.Run("missing video input is a hard error", func(t *testing.T) {
		schema := &ModelInputSchema{
			Properties: map[string]SchemaProperty{
				"image_url": {Name: "image_url", Kind: schemaScalar},
			},
			order: []string{"image_url"},
		}
		_, _, err := resolveVideoUpscaleBody(schema,
			VideoUpscaleRequest{Model: "fal-ai/esrgan", Video: "https://example.com/v.mp4", Scale: 2},
			builtinFalOverrides())
		if err == nil || !strings.Contains(err.Error(), "no video input") {
			t.Fatalf("err = %v, want a hard error naming the missing video input", err)
		}
	})
	t.Run("nil schema falls back to default field names", func(t *testing.T) {
		body, notices, err := resolveVideoUpscaleBody(nil,
			VideoUpscaleRequest{Model: "acme/upscaler", Video: "data:video/mp4;base64,AAA", Scale: 4},
			builtinFalOverrides())
		if err != nil {
			t.Fatalf("resolveVideoUpscaleBody error: %v", err)
		}
		if body["video_url"] != "data:video/mp4;base64,AAA" || body["scale"] != 4.0 {
			t.Fatalf("body = %v, want the legacy {video_url, scale} fallback", body)
		}
		if len(notices) != 1 {
			t.Fatalf("notices = %v, want the schema-unavailable notice", notices)
		}
	})
}

// TestUpscaleVideoResultForwardFeedsAttachment verifies the upscale result rides
// the shared ToolVideoResult forward-feed: an upscaled clip becomes the
// AttachedVideo a later tool in the same batch (e.g. lip_sync) consumes.
func TestUpscaleVideoResultForwardFeedsAttachment(t *testing.T) {
	result := HarnessToolResult{Status: "completed", Result: ToolVideoResult{
		Videos: []ToolVideoFile{{TempPath: "does-not-exist.mp4"}},
	}}
	if media := forwardableMediaFromResults([]HarnessToolResult{result}); media != nil {
		t.Fatalf("media = %+v, want nil — an unreadable temp file must not forward", media)
	}
}
