package main

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// tinyPNG is a minimal valid 1×1 PNG used to stand in for a downloaded image.
// It decodes cleanly (DetectedContentType → image/png) and passes isImageBytes.
var tinyPNG = base64.StdEncoding.EncodeToString([]byte{
	0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x00, 0x00, 0x0D,
	0x49, 0x48, 0x44, 0x52, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
	0x08, 0x06, 0x00, 0x00, 0x00, 0x1F, 0x15, 0xC4, 0x89, 0x00, 0x00, 0x00,
	0x0D, 0x49, 0x44, 0x41, 0x54, 0x78, 0x9C, 0x63, 0x60, 0x00, 0x00, 0x00,
	0x00, 0x02, 0x00, 0x01, 0xE2, 0x21, 0xBC, 0x33, 0x00, 0x00, 0x00, 0x00,
	0x49, 0x45, 0x4E, 0x44, 0xAE, 0x42, 0x60, 0x82,
})

// mustDecodeTinyPNG returns the raw PNG bytes for use as an HTTP response body.
func mustDecodeTinyPNG() []byte {
	data, err := base64.StdEncoding.DecodeString(tinyPNG)
	if err != nil {
		panic(err)
	}
	return data
}

func newFalTestClient(t *testing.T, transport http.RoundTripper) FalClient {
	t.Helper()
	return newFalClient(&http.Client{Transport: transport}, "test-key")
}

// falHandler routes a request by URL path to a response, mirroring the
// roundTripFunc + path-switch style used throughout app_test.go.
func falHandler(handler func(req *http.Request) (*http.Response, error)) http.RoundTripper {
	return roundTripFunc(handler)
}

func jsonResp(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}
}

func TestFalClientGenerateImageHappyPath(t *testing.T) {
	model := "fal-ai/flux/schnell"
	pollCount := 0
	client := newFalTestClient(t, falHandler(func(req *http.Request) (*http.Response, error) {
		switch {
		case req.Method == http.MethodPost && req.URL.Path == "/"+model:
			return jsonResp(`{"request_id":"req-123"}`), nil
		case req.Method == http.MethodGet && strings.HasSuffix(req.URL.Path, "/status"):
			pollCount++
			if pollCount == 1 {
				return jsonResp(`{"status":"IN_PROGRESS"}`), nil
			}
			return jsonResp(`{"status":"COMPLETED"}`), nil
		case req.Method == http.MethodGet && strings.HasSuffix(req.URL.Path, "/requests/req-123"):
			return jsonResp(`{"images":[{"url":"https://falcdn.example/img.png"}]}`), nil
		case req.Method == http.MethodGet && req.URL.Path == "/img.png":
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Body:       io.NopCloser(strings.NewReader(string(mustDecodeTinyPNG()))),
				Header:     http.Header{"Content-Type": []string{"image/png"}},
			}, nil
		default:
			t.Fatalf("unexpected request: %s %s", req.Method, req.URL.Path)
			return nil, nil
		}
	}))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, raw, err := client.GenerateImage(ctx, model, map[string]any{
		"prompt":              "a lighthouse at dusk",
		"num_images":          1,
		"image_size":          map[string]any{"width": 768, "height": 768},
		"num_inference_steps": 4,
	})
	if err != nil {
		t.Fatalf("GenerateImage returned error: %v", err)
	}
	if resp.Model != model {
		t.Errorf("response model = %q, want %q", resp.Model, model)
	}
	if len(resp.Images) != 1 {
		t.Fatalf("expected 1 image, got %d", len(resp.Images))
	}
	if !strings.HasPrefix(resp.Images[0], "data:image/png;base64,") {
		t.Errorf("image is not a png data url: %q", resp.Images[0][:40])
	}
	if resp.Image != resp.Images[0] {
		t.Errorf("Image field should mirror the first image")
	}
	// The fal client returns nil raw: it has already downloaded each result URL
	// into the base64 data URLs above, so the source URLs must not leak back to
	// the tool's collectImagesFromJSON backstop (which would re-harvest them and
	// break artifact decoding).
	if raw != nil {
		t.Errorf("raw JSON should be nil to avoid re-harvesting source URLs, got %s", string(raw))
	}
}

// TestFalClientGenerateImageImageToImage verifies the transport forwards a
// pre-built image-to-image body (carrying image_url, omitting image_size) to fal
// and downloads the result. The body itself is built by resolveImageBody (tested
// in fal_params_test.go); this test only exercises the submit/poll/download path.
func TestFalClientGenerateImageImageToImage(t *testing.T) {
	model := defaultFalImageEditModel
	client := newFalTestClient(t, falHandler(func(req *http.Request) (*http.Response, error) {
		switch {
		case req.Method == http.MethodPost && req.URL.Path == "/"+model:
			body, _ := io.ReadAll(req.Body)
			if !strings.Contains(string(body), `"image_url":"data:image/png;base64,ABC"`) {
				t.Errorf("submit body missing image_url data URI: %s", body)
			}
			if strings.Contains(string(body), `"image_size"`) {
				t.Errorf("image_size must be omitted for image-to-image: %s", body)
			}
			return jsonResp(`{"request_id":"req-i2i"}`), nil
		case strings.HasSuffix(req.URL.Path, "/status"):
			return jsonResp(`{"status":"COMPLETED"}`), nil
		case strings.HasSuffix(req.URL.Path, "/requests/req-i2i"):
			return jsonResp(`{"images":[{"url":"https://falcdn.example/img.png"}]}`), nil
		case req.URL.Path == "/img.png":
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(mustDecodeTinyPNG()))),
				Header: http.Header{"Content-Type": []string{"image/png"}}}, nil
		default:
			t.Fatalf("unexpected request: %s %s", req.Method, req.URL.Path)
			return nil, nil
		}
	}))

	resp, _, err := client.GenerateImage(context.Background(), model, map[string]any{
		"prompt":     "an impressionist painting of this",
		"num_images": 1,
		"image_url":  "data:image/png;base64,ABC",
	})
	if err != nil {
		t.Fatalf("GenerateImage returned error: %v", err)
	}
	if len(resp.Images) != 1 {
		t.Fatalf("expected 1 image, got %d", len(resp.Images))
	}
}

func TestFalClientGenerateImageModelFallback(t *testing.T) {
	client := newFalTestClient(t, falHandler(func(req *http.Request) (*http.Response, error) {
		if req.Method == http.MethodPost && req.URL.Path == "/"+defaultFalImageModel {
			return jsonResp(`{"request_id":"req-fb"}`), nil
		}
		if strings.HasSuffix(req.URL.Path, "/status") {
			return jsonResp(`{"status":"COMPLETED"}`), nil
		}
		if strings.HasSuffix(req.URL.Path, "/requests/req-fb") {
			return jsonResp(`{"images":[{"url":"https://falcdn.example/img.png"}]}`), nil
		}
		if req.URL.Path == "/img.png" {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(mustDecodeTinyPNG()))),
				Header: http.Header{"Content-Type": []string{"image/png"}}}, nil
		}
		t.Fatalf("unexpected request %s %s", req.Method, req.URL.Path)
		return nil, nil
	}))

	resp, _, err := client.GenerateImage(context.Background(), "", map[string]any{"prompt": "x"})
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if resp.Model != defaultFalImageModel {
		t.Errorf("model = %q, want default %q", resp.Model, defaultFalImageModel)
	}
}

func TestFalClientGenerateImageSubmitError(t *testing.T) {
	client := newFalTestClient(t, falHandler(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusUnauthorized,
			Status:     "401 Unauthorized",
			Body:       io.NopCloser(strings.NewReader(`{"detail":"invalid api key"}`)),
			Header:     http.Header{"Content-Type": []string{"application/json"}},
		}, nil
	}))

	_, _, err := client.GenerateImage(context.Background(), "fal-ai/flux/schnell", map[string]any{"prompt": "x"})
	if err == nil {
		t.Fatal("expected an authentication error, got nil")
	}
	if !strings.Contains(err.Error(), "authentication failed") {
		t.Errorf("expected auth-failed message, got %q", err.Error())
	}
}

func TestFalClientGenerateImageFailedStatus(t *testing.T) {
	model := "fal-ai/flux/schnell"
	client := newFalTestClient(t, falHandler(func(req *http.Request) (*http.Response, error) {
		if req.Method == http.MethodPost {
			return jsonResp(`{"request_id":"req-fail"}`), nil
		}
		if strings.HasSuffix(req.URL.Path, "/status") {
			return jsonResp(`{"status":"FAILED","error":"model crashed"}`), nil
		}
		t.Fatalf("unexpected request %s %s", req.Method, req.URL.Path)
		return nil, nil
	}))

	_, _, err := client.GenerateImage(context.Background(), model, map[string]any{"prompt": "x"})
	if err == nil || !strings.Contains(err.Error(), "model crashed") {
		t.Fatalf("expected failed-status error, got %v", err)
	}
}

func TestFalClientGenerateImagePollTimeout(t *testing.T) {
	model := "fal-ai/flux/schnell"
	client := newFalTestClient(t, falHandler(func(req *http.Request) (*http.Response, error) {
		if req.Method == http.MethodPost {
			return jsonResp(`{"request_id":"req-stuck"}`), nil
		}
		// Always report IN_PROGRESS so the context deadline fires.
		return jsonResp(`{"status":"IN_PROGRESS"}`), nil
	}))

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, _, err := client.GenerateImage(ctx, model, map[string]any{"prompt": "x"})
	if err == nil {
		t.Fatal("expected a cancellation error, got nil")
	}
}

func TestFalClientGenerateImageNoImages(t *testing.T) {
	model := "fal-ai/flux/schnell"
	client := newFalTestClient(t, falHandler(func(req *http.Request) (*http.Response, error) {
		if req.Method == http.MethodPost {
			return jsonResp(`{"request_id":"req-empty"}`), nil
		}
		if strings.HasSuffix(req.URL.Path, "/status") {
			return jsonResp(`{"status":"COMPLETED"}`), nil
		}
		return jsonResp(`{}`), nil
	}))

	_, _, err := client.GenerateImage(context.Background(), model, map[string]any{"prompt": "x"})
	if err == nil {
		t.Fatal("expected a no-images error, got nil")
	}
}

func TestFalClientRequiresAPIKey(t *testing.T) {
	client := newFalClient(&http.Client{}, "")
	_, _, err := client.GenerateImage(context.Background(), "fal-ai/flux/schnell", map[string]any{"prompt": "x"})
	if err == nil {
		t.Fatal("expected a key error, got nil")
	}
	// The client's do() helper guards against an empty key itself.
	if !strings.Contains(err.Error(), "key") {
		t.Errorf("expected key-related message, got %q", err.Error())
	}
}

func TestFalClientVerifyKeySucceeds(t *testing.T) {
	client := newFalTestClient(t, falHandler(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodGet || req.URL.Path != "/v1/models" {
			t.Fatalf("VerifyKey should GET /v1/models, got %s %s", req.Method, req.URL.Path)
		}
		if got := req.Header.Get("Authorization"); got != "Key test-key" {
			t.Errorf("Authorization header = %q, want 'Key test-key'", got)
		}
		return jsonResp(`{"data":[{"id":"fal-ai/flux/schnell"}]}`), nil
	}))
	if err := client.VerifyKey(context.Background()); err != nil {
		t.Fatalf("VerifyKey returned error: %v", err)
	}
}

func TestFalClientVerifyKeyRejectsInvalidKey(t *testing.T) {
	client := newFalTestClient(t, falHandler(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusUnauthorized,
			Status:     "401 Unauthorized",
			Body:       io.NopCloser(strings.NewReader(`{"detail":"invalid api key"}`)),
			Header:     http.Header{"Content-Type": []string{"application/json"}},
		}, nil
	}))
	err := client.VerifyKey(context.Background())
	if err == nil {
		t.Fatal("expected an authentication error, got nil")
	}
	if !strings.Contains(err.Error(), "authentication failed") {
		t.Errorf("expected auth-failed message, got %q", err.Error())
	}
}

func TestFalClientListModelsPaginates(t *testing.T) {
	// Page 1 (real /v1/models shape, trimmed) reports has_more with a cursor;
	// page 2 closes the catalog. The handler asserts the category filter and
	// cursor are threaded through correctly.
	page1 := `{"models":[
		{"endpoint_id":"fal-ai/flux/schnell","metadata":{"display_name":"FLUX.1 [schnell]","category":"text-to-image","status":"active","tags":[],"thumbnail_url":"https://v3b.fal.media/a.jpg"}},
		{"endpoint_id":"fal-ai/nano-banana-2","metadata":{"display_name":"Nano Banana 2","category":"text-to-image","status":"active","tags":[]}}
	],"next_cursor":"Mg","has_more":true}`
	page2 := `{"models":[
		{"endpoint_id":"openai/gpt-image-2","metadata":{"display_name":"GPT Image 2 API","category":"text-to-image","status":"active","tags":["openai"]}},
		{"endpoint_id":"fal-ai/no-metadata"}
	],"next_cursor":null,"has_more":false}`

	calls := 0
	client := newFalTestClient(t, falHandler(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodGet || req.URL.Path != "/v1/models" {
			t.Fatalf("expected GET /v1/models, got %s %s", req.Method, req.URL.Path)
		}
		q := req.URL.Query()
		if got := q.Get("category"); got != falTextToImageCategory {
			t.Errorf("category = %q, want %q", got, falTextToImageCategory)
		}
		calls++
		switch calls {
		case 1:
			if cur := q.Get("cursor"); cur != "" {
				t.Errorf("first page should have no cursor, got %q", cur)
			}
			return jsonResp(page1), nil
		case 2:
			if cur := q.Get("cursor"); cur != "Mg" {
				t.Errorf("second page cursor = %q, want %q", cur, "Mg")
			}
			return jsonResp(page2), nil
		default:
			t.Fatalf("unexpected third request")
			return nil, nil
		}
	}))

	models, err := client.ListModels(context.Background(), falTextToImageCategory, 0)
	if err != nil {
		t.Fatalf("ListModels returned error: %v", err)
	}
	if len(models) != 4 {
		t.Fatalf("expected 4 models across both pages, got %d", len(models))
	}
	if models[0].ID != "fal-ai/flux/schnell" || models[0].DisplayName != "FLUX.1 [schnell]" {
		t.Errorf("first model = %+v, want flux schnell", models[0])
	}
	// An entry without metadata falls back to using the endpoint id as its label.
	last := models[3]
	if last.ID != "fal-ai/no-metadata" || last.DisplayName != "fal-ai/no-metadata" {
		t.Errorf("metadata-less model = %+v, want id as display name", last)
	}
}

func TestFalClientListModelsRespectsMax(t *testing.T) {
	// has_more stays true, but maxModels caps accumulation and stops paging.
	page := `{"models":[
		{"endpoint_id":"m/1"},{"endpoint_id":"m/2"},{"endpoint_id":"m/3"}
	],"next_cursor":"next","has_more":true}`
	calls := 0
	client := newFalTestClient(t, falHandler(func(req *http.Request) (*http.Response, error) {
		calls++
		return jsonResp(page), nil
	}))

	models, err := client.ListModels(context.Background(), "", 2)
	if err != nil {
		t.Fatalf("ListModels returned error: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("expected max of 2 models, got %d", len(models))
	}
	if calls != 1 {
		t.Errorf("expected to stop after 1 page, made %d calls", calls)
	}
}

// tinyMP4 is a minimal byte sequence with an "ftyp" box at offset 4, enough for
// isVideoBytes to accept it as an MP4 container. It is not a playable video, but
// the client only sniffs the header.
func tinyMP4() []byte {
	return []byte{
		0x00, 0x00, 0x00, 0x18, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm',
		0x00, 0x00, 0x02, 0x00, 'i', 's', 'o', 'm', 'i', 's', 'o', '2',
	}
}

func mp4Resp(body []byte, contentType string) *http.Response {
	header := http.Header{}
	if contentType != "" {
		header.Set("Content-Type", contentType)
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Body:       io.NopCloser(strings.NewReader(string(body))),
		Header:     header,
	}
}

func TestFalClientGenerateVideoHappyPath(t *testing.T) {
	model := "fal-ai/kling-video/v2/master/text-to-video"
	pollCount := 0
	client := newFalTestClient(t, falHandler(func(req *http.Request) (*http.Response, error) {
		switch {
		case req.Method == http.MethodPost && req.URL.Path == "/"+model:
			body, _ := io.ReadAll(req.Body)
			if !strings.Contains(string(body), `"duration":"5"`) {
				t.Errorf("submit body missing duration: %s", body)
			}
			if !strings.Contains(string(body), `"aspect_ratio":"16:9"`) {
				t.Errorf("submit body missing aspect_ratio: %s", body)
			}
			return jsonResp(`{"request_id":"req-vid"}`), nil
		case req.Method == http.MethodGet && strings.HasSuffix(req.URL.Path, "/status"):
			pollCount++
			if pollCount == 1 {
				return jsonResp(`{"status":"IN_PROGRESS"}`), nil
			}
			return jsonResp(`{"status":"COMPLETED"}`), nil
		case req.Method == http.MethodGet && strings.HasSuffix(req.URL.Path, "/requests/req-vid"):
			return jsonResp(`{"video":{"url":"https://v3.fal.media/files/clip.mp4","content_type":"video/mp4","file_size":24}}`), nil
		case req.Method == http.MethodGet && req.URL.Path == "/files/clip.mp4":
			return mp4Resp(tinyMP4(), "video/mp4"), nil
		default:
			t.Fatalf("unexpected request: %s %s", req.Method, req.URL.Path)
			return nil, nil
		}
	}))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// GenerateVideo is now a thin transport; resolveVideoBody builds the body.
	video, err := client.GenerateVideo(ctx, model, map[string]any{
		"prompt":       "a drone shot over a forest",
		"duration":     "5",
		"aspect_ratio": "16:9",
	})
	if err != nil {
		t.Fatalf("GenerateVideo returned error: %v", err)
	}
	if len(video.Data) == 0 {
		t.Fatal("expected video bytes, got none")
	}
	if video.MimeType != "video/mp4" {
		t.Errorf("mime = %q, want video/mp4", video.MimeType)
	}
	if video.SourceURL != "https://v3.fal.media/files/clip.mp4" {
		t.Errorf("source url = %q", video.SourceURL)
	}
}

func TestFalClientGenerateVideoNegativePromptAndAudio(t *testing.T) {
	// Body construction (negative_prompt, generate_audio) is resolveVideoBody's
	// job now; this transport test verifies GenerateVideo forwards whatever body
	// it's given verbatim and omits nothing. The resolveVideoBody tests in
	// fal_params_test.go cover the canonical→native mapping.
	model := "fal-ai/kling-video/v2/master/text-to-video"
	falseFlag := false
	cases := []struct {
		name         string
		body         map[string]any
		wantContains []string
		wantOmits    []string
	}{
		{
			name:         "negative prompt and explicit silent",
			body:         map[string]any{"prompt": "x", "negative_prompt": "blurry, text", "generate_audio": falseFlag},
			wantContains: []string{`"negative_prompt":"blurry, text"`, `"generate_audio":false`},
		},
		{
			name:      "unset audio is omitted",
			body:      map[string]any{"prompt": "x"},
			wantOmits: []string{"generate_audio", "negative_prompt"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := newFalTestClient(t, falHandler(func(req *http.Request) (*http.Response, error) {
				switch {
				case req.Method == http.MethodPost && req.URL.Path == "/"+model:
					body, _ := io.ReadAll(req.Body)
					for _, want := range tc.wantContains {
						if !strings.Contains(string(body), want) {
							t.Errorf("submit body missing %s: %s", want, body)
						}
					}
					for _, omit := range tc.wantOmits {
						if strings.Contains(string(body), omit) {
							t.Errorf("submit body should omit %s: %s", omit, body)
						}
					}
					return jsonResp(`{"request_id":"req-na"}`), nil
				case strings.HasSuffix(req.URL.Path, "/status"):
					return jsonResp(`{"status":"COMPLETED"}`), nil
				case strings.HasSuffix(req.URL.Path, "/requests/req-na"):
					return jsonResp(`{"video":{"url":"https://v3.fal.media/files/clip.mp4"}}`), nil
				case req.URL.Path == "/files/clip.mp4":
					return mp4Resp(tinyMP4(), "video/mp4"), nil
				default:
					t.Fatalf("unexpected request: %s %s", req.Method, req.URL.Path)
					return nil, nil
				}
			}))
			if _, err := client.GenerateVideo(context.Background(), model, tc.body); err != nil {
				t.Fatalf("GenerateVideo returned error: %v", err)
			}
		})
	}
}

func TestFalClientGenerateVideoImageToVideo(t *testing.T) {
	model := "fal-ai/kling-video/v2/master/image-to-video"
	client := newFalTestClient(t, falHandler(func(req *http.Request) (*http.Response, error) {
		switch {
		case req.Method == http.MethodPost && req.URL.Path == "/"+model:
			body, _ := io.ReadAll(req.Body)
			if !strings.Contains(string(body), `"image_url":"data:image/png;base64,ABC"`) {
				t.Errorf("submit body missing image_url: %s", body)
			}
			return jsonResp(`{"request_id":"req-i2v"}`), nil
		case strings.HasSuffix(req.URL.Path, "/status"):
			return jsonResp(`{"status":"COMPLETED"}`), nil
		case strings.HasSuffix(req.URL.Path, "/requests/req-i2v"):
			return jsonResp(`{"video":{"url":"https://v3.fal.media/files/clip.mp4"}}`), nil
		case req.URL.Path == "/files/clip.mp4":
			return mp4Resp(tinyMP4(), "video/mp4"), nil
		default:
			t.Fatalf("unexpected request: %s %s", req.Method, req.URL.Path)
			return nil, nil
		}
	}))

	video, err := client.GenerateVideo(context.Background(), model, map[string]any{
		"prompt":    "make the character talk",
		"image_url": "data:image/png;base64,ABC",
	})
	if err != nil {
		t.Fatalf("GenerateVideo returned error: %v", err)
	}
	if len(video.Data) == 0 {
		t.Fatal("expected video bytes")
	}
}

func TestFalClientGenerateVideoModelFallback(t *testing.T) {
	client := newFalTestClient(t, falHandler(func(req *http.Request) (*http.Response, error) {
		if req.Method == http.MethodPost && req.URL.Path == "/"+defaultFalVideoModel {
			return jsonResp(`{"request_id":"req-fb"}`), nil
		}
		if strings.HasSuffix(req.URL.Path, "/status") {
			return jsonResp(`{"status":"COMPLETED"}`), nil
		}
		if strings.HasSuffix(req.URL.Path, "/requests/req-fb") {
			return jsonResp(`{"video":{"url":"https://v3.fal.media/files/clip.mp4"}}`), nil
		}
		if req.URL.Path == "/files/clip.mp4" {
			return mp4Resp(tinyMP4(), ""), nil
		}
		t.Fatalf("unexpected request %s %s", req.Method, req.URL.Path)
		return nil, nil
	}))

	video, err := client.GenerateVideo(context.Background(), "", map[string]any{"prompt": "x"})
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	// Content-Type absent on the download: the client sniffs the bytes and
	// falls back to video/mp4.
	if video.MimeType != "video/mp4" {
		t.Errorf("mime = %q, want sniffed video/mp4", video.MimeType)
	}
}

func TestFalClientGenerateVideoNoVideo(t *testing.T) {
	model := defaultFalVideoModel
	client := newFalTestClient(t, falHandler(func(req *http.Request) (*http.Response, error) {
		if req.Method == http.MethodPost {
			return jsonResp(`{"request_id":"req-empty"}`), nil
		}
		if strings.HasSuffix(req.URL.Path, "/status") {
			return jsonResp(`{"status":"COMPLETED"}`), nil
		}
		return jsonResp(`{}`), nil
	}))

	_, err := client.GenerateVideo(context.Background(), model, map[string]any{})
	if err == nil || !strings.Contains(err.Error(), "no video") {
		t.Fatalf("expected a no-video error, got %v", err)
	}
}

func TestFalClientGenerateVideoFallbackURL(t *testing.T) {
	// No top-level "video" object; the mp4 URL is nested. firstFalVideoURL must
	// still find it.
	model := defaultFalVideoModel
	client := newFalTestClient(t, falHandler(func(req *http.Request) (*http.Response, error) {
		if req.Method == http.MethodPost {
			return jsonResp(`{"request_id":"req-nested"}`), nil
		}
		if strings.HasSuffix(req.URL.Path, "/status") {
			return jsonResp(`{"status":"COMPLETED"}`), nil
		}
		if strings.HasSuffix(req.URL.Path, "/requests/req-nested") {
			return jsonResp(`{"data":{"output":{"clip":"https://v3.fal.media/files/nested.mp4"}}}`), nil
		}
		if req.URL.Path == "/files/nested.mp4" {
			return mp4Resp(tinyMP4(), "video/mp4"), nil
		}
		t.Fatalf("unexpected request %s %s", req.Method, req.URL.Path)
		return nil, nil
	}))

	video, err := client.GenerateVideo(context.Background(), model, map[string]any{})
	if err != nil {
		t.Fatalf("GenerateVideo returned error: %v", err)
	}
	if video.SourceURL != "https://v3.fal.media/files/nested.mp4" {
		t.Errorf("source url = %q, want nested url", video.SourceURL)
	}
}

func TestFalClientGenerateVideoRejectsNonVideo(t *testing.T) {
	// The download returns a PNG, not a video; the client must reject it.
	model := defaultFalVideoModel
	client := newFalTestClient(t, falHandler(func(req *http.Request) (*http.Response, error) {
		if req.Method == http.MethodPost {
			return jsonResp(`{"request_id":"req-png"}`), nil
		}
		if strings.HasSuffix(req.URL.Path, "/status") {
			return jsonResp(`{"status":"COMPLETED"}`), nil
		}
		if strings.HasSuffix(req.URL.Path, "/requests/req-png") {
			return jsonResp(`{"video":{"url":"https://v3.fal.media/files/notvideo.mp4"}}`), nil
		}
		return mp4Resp(mustDecodeTinyPNG(), "image/png"), nil
	}))

	_, err := client.GenerateVideo(context.Background(), model, map[string]any{})
	if err == nil || !strings.Contains(err.Error(), "not a supported video") {
		t.Fatalf("expected not-a-video error, got %v", err)
	}
}

// TestFalClientUsesSubmitStatusAndResponseURLs is the regression test for
// conv_346b8787832e3d922a66d673: the submit succeeded but the status poll 405'd
// because the client reconstructed the status path from the full endpoint id.
// fal returns status_url/response_url under the app id (fal-ai/kling-video, not
// the full .../v2/master/image-to-video); the client must use them verbatim.
func TestFalClientUsesSubmitStatusAndResponseURLs(t *testing.T) {
	model := "fal-ai/kling-video/v2/master/image-to-video"
	statusURL := "https://queue.fal.run/fal-ai/kling-video/requests/req-x/status"
	responseURL := "https://queue.fal.run/fal-ai/kling-video/requests/req-x"
	client := newFalTestClient(t, falHandler(func(req *http.Request) (*http.Response, error) {
		switch {
		case req.Method == http.MethodPost && req.URL.Path == "/"+model:
			return jsonResp(`{"request_id":"req-x","status_url":"` + statusURL + `","response_url":"` + responseURL + `"}`), nil
		case req.URL.Path == "/fal-ai/kling-video/requests/req-x/status":
			return jsonResp(`{"status":"COMPLETED"}`), nil
		case req.URL.Path == "/fal-ai/kling-video/requests/req-x":
			return jsonResp(`{"video":{"url":"https://v3.fal.media/files/clip.mp4"}}`), nil
		case req.URL.Path == "/files/clip.mp4":
			return mp4Resp(tinyMP4(), "video/mp4"), nil
		case strings.Contains(req.URL.Path, "/image-to-video/requests/"):
			// Reconstructing from the full endpoint id is the bug — fal 405s it.
			t.Fatalf("client reconstructed the full endpoint path instead of using status_url: %s", req.URL.Path)
			return nil, nil
		default:
			t.Fatalf("unexpected request: %s %s", req.Method, req.URL.Path)
			return nil, nil
		}
	}))

	video, err := client.GenerateVideo(context.Background(), model, map[string]any{
		"prompt":    "x",
		"image_url": "data:image/png;base64,ABC",
	})
	if err != nil {
		t.Fatalf("GenerateVideo returned error: %v", err)
	}
	if len(video.Data) == 0 {
		t.Fatal("expected video bytes")
	}
}

// TestFalImageURL is the regression for conv_784c285332d0d971d62e7a68: bare
// base64 (what the frontend sends) must be wrapped as a data URI, since fal
// rejects raw base64 with a 422. URLs and existing data URIs pass through.
func TestFalImageURL(t *testing.T) {
	if got := falImageURL(tinyPNG); got != "data:image/png;base64,"+tinyPNG {
		t.Errorf("bare base64 not wrapped as a png data URI: %q", got[:40])
	}
	dataURI := "data:image/jpeg;base64," + tinyPNG
	if got := falImageURL(dataURI); got != dataURI {
		t.Errorf("data URI should pass through unchanged, got %q", got)
	}
	if got := falImageURL("https://cdn.example/a.png"); got != "https://cdn.example/a.png" {
		t.Errorf("https URL should pass through unchanged, got %q", got)
	}
	if got := falImageURL("   "); got != "" {
		t.Errorf("empty input should return empty, got %q", got)
	}
}

// TestFalAppPath covers the app-path fallback used when fal omits status_url.
func TestFalAppPath(t *testing.T) {
	cases := map[string]string{
		"fal-ai/flux/schnell":                         "fal-ai/flux",
		"fal-ai/kling-video/v2/master/image-to-video": "fal-ai/kling-video",
		"bytedance/seedance-2.0/fast/text-to-video":   "bytedance/seedance-2.0",
		"ideogram/v4": "ideogram/v4",
	}
	for model, want := range cases {
		if got := falAppPath(model); got != want {
			t.Errorf("falAppPath(%q) = %q, want %q", model, got, want)
		}
	}
}

// TestFalClientSubmitFollowsRedirectPreservingPost reproduces the failure from
// conv_a89e35615db14bdeec29cfff: fal 3xx-redirects the queue submit to a
// canonical endpoint. The default net/http client would downgrade the followed
// POST to a GET, which the submit endpoint answers with 405. do() must follow
// the redirect with the POST (and body) intact.
func TestFalClientSubmitFollowsRedirectPreservingPost(t *testing.T) {
	model := "bytedance/seedance-2.0/fast/text-to-video"
	canonicalPath := "/fal-ai/bytedance/seedance/v2/fast/text-to-video"
	sawCanonicalMethod := ""
	client := newFalTestClient(t, falHandler(func(req *http.Request) (*http.Response, error) {
		switch {
		case req.URL.Path == "/"+model && req.Method == http.MethodPost:
			// Redirect the submit to the canonical endpoint.
			return &http.Response{
				StatusCode: http.StatusTemporaryRedirect,
				Status:     "307 Temporary Redirect",
				Header:     http.Header{"Location": []string{"https://queue.fal.run" + canonicalPath}},
				Body:       io.NopCloser(strings.NewReader("")),
				Request:    req,
			}, nil
		case req.URL.Path == canonicalPath:
			sawCanonicalMethod = req.Method
			if req.Method != http.MethodPost {
				// A downgraded GET is exactly the 405 bug being guarded against.
				return &http.Response{StatusCode: http.StatusMethodNotAllowed, Status: "405 Method Not Allowed",
					Body: io.NopCloser(strings.NewReader("")), Request: req}, nil
			}
			return jsonResp(`{"request_id":"req-redir"}`), nil
		case strings.HasSuffix(req.URL.Path, "/status"):
			return jsonResp(`{"status":"COMPLETED"}`), nil
		case strings.HasSuffix(req.URL.Path, "/requests/req-redir"):
			return jsonResp(`{"video":{"url":"https://v3.fal.media/files/clip.mp4"}}`), nil
		case req.URL.Path == "/files/clip.mp4":
			return mp4Resp(tinyMP4(), "video/mp4"), nil
		default:
			t.Fatalf("unexpected request: %s %s", req.Method, req.URL.Path)
			return nil, nil
		}
	}))

	video, err := client.GenerateVideo(context.Background(), model, map[string]any{"prompt": "x"})
	if err != nil {
		t.Fatalf("GenerateVideo returned error: %v", err)
	}
	if sawCanonicalMethod != http.MethodPost {
		t.Fatalf("canonical endpoint saw method %q, want POST (redirect downgraded the submit)", sawCanonicalMethod)
	}
	if len(video.Data) == 0 {
		t.Fatal("expected video bytes after following the redirect")
	}
}

// tinyMP3 is a minimal byte sequence with an ID3 tag header, enough for
// isAudioBytes to accept it. Not a playable clip; the client only sniffs.
func tinyMP3() []byte {
	return append([]byte("ID3\x04\x00\x00\x00\x00\x00\x00"), 0xFF, 0xFB, 0x90, 0x00)
}

func audioResp(body []byte, contentType string) *http.Response {
	header := http.Header{}
	if contentType != "" {
		header.Set("Content-Type", contentType)
	}
	return &http.Response{StatusCode: http.StatusOK, Status: "200 OK",
		Body: io.NopCloser(strings.NewReader(string(body))), Header: header}
}

func TestFalClientGenerateAudioHappyPath(t *testing.T) {
	model := "fal-ai/elevenlabs/tts/multilingual-v2"
	client := newFalTestClient(t, falHandler(func(req *http.Request) (*http.Response, error) {
		switch {
		case req.Method == http.MethodPost && req.URL.Path == "/"+model:
			body, _ := io.ReadAll(req.Body)
			// Sent as both prompt (music/sfx models) and text (TTS models).
			if !strings.Contains(string(body), `"text":"say hello"`) || !strings.Contains(string(body), `"prompt":"say hello"`) {
				t.Errorf("submit body missing prompt/text: %s", body)
			}
			return jsonResp(`{"request_id":"req-aud"}`), nil
		case strings.HasSuffix(req.URL.Path, "/status"):
			return jsonResp(`{"status":"COMPLETED"}`), nil
		case strings.HasSuffix(req.URL.Path, "/requests/req-aud"):
			return jsonResp(`{"audio":{"url":"https://v3.fal.media/files/clip.mp3","content_type":"audio/mpeg"}}`), nil
		case req.URL.Path == "/files/clip.mp3":
			return audioResp(tinyMP3(), "audio/mpeg"), nil
		default:
			t.Fatalf("unexpected request: %s %s", req.Method, req.URL.Path)
			return nil, nil
		}
	}))

	audio, err := client.GenerateAudio(context.Background(), model, map[string]any{"prompt": "say hello", "text": "say hello"})
	if err != nil {
		t.Fatalf("GenerateAudio returned error: %v", err)
	}
	if len(audio.Data) == 0 {
		t.Fatal("expected audio bytes")
	}
	if audio.MimeType != "audio/mpeg" {
		t.Errorf("mime = %q, want audio/mpeg", audio.MimeType)
	}
}

func TestFalClientGenerateAudioAudioFileField(t *testing.T) {
	// Some endpoints (e.g. cassetteai) return "audio_file" instead of "audio".
	model := defaultFalAudioModel
	client := newFalTestClient(t, falHandler(func(req *http.Request) (*http.Response, error) {
		if req.Method == http.MethodPost {
			return jsonResp(`{"request_id":"req-af"}`), nil
		}
		if strings.HasSuffix(req.URL.Path, "/status") {
			return jsonResp(`{"status":"COMPLETED"}`), nil
		}
		if strings.HasSuffix(req.URL.Path, "/requests/req-af") {
			return jsonResp(`{"audio_file":{"url":"https://v3.fal.media/files/song.mp3"}}`), nil
		}
		return audioResp(tinyMP3(), "audio/mpeg"), nil
	}))

	audio, err := client.GenerateAudio(context.Background(), model, map[string]any{"prompt": "a jazzy tune", "text": "a jazzy tune"})
	if err != nil {
		t.Fatalf("GenerateAudio returned error: %v", err)
	}
	if audio.SourceURL != "https://v3.fal.media/files/song.mp3" {
		t.Errorf("source url = %q, want the audio_file url", audio.SourceURL)
	}
}

func TestFalClientGenerateAudioForwardsBodyVerbatim(t *testing.T) {
	// GenerateAudio is now a thin transport: it submits the already-native body
	// unchanged (body construction is resolveAudioBody's job, tested separately).
	model := "fal-ai/minimax/music-01"
	client := newFalTestClient(t, falHandler(func(req *http.Request) (*http.Response, error) {
		switch {
		case req.Method == http.MethodPost && req.URL.Path == "/"+model:
			body, _ := io.ReadAll(req.Body)
			for _, want := range []string{`"prompt":"calm piano loop"`, `"duration_seconds":10`, `"loop":true`} {
				if !strings.Contains(string(body), want) {
					t.Errorf("submit body missing %s: %s", want, body)
				}
			}
			return jsonResp(`{"request_id":"req-dn"}`), nil
		case strings.HasSuffix(req.URL.Path, "/status"):
			return jsonResp(`{"status":"COMPLETED"}`), nil
		case strings.HasSuffix(req.URL.Path, "/requests/req-dn"):
			return jsonResp(`{"audio":{"url":"https://v3.fal.media/files/clip.mp3","content_type":"audio/mpeg"}}`), nil
		case req.URL.Path == "/files/clip.mp3":
			return audioResp(tinyMP3(), "audio/mpeg"), nil
		default:
			t.Fatalf("unexpected request: %s %s", req.Method, req.URL.Path)
			return nil, nil
		}
	}))

	body := map[string]any{"prompt": "calm piano loop", "duration_seconds": 10, "loop": true}
	audio, err := client.GenerateAudio(context.Background(), model, body)
	if err != nil {
		t.Fatalf("GenerateAudio returned error: %v", err)
	}
	if len(audio.Data) == 0 {
		t.Fatal("expected audio bytes")
	}
}

func TestFalClientGenerateAudioNoAudio(t *testing.T) {
	client := newFalTestClient(t, falHandler(func(req *http.Request) (*http.Response, error) {
		if req.Method == http.MethodPost {
			return jsonResp(`{"request_id":"req-empty"}`), nil
		}
		if strings.HasSuffix(req.URL.Path, "/status") {
			return jsonResp(`{"status":"COMPLETED"}`), nil
		}
		return jsonResp(`{}`), nil
	}))

	_, err := client.GenerateAudio(context.Background(), defaultFalAudioModel, map[string]any{"text": "x"})
	if err == nil || !strings.Contains(err.Error(), "no audio") {
		t.Fatalf("expected a no-audio error, got %v", err)
	}
}

func TestCollectFalImagesFromJSON(t *testing.T) {
	raw := []byte(`{"data":{"nested":[{"url":"https://cdn.fal.ai/a.png"},{"url":"https://cdn.fal.ai/b.jpg"}]}}`)
	images := collectFalImagesFromJSON(raw)
	if len(images) != 2 {
		t.Fatalf("expected 2 images, got %d", len(images))
	}
}

// TestFalAudioURL mirrors TestFalImageURL for the audio path: bare base64 is
// wrapped as a data URI (audio/mpeg default since DetectContentType can't
// reliably subtype audio), and data URIs / http(s) URLs / empty pass through.
func TestFalAudioURL(t *testing.T) {
	bare := base64.StdEncoding.EncodeToString(tinyMP3())
	if got := falAudioURL(bare); got != "data:audio/mpeg;base64,"+bare {
		t.Errorf("bare base64 not wrapped as an audio data URI: %q", got[:min(40, len(got))])
	}
	dataURI := "data:audio/wav;base64," + bare
	if got := falAudioURL(dataURI); got != dataURI {
		t.Errorf("data URI should pass through unchanged, got %q", got)
	}
	if got := falAudioURL("https://cdn.example/clip.mp3"); got != "https://cdn.example/clip.mp3" {
		t.Errorf("https URL should pass through unchanged, got %q", got)
	}
	if got := falAudioURL("   "); got != "" {
		t.Errorf("empty input should return empty, got %q", got)
	}
}

// TestFalClientTranscribeAudioSubmitsWizperBody asserts the submit goes to the
// wizper endpoint with audio_url as a data URI and the default task. The model
// is verified against the configured default so the test does not drift if the
// const changes.
func TestFalClientTranscribeAudioSubmitsWizperBody(t *testing.T) {
	model := defaultFalTranscribeModel
	var submitBody string
	client := newFalTestClient(t, falHandler(func(req *http.Request) (*http.Response, error) {
		switch {
		case req.Method == http.MethodPost && req.URL.Path == "/"+model:
			body, _ := io.ReadAll(req.Body)
			submitBody = string(body)
			return jsonResp(`{"request_id":"req-tr"}`), nil
		case strings.HasSuffix(req.URL.Path, "/status"):
			return jsonResp(`{"status":"COMPLETED"}`), nil
		case strings.HasSuffix(req.URL.Path, "/requests/req-tr"):
			return jsonResp(`{"text":"hello world"}`), nil
		default:
			t.Fatalf("unexpected request: %s %s", req.Method, req.URL.Path)
			return nil, nil
		}
	}))

	transcript, err := client.TranscribeAudio(context.Background(), "", "data:audio/wav;base64,AAAA", "", "")
	if err != nil {
		t.Fatalf("TranscribeAudio returned error: %v", err)
	}
	if transcript.Text != "hello world" {
		t.Errorf("transcript = %q, want \"hello world\"", transcript.Text)
	}
	if !strings.Contains(submitBody, `"audio_url":"data:audio/wav;base64,AAAA"`) {
		t.Errorf("submit body did not carry audio_url as the data URI: %s", submitBody)
	}
	if !strings.Contains(submitBody, `"task":"transcribe"`) {
		t.Errorf("submit body did not default task to transcribe: %s", submitBody)
	}
}

// TestFalClientTranscribeAudioForwardsTaskAndLanguage asserts an explicit task
// (translate) and language hint are forwarded on the submit body rather than
// overwritten with the defaults.
func TestFalClientTranscribeAudioForwardsTaskAndLanguage(t *testing.T) {
	model := defaultFalTranscribeModel
	var submitBody string
	client := newFalTestClient(t, falHandler(func(req *http.Request) (*http.Response, error) {
		switch {
		case req.Method == http.MethodPost && req.URL.Path == "/"+model:
			body, _ := io.ReadAll(req.Body)
			submitBody = string(body)
			return jsonResp(`{"request_id":"req-tr2"}`), nil
		case strings.HasSuffix(req.URL.Path, "/status"):
			return jsonResp(`{"status":"COMPLETED"}`), nil
		case strings.HasSuffix(req.URL.Path, "/requests/req-tr2"):
			return jsonResp(`{"text":"bonjour le monde"}`), nil
		default:
			t.Fatalf("unexpected request: %s %s", req.Method, req.URL.Path)
			return nil, nil
		}
	}))

	if _, err := client.TranscribeAudio(context.Background(), model, "data:audio/mpeg;base64,AAAA", "translate", "fr"); err != nil {
		t.Fatalf("TranscribeAudio returned error: %v", err)
	}
	if !strings.Contains(submitBody, `"task":"translate"`) {
		t.Errorf("task not forwarded: %s", submitBody)
	}
	if !strings.Contains(submitBody, `"language":"fr"`) {
		t.Errorf("language not forwarded: %s", submitBody)
	}
}

// TestFalClientTranscribeAudioNoText covers the error path: a result with no
// "text" field surfaces a clear error rather than an empty transcript.
func TestFalClientTranscribeAudioNoText(t *testing.T) {
	model := defaultFalTranscribeModel
	client := newFalTestClient(t, falHandler(func(req *http.Request) (*http.Response, error) {
		switch {
		case req.Method == http.MethodPost && req.URL.Path == "/"+model:
			return jsonResp(`{"request_id":"req-empty"}`), nil
		case strings.HasSuffix(req.URL.Path, "/status"):
			return jsonResp(`{"status":"COMPLETED"}`), nil
		case strings.HasSuffix(req.URL.Path, "/requests/req-empty"):
			return jsonResp(`{"chunks":[]}`), nil
		default:
			t.Fatalf("unexpected request: %s %s", req.Method, req.URL.Path)
			return nil, nil
		}
	}))

	_, err := client.TranscribeAudio(context.Background(), model, "data:audio/mpeg;base64,AAAA", "", "")
	if err == nil || !strings.Contains(err.Error(), "no text") {
		t.Fatalf("expected a no-text error, got %v", err)
	}
}

// TestFalClientTranscribeAudioUnmarshalDataText covers the structured-data path:
// when the result wraps the transcript under a "data" object (fal's usual shape),
// TranscribeAudio still extracts the text.
func TestFalClientTranscribeAudioUnmarshalDataText(t *testing.T) {
	model := defaultFalTranscribeModel
	client := newFalTestClient(t, falHandler(func(req *http.Request) (*http.Response, error) {
		switch {
		case req.Method == http.MethodPost && req.URL.Path == "/"+model:
			return jsonResp(`{"request_id":"req-data"}`), nil
		case strings.HasSuffix(req.URL.Path, "/status"):
			return jsonResp(`{"status":"COMPLETED"}`), nil
		case strings.HasSuffix(req.URL.Path, "/requests/req-data"):
			return jsonResp(`{"data":{"text":"nested transcript"}}`), nil
		default:
			t.Fatalf("unexpected request: %s %s", req.Method, req.URL.Path)
			return nil, nil
		}
	}))

	transcript, err := client.TranscribeAudio(context.Background(), model, "data:audio/mpeg;base64,AAAA", "", "")
	if err != nil {
		t.Fatalf("TranscribeAudio returned error: %v", err)
	}
	if transcript.Text != "nested transcript" {
		t.Errorf("transcript = %q, want \"nested transcript\"", transcript.Text)
	}
}
