package main

import (
	"context"
	"encoding/base64"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
)

// TestReframeVideoToolRequiresAttachedVideo verifies the tool errors when no
// source video is attached, since there is nothing to reframe.
func TestReframeVideoToolRequiresAttachedVideo(t *testing.T) {
	tools := HarnessToolExecutionContext{
		Config: AppConfig{Models: ConfigModels{ImageProvider: "fal"}},
		ReframeVideo: func(context.Context, VideoReframeRequest) (GeneratedVideo, error) {
			t.Fatal("ReframeVideo must not be called without an attached video")
			return GeneratedVideo{}, nil
		},
	}
	def := videoReframeToolDefinition(AppConfig{})
	_, _, err := def.Execute(t.Context(), tools, HarnessToolCall{AspectRatio: "9:16"})
	if err == nil || !strings.Contains(err.Error(), "attached video") {
		t.Fatalf("err = %v, want an error mentioning an attached video is required", err)
	}
}

// TestReframeVideoValidate pins the call-side validation: the target aspect
// ratio is the tool's entire purpose, so an omitted one is a plan correction
// rather than a runtime guess.
func TestReframeVideoValidate(t *testing.T) {
	def := videoReframeToolDefinition(AppConfig{})
	if errors := def.Validate("toolCalls[0]", HarnessToolCall{}); len(errors) == 0 || !strings.Contains(errors[0], ".aspectRatio is required") {
		t.Errorf("reframe without an aspect ratio = %v, want the required error", errors)
	}
	if errors := def.Validate("toolCalls[0]", HarnessToolCall{AspectRatio: "9:16", Resolution: "720p"}); len(errors) != 0 {
		t.Errorf("valid reframe = %v, want none", errors)
	}
}

// TestReframeVideoToolDefaultsAndMapping checks the default model, the
// forwarded source video, aspect ratio, and resolution (empty lets the model
// apply its own tier), and the result shape (a ToolVideoResult carrying one
// temp-file video, like generate_video).
func TestReframeVideoToolDefaultsAndMapping(t *testing.T) {
	tests := []struct {
		name           string
		call           HarnessToolCall
		wantAspect     string
		wantResolution string
	}{
		{"aspect only", HarnessToolCall{AspectRatio: "9:16"}, "9:16", ""},
		{"aspect plus resolution", HarnessToolCall{AspectRatio: "9:16", Resolution: "720p"}, "9:16", "720p"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var captured VideoReframeRequest
			tools := HarnessToolExecutionContext{
				Config:         AppConfig{Models: ConfigModels{ImageProvider: "fal"}},
				AttachedVideos: []string{"data:video/mp4;base64,AAA"},
				ReframeVideo: func(_ context.Context, req VideoReframeRequest) (GeneratedVideo, error) {
					captured = req
					return GeneratedVideo{Data: []byte("fake-mp4"), MimeType: "video/mp4", SourceURL: "https://fal.example/v.mp4"}, nil
				},
			}
			def := videoReframeToolDefinition(AppConfig{})
			result, summary, err := def.Execute(t.Context(), tools, tc.call)
			if err != nil {
				t.Fatalf("Execute returned error: %v", err)
			}
			if captured.Video != "data:video/mp4;base64,AAA" {
				t.Errorf("captured video = %q, want the attached source forwarded", captured.Video)
			}
			if captured.Model != defaultFalVideoReframeModel {
				t.Errorf("model = %q, want video reframe default %q", captured.Model, defaultFalVideoReframeModel)
			}
			if captured.AspectRatio != tc.wantAspect {
				t.Errorf("aspect ratio = %q, want %q", captured.AspectRatio, tc.wantAspect)
			}
			if captured.Resolution != tc.wantResolution {
				t.Errorf("resolution = %q, want %q", captured.Resolution, tc.wantResolution)
			}
			if !strings.Contains(summary, "9:16") {
				t.Errorf("summary = %q, want it to mention the target ratio", summary)
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

// TestReframeVideoToolHonorsModelOverride verifies precedence: a call-supplied
// model wins over the configured VideoReframeModel, which wins over the const
// default.
func TestReframeVideoToolHonorsModelOverride(t *testing.T) {
	var captured VideoReframeRequest
	tools := HarnessToolExecutionContext{
		Config:         AppConfig{Providers: ConfigProviders{Fal: ConfigFal{VideoReframeModel: "fal-ai/luma-dream-machine/ray-2/reframe"}}},
		AttachedVideos: []string{"data:video/mp4;base64,AAA"},
		ReframeVideo: func(_ context.Context, req VideoReframeRequest) (GeneratedVideo, error) {
			captured = req
			return GeneratedVideo{Data: []byte("fake-mp4"), MimeType: "video/mp4"}, nil
		},
	}
	def := videoReframeToolDefinition(AppConfig{})
	result, _, err := def.Execute(t.Context(), tools, HarnessToolCall{AspectRatio: "9:16", Model: "luma/agent/ray/v3.2/reframe"})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if captured.Model != "luma/agent/ray/v3.2/reframe" {
		t.Errorf("model = %q, want the call override", captured.Model)
	}
	if typed, ok := result.(ToolVideoResult); ok {
		t.Cleanup(func() { _ = os.Remove(typed.Videos[0].TempPath) })
	}
	// The configured model must also win when the call doesn't override it.
	captured = VideoReframeRequest{}
	if _, _, err := def.Execute(t.Context(), tools, HarnessToolCall{AspectRatio: "9:16"}); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if captured.Model != "fal-ai/luma-dream-machine/ray-2/reframe" {
		t.Errorf("model = %q, want the configured VideoReframeModel", captured.Model)
	}
}

// TestVideoReframeConfiguredAndResolver covers the gating (available whenever a
// fal.ai key is configured, like upscale_video — the default endpoint always
// applies) and the resolver's configured→default fallback.
func TestVideoReframeConfiguredAndResolver(t *testing.T) {
	keyring.MockInit()
	if err := saveFalAPIKey("fal-test-key"); err != nil {
		t.Fatalf("saveFalAPIKey: %v", err)
	}
	t.Cleanup(func() { _ = clearFalAPIKey() })

	if !videoReframeConfigured(AppConfig{}) {
		t.Error("videoReframeConfigured(with key) = false, want true")
	}
	configured := resolveDefaultVideoReframeModel(AppConfig{
		Providers: ConfigProviders{Fal: ConfigFal{VideoReframeModel: "fal-ai/luma-dream-machine/ray-2/reframe"}},
	})
	if configured != "fal-ai/luma-dream-machine/ray-2/reframe" {
		t.Errorf("resolveDefaultVideoReframeModel(configured) = %q, want the configured endpoint", configured)
	}
	if fallback := resolveDefaultVideoReframeModel(AppConfig{}); fallback != defaultFalVideoReframeModel {
		t.Errorf("resolveDefaultVideoReframeModel(default) = %q, want %q", fallback, defaultFalVideoReframeModel)
	}

	// With the key cleared the tool is not offered.
	if err := clearFalAPIKey(); err != nil {
		t.Fatalf("clearFalAPIKey: %v", err)
	}
	if videoReframeConfigured(AppConfig{}) {
		t.Error("videoReframeConfigured(no key) = true, want false")
	}
}

// videoReframeSchema builds a synthetic reframe schema declaring video_url,
// aspect_ratio, and resolution with the enums fal's endpoints publish (LTX-2.3
// lists 1:1/4:5/5:4/9:16/16:9 and 720p/1080p).
func videoReframeSchema(aspectEnum, resolutionEnum []string, omit ...string) *ModelInputSchema {
	schema := &ModelInputSchema{
		Properties: map[string]SchemaProperty{},
	}
	schema.Properties["video_url"] = SchemaProperty{Name: "video_url", Kind: schemaScalar}
	schema.order = []string{"video_url"}
	if !contains(omit, "aspect_ratio") {
		schema.Properties["aspect_ratio"] = SchemaProperty{Name: "aspect_ratio", Kind: schemaScalar, Enum: aspectEnum}
		schema.order = append(schema.order, "aspect_ratio")
	}
	if !contains(omit, "resolution") {
		schema.Properties["resolution"] = SchemaProperty{Name: "resolution", Kind: schemaScalar, Enum: resolutionEnum}
		schema.order = append(schema.order, "resolution")
	}
	return schema
}

// TestResolveVideoReframeBody covers the canonical→native mapping, the two
// fatal guards (no video input; no aspect-ratio input), the out-of-enum ratio
// error naming the model's supported set, the degradable resolution notice,
// and the nil-schema legacy fallback.
func TestResolveVideoReframeBody(t *testing.T) {
	ltxEnum := []string{"1:1", "4:5", "5:4", "9:16", "16:9"}
	t.Run("maps video, canonicalized ratio, and tier", func(t *testing.T) {
		body, notices, err := resolveVideoReframeBody(videoReframeSchema(ltxEnum, []string{"720p", "1080p"}),
			VideoReframeRequest{Model: "fal-ai/ltx-2.3/reframe", Video: "data:video/mp4;base64,AAA", AspectRatio: "9:16", Resolution: "1080p"},
			builtinFalOverrides())
		if err != nil {
			t.Fatalf("resolveVideoReframeBody error: %v", err)
		}
		if body["video_url"] != "data:video/mp4;base64,AAA" {
			t.Errorf("video_url = %v, want the attached clip", body["video_url"])
		}
		if body["aspect_ratio"] != "9:16" {
			t.Errorf("aspect_ratio = %v, want the requested ratio", body["aspect_ratio"])
		}
		if body["resolution"] != "1080p" {
			t.Errorf("resolution = %v, want the requested tier", body["resolution"])
		}
		if len(notices) != 0 {
			t.Errorf("notices = %v, want none", notices)
		}
	})
	t.Run("out-of-enum ratio is a hard error naming the supported set", func(t *testing.T) {
		_, _, err := resolveVideoReframeBody(videoReframeSchema(ltxEnum, nil),
			VideoReframeRequest{Model: "fal-ai/ltx-2.3/reframe", Video: "https://example.com/v.mp4", AspectRatio: "21:9"},
			builtinFalOverrides())
		if err == nil || !strings.Contains(err.Error(), "21:9") || !strings.Contains(err.Error(), "16:9") {
			t.Fatalf("err = %v, want a hard error naming the requested ratio and the supported set", err)
		}
	})
	t.Run("missing aspect-ratio input is a hard error", func(t *testing.T) {
		_, _, err := resolveVideoReframeBody(videoReframeSchema(nil, nil, "aspect_ratio"),
			VideoReframeRequest{Model: "acme/reframe", Video: "https://example.com/v.mp4", AspectRatio: "9:16"},
			builtinFalOverrides())
		if err == nil || !strings.Contains(err.Error(), "no aspect-ratio input") {
			t.Fatalf("err = %v, want a hard error naming the missing aspect-ratio input", err)
		}
	})
	t.Run("missing video input is a hard error", func(t *testing.T) {
		schema := &ModelInputSchema{
			Properties: map[string]SchemaProperty{
				"aspect_ratio": {Name: "aspect_ratio", Kind: schemaScalar, Enum: ltxEnum},
			},
			order: []string{"aspect_ratio"},
		}
		_, _, err := resolveVideoReframeBody(schema,
			VideoReframeRequest{Model: "fal-ai/esrgan", Video: "https://example.com/v.mp4", AspectRatio: "9:16"},
			builtinFalOverrides())
		if err == nil || !strings.Contains(err.Error(), "no video input") {
			t.Fatalf("err = %v, want a hard error naming the missing video input", err)
		}
	})
	t.Run("unsupported resolution degrades with a notice", func(t *testing.T) {
		body, notices, err := resolveVideoReframeBody(videoReframeSchema(ltxEnum, []string{"720p", "1080p"}),
			VideoReframeRequest{Model: "fal-ai/ltx-2.3/reframe", Video: "https://example.com/v.mp4", AspectRatio: "9:16", Resolution: "4k"},
			builtinFalOverrides())
		if err != nil {
			t.Fatalf("resolveVideoReframeBody error: %v", err)
		}
		if _, present := body["resolution"]; present {
			t.Errorf("body[resolution] = %v, want it omitted for an unsupported tier", body["resolution"])
		}
		if len(notices) != 1 || !strings.Contains(notices[0], "does not support resolution") {
			t.Fatalf("notices = %v, want one unsupported-resolution notice", notices)
		}
	})
	t.Run("missing resolution input degrades with a notice", func(t *testing.T) {
		body, notices, err := resolveVideoReframeBody(videoReframeSchema(ltxEnum, nil, "resolution"),
			VideoReframeRequest{Model: "acme/reframe", Video: "https://example.com/v.mp4", AspectRatio: "9:16", Resolution: "720p"},
			builtinFalOverrides())
		if err != nil {
			t.Fatalf("resolveVideoReframeBody error: %v", err)
		}
		if _, present := body["resolution"]; present {
			t.Errorf("body[resolution] = %v, want it omitted when the model declares no tier input", body["resolution"])
		}
		if len(notices) != 1 || !strings.Contains(notices[0], "no resolution control") {
			t.Fatalf("notices = %v, want one \"no resolution control\" notice", notices)
		}
	})
	t.Run("nil schema falls back to default field names", func(t *testing.T) {
		body, notices, err := resolveVideoReframeBody(nil,
			VideoReframeRequest{Model: "acme/reframe", Video: "data:video/mp4;base64,AAA", AspectRatio: "9:16", Resolution: "720p"},
			builtinFalOverrides())
		if err != nil {
			t.Fatalf("resolveVideoReframeBody error: %v", err)
		}
		if body["video_url"] != "data:video/mp4;base64,AAA" || body["aspect_ratio"] != "9:16" || body["resolution"] != "720p" {
			t.Fatalf("body = %v, want the legacy {video_url, aspect_ratio, resolution} fallback", body)
		}
		if len(notices) != 1 {
			t.Fatalf("notices = %v, want the schema-unavailable notice", notices)
		}
	})
}

// TestReframeVideoResultForwardFeedsAttachment verifies the reframe result
// rides the shared ToolVideoResult forward-feed: a reframed clip becomes the
// AttachedVideo a later tool in the same batch (e.g. upscale_video) consumes.
func TestReframeVideoResultForwardFeedsAttachment(t *testing.T) {
	result := HarnessToolResult{Status: "completed", Result: ToolVideoResult{
		Videos: []ToolVideoFile{{TempPath: "does-not-exist.mp4"}},
	}}
	if media := forwardableMediaFromResults([]HarnessToolResult{result}); media != nil {
		t.Fatalf("media = %+v, want nil — an unreadable temp file must not forward", media)
	}
}

// TestNoteReframeDurationMismatch pins the reframe duration guard: a rendered
// clip that drifts past reframeDurationToleranceSeconds from its staged source
// gains exactly one notice naming both durations and the frame-exact local
// alternative; rounding-sized drift, an unreadable source, or an unparseable
// output container all stay quiet, and earlier notices survive the append.
func TestNoteReframeDurationMismatch(t *testing.T) {
	cases := []struct {
		name         string
		source       float64
		sourceOK     bool
		output       []byte
		existing     []string
		wantContains []string
	}{
		{
			name:         "shorter re-render is flagged",
			source:       15.083,
			sourceOK:     true,
			output:       mp4FixtureWithDuration(0, 1000, 14208),
			existing:     []string{"pre-existing notice"},
			wantContains: []string{"14.2s", "15.1s", "transform_video"},
		},
		{
			name:         "longer re-render is flagged",
			source:       10,
			sourceOK:     true,
			output:       mp4FixtureWithDuration(0, 24000, 297600),
			wantContains: []string{"12.4s", "10.0s"},
		},
		{name: "within tolerance stays quiet", source: 10, sourceOK: true, output: mp4FixtureWithDuration(0, 1000, 10300)},
		{name: "exactly at tolerance stays quiet", source: 10, sourceOK: true, output: mp4FixtureWithDuration(0, 1000, 10500)},
		{name: "unreadable source skips the guard", source: 15, sourceOK: false, output: mp4FixtureWithDuration(0, 1000, 14208)},
		{name: "non-positive source skips the guard", source: 0, sourceOK: true, output: mp4FixtureWithDuration(0, 1000, 14208)},
		{name: "unparseable output skips the guard", source: 15, sourceOK: true, output: tinyMP4()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			generated := GeneratedVideo{Data: tc.output, Notices: tc.existing}
			noteReframeDurationMismatch(&generated, tc.source, tc.sourceOK)
			if len(tc.wantContains) == 0 {
				if len(generated.Notices) != len(tc.existing) {
					t.Fatalf("notices = %v, want none added", generated.Notices)
				}
				return
			}
			if len(generated.Notices) != len(tc.existing)+1 {
				t.Fatalf("notices = %v, want exactly one added", generated.Notices)
			}
			notice := generated.Notices[len(generated.Notices)-1]
			for _, want := range tc.wantContains {
				if !strings.Contains(notice, want) {
					t.Errorf("notice %q missing %q", notice, want)
				}
			}
		})
	}
}

// TestReframeBilledSeconds pins the fal reframe estimate's input-seconds rule:
// the endpoints bill per second of the INPUT clip, so a readable staged source
// wins even when the rendered container disagrees; the rendered clip is the
// fallback, and two unreadable containers price nothing.
func TestReframeBilledSeconds(t *testing.T) {
	output := mp4FixtureWithDuration(0, 1000, 14208) // 14.208s
	if got, ok := reframeBilledSeconds(15.083, true, output); !ok || got != 15.083 {
		t.Fatalf("reframeBilledSeconds(readable source) = (%v, %v), want (15.083, true)", got, ok)
	}
	if got, ok := reframeBilledSeconds(0, false, output); !ok || got != 14.208 {
		t.Fatalf("reframeBilledSeconds(unreadable source) = (%v, %v), want (14.208, true)", got, ok)
	}
	if _, ok := reframeBilledSeconds(0, false, tinyMP4()); ok {
		t.Fatal("reframeBilledSeconds with both containers unreadable should price nothing")
	}
}

// TestReframeVideoDurationGuardReplicate drives the replicate reframe branch
// end to end with real container durations: a rendered clip 0.875s short of
// its source comes back with exactly the guard's notice, and a
// within-tolerance render stays quiet.
func TestReframeVideoDurationGuardReplicate(t *testing.T) {
	keyring.MockInit()
	t.Cleanup(func() { _ = clearReplicateAPIKey() })
	if err := saveReplicateAPIKey("replicate-test-key"); err != nil {
		t.Fatalf("saveReplicateAPIKey: %v", err)
	}
	reframeSchema := `{"components":{"schemas":{"Input":{"type":"object","properties":{
		"video":{"type":"string"},
		"aspect_ratio":{"type":"string","enum":["16:9","9:16"]}
	}}}}}`
	for _, tc := range []struct {
		name       string
		sourceDur  uint32 // mvhd duration at timescale 1000
		outputDur  uint32
		wantNotice bool
	}{
		{name: "short render is flagged", sourceDur: 15083, outputDur: 14208, wantNotice: true},
		{name: "within-tolerance render stays quiet", sourceDur: 10000, outputDur: 10200, wantNotice: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			predictions := replicatePredictionHandler(t, nil,
				`"https://replicate.delivery/out/reframed.mp4"`, "/out/reframed.mp4",
				mp4FixtureWithDuration(0, 1000, tc.outputDur), "video/mp4")
			transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.Method == http.MethodGet && req.URL.Path == "/v1/models/luma/reframe-video" {
					return jsonResp(`{"latest_version":{"openapi_schema":` + reframeSchema + `}}`), nil
				}
				return predictions.RoundTrip(req)
			})
			app := &App{client: &http.Client{Transport: transport}}
			config := defaultAppConfig()
			config.Storage.Root = t.TempDir()
			config.Models.VideoProvider = "replicate"
			config.Providers.Replicate.VideoReframeModel = "luma/reframe-video"
			gateway := newToolGateway(app, config)

			source := base64.StdEncoding.EncodeToString(mp4FixtureWithDuration(0, 1000, tc.sourceDur))
			generated, err := gateway.tools.ReframeVideo(context.Background(), VideoReframeRequest{
				Model:       "luma/reframe-video",
				Video:       "data:video/mp4;base64," + source,
				AspectRatio: "9:16",
			})
			if err != nil {
				t.Fatalf("ReframeVideo: %v", err)
			}
			if tc.wantNotice {
				if len(generated.Notices) != 1 {
					t.Fatalf("notices = %v, want exactly the duration guard's notice", generated.Notices)
				}
				for _, want := range []string{"14.2s", "15.1s", "transform_video"} {
					if !strings.Contains(generated.Notices[0], want) {
						t.Errorf("notice %q missing %q", generated.Notices[0], want)
					}
				}
			} else if len(generated.Notices) != 0 {
				t.Fatalf("notices = %v, want none", generated.Notices)
			}
		})
	}
}
