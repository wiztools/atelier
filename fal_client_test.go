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
		case req.Method == http.MethodGet && req.URL.Path == "/"+model+"/requests/req-123":
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
	resp, raw, err := client.GenerateImage(ctx, ImageGenerateRequest{
		Model:  model,
		Prompt: "a lighthouse at dusk",
		Width:  768,
		Height: 768,
		Steps:  4,
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

	resp, _, err := client.GenerateImage(context.Background(), ImageGenerateRequest{Prompt: "x"})
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

	_, _, err := client.GenerateImage(context.Background(), ImageGenerateRequest{Model: "fal-ai/flux/schnell"})
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

	_, _, err := client.GenerateImage(context.Background(), ImageGenerateRequest{Model: model})
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
	_, _, err := client.GenerateImage(ctx, ImageGenerateRequest{Model: model})
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

	_, _, err := client.GenerateImage(context.Background(), ImageGenerateRequest{Model: model})
	if err == nil {
		t.Fatal("expected a no-images error, got nil")
	}
}

func TestFalClientRequiresAPIKey(t *testing.T) {
	client := newFalClient(&http.Client{}, "")
	_, _, err := client.GenerateImage(context.Background(), ImageGenerateRequest{Model: "fal-ai/flux/schnell"})
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

func TestCollectFalImagesFromJSON(t *testing.T) {
	raw := []byte(`{"data":{"nested":[{"url":"https://cdn.fal.ai/a.png"},{"url":"https://cdn.fal.ai/b.jpg"}]}}`)
	images := collectFalImagesFromJSON(raw)
	if len(images) != 2 {
		t.Fatalf("expected 2 images, got %d", len(images))
	}
}
