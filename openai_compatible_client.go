package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// openAICompatibleClient talks to a local server that speaks OpenAI's
// /v1/images/generations shape (LocalAI, a diffusers shim, ...). Like
// FalClient it is an image-only backend and deliberately not a ChatProvider.
// The apiKey is optional: most local servers run without auth, and an empty
// key omits the Authorization header entirely.
type openAICompatibleClient struct {
	httpClient *http.Client
	baseURL    string
	apiKey     string
}

func newOpenAICompatibleClient(httpClient *http.Client, baseURL, apiKey string) openAICompatibleClient {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return openAICompatibleClient{
		httpClient: httpClient,
		baseURL:    strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		apiKey:     strings.TrimSpace(apiKey),
	}
}

func (client openAICompatibleClient) endpoint(path string) string {
	return client.baseURL + path
}

// applyAuth sets the optional bearer header when a key is configured.
func (client openAICompatibleClient) applyAuth(req *http.Request) {
	if client.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+client.apiKey)
	}
}

// openAIImageResponse is the OpenAI images API result shape. Each entry
// carries either inline base64 bytes (b64_json, what we request via
// response_format) or a downloadable url.
type openAIImageResponse struct {
	Data []struct {
		URL     string `json:"url,omitempty"`
		B64JSON string `json:"b64_json,omitempty"`
	} `json:"data"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// openAIModelListResponse is the GET /v1/models shape shared by every
// OpenAI-compatible server.
type openAIModelListResponse struct {
	Data []struct {
		ID string `json:"id"`
	} `json:"data"`
}

// GenerateImage posts to /v1/images/generations and packs the results into
// the shared ollamaGenerateResponse shape as base64 data URLs. The raw JSON is
// returned nil on purpose — the same contract FalClient.GenerateImage uses —
// so collectImagesFromJSON cannot re-harvest source URLs from the payload.
func (client openAICompatibleClient) GenerateImage(ctx context.Context, req ImageGenerateRequest) (ollamaGenerateResponse, []byte, error) {
	body := map[string]any{
		"model":           req.Model,
		"prompt":          req.Prompt,
		"n":               1,
		"response_format": "b64_json",
	}
	if req.Width > 0 && req.Height > 0 {
		body["size"] = fmt.Sprintf("%dx%d", req.Width, req.Height)
	}
	if req.Steps > 0 {
		body["steps"] = req.Steps
	}
	// Attached source images ride the request ollama-style: a non-standard
	// extension img2img-capable servers understand and the rest ignore.
	if len(req.Images) > 0 {
		body["images"] = req.Images
	}

	data, err := json.Marshal(body)
	if err != nil {
		return ollamaGenerateResponse{}, nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, client.endpoint("/v1/images/generations"), bytes.NewReader(data))
	if err != nil {
		return ollamaGenerateResponse{}, nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	client.applyAuth(httpReq)

	resp, err := client.httpClient.Do(httpReq)
	if err != nil {
		return ollamaGenerateResponse{}, nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024*1024))
	if err != nil {
		return ollamaGenerateResponse{}, nil, err
	}
	if resp.StatusCode >= 400 {
		return ollamaGenerateResponse{}, nil, fmt.Errorf("openai-compatible image server returned %s: %s", resp.Status, strings.TrimSpace(string(raw)))
	}

	var payload openAIImageResponse
	if err := json.Unmarshal(raw, &payload); err != nil {
		return ollamaGenerateResponse{}, nil, err
	}
	if payload.Error != nil && strings.TrimSpace(payload.Error.Message) != "" {
		return ollamaGenerateResponse{}, nil, errors.New(payload.Error.Message)
	}

	dataURLs := make([]string, 0, len(payload.Data))
	for _, entry := range payload.Data {
		if b64 := strings.TrimSpace(entry.B64JSON); b64 != "" {
			dataURL, err := imageBase64ToDataURL(b64)
			if err != nil {
				return ollamaGenerateResponse{}, nil, err
			}
			dataURLs = append(dataURLs, dataURL)
		}
		if url := strings.TrimSpace(entry.URL); url != "" {
			dataURL, err := fetchImageAsDataURL(ctx, client.httpClient, url)
			if err != nil {
				return ollamaGenerateResponse{}, nil, err
			}
			dataURLs = append(dataURLs, dataURL)
		}
	}
	if len(dataURLs) == 0 {
		return ollamaGenerateResponse{}, nil, errors.New("openai-compatible image server returned no images")
	}
	return ollamaGenerateResponse{
		Model:  req.Model,
		Image:  dataURLs[0],
		Images: dataURLs,
		Done:   true,
	}, nil, nil
}

// ListModels returns the model ids advertised by GET /v1/models, preserving
// server order. Used by the Settings picker as a discovery aid; servers that
// do not implement the route surface the error to the UI hint.
func (client openAICompatibleClient) ListModels(ctx context.Context) ([]string, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, client.endpoint("/v1/models"), nil)
	if err != nil {
		return nil, err
	}
	client.applyAuth(httpReq)
	resp, err := client.httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("openai-compatible server returned %s: %s", resp.Status, strings.TrimSpace(string(raw)))
	}
	var payload openAIModelListResponse
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(payload.Data))
	for _, entry := range payload.Data {
		if id := strings.TrimSpace(entry.ID); id != "" {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

// imageBase64ToDataURL validates bare base64 image bytes and re-encodes them
// as a data URL, the canonical in-memory image shape the tool layer expects.
func imageBase64ToDataURL(b64 string) (string, error) {
	data, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return "", fmt.Errorf("invalid base64 image from openai-compatible server: %w", err)
	}
	if !isImageBytes(data) {
		return "", errors.New("openai-compatible server returned base64 that is not a supported image")
	}
	mediaType := http.DetectContentType(data)
	return "data:" + mediaType + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}
