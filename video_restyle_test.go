package main

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
)

// TestRestyleVideoToolRequiresAttachedVideo verifies the tool errors when no
// source video is attached, since there is nothing to restyle.
func TestRestyleVideoToolRequiresAttachedVideo(t *testing.T) {
	tools := HarnessToolExecutionContext{
		Config: AppConfig{Models: ConfigModels{ImageProvider: "fal"}},
		RestyleVideo: func(context.Context, VideoRestyleRequest) (GeneratedVideo, error) {
			t.Fatal("RestyleVideo must not be called without an attached video")
			return GeneratedVideo{}, nil
		},
	}
	def := videoRestyleToolDefinition()
	_, _, err := def.Execute(t.Context(), tools, HarnessToolCall{Content: "anime style"})
	if err == nil || !strings.Contains(err.Error(), "attached video") {
		t.Fatalf("err = %v, want an error mentioning an attached video is required", err)
	}
}

// TestRestyleVideoValidate pins the call-side validation: the style prompt is
// the tool's entire purpose, so an omitted one is a plan correction rather
// than a runtime guess.
func TestRestyleVideoValidate(t *testing.T) {
	def := videoRestyleToolDefinition()
	if errors := def.Validate("toolCalls[0]", HarnessToolCall{}); len(errors) == 0 || !strings.Contains(errors[0], ".content is required") {
		t.Errorf("restyle without a prompt = %v, want the required error", errors)
	}
	if errors := def.Validate("toolCalls[0]", HarnessToolCall{Content: "anime style", Resolution: "1080p", NegativePrompt: "watermark"}); len(errors) != 0 {
		t.Errorf("valid restyle = %v, want none", errors)
	}
}

// TestRestyleVideoToolDefaultsAndMapping checks the default model, the
// forwarded source video, prompt, reference images, negative prompt, and
// resolution (empty lets the model apply its own tier), and the result shape
// (a ToolVideoResult carrying one temp-file video, like generate_video).
func TestRestyleVideoToolDefaultsAndMapping(t *testing.T) {
	var captured VideoRestyleRequest
	tools := HarnessToolExecutionContext{
		Config:         AppConfig{Models: ConfigModels{ImageProvider: "fal"}},
		AttachedVideos: []string{"data:video/mp4;base64,AAA"},
		AttachedImages: []string{"data:image/png;base64,BBB", "data:image/png;base64,CCC"},
		RestyleVideo: func(_ context.Context, req VideoRestyleRequest) (GeneratedVideo, error) {
			captured = req
			return GeneratedVideo{Data: []byte("fake-mp4"), MimeType: "video/mp4", SourceURL: "https://fal.example/v.mp4"}, nil
		},
	}
	def := videoRestyleToolDefinition()
	result, summary, err := def.Execute(t.Context(), tools, HarnessToolCall{
		Content:        "hand-drawn anime style",
		NegativePrompt: "text, watermark",
		Resolution:     "1080p",
		Model:          "fal-ai/wan/v2.7/edit-video",
	})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if captured.Video != "data:video/mp4;base64,AAA" {
		t.Errorf("captured video = %q, want the attached source forwarded", captured.Video)
	}
	if captured.Prompt != "hand-drawn anime style" {
		t.Errorf("captured prompt = %q, want the call's content as the style instruction", captured.Prompt)
	}
	if len(captured.Images) != 2 || captured.Images[0] != "data:image/png;base64,BBB" {
		t.Errorf("captured images = %v, want both attached reference frames forwarded", captured.Images)
	}
	if captured.NegativePrompt != "text, watermark" {
		t.Errorf("captured negativePrompt = %q, want the forwarded value", captured.NegativePrompt)
	}
	if captured.Resolution != "1080p" {
		t.Errorf("captured resolution = %q, want the forwarded tier", captured.Resolution)
	}
	if !strings.Contains(summary, "fal-ai/wan/v2.7/edit-video") {
		t.Errorf("summary = %q, want it to name the model", summary)
	}
	typed, ok := result.(ToolVideoResult)
	if !ok || typed.Count != 1 || len(typed.Videos) != 1 || typed.Prompt != "hand-drawn anime style" {
		t.Fatalf("result = %+v, want a ToolVideoResult with one video and the prompt", result)
	}
	if typed.Videos[0].TempPath == "" || typed.Videos[0].MimeType != "video/mp4" {
		t.Errorf("video file = %+v, want a staged temp file with the mime type", typed.Videos[0])
	}
	t.Cleanup(func() { _ = os.Remove(typed.Videos[0].TempPath) })

	// Defaults: no override resolves the configured→const default model, and
	// empty optional fields stay empty.
	captured = VideoRestyleRequest{}
	call := HarnessToolCall{Content: "claymation"}
	if _, _, err := def.Execute(t.Context(), tools, call); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if captured.Model != defaultFalVideoRestyleModel {
		t.Errorf("model = %q, want the restyle default %q", captured.Model, defaultFalVideoRestyleModel)
	}
	if captured.Resolution != "" || captured.NegativePrompt != "" || len(captured.Images) != 2 {
		t.Errorf("captured = %+v, want empty optionals with attachments still forwarded", captured)
	}
}

// TestRestyleVideoToolHonorsModelOverride verifies precedence: a call-supplied
// model wins over the configured VideoRestyleModel, which wins over the const
// default.
func TestRestyleVideoToolHonorsModelOverride(t *testing.T) {
	var captured VideoRestyleRequest
	tools := HarnessToolExecutionContext{
		Config:         AppConfig{Providers: ConfigProviders{Fal: ConfigFal{VideoRestyleModel: "fal-ai/wan/v2.2-a14b/video-to-video"}}},
		AttachedVideos: []string{"data:video/mp4;base64,AAA"},
		RestyleVideo: func(_ context.Context, req VideoRestyleRequest) (GeneratedVideo, error) {
			captured = req
			return GeneratedVideo{Data: []byte("fake-mp4"), MimeType: "video/mp4"}, nil
		},
	}
	def := videoRestyleToolDefinition()
	result, _, err := def.Execute(t.Context(), tools, HarnessToolCall{Content: "anime", Model: "fal-ai/ltx-2.3-22b/video-to-video"})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if captured.Model != "fal-ai/ltx-2.3-22b/video-to-video" {
		t.Errorf("model = %q, want the call override", captured.Model)
	}
	if typed, ok := result.(ToolVideoResult); ok {
		t.Cleanup(func() { _ = os.Remove(typed.Videos[0].TempPath) })
	}
	// The configured model must also win when the call doesn't override it.
	captured = VideoRestyleRequest{}
	if _, _, err := def.Execute(t.Context(), tools, HarnessToolCall{Content: "anime"}); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if captured.Model != "fal-ai/wan/v2.2-a14b/video-to-video" {
		t.Errorf("model = %q, want the configured VideoRestyleModel", captured.Model)
	}
}

// TestVideoRestyleConfiguredAndResolver covers the gating (available whenever
// a fal.ai key is configured, like upscale_video — the default endpoint always
// applies) and the resolver's configured→default fallback.
func TestVideoRestyleConfiguredAndResolver(t *testing.T) {
	keyring.MockInit()
	if err := saveFalAPIKey("fal-test-key"); err != nil {
		t.Fatalf("saveFalAPIKey: %v", err)
	}
	t.Cleanup(func() { _ = clearFalAPIKey() })

	if !videoRestyleConfigured(AppConfig{}) {
		t.Error("videoRestyleConfigured(with key) = false, want true")
	}
	configured := resolveDefaultVideoRestyleModel(AppConfig{
		Providers: ConfigProviders{Fal: ConfigFal{VideoRestyleModel: "fal-ai/wan/v2.7/edit-video"}},
	})
	if configured != "fal-ai/wan/v2.7/edit-video" {
		t.Errorf("resolveDefaultVideoRestyleModel(configured) = %q, want the configured endpoint", configured)
	}
	if fallback := resolveDefaultVideoRestyleModel(AppConfig{}); fallback != defaultFalVideoRestyleModel {
		t.Errorf("resolveDefaultVideoRestyleModel(default) = %q, want %q", fallback, defaultFalVideoRestyleModel)
	}
	// The pricing batch must include the endpoint so a restyle turn gets its
	// dollar row.
	if !contains(configuredFalEndpointIDs(AppConfig{}), defaultFalVideoRestyleModel) {
		t.Errorf("configuredFalEndpointIDs missing %q", defaultFalVideoRestyleModel)
	}

	// With the key cleared the tool is not offered.
	if err := clearFalAPIKey(); err != nil {
		t.Fatalf("clearFalAPIKey: %v", err)
	}
	if videoRestyleConfigured(AppConfig{}) {
		t.Error("videoRestyleConfigured(no key) = true, want false")
	}
}

// videoRestyleSchema builds a synthetic restyle schema declaring video_url,
// prompt, and optional resolution/reference-image inputs with the shapes fal's
// endpoints publish (Kling o3 edit: scalar prompt, array image_urls; Wan 2.7:
// enum resolution; LTX-2.3-22B: negative_prompt).
func videoRestyleSchema(imageKind string, resolutionEnum []string, omit ...string) *ModelInputSchema {
	schema := &ModelInputSchema{
		Properties: map[string]SchemaProperty{},
	}
	schema.Properties["video_url"] = SchemaProperty{Name: "video_url", Kind: schemaScalar}
	schema.order = []string{"video_url"}
	schema.Properties["prompt"] = SchemaProperty{Name: "prompt", Kind: schemaScalar}
	schema.order = append(schema.order, "prompt")
	if !contains(omit, "image_urls") && imageKind != "" {
		prop := SchemaProperty{Name: "image_urls", Kind: schemaArray, Type: "array", Items: &SchemaProperty{Name: "image_urls", Kind: schemaScalar, Type: "string"}}
		if imageKind == "scalar" {
			prop = SchemaProperty{Name: "image_urls", Kind: schemaScalar}
		}
		schema.Properties["image_urls"] = prop
		schema.order = append(schema.order, "image_urls")
	}
	if !contains(omit, "negative_prompt") {
		schema.Properties["negative_prompt"] = SchemaProperty{Name: "negative_prompt", Kind: schemaScalar}
		schema.order = append(schema.order, "negative_prompt")
	}
	if !contains(omit, "resolution") && len(resolutionEnum) > 0 {
		schema.Properties["resolution"] = SchemaProperty{Name: "resolution", Kind: schemaScalar, Enum: resolutionEnum}
		schema.order = append(schema.order, "resolution")
	}
	return schema
}

// TestResolveVideoRestyleBody covers the canonical→native mapping, the two
// fatal guards (no video input; no prompt input), the degradable reference
// images (scalar takes the first with a notice; absent drops them with a
// notice), the degradable negative prompt and resolution, and the nil-schema
// legacy fallback.
func TestResolveVideoRestyleBody(t *testing.T) {
	t.Run("maps video, prompt, references, and tier", func(t *testing.T) {
		body, notices, err := resolveVideoRestyleBody(videoRestyleSchema("array", []string{"720p", "1080p"}),
			VideoRestyleRequest{
				Model:          defaultFalVideoRestyleModel,
				Video:          "data:video/mp4;base64,AAA",
				Prompt:         "hand-drawn anime style",
				Images:         []string{"data:image/png;base64,BBB"},
				NegativePrompt: "watermark",
				Resolution:     "1080p",
			},
			builtinFalOverrides())
		if err != nil {
			t.Fatalf("resolveVideoRestyleBody error: %v", err)
		}
		if body["video_url"] != "data:video/mp4;base64,AAA" {
			t.Errorf("video_url = %v, want the attached clip", body["video_url"])
		}
		if body["prompt"] != "hand-drawn anime style" {
			t.Errorf("prompt = %v, want the style instruction", body["prompt"])
		}
		images, ok := body["image_urls"].([]any)
		if !ok || len(images) != 1 || images[0] != "data:image/png;base64,BBB" {
			t.Errorf("image_urls = %v, want the array-coerced reference", body["image_urls"])
		}
		if body["negative_prompt"] != "watermark" {
			t.Errorf("negative_prompt = %v, want the forwarded value", body["negative_prompt"])
		}
		if body["resolution"] != "1080p" {
			t.Errorf("resolution = %v, want the requested tier", body["resolution"])
		}
		if len(notices) != 0 {
			t.Errorf("notices = %v, want none", notices)
		}
	})
	t.Run("missing video input is a hard error", func(t *testing.T) {
		schema := &ModelInputSchema{
			Properties: map[string]SchemaProperty{
				"prompt": {Name: "prompt", Kind: schemaScalar},
			},
			order: []string{"prompt"},
		}
		_, _, err := resolveVideoRestyleBody(schema,
			VideoRestyleRequest{Model: "fal-ai/esrgan", Video: "https://example.com/v.mp4", Prompt: "anime"},
			builtinFalOverrides())
		if err == nil || !strings.Contains(err.Error(), "no video input") {
			t.Fatalf("err = %v, want a hard error naming the missing video input", err)
		}
	})
	t.Run("missing prompt input is a hard error", func(t *testing.T) {
		schema := &ModelInputSchema{
			Properties: map[string]SchemaProperty{
				"video_url": {Name: "video_url", Kind: schemaScalar},
			},
			order: []string{"video_url"},
		}
		_, _, err := resolveVideoRestyleBody(schema,
			VideoRestyleRequest{Model: "fal-ai/video-upscaler", Video: "https://example.com/v.mp4", Prompt: "anime"},
			builtinFalOverrides())
		if err == nil || !strings.Contains(err.Error(), "no prompt input") {
			t.Fatalf("err = %v, want a hard error naming the missing prompt input", err)
		}
	})
	t.Run("scalar image input takes the first reference with a notice", func(t *testing.T) {
		body, notices, err := resolveVideoRestyleBody(videoRestyleSchema("scalar", nil),
			VideoRestyleRequest{Model: "fal-ai/wan/v2.7/edit-video", Video: "https://example.com/v.mp4", Prompt: "anime",
				Images: []string{"https://example.com/a.png", "https://example.com/b.png"}},
			builtinFalOverrides())
		if err != nil {
			t.Fatalf("resolveVideoRestyleBody error: %v", err)
		}
		if body["image_urls"] != "https://example.com/a.png" {
			t.Errorf("image_urls = %v, want only the first reference", body["image_urls"])
		}
		if len(notices) != 1 || !strings.Contains(notices[0], "single reference image") {
			t.Fatalf("notices = %v, want one first-of notice", notices)
		}
	})
	t.Run("model without an image input drops references with a notice", func(t *testing.T) {
		body, notices, err := resolveVideoRestyleBody(videoRestyleSchema("", nil, "image_urls"),
			VideoRestyleRequest{Model: "acme/restyle", Video: "https://example.com/v.mp4", Prompt: "anime",
				Images: []string{"https://example.com/a.png"}},
			builtinFalOverrides())
		if err != nil {
			t.Fatalf("resolveVideoRestyleBody error: %v", err)
		}
		if _, present := body["image_urls"]; present {
			t.Errorf("body[image_urls] = %v, want it omitted", body["image_urls"])
		}
		if len(notices) != 1 || !strings.Contains(notices[0], "no reference-image input") {
			t.Fatalf("notices = %v, want one dropped-references notice", notices)
		}
	})
	t.Run("unsupported resolution degrades with a notice", func(t *testing.T) {
		body, notices, err := resolveVideoRestyleBody(videoRestyleSchema("", []string{"720p", "1080p"}),
			VideoRestyleRequest{Model: "fal-ai/wan/v2.7/edit-video", Video: "https://example.com/v.mp4", Prompt: "anime", Resolution: "4k"},
			builtinFalOverrides())
		if err != nil {
			t.Fatalf("resolveVideoRestyleBody error: %v", err)
		}
		if _, present := body["resolution"]; present {
			t.Errorf("body[resolution] = %v, want it omitted for an unsupported tier", body["resolution"])
		}
		if len(notices) != 1 || !strings.Contains(notices[0], "does not support resolution") {
			t.Fatalf("notices = %v, want one unsupported-resolution notice", notices)
		}
	})
	t.Run("missing negative-prompt input degrades with a notice", func(t *testing.T) {
		body, notices, err := resolveVideoRestyleBody(videoRestyleSchema("", nil, "negative_prompt"),
			VideoRestyleRequest{Model: "fal-ai/kling-video/o3/pro/video-to-video/edit", Video: "https://example.com/v.mp4", Prompt: "anime", NegativePrompt: "watermark"},
			builtinFalOverrides())
		if err != nil {
			t.Fatalf("resolveVideoRestyleBody error: %v", err)
		}
		if _, present := body["negative_prompt"]; present {
			t.Errorf("body[negative_prompt] = %v, want it omitted when the model declares none", body["negative_prompt"])
		}
		if len(notices) != 1 || !strings.Contains(notices[0], "no negative-prompt control") {
			t.Fatalf("notices = %v, want one \"no negative-prompt control\" notice", notices)
		}
	})
	t.Run("nil schema falls back to default field names", func(t *testing.T) {
		body, notices, err := resolveVideoRestyleBody(nil,
			VideoRestyleRequest{Model: "acme/restyle", Video: "data:video/mp4;base64,AAA", Prompt: "anime", Resolution: "720p"},
			builtinFalOverrides())
		if err != nil {
			t.Fatalf("resolveVideoRestyleBody error: %v", err)
		}
		if body["video_url"] != "data:video/mp4;base64,AAA" || body["prompt"] != "anime" || body["resolution"] != "720p" {
			t.Fatalf("body = %v, want the legacy {video_url, prompt, resolution} fallback", body)
		}
		if len(notices) != 1 {
			t.Fatalf("notices = %v, want the schema-unavailable notice", notices)
		}
	})
}

// TestIsFalVideoRestyleModel pins the picker partition: the general restyle
// markers match on the id, and the five partitioned families (extend, lipsync,
// motion, upscale, reframe) plus utility transforms stay out — in id or tags.
func TestIsFalVideoRestyleModel(t *testing.T) {
	yes := []string{
		defaultFalVideoRestyleModel,
		"fal-ai/kling-video/o3/standard/video-to-video/edit",
		"fal-ai/wan/v2.7/edit-video",
		"fal-ai/wan/v2.2-a14b/video-to-video",
		"fal-ai/ltx-2.3-22b/video-to-video",
		"fal-ai/luma-dream-machine/ray-2/modify",
		"fal-ai/sora-2/video-to-video/remix",
		"xai/grok-imagine-video/edit-video",
	}
	for _, id := range yes {
		if !isFalVideoRestyleModel(FalModel{ID: id}) {
			t.Errorf("isFalVideoRestyleModel(%q) = false, want true", id)
		}
	}
	no := []FalModel{
		{ID: "fal-ai/ltx-2.3/reframe"},
		{ID: "fal-ai/video-upscaler"},
		{ID: "fal-ai/sync-lipsync/v2/pro", Tags: []string{"video-to-video"}},
		{ID: "fal-ai/veo3.1/extend-video"},
		{ID: "fal-ai/kling-video/v3/pro/motion-control", Tags: []string{"stylized", "transform", "editing"}},
		{ID: "bria/video/background-removal/v3", Tags: []string{"video-to-video"}},
		{ID: "sonilo/v1.1/video-to-video-music"},
		{ID: "fal-ai/id-v2v/relight"},
		{ID: "fal-ai/wan/v2.2-14b/animate/move", Tags: []string{"video to video", "motion"}},
		{ID: "fal-ai/wan-motion"},
	}
	for _, model := range no {
		if isFalVideoRestyleModel(model) {
			t.Errorf("isFalVideoRestyleModel(%q) = true, want false", model.ID)
		}
	}
}

// TestTriagePromptRoutesRestylingToVideoMode pins restyle's routing contract:
// the mediaEdit sentence must carve restyling out (a look change is generation,
// not a local edit — ffmpeg is not a remedy for it), and the video-mode
// paragraph must name restyle_video so a tooled restyle turn reaches the
// planner instead of prose.
func TestTriagePromptRoutesRestylingToVideoMode(t *testing.T) {
	registry := newHarnessToolRegistry([]HarnessToolDefinition{videoRestyleToolDefinition()})
	prompt := triageSystemPrompt(registry, nil, "/tmp/ws")
	if !strings.Contains(prompt, "restyle_video") {
		t.Fatalf("video-mode guidance should name restyle_video:\n%s", prompt)
	}
	for _, want := range []string{
		"RESTYLING is not one of these",
		`never mediaEdit`,
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("mediaEdit guidance missing %q:\n%s", want, prompt)
		}
	}
}

// TestTurnDeliveredVideoStandsDownEditNotice pins the ffmpeg-unavailable
// stand-down: a turn whose tools delivered a completed video (the fal
// video-to-video tools run without ffmpeg) must not claim local editing is
// impossible, while a failed or video-less turn keeps the notice.
func TestTurnDeliveredVideoStandsDownEditNotice(t *testing.T) {
	if turnDeliveredVideo(nil) {
		t.Error("turnDeliveredVideo(nil) = true, want false")
	}
	failed := []HarnessToolResult{{Name: "restyle_video", Status: "failed"}}
	if turnDeliveredVideo(failed) {
		t.Error("turnDeliveredVideo(failed) = true, want false")
	}
	delivered := []HarnessToolResult{{Name: "restyle_video", Status: "completed", Result: ToolVideoResult{Count: 1,
		Videos: []ToolVideoFile{{TempPath: "x.mp4"}}}}}
	if !turnDeliveredVideo(delivered) {
		t.Error("turnDeliveredVideo(completed restyle) = false, want true")
	}
	completedEmpty := []HarnessToolResult{{Name: "restyle_video", Status: "completed", Result: ToolVideoResult{}}}
	if turnDeliveredVideo(completedEmpty) {
		t.Error("turnDeliveredVideo(completed with no video) = true, want false")
	}
}
