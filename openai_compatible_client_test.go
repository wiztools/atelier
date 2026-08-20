package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zalando/go-keyring"
)

func newOpenAICompatibleTestClient(transport http.RoundTripper, apiKey string) openAICompatibleClient {
	return newOpenAICompatibleClient(&http.Client{Transport: transport}, "http://images.test", apiKey)
}

// captureImageRequest records the request the client sent and replies with body.
func captureImageRequest(t *testing.T, captured *map[string]any, auth *string, body string) http.RoundTripper {
	t.Helper()
	return roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/v1/images/generations" {
			t.Fatalf("unexpected request path: %s", req.URL.Path)
		}
		if req.Method != http.MethodPost {
			t.Fatalf("expected POST, got %s", req.Method)
		}
		*auth = req.Header.Get("Authorization")
		data, err := io.ReadAll(req.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		decoded := map[string]any{}
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatalf("request body is not JSON: %v", err)
		}
		*captured = decoded
		return jsonResp(body), nil
	})
}

func TestOpenAICompatibleClientGenerateImageHappyPath(t *testing.T) {
	var captured map[string]any
	var auth string
	client := newOpenAICompatibleTestClient(
		captureImageRequest(t, &captured, &auth, `{"data":[{"b64_json":"`+tinyPNG+`"}]}`),
		"local-secret",
	)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, raw, err := client.GenerateImage(ctx, ImageGenerateRequest{
		Model:  "flux2-klein",
		Prompt: "a lighthouse at dusk",
		Width:  1024,
		Height: 576,
		Steps:  4,
		Images: []string{"data:image/png;base64,AAAA"},
	})
	if err != nil {
		t.Fatalf("GenerateImage returned error: %v", err)
	}
	if auth != "Bearer local-secret" {
		t.Errorf("Authorization = %q, want Bearer local-secret", auth)
	}
	if captured["model"] != "flux2-klein" {
		t.Errorf("body model = %v, want flux2-klein", captured["model"])
	}
	if captured["prompt"] != "a lighthouse at dusk" {
		t.Errorf("body prompt = %v, want a lighthouse at dusk", captured["prompt"])
	}
	if captured["size"] != "1024x576" {
		t.Errorf("body size = %v, want 1024x576", captured["size"])
	}
	if captured["steps"] != float64(4) {
		t.Errorf("body steps = %v, want 4", captured["steps"])
	}
	if captured["response_format"] != "b64_json" {
		t.Errorf("body response_format = %v, want b64_json", captured["response_format"])
	}
	if captured["n"] != float64(1) {
		t.Errorf("body n = %v, want 1", captured["n"])
	}
	if images, ok := captured["images"].([]any); !ok || len(images) != 1 || images[0] != "data:image/png;base64,AAAA" {
		t.Errorf("body images = %v, want the attached source forwarded", captured["images"])
	}
	if resp.Model != "flux2-klein" {
		t.Errorf("response model = %q, want flux2-klein", resp.Model)
	}
	if len(resp.Images) != 1 || !strings.HasPrefix(resp.Images[0], "data:image/png;base64,") {
		t.Fatalf("images = %v, want one png data url", resp.Images)
	}
	if resp.Image != resp.Images[0] {
		t.Errorf("Image field should mirror the first image")
	}
	if !resp.Done {
		t.Errorf("Done should be true")
	}
	// Same contract as the fal client: results are already data URLs, so raw
	// must stay nil or collectImagesFromJSON re-harvests from the payload.
	if raw != nil {
		t.Errorf("raw JSON should be nil, got %s", string(raw))
	}
}

// TestOpenAICompatibleClientGenerateImageOmitsOptionalFields pins the
// keyless/local-server defaults: no Authorization header without a key, and no
// size/steps fields when the request carries no explicit values.
func TestOpenAICompatibleClientGenerateImageOmitsOptionalFields(t *testing.T) {
	var captured map[string]any
	var auth string
	client := newOpenAICompatibleTestClient(
		captureImageRequest(t, &captured, &auth, `{"data":[{"b64_json":"`+tinyPNG+`"}]}`),
		"",
	)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _, err := client.GenerateImage(ctx, ImageGenerateRequest{Model: "m", Prompt: "p"})
	if err != nil {
		t.Fatalf("GenerateImage returned error: %v", err)
	}
	if auth != "" {
		t.Errorf("Authorization = %q, want empty for keyless server", auth)
	}
	for _, field := range []string{"size", "steps", "images"} {
		if _, present := captured[field]; present {
			t.Errorf("body should omit %s when unset, got %v", field, captured[field])
		}
	}
}

// TestOpenAICompatibleClientGenerateImageDownloadsURLResult verifies the url
// result shape: a server that ignores response_format and answers with a link
// still yields a data URL via the shared download path.
func TestOpenAICompatibleClientGenerateImageDownloadsURLResult(t *testing.T) {
	client := newOpenAICompatibleTestClient(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/v1/images/generations" {
			return jsonResp(`{"data":[{"url":"http://images.test/generated.png"}]}`), nil
		}
		if req.Method == http.MethodGet && req.URL.Path == "/generated.png" {
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Body:       io.NopCloser(strings.NewReader(string(mustDecodeTinyPNG()))),
				Header:     http.Header{"Content-Type": []string{"image/png"}},
			}, nil
		}
		t.Fatalf("unexpected request: %s %s", req.Method, req.URL.Path)
		return nil, nil
	}), "")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, _, err := client.GenerateImage(ctx, ImageGenerateRequest{Model: "m", Prompt: "p"})
	if err != nil {
		t.Fatalf("GenerateImage returned error: %v", err)
	}
	if len(resp.Images) != 1 || !strings.HasPrefix(resp.Images[0], "data:image/png;base64,") {
		t.Fatalf("images = %v, want the downloaded png as a data url", resp.Images)
	}
}

func TestOpenAICompatibleClientGenerateImageErrors(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		body    string
		wantErr string
	}{
		{"http error", http.StatusBadRequest, `{"error":{"message":"no such model"}}`, "no such model"},
		{"embedded error", http.StatusOK, `{"error":{"message":"quota exceeded"}}`, "quota exceeded"},
		{"empty data", http.StatusOK, `{"data":[]}`, "no images"},
		{"invalid base64", http.StatusOK, `{"data":[{"b64_json":"!!not base64!!"}]}`, "invalid base64"},
		{"non-image base64", http.StatusOK, `{"data":[{"b64_json":"aGVsbG8gd29ybGQ="}]}`, "not a supported image"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: tc.status,
					Status:     http.StatusText(tc.status),
					Body:       io.NopCloser(strings.NewReader(tc.body)),
					Header:     http.Header{"Content-Type": []string{"application/json"}},
				}, nil
			})
			client := newOpenAICompatibleTestClient(transport, "")
			_, _, err := client.GenerateImage(context.Background(), ImageGenerateRequest{Model: "m", Prompt: "p"})
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %q, want it to contain %q", err.Error(), tc.wantErr)
			}
		})
	}
}

func TestOpenAICompatibleClientListModels(t *testing.T) {
	var auth string
	client := newOpenAICompatibleTestClient(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodGet || req.URL.Path != "/v1/models" {
			t.Fatalf("unexpected request: %s %s", req.Method, req.URL.Path)
		}
		auth = req.Header.Get("Authorization")
		return jsonResp(`{"data":[{"id":"flux2-klein"},{"id":""},{"id":"z-image-turbo"}]}`), nil
	}), "local-secret")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ids, err := client.ListModels(ctx)
	if err != nil {
		t.Fatalf("ListModels returned error: %v", err)
	}
	if auth != "Bearer local-secret" {
		t.Errorf("Authorization = %q, want Bearer local-secret", auth)
	}
	if len(ids) != 2 || ids[0] != "flux2-klein" || ids[1] != "z-image-turbo" {
		t.Errorf("ids = %v, want [flux2-klein z-image-turbo] with blanks skipped", ids)
	}
}

func TestMergeAppConfigOpenAICompatibleProvider(t *testing.T) {
	config := defaultAppConfig()
	config.Models.ImageProvider = " openai-compatible "
	config.Providers.OpenAICompatible.BaseURL = "http://images.local:9000/"
	config.Providers.OpenAICompatible.Model = "flux2-klein"
	merged := mergeAppConfig(config)
	if merged.Models.ImageProvider != "openai-compatible" {
		t.Errorf("imageProvider = %q, want openai-compatible preserved", merged.Models.ImageProvider)
	}
	if merged.Providers.OpenAICompatible.BaseURL != "http://images.local:9000" {
		t.Errorf("baseURL = %q, want trailing slash trimmed", merged.Providers.OpenAICompatible.BaseURL)
	}
	if merged.Providers.OpenAICompatible.Model != "flux2-klein" {
		t.Errorf("model = %q, want flux2-klein preserved", merged.Providers.OpenAICompatible.Model)
	}

	unknown := mergeAppConfig(func() AppConfig {
		c := defaultAppConfig()
		c.Models.ImageProvider = "banana"
		return c
	}())
	if unknown.Models.ImageProvider != "ollama" {
		t.Errorf("unknown provider = %q, want fallback to ollama", unknown.Models.ImageProvider)
	}

	emptyEndpoint := mergeAppConfig(func() AppConfig {
		c := defaultAppConfig()
		c.Providers.OpenAICompatible.BaseURL = ""
		return c
	}())
	if emptyEndpoint.Providers.OpenAICompatible.BaseURL != defaultOpenAICompatibleBaseURL {
		t.Errorf("empty baseURL = %q, want default %q", emptyEndpoint.Providers.OpenAICompatible.BaseURL, defaultOpenAICompatibleBaseURL)
	}
}

func TestOpenAICompatibleKeychainRoundTrip(t *testing.T) {
	keyring.MockInit()
	t.Cleanup(func() { _ = clearOpenAICompatibleAPIKey() })

	key, err := loadOpenAICompatibleAPIKey()
	if err != nil {
		t.Fatalf("load with no key saved: %v", err)
	}
	if key != "" {
		t.Fatalf("absent key should load empty, got %q", key)
	}
	if err := saveOpenAICompatibleAPIKey(" local-key "); err != nil {
		t.Fatalf("saveOpenAICompatibleAPIKey: %v", err)
	}
	key, err = loadOpenAICompatibleAPIKey()
	if err != nil || key != "local-key" {
		t.Fatalf("saved key should load trimmed, got %q err=%v", key, err)
	}
	if err := clearOpenAICompatibleAPIKey(); err != nil {
		t.Fatalf("clearOpenAICompatibleAPIKey: %v", err)
	}
	key, err = loadOpenAICompatibleAPIKey()
	if err != nil || key != "" {
		t.Fatalf("cleared key should load empty, got %q err=%v", key, err)
	}
}

// TestHarnessGeneratesImageViaOpenAICompatible is the OpenAI-compatible
// counterpart of TestHarnessGeneratesImageViaFal: the full chat turn (triage →
// planner → generate_image tool → local /v1/images/generations server → final
// response) with the image artifact persisted to history. No key is stored —
// the keyless local-server path must work without Authorization.
func TestHarnessGeneratesImageViaOpenAICompatible(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	config := defaultAppConfig()
	config.Storage = ConfigStorage{
		Root:      filepath.Join(home, ".atelier"),
		History:   filepath.Join(home, ".atelier", "history"),
		Artifacts: filepath.Join(home, ".atelier", "history"),
	}
	config.Providers.Ollama.BaseURL = "http://ollama.test"
	config.Providers.Ollama.Models.Primary = "chat-box-model"
	config.Providers.Ollama.Models.Harness = "chat-box-model"
	config.Models.ImageProvider = "openai-compatible"
	config.Providers.OpenAICompatible.BaseURL = "http://images.test"
	config.Providers.OpenAICompatible.Model = "flux2-klein"
	if err := writeAppConfig(config); err != nil {
		t.Fatalf("writeAppConfig: %v", err)
	}

	app := NewApp()
	imageCalls := 0
	var imageAuth string
	var imageBody map[string]any
	nonStreamCount := 0
	prepCalls := 0
	app.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host == "images.test" {
			if req.URL.Path != "/v1/images/generations" {
				t.Fatalf("unexpected image server request: %s %s", req.Method, req.URL.Path)
			}
			imageCalls++
			imageAuth = req.Header.Get("Authorization")
			data, err := io.ReadAll(req.Body)
			if err != nil {
				t.Fatalf("read image request body: %v", err)
			}
			imageBody = map[string]any{}
			if err := json.Unmarshal(data, &imageBody); err != nil {
				t.Fatalf("image request body is not JSON: %v", err)
			}
			return jsonResponse(`{"data":[{"b64_json":"` + tinyPNG + `"}]}`), nil
		}
		switch req.URL.Path {
		case "/api/show":
			return jsonResponse(`{"capabilities":[],"model_info":{},"details":{"family":"test","parameter_size":"1B"}}`), nil
		case "/api/chat":
			payload := chatPayload(t, req)
			if payload["stream"] == false {
				nonStreamCount++
				if nonStreamCount == 1 {
					decision := `{"needsTools":true,"responseMode":"image","toolTask":"Generate an image of a small house.","reason":"The user asked for an image."}`
					return chatCompletion("chat-box-model", decision), nil
				}
				prepCalls++
				body := `{"brief":"Generate the requested image.","needsTools":true,"reason":"image","toolCalls":[{"name":"generate_image","content":"a small house with a red roof"}]}`
				if prepCalls > 1 {
					body = `{"brief":"The image was generated.","needsTools":false,"reason":"done","toolCalls":[]}`
				}
				return chatCompletion("chat-box-model", body), nil
			}
			body := `{"model":"chat-box-model","message":{"role":"assistant","content":"Here is the small house."},"done":false}` + "\n" +
				`{"model":"chat-box-model","done":true,"done_reason":"stop","eval_count":3}` + "\n"
			return &http.Response{StatusCode: 200, Status: "200 OK",
				Body:   io.NopCloser(strings.NewReader(body)),
				Header: http.Header{"Content-Type": []string{"application/x-ndjson"}}}, nil
		default:
			t.Fatalf("unexpected request %s %s", req.Method, req.URL)
			return nil, nil
		}
	})

	app.runChatStream(context.Background(), "request-openai-image", ChatRequest{
		BaseURL: "http://ollama.test",
		Model:   "chat-box-model",
		Messages: []ChatMessage{
			{Role: "user", Content: "Create an image of a small house"},
		},
	})

	if imageCalls != 1 {
		t.Fatalf("image server calls = %d, want 1", imageCalls)
	}
	if imageAuth != "" {
		t.Errorf("Authorization = %q, want empty for keyless server", imageAuth)
	}
	if imageBody["model"] != "flux2-klein" {
		t.Errorf("image request model = %v, want flux2-klein", imageBody["model"])
	}
	if imageBody["prompt"] != "a small house with a red roof" {
		t.Errorf("image request prompt = %v, want the planner's tool content", imageBody["prompt"])
	}
	if imageBody["response_format"] != "b64_json" {
		t.Errorf("image request response_format = %v, want b64_json", imageBody["response_format"])
	}
	if _, ok := imageBody["size"]; !ok {
		t.Errorf("image request should carry a size from the aspect-ratio preset, got %v", imageBody)
	}

	conversations, err := listConversations(config.Storage)
	if err != nil {
		t.Fatalf("listConversations: %v", err)
	}
	if len(conversations) != 1 {
		t.Fatalf("conversation count = %d, want 1", len(conversations))
	}
	detail, err := getConversation(config.Storage, conversations[0].ID)
	if err != nil {
		t.Fatalf("getConversation: %v", err)
	}
	if len(detail.Turns) != 2 {
		t.Fatalf("turn count = %d, want user + assistant", len(detail.Turns))
	}
	if detail.Conversation.Stats.ArtifactCount != 1 {
		t.Fatalf("artifactCount = %d, want 1", detail.Conversation.Stats.ArtifactCount)
	}
	assistant := detail.Turns[1]
	images := historyImagesForTest(assistant.Content)
	if len(images) != 1 {
		t.Fatalf("assistant image content = %+v, want one image artifact", assistant.Content)
	}
	tool, ok := assistant.ProviderResponse["tool"].(map[string]any)
	if !ok || tool["name"] != "image_generation" || tool["model"] != "flux2-klein" {
		t.Fatalf("assistant provider tool = %+v, want image_generation via flux2-klein", assistant.ProviderResponse["tool"])
	}
}
