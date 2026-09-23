package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
)

// === Reference-guided aspect ratio + the "auto" sentinel ===
//
// conv_8b8bffd58dcb383ee1cbfeae: a 2752x1536 (16:9 landscape) character sheet
// attached to bytedance/seedance-2.5/reference-to-video produced a 720x1280
// (9:16 portrait) clip. The gateway correctly derived "16:9" from the image,
// but resolveVideoBody's source-present gate dropped every non-explicit ratio
// on the assumption that sourced models inherit the source media's
// orientation — reference models don't. Their reference images are guidance
// (prompt-addressed @ImageN arrays), so fal ran seedance's aspect_ratio
// default "auto", which ignored the reference and went portrait.
//
// The fix distinguishes guidance from canvas by the model's schema-declared
// input: reference-style inputs (image_urls, reference_image_urls, video_urls
// — plural, prompt-addressed, or self-described @ImageN/@VideoN) get the
// CONFIGURED default sent (the user's standing preference, never the
// reference image's shape); frame inputs (image_url and friends), extends,
// and motion control keep the inherit rule (conv_26cc3f515d6d645b316763cb).
// "auto" joins the vocabulary as the let-the-model-decide sentinel, dropping
// silently where a model's enum lacks it.

// TestResolveVideoBodyReferenceImageSendsConfiguredRatio is the direct
// regression for conv_8b8bffd58dcb383ee1cbfeae: a reference-style model
// (seedance-2.5's image_urls array) with an attached image and no explicit
// ratio must SEND the configured default instead of withholding it.
func TestResolveVideoBodyReferenceImageSendsConfiguredRatio(t *testing.T) {
	body, notices, _ := resolveVideoBody(loadSchema(t, "seedance-2.5-reference-to-video"),
		VideoGenerateRequest{
			Model:             "bytedance/seedance-2.5/reference-to-video",
			Prompt:            "the character examines her missing teeth in the mirror",
			Images:            []string{"data:image/png;base64,AAAA"},
			AspectRatio:       "16:9", // the gateway's image-derived ratio — reference media, not canvas
			ConfigAspectRatio: "16:9",
		},
		builtinFalOverrides())
	if body["aspect_ratio"] != "16:9" {
		t.Fatalf("aspect_ratio = %v, want the configured \"16:9\" sent on a reference-guided turn", body["aspect_ratio"])
	}
	urls, ok := body["image_urls"].([]any)
	if !ok || len(urls) != 1 {
		t.Fatalf("image_urls = %+v (%T), want the reference image as a one-element list", body["image_urls"], body["image_urls"])
	}
	if len(notices) != 0 {
		t.Fatalf("expected no notices, got %v", notices)
	}
}

// TestResolveVideoBodyReferenceImageConfigBeatsImageShape pins the semantics
// the fix is named for: on a reference-guided turn the configured default
// wins over the reference image's own orientation. A landscape character
// sheet with a vertical standing preference must produce a vertical request —
// the image is guidance, not the canvas.
func TestResolveVideoBodyReferenceImageConfigBeatsImageShape(t *testing.T) {
	body, _, _ := resolveVideoBody(loadSchema(t, "seedance-2.5-reference-to-video"),
		VideoGenerateRequest{
			Model:             "bytedance/seedance-2.5/reference-to-video",
			Prompt:            "the character walks away",
			Images:            []string{"data:image/png;base64,AAAA"},
			AspectRatio:       "16:9", // derived from a landscape reference image
			ConfigAspectRatio: "9:16", // the user's standing preference
		},
		builtinFalOverrides())
	if body["aspect_ratio"] != "9:16" {
		t.Fatalf("aspect_ratio = %v, want the configured \"9:16\" — the reference image's shape must not rule", body["aspect_ratio"])
	}
}

// TestResolveVideoBodyReferenceImageAutoSentWhenEnumAllows: with the config at
// "auto" and seedance's enum listing it, the sentinel is passed through — the
// model decides the orientation, which is exactly what was asked.
func TestResolveVideoBodyReferenceImageAutoSentWhenEnumAllows(t *testing.T) {
	body, notices, _ := resolveVideoBody(loadSchema(t, "seedance-2.5-reference-to-video"),
		VideoGenerateRequest{
			Model:             "bytedance/seedance-2.5/reference-to-video",
			Prompt:            "the character walks away",
			Images:            []string{"data:image/png;base64,AAAA"},
			ConfigAspectRatio: "auto",
		},
		builtinFalOverrides())
	if body["aspect_ratio"] != "auto" {
		t.Fatalf("aspect_ratio = %v, want \"auto\" passed through when the enum lists it", body["aspect_ratio"])
	}
	if len(notices) != 0 {
		t.Fatalf("expected no notices, got %v", notices)
	}
}

// TestResolveVideoBodyReferenceImageAutoSilentOnAdaptiveEnum: minimax's enum
// has "adaptive", not "auto". The unsendable sentinel must drop SILENTLY —
// the model using its own default is the request — where a concrete
// out-of-enum ratio still notices (covered by the enum-guard test in
// fal_params_test.go).
func TestResolveVideoBodyReferenceImageAutoSilentOnAdaptiveEnum(t *testing.T) {
	body, notices, _ := resolveVideoBody(loadSchema(t, "minimax-h3-max-reference-to-video"),
		VideoGenerateRequest{
			Model:             "minimax/h3-max/reference-to-video",
			Prompt:            "the character walks through the house",
			Images:            []string{"data:image/png;base64,AAAA"},
			ConfigAspectRatio: "auto",
		},
		builtinFalOverrides())
	if _, present := body["aspect_ratio"]; present {
		t.Fatalf("aspect_ratio must be dropped when the enum lacks \"auto\", got %v", body["aspect_ratio"])
	}
	if len(notices) != 0 {
		t.Fatalf("an unsendable \"auto\" must drop silently (the model's own default IS the request), got %v", notices)
	}
}

// TestResolveVideoBodyReferenceImageNoAspectInput covers a reference model
// with no aspect_ratio input at all: a concrete configured ratio notices with
// the honest "no aspect-ratio control" copy (the guidance sets no orientation
// to inherit, so the source-image notice would be false), and "auto" stays
// silent for the same reason as the enum case.
func TestResolveVideoBodyReferenceImageNoAspectInput(t *testing.T) {
	schema := &ModelInputSchema{
		Properties: map[string]SchemaProperty{
			"prompt": {Name: "prompt", Kind: schemaScalar},
			"image_urls": {Name: "image_urls", Kind: schemaArray, Type: "array",
				Items:       &SchemaProperty{Name: "image_urls", Kind: schemaScalar, Type: "string"},
				Description: "Reference images to guide video generation. Refer to them in the prompt as @Image1."},
		},
		order: []string{"prompt", "image_urls"},
	}
	t.Run("concrete config notices honestly", func(t *testing.T) {
		body, notices, _ := resolveVideoBody(schema,
			VideoGenerateRequest{
				Model:             "acme/reference-to-video",
				Prompt:            "the character walks away",
				Images:            []string{"data:image/png;base64,AAAA"},
				ConfigAspectRatio: "16:9",
			},
			builtinFalOverrides())
		if _, present := body["aspect_ratio"]; present {
			t.Fatalf("aspect_ratio must not be sent; the model has no such input. got %v", body["aspect_ratio"])
		}
		if len(notices) != 1 {
			t.Fatalf("expected one notice, got %v", notices)
		}
		if strings.Contains(notices[0], "derives the output aspect ratio from the source image") {
			t.Fatalf("reference guidance sets no orientation to inherit; the source-image notice is false here. got %q", notices[0])
		}
		if !strings.Contains(notices[0], "no aspect-ratio control") {
			t.Fatalf("notice should say the model has no aspect-ratio control, got %q", notices[0])
		}
	})
	t.Run("auto stays silent", func(t *testing.T) {
		_, notices, _ := resolveVideoBody(schema,
			VideoGenerateRequest{
				Model:             "acme/reference-to-video",
				Prompt:            "the character walks away",
				Images:            []string{"data:image/png;base64,AAAA"},
				ConfigAspectRatio: "auto",
			},
			builtinFalOverrides())
		if len(notices) != 0 {
			t.Fatalf("an unsendable \"auto\" must drop silently, got %v", notices)
		}
	})
}

// TestResolveVideoBodyFrameImageStillInheritsOrientation pins the inherit
// rule for true frame inputs (conv_26cc3f515d6d645b316763cb): seedance-2.0
// image-to-video declares a scalar image_url ("the starting frame image"), so
// neither the configured default nor the sentinel may be stamped onto the
// request — the output continues the attached frame.
func TestResolveVideoBodyFrameImageStillInheritsOrientation(t *testing.T) {
	for _, config := range []string{"16:9", "auto"} {
		body, notices, _ := resolveVideoBody(loadSchema(t, "seedance-2.0-image-to-video"),
			VideoGenerateRequest{
				Model:             "bytedance/seedance-2.0/image-to-video",
				Prompt:            "animate the frame",
				Images:            []string{"data:image/png;base64,AAAA"},
				AspectRatio:       "9:16", // derived from a portrait source frame
				ConfigAspectRatio: config,
			},
			builtinFalOverrides())
		if _, present := body["aspect_ratio"]; present {
			t.Fatalf("config %q must not be stamped onto a frame turn (inherit the source frame); got aspect_ratio %v", config, body["aspect_ratio"])
		}
		if len(notices) != 0 {
			t.Fatalf("config %q: expected no notices for an inherited frame ratio, got %v", config, notices)
		}
	}
}

// TestResolveVideoBodyOverrideRoutedFrameInputClassifiesAsFrame pins the
// classification subtlety: the builtin kling override routes image_url bytes
// onto image_urls for wire-format reasons, but the model is still
// image-to-video — classification must read the schema's own declared input
// (image_url), not the override-resolved wire path, so the config default is
// NOT stamped while the override placement keeps working.
func TestResolveVideoBodyOverrideRoutedFrameInputClassifiesAsFrame(t *testing.T) {
	body, notices, _ := resolveVideoBody(loadSchema(t, "kling-image-to-video"),
		VideoGenerateRequest{
			Model:             "fal-ai/kling-video/v2/master/image-to-video",
			Prompt:            "make the character walk forward",
			Images:            []string{"data:image/png;base64,AAAA"},
			AspectRatio:       "16:9",
			ConfigAspectRatio: "16:9",
		},
		builtinFalOverrides())
	if _, present := body["aspect_ratio"]; present {
		t.Fatalf("a frame input rerouted by an override must still inherit; got aspect_ratio %v", body["aspect_ratio"])
	}
	if _, ok := body["image_urls"].([]any); !ok {
		t.Fatalf("image_urls = %+v (%T), want the override's one-element list placement", body["image_urls"], body["image_urls"])
	}
	if len(notices) != 0 {
		t.Fatalf("expected no notices, got %v", notices)
	}
}

// TestResolveVideoBodyReferenceVideoVsExtend covers the video side: a
// reference video (useVideoAs:"reference" landing on seedance's prompt-
// addressed video_urls array) is guidance, so the configured default is sent;
// an extension of the same model (ExtendSource) continues the source clip and
// keeps the inherit rule even though its video input is reference-named.
func TestResolveVideoBodyReferenceVideoVsExtend(t *testing.T) {
	t.Run("reference video sends config", func(t *testing.T) {
		body, _, _ := resolveVideoBody(loadSchema(t, "seedance-2.5-reference-to-video"),
			VideoGenerateRequest{
				Model:             "bytedance/seedance-2.5/reference-to-video",
				Prompt:            "a new clip in the same style",
				Videos:            []string{"data:video/mp4;base64,AAA"},
				VideoRole:         "reference",
				AspectRatio:       "16:9",
				ConfigAspectRatio: "16:9",
			},
			builtinFalOverrides())
		if body["aspect_ratio"] != "16:9" {
			t.Fatalf("aspect_ratio = %v, want the configured default sent for reference-video guidance", body["aspect_ratio"])
		}
	})
	t.Run("extension inherits the clip", func(t *testing.T) {
		body, _, _ := resolveVideoBody(loadSchema(t, "seedance-2.5-reference-to-video"),
			VideoGenerateRequest{
				Model:             "bytedance/seedance-2.5/reference-to-video",
				Prompt:            "continue the shot",
				Videos:            []string{"data:video/mp4;base64,AAA"},
				ExtendSource:      true,
				AspectRatio:       "16:9",
				ConfigAspectRatio: "16:9",
			},
			builtinFalOverrides())
		if _, present := body["aspect_ratio"]; present {
			t.Fatalf("an extension must inherit the source clip's orientation; got aspect_ratio %v", body["aspect_ratio"])
		}
	})
}

// TestResolveVideoBodyLegacyPathWithholdsAuto: the no-schema fallback cannot
// enum-check "auto" and fal 422s on unknown enum members, so the sentinel is
// withheld there — the model's own default already delivers the defer.
func TestResolveVideoBodyLegacyPathWithholdsAuto(t *testing.T) {
	body, _, _ := resolveVideoBody(nil,
		VideoGenerateRequest{
			Model:       "acme/text-to-video",
			Prompt:      "a drone shot over the forest",
			AspectRatio: "auto",
		},
		builtinFalOverrides())
	if _, present := body["aspect_ratio"]; present {
		t.Fatalf("aspect_ratio must be withheld on the schema-less path for \"auto\", got %v", body["aspect_ratio"])
	}
}

// TestReferenceStyleMediaInput pins the classifier standalone: plural,
// prompt-addressed inputs (and any input whose description documents
// @ImageN/@VideoN tokens) are guidance; singular frame inputs and
// frame-language descriptions are the canvas.
func TestReferenceStyleMediaInput(t *testing.T) {
	cases := []struct {
		name string
		in   string
		prop SchemaProperty
		want bool
	}{
		{"token-documented array", "image_urls", SchemaProperty{Description: "Reference images to guide video generation. Refer to them in the prompt as @Image1."}, true},
		{"reference-named without tokens", "reference_image_urls", SchemaProperty{Description: "URLs of subject/style reference images, referenced in the prompt as Image 1, Image 2."}, true},
		{"singular frame input", "image_url", SchemaProperty{Description: "The URL of the starting frame image to animate."}, false},
		{"plural name with frame semantics", "image_urls", SchemaProperty{Description: "The starting frames of the output video."}, false},
		{"bare scalar video input", "video_url", SchemaProperty{Description: "The video to extend."}, false},
		{"token-documented video array", "video_urls", SchemaProperty{Description: "Reference videos. Refer to them as @Video1."}, true},
		{"custom-named input", "input_media", SchemaProperty{Description: "Media for the request."}, false},
		{"nested reference path", "options.image_urls", SchemaProperty{Description: "Reference images."}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := referenceStyleMediaInput(c.in, c.prop); got != c.want {
				t.Fatalf("referenceStyleMediaInput(%q) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

// TestIsAutoAspectRatio pins the sentinel matching: case- and
// whitespace-insensitive "auto", nothing else.
func TestIsAutoAspectRatio(t *testing.T) {
	for _, in := range []string{"auto", "AUTO", "  auto  ", "Auto"} {
		if !isAutoAspectRatio(in) {
			t.Errorf("isAutoAspectRatio(%q) = false, want true", in)
		}
	}
	for _, in := range []string{"16:9", "", "adaptive", "automatic"} {
		if isAutoAspectRatio(in) {
			t.Errorf("isAutoAspectRatio(%q) = true, want false", in)
		}
	}
}

// TestHarnessReferenceImageSendsConfiguredVideoRatio replays
// conv_8b8bffd58dcb383ee1cbfeae end to end: a character-sheet image attached
// to a turn whose video-image model is seedance-2.5 reference-to-video, the
// planner emitting no aspectRatio, and Settings at 16:9. The fal submission
// must carry aspect_ratio "16:9" (the configured default) alongside the
// reference image — not run seedance's "auto" default that ignored the
// landscape reference and produced a portrait clip.
func TestHarnessReferenceImageSendsConfiguredVideoRatio(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	keyring.MockInit()
	if err := saveFalAPIKey("fal-test-key"); err != nil {
		t.Fatalf("saveFalAPIKey: %v", err)
	}
	t.Cleanup(func() { _ = clearFalAPIKey() })

	config := defaultAppConfig()
	config.Storage = ConfigStorage{
		Root:      filepath.Join(home, ".atelier"),
		History:   filepath.Join(home, ".atelier", "history"),
		Artifacts: filepath.Join(home, ".atelier", "history"),
	}
	config.Providers.Ollama.BaseURL = "http://ollama.test"
	config.Providers.Ollama.Models.Primary = "chat-box-model"
	config.Providers.Ollama.Models.Harness = "chat-box-model"
	config.Providers.Fal.VideoImageModel = "bytedance/seedance-2.5/reference-to-video"
	config.Generation.Video.AspectRatio = "16:9"
	if err := writeAppConfig(config); err != nil {
		t.Fatalf("writeAppConfig: %v", err)
	}

	app := NewApp()
	submittedBody := ""
	nonStreamCount := 0
	prepCalls := 0
	app.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if strings.HasPrefix(req.URL.Path, "/v1/models/pricing") {
			return falTestPricingResponse(), nil
		}
		if strings.Contains(req.URL.Path, "/api/openapi/") {
			// seedance-2.5 reference-to-video shape: prompt-addressed image_urls
			// (guidance, not canvas) plus an aspect_ratio enum listing "auto".
			return jsonResponse(`{"components":{"schemas":{"Seedance25ReferenceToVideoInput":{"type":"object","required":["prompt"],"properties":{
				"prompt":{"type":"string"},
				"image_urls":{"type":"array","items":{"type":"string"},"description":"Reference images to guide video generation. Refer to them in the prompt as @Image1, @Image2."},
				"aspect_ratio":{"type":"string","enum":["auto","21:9","16:9","4:3","1:1","3:4","9:16"],"description":"The aspect ratio of the generated video."},
				"duration":{"type":"string","enum":["auto","4","5","6","12"],"description":"Duration of the video in seconds."}
			}}}}}`), nil
		}
		if strings.Contains(req.URL.Host, "fal.run") {
			if req.Method == http.MethodPost {
				body, _ := io.ReadAll(req.Body)
				submittedBody = string(body)
				return jsonResponse(`{"request_id":"req-ref-1"}`), nil
			}
			if strings.HasSuffix(req.URL.Path, "/status") {
				return jsonResponse(`{"status":"COMPLETED"}`), nil
			}
			if strings.HasSuffix(req.URL.Path, "/requests/req-ref-1") {
				return jsonResponse(`{"video":{"url":"https://queue.fal.run/generated.mp4","content_type":"video/mp4"}}`), nil
			}
			return &http.Response{StatusCode: 200, Status: "200 OK",
				Body:   io.NopCloser(strings.NewReader(string(tinyMP4()))),
				Header: http.Header{"Content-Type": []string{"video/mp4"}}}, nil
		}
		switch req.URL.Path {
		case "/api/show":
			return jsonResponse(`{"capabilities":[],"model_info":{},"details":{"family":"test","parameter_size":"1B"}}`), nil
		case "/api/chat":
			payload := chatPayload(t, req)
			if payload["stream"] == false {
				nonStreamCount++
				if nonStreamCount == 1 {
					decision := `{"needsTools":true,"responseMode":"video","toolTask":"Create a video from the character reference.","reason":"The user wants a video guided by the attached image."}`
					return chatCompletion("harness-model", decision), nil
				}
				prepCalls++
				body := `{"brief":"Create the video.","needsTools":true,"reason":"video","toolCalls":[{"name":"generate_video","content":"the character looks in the mirror","duration":"12"}]}`
				if prepCalls > 1 {
					body = `{"brief":"The video was generated.","needsTools":false,"reason":"done","toolCalls":[]}`
				}
				return chatCompletion("harness-model", body), nil
			}
			body := fmt.Sprintln(`{"model":"chat-box-model","message":{"role":"assistant","content":"The 12-second video has been created."},"done":false}`) +
				fmt.Sprintln(`{"model":"chat-box-model","done":true,"done_reason":"stop","eval_count":3}`)
			return &http.Response{StatusCode: 200, Status: "200 OK",
				Body:   io.NopCloser(strings.NewReader(body)),
				Header: http.Header{"Content-Type": []string{"application/x-ndjson"}}}, nil
		default:
			t.Fatalf("unexpected request %s %s", req.Method, req.URL)
			return nil, nil
		}
	})

	app.runChatStream(context.Background(), "request-fal-reference-ratio", ChatRequest{
		BaseURL: "http://ollama.test",
		Model:   "chat-box-model",
		Messages: []ChatMessage{
			// 1x1 square image: the gateway derives "1:1" from it, so a sent
			// "16:9" proves the configured default replaced the derived ratio —
			// the reference media's shape does not rule.
			{Role: "user", Content: "Create a 12s video of the attached character looking in the mirror", Images: []string{tinyPNG}},
		},
	})

	if submittedBody == "" {
		t.Fatal("no fal submission was captured")
	}
	if !strings.Contains(submittedBody, `"aspect_ratio":"16:9"`) {
		t.Fatalf("fal submit must carry the configured aspect_ratio \"16:9\" on a reference-guided turn; body: %s", submittedBody)
	}
	if !strings.Contains(submittedBody, `"image_urls":["data:image/`) {
		t.Fatalf("fal submit must carry the reference image on image_urls; body: %s", submittedBody)
	}
}
