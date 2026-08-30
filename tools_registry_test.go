package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
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
// what the resolver actually delivered, not what was attached: when the
// selected model drops an attached source with a "has no source-video input"
// notice, the summary must not claim a motion transfer that never carried the
// video. In conv_16bf42ce64997fad02f769a9 the contradictory pair ("transferred
// the attached video's motion..." + "the attached video was ignored") both
// reached the final model as evidence and it repeated both claims.
func TestVideoToolSummaryMatchesDeliveredSources(t *testing.T) {
	const model = "bytedance/seedance-2.0/reference-to-video"
	def := videoGenerationToolDefinition(false)
	exec := func(notices []string) HarnessToolExecutionContext {
		return HarnessToolExecutionContext{
			AttachedImages: []string{"data:image/png;base64,AAA", "data:image/png;base64,BBB"},
			AttachedVideos: []string{"data:video/mp4;base64,CCC"},
			GenerateVideo: func(ctx context.Context, req VideoGenerateRequest) (GeneratedVideo, error) {
				return GeneratedVideo{Data: []byte("mp4"), MimeType: "video/mp4", SourceURL: "https://example.com/v.mp4", Notices: notices}, nil
			},
		}
	}

	_, summary, err := def.Execute(context.Background(), exec(nil),
		HarnessToolCall{Name: "generate_video", Model: model, Content: "fly to the moon"})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if summary != "transferred the attached video's motion onto the attached image with "+model {
		t.Fatalf("summary = %q, want the motion-transfer phrasing when both sources are delivered", summary)
	}

	videoDropped := []string{fmt.Sprintf("The selected model %q has no source-video input; the attached video was ignored.", model)}
	_, summary, err = def.Execute(context.Background(), exec(videoDropped),
		HarnessToolCall{Name: "generate_video", Model: model, Content: "fly to the moon"})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if summary != "combined 2 attached images into a video with "+model {
		t.Fatalf("summary = %q, want the images-only phrasing when the video was dropped by notice", summary)
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
			wantSummary:  "used the attached image and 2 attached videos as references for a new video with " + imageModel,
		},
		{
			name:         "reference mode with videos only",
			call:         HarnessToolCall{Name: "generate_video", Content: "bed flies home", UseVideoAs: "reference"},
			videos:       []string{referenceVid1, referenceVid2},
			wantModel:    imageModel,
			wantVideoLen: 2,
			wantSummary:  "used 2 attached videos as references for a new video with " + imageModel,
		},
		{
			name:          "default keeps motion control and trims to one video",
			call:          HarnessToolCall{Name: "generate_video", Content: "bed flies home"},
			images:        []string{characterImg},
			videos:        []string{referenceVid1, referenceVid2},
			wantModel:     motionModel,
			wantVideoLen:  1,
			wantDroppedTo: 1,
			wantSummary:   "transferred the attached video's motion onto the attached image with " + motionModel,
		},
		{
			name:          "default video-only keeps extend and trims to one video",
			call:          HarnessToolCall{Name: "generate_video", Content: "bed flies home"},
			videos:        []string{referenceVid1, referenceVid2},
			wantModel:     extendModel,
			wantVideoLen:  1,
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
			def := videoGenerationToolDefinition(false)
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
	def := videoGenerationToolDefinition(false)
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
		def := videoGenerationToolDefinition(false)
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
	def := videoGenerationToolDefinition(false)
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
			def := lipsyncToolDefinition(false)
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
	def := lipsyncToolDefinition(false)
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
	def := lipsyncToolDefinition(false)
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
