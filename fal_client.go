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
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	falQueueBaseURL      = "https://queue.fal.run"
	falPlatformBaseURL   = "https://api.fal.ai"
	falOpenAPIBaseURL    = "https://fal.ai"
	defaultFalImageModel = "fal-ai/flux/schnell"
	// defaultFalImageEditModel is the image-to-image endpoint used to transform an
	// attached source image — the image-to-image sibling of defaultFalImageModel.
	defaultFalImageEditModel = "fal-ai/flux/dev/image-to-image"
	defaultFalVideoModel     = "fal-ai/kling-video/v2/master/text-to-video"
	// defaultFalVideoImageModel is the image-to-video endpoint used to animate an
	// attached image — the image-to-video sibling of defaultFalVideoModel.
	defaultFalVideoImageModel = "fal-ai/kling-video/v2/master/image-to-video"
	// defaultFalAudioModel is the text-to-audio endpoint used when none is
	// configured — a text-to-speech model, the most common "audio response" case.
	defaultFalAudioModel = "fal-ai/elevenlabs/tts/multilingual-v2"
	// defaultFalUpscaleModel is the image upscaler endpoint used when none is
	// configured — a simple, cheap ESRGAN-based upscaler that takes image_url +
	// scale. fal is the only upscale backend (Ollama has no upscaler).
	defaultFalUpscaleModel = "fal-ai/esrgan"
	// defaultFalVideoDuration / defaultFalVideoAspectRatio are fal's enum
	// defaults for text-to-video; kept here so config defaults and the client
	// agree on a single source of truth.
	defaultFalVideoDuration    = "5"
	defaultFalVideoAspectRatio = "16:9"
	// falPollInterval is the delay between queue status checks.
	falPollInterval = 1500 * time.Millisecond
	// falVideoMaxBytes caps a downloaded video. Generated clips are typically a
	// few MB; this bounds a runaway response without truncating a real result.
	falVideoMaxBytes = 256 * 1024 * 1024
	// falAudioMaxBytes caps a downloaded audio clip.
	falAudioMaxBytes = 128 * 1024 * 1024
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

// fetchOpenAPISchema returns the raw OpenAPI JSON describing a fal endpoint's
// input schema. This is a public endpoint; no Authorization is required.
func (client FalClient) fetchOpenAPISchema(ctx context.Context, model string) ([]byte, error) {
	path := "/api/openapi/queue/openapi.json?endpoint_id=" + url.QueryEscape(strings.TrimSpace(model))
	resp, err := client.do(ctx, falOpenAPIBaseURL, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
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
	Data      json.RawMessage `json:"data,omitempty"`
	Images    []falImage      `json:"images,omitempty"`
	Video     *falVideoFile   `json:"video,omitempty"`
	Audio     *falVideoFile   `json:"audio,omitempty"`
	AudioFile *falVideoFile   `json:"audio_file,omitempty"`
	Error     string          `json:"error,omitempty"`
}

// falVideoFile is a fal "File" result object (url + metadata). It backs the
// video and audio result fields, which share the same shape.
type falVideoFile struct {
	URL         string `json:"url"`
	ContentType string `json:"content_type,omitempty"`
	FileName    string `json:"file_name,omitempty"`
	FileSize    int64  `json:"file_size,omitempty"`
}

// GeneratedAudio is a downloaded text-to-audio result. Data holds the raw audio
// bytes so the caller can write them to disk as a file-path artifact.
type GeneratedAudio struct {
	Data      []byte
	MimeType  string
	SourceURL string
	// Notices holds deterministic, user-facing caveats produced while resolving
	// the request against the model's schema (e.g. a requested loop the model
	// cannot honor). Surfaced verbatim in the chat reply.
	Notices []string
}

// GeneratedVideo is a downloaded text-to-video result. Data holds the raw video
// bytes so the caller can write them to disk as a file-path artifact — video is
// never carried as a base64 data URL the way images are.
type GeneratedVideo struct {
	Data      []byte
	MimeType  string
	SourceURL string
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
	// Image-to-image: fal takes the source frame as image_url and derives the
	// output dimensions from it, so image_size is omitted. Present only when the
	// caller supplied a source image to transform.
	if image := falImageURL(firstNonEmpty(req.Images)); image != "" {
		body["image_url"] = image
	} else if req.Width > 0 && req.Height > 0 {
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
	statusURL, resultURL := falQueueURLs(submit, model, requestID)

	if err := client.waitForCompletion(ctx, statusURL); err != nil {
		return ollamaGenerateResponse{}, nil, err
	}

	result, _, err := client.fetchResult(ctx, resultURL)
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

// UpscaleImage submits an attached image to the configured fal upscaler
// endpoint, polls until completion, and returns the upscaled image as a base64
// data URL packed into a synthetic ollamaGenerateResponse — the same shape
// GenerateImage produces, so the tool's result normalization is reused. fal is
// the only upscale backend; there is no Ollama path.
func (client FalClient) UpscaleImage(ctx context.Context, req ImageUpscaleRequest) (ollamaGenerateResponse, error) {
	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = defaultFalUpscaleModel
	}

	scale := req.Scale
	if scale <= 0 {
		scale = 2
	}
	body := map[string]any{
		"image_url": falImageURL(req.Image),
		"scale":     scale,
	}

	submit, err := client.submit(ctx, model, body)
	if err != nil {
		return ollamaGenerateResponse{}, err
	}
	requestID := strings.TrimSpace(submit.RequestID)
	if requestID == "" {
		return ollamaGenerateResponse{}, errors.New("fal submit returned no request id")
	}
	statusURL, resultURL := falQueueURLs(submit, model, requestID)

	if err := client.waitForCompletion(ctx, statusURL); err != nil {
		return ollamaGenerateResponse{}, err
	}

	result, _, err := client.fetchResult(ctx, resultURL)
	if err != nil {
		return ollamaGenerateResponse{}, err
	}

	dataURLs, err := client.downloadImages(ctx, result.Images)
	if err != nil {
		return ollamaGenerateResponse{}, err
	}
	if len(dataURLs) == 0 {
		return ollamaGenerateResponse{}, errors.New("fal upscale returned no images")
	}

	return ollamaGenerateResponse{
		Model:  model,
		Image:  dataURLs[0],
		Images: dataURLs,
		Done:   true,
	}, nil
}

// firstNonEmpty returns the first entry of values that is non-empty after
// trimming, or "" when none is. Used to pick a single source image from a
// request's image slice.
func firstNonEmpty(values []string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// falImageURL normalizes an image reference for fal's image_url input. fal
// requires an HTTP(S) URL or a data URI and rejects bare base64 with a 422; the
// rest of the app carries attached images as bare base64 (the data: prefix is
// stripped for Ollama), so wrap those in a data URI, sniffing the media type
// from the decoded bytes. URLs and existing data URIs pass through unchanged.
func falImageURL(image string) string {
	image = strings.TrimSpace(image)
	if image == "" {
		return ""
	}
	if strings.HasPrefix(image, "http://") || strings.HasPrefix(image, "https://") || strings.HasPrefix(image, "data:") {
		return image
	}
	mediaType := "image/png"
	if data, err := base64.StdEncoding.DecodeString(image); err == nil {
		if detected := http.DetectContentType(data); strings.HasPrefix(detected, "image/") {
			mediaType = detected
		}
	}
	return "data:" + mediaType + ";base64," + image
}

// GenerateVideo submits a text-to-video request to the fal queue, polls until
// completion, and downloads the resulting clip as raw bytes. Video generation
// runs for minutes rather than seconds, so callers must pass a context with a
// suitably long deadline. The queue submit/poll mechanics are identical to
// GenerateImage; only the request body and result shape differ (a single
// "video" object rather than an "images" array).
func (client FalClient) GenerateVideo(ctx context.Context, req VideoGenerateRequest) (GeneratedVideo, error) {
	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = defaultFalVideoModel
	}

	body := map[string]any{"prompt": req.Prompt}
	if duration := strings.TrimSpace(req.Duration); duration != "" {
		body["duration"] = duration
	}
	if aspect := strings.TrimSpace(req.AspectRatio); aspect != "" {
		body["aspect_ratio"] = aspect
	}
	if negative := strings.TrimSpace(req.NegativePrompt); negative != "" {
		body["negative_prompt"] = negative
	}
	// generate_audio is a per-model boolean (e.g. Veo3, some Kling variants).
	// Present only when the caller set it explicitly, so models default their own
	// way when it's unspecified; audio-less endpoints ignore the field.
	if req.GenerateAudio != nil {
		body["generate_audio"] = *req.GenerateAudio
	}
	// Image-to-video: fal takes the source frame as image_url. Present only when
	// the caller supplied an image to animate.
	if image := falImageURL(req.Image); image != "" {
		body["image_url"] = image
	}

	submit, err := client.submit(ctx, model, body)
	if err != nil {
		return GeneratedVideo{}, err
	}
	requestID := strings.TrimSpace(submit.RequestID)
	if requestID == "" {
		return GeneratedVideo{}, errors.New("fal submit returned no request id")
	}
	statusURL, resultURL := falQueueURLs(submit, model, requestID)

	if err := client.waitForCompletion(ctx, statusURL); err != nil {
		return GeneratedVideo{}, err
	}

	result, raw, err := client.fetchResult(ctx, resultURL)
	if err != nil {
		return GeneratedVideo{}, err
	}

	videoURL := ""
	if result.Video != nil {
		videoURL = strings.TrimSpace(result.Video.URL)
	}
	if videoURL == "" {
		videoURL = firstFalVideoURL(raw)
	}
	if videoURL == "" {
		return GeneratedVideo{}, errors.New("fal result returned no video")
	}

	data, mimeType, err := client.downloadVideo(ctx, videoURL)
	if err != nil {
		return GeneratedVideo{}, err
	}
	return GeneratedVideo{Data: data, MimeType: mimeType, SourceURL: videoURL}, nil
}

// GenerateAudio submits a text-to-audio request to the fal queue, polls until
// completion, and downloads the resulting clip as raw bytes. It sends the prompt
// as both "prompt" and "text": music/sound-effect endpoints read "prompt" while
// text-to-speech endpoints read "text", and fal's audio inputs ignore the extra
// field. The result is a "audio" (or "audio_file") File object.
// GenerateAudio submits an already-native fal request body (built by
// resolveAudioBody against the model's schema) and downloads the result. It is a
// thin transport: it does not know about canonical params or notices.
func (client FalClient) GenerateAudio(ctx context.Context, model string, body map[string]any) (GeneratedAudio, error) {
	model = strings.TrimSpace(model)
	if model == "" {
		model = defaultFalAudioModel
	}
	if body == nil {
		body = map[string]any{}
	}

	submit, err := client.submit(ctx, model, body)
	if err != nil {
		return GeneratedAudio{}, err
	}
	requestID := strings.TrimSpace(submit.RequestID)
	if requestID == "" {
		return GeneratedAudio{}, errors.New("fal submit returned no request id")
	}
	statusURL, resultURL := falQueueURLs(submit, model, requestID)

	if err := client.waitForCompletion(ctx, statusURL); err != nil {
		return GeneratedAudio{}, err
	}

	result, raw, err := client.fetchResult(ctx, resultURL)
	if err != nil {
		return GeneratedAudio{}, err
	}

	audioURL := ""
	for _, file := range []*falVideoFile{result.Audio, result.AudioFile} {
		if file != nil && strings.TrimSpace(file.URL) != "" {
			audioURL = strings.TrimSpace(file.URL)
			break
		}
	}
	if audioURL == "" {
		audioURL = firstFalAudioURL(raw)
	}
	if audioURL == "" {
		return GeneratedAudio{}, errors.New("fal result returned no audio")
	}

	data, mimeType, err := client.downloadAudio(ctx, audioURL)
	if err != nil {
		return GeneratedAudio{}, err
	}
	return GeneratedAudio{Data: data, MimeType: mimeType, SourceURL: audioURL}, nil
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

// falQueueURLs resolves the status and result URLs for a submitted request. fal
// returns absolute status_url/response_url in the submit response; use those
// verbatim. Their path lives under the fal "app" id (the first two segments of
// the endpoint), NOT the full model id — reconstructing from the full model id
// yields a path fal answers with 405 Method Not Allowed. Reconstruct from the
// app path only as a fallback if fal omits the URLs.
func falQueueURLs(submit falSubmitResponse, model, requestID string) (statusURL, resultURL string) {
	statusURL = strings.TrimSpace(submit.StatusURL)
	resultURL = strings.TrimSpace(submit.ResponseURL)
	if statusURL == "" || resultURL == "" {
		base := falQueueBaseURL + "/" + falAppPath(model) + "/requests/" + requestID
		if statusURL == "" {
			statusURL = base + "/status"
		}
		if resultURL == "" {
			resultURL = base
		}
	}
	return statusURL, resultURL
}

// falAppPath returns the fal application path — the first two segments of an
// endpoint id (e.g. "fal-ai/kling-video" from
// "fal-ai/kling-video/v2/master/image-to-video"). fal's queue status and result
// routes live under the app path, not the full endpoint id.
func falAppPath(model string) string {
	segments := strings.Split(strings.Trim(strings.TrimSpace(model), "/"), "/")
	if len(segments) <= 2 {
		return strings.Join(segments, "/")
	}
	return strings.Join(segments[:2], "/")
}

func (client FalClient) waitForCompletion(ctx context.Context, statusURL string) error {
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("fal image generation cancelled: %w", err)
		}

		resp, err := client.do(ctx, "", http.MethodGet, statusURL, nil)
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

func (client FalClient) fetchResult(ctx context.Context, resultURL string) (falResultResponse, []byte, error) {
	resp, err := client.do(ctx, "", http.MethodGet, resultURL, nil)
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

// downloadVideo fetches a generated video URL and returns its raw bytes and MIME
// type. Unlike images (re-encoded as base64 data URLs), video bytes are handed
// back for the caller to write straight to a file-path artifact.
func (client FalClient) downloadVideo(ctx context.Context, videoURL string) ([]byte, string, error) {
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
	data, err := io.ReadAll(io.LimitReader(resp.Body, falVideoMaxBytes))
	if err != nil {
		return nil, "", err
	}
	if !isVideoBytes(data) {
		return nil, "", errors.New("downloaded fal result is not a supported video")
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

// downloadAudio fetches a generated audio URL and returns its raw bytes and MIME
// type, for the caller to write straight to a file-path artifact.
func (client FalClient) downloadAudio(ctx context.Context, audioURL string) ([]byte, string, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, audioURL, nil)
	if err != nil {
		return nil, "", err
	}
	resp, err := client.httpClient.Do(httpReq)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, "", fmt.Errorf("audio download failed: %s", resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, falAudioMaxBytes))
	if err != nil {
		return nil, "", err
	}
	mimeType := strings.TrimSpace(resp.Header.Get("Content-Type"))
	if !isAudioBytes(data) && !strings.HasPrefix(mimeType, "audio/") {
		return nil, "", errors.New("downloaded fal result is not a supported audio clip")
	}
	if !strings.HasPrefix(mimeType, "audio/") {
		if detected := http.DetectContentType(data); strings.HasPrefix(detected, "audio/") {
			mimeType = detected
		} else {
			mimeType = "audio/mpeg"
		}
	}
	return data, mimeType, nil
}

// firstFalAudioURL walks the raw fal result for the first http(s) URL that looks
// like an audio file. Used as a fallback when the top-level audio object is
// absent or named differently.
func firstFalAudioURL(raw []byte) string {
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
		for _, ext := range []string{".mp3", ".wav", ".ogg", ".flac", ".m4a", ".aac", ".opus"} {
			if strings.Contains(lower, ext) {
				found = trimmed
				return
			}
		}
	})
	return found
}

// firstFalVideoURL walks the raw fal result for the first http(s) URL that looks
// like a video file. Used as a fallback when the top-level "video" object is
// absent (some endpoints nest the result differently).
func firstFalVideoURL(raw []byte) string {
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

// FalModel is one entry from fal's public model catalog (GET /v1/models),
// flattened into the fields a model picker needs. It is separate from
// ModelInfo (the ChatProvider abstraction) because fal is image-only and never
// participates in the chat/harness model roles.
type FalModel struct {
	ID           string   `json:"id"`
	DisplayName  string   `json:"displayName"`
	Category     string   `json:"category"`
	Description  string   `json:"description"`
	Status       string   `json:"status"`
	Tags         []string `json:"tags"`
	ThumbnailURL string   `json:"thumbnailUrl"`
}

// falModelsPage mirrors the paginated /v1/models response. Each entry carries an
// endpoint id and (optionally) a metadata block — metadata is absent for
// endpoints without a registry entry, so it is a pointer we null-check.
type falModelsPage struct {
	Models []struct {
		EndpointID string `json:"endpoint_id"`
		Metadata   *struct {
			DisplayName  string   `json:"display_name"`
			Category     string   `json:"category"`
			Description  string   `json:"description"`
			Status       string   `json:"status"`
			Tags         []string `json:"tags"`
			ThumbnailURL string   `json:"thumbnail_url"`
		} `json:"metadata"`
	} `json:"models"`
	NextCursor *string `json:"next_cursor"`
	HasMore    bool    `json:"has_more"`
}

const (
	// falTextToImageCategory is the /v1/models category filter for text-to-image
	// endpoints — the ones eligible as an image-generation model.
	falTextToImageCategory = "text-to-image"
	// falTextToVideoCategory is the /v1/models category filter for text-to-video
	// endpoints — the ones eligible as a video-generation model.
	falTextToVideoCategory = "text-to-video"
	// falImageToVideoCategory is the /v1/models category filter for
	// image-to-video endpoints — used to animate an attached image.
	falImageToVideoCategory = "image-to-video"
	// falTextToAudioCategory / falTextToSpeechCategory are the /v1/models
	// category filters for audio-generation endpoints (music/sound effects and
	// speech, respectively).
	falTextToAudioCategory  = "text-to-audio"
	falTextToSpeechCategory = "text-to-speech"
	// falImageUpscalingCategory is not a dedicated /v1/models category — fal
	// files upscalers under the broader image-to-image bucket alongside
	// inpainting, background removal, and other image-editing endpoints. We
	// fetch that category and post-filter by id/tags so the Settings picker
	// shows only upscalers. Kept as a named constant for clarity at the call
	// site; ListFalUpscaleModels is the only consumer.
	falImageUpscalingCategory = "image-to-image"
	// falModelsPageSize is how many models to request per catalog page.
	falModelsPageSize = 100
	// falModelsDefaultMax caps how many models ListModels will accumulate so we
	// don't walk the entire (large, growing) catalog on a settings open.
	falModelsDefaultMax = 200
)

// ListModels returns fal's public model catalog filtered by category (empty
// means all categories), walking the paginated /v1/models endpoint until the
// catalog is exhausted or maxModels entries have been collected. maxModels <= 0
// applies falModelsDefaultMax. fal allows this endpoint keyless (a key only
// raises rate limits), but it routes through the shared do() helper, which
// requires the configured key — matching VerifyKey and the rest of the client.
func (client FalClient) ListModels(ctx context.Context, category string, maxModels int) ([]FalModel, error) {
	if maxModels <= 0 {
		maxModels = falModelsDefaultMax
	}

	models := make([]FalModel, 0, maxModels)
	cursor := ""
	for {
		query := url.Values{}
		if trimmed := strings.TrimSpace(category); trimmed != "" {
			query.Set("category", trimmed)
		}
		query.Set("limit", strconv.Itoa(falModelsPageSize))
		if cursor != "" {
			query.Set("cursor", cursor)
		}

		resp, err := client.do(ctx, falPlatformBaseURL, http.MethodGet, "/v1/models?"+query.Encode(), nil)
		if err != nil {
			return nil, err
		}
		var page falModelsPage
		decodeErr := json.NewDecoder(resp.Body).Decode(&page)
		resp.Body.Close()
		if decodeErr != nil {
			return nil, decodeErr
		}

		for _, entry := range page.Models {
			id := strings.TrimSpace(entry.EndpointID)
			if id == "" {
				continue
			}
			model := FalModel{ID: id, DisplayName: id}
			if entry.Metadata != nil {
				if name := strings.TrimSpace(entry.Metadata.DisplayName); name != "" {
					model.DisplayName = name
				}
				model.Category = entry.Metadata.Category
				model.Description = entry.Metadata.Description
				model.Status = entry.Metadata.Status
				model.Tags = entry.Metadata.Tags
				model.ThumbnailURL = entry.Metadata.ThumbnailURL
			}
			models = append(models, model)
			if len(models) >= maxModels {
				return models, nil
			}
		}

		if !page.HasMore || page.NextCursor == nil || strings.TrimSpace(*page.NextCursor) == "" {
			return models, nil
		}
		cursor = strings.TrimSpace(*page.NextCursor)
	}
}

// maxFalRedirects caps how many redirects do() follows while preserving the
// request method and body. fal can 3xx-redirect a submit to a canonical
// endpoint; net/http's default client downgrades a redirected POST to a GET
// (per the 301/302/303 spec), and the queue submit endpoint answers that GET
// with 405 Method Not Allowed. Following the redirect ourselves with the method
// and body intact avoids that failure mode.
const maxFalRedirects = 5

func (client FalClient) do(ctx context.Context, baseURL, method, path string, body map[string]any) (*http.Response, error) {
	if strings.TrimSpace(client.apiKey) == "" {
		return nil, errors.New("fal api key is not configured")
	}

	var bodyBytes []byte
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		bodyBytes = data
	}

	// Follow redirects manually so a redirected POST stays a POST. A copy of the
	// client whose CheckRedirect returns ErrUseLastResponse hands us the 3xx
	// response instead of auto-following (and downgrading) it, while still
	// sharing the underlying Transport and its connection pool.
	noFollow := *client.httpClient
	noFollow.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	target := baseURL + path
	var resp *http.Response
	for hop := 0; ; hop++ {
		var reader io.Reader
		if bodyBytes != nil {
			reader = bytes.NewReader(bodyBytes)
		}
		httpReq, err := http.NewRequestWithContext(ctx, method, target, reader)
		if err != nil {
			return nil, err
		}
		httpReq.Header.Set("Authorization", "Key "+client.apiKey)
		if bodyBytes != nil {
			httpReq.Header.Set("Content-Type", "application/json")
		}

		resp, err = noFollow.Do(httpReq)
		if err != nil {
			return nil, err
		}
		if !isRedirectStatus(resp.StatusCode) {
			break
		}
		location := strings.TrimSpace(resp.Header.Get("Location"))
		resolved, resolveErr := resp.Request.URL.Parse(location)
		resp.Body.Close()
		if location == "" || resolveErr != nil {
			return nil, fmt.Errorf("fal %s %s returned %s with no usable redirect location", method, target, resp.Status)
		}
		if hop >= maxFalRedirects {
			return nil, fmt.Errorf("fal %s %s exceeded %d redirects", method, target, maxFalRedirects)
		}
		target = resolved.String()
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
			// Name the method and endpoint: a bare "fal returned 405" is opaque
			// when several models are in play.
			return nil, fmt.Errorf("fal %s %s returned %s: %s", method, target, resp.Status, trimmed)
		}
	}
	return resp, nil
}

func isRedirectStatus(code int) bool {
	switch code {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
		http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		return true
	}
	return false
}
