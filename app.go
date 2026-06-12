package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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

// defaultOllamaNumCtx is sent as num_ctx on every chat call so the context
// window is explicit and identical across calls (Ollama reloads the model
// when num_ctx changes between requests).
const defaultOllamaNumCtx = 8192

type App struct {
	ctx            context.Context
	client         *http.Client
	baseURL        string
	configMu       sync.Mutex
	streams        map[string]context.CancelFunc
	streamsMu      sync.Mutex
	permissions    map[string]chan bool
	permissionsMu  sync.Mutex
	toolPermission func(context.Context, ToolPermissionRequestEvent) bool
}

func NewApp() *App {
	app := &App{
		client: &http.Client{
			Timeout: 10 * time.Minute,
		},
		baseURL:     defaultOllamaBaseURL,
		streams:     map[string]context.CancelFunc{},
		permissions: map[string]chan bool{},
	}
	app.toolPermission = app.requestToolPermission
	return app
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
	Tools      ConfigTools      `json:"tools"`
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
	NumCtx  int                `json:"numCtx"`
}

type ConfigOllamaModels struct {
	Chat    string `json:"chat"`
	Harness string `json:"harness"`
	Image   string `json:"image"`
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

type ConfigTools struct {
	Filesystem ConfigFilesystemTool `json:"filesystem"`
}

type ConfigFilesystemTool struct {
	Root            string   `json:"root"`
	MaxOutputBytes  int      `json:"maxOutputBytes"`
	TimeoutMS       int      `json:"timeoutMs"`
	AllowedCommands []string `json:"allowedCommands"`
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
	Name            string   `json:"name"`
	ModifiedAt      string   `json:"modifiedAt,omitempty"`
	Size            int64    `json:"size,omitempty"`
	Family          string   `json:"family,omitempty"`
	Parameter       string   `json:"parameter,omitempty"`
	Capabilities    []string `json:"capabilities,omitempty"`
	ImageGeneration bool     `json:"imageGeneration,omitempty"`
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

type ollamaShowResponse struct {
	Capabilities []string       `json:"capabilities"`
	ModelInfo    map[string]any `json:"model_info"`
	Details      struct {
		Family        string `json:"family"`
		ParameterSize string `json:"parameter_size"`
	} `json:"details"`
}

type ChatMessage struct {
	Role    string   `json:"role"`
	Content string   `json:"content"`
	Images  []string `json:"images,omitempty"`
}

type ChatRequest struct {
	RequestID      string `json:"requestID,omitempty"`
	ConversationID string `json:"conversationId,omitempty"`
	turnStarted    bool
	BaseURL        string         `json:"baseURL,omitempty"`
	Model          string         `json:"model"`
	SelectedModel  string         `json:"selectedModel,omitempty"`
	System         string         `json:"system,omitempty"`
	Messages       []ChatMessage  `json:"messages"`
	Think          any            `json:"think,omitempty"`
	Options        map[string]any `json:"options,omitempty"`
	Format         any            `json:"format,omitempty"`
}

type ChatStreamStart struct {
	RequestID      string `json:"requestID"`
	ConversationID string `json:"conversationId"`
}

type ChatStreamEvent struct {
	RequestID      string   `json:"requestID"`
	Content        string   `json:"content,omitempty"`
	Thinking       string   `json:"thinking,omitempty"`
	Images         []string `json:"images,omitempty"`
	Done           bool     `json:"done"`
	Error          string   `json:"error,omitempty"`
	Model          string   `json:"model,omitempty"`
	Reason         string   `json:"reason,omitempty"`
	Tokens         int      `json:"tokens,omitempty"`
	ConversationID string   `json:"conversationId,omitempty"`
}

type ToolPermissionRequestEvent struct {
	ID             string   `json:"id"`
	RequestID      string   `json:"requestID,omitempty"`
	ConversationID string   `json:"conversationId,omitempty"`
	ToolName       string   `json:"toolName"`
	Action         string   `json:"action"`
	Summary        string   `json:"summary"`
	Command        []string `json:"command,omitempty"`
	Cwd            string   `json:"cwd,omitempty"`
	Path           string   `json:"path,omitempty"`
	ContentPreview string   `json:"contentPreview,omitempty"`
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
		Role     string `json:"role"`
		Content  string `json:"content"`
		Thinking string `json:"thinking"`
	} `json:"message"`
	Response   string `json:"response"`
	Done       bool   `json:"done"`
	DoneReason string `json:"done_reason"`
	EvalCount  int    `json:"eval_count"`
	Error      string `json:"error"`
}

type ImageGenerateRequest struct {
	RequestID      string   `json:"requestID,omitempty"`
	ConversationID string   `json:"conversationId,omitempty"`
	BaseURL        string   `json:"baseURL,omitempty"`
	Model          string   `json:"model"`
	Prompt         string   `json:"prompt"`
	Width          int      `json:"width,omitempty"`
	Height         int      `json:"height,omitempty"`
	Steps          int      `json:"steps,omitempty"`
	Images         []string `json:"images,omitempty"`
}

type ImageGenerateResponse struct {
	Model          string   `json:"model,omitempty"`
	Text           string   `json:"text,omitempty"`
	Images         []string `json:"images"`
	Raw            string   `json:"raw,omitempty"`
	Error          string   `json:"error,omitempty"`
	ConversationID string   `json:"conversationId,omitempty"`
}

type ImageGenerateEvent struct {
	RequestID      string   `json:"requestID"`
	Done           bool     `json:"done"`
	Model          string   `json:"model,omitempty"`
	Text           string   `json:"text,omitempty"`
	Images         []string `json:"images,omitempty"`
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

type HarnessRun struct {
	ID             string                `json:"id"`
	Mode           string                `json:"mode"`
	Status         string                `json:"status"`
	StartedAt      string                `json:"startedAt"`
	CompletedAt    string                `json:"completedAt,omitempty"`
	DurationMS     int64                 `json:"durationMs,omitempty"`
	RequestID      string                `json:"requestId,omitempty"`
	ConversationID string                `json:"conversationId,omitempty"`
	Loop           HarnessLoop           `json:"loop"`
	Skill          *HarnessSkillDecision `json:"skill,omitempty"`
	Steps          []HarnessStep         `json:"steps"`
}

type HarnessLoop struct {
	MaxSteps      int    `json:"maxSteps"`
	MaxWallTimeMS int64  `json:"maxWallTimeMs"`
	Iterations    int    `json:"iterations"`
	StopReason    string `json:"stopReason,omitempty"`
}

type HarnessStep struct {
	ID          string                `json:"id"`
	Kind        string                `json:"kind"`
	Iteration   int                   `json:"iteration,omitempty"`
	Provider    string                `json:"provider"`
	Model       string                `json:"model"`
	Status      string                `json:"status"`
	StartedAt   string                `json:"startedAt"`
	CompletedAt string                `json:"completedAt,omitempty"`
	DurationMS  int64                 `json:"durationMs,omitempty"`
	Decision    string                `json:"decision,omitempty"`
	DoneReason  string                `json:"doneReason,omitempty"`
	Summary     string                `json:"summary,omitempty"`
	Error       string                `json:"error,omitempty"`
	Tokens      int                   `json:"tokens,omitempty"`
	Tools       []HarnessToolActivity `json:"tools,omitempty"`
}

type HarnessToolActivity struct {
	Name          string   `json:"name"`
	Status        string   `json:"status"`
	Path          string   `json:"path,omitempty"`
	Command       []string `json:"command,omitempty"`
	ExitCode      int      `json:"exitCode,omitempty"`
	StdoutPreview string   `json:"stdoutPreview,omitempty"`
	StderrPreview string   `json:"stderrPreview,omitempty"`
	DurationMS    int64    `json:"durationMs,omitempty"`
	Error         string   `json:"error,omitempty"`
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
	return a.ollamaClient(baseURL).Check(context.Background())
}

func (a *App) ListModels(baseURL string) ([]OllamaModel, error) {
	return a.ollamaClient(baseURL).ListModels(context.Background())
}

func (a *App) ChooseToolWorkspace(current string) (string, error) {
	current = normalizeStoragePath(current)
	if current == "" {
		current = defaultAppConfig().Tools.Filesystem.Root
	}
	if err := os.MkdirAll(current, 0755); err != nil {
		return "", err
	}
	path, err := runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{
		Title:                "Choose Atelier workspace",
		DefaultDirectory:     current,
		CanCreateDirectories: true,
	})
	if err != nil {
		return "", err
	}
	if path == "" {
		return "", nil
	}
	return normalizeStoragePath(path), nil
}

func (a *App) ResolveToolPermission(permissionID string, approved bool) error {
	permissionID = strings.TrimSpace(permissionID)
	if permissionID == "" {
		return errors.New("permission id is required")
	}
	a.permissionsMu.Lock()
	response, ok := a.permissions[permissionID]
	if ok {
		delete(a.permissions, permissionID)
	}
	a.permissionsMu.Unlock()
	if !ok {
		return fmt.Errorf("permission request %q is not pending", permissionID)
	}
	select {
	case response <- approved:
	default:
	}
	return nil
}

func (a *App) RunToolCommand(req ToolCommandRequest) (ToolCommandResult, error) {
	result, err := a.ExecuteTool(ToolExecutionRequest{
		Name: "run_command",
		Call: HarnessToolCall{
			Name:      "run_command",
			Command:   req.Command,
			Args:      req.Args,
			Cwd:       req.Cwd,
			Env:       req.Env,
			TimeoutMS: req.TimeoutMS,
		},
		Source: "api",
	})
	if err != nil {
		return ToolCommandResult{}, err
	}
	if result.Status == "denied" {
		return ToolCommandResult{}, errors.New(result.Error)
	}
	output, ok := result.Result.(ToolCommandResult)
	if !ok {
		return ToolCommandResult{}, fmt.Errorf("run_command returned %T", result.Result)
	}
	if result.Status == "failed" {
		return output, errors.New(result.Error)
	}
	return output, nil
}

func (a *App) ListToolFiles(req ToolFileListRequest) (ToolFileListResult, error) {
	config, err := loadAppConfig()
	if err != nil {
		return ToolFileListResult{}, err
	}
	if err := ensureStorageDirs(config.Storage); err != nil {
		return ToolFileListResult{}, err
	}
	return newFilesystemToolLayer(config.Tools.Filesystem).ListFiles(req)
}

func (a *App) ReadToolFile(req ToolFileReadRequest) (ToolFileReadResult, error) {
	config, err := loadAppConfig()
	if err != nil {
		return ToolFileReadResult{}, err
	}
	if err := ensureStorageDirs(config.Storage); err != nil {
		return ToolFileReadResult{}, err
	}
	return newFilesystemToolLayer(config.Tools.Filesystem).ReadFile(req)
}

func (a *App) WriteToolFile(req ToolFileWriteRequest) (ToolFileWriteResult, error) {
	result, err := a.ExecuteTool(ToolExecutionRequest{
		Name: "write_file",
		Call: HarnessToolCall{
			Name:      "write_file",
			Path:      req.Path,
			Content:   req.Content,
			Append:    req.Append,
			Overwrite: req.Overwrite,
		},
		Source: "api",
	})
	if err != nil {
		return ToolFileWriteResult{}, err
	}
	if result.Status == "denied" {
		return ToolFileWriteResult{}, errors.New(result.Error)
	}
	output, ok := result.Result.(ToolFileWriteResult)
	if !ok {
		return ToolFileWriteResult{}, fmt.Errorf("write_file returned %T", result.Result)
	}
	if result.Status == "failed" {
		return output, errors.New(result.Error)
	}
	return output, nil
}

func (a *App) ExecuteTool(req ToolExecutionRequest) (HarnessToolResult, error) {
	config, err := loadAppConfig()
	if err != nil {
		return HarnessToolResult{}, err
	}
	if err := ensureStorageDirs(config.Storage); err != nil {
		return HarnessToolResult{}, err
	}
	result := newToolGateway(a, config).Execute(context.Background(), req)
	return result, nil
}

func (a *App) StreamChat(req ChatRequest) (*ChatStreamStart, error) {
	if len(req.Messages) == 0 {
		return nil, errors.New("at least one message is required")
	}

	config, err := loadAppConfig()
	if err != nil {
		return nil, err
	}
	if err := ensureStorageDirs(config.Storage); err != nil {
		return nil, err
	}
	engine := newHarnessEngine(config, a)
	req = engine.chatRequestForHarness(req)
	if strings.TrimSpace(req.Model) == "" {
		return nil, errors.New("model is required")
	}

	requestID := strings.TrimSpace(req.RequestID)
	if requestID == "" {
		requestID = fmt.Sprintf("chat-%d", time.Now().UnixNano())
	}

	conversationID, err := engine.StartChatTurn(req)
	if err != nil {
		return nil, err
	}
	req.ConversationID = conversationID
	req.turnStarted = true

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
		engine.RunChatStream(streamCtx, requestID, req)
	}()

	return &ChatStreamStart{RequestID: requestID, ConversationID: conversationID}, nil
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
	return a.generateImage(context.Background(), req, "")
}

func (a *App) StartImageGeneration(req ImageGenerateRequest) (string, error) {
	if strings.TrimSpace(req.Model) == "" {
		return "", errors.New("image model is required")
	}
	if strings.TrimSpace(req.Prompt) == "" {
		return "", errors.New("prompt is required")
	}

	requestID := strings.TrimSpace(req.RequestID)
	if requestID == "" {
		requestID = fmt.Sprintf("image-%d", time.Now().UnixNano())
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
		a.runImageGeneration(streamCtx, requestID, req)
	}()

	return requestID, nil
}

func (a *App) runImageGeneration(ctx context.Context, requestID string, req ImageGenerateRequest) {
	config, err := loadAppConfig()
	if err != nil {
		a.emitImageEvent(ImageGenerateEvent{RequestID: requestID, Error: err.Error(), Done: true})
		return
	}
	if err := ensureStorageDirs(config.Storage); err != nil {
		a.emitImageEvent(ImageGenerateEvent{RequestID: requestID, Error: err.Error(), Done: true})
		return
	}

	conversationID, err := writePendingImageGenerationConversation(config, req)
	if err != nil {
		a.emitImageEvent(ImageGenerateEvent{RequestID: requestID, Error: err.Error(), Done: true})
		return
	}
	req.ConversationID = conversationID
	a.emitImageEvent(ImageGenerateEvent{RequestID: requestID, ConversationID: conversationID})

	result, err := a.generateImage(ctx, req, conversationID)
	if err != nil {
		a.emitImageEvent(ImageGenerateEvent{RequestID: requestID, ConversationID: conversationID, Error: err.Error(), Done: true})
		return
	}
	a.emitImageEvent(ImageGenerateEvent{
		RequestID:      requestID,
		Done:           true,
		Model:          result.Model,
		Text:           result.Text,
		Images:         result.Images,
		Raw:            result.Raw,
		ConversationID: result.ConversationID,
	})
}

func (a *App) generateImage(ctx context.Context, req ImageGenerateRequest, conversationID string) (*ImageGenerateResponse, error) {
	payload, raw, err := a.ollamaClient(req.BaseURL).GenerateImage(ctx, req)
	if err != nil {
		return nil, err
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
	if strings.TrimSpace(conversationID) == "" {
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
	} else {
		config, err := loadAppConfig()
		if err != nil {
			return nil, err
		}
		if err := appendImageGenerationResult(config, conversationID, req, payload, images, compactRawResponse(raw)); err != nil {
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

func (a *App) emitImageEvent(event ImageGenerateEvent) {
	if a.ctx != nil {
		runtime.EventsEmit(a.ctx, "ollama:image:result", event)
	}
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
	config, err := loadAppConfig()
	if err != nil {
		a.emitChatEvent(ChatStreamEvent{RequestID: requestID, Error: err.Error(), Done: true})
		return
	}
	if err := ensureStorageDirs(config.Storage); err != nil {
		a.emitChatEvent(ChatStreamEvent{RequestID: requestID, Error: err.Error(), Done: true})
		return
	}
	newHarnessEngine(config, a).RunChatStream(ctx, requestID, req)
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
	run := fallbackHarnessRun(req.Model, reason, tokens)
	return newHarnessEngine(config).SaveChatTurn(req, assistantContent, assistantThinking, model, reason, tokens, title, run)
}

func (a *App) generateConversationTitle(config AppConfig, req ChatRequest, assistantContent string) string {
	userPrompt := lastUserPrompt(req.Messages)
	fallback := titleFromPrompt(userPrompt)
	if strings.TrimSpace(req.Model) == "" || strings.TrimSpace(userPrompt) == "" {
		return fallback
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	req.Options = withNumCtx(req.Options, ollamaNumCtx(config))
	title, err := a.ollamaClient(req.BaseURL).GenerateChatTitle(ctx, req, userPrompt, assistantContent)
	if err != nil {
		return fallback
	}
	return title
}

func (a *App) emitChatEvent(event ChatStreamEvent) {
	if a.ctx != nil {
		runtime.EventsEmit(a.ctx, "ollama:chat:chunk", event)
	}
}

func (a *App) requestToolPermission(ctx context.Context, event ToolPermissionRequestEvent) bool {
	if a.ctx == nil {
		// No UI is attached, so nobody can approve: fail closed.
		return false
	}
	if strings.TrimSpace(event.ID) == "" {
		event.ID = randomID("permission")
	}
	response := make(chan bool, 1)
	a.permissionsMu.Lock()
	a.permissions[event.ID] = response
	a.permissionsMu.Unlock()
	runtime.EventsEmit(a.ctx, "atelier:tool-permission", event)

	select {
	case approved := <-response:
		return approved
	case <-ctx.Done():
		a.permissionsMu.Lock()
		delete(a.permissions, event.ID)
		a.permissionsMu.Unlock()
		return false
	case <-time.After(2 * time.Minute):
		a.permissionsMu.Lock()
		delete(a.permissions, event.ID)
		a.permissionsMu.Unlock()
		return false
	}
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

func (a *App) ollamaClient(baseURL string) OllamaClient {
	return newOllamaClient(a.client, a.resolveBaseURL(baseURL))
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
					Chat:    "mistral-small3.1:latest",
					Harness: "mistral-small3.1:latest",
					Image:   "x/z-image-turbo:latest",
				},
				NumCtx: defaultOllamaNumCtx,
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
		Tools: ConfigTools{
			Filesystem: ConfigFilesystemTool{
				Root:            defaultDocumentsRoot(),
				MaxOutputBytes:  64 * 1024,
				TimeoutMS:       defaultToolTimeoutMS,
				AllowedCommands: defaultFilesystemToolAllowedCommands(),
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
	if strings.TrimSpace(config.Providers.Ollama.Models.Harness) == "" {
		config.Providers.Ollama.Models.Harness = config.Providers.Ollama.Models.Chat
	}
	if strings.TrimSpace(config.Providers.Ollama.Models.Image) == "" {
		config.Providers.Ollama.Models.Image = defaults.Providers.Ollama.Models.Image
	}
	if config.Providers.Ollama.NumCtx <= 0 {
		config.Providers.Ollama.NumCtx = defaults.Providers.Ollama.NumCtx
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
	config.Tools = mergeToolsConfig(config.Tools, defaults.Tools)
	if config.UI.Mode != "chat" && config.UI.Mode != "image" {
		config.UI.Mode = defaults.UI.Mode
	}
	return config
}

func mergeToolsConfig(tools ConfigTools, defaults ConfigTools) ConfigTools {
	tools.Filesystem.Root = normalizeStoragePath(tools.Filesystem.Root)
	if tools.Filesystem.Root == "" {
		tools.Filesystem.Root = defaults.Filesystem.Root
	}
	if tools.Filesystem.MaxOutputBytes <= 0 {
		tools.Filesystem.MaxOutputBytes = defaults.Filesystem.MaxOutputBytes
	}
	if tools.Filesystem.TimeoutMS <= 0 {
		tools.Filesystem.TimeoutMS = defaults.Filesystem.TimeoutMS
	}
	if len(tools.Filesystem.AllowedCommands) == 0 {
		tools.Filesystem.AllowedCommands = append([]string{}, defaults.Filesystem.AllowedCommands...)
	}
	return tools
}

func defaultFilesystemToolAllowedCommands() []string {
	return []string{
		"cat",
		"echo",
		"find",
		"grep",
		"head",
		"ls",
		"pwd",
		"rg",
		"tail",
		"wc",
	}
}

func defaultStorageRoot() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", ".atelier")
	}
	return filepath.Join(home, ".atelier")
}

func defaultDocumentsRoot() string {
	if path := strings.TrimSpace(os.Getenv("XDG_DOCUMENTS_DIR")); path != "" {
		path = strings.Trim(path, `"`)
		path = strings.ReplaceAll(path, "$HOME", userHomeFallback())
		return normalizeStoragePath(path)
	}
	return filepath.Join(userHomeFallback(), "Documents")
}

func userHomeFallback() string {
	home, err := os.UserHomeDir()
	if err == nil && home != "" {
		return home
	}
	if home := strings.TrimSpace(os.Getenv("USERPROFILE")); home != "" {
		return home
	}
	if home := strings.TrimSpace(os.Getenv("HOME")); home != "" {
		return home
	}
	return "."
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

func writeChatConversation(config AppConfig, req ChatRequest, assistantContent, assistantThinking, model, reason string, tokens int, title string, run ...HarnessRun) (string, error) {
	now := time.Now()
	nowText := now.Format(time.RFC3339)
	store := newHistoryStore(config.Storage)
	workspace, err := store.newWorkspace(now)
	if err != nil {
		return "", err
	}

	userPrompt := lastUserPrompt(req.Messages)
	conversation := HistoryConversation{
		SchemaVersion: 1,
		ID:            workspace.ID,
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

	userTurn, assistantTurn, err := buildChatTurnPair(workspace.ID, 1, nowText, req, assistantContent, assistantThinking, model, reason, tokens, workspace.ArtifactsDir, firstHarnessRun(model, reason, tokens, run))
	if err != nil {
		return "", err
	}

	if err := store.writeSnapshot(workspace, conversation, userTurn, assistantTurn); err != nil {
		return "", err
	}
	return workspace.ID, nil
}

func writePendingChatConversation(config AppConfig, req ChatRequest) (string, error) {
	now := time.Now()
	nowText := now.Format(time.RFC3339)
	store := newHistoryStore(config.Storage)
	workspace, err := store.newWorkspace(now)
	if err != nil {
		return "", err
	}

	userPrompt := lastUserPrompt(req.Messages)
	conversation := HistoryConversation{
		SchemaVersion: 1,
		ID:            workspace.ID,
		Kind:          "chat",
		Title:         titleFromPrompt(userPrompt),
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
			TurnCount:     1,
			ArtifactCount: countMessageImages([]ChatMessage{lastUserMessage(req.Messages)}),
		},
	}
	userTurn, err := buildChatUserTurn(workspace.ID, 1, nowText, req, workspace.ArtifactsDir)
	if err != nil {
		return "", err
	}

	if err := store.writeSnapshot(workspace, conversation, userTurn); err != nil {
		return "", err
	}
	return workspace.ID, nil
}

func buildChatTurnPair(conversationID string, firstTurnNumber int, createdAt string, req ChatRequest, assistantContent, assistantThinking, model, reason string, tokens int, artifactsDir string, run HarnessRun) (HistoryTurn, HistoryTurn, error) {
	userTurn, err := buildChatUserTurn(conversationID, firstTurnNumber, createdAt, req, artifactsDir)
	if err != nil {
		return HistoryTurn{}, HistoryTurn{}, err
	}
	assistantTurn := buildChatAssistantTurn(conversationID, firstTurnNumber+1, createdAt, assistantContent, assistantThinking, model, reason, tokens, run)
	return userTurn, assistantTurn, nil
}

func buildChatUserTurn(conversationID string, turnNumber int, createdAt string, req ChatRequest, artifactsDir string) (HistoryTurn, error) {
	userContent, err := historyContentForMessage(lastUserMessage(req.Messages), artifactsDir, turnNumber)
	if err != nil {
		return HistoryTurn{}, err
	}
	userTurn := HistoryTurn{
		SchemaVersion:  1,
		ID:             fmt.Sprintf("turn_%06d", turnNumber),
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
	if selectedModel := strings.TrimSpace(req.SelectedModel); selectedModel != "" && selectedModel != req.Model {
		userTurn.Request["selectedModel"] = selectedModel
	}
	return userTurn, nil
}

func buildChatAssistantTurn(conversationID string, turnNumber int, createdAt string, assistantContent, assistantThinking, model, reason string, tokens int, run HarnessRun) HistoryTurn {
	assistantContents := []HistoryContent{{Type: "text", Text: assistantContent}}
	if strings.TrimSpace(assistantThinking) != "" {
		assistantContents = append(assistantContents, HistoryContent{Type: "thinking", Text: assistantThinking})
	}
	providerResponse := map[string]any{
		"doneReason": reason,
		"harnessRun": run,
		"tokens":     tokens,
	}
	if run.Skill != nil {
		providerResponse["skill"] = run.Skill
	}
	return HistoryTurn{
		SchemaVersion:    1,
		ID:               fmt.Sprintf("turn_%06d", turnNumber),
		ConversationID:   conversationID,
		CreatedAt:        createdAt,
		Kind:             "chat",
		Role:             "assistant",
		Model:            model,
		Content:          assistantContents,
		ProviderResponse: providerResponse,
	}
}

func appendChatConversation(config AppConfig, req ChatRequest, assistantContent, assistantThinking, model, reason string, tokens int, run ...HarnessRun) (string, error) {
	conversationID := strings.TrimSpace(req.ConversationID)
	store := newHistoryStore(config.Storage)
	loaded, err := store.loadForAppend(conversationID, "chat", "a chat")
	if err != nil {
		return "", err
	}
	nowText := time.Now().Format(time.RFC3339)
	userTurn, assistantTurn, err := buildChatTurnPair(conversationID, loaded.NextTurnNumber, nowText, req, assistantContent, assistantThinking, model, reason, tokens, loaded.ArtifactsDir, firstHarnessRun(model, reason, tokens, run))
	if err != nil {
		return "", err
	}

	loaded.Conversation.UpdatedAt = nowText
	loaded.Conversation.Stats.TurnCount += 2
	loaded.Conversation.Stats.ArtifactCount += countMessageImages([]ChatMessage{lastUserMessage(req.Messages)})
	if err := store.writeConversation(loaded.Path, loaded.Conversation); err != nil {
		return "", err
	}
	if err := store.writeTurn(loaded.TurnsDir, userTurn); err != nil {
		return "", err
	}
	if err := store.writeTurn(loaded.TurnsDir, assistantTurn); err != nil {
		return "", err
	}
	return conversationID, nil
}

func appendChatUserTurn(config AppConfig, req ChatRequest) (string, error) {
	conversationID := strings.TrimSpace(req.ConversationID)
	store := newHistoryStore(config.Storage)
	loaded, err := store.loadForAppend(conversationID, "chat", "a chat")
	if err != nil {
		return "", err
	}
	nowText := time.Now().Format(time.RFC3339)
	userTurn, err := buildChatUserTurn(conversationID, loaded.NextTurnNumber, nowText, req, loaded.ArtifactsDir)
	if err != nil {
		return "", err
	}

	loaded.Conversation.UpdatedAt = nowText
	loaded.Conversation.Stats.TurnCount++
	loaded.Conversation.Stats.ArtifactCount += countMessageImages([]ChatMessage{lastUserMessage(req.Messages)})
	if err := store.writeConversation(loaded.Path, loaded.Conversation); err != nil {
		return "", err
	}
	if err := store.writeTurn(loaded.TurnsDir, userTurn); err != nil {
		return "", err
	}
	return conversationID, nil
}

func appendChatAssistantTurn(config AppConfig, conversationID, assistantContent, assistantThinking, model, reason string, tokens int, run HarnessRun) error {
	store := newHistoryStore(config.Storage)
	loaded, err := store.loadForAppend(conversationID, "chat", "a chat")
	if err != nil {
		return err
	}
	nowText := time.Now().Format(time.RFC3339)
	assistantTurn := buildChatAssistantTurn(conversationID, loaded.NextTurnNumber, nowText, assistantContent, assistantThinking, model, reason, tokens, run)

	loaded.Conversation.UpdatedAt = nowText
	loaded.Conversation.Stats.TurnCount++
	if err := store.writeConversation(loaded.Path, loaded.Conversation); err != nil {
		return err
	}
	return store.writeTurn(loaded.TurnsDir, assistantTurn)
}

func appendChatAssistantTurnWithImages(config AppConfig, conversationID, assistantContent, model, reason string, images []string, raw string, run HarnessRun, imageReq ImageGenerateRequest) error {
	store := newHistoryStore(config.Storage)
	loaded, err := store.loadForAppend(conversationID, "chat", "a chat")
	if err != nil {
		return err
	}
	nowText := time.Now().Format(time.RFC3339)
	imageContents, err := writeChatImageArtifacts(loaded.ArtifactsDir, imageReq, images, loaded.NextTurnNumber)
	if err != nil {
		return err
	}
	contents := []HistoryContent{{Type: "text", Text: assistantContent}}
	contents = append(contents, imageContents...)
	assistantTurn := HistoryTurn{
		SchemaVersion:  1,
		ID:             fmt.Sprintf("turn_%06d", loaded.NextTurnNumber),
		ConversationID: conversationID,
		CreatedAt:      nowText,
		Kind:           "chat",
		Role:           "assistant",
		Model:          model,
		Content:        contents,
		ProviderResponse: map[string]any{
			"doneReason": reason,
			"harnessRun": run,
			"tool": map[string]any{
				"name":       "image_generation",
				"model":      model,
				"imageCount": len(imageContents),
				"rawCompact": raw,
			},
		},
	}

	loaded.Conversation.UpdatedAt = nowText
	loaded.Conversation.Stats.TurnCount++
	loaded.Conversation.Stats.ArtifactCount += len(imageContents)
	if err := store.writeConversation(loaded.Path, loaded.Conversation); err != nil {
		return err
	}
	return store.writeTurn(loaded.TurnsDir, assistantTurn)
}

func writeImageGenerationConversation(config AppConfig, req ImageGenerateRequest, payload ollamaGenerateResponse, images []string, raw string) (string, error) {
	now := time.Now()
	nowText := now.Format(time.RFC3339)
	store := newHistoryStore(config.Storage)
	workspace, err := store.newWorkspace(now)
	if err != nil {
		return "", err
	}

	imageContents, err := writeImageArtifacts(workspace.ArtifactsDir, req, images)
	if err != nil {
		return "", err
	}

	conversation := HistoryConversation{
		SchemaVersion: 1,
		ID:            workspace.ID,
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

	userContent, err := imageGenerationUserContent(workspace.ArtifactsDir, req)
	if err != nil {
		return "", err
	}
	userTurn := buildImageGenerationUserTurn(workspace.ID, "turn_000001", nowText, req, userContent)

	assistantTurn := HistoryTurn{
		SchemaVersion:  1,
		ID:             "turn_000002",
		ConversationID: workspace.ID,
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

	if err := store.writeSnapshot(workspace, conversation, userTurn, assistantTurn); err != nil {
		return "", err
	}
	return workspace.ID, nil
}

func writeImageArtifacts(artifactsDir string, req ImageGenerateRequest, images []string) ([]HistoryContent, error) {
	if err := os.MkdirAll(artifactsDir, 0755); err != nil {
		return nil, err
	}
	imageContents := make([]HistoryContent, 0, len(images))
	for index, image := range images {
		data, extension, err := decodeImagePayload(image)
		if err != nil {
			return nil, err
		}
		artifactID := fmt.Sprintf("img_%06d", index+1)
		filename := artifactID + extension
		artifactPath := filepath.Join(artifactsDir, filename)
		if err := os.WriteFile(artifactPath, data, 0644); err != nil {
			return nil, err
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
	return imageContents, nil
}

func writeChatImageArtifacts(artifactsDir string, req ImageGenerateRequest, images []string, turnNumber int) ([]HistoryContent, error) {
	if err := os.MkdirAll(artifactsDir, 0755); err != nil {
		return nil, err
	}
	imageContents := make([]HistoryContent, 0, len(images))
	for index, image := range images {
		data, extension, err := decodeImagePayload(image)
		if err != nil {
			return nil, err
		}
		artifactID := fmt.Sprintf("turn_%06d_img_%06d", turnNumber, index+1)
		filename := artifactID + extension
		artifactPath := filepath.Join(artifactsDir, filename)
		if err := os.WriteFile(artifactPath, data, 0644); err != nil {
			return nil, err
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
	return imageContents, nil
}

func writePendingImageGenerationConversation(config AppConfig, req ImageGenerateRequest) (string, error) {
	now := time.Now()
	nowText := now.Format(time.RFC3339)
	store := newHistoryStore(config.Storage)
	workspace, err := store.newWorkspace(now)
	if err != nil {
		return "", err
	}

	conversation := HistoryConversation{
		SchemaVersion: 1,
		ID:            workspace.ID,
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
			TurnCount: 1,
		},
	}
	userContent, err := imageGenerationUserContent(workspace.ArtifactsDir, req)
	if err != nil {
		return "", err
	}
	userTurn := buildImageGenerationUserTurn(workspace.ID, "turn_000001", nowText, req, userContent)
	if err := store.writeSnapshot(workspace, conversation, userTurn); err != nil {
		return "", err
	}
	return workspace.ID, nil
}

func appendImageGenerationResult(config AppConfig, conversationID string, req ImageGenerateRequest, payload ollamaGenerateResponse, images []string, raw string) error {
	store := newHistoryStore(config.Storage)
	loaded, err := store.loadForAppend(conversationID, "image_generation", "an image")
	if err != nil {
		return err
	}
	imageContents, err := writeImageArtifacts(loaded.ArtifactsDir, req, images)
	if err != nil {
		return err
	}
	assistantContents := imageContents
	if strings.TrimSpace(payload.Response) != "" && len(imageContents) == 0 {
		assistantContents = append(assistantContents, HistoryContent{Type: "text", Text: payload.Response})
	}
	nowText := time.Now().Format(time.RFC3339)
	assistantTurn := HistoryTurn{
		SchemaVersion:  1,
		ID:             fmt.Sprintf("turn_%06d", loaded.NextTurnNumber),
		ConversationID: conversationID,
		CreatedAt:      nowText,
		Kind:           "image_generation",
		Role:           "assistant",
		Model:          req.Model,
		Content:        assistantContents,
		ProviderResponse: map[string]any{
			"done":       payload.Done,
			"rawCompact": raw,
		},
	}
	loaded.Conversation.UpdatedAt = nowText
	loaded.Conversation.Stats.TurnCount++
	loaded.Conversation.Stats.ArtifactCount += len(imageContents)
	if err := store.writeConversation(loaded.Path, loaded.Conversation); err != nil {
		return err
	}
	return store.writeTurn(loaded.TurnsDir, assistantTurn)
}

func imageGenerationUserContent(artifactsDir string, req ImageGenerateRequest) ([]HistoryContent, error) {
	return historyContentForMessage(ChatMessage{
		Role:    "user",
		Content: req.Prompt,
		Images:  req.Images,
	}, artifactsDir, 1)
}

func buildImageGenerationUserTurn(conversationID, turnID, createdAt string, req ImageGenerateRequest, content []HistoryContent) HistoryTurn {
	return HistoryTurn{
		SchemaVersion:  1,
		ID:             turnID,
		ConversationID: conversationID,
		CreatedAt:      createdAt,
		Kind:           "image_generation",
		Role:           "user",
		Content:        content,
		Request: map[string]any{
			"prompt": req.Prompt,
			"width":  req.Width,
			"height": req.Height,
			"steps":  req.Steps,
			"images": len(req.Images),
		},
	}
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
		summaries = append(summaries, conversationSummaryFrom(conversation))
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
	return conversationSummaryFrom(conversation), nil
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

func pluralSuffix(count int) string {
	if count == 1 {
		return ""
	}
	return "s"
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
