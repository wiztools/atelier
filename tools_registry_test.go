package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"atelier/internal/mediasniff/mediasnifftest"
)

// TestImageSizeForAspectRatio checks that named aspect ratios derive concrete
// width/height from the configured long-edge budget, rounded to a multiple of
// 16, and that an unknown ratio returns (0, 0) so the caller keeps its
// configured defaults.
func TestImageSizeForAspectRatio(t *testing.T) {
	cases := []struct {
		name     string
		baseLong int
		ratio    string
		wantW    int
		wantH    int
	}{
		{"square", 768, "1:1", 768, 768},
		{"landscape16x9", 768, "16:9", 768, 432},
		{"portrait9x16", 768, "9:16", 432, 768},
		{"landscape4x3", 768, "4:3", 768, 576},
		{"portrait3x4", 768, "3:4", 576, 768},
		{"landscape3x2", 768, "3:2", 768, 512},
		{"portrait2x3", 768, "2:3", 512, 768},
		{"cinematic21x9", 768, "21:9", 768, 336},
		{"unknownRatioFallsBack", 768, "totally-bogus", 0, 0},
		{"emptyRatioFallsBack", 768, "", 0, 0},
		{"shortEdgeFlooredAt256", 256, "16:9", 256, 256},
		{"zeroBaseUsesDefault", 0, "1:1", 1024, 1024},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotW, gotH := imageSizeForAspectRatio(tc.baseLong, tc.ratio)
			if gotW != tc.wantW || gotH != tc.wantH {
				t.Errorf("imageSizeForAspectRatio(%d, %q) = (%d, %d), want (%d, %d)",
					tc.baseLong, tc.ratio, gotW, gotH, tc.wantW, tc.wantH)
			}
			if tc.wantW != 0 {
				if gotW%16 != 0 || gotH%16 != 0 {
					t.Errorf("dimensions not multiples of 16: (%d, %d)", gotW, gotH)
				}
			}
		})
	}
}

// TestGenerateVideoParamSchemaExposesDuration is the regression for
// conv_484449cf8fe4a13c1ffa6bb4: a request to "extend another 5s" produced a
// ~10s clip because the planner had no duration field to express the 5, so it
// landed only in the prompt text and fal applied its default. The video tool
// must expose a `duration` param (mirroring the audio tool) so the planner can
// carry an explicit clip length / extension length, and its description must
// flag the extend-vs-total distinction (otherwise a "5s" extend reads as total
// length and the same confusion recurs).
func TestGenerateVideoParamSchemaExposesDuration(t *testing.T) {
	schema := generateVideoParamSchema()
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("expected properties map, got %T", schema["properties"])
	}
	prop, ok := props["duration"].(map[string]any)
	if !ok {
		t.Fatalf("generate_video param schema must expose a `duration` property; got %v", props)
	}
	desc, _ := prop["description"].(string)
	if !strings.Contains(desc, "extension") || !strings.Contains(desc, "total") {
		t.Fatalf("duration description must distinguish extension length from total clip length for extend. got %q", desc)
	}
}

// TestVideoToolSummaryMatchesDeliveredSources pins the video summary against
// what the call actually delivered. A model whose schema can't accept an
// attached side refuses in the resolvers (fal and Replicate hold the same
// line), so a successful call used everything attached and the summary can
// phrase delivery straight from the attachments. Before those refusals, a
// dropped side degraded with a notice and the summary must not have claimed a
// motion transfer that never carried the video — in conv_16bf42ce64997fad02f769a9
// the contradictory pair ("transferred the attached video's motion..." + "the
// attached video was ignored") both reached the final model as evidence and it
// repeated both claims. The one remaining way a side goes unused is the tool
// layer's own source-mode trimming (source:"video" drops the images before
// the call), which the attachments the executor sees already reflect.
func TestVideoToolSummaryMatchesDeliveredSources(t *testing.T) {
	const model = "bytedance/seedance-2.0/reference-to-video"
	def := videoGenerationToolDefinition(AppConfig{}, false)
	exec := func(images, videos []string) HarnessToolExecutionContext {
		return HarnessToolExecutionContext{
			AttachedImages: images,
			AttachedVideos: videos,
			GenerateVideo: func(ctx context.Context, req VideoGenerateRequest) (GeneratedVideo, error) {
				return GeneratedVideo{Data: []byte("mp4"), MimeType: "video/mp4", SourceURL: "https://example.com/v.mp4"}, nil
			},
		}
	}

	_, summary, err := def.Execute(context.Background(),
		exec([]string{"data:image/png;base64,AAA", "data:image/png;base64,BBB"}, []string{"data:video/mp4;base64,CCC"}),
		HarnessToolCall{Name: "generate_video", Model: model, Content: "fly to the moon"})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if summary != "transferred the attached video's motion onto the attached image with "+model {
		t.Fatalf("summary = %q, want the motion-transfer phrasing when both sources are delivered", summary)
	}

	_, summary, err = def.Execute(context.Background(),
		exec([]string{"data:image/png;base64,AAA", "data:image/png;base64,BBB"}, nil),
		HarnessToolCall{Name: "generate_video", Model: model, Content: "fly to the moon"})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if summary != "combined 2 attached images into a video with "+model {
		t.Fatalf("summary = %q, want the images-only phrasing when no video is attached", summary)
	}
}

// TestGenerateVideoUseVideoAsRouting pins the planner-facing source-mode
// choice: with useVideoAs:"reference" an image+video (or video-only) turn
// routes to the configured reference-capable image model and sends EVERY
// attached video, while the default keeps source semantics — motion control
// with an image, Veo extend without — and trims the request to the first clip
// with a notice pointing at reference mode. conv_f4048032debc0e335da6a085
// routed a character sheet plus two style reference clips to motion control,
// whose motion analyzer rejected the freeze-frame-heavy references.
func TestGenerateVideoUseVideoAsRouting(t *testing.T) {
	const (
		imageModel    = "bytedance/seedance-2.5/reference-to-video"
		motionModel   = "fal-ai/kling-video/v2.6/pro/motion-control"
		extendModel   = "fal-ai/veo3.1/extend-video"
		textModel     = "bytedance/seedance-2.0/text-to-video"
		referenceVid1 = "data:video/mp4;base64,BBB"
		referenceVid2 = "data:video/mp4;base64,CCC"
		characterImg  = "data:image/png;base64,AAA"
	)

	cases := []struct {
		name          string
		call          HarnessToolCall
		images        []string
		videos        []string
		wantModel     string
		wantVideoLen  int
		wantExtend    bool
		wantSummary   string
		wantDroppedTo int // number of videos the request should carry, post-trim
	}{
		{
			name:         "reference mode with image and two videos",
			call:         HarnessToolCall{Name: "generate_video", Content: "bed flies home", UseVideoAs: "reference"},
			images:       []string{characterImg},
			videos:       []string{referenceVid1, referenceVid2},
			wantModel:    imageModel,
			wantVideoLen: 2,
			wantExtend:   false,
			wantSummary:  "used the attached image and 2 attached videos as references for a new video with " + imageModel,
		},
		{
			name:         "reference mode with videos only",
			call:         HarnessToolCall{Name: "generate_video", Content: "bed flies home", UseVideoAs: "reference"},
			videos:       []string{referenceVid1, referenceVid2},
			wantModel:    imageModel,
			wantVideoLen: 2,
			wantExtend:   false,
			wantSummary:  "used 2 attached videos as references for a new video with " + imageModel,
		},
		{
			name:          "default keeps motion control and trims to one video",
			call:          HarnessToolCall{Name: "generate_video", Content: "bed flies home"},
			images:        []string{characterImg},
			videos:        []string{referenceVid1, referenceVid2},
			wantModel:     motionModel,
			wantVideoLen:  1,
			wantExtend:    false,
			wantDroppedTo: 1,
			wantSummary:   "transferred the attached video's motion onto the attached image with " + motionModel,
		},
		{
			name:          "default video-only keeps extend and trims to one video",
			call:          HarnessToolCall{Name: "generate_video", Content: "bed flies home"},
			videos:        []string{referenceVid1, referenceVid2},
			wantModel:     extendModel,
			wantVideoLen:  1,
			wantExtend:    true,
			wantDroppedTo: 1,
			wantSummary:   "extended the attached video into a longer clip with " + extendModel,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			config := defaultAppConfig()
			config.Providers.Fal.VideoModel = textModel
			config.Providers.Fal.VideoImageModel = imageModel
			config.Providers.Fal.VideoMotionModel = motionModel
			config.Providers.Fal.VideoExtendModel = extendModel

			var gotReq VideoGenerateRequest
			def := videoGenerationToolDefinition(AppConfig{}, false)
			exec := HarnessToolExecutionContext{
				Config:         config,
				AttachedImages: tc.images,
				AttachedVideos: tc.videos,
				GenerateVideo: func(ctx context.Context, req VideoGenerateRequest) (GeneratedVideo, error) {
					gotReq = req
					return GeneratedVideo{Data: []byte("mp4"), MimeType: "video/mp4", SourceURL: "https://example.com/v.mp4"}, nil
				},
			}
			result, summary, err := def.Execute(context.Background(), exec, tc.call)
			if err != nil {
				t.Fatalf("expected no error, got %v", err)
			}
			if gotReq.Model != tc.wantModel {
				t.Fatalf("model = %q, want %q", gotReq.Model, tc.wantModel)
			}
			// ExtendSource must mirror the routing diagnosis: only the video-only,
			// non-reference turn is a continuation (multi-task models key their
			// extension task off it); reference mode and motion control stay false.
			if gotReq.ExtendSource != tc.wantExtend {
				t.Fatalf("ExtendSource = %v, want %v", gotReq.ExtendSource, tc.wantExtend)
			}
			if len(gotReq.SourceVideos()) != tc.wantVideoLen {
				t.Fatalf("request videos = %v, want %d", gotReq.SourceVideos(), tc.wantVideoLen)
			}
			if summary != tc.wantSummary {
				t.Fatalf("summary = %q, want %q", summary, tc.wantSummary)
			}
			if tc.wantDroppedTo > 0 {
				// trimmed case: a notice must tell the user the extras were dropped
				typed, ok := result.(ToolVideoResult)
				if !ok {
					t.Fatalf("result type = %T, want ToolVideoResult", result)
				}
				found := false
				for _, n := range typed.Notices {
					if strings.Contains(n, "pass useVideoAs:\"reference\"") {
						found = true
					}
				}
				if !found {
					t.Fatalf("notices = %v, want one pointing at useVideoAs reference for the dropped extras", typed.Notices)
				}
			}
		})
	}
}

// TestGenerateVideoUseVideoAsValidation pins the enum guard: an unknown
// useVideoAs value is a plan correction, not a silent fallthrough to the
// default interpretation.
func TestGenerateVideoUseVideoAsValidation(t *testing.T) {
	def := videoGenerationToolDefinition(AppConfig{}, false)
	problems := def.Validate("toolCalls[0]", HarnessToolCall{Name: "generate_video", Content: "x", UseVideoAs: "inspiration"})
	if len(problems) != 1 || !strings.Contains(problems[0], "useVideoAs must be") {
		t.Fatalf("problems = %v, want the useVideoAs enum correction", problems)
	}
	for _, ok := range []string{"", "motion", "reference"} {
		if problems := def.Validate("toolCalls[0]", HarnessToolCall{Name: "generate_video", Content: "x", UseVideoAs: ok}); len(problems) != 0 {
			t.Fatalf("useVideoAs %q problems = %v, want none", ok, problems)
		}
	}
}

// TestGenerateVideoImageRoleValidation pins the enum guard for imageRole: only
// "keyframes" (or empty) is accepted; any other value is a plan correction.
func TestGenerateVideoImageRoleValidation(t *testing.T) {
	def := videoGenerationToolDefinition(AppConfig{}, false)
	valid := HarnessToolCall{Name: "generate_video", Content: "morph", ImageRole: "keyframes"}
	if errs := def.Validate("toolCalls[0]", valid); len(errs) != 0 {
		t.Fatalf("imageRole \"keyframes\" should validate, got %v", errs)
	}
	bad := HarnessToolCall{Name: "generate_video", Content: "morph", ImageRole: "bogus"}
	errs := def.Validate("toolCalls[0]", bad)
	found := false
	for _, e := range errs {
		if strings.Contains(e, "imageRole") {
			found = true
		}
	}
	if !found {
		t.Fatalf("imageRole \"bogus\" should be rejected, got %v", errs)
	}
}

// TestGenerateVideoRejectsForeignParams pins the foreign-param corrections
// (conv_c10e9937ac0dba41911a64f5): a triage note naming the frame artifact ids
// led the planner to split the turn into one generate_video call per image,
// binding each id through artifactId (a convert_subtitles/burn_subtitles param
// the executor silently ignores) alongside screenshot_video's at. The
// attachments already ride the call, and a transition is ONE call — the
// correction must say both.
func TestGenerateVideoRejectsForeignParams(t *testing.T) {
	def := videoGenerationToolDefinition(AppConfig{}, false)

	errs := def.Validate("toolCalls[0]", HarnessToolCall{
		Name: "generate_video", Content: "morph", ImageRole: "keyframes", ArtifactID: "img_f06f522b"})
	if len(errs) != 1 || !strings.Contains(errs[0], "artifactId is not a generate_video parameter") {
		t.Fatalf("artifactId should be rejected with the binding correction, got %v", errs)
	}
	for _, want := range []string{"already resolve onto the call", "ONE call carrying both frames"} {
		if !strings.Contains(errs[0], want) {
			t.Fatalf("artifactId correction = %q, want it to contain %q", errs[0], want)
		}
	}

	errs = def.Validate("toolCalls[0]", HarnessToolCall{Name: "generate_video", Content: "morph", At: "0"})
	if len(errs) != 1 || !strings.Contains(errs[0], ".at is not a generate_video parameter") {
		t.Fatalf("at should be rejected naming screenshot_video, got %v", errs)
	}
}

// TestGenerateVideoFrameCorrectionsNameTheOmitRemedy pins the wording of the
// two frame-param corrections: each must tell the planner to OMIT the param,
// since that is the fix for a single starting frame. The old imageRole message
// named only "keyframes", which a planner holding one image read as an
// impossible requirement and gave up on the video
// (conv_85f269b404afda6d3bf7f845).
func TestGenerateVideoFrameCorrectionsNameTheOmitRemedy(t *testing.T) {
	def := videoGenerationToolDefinition(AppConfig{}, false)

	errs := def.Validate("toolCalls[0]", HarnessToolCall{Name: "generate_video", Content: "dolly in", ImageRole: "reference", OpeningFrame: "first"})
	if len(errs) != 1 {
		t.Fatalf("imageRole \"reference\" should yield one correction, got %v", errs)
	}
	for _, want := range []string{`toolCalls[0].imageRole "reference"`, "omit imageRole", "single attached image", `useVideoAs:"reference"`} {
		if !strings.Contains(errs[0], want) {
			t.Fatalf("imageRole correction = %q, want it to contain %q", errs[0], want)
		}
	}
	if strings.Contains(errs[0], "must be \"keyframes\"") {
		t.Fatalf("imageRole correction = %q, must not read as a demand for keyframes", errs[0])
	}

	errs = def.Validate("toolCalls[0]", HarnessToolCall{Name: "generate_video", Content: "dolly in", OpeningFrame: "first"})
	if len(errs) != 1 {
		t.Fatalf("openingFrame without keyframes should yield one correction, got %v", errs)
	}
	for _, want := range []string{"toolCalls[0].openingFrame", "omit openingFrame", "already the clip's starting frame"} {
		if !strings.Contains(errs[0], want) {
			t.Fatalf("openingFrame correction = %q, want it to contain %q", errs[0], want)
		}
	}
}

// TestGenerateVideoImageRoleKeyframesRouting pins the keyframe diagnosis: an
// imageRole:"keyframes" turn with exactly two attached images routes to the
// configured keyframe model, sets the internal Keyframes flag, and forwards
// both frames; one image fails the call before GenerateVideo is reached.
func TestGenerateVideoImageRoleKeyframesRouting(t *testing.T) {
	const (
		keyframeModel = "fal-ai/test/keyframe"
		firstFrame    = "data:image/png;base64,AAA"
		lastFrame     = "data:image/png;base64,BBB"
	)

	t.Run("two images route to the keyframe model and set the flag", func(t *testing.T) {
		config := defaultAppConfig()
		config.Providers.Fal.VideoKeyframeModel = keyframeModel

		var captured VideoGenerateRequest
		called := false
		def := videoGenerationToolDefinition(AppConfig{}, false)
		exec := HarnessToolExecutionContext{
			Config:         config,
			AttachedImages: []string{firstFrame, lastFrame},
			GenerateVideo: func(ctx context.Context, req VideoGenerateRequest) (GeneratedVideo, error) {
				captured = req
				called = true
				return GeneratedVideo{Data: []byte("mp4"), MimeType: "video/mp4", SourceURL: "https://example.com/v.mp4"}, nil
			},
		}
		call := HarnessToolCall{Name: "generate_video", Content: "morph the first into the last", ImageRole: "keyframes"}
		if _, _, err := def.Execute(context.Background(), exec, call); err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if !called {
			t.Fatalf("GenerateVideo was not called")
		}
		if !captured.Keyframes {
			t.Fatalf("captured.Keyframes = false, want true")
		}
		if captured.Model != keyframeModel {
			t.Fatalf("captured.Model = %q, want %q", captured.Model, keyframeModel)
		}
		if imgs := captured.SourceImages(); len(imgs) != 2 {
			t.Fatalf("forwarded images = %v, want two", imgs)
		}
	})

	t.Run("one image fails the call before GenerateVideo", func(t *testing.T) {
		config := defaultAppConfig()
		config.Providers.Fal.VideoKeyframeModel = keyframeModel

		called := false
		def := videoGenerationToolDefinition(AppConfig{}, false)
		exec := HarnessToolExecutionContext{
			Config:         config,
			AttachedImages: []string{firstFrame},
			GenerateVideo: func(ctx context.Context, req VideoGenerateRequest) (GeneratedVideo, error) {
				called = true
				return GeneratedVideo{Data: []byte("mp4"), MimeType: "video/mp4"}, nil
			},
		}
		call := HarnessToolCall{Name: "generate_video", Content: "morph", ImageRole: "keyframes"}
		if _, _, err := def.Execute(context.Background(), exec, call); err == nil {
			t.Fatalf("expected an error for one keyframe image, got nil")
		}
		if called {
			t.Fatalf("GenerateVideo should not be called when the image count is wrong")
		}
	})
}

// TestGenerateVideoKeyframesRecoveredFromTriage pins the keyframe-intent
// recovery (conv_3bf4561b7c7dbb19316e2936): triage routed the turn as a
// start→end transition, the planner authored the call without imageRole, and
// the bare two-image call degraded to the reference diagnosis — both frames
// rode as loose guidance and the clip's ending was never anchored to the end
// frame. When the triage toolTask names keyframes and the call expresses no
// role of its own, the executor binds the transition; the planner's explicit
// choices always win.
func TestGenerateVideoKeyframesRecoveredFromTriage(t *testing.T) {
	const (
		keyframeModel = "fal-ai/test/keyframe"
		imageModel    = "fal-ai/test/image-to-video"
		firstFrame    = "data:image/png;base64,AAA"
		lastFrame     = "data:image/png;base64,BBB"
		triageTask    = "generate_video with imageRole: 'keyframes' using @ast_1 as the opening frame and @img_2 as the closing frame"
	)

	run := func(t *testing.T, call HarnessToolCall, exec HarnessToolExecutionContext) (VideoGenerateRequest, []string, string, error) {
		t.Helper()
		config := defaultAppConfig()
		config.Providers.Fal.VideoKeyframeModel = keyframeModel
		config.Providers.Fal.VideoImageModel = imageModel
		exec.Config = config
		var captured VideoGenerateRequest
		exec.GenerateVideo = func(ctx context.Context, req VideoGenerateRequest) (GeneratedVideo, error) {
			captured = req
			return GeneratedVideo{Data: []byte("mp4"), MimeType: "video/mp4", SourceURL: "https://example.com/v.mp4"}, nil
		}
		def := videoGenerationToolDefinition(AppConfig{}, false)
		result, summary, err := def.Execute(context.Background(), exec, call)
		var diags []string
		if typed, ok := result.(ToolVideoResult); ok {
			diags = typed.Diagnostics
		}
		return captured, diags, summary, err
	}

	t.Run("triage naming keyframes binds the dropped role", func(t *testing.T) {
		req, diags, summary, err := run(t,
			HarnessToolCall{Name: "generate_video", Content: "morph the first into the last"},
			HarnessToolExecutionContext{
				AttachedImages: []string{firstFrame, lastFrame},
				TriageTask:     triageTask,
			})
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if !req.Keyframes {
			t.Fatalf("captured.Keyframes = false, want the triage intent recovered")
		}
		if req.Model != keyframeModel {
			t.Fatalf("captured.Model = %q, want the keyframe slot %q", req.Model, keyframeModel)
		}
		imgs := req.SourceImages()
		if len(imgs) != 2 || imgs[0] != firstFrame || imgs[1] != lastFrame {
			t.Fatalf("forwarded images = %v, want the ordered pair", imgs)
		}
		foundDiag := false
		for _, d := range diags {
			if strings.Contains(d, "imageRole") && strings.Contains(d, "triaged as a start→end transition") {
				foundDiag = true
			}
		}
		if !foundDiag {
			t.Fatalf("diagnostics %v, want one naming the recovered role", diags)
		}
		if !strings.Contains(summary, "start→end transition") {
			t.Fatalf("summary = %q, want it to name the transition", summary)
		}
	})

	t.Run("openingFrame swaps the pair under recovery", func(t *testing.T) {
		req, _, _, err := run(t,
			HarnessToolCall{Name: "generate_video", Content: "morph", OpeningFrame: "second"},
			HarnessToolExecutionContext{
				AttachedImages: []string{firstFrame, lastFrame},
				TriageTask:     triageTask,
			})
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if !req.Keyframes {
			t.Fatalf("captured.Keyframes = false, want recovered")
		}
		imgs := req.SourceImages()
		if len(imgs) != 2 || imgs[0] != lastFrame || imgs[1] != firstFrame {
			t.Fatalf("forwarded images = %v, want the swapped pair", imgs)
		}
	})

	t.Run("an explicit reference judgment wins over the triage prose", func(t *testing.T) {
		req, diags, summary, err := run(t,
			HarnessToolCall{Name: "generate_video", Content: "a new scene with these characters", UseVideoAs: "reference"},
			HarnessToolExecutionContext{
				AttachedImages: []string{firstFrame, lastFrame},
				TriageTask:     triageTask,
			})
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if req.Keyframes {
			t.Fatalf("captured.Keyframes = true, want useVideoAs:\"reference\" to suppress the recovery")
		}
		if req.Model != imageModel {
			t.Fatalf("captured.Model = %q, want the image-to-video slot %q", req.Model, imageModel)
		}
		if len(diags) != 0 {
			t.Fatalf("diagnostics = %v, want none without the recovery", diags)
		}
		if strings.Contains(summary, "transition") {
			t.Fatalf("summary = %q, want the reference phrasing", summary)
		}
	})

	t.Run("an explicit source wins over the triage prose", func(t *testing.T) {
		req, _, _, err := run(t,
			HarnessToolCall{Name: "generate_video", Content: "animate", Source: "image"},
			HarnessToolExecutionContext{
				AttachedImages: []string{firstFrame, lastFrame},
				TriageTask:     triageTask,
			})
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if req.Keyframes {
			t.Fatalf("captured.Keyframes = true, want source:\"image\" to suppress the recovery")
		}
	})

	t.Run("a triage task without the transition vocabulary does not bind", func(t *testing.T) {
		req, _, _, err := run(t,
			HarnessToolCall{Name: "generate_video", Content: "a drone shot"},
			HarnessToolExecutionContext{
				AttachedImages: []string{firstFrame, lastFrame},
				TriageTask:     "generate_video for a cinematic aerial establishing shot",
			})
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if req.Keyframes {
			t.Fatalf("captured.Keyframes = true, want no recovery without the keyframes wording")
		}
	})

	t.Run("an empty triage task does not bind", func(t *testing.T) {
		req, _, _, err := run(t,
			HarnessToolCall{Name: "generate_video", Content: "a drone shot"},
			HarnessToolExecutionContext{AttachedImages: []string{firstFrame, lastFrame}})
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if req.Keyframes {
			t.Fatalf("captured.Keyframes = true, want no recovery on the direct path (no triage)")
		}
	})

	t.Run("an attached video keeps the motion-control diagnosis", func(t *testing.T) {
		req, _, _, err := run(t,
			HarnessToolCall{Name: "generate_video", Content: "move"},
			HarnessToolExecutionContext{
				AttachedImages: []string{firstFrame, lastFrame},
				AttachedVideos: []string{"data:video/mp4;base64,CCC"},
				TriageTask:     triageTask,
			})
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if req.Keyframes {
			t.Fatalf("captured.Keyframes = true, want no recovery beside an attached video")
		}
	})

	t.Run("three attached images do not recover", func(t *testing.T) {
		// The recovery requires the two-image shape; a wider set is not a
		// transition ask, so the default reference diagnosis stands. Only a
		// PLANNER-passed keyframes flag trips the two-image guard.
		req, diags, _, err := run(t,
			HarnessToolCall{Name: "generate_video", Content: "morph"},
			HarnessToolExecutionContext{
				AttachedImages: []string{firstFrame, lastFrame, "data:image/png;base64,DDD"},
				TriageTask:     triageTask,
			})
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if req.Keyframes {
			t.Fatalf("captured.Keyframes = true, want no recovery off the two-image shape")
		}
		if len(diags) != 0 {
			t.Fatalf("diagnostics = %v, want none without the recovery", diags)
		}
	})
}

// TestGenerateVideoKeyframesSlotFallback pins the frame-less keyframes-pin
// fallback (conv_c10e9937ac0dba41911a64f5): the recovered transition landed on
// the pinned keyframes model, whose schema declares no frame fields, degraded
// to loose references, and the paid clip again ignored the end frame. The
// resolver now refuses such a request before any money moves
// (errFalKeyframesUnsupported) and the executor retargets the transition onto
// the frame-capable image-to-video slot once, under a notice naming the swap.
func TestGenerateVideoKeyframesSlotFallback(t *testing.T) {
	const (
		refusedModel = "fal-ai/test/keyframe-frameless"
		imageModel   = "fal-ai/test/image-to-video"
		firstFrame   = "data:image/png;base64,AAA"
		lastFrame    = "data:image/png;base64,BBB"
		triageTask   = "generate_video with imageRole:\"keyframes\", openingFrame:\"first\""
	)

	t.Run("a refused transition retargets onto the image-to-video slot", func(t *testing.T) {
		var requests []VideoGenerateRequest
		config := defaultAppConfig()
		config.Providers.Fal.VideoKeyframeModel = refusedModel
		config.Providers.Fal.VideoImageModel = imageModel
		def := videoGenerationToolDefinition(AppConfig{}, false)
		exec := HarnessToolExecutionContext{
			Config:         config,
			AttachedImages: []string{firstFrame, lastFrame},
			TriageTask:     triageTask,
			GenerateVideo: func(ctx context.Context, req VideoGenerateRequest) (GeneratedVideo, error) {
				requests = append(requests, req)
				if req.Model == refusedModel {
					return GeneratedVideo{}, fmt.Errorf("model %q: %w", req.Model, errFalKeyframesUnsupported)
				}
				return GeneratedVideo{Data: []byte("mp4"), MimeType: "video/mp4", SourceURL: "https://example.com/v.mp4"}, nil
			},
		}
		result, summary, err := def.Execute(context.Background(), exec,
			HarnessToolCall{Name: "generate_video", Content: "morph the first into the last"})
		if err != nil {
			t.Fatalf("expected the fallback to succeed, got %v", err)
		}
		if len(requests) != 2 {
			t.Fatalf("GenerateVideo calls = %d, want the refused attempt plus the fallback", len(requests))
		}
		if requests[0].Model != refusedModel || requests[1].Model != imageModel {
			t.Fatalf("call models = [%q, %q], want %q then %q", requests[0].Model, requests[1].Model, refusedModel, imageModel)
		}
		if !requests[1].Keyframes {
			t.Fatalf("fallback request lost the keyframes flag")
		}
		imgs := requests[1].SourceImages()
		if len(imgs) != 2 || imgs[0] != firstFrame || imgs[1] != lastFrame {
			t.Fatalf("fallback images = %v, want the ordered pair", imgs)
		}
		typed, ok := result.(ToolVideoResult)
		if !ok {
			t.Fatalf("result type %T, want ToolVideoResult", result)
		}
		found := false
		for _, n := range typed.Notices {
			if strings.Contains(n, "ran on the image-to-video model") && strings.Contains(n, "Settings") {
				found = true
			}
		}
		if !found {
			t.Fatalf("notices = %v, want one naming the fallback swap and remedy", typed.Notices)
		}
		if typed.Model != imageModel {
			t.Fatalf("result model = %q, want the fallback %q", typed.Model, imageModel)
		}
		if !strings.Contains(summary, "start→end transition") || !strings.Contains(summary, imageModel) {
			t.Fatalf("summary = %q, want the transition named on the fallback model", summary)
		}
	})

	t.Run("a planner-specified model is respected with no retry", func(t *testing.T) {
		calls := 0
		config := defaultAppConfig()
		config.Providers.Fal.VideoKeyframeModel = refusedModel
		config.Providers.Fal.VideoImageModel = imageModel
		def := videoGenerationToolDefinition(AppConfig{}, false)
		exec := HarnessToolExecutionContext{
			Config:         config,
			AttachedImages: []string{firstFrame, lastFrame},
			GenerateVideo: func(ctx context.Context, req VideoGenerateRequest) (GeneratedVideo, error) {
				calls++
				return GeneratedVideo{}, fmt.Errorf("model %q: %w", req.Model, errFalKeyframesUnsupported)
			},
		}
		_, _, err := def.Execute(context.Background(), exec,
			HarnessToolCall{Name: "generate_video", Content: "morph", ImageRole: "keyframes", Model: "fal-ai/planner-picked"})
		if err == nil {
			t.Fatalf("expected the refusal to surface, got nil")
		}
		if !errors.Is(err, errFalKeyframesUnsupported) {
			t.Fatalf("error = %v, want the sentinel", err)
		}
		if calls != 1 {
			t.Fatalf("GenerateVideo calls = %d, want exactly the planner-specified attempt", calls)
		}
	})

	t.Run("a sibling video in the slots stands the retry down", func(t *testing.T) {
		calls := 0
		config := defaultAppConfig()
		config.Providers.Fal.VideoKeyframeModel = refusedModel
		config.Providers.Fal.VideoImageModel = imageModel
		def := videoGenerationToolDefinition(AppConfig{}, false)
		exec := HarnessToolExecutionContext{
			Config:         config,
			AttachedImages: []string{firstFrame, lastFrame},
			AttachedVideos: []string{"data:video/mp4;base64,CCC"},
			GenerateVideo: func(ctx context.Context, req VideoGenerateRequest) (GeneratedVideo, error) {
				calls++
				return GeneratedVideo{}, fmt.Errorf("model %q: %w", req.Model, errFalKeyframesUnsupported)
			},
		}
		_, _, err := def.Execute(context.Background(), exec,
			HarnessToolCall{Name: "generate_video", Content: "morph", ImageRole: "keyframes"})
		if err == nil {
			t.Fatalf("expected the refusal to surface, got nil")
		}
		if calls != 1 {
			t.Fatalf("GenerateVideo calls = %d, want no retry beside a sibling clip", calls)
		}
	})

	t.Run("a fallback that refuses too returns the combined remedy", func(t *testing.T) {
		config := defaultAppConfig()
		config.Providers.Fal.VideoKeyframeModel = refusedModel
		config.Providers.Fal.VideoImageModel = imageModel
		def := videoGenerationToolDefinition(AppConfig{}, false)
		exec := HarnessToolExecutionContext{
			Config:         config,
			AttachedImages: []string{firstFrame, lastFrame},
			GenerateVideo: func(ctx context.Context, req VideoGenerateRequest) (GeneratedVideo, error) {
				return GeneratedVideo{}, fmt.Errorf("model %q: %w", req.Model, errFalKeyframesUnsupported)
			},
		}
		_, _, err := def.Execute(context.Background(), exec,
			HarnessToolCall{Name: "generate_video", Content: "morph", ImageRole: "keyframes"})
		if err == nil {
			t.Fatalf("expected the combined refusal, got nil")
		}
		for _, want := range []string{refusedModel, imageModel, "Settings"} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("error = %v, want it to name %q", err, want)
			}
		}
	})
}

// TestGenerateVideoKeyframesRouteOnRunware pins the runware keyframe lift: a
// two-image transition is no longer refused on the runware backend — it rides
// the image-to-video slot (the same packaging declares t2v, i2v, and the
// two-frame inputs.frameImages — Kling 4.0, MiniMax H3) — while motion control
// and video-reference turns keep refusing up front
// (errRunwareVideoSourceUnsupported).
func TestGenerateVideoKeyframesRouteOnRunware(t *testing.T) {
	const (
		imageModel = "klingai:test@4.0"
		firstFrame = "data:image/png;base64,AAA"
		lastFrame  = "data:image/png;base64,BBB"
	)

	t.Run("keyframes route to the runware image-to-video slot", func(t *testing.T) {
		config := defaultAppConfig()
		config.Models.VideoProvider = "runware"
		config.Providers.Runware.VideoImageModel = imageModel

		var captured VideoGenerateRequest
		called := false
		def := videoGenerationToolDefinition(AppConfig{}, false)
		exec := HarnessToolExecutionContext{
			Config:         config,
			AttachedImages: []string{firstFrame, lastFrame},
			GenerateVideo: func(ctx context.Context, req VideoGenerateRequest) (GeneratedVideo, error) {
				captured = req
				called = true
				return GeneratedVideo{Data: []byte("mp4"), MimeType: "video/mp4", SourceURL: "https://example.com/v.mp4"}, nil
			},
		}
		call := HarnessToolCall{Name: "generate_video", Content: "morph the first into the last", ImageRole: "keyframes"}
		if _, _, err := def.Execute(context.Background(), exec, call); err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if !called {
			t.Fatalf("GenerateVideo was not called")
		}
		if !captured.Keyframes {
			t.Fatalf("captured.Keyframes = false, want true")
		}
		if captured.Model != imageModel {
			t.Fatalf("captured.Model = %q, want the runware image-to-video slot %q", captured.Model, imageModel)
		}
		imgs := captured.SourceImages()
		if len(imgs) != 2 || imgs[0] != firstFrame || imgs[1] != lastFrame {
			t.Fatalf("forwarded images = %v, want the ordered pair", imgs)
		}
	})

	// A single image the planner marked as a reference reaches the resolver
	// with its role intact: on this backend the role is what routes the image
	// to referenceImages instead of the opening frame (runware_params.go).
	t.Run("a reference turn forwards the role beside a single image", func(t *testing.T) {
		config := defaultAppConfig()
		config.Models.VideoProvider = "runware"
		config.Providers.Runware.VideoImageModel = imageModel

		var captured VideoGenerateRequest
		def := videoGenerationToolDefinition(AppConfig{}, false)
		exec := HarnessToolExecutionContext{
			Config:         config,
			AttachedImages: []string{firstFrame},
			GenerateVideo: func(ctx context.Context, req VideoGenerateRequest) (GeneratedVideo, error) {
				captured = req
				return GeneratedVideo{Data: []byte("mp4"), MimeType: "video/mp4", SourceURL: "https://example.com/v.mp4"}, nil
			},
		}
		call := HarnessToolCall{Name: "generate_video", Content: "this character walking through Paris", UseVideoAs: "reference"}
		if _, _, err := def.Execute(context.Background(), exec, call); err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if captured.VideoRole != "reference" {
			t.Fatalf("captured.VideoRole = %q, want the reference role forwarded", captured.VideoRole)
		}
		if captured.Keyframes {
			t.Fatal("captured.Keyframes = true, want false on a one-image reference turn")
		}
		if captured.Model != imageModel {
			t.Fatalf("captured.Model = %q, want the runware image-to-video slot %q", captured.Model, imageModel)
		}
	})

	t.Run("motion and video references still refuse", func(t *testing.T) {
		config := defaultAppConfig()
		config.Models.VideoProvider = "runware"

		for _, tc := range []struct {
			name string
			call HarnessToolCall
		}{
			{name: "motion control", call: HarnessToolCall{Name: "generate_video", Content: "apply the motion"}},
			{name: "video reference", call: HarnessToolCall{Name: "generate_video", Content: "new clip", UseVideoAs: "reference"}},
		} {
			called := false
			def := videoGenerationToolDefinition(AppConfig{}, false)
			exec := HarnessToolExecutionContext{
				Config:         config,
				AttachedImages: []string{firstFrame},
				AttachedVideos: []string{"data:video/mp4;base64,AAAA"},
				GenerateVideo: func(ctx context.Context, req VideoGenerateRequest) (GeneratedVideo, error) {
					called = true
					return GeneratedVideo{Data: []byte("mp4"), MimeType: "video/mp4"}, nil
				},
			}
			_, _, err := def.Execute(context.Background(), exec, tc.call)
			if !errors.Is(err, errRunwareVideoSourceUnsupported) {
				t.Fatalf("%s: expected errRunwareVideoSourceUnsupported, got %v", tc.name, err)
			}
			if called {
				t.Fatalf("%s: GenerateVideo must not run on a refused turn", tc.name)
			}
		}
	})
}

// TestGenerateVideoRunwareDeliveredShape pins the check that closes the loop
// on an explicit aspect ratio: a size the resolver sent is not yet a shape
// the model delivered (Runware enforces its schemas' rules for some models
// and not others), so the measured clip is compared with the ask and a
// disagreement is said out loud. A preset's rounding is not a disagreement,
// and other backends keep their own notices.
func TestGenerateVideoRunwareDeliveredShape(t *testing.T) {
	const frame = "data:image/png;base64,AAA"
	run := func(t *testing.T, provider, aspect string, delivered []byte) ToolVideoResult {
		t.Helper()
		config := defaultAppConfig()
		config.Models.VideoProvider = provider
		def := videoGenerationToolDefinition(AppConfig{}, false)
		exec := HarnessToolExecutionContext{
			Config:         config,
			AttachedImages: []string{frame},
			GenerateVideo: func(ctx context.Context, req VideoGenerateRequest) (GeneratedVideo, error) {
				return GeneratedVideo{Data: delivered, MimeType: "video/mp4"}, nil
			},
		}
		result, _, err := def.Execute(context.Background(), exec, HarnessToolCall{Name: "generate_video", Content: "animate", AspectRatio: aspect})
		if err != nil {
			t.Fatalf("execute: %v", err)
		}
		video, ok := result.(ToolVideoResult)
		if !ok {
			t.Fatalf("result = %T, want ToolVideoResult", result)
		}
		return video
	}

	// Honoured: the 9:16 ask came back 1080x1920 (Kling 4.0, live
	// 2026-11-24), and nothing needs saying.
	if video := run(t, "runware", "9:16", mediasnifftest.MP4WithVideoTrack(1080, 1920)); len(video.Notices) != 0 {
		t.Fatalf("a delivered shape that matches the ask needs no notice, got %v", video.Notices)
	}
	// A preset's rounding is not a different shape: MiniMax H3's 9:16 class
	// renders 480x864.
	if video := run(t, "runware", "9:16", mediasnifftest.MP4WithVideoTrack(480, 864)); len(video.Notices) != 0 {
		t.Fatalf("a preset's rounding is not a disagreement, got %v", video.Notices)
	}
	// Ignored: the model kept the frame's square shape.
	video := run(t, "runware", "9:16", mediasnifftest.MP4WithVideoTrack(720, 720))
	if len(video.Notices) != 1 || !strings.Contains(video.Notices[0], "Requested a 9:16 video") || !strings.Contains(video.Notices[0], "delivered 720x720") {
		t.Fatalf("a shape that disagrees with the ask must be said, got %v", video.Notices)
	}
	// Without an explicit ask there is nothing to compare against.
	if video := run(t, "runware", "", mediasnifftest.MP4WithVideoTrack(720, 720)); len(video.Notices) != 0 {
		t.Fatalf("no explicit ask, no dispute, got %v", video.Notices)
	}
	// Another backend keeps its own notices.
	if video := run(t, "fal", "9:16", mediasnifftest.MP4WithVideoTrack(720, 720)); len(video.Notices) != 0 {
		t.Fatalf("the check is scoped to the backend it was verified on, got %v", video.Notices)
	}
}

// TestGenerateVideoOpeningFrameValidation pins the guards for openingFrame: it
// is keyframes-only (any other imageRole is a plan correction), enum-checked to
// "first"/"second", and valid values pass.
func TestGenerateVideoOpeningFrameValidation(t *testing.T) {
	def := videoGenerationToolDefinition(AppConfig{}, false)
	for _, opening := range []string{"first", "second"} {
		valid := HarnessToolCall{Name: "generate_video", Content: "morph", ImageRole: "keyframes", OpeningFrame: opening}
		if errs := def.Validate("toolCalls[0]", valid); len(errs) != 0 {
			t.Fatalf("openingFrame %q with keyframes should validate, got %v", opening, errs)
		}
	}
	badRole := HarnessToolCall{Name: "generate_video", Content: "morph", OpeningFrame: "second"}
	if errs := def.Validate("toolCalls[0]", badRole); len(errs) == 0 {
		t.Fatalf("openingFrame without imageRole \"keyframes\" should be rejected")
	}
	badEnum := HarnessToolCall{Name: "generate_video", Content: "morph", ImageRole: "keyframes", OpeningFrame: "last"}
	errs := def.Validate("toolCalls[0]", badEnum)
	found := false
	for _, e := range errs {
		if strings.Contains(e, "openingFrame") {
			found = true
		}
	}
	if !found {
		t.Fatalf("openingFrame \"last\" should be rejected with an openingFrame correction, got %v", errs)
	}
}

// TestGenerateVideoOpeningFrameSwapsKeyframes pins the executor contract for
// openingFrame:"second": the two-image pair is swapped before the request is
// built, so the resolver's start/end split (first → start_image_url, last →
// end_image_url) opens on the second image. The slot order is attachments then
// @-mentions (resolveTurnMedia), which need not match the order the user's
// words name the frames — conv_0f9cc90f61402bae64acf707 named the mention as
// the start frame, but the mention resolved second and the generated clip ran
// the transition backwards. The default (no openingFrame) keeps slot order.
func TestGenerateVideoOpeningFrameSwapsKeyframes(t *testing.T) {
	const (
		keyframeModel = "fal-ai/test/keyframe"
		firstFrame    = "data:image/png;base64,AAA"
		lastFrame     = "data:image/png;base64,BBB"
	)

	capture := func(call HarnessToolCall) VideoGenerateRequest {
		config := defaultAppConfig()
		config.Providers.Fal.VideoKeyframeModel = keyframeModel
		var captured VideoGenerateRequest
		def := videoGenerationToolDefinition(AppConfig{}, false)
		exec := HarnessToolExecutionContext{
			Config:         config,
			AttachedImages: []string{firstFrame, lastFrame},
			GenerateVideo: func(ctx context.Context, req VideoGenerateRequest) (GeneratedVideo, error) {
				captured = req
				return GeneratedVideo{Data: []byte("mp4"), MimeType: "video/mp4", SourceURL: "https://example.com/v.mp4"}, nil
			},
		}
		if _, _, err := def.Execute(context.Background(), exec, call); err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		return captured
	}

	t.Run("openingFrame second opens on the second image", func(t *testing.T) {
		captured := capture(HarnessToolCall{Name: "generate_video", Content: "morph", ImageRole: "keyframes", OpeningFrame: "second"})
		imgs := captured.SourceImages()
		if len(imgs) != 2 || imgs[0] != lastFrame || imgs[1] != firstFrame {
			t.Fatalf("forwarded images = %v, want [%s %s]", imgs, lastFrame, firstFrame)
		}
	})

	t.Run("default keeps slot order", func(t *testing.T) {
		captured := capture(HarnessToolCall{Name: "generate_video", Content: "morph", ImageRole: "keyframes"})
		imgs := captured.SourceImages()
		if len(imgs) != 2 || imgs[0] != firstFrame || imgs[1] != lastFrame {
			t.Fatalf("forwarded images = %v, want [%s %s]", imgs, firstFrame, lastFrame)
		}
	})
}

// TestGenerateVideoParamSchemaDocumentsUseVideoAs pins the planner-facing
// contract: the param schema must expose useVideoAs with exactly the motion and
// reference enum values and a description that says what each interpretation
// does, so the planner can make the source-vs-reference call itself.
func TestGenerateVideoParamSchemaDocumentsUseVideoAs(t *testing.T) {
	schema := generateVideoParamSchema()
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("expected properties map, got %T", schema["properties"])
	}
	prop, ok := props["useVideoAs"].(map[string]any)
	if !ok {
		t.Fatalf("generate_video param schema must expose a `useVideoAs` property; got %v", props)
	}
	enum, _ := prop["enum"].([]string)
	if len(enum) != 2 || enum[0] != "motion" || enum[1] != "reference" {
		t.Fatalf("useVideoAs enum = %v, want [motion reference]", prop["enum"])
	}
	desc, _ := prop["description"].(string)
	if !strings.Contains(desc, "reference") || !strings.Contains(desc, "motion") {
		t.Fatalf("useVideoAs description = %q, want it to explain both interpretations", desc)
	}
	if !strings.Contains(videoGenerationDescription(false), "useVideoAs") {
		t.Fatalf("generate_video description must surface the useVideoAs choice to the planner")
	}
}

// TestGenerateVideoParamSchemaDocumentsOpeningFrame pins the planner-facing
// contract for the keyframe ordering override: openingFrame is declared with
// the first/second enum and a description that teaches the slot-resolution
// order, and the tool description surfaces it beside the keyframes sentence so
// a planner reading only the description still learns the rule.
func TestGenerateVideoParamSchemaDocumentsOpeningFrame(t *testing.T) {
	schema := generateVideoParamSchema()
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("expected properties map, got %T", schema["properties"])
	}
	prop, ok := props["openingFrame"].(map[string]any)
	if !ok {
		t.Fatalf("generate_video param schema must expose an `openingFrame` property; got %v", props)
	}
	enum, _ := prop["enum"].([]string)
	if len(enum) != 2 || enum[0] != "first" || enum[1] != "second" {
		t.Fatalf("openingFrame enum = %v, want [first second]", prop["enum"])
	}
	desc, _ := prop["description"].(string)
	if !strings.Contains(desc, "attached files first") || !strings.Contains(desc, "second") {
		t.Fatalf("openingFrame description = %q, want it to teach the attachment-then-mention order", desc)
	}
	if !strings.Contains(videoGenerationDescription(false), "openingFrame") {
		t.Fatalf("generate_video description must surface the openingFrame choice to the planner")
	}
}

// TestGenerateVideoSourceRouting pins the planner's source override: when the
// user's words point at one kind and the attachments imply another — "animate
// the image" on a turn whose fallback slot carries the newest artifact, a
// video — source:"image"/"video" makes the named kind drive the clip, drops
// the other side with a notice, and re-reads the named kind from conversation
// history when the slots lack it. An unfulfillable source fails the call
// rather than generating a clip nobody asked for.
func TestGenerateVideoSourceRouting(t *testing.T) {
	const (
		imageModel  = "bytedance/seedance-2.5/reference-to-video"
		motionModel = "fal-ai/kling-video/v2.6/pro/motion-control"
		extendModel = "fal-ai/veo3.1/extend-video"
	)

	videoConfig := func() AppConfig {
		config := defaultAppConfig()
		config.Providers.Fal.VideoImageModel = imageModel
		config.Providers.Fal.VideoMotionModel = motionModel
		config.Providers.Fal.VideoExtendModel = extendModel
		return config
	}

	run := func(images, videos []string, storage ConfigStorage, conversationID string, call HarnessToolCall) (VideoGenerateRequest, ToolVideoResult, string, error) {
		var gotReq VideoGenerateRequest
		def := videoGenerationToolDefinition(AppConfig{}, false)
		exec := HarnessToolExecutionContext{
			Config:         videoConfig(),
			AttachedImages: images,
			AttachedVideos: videos,
			Storage:        storage,
			ConversationID: conversationID,
			GenerateVideo: func(ctx context.Context, req VideoGenerateRequest) (GeneratedVideo, error) {
				gotReq = req
				return GeneratedVideo{Data: []byte("mp4"), MimeType: "video/mp4", SourceURL: "https://example.com/v.mp4"}, nil
			},
		}
		result, summary, err := def.Execute(context.Background(), exec, call)
		typed, _ := result.(ToolVideoResult)
		return gotReq, typed, summary, err
	}

	t.Run("source image drops the attached video and routes image-to-video", func(t *testing.T) {
		call := HarnessToolCall{Name: "generate_video", Content: "gentle candle flicker", Source: "image"}
		gotReq, result, summary, err := run([]string{"data:image/png;base64,AAA"}, []string{"data:video/mp4;base64,BBB"}, ConfigStorage{}, "", call)
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if gotReq.Model != imageModel {
			t.Fatalf("model = %q, want the image-to-video model %q", gotReq.Model, imageModel)
		}
		if len(gotReq.SourceImages()) != 1 || len(gotReq.SourceVideos()) != 0 {
			t.Fatalf("request sources = %d image(s) / %d video(s), want 1/0", len(gotReq.SourceImages()), len(gotReq.SourceVideos()))
		}
		if summary != "animated the attached image into a video with "+imageModel {
			t.Fatalf("summary = %q, want the image-to-video phrasing", summary)
		}
		foundNotice := false
		for _, notice := range result.Notices {
			if strings.Contains(notice, `the attached video was not used`) {
				foundNotice = true
			}
		}
		if !foundNotice {
			t.Fatalf("notices = %v, want one saying the attached video was not used", result.Notices)
		}
	})

	t.Run("source video drops the attached image and routes extend", func(t *testing.T) {
		call := HarnessToolCall{Name: "generate_video", Content: "continue the flight", Source: "video"}
		gotReq, _, summary, err := run([]string{"data:image/png;base64,AAA"}, []string{"data:video/mp4;base64,BBB"}, ConfigStorage{}, "", call)
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if gotReq.Model != extendModel {
			t.Fatalf("model = %q, want the extend model %q", gotReq.Model, extendModel)
		}
		if len(gotReq.SourceImages()) != 0 || len(gotReq.SourceVideos()) != 1 {
			t.Fatalf("request sources = %d image(s) / %d video(s), want 0/1", len(gotReq.SourceImages()), len(gotReq.SourceVideos()))
		}
		if summary != "extended the attached video into a longer clip with "+extendModel {
			t.Fatalf("summary = %q, want the extend phrasing", summary)
		}
	})

	t.Run("source image fetches the history image when only a video is attached", func(t *testing.T) {
		// Video newest, so the turn's fallback slot carries the video while the
		// user asked to animate the image — the conv_150c5923 gap.
		storage, conversationID := writeConversationWithImageAndVideo(t, false)
		call := HarnessToolCall{Name: "generate_video", Content: "animate the watercolour", Source: "image"}
		gotReq, _, summary, err := run(nil, []string{"data:video/mp4;base64,BBB"}, storage, conversationID, call)
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if gotReq.Model != imageModel {
			t.Fatalf("model = %q, want the image-to-video model %q", gotReq.Model, imageModel)
		}
		if len(gotReq.SourceImages()) != 1 || !strings.HasPrefix(gotReq.SourceImages()[0], "data:image/png;base64,") {
			t.Fatalf("request images = %v, want the re-read history image", gotReq.SourceImages())
		}
		if len(gotReq.SourceVideos()) != 0 {
			t.Fatalf("request videos = %v, want none", gotReq.SourceVideos())
		}
		if summary != "animated the attached image into a video with "+imageModel {
			t.Fatalf("summary = %q, want the image-to-video phrasing", summary)
		}
	})

	t.Run("source video fetches the history video when only an image is attached", func(t *testing.T) {
		storage, conversationID := writeConversationWithImageAndVideo(t, true)
		call := HarnessToolCall{Name: "generate_video", Content: "extend the clip", Source: "video"}
		gotReq, _, _, err := run([]string{"data:image/png;base64,AAA"}, nil, storage, conversationID, call)
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if gotReq.Model != extendModel {
			t.Fatalf("model = %q, want the extend model %q", gotReq.Model, extendModel)
		}
		if len(gotReq.SourceVideos()) != 1 {
			t.Fatalf("request videos = %v, want the re-read history video", gotReq.SourceVideos())
		}
	})

	t.Run("source image with no image anywhere fails the call", func(t *testing.T) {
		call := HarnessToolCall{Name: "generate_video", Content: "animate it", Source: "image"}
		_, _, _, err := run(nil, []string{"data:video/mp4;base64,BBB"}, ConfigStorage{}, "", call)
		if err == nil || !strings.Contains(err.Error(), `source "image"`) {
			t.Fatalf("err = %v, want the unfulfillable-source error", err)
		}
	})

	t.Run("source motion with one side missing fails the call", func(t *testing.T) {
		call := HarnessToolCall{Name: "generate_video", Content: "transfer the motion", Source: "motion"}
		_, _, _, err := run([]string{"data:image/png;base64,AAA"}, nil, ConfigStorage{}, "", call)
		if err == nil || !strings.Contains(err.Error(), `source "motion"`) {
			t.Fatalf("err = %v, want the needs-both error", err)
		}
	})

	t.Run("reference mode wins over source", func(t *testing.T) {
		call := HarnessToolCall{Name: "generate_video", Content: "a new clip in this style", UseVideoAs: "reference", Source: "image"}
		gotReq, _, _, err := run([]string{"data:image/png;base64,AAA"}, []string{"data:video/mp4;base64,BBB"}, ConfigStorage{}, "", call)
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if gotReq.Model != imageModel || len(gotReq.SourceVideos()) != 1 {
			t.Fatalf("model = %q with %d video(s), want the reference model carrying the video too", gotReq.Model, len(gotReq.SourceVideos()))
		}
	})
}

// TestGenerateVideoSourceValidation pins the enum guard: an unknown source
// value is a plan correction, not a silent fallthrough to the default
// interpretation.
func TestGenerateVideoSourceValidation(t *testing.T) {
	def := videoGenerationToolDefinition(AppConfig{}, false)
	problems := def.Validate("toolCalls[0]", HarnessToolCall{Name: "generate_video", Content: "x", Source: "the neighbors"})
	if len(problems) != 1 || !strings.Contains(problems[0], "source must be") {
		t.Fatalf("problems = %v, want the source enum correction", problems)
	}
	for _, ok := range []string{"", "image", "video", "motion"} {
		if problems := def.Validate("toolCalls[0]", HarnessToolCall{Name: "generate_video", Content: "x", Source: ok}); len(problems) != 0 {
			t.Fatalf("source %q problems = %v, want none", ok, problems)
		}
	}
}

// TestGenerateVideoParamSchemaDocumentsSource pins the planner-facing
// contract: the param schema must expose source with the image/video/motion
// enum and a description that says when to set it — when the user's words and
// the attachments disagree about which media drives the clip.
func TestGenerateVideoParamSchemaDocumentsSource(t *testing.T) {
	schema := generateVideoParamSchema()
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("expected properties map, got %T", schema["properties"])
	}
	prop, ok := props["source"].(map[string]any)
	if !ok {
		t.Fatalf("generate_video param schema must expose a `source` property; got %v", props)
	}
	enum, _ := prop["enum"].([]string)
	if len(enum) != 3 || enum[0] != "image" || enum[1] != "video" || enum[2] != "motion" {
		t.Fatalf("source enum = %v, want [image video motion]", prop["enum"])
	}
	desc, _ := prop["description"].(string)
	if !strings.Contains(desc, "overriding") || !strings.Contains(desc, "conversation history") {
		t.Fatalf("source description = %q, want it to say it overrides the default routing and can fetch from history", desc)
	}
	if !strings.Contains(videoGenerationDescription(false), "source:") {
		t.Fatalf("generate_video description must surface the source choice to the planner")
	}
}

// TestLipsyncFaceFromRouting pins the planner-facing face choice: with both an
// image and a video attached, the video face wins by default (with a notice
// saying so), faceFrom:"image" drives the attached image instead, and the
// request carries exactly one face — LipsyncGenerateRequest's "exactly one of
// Image or Video" contract. A faceFrom naming a face that isn't attached is a
// hard error the planner can correct, not a silent fallthrough.
func TestLipsyncFaceFromRouting(t *testing.T) {
	const (
		lipsyncImageModel = "fal-ai/sync-lipsync/v3/image-to-video"
		lipsyncVideoModel = "google/gemini-omni-flash/edit"
		audioClip         = "data:audio/mpeg;base64,AAA"
		faceImage         = "data:image/png;base64,BBB"
		faceVideo         = "data:video/mp4;base64,CCC"
	)

	cases := []struct {
		name        string
		call        HarnessToolCall
		images      []string
		videos      []string
		wantModel   string
		wantImage   string
		wantVideo   string
		wantSummary string
		wantNotice  bool
	}{
		{
			name:        "both faces default to video with a notice",
			call:        HarnessToolCall{Name: "lip_sync"},
			images:      []string{faceImage},
			videos:      []string{faceVideo},
			wantModel:   lipsyncVideoModel,
			wantVideo:   faceVideo,
			wantSummary: "lip-synced the attached audio to a video with " + lipsyncVideoModel,
			wantNotice:  true,
		},
		{
			name:        "faceFrom image drives the attached image",
			call:        HarnessToolCall{Name: "lip_sync", FaceFrom: "image"},
			images:      []string{faceImage},
			videos:      []string{faceVideo},
			wantModel:   lipsyncImageModel,
			wantImage:   faceImage,
			wantSummary: "lip-synced the attached audio to a image with " + lipsyncImageModel,
		},
		{
			name:        "faceFrom video is explicit, no heuristic notice",
			call:        HarnessToolCall{Name: "lip_sync", FaceFrom: "video"},
			images:      []string{faceImage},
			videos:      []string{faceVideo},
			wantModel:   lipsyncVideoModel,
			wantVideo:   faceVideo,
			wantSummary: "lip-synced the attached audio to a video with " + lipsyncVideoModel,
		},
		{
			name:        "image only routes to the talking-head model",
			call:        HarnessToolCall{Name: "lip_sync"},
			images:      []string{faceImage},
			wantModel:   lipsyncImageModel,
			wantImage:   faceImage,
			wantSummary: "lip-synced the attached audio to a image with " + lipsyncImageModel,
		},
		{
			name:        "video only routes to the re-sync model",
			call:        HarnessToolCall{Name: "lip_sync"},
			videos:      []string{faceVideo},
			wantModel:   lipsyncVideoModel,
			wantVideo:   faceVideo,
			wantSummary: "lip-synced the attached audio to a video with " + lipsyncVideoModel,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			config := defaultAppConfig()
			config.Providers.Fal.LipsyncImageModel = lipsyncImageModel
			config.Providers.Fal.LipsyncVideoModel = lipsyncVideoModel

			var gotReq LipsyncGenerateRequest
			def := lipsyncToolDefinition(defaultAppConfig(), false)
			exec := HarnessToolExecutionContext{
				Config:         config,
				AttachedImages: tc.images,
				AttachedVideos: tc.videos,
				AttachedAudios: []string{audioClip},
				GenerateLipsync: func(ctx context.Context, req LipsyncGenerateRequest) (GeneratedVideo, error) {
					gotReq = req
					return GeneratedVideo{Data: []byte("mp4"), MimeType: "video/mp4"}, nil
				},
			}
			result, summary, err := def.Execute(context.Background(), exec, tc.call)
			if err != nil {
				t.Fatalf("expected no error, got %v", err)
			}
			if gotReq.Model != tc.wantModel {
				t.Fatalf("model = %q, want %q", gotReq.Model, tc.wantModel)
			}
			if gotReq.Image != tc.wantImage || gotReq.Video != tc.wantVideo {
				t.Fatalf("request face = image:%q video:%q, want image:%q video:%q (exactly one face)",
					gotReq.Image, gotReq.Video, tc.wantImage, tc.wantVideo)
			}
			if summary != tc.wantSummary {
				t.Fatalf("summary = %q, want %q", summary, tc.wantSummary)
			}
			typed, ok := result.(ToolVideoResult)
			if !ok {
				t.Fatalf("result type = %T, want ToolVideoResult", result)
			}
			hasShadowNotice := false
			for _, n := range typed.Notices {
				if strings.Contains(n, "faceFrom") {
					hasShadowNotice = true
				}
			}
			if hasShadowNotice != tc.wantNotice {
				t.Fatalf("faceFrom notice present = %v, want %v (notices: %v)", hasShadowNotice, tc.wantNotice, typed.Notices)
			}
		})
	}
}

// TestLipsyncFaceFromMissingFace pins the guard: a faceFrom naming a face that
// isn't attached fails the call with a correction the planner can act on.
func TestLipsyncFaceFromMissingFace(t *testing.T) {
	def := lipsyncToolDefinition(defaultAppConfig(), false)
	exec := HarnessToolExecutionContext{
		Config:         defaultAppConfig(),
		AttachedVideos: []string{"data:video/mp4;base64,CCC"},
		AttachedAudios: []string{"data:audio/mpeg;base64,AAA"},
		GenerateLipsync: func(ctx context.Context, req LipsyncGenerateRequest) (GeneratedVideo, error) {
			t.Fatal("GenerateLipsync must not run when the requested face is missing")
			return GeneratedVideo{}, nil
		},
	}
	_, _, err := def.Execute(context.Background(), exec, HarnessToolCall{Name: "lip_sync", FaceFrom: "image"})
	if err == nil || !strings.Contains(err.Error(), `faceFrom is "image" but no image is attached`) {
		t.Fatalf("error = %v, want the missing-image-face correction", err)
	}
}

// TestLipsyncFaceFromValidation pins the enum guard: an unknown faceFrom value
// is a plan correction, not a silent fallthrough to the default face.
func TestLipsyncFaceFromValidation(t *testing.T) {
	def := lipsyncToolDefinition(defaultAppConfig(), false)
	problems := def.Validate("toolCalls[0]", HarnessToolCall{Name: "lip_sync", FaceFrom: "photograph"})
	if len(problems) != 1 || !strings.Contains(problems[0], "faceFrom must be") {
		t.Fatalf("problems = %v, want the faceFrom enum correction", problems)
	}
	for _, ok := range []string{"", "image", "video"} {
		if problems := def.Validate("toolCalls[0]", HarnessToolCall{Name: "lip_sync", FaceFrom: ok}); len(problems) != 0 {
			t.Fatalf("faceFrom %q problems = %v, want none", ok, problems)
		}
	}
}

// TestLipsyncParamSchemaDocumentsFaceFrom pins the planner-facing contract:
// the param schema exposes faceFrom with exactly the image and video enum
// values, and the tool description surfaces the choice.
func TestLipsyncParamSchemaDocumentsFaceFrom(t *testing.T) {
	schema := lipsyncParamSchema()
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("expected properties map, got %T", schema["properties"])
	}
	prop, ok := props["faceFrom"].(map[string]any)
	if !ok {
		t.Fatalf("lip_sync param schema must expose a `faceFrom` property; got %v", props)
	}
	enum, _ := prop["enum"].([]string)
	if len(enum) != 2 || enum[0] != "image" || enum[1] != "video" {
		t.Fatalf("faceFrom enum = %v, want [image video]", prop["enum"])
	}
	if !strings.Contains(lipsyncDescription(false), "faceFrom") {
		t.Fatalf("lip_sync description must surface the faceFrom choice to the planner")
	}
}

// TestImageGenerationDescriptionFramesAttachmentsAsReferences pins the
// planner-facing framing of the attached-image path: an attachment is a
// reference the prompt directs — transform OR create anew — not a
// transform-only input. The old transform-only wording biased planner prompts
// toward edit framing even when the user's attachment was a style/character
// reference for a brand-new image.
func TestImageGenerationDescriptionFramesAttachmentsAsReferences(t *testing.T) {
	desc := imageGenerationToolDefinition(defaultAppConfig()).Description
	for _, fragment := range []string{"reference the prompt directs", "transformation or restyle", "new creation guided by it"} {
		if !strings.Contains(desc, fragment) {
			t.Fatalf("generate_image description = %q, want it to include %q", desc, fragment)
		}
	}
}

// TestDefaultHarnessToolActivityCopiesCost pins the media-cost ledger: the
// generation result's estimated cost (stamped by the tool gateway from fal's
// pricing API) rides the persisted activity beside MediaKind/MediaCount, and
// transcription joins with cost but no media kind. Failed calls carry no
// payload, so they keep zero cost — matching how counts drop out.
func TestDefaultHarnessToolActivityCopiesCost(t *testing.T) {
	video := defaultHarnessToolActivity(HarnessToolResult{Result: ToolVideoResult{Model: "fal-ai/kling-video/v2/master/text-to-video", Count: 1, CostMicros: 350000}})
	if video.CostMicros != 350000 || video.MediaKind != "video" || video.Model != "fal-ai/kling-video/v2/master/text-to-video" {
		t.Fatalf("video activity = %+v, want cost 350000 on the media fields", video)
	}
	image := defaultHarnessToolActivity(HarnessToolResult{Result: ToolImageResult{Model: "fal-ai/flux/schnell", Count: 2, CostMicros: 6000}})
	if image.CostMicros != 6000 || image.MediaKind != "image" {
		t.Fatalf("image activity = %+v, want cost 6000", image)
	}
	transcript := defaultHarnessToolActivity(HarnessToolResult{Result: ToolTranscribeResult{Model: "fal-ai/wizper", CostMicros: 500}})
	if transcript.CostMicros != 500 || transcript.MediaKind != "" {
		t.Fatalf("transcript activity = %+v, want cost 500 with no media kind", transcript)
	}
	failed := defaultHarnessToolActivity(HarnessToolResult{Error: "boom"})
	if failed.CostMicros != 0 || failed.MediaKind != "" {
		t.Fatalf("failed activity = %+v, want no cost and no media fields", failed)
	}
}

// TestHarnessRunCostMicros pins the turn-total fold: model-call steps and tool
// activities sum, bookkeeping steps contribute nothing, so the persisted
// providerResponse.costMicros reconciles with what the usage rows display.
func TestHarnessRunCostMicros(t *testing.T) {
	run := HarnessRun{Steps: []HarnessStep{
		{Kind: "triage", CostMicros: 100},
		{Kind: "planning", CostMicros: 250},
		{Kind: "tool_call", Tools: []HarnessToolActivity{
			{Name: "generate_video", CostMicros: 350000},
			{Name: "transcribe_audio", CostMicros: 500},
		}},
		{Kind: "streaming", CostMicros: 243},
		{Kind: "saved"},
	}}
	if got := harnessRunCostMicros(run); got != 351093 {
		t.Fatalf("harnessRunCostMicros = %d, want 351093", got)
	}
	if got := harnessRunCostMicros(HarnessRun{}); got != 0 {
		t.Fatalf("empty run cost = %d, want 0", got)
	}
}

// TestTranscribeAudioSubtitleParamContract pins the planner-facing contract of
// the subtitle ask: the param schema declares `subtitle` with a description
// naming the file formats, the tool description teaches it unconditionally (a
// same-language "create subtitle" ask has no translate clause to lean on —
// conv_8ef7cf03a54ec7d30b62bce1's turn 6), and transcribeRequestTimestamps
// folds subtitle:true to segment timings so the whisper runner writes -ovtt
// and the .vtt artifact rides the reply.
func TestTranscribeAudioSubtitleParamContract(t *testing.T) {
	schema := transcribeAudioParamSchema()
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("expected properties map, got %T", schema["properties"])
	}
	prop, ok := props["subtitle"].(map[string]any)
	if !ok {
		t.Fatalf("transcribe_audio param schema must expose a `subtitle` property; got %v", props)
	}
	if prop["type"] != "boolean" {
		t.Fatalf("subtitle type = %v, want boolean", prop["type"])
	}
	desc, _ := prop["description"].(string)
	for _, want := range []string{"vtt", "srt", "captions"} {
		if !strings.Contains(desc, want) {
			t.Fatalf("subtitle description %q must name %q", desc, want)
		}
	}
	toolDesc := transcribeAudioDescription(transcriptionProviderFal)
	if !strings.Contains(toolDesc, "subtitle") {
		t.Fatalf("transcribe_audio description must surface the subtitle flag to the planner")
	}
	if !strings.Contains(toolDesc, "plain text transcript with no timing") {
		t.Fatalf("transcribe_audio description must state the consequence of omitting subtitle")
	}
}

// TestTranscribeRequestTimestampsFoldsSubtitle pins the fold: an explicit
// timestamps value wins (word-level stays word-level beside subtitle), and
// subtitle:true alone lands on segment-level — the granularity that becomes a
// subtitle file on every backend.
func TestTranscribeRequestTimestampsFoldsSubtitle(t *testing.T) {
	cases := []struct {
		name string
		call HarnessToolCall
		want string
	}{
		{"plain call", HarnessToolCall{}, ""},
		{"subtitle alone", HarnessToolCall{Subtitle: true}, "segments"},
		{"explicit segments", HarnessToolCall{Timestamps: "segments"}, "segments"},
		{"subtitle plus words", HarnessToolCall{Subtitle: true, Timestamps: "words"}, "words"},
		{"whitespace timestamps wins", HarnessToolCall{Subtitle: true, Timestamps: " words "}, "words"},
	}
	for _, tc := range cases {
		if got := transcribeRequestTimestamps(tc.call); got != tc.want {
			t.Errorf("%s: transcribeRequestTimestamps = %q, want %q", tc.name, got, tc.want)
		}
	}
	// The native tool-call dialect decodes by json tag, so the planner's
	// subtitle flag must land on the field the fold reads.
	var call HarnessToolCall
	if err := json.Unmarshal([]byte(`{"name":"transcribe_audio","subtitle":true}`), &call); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !call.Subtitle {
		t.Fatalf("native decode lost the subtitle flag: %+v", call)
	}
}

// TestHarnessToolPlanSchemaHasTranscribeParams pins the format-mode grammar:
// without task/language/timestamps/subtitle in the plan schema, a
// grammar-constrained planner cannot emit them at all and every transcription
// degrades to a plain same-language transcript.
func TestHarnessToolPlanSchemaHasTranscribeParams(t *testing.T) {
	schema := harnessToolPlanSchema(filesystemToolRegistry())
	items := schema["properties"].(map[string]any)["toolCalls"].(map[string]any)["items"].(map[string]any)
	properties := items["properties"].(map[string]any)
	if _, ok := properties["task"].(map[string]any); !ok {
		t.Errorf("plan schema properties missing %q", "task")
	}
	if _, ok := properties["language"].(map[string]any); !ok {
		t.Errorf("plan schema properties missing %q", "language")
	}
	if _, ok := properties["timestamps"].(map[string]any); !ok {
		t.Errorf("plan schema properties missing %q", "timestamps")
	}
	subtitle, ok := properties["subtitle"].(map[string]any)
	if !ok {
		t.Fatalf("plan schema properties missing %q", "subtitle")
	}
	if subtitle["type"] != "boolean" {
		t.Fatalf("plan schema subtitle type = %v, want boolean", subtitle["type"])
	}
}

// TestApplyKwargsSubtitleAndTimestamps covers the kwargs/XML recovery dialect:
// both keys must reach the fields the executor's fold reads, with subtitle
// parsed as a boolean.
func TestApplyKwargsSubtitleAndTimestamps(t *testing.T) {
	var call HarnessToolCall
	applyKwargs(&call, "task='translate', language='it', subtitle=true")
	if call.Task != "translate" || call.Language != "it" || !call.Subtitle {
		t.Fatalf("applyKwargs = %+v, want task/language/subtitle mapped", call)
	}
	if got := transcribeRequestTimestamps(call); got != "segments" {
		t.Fatalf("folded timestamps = %q, want segments", got)
	}
	var wordy HarnessToolCall
	applyKwargs(&wordy, "timestamps='words'")
	if wordy.Timestamps != "words" {
		t.Fatalf("timestamps kwarg = %q, want words", wordy.Timestamps)
	}
	if got := transcribeRequestTimestamps(wordy); got != "words" {
		t.Fatalf("folded timestamps = %q, want words", got)
	}
}
