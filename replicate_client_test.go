package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
)

// tinyReplicateMP4 reuses fal_client_test.go's tinyMP4 (an "ftyp" box header,
// all isVideoBytes needs) — bound once here for the media handlers below.
var tinyReplicateMP4 = tinyMP4()

func newReplicateTestClient(transport http.RoundTripper) ReplicateClient {
	return newReplicateClient(&http.Client{Transport: transport}, "test-key")
}

// replicatePredictionHandler serves the create → poll → fetch lifecycle for
// one prediction: POST /v1/models/{owner}/{name}/predictions returns the id,
// GET /v1/predictions/{id} walks statuses, and the media URL is downloadable.
func replicatePredictionHandler(t *testing.T, statuses []string, output string, mediaPath string, mediaBody []byte, mediaHeader string) http.RoundTripper {
	t.Helper()
	fetches := 0
	return roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch {
		case req.Method == http.MethodPost && strings.HasPrefix(req.URL.Path, "/v1/models/") && strings.HasSuffix(req.URL.Path, "/predictions"):
			if got := req.Header.Get("Authorization"); !strings.HasPrefix(got, "Bearer ") {
				t.Fatalf("Authorization = %q, want a Bearer token", got)
			}
			var body map[string]any
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				t.Fatalf("decode create body: %v", err)
			}
			if _, ok := body["input"]; !ok {
				t.Fatal("create body must wrap the input map under \"input\"")
			}
			return jsonResp(`{"id":"pred-1","status":"starting"}`), nil
		case req.Method == http.MethodGet && req.URL.Path == "/v1/predictions/pred-1":
			if fetches < len(statuses) {
				status := statuses[fetches]
				fetches++
				return jsonResp(`{"id":"pred-1","status":"` + status + `"}`), nil
			}
			return jsonResp(`{"id":"pred-1","status":"succeeded","output":` + output + `}`), nil
		case req.Method == http.MethodGet && req.URL.Path == mediaPath:
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Body:       io.NopCloser(strings.NewReader(string(mediaBody))),
				Header:     http.Header{"Content-Type": []string{mediaHeader}},
			}, nil
		default:
			t.Fatalf("unexpected request: %s %s", req.Method, req.URL.Path)
			return nil, nil
		}
	})
}

func TestReplicateGenerateImageHappyPath(t *testing.T) {
	client := newReplicateTestClient(replicatePredictionHandler(t,
		[]string{"processing"},
		`"https://replicate.delivery/example/output.png"`,
		"/example/output.png", mustDecodeTinyPNG(), "image/png"))
	resp, err := client.GenerateImage(context.Background(), "black-forest-labs/flux-schnell", map[string]any{"prompt": "a red panda"})
	if err != nil {
		t.Fatalf("GenerateImage: %v", err)
	}
	if resp.Model != "black-forest-labs/flux-schnell" {
		t.Fatalf("Model = %q", resp.Model)
	}
	if len(resp.Images) != 1 || !strings.HasPrefix(resp.Images[0], "data:image/png;base64,") {
		t.Fatalf("Images = %v, want one png data URL", resp.Images)
	}
	if resp.Image != resp.Images[0] {
		t.Fatal("Image should mirror the first entry")
	}
}

func TestReplicateGenerateImageOutputArray(t *testing.T) {
	client := newReplicateTestClient(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/v1/models/owner/model/predictions":
			return jsonResp(`{"id":"pred-1","status":"starting"}`), nil
		case "/v1/predictions/pred-1":
			return jsonResp(`{"id":"pred-1","status":"succeeded","output":["https://replicate.delivery/a/one.png","https://replicate.delivery/b/two.png"]}`), nil
		case "/a/one.png":
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Body:       io.NopCloser(strings.NewReader(string(mustDecodeTinyPNG()))),
				Header:     http.Header{"Content-Type": []string{"image/png"}},
			}, nil
		default:
			// The second URL 404s; downloadImages keeps the decodable entry.
			return &http.Response{StatusCode: http.StatusNotFound, Status: "404", Body: io.NopCloser(strings.NewReader(""))}, nil
		}
	}))
	resp, err := client.GenerateImage(context.Background(), "owner/model", map[string]any{"prompt": "x"})
	if err != nil {
		t.Fatalf("GenerateImage: %v", err)
	}
	if len(resp.Images) != 1 || !strings.HasPrefix(resp.Images[0], "data:image/png;base64,") {
		t.Fatalf("Images = %v, want the one decodable entry", resp.Images)
	}
}

func TestReplicateGenerateVideoHappyPath(t *testing.T) {
	client := newReplicateTestClient(replicatePredictionHandler(t,
		nil,
		`"https://replicate.delivery/v/clip.mp4"`,
		"/v/clip.mp4", tinyReplicateMP4, "video/mp4"))
	generated, err := client.GenerateVideo(context.Background(), "wan-video/wan-2.5-t2v", map[string]any{"prompt": "waves"})
	if err != nil {
		t.Fatalf("GenerateVideo: %v", err)
	}
	if len(generated.Data) != len(tinyReplicateMP4) || generated.MimeType != "video/mp4" {
		t.Fatalf("generated = %d bytes %q", len(generated.Data), generated.MimeType)
	}
	if generated.SourceURL != "https://replicate.delivery/v/clip.mp4" {
		t.Fatalf("SourceURL = %q", generated.SourceURL)
	}
}

func TestReplicateFailedPrediction(t *testing.T) {
	client := newReplicateTestClient(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method == http.MethodPost {
			return jsonResp(`{"id":"pred-1","status":"starting"}`), nil
		}
		return jsonResp(`{"id":"pred-1","status":"failed","error":"CUDA out of memory"}`), nil
	}))
	_, err := client.GenerateImage(context.Background(), "owner/model", map[string]any{"prompt": "x"})
	if err == nil || !strings.Contains(err.Error(), "CUDA out of memory") {
		t.Fatalf("err = %v, want the prediction's error text", err)
	}
}

func TestReplicateCanceledPrediction(t *testing.T) {
	client := newReplicateTestClient(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method == http.MethodPost {
			return jsonResp(`{"id":"pred-1","status":"starting"}`), nil
		}
		return jsonResp(`{"id":"pred-1","status":"canceled"}`), nil
	}))
	if _, err := client.GenerateImage(context.Background(), "owner/model", nil); err == nil || !strings.Contains(err.Error(), "canceled") {
		t.Fatalf("err = %v, want canceled", err)
	}
}

func TestReplicateErrorMapping(t *testing.T) {
	cases := []struct {
		name     string
		status   int
		body     string
		contains string
	}{
		{"auth", http.StatusUnauthorized, `{"title":"Unauthenticated","detail":"Invalid token"}`, "replicate authentication failed"},
		{"rate limited", http.StatusTooManyRequests, `{"title":"Too Many Requests"}`, "replicate rate limited"},
		{"credit exhausted", http.StatusPaymentRequired, `{"detail":"Out of credit"}`, "replicate credit exhausted"},
		{"validation", http.StatusUnprocessableEntity, `{"title":"Invalid input","detail":"prompt is required"}`, "Invalid input: prompt is required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := newReplicateTestClient(roundTripFunc(func(req *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: tc.status,
					Status:     http.StatusText(tc.status),
					Body:       io.NopCloser(strings.NewReader(tc.body)),
					Header:     http.Header{"Content-Type": []string{"application/json"}},
				}, nil
			}))
			_, err := client.GenerateImage(context.Background(), "owner/model", nil)
			if err == nil || !strings.Contains(err.Error(), tc.contains) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.contains)
			}
		})
	}
}

func TestReplicateInvalidModelID(t *testing.T) {
	client := newReplicateTestClient(roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("no request should be issued for a malformed id")
		return nil, nil
	}))
	if _, err := client.GenerateImage(context.Background(), "no-slash", nil); err == nil || !strings.Contains(err.Error(), "owner/name") {
		t.Fatalf("err = %v, want the owner/name diagnostic", err)
	}
}

func TestReplicateClientKeyRequired(t *testing.T) {
	client := newReplicateClient(&http.Client{}, "")
	if _, err := client.GenerateImage(context.Background(), "owner/model", nil); err != errReplicateKeyNotConfigured {
		t.Fatalf("err = %v, want errReplicateKeyNotConfigured", err)
	}
}

func TestReplicateImageURLCandidates(t *testing.T) {
	cases := []struct {
		name   string
		output string
		first  string
	}{
		{"string", `"https://replicate.delivery/out.png"`, "https://replicate.delivery/out.png"},
		{"array of strings", `["https://replicate.delivery/1.png","https://replicate.delivery/2.png"]`, "https://replicate.delivery/1.png"},
		{"array of objects", `[{"url":"https://replicate.delivery/x.png"}]`, "https://replicate.delivery/x.png"},
		{"nested walk", `{"images":["https://replicate.delivery/deep/result.webp"]}`, "https://replicate.delivery/deep/result.webp"},
		{"extensionless url", `["https://replicate.delivery/mbxt/xyz"]`, "https://replicate.delivery/mbxt/xyz"},
		{"empty", `null`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			urls := replicateImageURLCandidates(json.RawMessage(tc.output))
			if tc.first == "" {
				if len(urls) != 0 {
					t.Fatalf("urls = %v, want none", urls)
				}
				return
			}
			if len(urls) == 0 || urls[0] != tc.first {
				t.Fatalf("urls = %v, want first %q", urls, tc.first)
			}
		})
	}
}

func TestReplicateVideoURL(t *testing.T) {
	cases := []struct {
		output string
		want   string
	}{
		{`"https://replicate.delivery/v/a.mp4"`, "https://replicate.delivery/v/a.mp4"},
		{`["https://replicate.delivery/v/b.webm"]`, "https://replicate.delivery/v/b.webm"},
		{`{"video":"https://x.test/c.mp4","other":1}`, "https://x.test/c.mp4"},
		{`"not-a-url"`, ""},
		// A direct array shape wins before extension filtering — an
		// extensionless first entry is still the output clip; if it isn't, the
		// download's isVideoBytes check fails with a clear error.
		{`["https://replicate.delivery/no-ext"]`, "https://replicate.delivery/no-ext"},
	}
	for _, tc := range cases {
		if got := replicateVideoURL(json.RawMessage(tc.output)); got != tc.want {
			t.Fatalf("replicateVideoURL(%s) = %q, want %q", tc.output, got, tc.want)
		}
	}
}

func TestReplicateListCollectionModelsFiltersOfficial(t *testing.T) {
	client := newReplicateTestClient(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodGet || req.URL.Path != "/v1/collections/text-to-video" {
			t.Fatalf("unexpected request: %s %s", req.Method, req.URL.Path)
		}
		return jsonResp(`{
			"slug": "text-to-video",
			"models": [
				{"owner":"wan-video","name":"wan-2.5-t2v","description":"Wan 2.5","is_official":true,"cover_image_url":"https://replicate.delivery/pic.jpg"},
				{"owner":"someone","name":"community-model","is_official":false},
				{"owner":"kwaivgi","name":"kling-v2.5-turbo-pro","is_official":true}
			]
		}`), nil
	}))
	models, err := client.ListCollectionModels(context.Background(), replicateTextToVideoCollection)
	if err != nil {
		t.Fatalf("ListCollectionModels: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("models = %v, want the two official entries", models)
	}
	if models[0].ID != "wan-video/wan-2.5-t2v" || models[0].DisplayName != "wan-2.5-t2v" || models[0].Category != "text-to-video" {
		t.Fatalf("first model = %+v", models[0])
	}
	if models[0].ThumbnailURL != "https://replicate.delivery/pic.jpg" {
		t.Fatalf("ThumbnailURL = %q", models[0].ThumbnailURL)
	}
}

func TestReplicateGetModelInputSchema(t *testing.T) {
	client := newReplicateTestClient(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodGet || req.URL.Path != "/v1/models/owner/name" {
			t.Fatalf("unexpected request: %s %s", req.Method, req.URL.Path)
		}
		return jsonResp(`{"owner":"owner","name":"name","latest_version":{"id":"v1","openapi_schema":{"components":{"schemas":{"Input":{"type":"object","properties":{"prompt":{"type":"string"}}}}}}}}`), nil
	}))
	raw, err := client.GetModelInputSchema(context.Background(), "owner/name")
	if err != nil {
		t.Fatalf("GetModelInputSchema: %v", err)
	}
	schema, err := parseModelInputSchema(raw)
	if err != nil {
		t.Fatalf("parseModelInputSchema on the replicate doc: %v", err)
	}
	if _, ok := schema.property("prompt"); !ok {
		t.Fatal("parsed schema should carry the Input properties")
	}
}

func TestReplicateVerifyKey(t *testing.T) {
	client := newReplicateTestClient(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/v1/account" {
			t.Fatalf("unexpected request: %s %s", req.Method, req.URL.Path)
		}
		return jsonResp(`{"type":"user","username":"me"}`), nil
	}))
	if err := client.VerifyKey(context.Background()); err != nil {
		t.Fatalf("VerifyKey: %v", err)
	}
}

func TestReplicateUploadFile(t *testing.T) {
	client := newReplicateTestClient(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodPost || req.URL.Path != "/v1/files" {
			t.Fatalf("unexpected request: %s %s", req.Method, req.URL.Path)
		}
		if !strings.HasPrefix(req.Header.Get("Content-Type"), "multipart/form-data") {
			t.Fatalf("Content-Type = %q, want multipart", req.Header.Get("Content-Type"))
		}
		if err := req.ParseMultipartForm(1 << 20); err != nil {
			t.Fatalf("ParseMultipartForm: %v", err)
		}
		file, header, err := req.FormFile("content")
		if err != nil {
			t.Fatalf("FormFile(content): %v", err)
		}
		defer file.Close()
		if header.Filename != "source-image.png" {
			t.Fatalf("filename = %q", header.Filename)
		}
		return jsonResp(`{"id":"file-1","urls":{"get":"https://replicate.delivery/files/file-1"}}`), nil
	}))
	url, err := client.UploadFile(context.Background(), []byte("pngbytes"), "source-image.png", "image/png")
	if err != nil {
		t.Fatalf("UploadFile: %v", err)
	}
	if url != "https://replicate.delivery/files/file-1" {
		t.Fatalf("url = %q", url)
	}
}

func TestReplicateResolveMediaURL(t *testing.T) {
	// Hosted URLs pass through untouched.
	passThrough := newReplicateTestClient(roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("no request expected for a hosted URL")
		return nil, nil
	}))
	if got, err := passThrough.ResolveMediaURL(context.Background(), "https://x.test/a.png", "image/png", "a.png"); err != nil || got != "https://x.test/a.png" {
		t.Fatalf("pass-through = %q, %v", got, err)
	}

	// Small data URIs stay inline; no upload request is issued.
	small := newReplicateTestClient(roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("no upload expected for a small payload")
		return nil, nil
	}))
	if got, err := small.ResolveMediaURL(context.Background(), "data:image/png;base64,"+tinyPNG, "image/png", "a.png"); err != nil || !strings.HasPrefix(got, "data:image/png;base64,") {
		t.Fatalf("small payload = %q, %v", got, err)
	}

	// Oversized payloads upload and return the hosted URL.
	bigPayload := make([]byte, replicateInlineMediaMaxBytes+1)
	for i := range bigPayload {
		bigPayload[i] = byte('a' + i%26)
	}
	big := newReplicateTestClient(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/v1/files" {
			t.Fatalf("unexpected request: %s %s", req.Method, req.URL.Path)
		}
		return jsonResp(`{"urls":{"get":"https://replicate.delivery/files/big"}}`), nil
	}))
	if got, err := big.ResolveMediaURL(context.Background(), "data:image/png;base64,"+base64.StdEncoding.EncodeToString(bigPayload), "image/png", "big.png"); err != nil || got != "https://replicate.delivery/files/big" {
		t.Fatalf("big payload = %q, %v", got, err)
	}

	// Upload failure falls back to the inline data URI, not an error.
	failing := newReplicateTestClient(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusInternalServerError, Status: "500", Body: io.NopCloser(strings.NewReader("boom"))}, nil
	}))
	got, err := failing.ResolveMediaURL(context.Background(), "data:image/png;base64,"+base64.StdEncoding.EncodeToString(bigPayload), "image/png", "big.png")
	if err != nil || !strings.HasPrefix(got, "data:image/png;base64,") {
		t.Fatalf("fail-soft = %q, %v", got, err)
	}
}

// TestReplicateGatewayImageRouting drives the gateway's GenerateImage
// replicate branch end to end against a mocked transport: key load, schema
// fetch, input resolution, prediction lifecycle, and result normalization.
func TestReplicateGatewayImageRouting(t *testing.T) {
	keyring.MockInit()
	t.Cleanup(func() { _ = clearReplicateAPIKey() })
	if err := saveReplicateAPIKey("replicate-test-key"); err != nil {
		t.Fatalf("saveReplicateAPIKey: %v", err)
	}

	schemaJSON := `{"components":{"schemas":{"Input":{"type":"object","properties":{
		"prompt":{"type":"string"},
		"image":{"type":"string"},
		"aspect_ratio":{"type":"string","enum":["1:1","16:9","9:16"]},
		"num_outputs":{"type":"integer","default":1}
	}}}}}`
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch {
		case req.Method == http.MethodGet && req.URL.Path == "/v1/models/owner/art-model":
			return jsonResp(`{"latest_version":{"openapi_schema":` + schemaJSON + `}}`), nil
		case req.Method == http.MethodPost && req.URL.Path == "/v1/models/owner/art-model/predictions":
			var body struct {
				Input map[string]any `json:"input"`
			}
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				t.Fatalf("decode prediction body: %v", err)
			}
			if body.Input["prompt"] != "a lighthouse" {
				t.Fatalf("input.prompt = %v", body.Input["prompt"])
			}
			if _, exists := body.Input["num_images"]; exists {
				t.Fatal("replicate input must not carry speculative fal-side keys like num_images")
			}
			return jsonResp(`{"id":"pred-9","status":"starting"}`), nil
		case req.Method == http.MethodGet && req.URL.Path == "/v1/predictions/pred-9":
			return jsonResp(`{"id":"pred-9","status":"succeeded","output":"https://replicate.delivery/out/art.png"}`), nil
		case req.Method == http.MethodGet && req.URL.Path == "/out/art.png":
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
	})
	app := &App{client: &http.Client{Transport: transport}}
	config := defaultAppConfig()
	config.Storage.Root = t.TempDir()
	config.Models.ImageProvider = "replicate"
	config.Providers.Replicate.Model = "owner/art-model"
	gateway := newToolGateway(app, config)
	resp, _, _, err := gateway.tools.GenerateImage(context.Background(), ImageGenerateRequest{
		Model:       "owner/art-model",
		Prompt:      "a lighthouse",
		Images:      []string{"data:image/png;base64," + tinyPNG},
		AspectRatio: "16:9",
	})
	if err != nil {
		t.Fatalf("GenerateImage: %v", err)
	}
	if len(resp.Images) != 1 {
		t.Fatalf("Images = %v", resp.Images)
	}
	if resp.CostMicros != 0 {
		t.Fatalf("CostMicros = %d, want 0 (unknown, not free)", resp.CostMicros)
	}
}

// TestReplicateGatewayVideoRouting drives the gateway's GenerateVideo
// replicate branch, including the video-source refusal.
func TestReplicateGatewayVideoRouting(t *testing.T) {
	keyring.MockInit()
	t.Cleanup(func() { _ = clearReplicateAPIKey() })
	if err := saveReplicateAPIKey("replicate-test-key"); err != nil {
		t.Fatalf("saveReplicateAPIKey: %v", err)
	}

	videoSchema := `{"components":{"schemas":{"Input":{"type":"object","properties":{
		"prompt":{"type":"string"},
		"duration":{"type":"number"},
		"aspect_ratio":{"type":"string","enum":["16:9","9:16","1:1"]},
		"image":{"type":"string"}
	}}}}}`
	predictions := replicatePredictionHandler(t,
		nil,
		`"https://replicate.delivery/out/clip.mp4"`,
		"/out/clip.mp4", tinyReplicateMP4, "video/mp4")
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method == http.MethodGet && req.URL.Path == "/v1/models/wan-video/wan-2.5-t2v" {
			return jsonResp(`{"latest_version":{"openapi_schema":` + videoSchema + `}}`), nil
		}
		return predictions.RoundTrip(req)
	})
	app := &App{client: &http.Client{Transport: transport}}
	config := defaultAppConfig()
	config.Storage.Root = t.TempDir()
	config.Models.VideoProvider = "replicate"
	config.Providers.Replicate.VideoModel = "wan-video/wan-2.5-t2v"
	gateway := newToolGateway(app, config)

	generated, err := gateway.tools.GenerateVideo(context.Background(), VideoGenerateRequest{
		Model:  "wan-video/wan-2.5-t2v",
		Prompt: "a storm",
	})
	if err != nil {
		t.Fatalf("GenerateVideo: %v", err)
	}
	if len(generated.Data) == 0 {
		t.Fatal("no video bytes returned")
	}
	if generated.CostMicros != 0 {
		t.Fatalf("CostMicros = %d, want 0", generated.CostMicros)
	}

	// A video-source request fails with the deterministic backend error
	// before any prediction is created.
	_, err = gateway.tools.GenerateVideo(context.Background(), VideoGenerateRequest{
		Model:  "wan-video/wan-2.5-t2v",
		Prompt: "extend this",
		Videos: []string{"data:video/mp4;base64,AAAA"},
	})
	if err == nil || !strings.Contains(err.Error(), "Replicate video backend") {
		t.Fatalf("err = %v, want the video-source refusal", err)
	}
}

// TestMediaProviderRouting pins the two routing seams: the image switch and
// the new video switch, including the empty-value defaults.
func TestMediaProviderRouting(t *testing.T) {
	cases := []struct {
		name      string
		config    AppConfig
		imageWant string
		videoWant string
	}{
		{"defaults", AppConfig{}, "ollama", "fal"},
		{"fal selected", AppConfig{Models: ConfigModels{ImageProvider: "fal", VideoProvider: "fal"}}, "fal", "fal"},
		{"replicate selected", AppConfig{Models: ConfigModels{ImageProvider: "replicate", VideoProvider: "replicate"}}, "replicate", "replicate"},
		{"unknown ids", AppConfig{Models: ConfigModels{ImageProvider: "dalle", VideoProvider: "runware"}}, "ollama", "fal"},
		{"openai-compatible image", AppConfig{Models: ConfigModels{ImageProvider: "openai-compatible"}}, "openai-compatible", "fal"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := imageGenerationProvider(tc.config); got != tc.imageWant {
				t.Fatalf("imageGenerationProvider = %q, want %q", got, tc.imageWant)
			}
			if got := videoGenerationProvider(tc.config); got != tc.videoWant {
				t.Fatalf("videoGenerationProvider = %q, want %q", got, tc.videoWant)
			}
		})
	}
}

// TestReplicateVideoToolGating mirrors TestVideoGenerationToolGating for the
// replicate branch: the tool registers only with a Replicate video model AND
// its key, and the default resolvers route by provider.
func TestReplicateVideoToolGating(t *testing.T) {
	keyring.MockInit()
	t.Cleanup(func() {
		_ = clearReplicateAPIKey()
		_ = clearFalAPIKey()
	})

	replicateConfigured := defaultAppConfig()
	replicateConfigured.Models.VideoProvider = "replicate"
	replicateConfigured.Providers.Replicate.VideoModel = "wan-video/wan-2.5-t2v"
	if videoGenerationConfigured(replicateConfigured) {
		t.Fatal("replicate video should not be configured without a replicate key")
	}
	if err := saveReplicateAPIKey("replicate-test-key"); err != nil {
		t.Fatalf("saveReplicateAPIKey: %v", err)
	}
	if !videoGenerationConfigured(replicateConfigured) {
		t.Fatal("replicate video should be configured with a model and key")
	}
	if _, ok := defaultHarnessToolRegistry(context.Background(), replicateConfigured, nil).Get("generate_video"); !ok {
		t.Fatal("generate_video should be registered on the replicate path")
	}
	if got := resolveDefaultVideoModel(replicateConfigured); got != "wan-video/wan-2.5-t2v" {
		t.Fatalf("resolveDefaultVideoModel = %q", got)
	}
	if got := resolveDefaultVideoImageModel(replicateConfigured); got != defaultReplicateVideoImageModel {
		t.Fatalf("resolveDefaultVideoImageModel = %q, want the default", got)
	}

	// With no replicate key but a fal key + fal models, the fal default path
	// still routes — the provider dimension selects the backend.
	_ = clearReplicateAPIKey()
	if err := saveFalAPIKey("fal-test-key"); err != nil {
		t.Fatalf("saveFalAPIKey: %v", err)
	}
	falConfig := defaultAppConfig()
	falConfig.Providers.Fal.VideoModel = "fal-ai/some/video-model"
	if !videoGenerationConfigured(falConfig) {
		t.Fatal("fal video should be configured with a fal model and key")
	}
	if got := resolveDefaultVideoModel(falConfig); got != "fal-ai/some/video-model" {
		t.Fatalf("resolveDefaultVideoModel = %q", got)
	}
	// Provider wins: a replicate-selected config with fal slots populated
	// resolves the replicate default, not the fal model.
	if got := resolveDefaultVideoModel(replicateConfigured); got != "wan-video/wan-2.5-t2v" {
		t.Fatalf("provider-routed resolveDefaultVideoModel = %q", got)
	}
}

// TestReplicateVideoToolRefusesVideoSource drives the generate_video tool
// executor on the replicate path: a video-source or keyframe turn fails with
// the backend refusal before any model is selected.
func TestReplicateVideoToolRefusesVideoSource(t *testing.T) {
	keyring.MockInit()
	t.Cleanup(func() { _ = clearReplicateAPIKey() })
	if err := saveReplicateAPIKey("replicate-test-key"); err != nil {
		t.Fatalf("saveReplicateAPIKey: %v", err)
	}
	config := defaultAppConfig()
	config.Models.VideoProvider = "replicate"
	config.Providers.Replicate.VideoModel = "wan-video/wan-2.5-t2v"
	definition := videoGenerationToolDefinition(config, false)

	newTools := func() HarnessToolExecutionContext {
		tools := newHarnessToolExecutionContext(config)
		// The refusal fires before any model is resolved; a GenerateVideo
		// stub that fails the test proves it never got that far.
		tools.GenerateVideo = func(context.Context, VideoGenerateRequest) (GeneratedVideo, error) {
			t.Fatal("GenerateVideo must not be reached on a refused turn")
			return GeneratedVideo{}, nil
		}
		return tools
	}

	tools := newTools()
	tools.AttachedVideos = []string{"data:video/mp4;base64,AAAA"}
	_, _, err := definition.Execute(context.Background(), tools, HarnessToolCall{Name: "generate_video", Content: "extend this"})
	if err == nil || !strings.Contains(err.Error(), "Replicate video backend") {
		t.Fatalf("err = %v, want the backend refusal", err)
	}

	tools = newTools()
	tools.AttachedImages = []string{"data:image/png;base64," + tinyPNG, "data:image/png;base64," + tinyPNG}
	_, _, err = definition.Execute(context.Background(), tools, HarnessToolCall{Name: "generate_video", Content: "transition", ImageRole: "keyframes"})
	if err == nil || !strings.Contains(err.Error(), "Replicate video backend") {
		t.Fatalf("keyframes err = %v, want the backend refusal", err)
	}
}

// TestReplicateVideoActivityCommandAttribution checks the activity Command's
// provider token follows the routed backend.
func TestReplicateVideoActivityCommandAttribution(t *testing.T) {
	config := defaultAppConfig()
	config.Models.VideoProvider = "replicate"
	definition := videoGenerationToolDefinition(config, false)
	result := HarnessToolResult{Name: "generate_video", Status: "completed", Result: ToolVideoResult{Model: "wan-video/wan-2.5-t2v", Count: 1}}
	activity := definition.Activity(result)
	if len(activity.Command) != 3 || activity.Command[0] != "replicate" {
		t.Fatalf("Command = %v, want [replicate generate <model>]", activity.Command)
	}
}

// TestMergeAppConfigReplicateProviders pins the config normalization: the
// replicate whitelists and the default-model seed.
func TestMergeAppConfigReplicateProviders(t *testing.T) {
	config := defaultAppConfig()
	config.Models.ImageProvider = "replicate"
	config.Models.VideoProvider = "replicate"
	merged := mergeAppConfig(config)
	if merged.Models.ImageProvider != "replicate" || merged.Models.VideoProvider != "replicate" {
		t.Fatalf("providers = %q/%q", merged.Models.ImageProvider, merged.Models.VideoProvider)
	}
	if merged.Providers.Replicate.Model != defaultReplicateImageModel {
		t.Fatalf("Replicate.Model = %q, want the seeded default", merged.Providers.Replicate.Model)
	}

	unknown := defaultAppConfig()
	unknown.Models.ImageProvider = "runware"
	unknown.Models.VideoProvider = "runware"
	merged = mergeAppConfig(unknown)
	if merged.Models.ImageProvider != "ollama" {
		t.Fatalf("unknown image provider = %q, want ollama", merged.Models.ImageProvider)
	}
	if merged.Models.VideoProvider != "fal" {
		t.Fatalf("unknown video provider = %q, want fal", merged.Models.VideoProvider)
	}

	legacy := defaultAppConfig()
	legacy.Models.VideoProvider = ""
	merged = mergeAppConfig(legacy)
	if merged.Models.VideoProvider != "fal" {
		t.Fatalf("empty video provider = %q, want fal", merged.Models.VideoProvider)
	}
}

// TestReplicateUpscaleImageHappyPath drives the client's upscale transport:
// prediction lifecycle → output URL → downloaded data URL packed into the
// shared ollamaGenerateResponse shape.
func TestReplicateUpscaleImageHappyPath(t *testing.T) {
	client := newReplicateTestClient(replicatePredictionHandler(t,
		nil,
		`"https://replicate.delivery/out/up.png"`,
		"/out/up.png", mustDecodeTinyPNG(), "image/png"))
	resp, err := client.UpscaleImage(context.Background(), "nightmareai/real-esrgan", map[string]any{"image": "data:image/png;base64," + tinyPNG, "scale": 2})
	if err != nil {
		t.Fatalf("UpscaleImage: %v", err)
	}
	if resp.Model != "nightmareai/real-esrgan" || len(resp.Images) != 1 || !strings.HasPrefix(resp.Images[0], "data:image/png;base64,") {
		t.Fatalf("resp = %+v", resp)
	}
}

// TestReplicateGatewayUpscaleRouting drives the gateway's UpscaleImage
// replicate branch end to end, including the input resolution against the
// model's schema.
func TestReplicateGatewayUpscaleRouting(t *testing.T) {
	keyring.MockInit()
	t.Cleanup(func() { _ = clearReplicateAPIKey() })
	if err := saveReplicateAPIKey("replicate-test-key"); err != nil {
		t.Fatalf("saveReplicateAPIKey: %v", err)
	}

	schemaJSON := `{"components":{"schemas":{"Input":{"type":"object","properties":{
		"image":{"type":"string"},
		"scale":{"type":"number","minimum":0,"maximum":10},
		"face_enhance":{"type":"boolean","default":false}
	}}}}}`
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch {
		case req.Method == http.MethodGet && req.URL.Path == "/v1/models/nightmareai/real-esrgan":
			return jsonResp(`{"latest_version":{"openapi_schema":` + schemaJSON + `}}`), nil
		case req.Method == http.MethodPost && req.URL.Path == "/v1/models/nightmareai/real-esrgan/predictions":
			var body struct {
				Input map[string]any `json:"input"`
			}
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				t.Fatalf("decode prediction body: %v", err)
			}
			if got, ok := body.Input["image"].(string); !ok || !strings.HasPrefix(got, "data:image/png;base64,") {
				t.Fatalf("input.image = %v", body.Input["image"])
			}
			if body.Input["scale"] != float64(2) && body.Input["scale"] != 2 {
				t.Fatalf("input.scale = %v (%T), want numeric 2", body.Input["scale"], body.Input["scale"])
			}
			return jsonResp(`{"id":"pred-7","status":"starting"}`), nil
		case req.Method == http.MethodGet && req.URL.Path == "/v1/predictions/pred-7":
			return jsonResp(`{"id":"pred-7","status":"succeeded","output":"https://replicate.delivery/up.png"}`), nil
		case req.Method == http.MethodGet && req.URL.Path == "/up.png":
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
	})
	app := &App{client: &http.Client{Transport: transport}}
	config := defaultAppConfig()
	config.Storage.Root = t.TempDir()
	config.Models.ImageProvider = "replicate"
	config.Providers.Replicate.UpscaleModel = "nightmareai/real-esrgan"
	gateway := newToolGateway(app, config)
	resp, notices, err := gateway.tools.UpscaleImage(context.Background(), ImageUpscaleRequest{
		Model: "nightmareai/real-esrgan",
		Image: "data:image/png;base64," + tinyPNG,
		Scale: 2,
	})
	if err != nil {
		t.Fatalf("UpscaleImage: %v (notices %v)", err, notices)
	}
	if len(resp.Images) != 1 {
		t.Fatalf("Images = %v", resp.Images)
	}
	if resp.CostMicros != 0 {
		t.Fatalf("CostMicros = %d, want 0 (unknown on replicate)", resp.CostMicros)
	}
	if !imageUpscaleConfigured(config) {
		t.Fatal("imageUpscaleConfigured should hold on the replicate path with a token")
	}
	if got := resolveDefaultImageUpscaleModel(config); got != "nightmareai/real-esrgan" {
		t.Fatalf("resolveDefaultImageUpscaleModel = %q", got)
	}
}

// TestReplicateGatewayUpscaleVideoRouting drives the gateway's UpscaleVideo
// replicate branch: schema-driven input, the source swap for a hosted URL on
// an oversized clip, and the shared GenerateVideo transport.
func TestReplicateGatewayUpscaleVideoRouting(t *testing.T) {
	keyring.MockInit()
	t.Cleanup(func() { _ = clearReplicateAPIKey() })
	if err := saveReplicateAPIKey("replicate-test-key"); err != nil {
		t.Fatalf("saveReplicateAPIKey: %v", err)
	}

	schemaJSON := `{"components":{"schemas":{"Input":{"type":"object","properties":{
		"video":{"type":"string"},
		"scale":{"type":"number"}
	}}}}}`
	predictions := replicatePredictionHandler(t,
		nil,
		`"https://replicate.delivery/out/up.mp4"`,
		"/out/up.mp4", tinyReplicateMP4, "video/mp4")
	var sawHosted bool
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch {
		case req.Method == http.MethodGet && req.URL.Path == "/v1/models/owner/vup":
			return jsonResp(`{"latest_version":{"openapi_schema":` + schemaJSON + `}}`), nil
		case req.Method == http.MethodPost && req.URL.Path == "/v1/files":
			sawHosted = true
			return jsonResp(`{"urls":{"get":"https://replicate.delivery/files/src"}}`), nil
		case req.Method == http.MethodPost && req.URL.Path == "/v1/models/owner/vup/predictions":
			var body struct {
				Input map[string]any `json:"input"`
			}
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				t.Fatalf("decode prediction body: %v", err)
			}
			if got, ok := body.Input["video"].(string); !ok || !strings.HasPrefix(got, "https://replicate.delivery/files/src") {
				t.Fatalf("input.video = %v, want the hosted upload URL", body.Input["video"])
			}
			if body.Input["scale"] != float64(2) && body.Input["scale"] != 2 {
				t.Fatalf("input.scale = %v (%T)", body.Input["scale"], body.Input["scale"])
			}
			return jsonResp(`{"id":"pred-1","status":"starting"}`), nil
		default:
			return predictions.RoundTrip(req)
		}
	})
	app := &App{client: &http.Client{Transport: transport}}
	config := defaultAppConfig()
	config.Storage.Root = t.TempDir()
	config.Models.VideoProvider = "replicate"
	config.Providers.Replicate.VideoUpscaleModel = "owner/vup"
	gateway := newToolGateway(app, config)

	// An oversized inline clip forces the Files-API upload path.
	bigClip := make([]byte, replicateInlineMediaMaxBytes+64)
	for i := range bigClip {
		bigClip[i] = 0x61
	}
	generated, err := gateway.tools.UpscaleVideo(context.Background(), VideoUpscaleRequest{
		Model: "owner/vup",
		Video: "data:video/mp4;base64," + base64.StdEncoding.EncodeToString(bigClip),
		Scale: 2,
	})
	if err != nil {
		t.Fatalf("UpscaleVideo: %v", err)
	}
	if len(generated.Data) == 0 {
		t.Fatal("no video bytes returned")
	}
	if generated.CostMicros != 0 {
		t.Fatalf("CostMicros = %d, want 0 (unknown on replicate)", generated.CostMicros)
	}
	if !sawHosted {
		t.Fatal("the oversized source should have uploaded through the Files API")
	}
	if !videoUpscaleConfigured(config) {
		t.Fatal("videoUpscaleConfigured should hold on the replicate path with a token")
	}
	if got := resolveDefaultVideoUpscaleModel(config); got != "owner/vup" {
		t.Fatalf("resolveDefaultVideoUpscaleModel = %q", got)
	}

	// The tool activity command attributes to the routed provider.
	definition := videoUpscaleToolDefinition(config)
	activity := definition.Activity(HarnessToolResult{Status: "completed", Result: ToolVideoResult{Model: "owner/vup", Count: 1}})
	if len(activity.Command) != 3 || activity.Command[0] != "replicate" {
		t.Fatalf("Command = %v, want [replicate upscale <model>]", activity.Command)
	}
}

// TestReplicateGatewayRestyleReframeRouting drives both transform branches on
// the replicate path end to end: schema-driven input (reference images, the
// mode default), the shared GenerateVideo transport, and the routing helpers.
func TestReplicateGatewayRestyleReframeRouting(t *testing.T) {
	keyring.MockInit()
	t.Cleanup(func() { _ = clearReplicateAPIKey() })
	if err := saveReplicateAPIKey("replicate-test-key"); err != nil {
		t.Fatalf("saveReplicateAPIKey: %v", err)
	}

	restyleSchema := `{"components":{"schemas":{"Input":{"type":"object","properties":{
		"prompt":{"type":"string"},
		"video":{"type":"string"},
		"image_urls":{"type":"array","items":{"type":"string"}}
	}}}}}`
	reframeSchema := `{"components":{"schemas":{"Input":{"type":"object","properties":{
		"video":{"type":"string"},
		"aspect_ratio":{"type":"string","enum":["16:9","9:16"]}
	}}}}}`
	predictions := replicatePredictionHandler(t,
		nil,
		`"https://replicate.delivery/out/edit.mp4"`,
		"/out/edit.mp4", tinyReplicateMP4, "video/mp4")
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method == http.MethodGet && req.URL.Path == "/v1/models/kwaivgi/kling-v3-omni-video" {
			return jsonResp(`{"latest_version":{"openapi_schema":` + restyleSchema + `}}`), nil
		}
		if req.Method == http.MethodGet && req.URL.Path == "/v1/models/luma/reframe-video" {
			return jsonResp(`{"latest_version":{"openapi_schema":` + reframeSchema + `}}`), nil
		}
		if req.Method == http.MethodPost && (strings.HasSuffix(req.URL.Path, "/predictions")) {
			var body struct {
				Input map[string]any `json:"input"`
			}
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				t.Fatalf("decode prediction body: %v", err)
			}
			if strings.Contains(req.URL.Path, "kling") {
				refs, ok := body.Input["image_urls"].([]any)
				if !ok || len(refs) != 1 || !strings.HasPrefix(refs[0].(string), "data:image/png;base64,") {
					t.Fatalf("image_urls = %v, want the reference forwarded", body.Input["image_urls"])
				}
			}
			if strings.Contains(req.URL.Path, "reframe") && body.Input["aspect_ratio"] != "9:16" {
				t.Fatalf("aspect_ratio = %v", body.Input["aspect_ratio"])
			}
			return jsonResp(`{"id":"pred-1","status":"starting"}`), nil
		}
		return predictions.RoundTrip(req)
	})
	app := &App{client: &http.Client{Transport: transport}}
	config := defaultAppConfig()
	config.Storage.Root = t.TempDir()
	config.Models.VideoProvider = "replicate"
	config.Providers.Replicate.VideoRestyleModel = "kwaivgi/kling-v3-omni-video"
	config.Providers.Replicate.VideoReframeModel = "luma/reframe-video"
	gateway := newToolGateway(app, config)

	restyled, err := gateway.tools.RestyleVideo(context.Background(), VideoRestyleRequest{
		Model:  "kwaivgi/kling-v3-omni-video",
		Video:  "data:video/mp4;base64,QUFB",
		Prompt: "anime style",
		Images: []string{"data:image/png;base64," + tinyPNG},
	})
	if err != nil {
		t.Fatalf("RestyleVideo: %v", err)
	}
	if len(restyled.Data) == 0 || restyled.CostMicros != 0 {
		t.Fatalf("restyled = %d bytes, cost %d", len(restyled.Data), restyled.CostMicros)
	}

	reframed, err := gateway.tools.ReframeVideo(context.Background(), VideoReframeRequest{
		Model:       "luma/reframe-video",
		Video:       "data:video/mp4;base64,QUFB",
		AspectRatio: "9:16",
	})
	if err != nil {
		t.Fatalf("ReframeVideo: %v", err)
	}
	if len(reframed.Data) == 0 {
		t.Fatal("no video bytes returned")
	}

	if !videoRestyleConfigured(config) || !videoReframeConfigured(config) {
		t.Fatal("both transform gates should hold on the replicate path with a token")
	}
	if got := resolveDefaultVideoRestyleModel(config); got != "kwaivgi/kling-v3-omni-video" {
		t.Fatalf("resolveDefaultVideoRestyleModel = %q", got)
	}
	if got := resolveDefaultVideoReframeModel(config); got != "luma/reframe-video" {
		t.Fatalf("resolveDefaultVideoReframeModel = %q", got)
	}
	// Both activity commands attribute to the routed provider.
	for _, pair := range []struct {
		def  HarnessToolDefinition
		verb string
	}{
		{videoRestyleToolDefinition(config), "restyle"},
		{videoReframeToolDefinition(config), "reframe"},
	} {
		activity := pair.def.Activity(HarnessToolResult{Status: "completed", Result: ToolVideoResult{Model: "owner/m", Count: 1}})
		if len(activity.Command) != 3 || activity.Command[0] != "replicate" || activity.Command[1] != pair.verb {
			t.Fatalf("Command = %v, want [replicate %s <model>]", activity.Command, pair.verb)
		}
	}
}
