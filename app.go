package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
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
	configMu  sync.Mutex
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

type AppConfig struct {
	Version    int              `json:"version"`
	Storage    ConfigStorage    `json:"storage"`
	Providers  ConfigProviders  `json:"providers"`
	Prompts    ConfigPrompts    `json:"prompts"`
	Generation ConfigGeneration `json:"generation"`
	UI         ConfigUI         `json:"ui"`
}

type ConfigStorage struct {
	Root      string `json:"root"`
	History   string `json:"history"`
	Artifacts string `json:"artifacts"`
}

type ConfigProviders struct {
	Ollama ConfigOllama `json:"ollama"`
}

type ConfigOllama struct {
	BaseURL string             `json:"baseURL"`
	Models  ConfigOllamaModels `json:"models"`
}

type ConfigOllamaModels struct {
	Chat  string `json:"chat"`
	Image string `json:"image"`
}

type ConfigPrompts struct {
	System string `json:"system"`
}

type ConfigGeneration struct {
	Image ConfigImageGeneration `json:"image"`
}

type ConfigImageGeneration struct {
	Width  int `json:"width"`
	Height int `json:"height"`
	Steps  int `json:"steps"`
}

type ConfigUI struct {
	Mode string `json:"mode"`
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
	RequestID      string         `json:"requestID,omitempty"`
	ConversationID string         `json:"conversationId,omitempty"`
	BaseURL        string         `json:"baseURL,omitempty"`
	Model          string         `json:"model"`
	System         string         `json:"system,omitempty"`
	Messages       []ChatMessage  `json:"messages"`
	Think          any            `json:"think,omitempty"`
	Options        map[string]any `json:"options,omitempty"`
}

type ChatStreamEvent struct {
	RequestID      string `json:"requestID"`
	Content        string `json:"content,omitempty"`
	Thinking       string `json:"thinking,omitempty"`
	Done           bool   `json:"done"`
	Error          string `json:"error,omitempty"`
	Model          string `json:"model,omitempty"`
	Reason         string `json:"reason,omitempty"`
	Tokens         int    `json:"tokens,omitempty"`
	ConversationID string `json:"conversationId,omitempty"`
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

type ollamaChatResponse struct {
	Model   string `json:"model"`
	Message struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"message"`
	Response string `json:"response"`
	Done     bool   `json:"done"`
	Error    string `json:"error"`
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
	Model          string   `json:"model,omitempty"`
	Text           string   `json:"text,omitempty"`
	Images         []string `json:"images"`
	Raw            string   `json:"raw,omitempty"`
	Error          string   `json:"error,omitempty"`
	ConversationID string   `json:"conversationId,omitempty"`
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

type ConversationSummary struct {
	ID            string `json:"id"`
	Kind          string `json:"kind"`
	Title         string `json:"title"`
	CreatedAt     string `json:"createdAt"`
	UpdatedAt     string `json:"updatedAt"`
	DeletedAt     string `json:"deletedAt,omitempty"`
	TurnCount     int    `json:"turnCount"`
	ArtifactCount int    `json:"artifactCount"`
}

type ConversationDetail struct {
	Conversation HistoryConversation `json:"conversation"`
	Turns        []HistoryTurn       `json:"turns"`
}

type PurgeArchivedResult struct {
	DeletedConversations int `json:"deletedConversations"`
	DeletedAssets        int `json:"deletedAssets"`
}

type HistoryConversation struct {
	SchemaVersion int                      `json:"schemaVersion"`
	ID            string                   `json:"id"`
	Kind          string                   `json:"kind"`
	Title         string                   `json:"title"`
	CreatedAt     string                   `json:"createdAt"`
	UpdatedAt     string                   `json:"updatedAt"`
	DeletedAt     string                   `json:"deletedAt,omitempty"`
	Provider      HistoryProvider          `json:"provider"`
	Defaults      HistoryDefaults          `json:"defaults"`
	Stats         HistoryConversationStats `json:"stats"`
}

type HistoryProvider struct {
	ID      string `json:"id"`
	BaseURL string `json:"baseURL"`
}

type HistoryDefaults struct {
	ChatModel  string `json:"chatModel,omitempty"`
	ImageModel string `json:"imageModel,omitempty"`
	System     string `json:"system,omitempty"`
}

type HistoryConversationStats struct {
	TurnCount     int `json:"turnCount"`
	ArtifactCount int `json:"artifactCount"`
}

type HistoryTurn struct {
	SchemaVersion    int              `json:"schemaVersion"`
	ID               string           `json:"id"`
	ConversationID   string           `json:"conversationId"`
	CreatedAt        string           `json:"createdAt"`
	Kind             string           `json:"kind"`
	Role             string           `json:"role"`
	Model            string           `json:"model,omitempty"`
	Content          []HistoryContent `json:"content"`
	Request          map[string]any   `json:"request,omitempty"`
	ProviderResponse map[string]any   `json:"providerResponse,omitempty"`
	DeletedAt        string           `json:"deletedAt,omitempty"`
}

type HistoryContent struct {
	Type       string `json:"type"`
	Text       string `json:"text,omitempty"`
	ArtifactID string `json:"artifactId,omitempty"`
	Path       string `json:"path,omitempty"`
	MimeType   string `json:"mimeType,omitempty"`
	Width      int    `json:"width,omitempty"`
	Height     int    `json:"height,omitempty"`
}

func (a *App) GetConfig() (AppConfig, error) {
	a.configMu.Lock()
	defer a.configMu.Unlock()

	config, err := loadAppConfig()
	if err != nil {
		return AppConfig{}, err
	}
	if err := writeAppConfig(config); err != nil {
		return AppConfig{}, err
	}
	if err := ensureStorageDirs(config.Storage); err != nil {
		return AppConfig{}, err
	}
	a.baseURL = config.Providers.Ollama.BaseURL
	return config, nil
}

func (a *App) SaveConfig(config AppConfig) error {
	a.configMu.Lock()
	defer a.configMu.Unlock()

	merged := mergeAppConfig(config)
	if err := writeAppConfig(merged); err != nil {
		return err
	}
	if err := ensureStorageDirs(merged.Storage); err != nil {
		return err
	}
	a.baseURL = merged.Providers.Ollama.BaseURL
	return nil
}

func (a *App) ListConversations() ([]ConversationSummary, error) {
	config, err := loadAppConfig()
	if err != nil {
		return nil, err
	}
	if err := ensureStorageDirs(config.Storage); err != nil {
		return nil, err
	}
	return listConversations(config.Storage)
}

func (a *App) GetConversation(conversationID string) (ConversationDetail, error) {
	config, err := loadAppConfig()
	if err != nil {
		return ConversationDetail{}, err
	}
	return getConversation(config.Storage, conversationID)
}

func (a *App) DeleteConversation(conversationID string) error {
	config, err := loadAppConfig()
	if err != nil {
		return err
	}
	return deleteConversation(config.Storage, conversationID)
}

func (a *App) PurgeArchivedConversations() (PurgeArchivedResult, error) {
	config, err := loadAppConfig()
	if err != nil {
		return PurgeArchivedResult{}, err
	}
	return purgeArchivedConversations(config.Storage)
}

func (a *App) UpdateConversationTitle(conversationID string, title string) (ConversationSummary, error) {
	config, err := loadAppConfig()
	if err != nil {
		return ConversationSummary{}, err
	}
	return updateConversationTitle(config.Storage, conversationID, title)
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

	images = dedupeStrings(images)
	conversationID := ""
	if len(images) > 0 {
		config, err := loadAppConfig()
		if err != nil {
			return nil, err
		}
		if err := ensureStorageDirs(config.Storage); err != nil {
			return nil, err
		}
		conversationID, err = writeImageGenerationConversation(config, req, payload, images, compactRawResponse(raw))
		if err != nil {
			return nil, err
		}
	}

	return &ImageGenerateResponse{
		Model:          payload.Model,
		Text:           payload.Response,
		Images:         images,
		Raw:            compactRawResponse(raw),
		ConversationID: conversationID,
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
	var assistantContent strings.Builder
	var assistantThinking strings.Builder
	var finalModel string
	var finalReason string
	var finalTokens int
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

		assistantContent.WriteString(chunk.Message.Content)
		assistantThinking.WriteString(chunk.Message.Thinking)
		if chunk.Model != "" {
			finalModel = chunk.Model
		}
		if chunk.DoneReason != "" {
			finalReason = chunk.DoneReason
		}
		if chunk.EvalCount > 0 {
			finalTokens = chunk.EvalCount
		}

		conversationID := ""
		if chunk.Done {
			var err error
			conversationID, err = a.writeChatConversation(req, assistantContent.String(), assistantThinking.String(), finalModel, finalReason, finalTokens)
			if err != nil {
				a.emitChatEvent(ChatStreamEvent{RequestID: requestID, Error: fmt.Sprintf("history save failed: %v", err), Done: true})
				return
			}
		}

		a.emitChatEvent(ChatStreamEvent{
			RequestID:      requestID,
			Content:        chunk.Message.Content,
			Thinking:       chunk.Message.Thinking,
			Done:           chunk.Done,
			Model:          chunk.Model,
			Reason:         chunk.DoneReason,
			Tokens:         chunk.EvalCount,
			ConversationID: conversationID,
		})
		if chunk.Done {
			return
		}
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, context.Canceled) {
		a.emitChatEvent(ChatStreamEvent{RequestID: requestID, Error: err.Error(), Done: true})
	}
}

func (a *App) writeChatConversation(req ChatRequest, assistantContent, assistantThinking, model, reason string, tokens int) (string, error) {
	if strings.TrimSpace(assistantContent) == "" && strings.TrimSpace(assistantThinking) == "" {
		return "", nil
	}
	config, err := loadAppConfig()
	if err != nil {
		return "", err
	}
	if err := ensureStorageDirs(config.Storage); err != nil {
		return "", err
	}
	if strings.TrimSpace(model) == "" {
		model = req.Model
	}
	title := ""
	if strings.TrimSpace(req.ConversationID) == "" {
		title = a.generateConversationTitle(config, req, assistantContent)
	}
	return newHarnessEngine(config).SaveChatTurn(req, assistantContent, assistantThinking, model, reason, tokens, title)
}

func (a *App) generateConversationTitle(config AppConfig, req ChatRequest, assistantContent string) string {
	userPrompt := lastUserPrompt(req.Messages)
	fallback := titleFromPrompt(userPrompt)
	if strings.TrimSpace(req.Model) == "" || strings.TrimSpace(userPrompt) == "" {
		return fallback
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	titlePrompt := "Generate a concise title for this chat conversation. Return only the title, no quotes, no punctuation wrapper, no explanation. Keep it under 8 words.\n\nUser:\n" +
		compactString(userPrompt, 1600) +
		"\n\nAssistant:\n" +
		compactString(assistantContent, 1600)
	body := map[string]any{
		"model":  req.Model,
		"stream": false,
		"messages": []ChatMessage{
			{Role: "system", Content: "You create short, specific conversation titles."},
			{Role: "user", Content: titlePrompt},
		},
		"options": map[string]any{
			"temperature": 0,
			"num_predict": 24,
		},
	}

	resp, err := a.postJSON(ctx, a.resolveBaseURL(req.BaseURL)+"/api/chat", body)
	if err != nil {
		return fallback
	}
	defer resp.Body.Close()

	var payload ollamaChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return fallback
	}
	if payload.Error != "" {
		return fallback
	}
	title := cleanGeneratedTitle(payload.Message.Content)
	if title == "" {
		title = cleanGeneratedTitle(payload.Response)
	}
	if title == "" {
		return fallback
	}
	return title
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

func configPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".atelier", "config.json"), nil
}

func loadAppConfig() (AppConfig, error) {
	path, err := configPath()
	if err != nil {
		return AppConfig{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return defaultAppConfig(), nil
		}
		return AppConfig{}, err
	}

	var config AppConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return AppConfig{}, err
	}
	return mergeAppConfig(config), nil
}

func writeAppConfig(config AppConfig) error {
	path, err := configPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}

	data, err := json.MarshalIndent(mergeAppConfig(config), "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	tempPath := path + ".tmp"
	if err := os.WriteFile(tempPath, data, 0644); err != nil {
		return err
	}
	return os.Rename(tempPath, path)
}

func defaultAppConfig() AppConfig {
	root := defaultStorageRoot()
	history := filepath.Join(root, "history")
	return AppConfig{
		Version: 1,
		Storage: ConfigStorage{
			Root:      root,
			History:   history,
			Artifacts: history,
		},
		Providers: ConfigProviders{
			Ollama: ConfigOllama{
				BaseURL: defaultOllamaBaseURL,
				Models: ConfigOllamaModels{
					Chat:  "mistral-small3.1:latest",
					Image: "x/z-image-turbo:latest",
				},
			},
		},
		Prompts: ConfigPrompts{
			System: "You are Atelier, a precise local AI collaborator.",
		},
		Generation: ConfigGeneration{
			Image: ConfigImageGeneration{
				Width:  768,
				Height: 768,
				Steps:  24,
			},
		},
		UI: ConfigUI{
			Mode: "chat",
		},
	}
}

func mergeAppConfig(config AppConfig) AppConfig {
	defaults := defaultAppConfig()
	if config.Version <= 0 {
		config.Version = defaults.Version
	}
	config.Storage = mergeStorageConfig(config.Storage, defaults.Storage)
	if normalized, err := normalizeBaseURL(config.Providers.Ollama.BaseURL); err == nil && normalized != "" {
		config.Providers.Ollama.BaseURL = normalized
	} else {
		config.Providers.Ollama.BaseURL = defaults.Providers.Ollama.BaseURL
	}
	if strings.TrimSpace(config.Providers.Ollama.Models.Chat) == "" {
		config.Providers.Ollama.Models.Chat = defaults.Providers.Ollama.Models.Chat
	}
	if strings.TrimSpace(config.Providers.Ollama.Models.Image) == "" {
		config.Providers.Ollama.Models.Image = defaults.Providers.Ollama.Models.Image
	}
	if strings.TrimSpace(config.Prompts.System) == "" {
		config.Prompts.System = defaults.Prompts.System
	}
	if config.Generation.Image.Width <= 0 {
		config.Generation.Image.Width = defaults.Generation.Image.Width
	}
	if config.Generation.Image.Height <= 0 {
		config.Generation.Image.Height = defaults.Generation.Image.Height
	}
	if config.Generation.Image.Steps <= 0 {
		config.Generation.Image.Steps = defaults.Generation.Image.Steps
	}
	if config.UI.Mode != "chat" && config.UI.Mode != "image" {
		config.UI.Mode = defaults.UI.Mode
	}
	return config
}

func defaultStorageRoot() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", ".atelier")
	}
	return filepath.Join(home, ".atelier")
}

func mergeStorageConfig(storage ConfigStorage, defaults ConfigStorage) ConfigStorage {
	storage.Root = normalizeStoragePath(storage.Root)
	storage.History = normalizeStoragePath(storage.History)
	storage.Artifacts = normalizeStoragePath(storage.Artifacts)
	if storage.Root == "" {
		storage.Root = defaults.Root
	}
	if storage.History == "" {
		storage.History = filepath.Join(storage.Root, "history")
	}
	if storage.Artifacts == "" {
		storage.Artifacts = storage.History
	}
	return storage
}

func normalizeStoragePath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return path
		}
		if path == "~" {
			return home
		}
		path = filepath.Join(home, path[2:])
	}
	if absolute, err := filepath.Abs(path); err == nil {
		return absolute
	}
	return path
}

func ensureStorageDirs(storage ConfigStorage) error {
	for _, path := range []string{
		storage.Root,
		storage.History,
		storage.Artifacts,
		filepath.Join(storage.History, "conversations"),
		filepath.Join(storage.History, "indexes"),
	} {
		if err := os.MkdirAll(path, 0755); err != nil {
			return err
		}
	}
	return nil
}

func writeChatConversation(config AppConfig, req ChatRequest, assistantContent, assistantThinking, model, reason string, tokens int, title string) (string, error) {
	now := time.Now()
	nowText := now.Format(time.RFC3339)
	conversationID := randomID("conv")
	conversationDir := conversationDir(config.Storage, now, conversationID)
	turnsDir := filepath.Join(conversationDir, "turns")
	artifactsDir := filepath.Join(conversationDir, "artifacts")
	if err := os.MkdirAll(turnsDir, 0755); err != nil {
		return "", err
	}
	if err := os.MkdirAll(artifactsDir, 0755); err != nil {
		return "", err
	}

	userPrompt := lastUserPrompt(req.Messages)
	conversation := HistoryConversation{
		SchemaVersion: 1,
		ID:            conversationID,
		Kind:          "chat",
		Title:         normalizeConversationTitle(title, userPrompt),
		CreatedAt:     nowText,
		UpdatedAt:     nowText,
		Provider: HistoryProvider{
			ID:      "ollama",
			BaseURL: config.Providers.Ollama.BaseURL,
		},
		Defaults: HistoryDefaults{
			ChatModel: req.Model,
			System:    req.System,
		},
		Stats: HistoryConversationStats{
			TurnCount:     2,
			ArtifactCount: countMessageImages([]ChatMessage{lastUserMessage(req.Messages)}),
		},
	}

	userTurn, assistantTurn, err := buildChatTurnPair(conversationID, 1, nowText, req, assistantContent, assistantThinking, model, reason, tokens, artifactsDir)
	if err != nil {
		return "", err
	}

	if err := writeJSONFile(filepath.Join(conversationDir, "conversation.json"), conversation); err != nil {
		return "", err
	}
	if err := writeJSONFile(filepath.Join(turnsDir, userTurn.ID+".json"), userTurn); err != nil {
		return "", err
	}
	if err := writeJSONFile(filepath.Join(turnsDir, assistantTurn.ID+".json"), assistantTurn); err != nil {
		return "", err
	}
	return conversationID, nil
}

func buildChatTurnPair(conversationID string, firstTurnNumber int, createdAt string, req ChatRequest, assistantContent, assistantThinking, model, reason string, tokens int, artifactsDir string) (HistoryTurn, HistoryTurn, error) {
	userContent, err := historyContentForMessage(lastUserMessage(req.Messages), artifactsDir, firstTurnNumber)
	if err != nil {
		return HistoryTurn{}, HistoryTurn{}, err
	}
	userTurn := HistoryTurn{
		SchemaVersion:  1,
		ID:             fmt.Sprintf("turn_%06d", firstTurnNumber),
		ConversationID: conversationID,
		CreatedAt:      createdAt,
		Kind:           "chat",
		Role:           "user",
		Content:        userContent,
		Request: map[string]any{
			"model": req.Model,
		},
	}
	if req.Options != nil {
		userTurn.Request["options"] = req.Options
	}
	if req.Think != nil {
		userTurn.Request["think"] = req.Think
	}

	assistantContents := []HistoryContent{{Type: "text", Text: assistantContent}}
	if strings.TrimSpace(assistantThinking) != "" {
		assistantContents = append(assistantContents, HistoryContent{Type: "thinking", Text: assistantThinking})
	}
	assistantTurn := HistoryTurn{
		SchemaVersion:  1,
		ID:             fmt.Sprintf("turn_%06d", firstTurnNumber+1),
		ConversationID: conversationID,
		CreatedAt:      createdAt,
		Kind:           "chat",
		Role:           "assistant",
		Model:          model,
		Content:        assistantContents,
		ProviderResponse: map[string]any{
			"doneReason": reason,
			"tokens":     tokens,
		},
	}
	return userTurn, assistantTurn, nil
}

func appendChatConversation(config AppConfig, req ChatRequest, assistantContent, assistantThinking, model, reason string, tokens int) (string, error) {
	conversationID := strings.TrimSpace(req.ConversationID)
	conversationPath, err := findConversationPath(config.Storage, conversationID)
	if err != nil {
		return "", err
	}

	var conversation HistoryConversation
	if err := readJSONFile(conversationPath, &conversation); err != nil {
		return "", err
	}
	if conversation.DeletedAt != "" {
		return "", fmt.Errorf("conversation %s is deleted", conversationID)
	}
	if conversation.Kind != "chat" {
		return "", fmt.Errorf("conversation %s is not a chat conversation", conversationID)
	}

	detail, err := getConversation(config.Storage, conversationID)
	if err != nil {
		return "", err
	}
	nextTurnNumber := len(detail.Turns) + 1
	nowText := time.Now().Format(time.RFC3339)
	artifactsDir := filepath.Join(filepath.Dir(conversationPath), "artifacts")
	userTurn, assistantTurn, err := buildChatTurnPair(conversationID, nextTurnNumber, nowText, req, assistantContent, assistantThinking, model, reason, tokens, artifactsDir)
	if err != nil {
		return "", err
	}
	turnsDir := filepath.Join(filepath.Dir(conversationPath), "turns")
	if err := os.MkdirAll(turnsDir, 0755); err != nil {
		return "", err
	}

	conversation.UpdatedAt = nowText
	conversation.Stats.TurnCount += 2
	conversation.Stats.ArtifactCount += countMessageImages([]ChatMessage{lastUserMessage(req.Messages)})
	if err := writeJSONFile(conversationPath, conversation); err != nil {
		return "", err
	}
	if err := writeJSONFile(filepath.Join(turnsDir, userTurn.ID+".json"), userTurn); err != nil {
		return "", err
	}
	if err := writeJSONFile(filepath.Join(turnsDir, assistantTurn.ID+".json"), assistantTurn); err != nil {
		return "", err
	}
	return conversationID, nil
}

func writeImageGenerationConversation(config AppConfig, req ImageGenerateRequest, payload ollamaGenerateResponse, images []string, raw string) (string, error) {
	now := time.Now()
	nowText := now.Format(time.RFC3339)
	conversationID := randomID("conv")
	conversationDir := conversationDir(config.Storage, now, conversationID)
	turnsDir := filepath.Join(conversationDir, "turns")
	artifactsDir := filepath.Join(conversationDir, "artifacts")
	if err := os.MkdirAll(turnsDir, 0755); err != nil {
		return "", err
	}
	if err := os.MkdirAll(artifactsDir, 0755); err != nil {
		return "", err
	}

	imageContents := make([]HistoryContent, 0, len(images))
	for index, image := range images {
		data, extension, err := decodeImagePayload(image)
		if err != nil {
			return "", err
		}
		artifactID := fmt.Sprintf("img_%06d", index+1)
		filename := artifactID + extension
		artifactPath := filepath.Join(artifactsDir, filename)
		if err := os.WriteFile(artifactPath, data, 0644); err != nil {
			return "", err
		}
		imageContents = append(imageContents, HistoryContent{
			Type:       "image",
			ArtifactID: artifactID,
			Path:       filepath.ToSlash(filepath.Join("artifacts", filename)),
			MimeType:   mediaTypeForExtension(extension),
			Width:      req.Width,
			Height:     req.Height,
		})
	}

	conversation := HistoryConversation{
		SchemaVersion: 1,
		ID:            conversationID,
		Kind:          "image_generation",
		Title:         titleFromPrompt(req.Prompt),
		CreatedAt:     nowText,
		UpdatedAt:     nowText,
		Provider: HistoryProvider{
			ID:      "ollama",
			BaseURL: config.Providers.Ollama.BaseURL,
		},
		Defaults: HistoryDefaults{
			ChatModel:  config.Providers.Ollama.Models.Chat,
			ImageModel: req.Model,
			System:     config.Prompts.System,
		},
		Stats: HistoryConversationStats{
			TurnCount:     2,
			ArtifactCount: len(imageContents),
		},
	}

	userTurn := HistoryTurn{
		SchemaVersion:  1,
		ID:             "turn_000001",
		ConversationID: conversationID,
		CreatedAt:      nowText,
		Kind:           "image_generation",
		Role:           "user",
		Content: []HistoryContent{
			{Type: "text", Text: req.Prompt},
		},
		Request: map[string]any{
			"prompt": req.Prompt,
			"width":  req.Width,
			"height": req.Height,
			"steps":  req.Steps,
		},
	}

	assistantTurn := HistoryTurn{
		SchemaVersion:  1,
		ID:             "turn_000002",
		ConversationID: conversationID,
		CreatedAt:      nowText,
		Kind:           "image_generation",
		Role:           "assistant",
		Model:          req.Model,
		Content:        imageContents,
		ProviderResponse: map[string]any{
			"done":       payload.Done,
			"rawCompact": raw,
		},
	}

	if err := writeJSONFile(filepath.Join(conversationDir, "conversation.json"), conversation); err != nil {
		return "", err
	}
	if err := writeJSONFile(filepath.Join(turnsDir, userTurn.ID+".json"), userTurn); err != nil {
		return "", err
	}
	if err := writeJSONFile(filepath.Join(turnsDir, assistantTurn.ID+".json"), assistantTurn); err != nil {
		return "", err
	}
	return conversationID, nil
}

func listConversations(storage ConfigStorage) ([]ConversationSummary, error) {
	root := filepath.Join(storage.History, "conversations")
	summaries := []ConversationSummary{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || filepath.Base(path) != "conversation.json" {
			return nil
		}
		var conversation HistoryConversation
		if err := readJSONFile(path, &conversation); err != nil {
			return err
		}
		if conversation.DeletedAt != "" {
			return nil
		}
		summaries = append(summaries, ConversationSummary{
			ID:            conversation.ID,
			Kind:          conversation.Kind,
			Title:         conversation.Title,
			CreatedAt:     conversation.CreatedAt,
			UpdatedAt:     conversation.UpdatedAt,
			TurnCount:     conversation.Stats.TurnCount,
			ArtifactCount: conversation.Stats.ArtifactCount,
		})
		return nil
	})
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []ConversationSummary{}, nil
		}
		return nil, err
	}
	sort.Slice(summaries, func(i, j int) bool {
		return summaries[i].UpdatedAt > summaries[j].UpdatedAt
	})
	return summaries, nil
}

func getConversation(storage ConfigStorage, conversationID string) (ConversationDetail, error) {
	conversationPath, err := findConversationPath(storage, conversationID)
	if err != nil {
		return ConversationDetail{}, err
	}

	var conversation HistoryConversation
	if err := readJSONFile(conversationPath, &conversation); err != nil {
		return ConversationDetail{}, err
	}
	if conversation.DeletedAt != "" {
		return ConversationDetail{}, fmt.Errorf("conversation %s is deleted", conversationID)
	}

	turnsDir := filepath.Join(filepath.Dir(conversationPath), "turns")
	turns := []HistoryTurn{}
	err = filepath.WalkDir(turnsDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || filepath.Ext(path) != ".json" {
			return nil
		}
		var turn HistoryTurn
		if err := readJSONFile(path, &turn); err != nil {
			return err
		}
		if turn.DeletedAt != "" {
			return nil
		}
		turn.Content = hydrateHistoryContent(filepath.Dir(conversationPath), turn.Content)
		turns = append(turns, turn)
		return nil
	})
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return ConversationDetail{}, err
		}
	}
	sort.Slice(turns, func(i, j int) bool {
		return turns[i].ID < turns[j].ID
	})
	return ConversationDetail{Conversation: conversation, Turns: turns}, nil
}

func deleteConversation(storage ConfigStorage, conversationID string) error {
	conversationPath, err := findConversationPath(storage, conversationID)
	if err != nil {
		return err
	}
	now := time.Now().Format(time.RFC3339)
	var conversation HistoryConversation
	if err := readJSONFile(conversationPath, &conversation); err != nil {
		return err
	}
	conversation.DeletedAt = now
	if err := writeJSONFile(conversationPath, conversation); err != nil {
		return err
	}
	tombstone := map[string]any{
		"schemaVersion":  1,
		"conversationId": conversationID,
		"deletedAt":      now,
		"reason":         "user_deleted",
	}
	return writeJSONFile(filepath.Join(filepath.Dir(conversationPath), "tombstone.json"), tombstone)
}

func purgeArchivedConversations(storage ConfigStorage) (PurgeArchivedResult, error) {
	root := filepath.Join(storage.History, "conversations")
	var archivedDirs []string
	deletedAssets := 0
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || filepath.Base(path) != "conversation.json" {
			return nil
		}
		var conversation HistoryConversation
		if err := readJSONFile(path, &conversation); err != nil {
			return err
		}
		if conversation.DeletedAt != "" {
			archivedDirs = append(archivedDirs, filepath.Dir(path))
			deletedAssets += countFiles(filepath.Join(filepath.Dir(path), "artifacts"))
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return PurgeArchivedResult{}, nil
		}
		return PurgeArchivedResult{}, err
	}

	for _, dir := range archivedDirs {
		if err := os.RemoveAll(dir); err != nil {
			return PurgeArchivedResult{}, err
		}
	}
	return PurgeArchivedResult{DeletedConversations: len(archivedDirs), DeletedAssets: deletedAssets}, nil
}

func countFiles(root string) int {
	count := 0
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return nil
		}
		count++
		return nil
	})
	return count
}

func updateConversationTitle(storage ConfigStorage, conversationID string, title string) (ConversationSummary, error) {
	conversationPath, err := findConversationPath(storage, conversationID)
	if err != nil {
		return ConversationSummary{}, err
	}

	var conversation HistoryConversation
	if err := readJSONFile(conversationPath, &conversation); err != nil {
		return ConversationSummary{}, err
	}
	if conversation.DeletedAt != "" {
		return ConversationSummary{}, fmt.Errorf("conversation %s is deleted", conversationID)
	}
	conversation.Title = normalizeConversationTitle(title, conversation.Title)
	conversation.UpdatedAt = time.Now().Format(time.RFC3339)
	if err := writeJSONFile(conversationPath, conversation); err != nil {
		return ConversationSummary{}, err
	}
	return ConversationSummary{
		ID:            conversation.ID,
		Kind:          conversation.Kind,
		Title:         conversation.Title,
		CreatedAt:     conversation.CreatedAt,
		UpdatedAt:     conversation.UpdatedAt,
		TurnCount:     conversation.Stats.TurnCount,
		ArtifactCount: conversation.Stats.ArtifactCount,
	}, nil
}

func findConversationPath(storage ConfigStorage, conversationID string) (string, error) {
	root := filepath.Join(storage.History, "conversations")
	var found string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || filepath.Base(path) != "conversation.json" {
			return nil
		}
		var conversation HistoryConversation
		if err := readJSONFile(path, &conversation); err != nil {
			return err
		}
		if conversation.ID == conversationID {
			found = path
			return filepath.SkipAll
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if found == "" {
		return "", fmt.Errorf("conversation %s not found", conversationID)
	}
	return found, nil
}

func conversationDir(storage ConfigStorage, createdAt time.Time, conversationID string) string {
	return filepath.Join(
		storage.History,
		"conversations",
		createdAt.Format("2006"),
		createdAt.Format("01"),
		conversationID,
	)
}

func writeJSONFile(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tempPath := path + ".tmp"
	if err := os.WriteFile(tempPath, data, 0644); err != nil {
		return err
	}
	return os.Rename(tempPath, path)
}

func readJSONFile(path string, target any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, target)
}

func hydrateHistoryContent(conversationDir string, contents []HistoryContent) []HistoryContent {
	hydrated := make([]HistoryContent, 0, len(contents))
	for _, content := range contents {
		if content.Type == "image" && content.Path != "" && !strings.HasPrefix(content.Path, "data:image/") {
			path := filepath.Join(conversationDir, filepath.FromSlash(content.Path))
			if data, err := os.ReadFile(path); err == nil && isImageBytes(data) {
				content.Text = "data:" + content.MimeType + ";base64," + base64.StdEncoding.EncodeToString(data)
			}
		}
		hydrated = append(hydrated, content)
	}
	return hydrated
}

func randomID(prefix string) string {
	var bytes [12]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
	}
	return prefix + "_" + hex.EncodeToString(bytes[:])
}

func titleFromPrompt(prompt string) string {
	prompt = strings.Join(strings.Fields(prompt), " ")
	if prompt == "" {
		return "Untitled"
	}
	return compactString(prompt, 72)
}

func cleanGeneratedTitle(title string) string {
	title = strings.TrimSpace(title)
	title = strings.TrimPrefix(title, "#")
	title = strings.TrimSpace(title)
	for _, prefix := range []string{"Title:", "title:", "Conversation title:", "conversation title:"} {
		title = strings.TrimSpace(strings.TrimPrefix(title, prefix))
	}
	title = strings.Trim(title, "\"'`“”‘’")
	title = strings.Join(strings.Fields(title), " ")
	return compactString(title, 72)
}

func normalizeConversationTitle(title string, fallback string) string {
	title = cleanGeneratedTitle(title)
	if title != "" {
		return title
	}
	return titleFromPrompt(fallback)
}

func lastUserMessage(messages []ChatMessage) ChatMessage {
	for index := len(messages) - 1; index >= 0; index-- {
		if messages[index].Role == "user" {
			return messages[index]
		}
	}
	if len(messages) == 0 {
		return ChatMessage{}
	}
	return messages[len(messages)-1]
}

func lastUserPrompt(messages []ChatMessage) string {
	return lastUserMessage(messages).Content
}

func historyContentForMessage(message ChatMessage, artifactsDir string, turnNumber int) ([]HistoryContent, error) {
	contents := []HistoryContent{}
	if strings.TrimSpace(message.Content) != "" {
		contents = append(contents, HistoryContent{Type: "text", Text: message.Content})
	}
	if len(message.Images) > 0 {
		if err := os.MkdirAll(artifactsDir, 0755); err != nil {
			return nil, err
		}
	}
	for index, image := range message.Images {
		data, extension, err := decodeImagePayload(image)
		if err != nil {
			return nil, err
		}
		artifactID := fmt.Sprintf("input_%06d_%06d", turnNumber, index+1)
		filename := artifactID + extension
		artifactPath := filepath.Join(artifactsDir, filename)
		if err := os.WriteFile(artifactPath, data, 0644); err != nil {
			return nil, err
		}
		contents = append(contents, HistoryContent{
			Type:       "image",
			ArtifactID: artifactID,
			Path:       filepath.ToSlash(filepath.Join("artifacts", filename)),
			MimeType:   mediaTypeForExtension(extension),
		})
	}
	if len(contents) == 0 {
		return []HistoryContent{{Type: "text", Text: ""}}, nil
	}
	return contents, nil
}

func countMessageImages(messages []ChatMessage) int {
	count := 0
	for _, message := range messages {
		count += len(message.Images)
	}
	return count
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

func mediaTypeForExtension(extension string) string {
	switch strings.ToLower(extension) {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".webp":
		return "image/webp"
	case ".gif":
		return "image/gif"
	default:
		return "image/png"
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
