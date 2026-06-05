package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

const defaultOllamaBaseURL = "http://localhost:11434"

type App struct {
	ctx       context.Context
	client    *http.Client
	baseURL   string
	streams   map[string]context.CancelFunc
	streamsMu sync.Mutex
}

func NewApp() *App {
	return &App{
		client: &http.Client{
			Timeout: 10 * time.Minute,
		},
		baseURL: defaultOllamaBaseURL,
		streams: map[string]context.CancelFunc{},
	}
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
}

type OllamaStatus struct {
	Online  bool   `json:"online"`
	Version string `json:"version,omitempty"`
	BaseURL string `json:"baseURL"`
	Error   string `json:"error,omitempty"`
}

type OllamaModel struct {
	Name       string `json:"name"`
	ModifiedAt string `json:"modifiedAt,omitempty"`
	Size       int64  `json:"size,omitempty"`
	Family     string `json:"family,omitempty"`
	Parameter  string `json:"parameter,omitempty"`
}

type ollamaTagsResponse struct {
	Models []struct {
		Name       string `json:"name"`
		ModifiedAt string `json:"modified_at"`
		Size       int64  `json:"size"`
		Details    struct {
			Family        string `json:"family"`
			ParameterSize string `json:"parameter_size"`
		} `json:"details"`
	} `json:"models"`
}

type ChatMessage struct {
	Role    string   `json:"role"`
	Content string   `json:"content"`
	Images  []string `json:"images,omitempty"`
}

type ChatRequest struct {
	RequestID string         `json:"requestID,omitempty"`
	BaseURL   string         `json:"baseURL,omitempty"`
	Model     string         `json:"model"`
	System    string         `json:"system,omitempty"`
	Messages  []ChatMessage  `json:"messages"`
	Think     any            `json:"think,omitempty"`
	Options   map[string]any `json:"options,omitempty"`
}

type ChatStreamEvent struct {
	RequestID string `json:"requestID"`
	Content   string `json:"content,omitempty"`
	Thinking  string `json:"thinking,omitempty"`
	Done      bool   `json:"done"`
	Error     string `json:"error,omitempty"`
	Model     string `json:"model,omitempty"`
	Reason    string `json:"reason,omitempty"`
	Tokens    int    `json:"tokens,omitempty"`
}

type ollamaChatChunk struct {
	Model   string `json:"model"`
	Done    bool   `json:"done"`
	Message struct {
		Role     string `json:"role"`
		Content  string `json:"content"`
		Thinking string `json:"thinking"`
	} `json:"message"`
	DoneReason string `json:"done_reason"`
	EvalCount  int    `json:"eval_count"`
	Error      string `json:"error"`
}

type ImageGenerateRequest struct {
	BaseURL string `json:"baseURL,omitempty"`
	Model   string `json:"model"`
	Prompt  string `json:"prompt"`
	Width   int    `json:"width,omitempty"`
	Height  int    `json:"height,omitempty"`
	Steps   int    `json:"steps,omitempty"`
}

type ImageGenerateResponse struct {
	Model  string   `json:"model,omitempty"`
	Text   string   `json:"text,omitempty"`
	Images []string `json:"images"`
	Raw    string   `json:"raw,omitempty"`
	Error  string   `json:"error,omitempty"`
}

type SaveImageRequest struct {
	Image         string `json:"image"`
	SuggestedName string `json:"suggestedName,omitempty"`
}

type ollamaGenerateResponse struct {
	Model    string   `json:"model"`
	Response string   `json:"response"`
	Image    string   `json:"image"`
	Images   []string `json:"images"`
	Done     bool     `json:"done"`
	Error    string   `json:"error"`
}

func (a *App) SetOllamaBaseURL(baseURL string) error {
	normalized, err := normalizeBaseURL(baseURL)
	if err != nil {
		return err
	}
	a.baseURL = normalized
	return nil
}

func (a *App) CheckOllama(baseURL string) OllamaStatus {
	normalized := a.resolveBaseURL(baseURL)
	versionURL := normalized + "/api/version"
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, versionURL, nil)
	if err != nil {
		return OllamaStatus{Online: false, BaseURL: normalized, Error: err.Error()}
	}

	resp, err := a.client.Do(req)
	if err != nil {
		return OllamaStatus{Online: false, BaseURL: normalized, Error: err.Error()}
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return OllamaStatus{Online: false, BaseURL: normalized, Error: fmt.Sprintf("Ollama returned %s", resp.Status)}
	}

	var payload struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return OllamaStatus{Online: false, BaseURL: normalized, Error: err.Error()}
	}
	return OllamaStatus{Online: true, Version: payload.Version, BaseURL: normalized}
}

func (a *App) ListModels(baseURL string) ([]OllamaModel, error) {
	normalized := a.resolveBaseURL(baseURL)
	resp, err := a.getJSON(normalized + "/api/tags")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var tags ollamaTagsResponse
	if err := json.NewDecoder(resp.Body).Decode(&tags); err != nil {
		return nil, err
	}

	models := make([]OllamaModel, 0, len(tags.Models))
	for _, model := range tags.Models {
		models = append(models, OllamaModel{
			Name:       model.Name,
			ModifiedAt: model.ModifiedAt,
			Size:       model.Size,
			Family:     model.Details.Family,
			Parameter:  model.Details.ParameterSize,
		})
	}
	return models, nil
}

func (a *App) StreamChat(req ChatRequest) (string, error) {
	if strings.TrimSpace(req.Model) == "" {
		return "", errors.New("model is required")
	}
	if len(req.Messages) == 0 {
		return "", errors.New("at least one message is required")
	}

	requestID := strings.TrimSpace(req.RequestID)
	if requestID == "" {
		requestID = fmt.Sprintf("chat-%d", time.Now().UnixNano())
	}

	streamCtx, cancel := context.WithCancel(context.Background())
	a.streamsMu.Lock()
	a.streams[requestID] = cancel
	a.streamsMu.Unlock()

	go func() {
		defer func() {
			a.streamsMu.Lock()
			delete(a.streams, requestID)
			a.streamsMu.Unlock()
		}()
		a.runChatStream(streamCtx, requestID, req)
	}()

	return requestID, nil
}

func (a *App) CancelStream(requestID string) {
	a.streamsMu.Lock()
	cancel := a.streams[requestID]
	a.streamsMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (a *App) GenerateImage(req ImageGenerateRequest) (*ImageGenerateResponse, error) {
	if strings.TrimSpace(req.Model) == "" {
		return nil, errors.New("image model is required")
	}
	if strings.TrimSpace(req.Prompt) == "" {
		return nil, errors.New("prompt is required")
	}

	body := map[string]any{
		"model":  req.Model,
		"prompt": req.Prompt,
		"stream": false,
	}
	if req.Width > 0 {
		body["width"] = req.Width
	}
	if req.Height > 0 {
		body["height"] = req.Height
	}
	if req.Steps > 0 {
		body["steps"] = req.Steps
	}

	resp, err := a.postJSON(context.Background(), a.resolveBaseURL(req.BaseURL)+"/api/generate", body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var payload ollamaGenerateResponse
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}
	if payload.Error != "" {
		return nil, errors.New(payload.Error)
	}

	images := normalizeImagePayloads(payload.Images)
	if maybeImage := normalizeImagePayload(payload.Image); maybeImage != "" {
		images = append(images, maybeImage)
	}
	if maybeImage := normalizeImagePayload(payload.Response); maybeImage != "" {
		images = append(images, maybeImage)
	}
	images = append(images, collectImagesFromJSON(raw)...)

	return &ImageGenerateResponse{
		Model:  payload.Model,
		Text:   payload.Response,
		Images: dedupeStrings(images),
		Raw:    compactRawResponse(raw),
	}, nil
}

func (a *App) SaveImage(req SaveImageRequest) (string, error) {
	data, extension, err := decodeImagePayload(req.Image)
	if err != nil {
		return "", err
	}

	filename := sanitizeFilename(req.SuggestedName)
	if filename == "" {
		filename = "atelier-image" + extension
	}
	if !strings.HasSuffix(strings.ToLower(filename), extension) {
		filename += extension
	}

	path, err := runtime.SaveFileDialog(a.ctx, runtime.SaveDialogOptions{
		Title:           "Save generated image",
		DefaultFilename: filename,
		Filters: []runtime.FileFilter{
			{DisplayName: "Image Files", Pattern: "*.png;*.jpg;*.jpeg;*.webp;*.gif"},
			{DisplayName: "All Files", Pattern: "*.*"},
		},
	})
	if err != nil {
		return "", err
	}
	if path == "" {
		return "", nil
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		return "", err
	}
	return path, nil
}

func decodeImagePayload(image string) ([]byte, string, error) {
	image = strings.TrimSpace(image)
	if image == "" {
		return nil, "", errors.New("image data is empty")
	}

	extension := ".png"
	if strings.HasPrefix(image, "data:image/") {
		headerEnd := strings.Index(image, ",")
		if headerEnd < 0 {
			return nil, "", errors.New("image data URL is missing payload")
		}
		mediaType := image[len("data:"):headerEnd]
		if semicolon := strings.Index(mediaType, ";"); semicolon >= 0 {
			mediaType = mediaType[:semicolon]
		}
		extension = extensionForMediaType(mediaType)
		image = image[headerEnd+1:]
	}

	data, err := base64.StdEncoding.DecodeString(image)
	if err != nil {
		return nil, "", err
	}
	if !isImageBytes(data) {
		return nil, "", errors.New("payload is not a supported image")
	}
	return data, extension, nil
}

func (a *App) runChatStream(ctx context.Context, requestID string, req ChatRequest) {
	body := map[string]any{
		"model":    req.Model,
		"messages": req.Messages,
		"stream":   true,
	}
	if req.System != "" {
		body["messages"] = append([]ChatMessage{{Role: "system", Content: req.System}}, req.Messages...)
	}
	if req.Think != nil {
		body["think"] = req.Think
	}
	if req.Options != nil {
		body["options"] = req.Options
	}

	resp, err := a.postJSON(ctx, a.resolveBaseURL(req.BaseURL)+"/api/chat", body)
	if err != nil {
		a.emitChatEvent(ChatStreamEvent{RequestID: requestID, Error: err.Error(), Done: true})
		return
	}
	defer resp.Body.Close()

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		var chunk ollamaChatChunk
		if err := json.Unmarshal([]byte(line), &chunk); err != nil {
			a.emitChatEvent(ChatStreamEvent{RequestID: requestID, Error: err.Error(), Done: true})
			return
		}
		if chunk.Error != "" {
			a.emitChatEvent(ChatStreamEvent{RequestID: requestID, Error: chunk.Error, Done: true})
			return
		}

		a.emitChatEvent(ChatStreamEvent{
			RequestID: requestID,
			Content:   chunk.Message.Content,
			Thinking:  chunk.Message.Thinking,
			Done:      chunk.Done,
			Model:     chunk.Model,
			Reason:    chunk.DoneReason,
			Tokens:    chunk.EvalCount,
		})
		if chunk.Done {
			return
		}
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, context.Canceled) {
		a.emitChatEvent(ChatStreamEvent{RequestID: requestID, Error: err.Error(), Done: true})
	}
}

func (a *App) emitChatEvent(event ChatStreamEvent) {
	if a.ctx != nil {
		runtime.EventsEmit(a.ctx, "ollama:chat:chunk", event)
	}
}

func (a *App) getJSON(endpoint string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		message, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("ollama returned %s: %s", resp.Status, strings.TrimSpace(string(message)))
	}
	return resp, nil
}

func (a *App) postJSON(ctx context.Context, endpoint string, body map[string]any) (*http.Response, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := a.client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		message, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("ollama returned %s: %s", resp.Status, strings.TrimSpace(string(message)))
	}
	return resp, nil
}

func (a *App) resolveBaseURL(baseURL string) string {
	normalized, err := normalizeBaseURL(baseURL)
	if err != nil {
		return a.baseURL
	}
	if normalized == "" {
		return a.baseURL
	}
	return normalized
}

func normalizeBaseURL(baseURL string) (string, error) {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return "", nil
	}
	if !strings.HasPrefix(baseURL, "http://") && !strings.HasPrefix(baseURL, "https://") {
		baseURL = "http://" + baseURL
	}
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return "", err
	}
	if parsed.Host == "" {
		return "", errors.New("base URL must include a host")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return strings.TrimRight(parsed.String(), "/"), nil
}

func normalizeImagePayloads(images []string) []string {
	normalized := make([]string, 0, len(images))
	for _, image := range images {
		if payload := normalizeImagePayload(image); payload != "" {
			normalized = append(normalized, payload)
		}
	}
	return normalized
}

func normalizeImagePayload(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if strings.HasPrefix(value, "data:image/") {
		if _, _, err := decodeImagePayload(value); err == nil {
			return value
		}
		return ""
	}
	if strings.HasPrefix(value, "http://") || strings.HasPrefix(value, "https://") {
		return value
	}
	if strings.HasPrefix(value, "![") {
		start := strings.Index(value, "](")
		end := strings.LastIndex(value, ")")
		if start >= 0 && end > start+2 {
			return normalizeImagePayload(value[start+2 : end])
		}
	}
	if strings.Contains(value, "data:image/") {
		start := strings.Index(value, "data:image/")
		end := start
		for end < len(value) && !strings.ContainsRune(" \n\r\t\"')]", rune(value[end])) {
			end++
		}
		return normalizeImagePayload(value[start:end])
	}
	data, err := base64.StdEncoding.DecodeString(value)
	if err == nil && isImageBytes(data) {
		return "data:image/png;base64," + value
	}
	return ""
}

func isImageBytes(data []byte) bool {
	if len(data) >= 8 && bytes.Equal(data[:8], []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}) {
		return true
	}
	if len(data) >= 3 && data[0] == 0xff && data[1] == 0xd8 && data[2] == 0xff {
		return true
	}
	if len(data) >= 12 && bytes.Equal(data[:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP")) {
		return true
	}
	if len(data) >= 6 && (bytes.Equal(data[:6], []byte("GIF87a")) || bytes.Equal(data[:6], []byte("GIF89a"))) {
		return true
	}
	return false
}

func collectImagesFromJSON(raw []byte) []string {
	var payload any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil
	}
	var images []string
	walkJSONStrings(payload, func(value string) {
		if image := normalizeImagePayload(value); image != "" {
			images = append(images, image)
		}
	})
	return images
}

func walkJSONStrings(value any, visit func(string)) {
	switch typed := value.(type) {
	case string:
		visit(typed)
	case []any:
		for _, item := range typed {
			walkJSONStrings(item, visit)
		}
	case map[string]any:
		for _, item := range typed {
			walkJSONStrings(item, visit)
		}
	}
}

func dedupeStrings(values []string) []string {
	seen := map[string]bool{}
	deduped := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		deduped = append(deduped, value)
	}
	return deduped
}

func compactRawResponse(raw []byte) string {
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return compactString(string(raw), 1200)
	}
	redactLargeImageStrings(payload)
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return compactString(string(raw), 1200)
	}
	return compactString(string(data), 2000)
}

func redactLargeImageStrings(value any) {
	switch typed := value.(type) {
	case map[string]any:
		for key, item := range typed {
			if text, ok := item.(string); ok && normalizeImagePayload(text) != "" {
				typed[key] = fmt.Sprintf("<image data: %d chars>", len(text))
				continue
			}
			redactLargeImageStrings(item)
		}
	case []any:
		for index, item := range typed {
			if text, ok := item.(string); ok && normalizeImagePayload(text) != "" {
				typed[index] = fmt.Sprintf("<image data: %d chars>", len(text))
				continue
			}
			redactLargeImageStrings(item)
		}
	}
}

func compactString(value string, maxLength int) string {
	if len(value) <= maxLength {
		return value
	}
	return value[:maxLength] + "..."
}

func extensionForMediaType(mediaType string) string {
	switch strings.ToLower(mediaType) {
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/webp":
		return ".webp"
	case "image/gif":
		return ".gif"
	default:
		return ".png"
	}
}

func sanitizeFilename(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	for _, invalid := range []string{"/", "\\", ":", "*", "?", "\"", "<", ">", "|"} {
		value = strings.ReplaceAll(value, invalid, "-")
	}
	value = strings.Trim(value, ". ")
	return compactString(value, 80)
}
