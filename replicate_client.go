package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"
)

// ReplicateClient talks to Replicate's REST API (api.replicate.com) for image
// and video generation. It is deliberately not a ChatProvider — Replicate only
// generates media here — and it mirrors the FalClient shape (value type,
// injected HTTP client, token read from the OS keychain at construction time).
// The transport is Replicate's prediction lifecycle: POST a prediction, poll
// GET /v1/predictions/{id} until a terminal status, then scavenge the
// model-defined output for media URLs.
const (
	replicateAPIBaseURL = "https://api.replicate.com"
	// defaultReplicateImageModel is the text-to-image model used when none is
	// configured — the fastest Flux tier, priced per output image.
	defaultReplicateImageModel = "black-forest-labs/flux-schnell"
	// defaultReplicateImageEditModel is the image-to-image model used when the
	// user attaches a source image to transform — the edit sibling of
	// defaultReplicateImageModel (instruction-based editing with the source
	// frame attached).
	defaultReplicateImageEditModel = "black-forest-labs/flux-kontext-pro"
	// defaultReplicateVideoModel is the text-to-video model used when none is
	// configured; defaultReplicateVideoImageModel is the image-to-video sibling
	// used to animate an attached image (the first frame).
	defaultReplicateVideoModel      = "wan-video/wan-2.5-t2v"
	defaultReplicateVideoImageModel = "wan-video/wan-2.5-i2v"
	// defaultReplicateUpscaleModel is the image upscaler used when none is
	// configured — Real-ESRGAN, the collection's most-run official upscaler
	// (image + scale + optional face enhancement), the same lineage as fal's
	// default esrgan endpoint.
	defaultReplicateUpscaleModel = "nightmareai/real-esrgan"
	// replicatePollInterval is the delay between prediction status checks,
	// matching fal's cadence.
	replicatePollInterval = 1500 * time.Millisecond
	// replicateVideoMaxBytes caps a downloaded video, matching fal's bound.
	replicateVideoMaxBytes = 256 * 1024 * 1024
	// replicateInlineMediaMaxBytes is the size above which embedded media
	// (base64 data URIs) is uploaded through Replicate's Files API before
	// submission. Replicate recommends data URIs only under ~1MB, so the
	// threshold matches fal's inline limit; larger media gets a hosted URL.
	replicateInlineMediaMaxBytes = 1024 * 1024
	// Replicate collection slugs — the curated buckets the Settings pickers
	// list, mirroring fal's /v1/models categories.
	replicateTextToImageCollection     = "text-to-image"
	replicateImageEditingCollection    = "image-editing"
	replicateTextToVideoCollection     = "text-to-video"
	replicateImageToVideoCollection    = "image-to-video"
	replicateSuperResolutionCollection = "super-resolution"
	// maxReplicateTransientRetries is the number of times do() re-issues a
	// request after a transient 5xx, matching the fal client's posture.
	maxReplicateTransientRetries = 1
)

type ReplicateClient struct {
	httpClient *http.Client
	apiKey     string
}

func newReplicateClient(httpClient *http.Client, apiKey string) ReplicateClient {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return ReplicateClient{httpClient: httpClient, apiKey: strings.TrimSpace(apiKey)}
}

// replicatePrediction mirrors one prediction object. Output is left raw
// because its shape is model-defined (a URL string, an array of URLs, an
// object, ...) and is scavenged per media kind.
type replicatePrediction struct {
	ID     string          `json:"id"`
	Status string          `json:"status"` // starting | processing | succeeded | failed | canceled
	Output json.RawMessage `json:"output"`
	Error  json.RawMessage `json:"error"`
	Logs   string          `json:"logs"`
}

// GenerateImage submits an already-native input object (built by
// resolveReplicateImageInput against the model's schema), polls until the
// prediction succeeds, and downloads the output images as base64 data URLs
// packed into an ollamaGenerateResponse — the same shape FalClient.
// GenerateImage produces, so generate_image's result normalization is reused.
// Output URLs expire an hour after the prediction completes, so bytes are
// always downloaded inside this call, never lazily.
func (client ReplicateClient) GenerateImage(ctx context.Context, model string, input map[string]any) (ollamaGenerateResponse, error) {
	model = strings.TrimSpace(model)
	if model == "" {
		model = defaultReplicateImageModel
	}
	if input == nil {
		input = map[string]any{}
	}
	prediction, err := client.runPrediction(ctx, model, input)
	if err != nil {
		return ollamaGenerateResponse{}, err
	}
	dataURLs, err := client.downloadImages(ctx, replicateImageURLCandidates(prediction.Output))
	if err != nil {
		return ollamaGenerateResponse{}, err
	}
	if len(dataURLs) == 0 {
		return ollamaGenerateResponse{}, errors.New("replicate prediction returned no images")
	}
	return ollamaGenerateResponse{
		Model:  model,
		Image:  dataURLs[0],
		Images: dataURLs,
		Done:   true,
	}, nil
}

// GenerateVideo submits an already-native input object (built by
// resolveReplicateVideoInput), polls until the prediction succeeds, and
// downloads the output clip as raw bytes — the shared GeneratedVideo shape,
// so every video consumer (artifacts, carry-forward, telemetry) is unchanged.
// Video predictions run for minutes; the caller must pass a context with a
// suitably long deadline. Output URLs expire an hour after the prediction
// completes, so bytes are always downloaded inside this call.
func (client ReplicateClient) GenerateVideo(ctx context.Context, model string, input map[string]any) (GeneratedVideo, error) {
	model = strings.TrimSpace(model)
	if model == "" {
		model = defaultReplicateVideoModel
	}
	if input == nil {
		input = map[string]any{}
	}
	prediction, err := client.runPrediction(ctx, model, input)
	if err != nil {
		return GeneratedVideo{}, err
	}
	videoURL := replicateVideoURL(prediction.Output)
	if videoURL == "" {
		return GeneratedVideo{}, errors.New("replicate prediction returned no video")
	}
	data, mimeType, err := client.downloadVideo(ctx, videoURL)
	if err != nil {
		return GeneratedVideo{}, err
	}
	return GeneratedVideo{Data: data, MimeType: mimeType, SourceURL: videoURL}, nil
}

// runPrediction creates a prediction for the model and blocks until it reaches
// a terminal status, returning the final prediction object. The create goes to
// the model-scoped route (POST /v1/models/{owner}/{name}/predictions), which
// targets the model's latest version — official models are pinned by their
// owners, so latest is the sanctioned target.
func (client ReplicateClient) runPrediction(ctx context.Context, model string, input map[string]any) (replicatePrediction, error) {
	prediction, err := client.createPrediction(ctx, model, input)
	if err != nil {
		return replicatePrediction{}, err
	}
	if strings.TrimSpace(prediction.ID) == "" {
		return replicatePrediction{}, errors.New("replicate prediction response returned no id")
	}
	if err := client.waitForPrediction(ctx, prediction.ID); err != nil {
		return replicatePrediction{}, err
	}
	return client.fetchPrediction(ctx, prediction.ID)
}

func (client ReplicateClient) createPrediction(ctx context.Context, model string, input map[string]any) (replicatePrediction, error) {
	owner, name, err := replicateModelPath(model)
	if err != nil {
		return replicatePrediction{}, err
	}
	body, err := json.Marshal(map[string]any{"input": input})
	if err != nil {
		return replicatePrediction{}, err
	}
	resp, err := client.do(ctx, http.MethodPost, "/v1/models/"+owner+"/"+name+"/predictions", body)
	if err != nil {
		return replicatePrediction{}, err
	}
	defer resp.Body.Close()
	var prediction replicatePrediction
	if err := json.NewDecoder(resp.Body).Decode(&prediction); err != nil {
		return replicatePrediction{}, err
	}
	return prediction, nil
}

func (client ReplicateClient) fetchPrediction(ctx context.Context, id string) (replicatePrediction, error) {
	resp, err := client.do(ctx, http.MethodGet, "/v1/predictions/"+strings.TrimSpace(id), nil)
	if err != nil {
		return replicatePrediction{}, err
	}
	defer resp.Body.Close()
	var prediction replicatePrediction
	if err := json.NewDecoder(resp.Body).Decode(&prediction); err != nil {
		return replicatePrediction{}, err
	}
	return prediction, nil
}

// waitForPrediction polls GET /v1/predictions/{id} until the prediction
// reaches a terminal status — succeeded, failed, or canceled — mirroring the
// fal client's waitForCompletion contract (ctx cancellation wins, an empty
// status is an error, unknown non-empty statuses are treated as in-flight).
func (client ReplicateClient) waitForPrediction(ctx context.Context, id string) error {
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("replicate generation cancelled: %w", err)
		}
		prediction, err := client.fetchPrediction(ctx, id)
		if err != nil {
			return err
		}
		switch strings.ToLower(strings.TrimSpace(prediction.Status)) {
		case "succeeded":
			return nil
		case "failed":
			return errors.New(replicateFailureMessage(prediction))
		case "canceled":
			return errors.New("replicate prediction was canceled")
		case "":
			return errors.New("replicate prediction response was missing a status field")
		default:
			// starting / processing / anything unknown: keep polling.
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("replicate generation cancelled: %w", ctx.Err())
		case <-time.After(replicatePollInterval):
		}
	}
}

// replicateFailureMessage renders a failed prediction's error field, which may
// be a plain string or an object, falling back to the log tail.
func replicateFailureMessage(prediction replicatePrediction) string {
	if msg := replicateRawMessageText(prediction.Error); msg != "" {
		return msg
	}
	if logs := strings.TrimSpace(prediction.Logs); logs != "" {
		lines := strings.Split(strings.TrimRight(logs, "\n"), "\n")
		return lines[len(lines)-1]
	}
	return "replicate reported a failed prediction"
}

// replicateRawMessageText stringifies a raw JSON field that may be a string or
// an object with a message/detail/error member.
func replicateRawMessageText(raw json.RawMessage) string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return ""
	}
	var s string
	if err := json.Unmarshal(trimmed, &s); err == nil {
		return strings.TrimSpace(s)
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &obj); err == nil {
		for _, key := range []string{"message", "detail", "error"} {
			if text := replicateRawMessageText(obj[key]); text != "" {
				return text
			}
		}
	}
	return strings.TrimSpace(string(trimmed))
}

// replicateModelPath splits an owner/name slug into escaped path segments.
// Replicate model ids are always two segments; a malformed id fails here
// rather than as a confusing 404 from the API.
func replicateModelPath(model string) (owner, name string, err error) {
	parts := strings.SplitN(strings.TrimSpace(model), "/", 2)
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return "", "", fmt.Errorf("invalid replicate model id %q: expected owner/name", model)
	}
	return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), nil
}

// replicateImageURLCandidates collects output URLs that may hold images, in
// priority order: direct shapes first (a URL string, an array of strings or
// {url} objects — the common image-model output), then any http(s) URL whose
// path ends in an image extension, then every http(s) URL (the download step
// validates content and drops non-images). Replicate delivery URLs usually
// carry the output's extension, but not always; the layered walk keeps a
// well-shaped model working either way.
func replicateImageURLCandidates(raw json.RawMessage) []string {
	var urls []string
	seen := map[string]bool{}
	add := func(value string) {
		value = strings.TrimSpace(value)
		if value != "" && !seen[value] {
			seen[value] = true
			urls = append(urls, value)
		}
	}
	collectHTTPURLs := func(extensionFilter []string) {
		var payload any
		if err := json.Unmarshal(raw, &payload); err != nil {
			return
		}
		walkJSONStrings(payload, func(value string) {
			trimmed := strings.TrimSpace(value)
			if !strings.HasPrefix(trimmed, "http://") && !strings.HasPrefix(trimmed, "https://") {
				return
			}
			if len(extensionFilter) > 0 {
				lower := strings.ToLower(trimmed)
				for _, ext := range extensionFilter {
					if strings.Contains(lower, ext) {
						add(trimmed)
						return
					}
				}
				return
			}
			add(trimmed)
		})
	}

	// Direct shapes.
	var single string
	if err := json.Unmarshal(raw, &single); err == nil && strings.HasPrefix(single, "http") {
		add(single)
	}
	var list []any
	if err := json.Unmarshal(raw, &list); err == nil {
		for _, entry := range list {
			switch typed := entry.(type) {
			case string:
				if strings.HasPrefix(typed, "http") {
					add(typed)
				}
			case map[string]any:
				if url, ok := typed["url"].(string); ok {
					add(url)
				}
			}
		}
	}
	// Extension-anchored walk, then the any-URL fallback.
	collectHTTPURLs([]string{".png", ".jpg", ".jpeg", ".webp", ".gif"})
	collectHTTPURLs(nil)
	return urls
}

// replicateVideoURL finds the clip URL in a video prediction's output. Video
// models return a single URL string (or a one-element array); the fallback
// walk matches http(s) URLs with video extensions the way the fal client's
// firstFalVideoURL does.
func replicateVideoURL(raw json.RawMessage) string {
	var single string
	if err := json.Unmarshal(raw, &single); err == nil {
		if strings.HasPrefix(single, "http") {
			return strings.TrimSpace(single)
		}
	}
	var list []any
	if err := json.Unmarshal(raw, &list); err == nil {
		for _, entry := range list {
			if s, ok := entry.(string); ok && strings.HasPrefix(s, "http") {
				return strings.TrimSpace(s)
			}
		}
	}
	var payload any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return ""
	}
	found := ""
	walkJSONStrings(payload, func(value string) {
		if found != "" {
			return
		}
		trimmed := strings.TrimSpace(value)
		if !strings.HasPrefix(trimmed, "http://") && !strings.HasPrefix(trimmed, "https://") {
			return
		}
		lower := strings.ToLower(trimmed)
		for _, ext := range []string{".mp4", ".webm", ".mov", ".m4v"} {
			if strings.Contains(lower, ext) {
				found = trimmed
				return
			}
		}
	})
	return found
}

// downloadImages fetches each candidate URL through the shared
// fetchImageAsDataURL (which rejects non-image bytes), keeping the ones that
// decode. Ordering is preserved, so the first successfully downloaded image is
// the output's own first entry.
func (client ReplicateClient) downloadImages(ctx context.Context, urls []string) ([]string, error) {
	dataURLs := make([]string, 0, len(urls))
	var lastErr error
	for _, url := range urls {
		dataURL, err := fetchImageAsDataURL(ctx, client.httpClient, url)
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

// downloadVideo fetches a generated video URL and returns its raw bytes and
// MIME type — the fal client's downloadVideo contract, shared here because
// video bytes are handed back for the caller to write straight to a
// file-path artifact.
func (client ReplicateClient) downloadVideo(ctx context.Context, videoURL string) ([]byte, string, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, videoURL, nil)
	if err != nil {
		return nil, "", err
	}
	resp, err := client.httpClient.Do(httpReq)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, "", fmt.Errorf("video download failed: %s", resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, replicateVideoMaxBytes))
	if err != nil {
		return nil, "", err
	}
	if !isVideoBytes(data) {
		return nil, "", errors.New("downloaded replicate result is not a supported video")
	}
	mimeType := strings.TrimSpace(resp.Header.Get("Content-Type"))
	if mimeType == "" || !strings.HasPrefix(mimeType, "video/") {
		mimeType = http.DetectContentType(data)
	}
	if !strings.HasPrefix(mimeType, "video/") {
		mimeType = "video/mp4"
	}
	return data, mimeType, nil
}

// UpscaleImage submits an attached image to a Replicate upscaler model
// (input built by resolveReplicateUpscaleInput against the model's schema),
// polls until the prediction succeeds, and returns the upscaled image as a
// base64 data URL packed into a synthetic ollamaGenerateResponse — the same
// shape FalClient.UpscaleImage produces, so the tool's result normalization
// is reused. Routed to when the image provider is replicate
// (imageGenerationProvider); fal remains the backend otherwise.
func (client ReplicateClient) UpscaleImage(ctx context.Context, model string, input map[string]any) (ollamaGenerateResponse, error) {
	model = strings.TrimSpace(model)
	if model == "" {
		model = defaultReplicateUpscaleModel
	}
	if input == nil {
		input = map[string]any{}
	}
	prediction, err := client.runPrediction(ctx, model, input)
	if err != nil {
		return ollamaGenerateResponse{}, err
	}
	dataURLs, err := client.downloadImages(ctx, replicateImageURLCandidates(prediction.Output))
	if err != nil {
		return ollamaGenerateResponse{}, err
	}
	if len(dataURLs) == 0 {
		return ollamaGenerateResponse{}, errors.New("replicate prediction returned no images")
	}
	return ollamaGenerateResponse{
		Model:  model,
		Image:  dataURLs[0],
		Images: dataURLs,
		Done:   true,
	}, nil
}

// ResolveMediaURL normalizes a media reference for Replicate, uploading it
// through the Files API when the decoded payload exceeds the inline limit —
// the fal resolveMediaURL pattern. Small payloads stay inline as data URIs
// (Replicate accepts them under ~1MB); large ones — typically attached source
// images and clips — get a hosted URL. On any upload failure the original
// data URI is returned so the request still goes through and Replicate's own
// error surfaces. defaultMediaType is the fallback when the payload's MIME
// can't be detected.
func (client ReplicateClient) ResolveMediaURL(ctx context.Context, reference, defaultMediaType, filename string) (string, error) {
	reference = strings.TrimSpace(reference)
	if reference == "" {
		return "", nil
	}
	if strings.HasPrefix(reference, "http://") || strings.HasPrefix(reference, "https://") {
		return reference, nil
	}
	var data []byte
	var mediaType string
	if strings.HasPrefix(reference, "data:") {
		comma := strings.Index(reference, ",")
		if comma < 0 {
			return reference, nil // malformed data URI — let Replicate reject it
		}
		header := reference[len("data:"):comma]
		mediaType = header
		if semicolon := strings.Index(header, ";"); semicolon >= 0 {
			mediaType = header[:semicolon]
		}
		if mediaType == "" || mediaType == "base64" {
			mediaType = defaultMediaType
		}
		decoded, err := base64.StdEncoding.DecodeString(reference[comma+1:])
		if err != nil {
			return reference, nil // undecodable — pass through, let Replicate reject
		}
		data = decoded
	} else {
		decoded, err := base64.StdEncoding.DecodeString(reference)
		if err != nil {
			return reference, nil
		}
		data = decoded
		mediaType = defaultMediaType
		if detected := http.DetectContentType(data); strings.HasPrefix(detected, strings.SplitN(defaultMediaType, "/", 2)[0]+"/") {
			mediaType = detected
		}
	}
	if len(data) <= replicateInlineMediaMaxBytes {
		return "data:" + mediaType + ";base64," + base64.StdEncoding.EncodeToString(data), nil
	}
	accessURL, err := client.UploadFile(ctx, data, filename, mediaType)
	if err != nil {
		return "data:" + mediaType + ";base64," + base64.StdEncoding.EncodeToString(data), nil
	}
	return accessURL, nil
}

// UploadFile uploads raw media bytes through Replicate's Files API
// (multipart POST /v1/files, "content" field) and returns the hosted access
// URL — the input form video/image models accept for large media. The URL is
// what gets passed as the model input value.
func (client ReplicateClient) UploadFile(ctx context.Context, data []byte, filename, mediaType string) (string, error) {
	if strings.TrimSpace(client.apiKey) == "" {
		return "", errReplicateKeyNotConfigured
	}
	if filename == "" {
		filename = "upload"
		if parts := strings.Split(mediaType, "/"); len(parts) == 2 {
			filename += "." + parts[1]
		}
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("content", filename)
	if err != nil {
		return "", err
	}
	if _, err := part.Write(data); err != nil {
		return "", err
	}
	if err := writer.Close(); err != nil {
		return "", err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, replicateAPIBaseURL+"/v1/files", &body)
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Authorization", "Bearer "+client.apiKey)
	httpReq.Header.Set("Content-Type", writer.FormDataContentType())
	resp, err := client.httpClient.Do(httpReq)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		message, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("replicate file upload returned %s: %s", resp.Status, strings.TrimSpace(string(message)))
	}
	var result struct {
		URLs struct {
			Get string `json:"get"`
		} `json:"urls"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	if strings.TrimSpace(result.URLs.Get) == "" {
		return "", errors.New("replicate file upload returned no access url")
	}
	return result.URLs.Get, nil
}

// ReplicateModel is one entry from a Replicate collection, flattened into the
// same field names FalModel uses (id, displayName, ...) so the frontend model
// pickers render both providers' catalogs identically.
type ReplicateModel struct {
	ID           string   `json:"id"`
	DisplayName  string   `json:"displayName"`
	Category     string   `json:"category"`
	Description  string   `json:"description"`
	Status       string   `json:"status"`
	Tags         []string `json:"tags"`
	ThumbnailURL string   `json:"thumbnailUrl"`
}

// replicateCollectionResponse mirrors GET /v1/collections/{slug}.
type replicateCollectionResponse struct {
	Name   string                     `json:"name"`
	Slug   string                     `json:"slug"`
	Models []replicateCollectionModel `json:"models"`
}

type replicateCollectionModel struct {
	Owner         string `json:"owner"`
	Name          string `json:"name"`
	Description   string `json:"description"`
	Visibility    string `json:"visibility"`
	IsOfficial    bool   `json:"is_official"`
	RunCount      int64  `json:"run_count"`
	CoverImageURL string `json:"cover_image_url"`
}

// ListCollectionModels returns the models of a Replicate collection
// (GET /v1/collections/{slug}), filtered to official models. Official models
// are per-output priced and always on — the curated list a Settings picker
// should offer — while community models bill GPU time, can go offline with
// their authors, and their input schemas drift. The fal listers' category
// walk is the sibling here; collections are pre-bucketed so no pagination is
// needed.
func (client ReplicateClient) ListCollectionModels(ctx context.Context, slug string) ([]ReplicateModel, error) {
	slug = strings.TrimSpace(slug)
	if slug == "" {
		return nil, errors.New("replicate collection slug is required")
	}
	resp, err := client.do(ctx, http.MethodGet, "/v1/collections/"+slug, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var collection replicateCollectionResponse
	if err := json.NewDecoder(resp.Body).Decode(&collection); err != nil {
		return nil, err
	}
	models := make([]ReplicateModel, 0, len(collection.Models))
	for _, entry := range collection.Models {
		owner := strings.TrimSpace(entry.Owner)
		name := strings.TrimSpace(entry.Name)
		if owner == "" || name == "" || !entry.IsOfficial {
			continue
		}
		models = append(models, ReplicateModel{
			ID:           owner + "/" + name,
			DisplayName:  name,
			Category:     slug,
			Description:  entry.Description,
			Status:       "official",
			Tags:         []string{owner},
			ThumbnailURL: entry.CoverImageURL,
		})
	}
	return models, nil
}

// GetModelInputSchema returns the raw OpenAPI JSON describing a model's input
// (latest_version.openapi_schema) — the same dialect fal's per-endpoint
// schemas speak, so parseModelInputSchema consumes either unchanged.
func (client ReplicateClient) GetModelInputSchema(ctx context.Context, model string) ([]byte, error) {
	owner, name, err := replicateModelPath(model)
	if err != nil {
		return nil, err
	}
	resp, err := client.do(ctx, http.MethodGet, "/v1/models/"+owner+"/"+name, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var payload struct {
		LatestVersion *struct {
			OpenAPISchema json.RawMessage `json:"openapi_schema"`
		} `json:"latest_version"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	if payload.LatestVersion == nil || len(payload.LatestVersion.OpenAPISchema) == 0 {
		return nil, fmt.Errorf("model %q has no published input schema", model)
	}
	return payload.LatestVersion.OpenAPISchema, nil
}

// VerifyKey confirms the API token is accepted by Replicate without starting
// a prediction — a cheap authenticated GET (the account endpoint) that
// returns 200 for a valid token and 401 for a bad one. Used by the Settings
// "Check Connection" button so a bad token fails fast and cheaply.
func (client ReplicateClient) VerifyKey(ctx context.Context) error {
	resp, err := client.do(ctx, http.MethodGet, "/v1/account", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}

// do issues one API request with Bearer auth, retrying once on a transient
// 5xx. Error bodies carry {title, detail}; the mapped prefixes
// ("replicate authentication failed", "replicate rate limited",
// "replicate credit exhausted") are what the harness's error-remediation
// rules match on, mirroring the fal client's error vocabulary.
func (client ReplicateClient) do(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
	if strings.TrimSpace(client.apiKey) == "" {
		return nil, errReplicateKeyNotConfigured
	}
	target := replicateAPIBaseURL + path
	var resp *http.Response
	for attempt := 0; ; attempt++ {
		var reader io.Reader
		if body != nil {
			reader = bytes.NewReader(body)
		}
		httpReq, err := http.NewRequestWithContext(ctx, method, target, reader)
		if err != nil {
			return nil, err
		}
		httpReq.Header.Set("Authorization", "Bearer "+client.apiKey)
		if body != nil {
			httpReq.Header.Set("Content-Type", "application/json")
		}
		resp, err = client.httpClient.Do(httpReq)
		if err != nil {
			return nil, err
		}
		if !isReplicateTransientStatus(resp.StatusCode) || attempt >= maxReplicateTransientRetries {
			break
		}
		// Drain before retry so the connection can be reused.
		resp.Body.Close()
	}
	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		message, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		trimmed := strings.TrimSpace(string(message))
		if detail := replicateErrorDetail(message); detail != "" {
			trimmed = detail
		}
		switch resp.StatusCode {
		case http.StatusUnauthorized:
			return nil, fmt.Errorf("replicate authentication failed: %s", trimmed)
		case http.StatusPaymentRequired:
			return nil, fmt.Errorf("replicate credit exhausted: %s", trimmed)
		case http.StatusTooManyRequests:
			return nil, fmt.Errorf("replicate rate limited: %s", trimmed)
		default:
			// Name the method and endpoint: a bare "replicate returned 422"
			// is opaque when several models are in play.
			return nil, fmt.Errorf("replicate %s %s returned %s: %s", method, path, resp.Status, trimmed)
		}
	}
	return resp, nil
}

// replicateErrorDetail extracts the {title, detail} pair Replicate's error
// bodies carry (detail may itself be a string or an array of validation
// errors), so a 401/422 message reads "Unauthenticated: ..." instead of raw
// JSON.
func replicateErrorDetail(body []byte) string {
	var payload struct {
		Title  string          `json:"title"`
		Detail json.RawMessage `json:"detail"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return ""
	}
	title := strings.TrimSpace(payload.Title)
	detail := replicateRawMessageText(payload.Detail)
	switch {
	case title != "" && detail != "":
		return title + ": " + detail
	case title != "":
		return title
	default:
		return detail
	}
}

// isReplicateTransientStatus reports whether a Replicate HTTP status is worth
// retrying — the same posture as the fal client: 5xx transient, 429 excluded
// (it has a dedicated rate-limit message), 4xx deterministic.
func isReplicateTransientStatus(code int) bool {
	switch code {
	case http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return false
}
