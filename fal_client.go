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
	"time"
)

const (
	falQueueBaseURL      = "https://queue.fal.run"
	falPlatformBaseURL   = "https://api.fal.ai"
	defaultFalImageModel = "fal-ai/flux/schnell"
	// falPollInterval is the delay between queue status checks.
	falPollInterval = 1500 * time.Millisecond
)

// FalClient talks to fal.ai's asynchronous queue API for image generation.
// It is deliberately not a ChatProvider — fal.ai only generates images here.
// The client mirrors the OpenRouter client shape (value type, injected HTTP
// client, key read from the OS keyring at construction time).
type FalClient struct {
	httpClient *http.Client
	apiKey     string
}

func newFalClient(httpClient *http.Client, apiKey string) FalClient {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return FalClient{httpClient: httpClient, apiKey: strings.TrimSpace(apiKey)}
}

// falImage is a single image entry in a fal result payload.
type falImage struct {
	URL    string `json:"url"`
	Width  int    `json:"width,omitempty"`
	Height int    `json:"height,omitempty"`
}

// falSubmitResponse is returned by POST {base}/{model}.
type falSubmitResponse struct {
	RequestID   string `json:"request_id"`
	StatusURL   string `json:"status_url"`
	ResponseURL string `json:"response_url"`
}

// falStatusResponse is returned by GET {base}/{model}/requests/{id}/status.
type falStatusResponse struct {
	Status string `json:"status"` // IN_QUEUE | IN_PROGRESS | COMPLETED | FAILED
	Error  string `json:"error,omitempty"`
	Logs   []struct {
		Message string `json:"message"`
		Level   string `json:"level,omitempty"`
	} `json:"logs,omitempty"`
}

// falResultResponse is returned by GET {base}/{model}/requests/{id}.
type falResultResponse struct {
	Data   json.RawMessage `json:"data,omitempty"`
	Images []falImage      `json:"images,omitempty"`
	Error  string          `json:"error,omitempty"`
}

// GenerateImage submits a request to the fal queue, polls until completion, and
// returns a synthetic ollamaGenerateResponse whose Images field carries the
// generated images as base64 data URLs. The raw fal JSON is also returned so
// the tool's existing harvest logic (collectImagesFromJSON) can scavenge any
// image fields in unexpected locations.
func (client FalClient) GenerateImage(ctx context.Context, req ImageGenerateRequest) (ollamaGenerateResponse, []byte, error) {
	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = defaultFalImageModel
	}

	body := map[string]any{
		"prompt":     req.Prompt,
		"num_images": 1,
	}
	if req.Width > 0 && req.Height > 0 {
		body["image_size"] = map[string]any{"width": req.Width, "height": req.Height}
	}
	if req.Steps > 0 {
		body["num_inference_steps"] = req.Steps
	}

	submit, err := client.submit(ctx, model, body)
	if err != nil {
		return ollamaGenerateResponse{}, nil, err
	}
	requestID := strings.TrimSpace(submit.RequestID)
	if requestID == "" {
		return ollamaGenerateResponse{}, nil, errors.New("fal submit returned no request id")
	}

	if err := client.waitForCompletion(ctx, model, requestID); err != nil {
		return ollamaGenerateResponse{}, nil, err
	}

	result, _, err := client.fetchResult(ctx, model, requestID)
	if err != nil {
		return ollamaGenerateResponse{}, nil, err
	}

	dataURLs, err := client.downloadImages(ctx, result.Images)
	if err != nil {
		return ollamaGenerateResponse{}, nil, err
	}
	if len(dataURLs) == 0 {
		return ollamaGenerateResponse{}, nil, errors.New("fal result returned no images")
	}

	response := ollamaGenerateResponse{
		Model:  model,
		Image:  dataURLs[0],
		Images: dataURLs,
		Done:   true,
	}
	// Return nil raw: the fal client has already downloaded each result URL
	// into the base64 data URLs above. Passing the raw fal JSON up would let
	// the tool's collectImagesFromJSON backstop re-harvest the source URLs
	// (https://...) alongside the data URLs; those URLs then fail to decode at
	// artifact-write time and the whole turn save aborts with an orphaned file.
	return response, nil, nil
}

func (client FalClient) submit(ctx context.Context, model string, body map[string]any) (falSubmitResponse, error) {
	resp, err := client.do(ctx, falQueueBaseURL, http.MethodPost, "/"+model, body)
	if err != nil {
		return falSubmitResponse{}, err
	}
	defer resp.Body.Close()

	var payload falSubmitResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return falSubmitResponse{}, err
	}
	return payload, nil
}

func (client FalClient) waitForCompletion(ctx context.Context, model, requestID string) error {
	statusPath := "/" + model + "/requests/" + requestID + "/status"
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("fal image generation cancelled: %w", err)
		}

		resp, err := client.do(ctx, falQueueBaseURL, http.MethodGet, statusPath, nil)
		if err != nil {
			return err
		}
		var status falStatusResponse
		decodeErr := json.NewDecoder(resp.Body).Decode(&status)
		resp.Body.Close()
		if decodeErr != nil {
			return decodeErr
		}

		switch strings.ToUpper(strings.TrimSpace(status.Status)) {
		case "COMPLETED":
			return nil
		case "FAILED":
			msg := strings.TrimSpace(status.Error)
			if msg == "" && len(status.Logs) > 0 {
				msg = status.Logs[len(status.Logs)-1].Message
			}
			if msg == "" {
				msg = "fal reported a failed generation"
			}
			return errors.New(msg)
		case "":
			return errors.New("fal status response was missing a status field")
		case "IN_QUEUE", "IN_PROGRESS":
			// keep polling
		default:
			// Unknown but non-empty status: treat as still in progress.
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("fal image generation cancelled: %w", ctx.Err())
		case <-time.After(falPollInterval):
		}
	}
}

func (client FalClient) fetchResult(ctx context.Context, model, requestID string) (falResultResponse, []byte, error) {
	resultPath := "/" + model + "/requests/" + requestID
	resp, err := client.do(ctx, falQueueBaseURL, http.MethodGet, resultPath, nil)
	if err != nil {
		return falResultResponse{}, nil, err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32*1024*1024))
	if err != nil {
		return falResultResponse{}, nil, err
	}
	var payload falResultResponse
	if err := json.Unmarshal(raw, &payload); err != nil {
		return falResultResponse{}, raw, err
	}
	if strings.TrimSpace(payload.Error) != "" {
		return falResultResponse{}, raw, errors.New(payload.Error)
	}
	// fal nests image arrays under data for some models; fall back to scanning
	// the raw JSON for any url fields if the top-level Images list is empty.
	if len(payload.Images) == 0 {
		payload.Images = collectFalImagesFromJSON(raw)
	}
	return payload, raw, nil
}

// downloadImages fetches each image URL and re-encodes it as a data URL so the
// rest of the pipeline (which assumes base64) works unchanged. If a URL cannot
// be fetched the error is returned unless at least one image succeeded.
func (client FalClient) downloadImages(ctx context.Context, images []falImage) ([]string, error) {
	dataURLs := make([]string, 0, len(images))
	var lastErr error
	for _, image := range images {
		url := strings.TrimSpace(image.URL)
		if url == "" {
			continue
		}
		dataURL, err := client.fetchAsDataURL(ctx, url)
		if err != nil {
			lastErr = err
			continue
		}
		dataURLs = append(dataURLs, dataURL)
	}
	if len(dataURLs) == 0 && lastErr != nil {
		return nil, lastErr
	}
	return dataURLs, nil
}

func (client FalClient) fetchAsDataURL(ctx context.Context, url string) (string, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.httpClient.Do(httpReq)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("image download failed: %s", resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 32*1024*1024))
	if err != nil {
		return "", err
	}
	if !isImageBytes(data) {
		return "", errors.New("downloaded fal image is not a supported image")
	}
	mediaType := http.DetectContentType(data)
	return "data:" + mediaType + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}

// collectFalImagesFromJSON walks the raw fal result and pulls out any string
// value that looks like an image URL (an http(s) URL ending in an image
// extension or referenced under an "images"/"image" key). Used as a fallback
// when the top-level Images list is empty.
func collectFalImagesFromJSON(raw []byte) []falImage {
	var payload any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil
	}
	var images []falImage
	walkJSONStrings(payload, func(value string) {
		trimmed := strings.TrimSpace(value)
		if !strings.HasPrefix(trimmed, "http://") && !strings.HasPrefix(trimmed, "https://") {
			return
		}
		lower := strings.ToLower(trimmed)
		for _, ext := range []string{".png", ".jpg", ".jpeg", ".webp", ".gif"} {
			if strings.Contains(lower, ext) {
				images = append(images, falImage{URL: trimmed})
				return
			}
		}
	})
	return images
}

// VerifyKey confirms the API key is accepted by fal without starting a
// generation. It pings the platform API's model-search endpoint — a cheap
// authenticated GET that returns 200 for a valid key and 401 for a bad one.
// Used by the Settings "Check Connection" button so a bad key fails fast and
// cheaply rather than mid-generation.
func (client FalClient) VerifyKey(ctx context.Context) error {
	resp, err := client.do(ctx, falPlatformBaseURL, http.MethodGet, "/v1/models", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}

func (client FalClient) do(ctx context.Context, baseURL, method, path string, body map[string]any) (*http.Response, error) {
	if strings.TrimSpace(client.apiKey) == "" {
		return nil, errors.New("fal api key is not configured")
	}

	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(data)
	}
	httpReq, err := http.NewRequestWithContext(ctx, method, baseURL+path, reader)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Authorization", "Key "+client.apiKey)
	if body != nil {
		httpReq.Header.Set("Content-Type", "application/json")
	}

	resp, err := client.httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		message, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		trimmed := strings.TrimSpace(string(message))
		switch resp.StatusCode {
		case http.StatusUnauthorized:
			return nil, fmt.Errorf("fal authentication failed: %s", trimmed)
		case http.StatusTooManyRequests:
			return nil, fmt.Errorf("fal rate limited: %s", trimmed)
		default:
			return nil, fmt.Errorf("fal returned %s: %s", resp.Status, trimmed)
		}
	}
	return resp, nil
}
