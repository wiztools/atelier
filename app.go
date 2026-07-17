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
	// Create the storage directories once at launch from the current config so
	// the first chat/tool/history operation doesn't pay for it. The per-call
	// ensureStorageDirs in loadReadyConfig stays as a no-op-ish backstop for a
	// SaveConfig that changed the storage root mid-session.
	if config, err := loadAppConfig(); err == nil {
		_ = ensureStorageDirs(config.Storage)
		a.baseURL = config.Providers.Ollama.BaseURL
	}
}

// beforeClose is wired to Wails' OnBeforeClose hook. It prompts the user to
// confirm before the window closes; returning true prevents the close.
func (a *App) beforeClose(ctx context.Context) (prevent bool) {
	choice, err := runtime.MessageDialog(ctx, runtime.MessageDialogOptions{
		Type:          runtime.QuestionDialog,
		Title:         "Quit Atelier?",
		Message:       "Are you sure you want to quit Atelier?",
		Buttons:       []string{"Quit", "Cancel"},
		DefaultButton: "Quit",
		CancelButton:  "Cancel",
	})
	if err != nil {
		// If the dialog fails for any reason, don't trap the user in the app.
		return false
	}
	return choice != "Quit"
}

type AppConfig struct {
	Version    int              `json:"version"`
	Storage    ConfigStorage    `json:"storage"`
	Providers  ConfigProviders  `json:"providers"`
	Models     ConfigModels     `json:"models"`
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
	Ollama     ConfigOllama     `json:"ollama"`
	OpenRouter ConfigOpenRouter `json:"openrouter"`
	Fal        ConfigFal        `json:"fal"`
}

type ConfigOllama struct {
	BaseURL string             `json:"baseURL"`
	Models  ConfigOllamaModels `json:"models"`
	NumCtx  int                `json:"numCtx"`
}

type ConfigOllamaModels struct {
	Primary string `json:"primary"`
	Harness string `json:"harness"`
	Image   string `json:"image"`
}

type ConfigOpenRouter struct {
	Enabled bool   `json:"enabled"`
	Primary string `json:"primary,omitempty"`
	Harness string `json:"harness,omitempty"`
}

// ConfigFal configures the fal.ai image-generation backend. The API key lives
// in the OS keychain (see keychain.go), not in config — Enabled mirrors the
// key's presence for the frontend, like ConfigOpenRouter.
type ConfigFal struct {
	Enabled bool   `json:"enabled"`
	Model   string `json:"model,omitempty"`
	// ImageEditModel is the image-to-image endpoint used when the user attaches a
	// source image to transform; Model is the text-to-image endpoint.
	ImageEditModel string `json:"imageEditModel,omitempty"`
	// VideoModel is the text-to-video endpoint; VideoImageModel is the
	// image-to-video endpoint used when the user attaches an image to animate.
	VideoModel      string `json:"videoModel,omitempty"`
	VideoImageModel string `json:"videoImageModel,omitempty"`
	// AudioModel is the text-to-audio endpoint (speech, music, or sound effects).
	AudioModel string `json:"audioModel,omitempty"`
}

type ConfigModels struct {
	PrimaryProvider string `json:"primaryProvider,omitempty"`
	// HarnessProvider selects where the harness model runs (triage, skill
	// selection, planning). Absent in configs written before harness provider
	// selection existed, so it normalizes to "ollama" — see mergeAppConfig.
	HarnessProvider string `json:"harnessProvider,omitempty"`
	ImageProvider   string `json:"imageProvider,omitempty"`
}

type ConfigPrompts struct {
	System string `json:"system"`
}

type ConfigGeneration struct {
	Image ConfigImageGeneration `json:"image"`
	Video ConfigVideoGeneration `json:"video"`
}

type ConfigImageGeneration struct {
	Width  int `json:"width"`
	Height int `json:"height"`
	Steps  int `json:"steps"`
}

// ConfigVideoGeneration holds the two user-facing text-to-video knobs. Duration
// and AspectRatio are fal enum strings ("5"/"10", "16:9"/"9:16"/"1:1"); other
// fal parameters (negative prompt, cfg scale) use the model's own defaults.
type ConfigVideoGeneration struct {
	Duration    string `json:"duration"`
	AspectRatio string `json:"aspectRatio"`
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
	Role      string     `json:"role"`
	Content   string     `json:"content"`
	Images    []string   `json:"images,omitempty"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
}

// ToolCall mirrors the function-call shape a provider returns in
// message.tool_calls (and accepts in assistant message history). Both Ollama
// and OpenRouter use this shape, despite the Ollama-originated field name on
// the wire. Arguments is kept raw so the planner loop can validate before
// unmarshalling. The nested function payload is a named type (not an anonymous
// struct) so the Wails binding generator can mirror it into models.ts.
type ToolCall struct {
	Type     string       `json:"type,omitempty"`
	Function ToolFunction `json:"function"`
}

// ToolFunction is the function payload of a tool call.
type ToolFunction struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type ChatRequest struct {
	RequestID      string         `json:"requestID,omitempty"`
	ConversationID string         `json:"conversationId,omitempty"`
	BaseURL        string         `json:"baseURL,omitempty"`
	Provider       string         `json:"provider,omitempty"`
	Model          string         `json:"model"`
	SelectedModel  string         `json:"selectedModel,omitempty"`
	System         string         `json:"system,omitempty"`
	Messages       []ChatMessage  `json:"messages"`
	Think          any            `json:"think,omitempty"`
	Options        map[string]any `json:"options,omitempty"`
	Format         any            `json:"format,omitempty"`
	// Tools is only set on planner calls when the harness model supports native
	// tool-calling. The final-response request (preparedResponseRequest) never
	// sets it, so the primary model stays tool-free.
	Tools []map[string]any `json:"tools,omitempty"`
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
	Videos         []string `json:"videos,omitempty"`
	Audios         []string `json:"audios,omitempty"`
	Done           bool     `json:"done"`
	Error          string   `json:"error,omitempty"`
	Model          string   `json:"model,omitempty"`
	Provider       string   `json:"provider,omitempty"`
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
		Role      string     `json:"role"`
		Content   string     `json:"content"`
		Thinking  string     `json:"thinking"`
		ToolCalls []ToolCall `json:"tool_calls"`
	} `json:"message"`
	DoneReason string `json:"done_reason"`
	EvalCount  int    `json:"eval_count"`
	Error      string `json:"error"`
}

type ollamaChatResponse struct {
	Model   string `json:"model"`
	Message struct {
		Role      string     `json:"role"`
		Content   string     `json:"content"`
		Thinking  string     `json:"thinking"`
		ToolCalls []ToolCall `json:"tool_calls"`
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

// VideoGenerateRequest is the input to a text-to-video generation. Unlike
// images, videos take a duration and aspect ratio rather than width/height/steps
// — these mirror fal's text-to-video schema (see FalClient.GenerateVideo).
type VideoGenerateRequest struct {
	Model          string `json:"model"`
	Prompt         string `json:"prompt"`
	Duration       string `json:"duration,omitempty"`
	AspectRatio    string `json:"aspectRatio,omitempty"`
	NegativePrompt string `json:"negativePrompt,omitempty"`
	// Image, when set, is the source frame for image-to-video generation — a
	// URL or a base64 data URI. It maps to fal's image_url input and requires an
	// image-to-video model.
	Image string `json:"image,omitempty"`
	// GenerateAudio maps to fal's generate_audio input on audio-capable video
	// models. A pointer so "unspecified" (nil, let the model default) stays
	// distinct from an explicit false (silent clip) — the latter is what "video
	// without audio" requests. Endpoints that never emit audio ignore it.
	GenerateAudio *bool `json:"generateAudio,omitempty"`
}

type SaveImageRequest struct {
	Image         string `json:"image"`
	SuggestedName string `json:"suggestedName,omitempty"`
}

// SaveVideoRequest asks to copy a generated video artifact to a user-chosen
// location. Path is the on-disk artifact path (not a URL); the frontend passes
// the plain filesystem path the asset handler served the video from.
type SaveVideoRequest struct {
	Path          string `json:"path"`
	SuggestedName string `json:"suggestedName,omitempty"`
}

// AudioGenerateRequest is the input to a text-to-audio generation. The prompt is
// the text to synthesize (speech) or describe (music/sound effects). Duration and
// NegativePrompt are forwarded only when set — text-to-speech models ignore them,
// so callers can leave both empty (see FalClient.GenerateAudio).
type AudioGenerateRequest struct {
	Model          string `json:"model"`
	Prompt         string `json:"prompt"`
	Duration       string `json:"duration,omitempty"`
	NegativePrompt string `json:"negativePrompt,omitempty"`
	// Loop requests a seamless, gapless loop (sound-effect models). Voice selects
	// a text-to-speech voice. Both are resolved against the target model's schema
	// and dropped-with-notice when the model has no matching parameter.
	Loop  bool   `json:"loop,omitempty"`
	Voice string `json:"voice,omitempty"`
}

// SaveAudioRequest asks to copy a generated audio artifact to a user-chosen
// location, mirroring SaveVideoRequest.
type SaveAudioRequest struct {
	Path          string `json:"path"`
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
	Provider         string           `json:"provider,omitempty"`
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
	ID             string                 `json:"id"`
	Mode           string                 `json:"mode"`
	Status         string                 `json:"status"`
	StartedAt      string                 `json:"startedAt"`
	CompletedAt    string                 `json:"completedAt,omitempty"`
	DurationMS     int64                  `json:"durationMs,omitempty"`
	RequestID      string                 `json:"requestId,omitempty"`
	ConversationID string                 `json:"conversationId,omitempty"`
	Loop           HarnessLoop            `json:"loop"`
	Skill          *HarnessSkillDecision  `json:"skill,omitempty"`
	Triage         *HarnessTriageDecision `json:"triage,omitempty"`
	Steps          []HarnessStep          `json:"steps"`
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
	Name   string `json:"name"`
	Status string `json:"status"`
	// Call is the planner's emitted inputs for this tool call (the plan's
	// per-call params: content, model, path, command, duration, negativePrompt,
	// etc.). Recorded on every tool_call step so the emitted params are
	// inspectable post-hoc — the result struct carries only what the tool
	// produced, not what was requested. Zipped onto the activity at the
	// recording site (harness.go toolActivities), never via HarnessToolResult,
	// so planner params cannot leak into role:"tool" evidence.
	Call          HarnessToolCall `json:"call,omitempty"`
	Path          string          `json:"path,omitempty"`
	Command       []string        `json:"command,omitempty"`
	ExitCode      int             `json:"exitCode,omitempty"`
	StdoutPreview string          `json:"stdoutPreview,omitempty"`
	StderrPreview string          `json:"stderrPreview,omitempty"`
	DurationMS    int64           `json:"durationMs,omitempty"`
	Error         string          `json:"error,omitempty"`
}

func (a *App) GetConfig() (AppConfig, error) {
	a.configMu.Lock()
	defer a.configMu.Unlock()

	config, err := loadAppConfig()
	if err != nil {
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
	config, err := loadReadyConfig()
	if err != nil {
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
	config, err := loadReadyConfig()
	if err != nil {
		return ToolFileListResult{}, err
	}
	return newFilesystemToolLayer(config.Tools.Filesystem).ListFiles(req)
}

func (a *App) ReadToolFile(req ToolFileReadRequest) (ToolFileReadResult, error) {
	config, err := loadReadyConfig()
	if err != nil {
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
	config, err := loadReadyConfig()
	if err != nil {
		return HarnessToolResult{}, err
	}
	result := newToolGateway(a, config).Execute(context.Background(), req)
	return result, nil
}

func (a *App) StreamChat(req ChatRequest) (*ChatStreamStart, error) {
	if len(req.Messages) == 0 {
		return nil, errors.New("at least one message is required")
	}

	config, err := loadReadyConfig()
	if err != nil {
		return nil, err
	}
	engine := newHarnessEngine(config, a)
	if strings.TrimSpace(req.Model) == "" {
		req.Model = strings.TrimSpace(config.Providers.Ollama.Models.Primary)
	}
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
		engine.RunChatStream(streamCtx, requestID, req, true)
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

func (a *App) SaveImage(req SaveImageRequest) (string, error) {
	data, extension, err := decodeImagePayload(req.Image)
	if err != nil {
		return "", err
	}
	return a.saveArtifactDialog(data, req.SuggestedName, "atelier-image", extension, "Save generated image", []runtime.FileFilter{
		{DisplayName: "Image Files", Pattern: "*.png;*.jpg;*.jpeg;*.webp;*.gif"},
		{DisplayName: "All Files", Pattern: "*.*"},
	})
}

// SaveVideo copies a generated video artifact to a user-chosen location. The
// frontend passes the path the asset handler served the video from (either a
// bare filesystem path or the "/atelier-artifact"-prefixed URL); this strips the
// prefix, confirms the bytes are a playable video, and streams a Save dialog
// copy. Returns the chosen path, or "" if the dialog was cancelled.
func (a *App) SaveVideo(req SaveVideoRequest) (string, error) {
	sourcePath := strings.TrimSpace(req.Path)
	sourcePath = strings.TrimPrefix(sourcePath, artifactPrefix)
	if sourcePath == "" {
		return "", errors.New("video path is empty")
	}

	data, err := os.ReadFile(sourcePath)
	if err != nil {
		return "", err
	}
	if !isVideoBytes(data) {
		return "", errors.New("artifact is not a supported video")
	}

	extension := strings.ToLower(filepath.Ext(sourcePath))
	if extension == "" {
		extension = ".mp4"
	}
	return a.saveArtifactDialog(data, req.SuggestedName, "atelier-video", extension, "Save generated video", []runtime.FileFilter{
		{DisplayName: "Video Files", Pattern: "*.mp4;*.webm;*.mov;*.m4v"},
		{DisplayName: "All Files", Pattern: "*.*"},
	})
}

// SaveAudio copies a generated audio artifact to a user-chosen location,
// mirroring SaveVideo.
func (a *App) SaveAudio(req SaveAudioRequest) (string, error) {
	sourcePath := strings.TrimSpace(req.Path)
	sourcePath = strings.TrimPrefix(sourcePath, artifactPrefix)
	if sourcePath == "" {
		return "", errors.New("audio path is empty")
	}

	data, err := os.ReadFile(sourcePath)
	if err != nil {
		return "", err
	}
	if !isAudioBytes(data) {
		return "", errors.New("artifact is not a supported audio clip")
	}

	extension := strings.ToLower(filepath.Ext(sourcePath))
	if extension == "" {
		extension = ".mp3"
	}
	return a.saveArtifactDialog(data, req.SuggestedName, "atelier-audio", extension, "Save generated audio", []runtime.FileFilter{
		{DisplayName: "Audio Files", Pattern: "*.mp3;*.wav;*.ogg;*.flac;*.m4a;*.aac;*.opus"},
		{DisplayName: "All Files", Pattern: "*.*"},
	})
}

// saveArtifactDialog is the shared body for SaveImage/SaveVideo/SaveAudio: it
// builds a filename (defaultName + extension when the user gave none, ensuring
// the extension is present), opens a Save dialog with the given title/filters,
// and writes data to the chosen path. Each caller keeps its own source-loading
// and byte-sniffing; this owns the filename-guard → dialog → write ritual they
// all shared. Returns the chosen path, or ("", nil) if the dialog was cancelled.
func (a *App) saveArtifactDialog(data []byte, suggestedName, defaultName, extension, title string, filters []runtime.FileFilter) (string, error) {
	filename := sanitizeFilename(suggestedName)
	if filename == "" {
		filename = defaultName + extension
	}
	if !strings.HasSuffix(strings.ToLower(filename), extension) {
		filename += extension
	}

	path, err := runtime.SaveFileDialog(a.ctx, runtime.SaveDialogOptions{
		Title:           title,
		DefaultFilename: filename,
		Filters:         filters,
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

	// A conversation loaded from history renders its images as "/atelier-artifact"
	// URLs served from disk rather than inline data URLs (hydrateHistoryContent),
	// so resolve those back to bytes on disk — mirroring SaveVideo/SaveAudio.
	if sourcePath := strings.TrimPrefix(image, artifactPrefix); sourcePath != image {
		data, err := os.ReadFile(sourcePath)
		if err != nil {
			return nil, "", err
		}
		if !isImageBytes(data) {
			return nil, "", errors.New("artifact is not a supported image")
		}
		extension := strings.ToLower(filepath.Ext(sourcePath))
		if extension == "" {
			extension = ".png"
		}
		return data, extension, nil
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

func (a *App) writeChatConversation(req ChatRequest, assistantContent, assistantThinking, model, reason string, tokens int) (string, error) {
	if strings.TrimSpace(assistantContent) == "" && strings.TrimSpace(assistantThinking) == "" {
		return "", nil
	}
	config, err := loadReadyConfig()
	if err != nil {
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
	return newHarnessEngine(config).SaveChatTurn(req, assistantContent, assistantThinking, model, resolvedProvider(req), reason, tokens, title, run)
}

// generateConversationTitle names a new conversation with the primary model,
// falling back to the truncated prompt whenever the call can't be made or
// comes back empty. It resolves through the provider layer rather than
// reaching for ollamaClient: req.Model is the *primary* chat model, so an
// OpenRouter primary would otherwise send an OpenRouter model name to the
// local Ollama endpoint and silently degrade every title to the fallback.
func (a *App) generateConversationTitle(config AppConfig, req ChatRequest, assistantContent string) string {
	userPrompt := lastUserPrompt(req.Messages)
	fallback := titleFromPrompt(userPrompt)
	if strings.TrimSpace(req.Model) == "" || strings.TrimSpace(userPrompt) == "" {
		return fallback
	}

	provider, err := a.providerFor(resolvedProvider(req), req.BaseURL)
	if err != nil {
		return fallback
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	completion, err := provider.CompleteChat(ctx, conversationTitleRequest(config, req, userPrompt, assistantContent))
	if err != nil {
		return fallback
	}
	return normalizeConversationTitle(completion.Content, userPrompt)
}

// conversationTitleRequest builds the title call for a finished turn. The
// options are Ollama tuning knobs; openRouterChatBody drops options entirely,
// so the same request is valid on either provider.
func conversationTitleRequest(config AppConfig, req ChatRequest, userPrompt, assistantContent string) ChatRequest {
	titlePrompt := "Generate a concise title for this chat conversation. Return only the title, no quotes, no punctuation wrapper, no explanation. Keep it under 8 words.\n\nUser:\n" +
		compactString(userPrompt, 1600) +
		"\n\nAssistant:\n" +
		compactString(assistantContent, 1600)
	options := map[string]any{
		"temperature": 0,
		"num_predict": 24,
	}
	for key, value := range req.Options {
		if _, exists := options[key]; !exists {
			options[key] = value
		}
	}
	return ChatRequest{
		BaseURL:  req.BaseURL,
		Provider: resolvedProvider(req),
		Model:    req.Model,
		System:   "You create short, specific conversation titles.",
		Messages: []ChatMessage{{Role: "user", Content: titlePrompt}},
		Options:  withNumCtx(options, ollamaNumCtx(config)),
	}
}

func (a *App) emitChatEvent(event ChatStreamEvent) {
	if a.ctx != nil {
		runtime.EventsEmit(a.ctx, "chat:chunk", event)
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
		return a.cachedBaseURL()
	}
	if normalized == "" {
		return a.cachedBaseURL()
	}
	return normalized
}

// cachedBaseURL reads the runtime Ollama base URL set by GetConfig, SaveConfig,
// or SetOllamaBaseURL. Those writers hold configMu; this read takes the same
// lock so a config save during a concurrent chat stream can't race the field.
func (a *App) cachedBaseURL() string {
	a.configMu.Lock()
	defer a.configMu.Unlock()
	return a.baseURL
}

func (a *App) ollamaClient(baseURL string) OllamaClient {
	return newOllamaClient(a.client, a.resolveBaseURL(baseURL))
}

func (a *App) providerFor(providerID, baseURL string) (ChatProvider, error) {
	return newProviderRegistry(a).Resolve(providerID, baseURL)
}

// ListPrimaryModels lists models available for the primary chat role from
// the given provider. Harness/image model lists still go through the
// existing Ollama-only ListModels method — this is deliberately separate so
// the primary-role picker doesn't disturb the harness/image UI's capability
// detection (image-generation flags, family/parameter metadata), which only
// OllamaModel carries today.
func (a *App) ListPrimaryModels(provider, baseURL string) ([]ModelInfo, error) {
	resolved, err := a.providerFor(provider, baseURL)
	if err != nil {
		return nil, err
	}
	return resolved.ListModels(context.Background())
}

func (a *App) SaveOpenRouterAPIKey(apiKey string) error {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return clearOpenRouterAPIKey()
	}
	return saveOpenRouterAPIKey(apiKey)
}

func (a *App) HasOpenRouterAPIKey() (bool, error) {
	key, err := loadOpenRouterAPIKey()
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(key) != "", nil
}

func (a *App) SaveFalAPIKey(apiKey string) error {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return clearFalAPIKey()
	}
	return saveFalAPIKey(apiKey)
}

func (a *App) HasFalAPIKey() (bool, error) {
	key, err := loadFalAPIKey()
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(key) != "", nil
}

// CheckFalConnection validates the stored fal.ai API key with a cheap
// authenticated ping (no generation). Returns an error describing why the key
// is rejected, or nil when it resolves. Used by the Settings "Check Connection"
// button — the OpenRouter equivalent doubles as a model list, but fal's model
// field is free text, so this only confirms the key works.
func (a *App) CheckFalConnection() error {
	key, err := loadFalAPIKey()
	if err != nil {
		return err
	}
	if strings.TrimSpace(key) == "" {
		return errFalKeyNotConfigured
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return newFalClient(a.client, key).VerifyKey(ctx)
}

// ListFalModels returns fal's text-to-image catalog for the Settings image-model
// picker, replacing the free-text fal model field. Unlike the chat primary
// picker (ListPrimaryModels), fal is not a ChatProvider — this hits fal's
// /v1/models catalog directly. Requires a stored fal key (the underlying client
// attaches it to every request), so call it after the key is saved.
func (a *App) ListFalModels() ([]FalModel, error) {
	key, err := loadFalAPIKey()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return newFalClient(a.client, key).ListModels(ctx, falTextToImageCategory, 0)
}

// ListFalVideoModels returns fal's text-to-video catalog for the Settings
// video-model picker. It mirrors ListFalModels but filters to the text-to-video
// category; fal is the only video backend (Ollama has no video models).
func (a *App) ListFalVideoModels() ([]FalModel, error) {
	key, err := loadFalAPIKey()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return newFalClient(a.client, key).ListModels(ctx, falTextToVideoCategory, 0)
}

// ListFalVideoImageModels returns fal's image-to-video catalog for the Settings
// image-to-video model picker (used to animate an attached image).
func (a *App) ListFalVideoImageModels() ([]FalModel, error) {
	key, err := loadFalAPIKey()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return newFalClient(a.client, key).ListModels(ctx, falImageToVideoCategory, 0)
}

// ListFalAudioModels returns fal's audio-generation catalog for the Settings
// audio-model picker. It merges the text-to-audio (music/sound effects) and
// text-to-speech categories, deduped by endpoint id.
func (a *App) ListFalAudioModels() ([]FalModel, error) {
	key, err := loadFalAPIKey()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	client := newFalClient(a.client, key)

	merged := []FalModel{}
	seen := map[string]bool{}
	for _, category := range []string{falTextToSpeechCategory, falTextToAudioCategory} {
		models, err := client.ListModels(ctx, category, 0)
		if err != nil {
			return nil, err
		}
		for _, model := range models {
			if model.ID == "" || seen[model.ID] {
				continue
			}
			seen[model.ID] = true
			merged = append(merged, model)
		}
	}
	return merged, nil
}

// resolvedPrimaryModelAndProvider returns which model/provider the primary
// chat role should use when a request doesn't specify one explicitly.
func (a *App) resolvedPrimaryModelAndProvider(config AppConfig) (model, provider string) {
	provider = strings.TrimSpace(config.Models.PrimaryProvider)
	if provider != "openrouter" {
		provider = "ollama"
	}
	if provider == "openrouter" {
		return strings.TrimSpace(config.Providers.OpenRouter.Primary), provider
	}
	return strings.TrimSpace(config.Providers.Ollama.Models.Primary), provider
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

// loadReadyConfig loads the merged config and ensures every storage directory
// it references exists. This is the boilerplate every read-side App method
// needs before touching history or artifacts, factored out so each caller is a
// single line instead of the load → ensure → error ritual. Read-only: unlike
// GetConfig, it never writes config back to disk.
func loadReadyConfig() (AppConfig, error) {
	config, err := loadAppConfig()
	if err != nil {
		return AppConfig{}, err
	}
	if err := ensureStorageDirs(config.Storage); err != nil {
		return AppConfig{}, err
	}
	return config, nil
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
					Primary: "mistral-small3.1:latest",
					Harness: "mistral-small3.1:latest",
					Image:   "x/z-image-turbo:latest",
				},
				NumCtx: defaultOllamaNumCtx,
			},
		},
		Models: ConfigModels{
			PrimaryProvider: "ollama",
			HarnessProvider: "ollama",
			ImageProvider:   "ollama",
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
			Video: ConfigVideoGeneration{
				Duration:    defaultFalVideoDuration,
				AspectRatio: defaultFalVideoAspectRatio,
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
	if strings.TrimSpace(config.Providers.Ollama.Models.Primary) == "" {
		config.Providers.Ollama.Models.Primary = defaults.Providers.Ollama.Models.Primary
	}
	if strings.TrimSpace(config.Providers.Ollama.Models.Harness) == "" {
		config.Providers.Ollama.Models.Harness = config.Providers.Ollama.Models.Primary
	}
	if strings.TrimSpace(config.Providers.Ollama.Models.Image) == "" {
		config.Providers.Ollama.Models.Image = defaults.Providers.Ollama.Models.Image
	}
	if config.Providers.Ollama.NumCtx <= 0 {
		config.Providers.Ollama.NumCtx = defaults.Providers.Ollama.NumCtx
	}
	if strings.TrimSpace(config.Models.PrimaryProvider) == "" {
		config.Models.PrimaryProvider = defaults.Models.PrimaryProvider
	}
	// HarnessProvider selects where triage, skill selection, and planning run.
	// Normalize unknown or empty values to the Ollama default: an absent field
	// means a config written before this setting existed, which must keep its
	// old behaviour exactly.
	switch strings.TrimSpace(config.Models.HarnessProvider) {
	case "ollama", "openrouter":
		config.Models.HarnessProvider = strings.TrimSpace(config.Models.HarnessProvider)
	default:
		config.Models.HarnessProvider = defaults.Models.HarnessProvider
	}
	// ImageProvider selects the generate_image backend. Normalize unknown or
	// empty values to the Ollama default so the tool path never sees a stray id.
	switch strings.TrimSpace(config.Models.ImageProvider) {
	case "ollama", "fal":
		config.Models.ImageProvider = strings.TrimSpace(config.Models.ImageProvider)
	default:
		config.Models.ImageProvider = defaults.Models.ImageProvider
	}
	if config.Models.ImageProvider == "fal" && strings.TrimSpace(config.Providers.Fal.Model) == "" {
		config.Providers.Fal.Model = defaultFalImageModel
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
	if strings.TrimSpace(config.Generation.Video.Duration) == "" {
		config.Generation.Video.Duration = defaults.Generation.Video.Duration
	}
	if strings.TrimSpace(config.Generation.Video.AspectRatio) == "" {
		config.Generation.Video.AspectRatio = defaults.Generation.Video.AspectRatio
	}
	config.Tools = mergeToolsConfig(config.Tools, defaults.Tools)
	config.UI.Mode = defaults.UI.Mode
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
		"df",
		"du",
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

func writeChatConversation(config AppConfig, req ChatRequest, assistantContent, assistantThinking, model, provider, reason string, tokens int, title string, run HarnessRun) (string, error) {
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

	userTurn, assistantTurn, err := buildChatTurnPair(workspace.ID, 1, nowText, req, assistantContent, assistantThinking, model, provider, reason, tokens, workspace.ArtifactsDir, run)
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

func buildChatTurnPair(conversationID string, firstTurnNumber int, createdAt string, req ChatRequest, assistantContent, assistantThinking, model, provider, reason string, tokens int, artifactsDir string, run HarnessRun) (HistoryTurn, HistoryTurn, error) {
	userTurn, err := buildChatUserTurn(conversationID, firstTurnNumber, createdAt, req, artifactsDir)
	if err != nil {
		return HistoryTurn{}, HistoryTurn{}, err
	}
	assistantTurn := buildChatAssistantTurn(conversationID, firstTurnNumber+1, createdAt, assistantContent, assistantThinking, model, provider, reason, tokens, run)
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

func buildChatAssistantTurn(conversationID string, turnNumber int, createdAt string, assistantContent, assistantThinking, model, provider, reason string, tokens int, run HarnessRun) HistoryTurn {
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
		Provider:         provider,
		Content:          assistantContents,
		ProviderResponse: providerResponse,
	}
}

func appendChatConversation(config AppConfig, req ChatRequest, assistantContent, assistantThinking, model, provider, reason string, tokens int, run HarnessRun) (string, error) {
	conversationID := strings.TrimSpace(req.ConversationID)
	store := newHistoryStore(config.Storage)
	loaded, err := store.loadForAppend(conversationID, "chat", "a chat")
	if err != nil {
		return "", err
	}
	nowText := time.Now().Format(time.RFC3339)
	userTurn, assistantTurn, err := buildChatTurnPair(conversationID, loaded.NextTurnNumber, nowText, req, assistantContent, assistantThinking, model, provider, reason, tokens, loaded.ArtifactsDir, run)
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

func appendChatAssistantTurn(config AppConfig, conversationID, assistantContent, assistantThinking, model, provider, reason string, tokens int, run HarnessRun) error {
	store := newHistoryStore(config.Storage)
	loaded, err := store.loadForAppend(conversationID, "chat", "a chat")
	if err != nil {
		return err
	}
	nowText := time.Now().Format(time.RFC3339)
	assistantTurn := buildChatAssistantTurn(conversationID, loaded.NextTurnNumber, nowText, assistantContent, assistantThinking, model, provider, reason, tokens, run)

	loaded.Conversation.UpdatedAt = nowText
	loaded.Conversation.Stats.TurnCount++
	if err := store.writeConversation(loaded.Path, loaded.Conversation); err != nil {
		return err
	}
	return store.writeTurn(loaded.TurnsDir, assistantTurn)
}

// chatAssistantTurnMedia is the per-call piece a media-bearing assistant turn
// needs. build writes that turn's artifacts (images, videos, audios) into
// artifactsDir using turnNumber, and returns the HistoryContent entries they
// become, any live-render URLs, and the tool metadata recorded on the saved
// turn. The shared append helper calls build after loading the conversation,
// since artifact writing needs the resolved ArtifactsDir and NextTurnNumber.
type chatAssistantTurnMedia struct {
	build func(artifactsDir string, turnNumber int) (contents []HistoryContent, urls []string, tool map[string]any, err error)
}

// appendChatAssistantTurnWithMedia is the shared skeleton for persisting an
// assistant turn that produced media. It loads the conversation, delegates
// artifact writing + metadata assembly to media.build, then writes a single
// assistant turn whose ProviderResponse carries the tool metadata. The three
// media-specific append functions (images/videos/audios) are thin wrappers
// that differ only in their build closure; the load, content assembly, stat
// update, and persistence are identical and live here once.
func appendChatAssistantTurnWithMedia(config AppConfig, conversationID, assistantContent, assistantThinking, model, provider, reason string, run HarnessRun, media chatAssistantTurnMedia) ([]string, error) {
	store := newHistoryStore(config.Storage)
	loaded, err := store.loadForAppend(conversationID, "chat", "a chat")
	if err != nil {
		return nil, err
	}
	nowText := time.Now().Format(time.RFC3339)

	mediaContents, urls, tool, err := media.build(loaded.ArtifactsDir, loaded.NextTurnNumber)
	if err != nil {
		return nil, err
	}

	contents := []HistoryContent{{Type: "text", Text: assistantContent}}
	if strings.TrimSpace(assistantThinking) != "" {
		contents = append(contents, HistoryContent{Type: "thinking", Text: assistantThinking})
	}
	contents = append(contents, mediaContents...)
	assistantTurn := HistoryTurn{
		SchemaVersion:  1,
		ID:             fmt.Sprintf("turn_%06d", loaded.NextTurnNumber),
		ConversationID: conversationID,
		CreatedAt:      nowText,
		Kind:           "chat",
		Role:           "assistant",
		Model:          model,
		Provider:       provider,
		Content:        contents,
		ProviderResponse: map[string]any{
			"doneReason": reason,
			"harnessRun": run,
			"tool":       tool,
		},
	}

	loaded.Conversation.UpdatedAt = nowText
	loaded.Conversation.Stats.TurnCount++
	loaded.Conversation.Stats.ArtifactCount += len(mediaContents)
	if err := store.writeConversation(loaded.Path, loaded.Conversation); err != nil {
		return nil, err
	}
	if err := store.writeTurn(loaded.TurnsDir, assistantTurn); err != nil {
		return nil, err
	}
	return urls, nil
}

func appendChatAssistantTurnWithImages(config AppConfig, conversationID, assistantContent, assistantThinking, model, provider, reason string, images []string, raw string, run HarnessRun, imageReq ImageGenerateRequest) error {
	_, err := appendChatAssistantTurnWithMedia(config, conversationID, assistantContent, assistantThinking, model, provider, reason, run, chatAssistantTurnMedia{
		build: func(artifactsDir string, turnNumber int) ([]HistoryContent, []string, map[string]any, error) {
			imageContents, err := writeChatImageArtifacts(artifactsDir, imageReq, images, turnNumber)
			if err != nil {
				return nil, nil, nil, err
			}
			tool := map[string]any{
				"name":       "image_generation",
				"model":      imageReq.Model,
				"imageCount": len(imageContents),
				"rawCompact": raw,
			}
			return imageContents, nil, tool, nil
		},
	})
	return err
}

// appendChatAssistantTurnWithVideos persists an assistant turn that produced one
// or more generated videos (and, in the rare case a turn produced both, any
// images too). Video temp files are moved into the conversation's artifacts
// directory; the returned URLs are the "/atelier-artifact" links the live UI
// renders before the turn is reloaded from history.
func appendChatAssistantTurnWithVideos(config AppConfig, conversationID, assistantContent, assistantThinking, model, provider, reason string, images []string, imageReq ImageGenerateRequest, videos []ToolVideoFile, videoReq VideoGenerateRequest, run HarnessRun) ([]string, error) {
	return appendChatAssistantTurnWithMedia(config, conversationID, assistantContent, assistantThinking, model, provider, reason, run, chatAssistantTurnMedia{
		build: func(artifactsDir string, turnNumber int) ([]HistoryContent, []string, map[string]any, error) {
			imageContents, err := writeChatImageArtifacts(artifactsDir, imageReq, images, turnNumber)
			if err != nil {
				return nil, nil, nil, err
			}
			videoContents, videoURLs, err := writeChatVideoArtifacts(artifactsDir, videos, turnNumber)
			if err != nil {
				return nil, nil, nil, err
			}
			contents := append([]HistoryContent{}, imageContents...)
			contents = append(contents, videoContents...)
			tool := map[string]any{
				"name":       "video_generation",
				"model":      videoReq.Model,
				"videoCount": len(videoContents),
				"imageCount": len(imageContents),
			}
			return contents, videoURLs, tool, nil
		},
	})
}

// writeChatVideoArtifacts moves each generated video's temp file into the
// artifacts directory as turn_NNNNNN_vid_NNNNNN.<ext> and returns the history
// content entries plus the "/atelier-artifact" URLs the live UI renders. A temp
// file that can't be resolved to a video is skipped rather than aborting the
// whole turn save. Thin wrapper over the shared media-artifact writer.
func writeChatVideoArtifacts(artifactsDir string, videos []ToolVideoFile, turnNumber int) ([]HistoryContent, []string, error) {
	files := make([]mediaArtifactEntry, len(videos))
	for i, v := range videos {
		files[i] = mediaArtifactEntry{tempPath: v.TempPath, mimeType: v.MimeType}
	}
	return writeChatMediaArtifacts(artifactsDir, files, turnNumber, "vid", "video", videoExtensionForMediaType)
}

// writeChatMediaArtifacts is the shared body for moving generated video/audio
// temp files into a conversation's artifacts directory. Each file is named
// turn_NNNNNN_<kindTag>_NNNNNN.<ext>; it returns the HistoryContent entries and
// the "/atelier-artifact" URLs the live UI renders before reload. A file with
// no temp path is skipped rather than aborting the whole turn save. The
// contentType ("video"/"audio") and extensionFn differ per media kind.
type mediaArtifactEntry struct {
	tempPath string
	mimeType string
}

func writeChatMediaArtifacts(artifactsDir string, files []mediaArtifactEntry, turnNumber int, kindTag, contentType string, extensionFn func(string) string) ([]HistoryContent, []string, error) {
	if len(files) == 0 {
		return nil, nil, nil
	}
	if err := os.MkdirAll(artifactsDir, 0755); err != nil {
		return nil, nil, err
	}
	absArtifactsDir, err := filepath.Abs(artifactsDir)
	if err != nil {
		return nil, nil, err
	}
	contents := make([]HistoryContent, 0, len(files))
	urls := make([]string, 0, len(files))
	for index, file := range files {
		tempPath := strings.TrimSpace(file.tempPath)
		if tempPath == "" {
			continue
		}
		extension := extensionFn(file.mimeType)
		artifactID := fmt.Sprintf("turn_%06d_%s_%06d", turnNumber, kindTag, index+1)
		filename := artifactID + extension
		destPath := filepath.Join(absArtifactsDir, filename)
		if err := moveFile(tempPath, destPath); err != nil {
			return nil, nil, err
		}
		contents = append(contents, HistoryContent{
			Type:       contentType,
			ArtifactID: artifactID,
			Path:       filepath.ToSlash(filepath.Join("artifacts", filename)),
			MimeType:   mediaTypeForExtension(extension),
		})
		urls = append(urls, artifactPrefix+destPath)
	}
	return contents, urls, nil
}

// appendChatAssistantTurnWithAudios persists an assistant turn that produced one
// or more generated audio clips. Audio temp files are moved into the
// conversation's artifacts directory; the returned URLs are the
// "/atelier-artifact" links the live UI renders before reload.
func appendChatAssistantTurnWithAudios(config AppConfig, conversationID, assistantContent, assistantThinking, model, provider, reason string, audios []ToolAudioFile, audioReq AudioGenerateRequest, run HarnessRun) ([]string, error) {
	return appendChatAssistantTurnWithMedia(config, conversationID, assistantContent, assistantThinking, model, provider, reason, run, chatAssistantTurnMedia{
		build: func(artifactsDir string, turnNumber int) ([]HistoryContent, []string, map[string]any, error) {
			audioContents, audioURLs, err := writeChatAudioArtifacts(artifactsDir, audios, turnNumber)
			if err != nil {
				return nil, nil, nil, err
			}
			tool := map[string]any{
				"name":       "audio_generation",
				"model":      audioReq.Model,
				"audioCount": len(audioContents),
			}
			return audioContents, audioURLs, tool, nil
		},
	})
}

// writeChatAudioArtifacts moves each generated audio's temp file into the
// artifacts directory as turn_NNNNNN_aud_NNNNNN.<ext> and returns the history
// content entries plus the "/atelier-artifact" URLs the live UI renders. Thin
// wrapper over the shared media-artifact writer, mirroring writeChatVideoArtifacts.
func writeChatAudioArtifacts(artifactsDir string, audios []ToolAudioFile, turnNumber int) ([]HistoryContent, []string, error) {
	files := make([]mediaArtifactEntry, len(audios))
	for i, a := range audios {
		files[i] = mediaArtifactEntry{tempPath: a.TempPath, mimeType: a.MimeType}
	}
	return writeChatMediaArtifacts(artifactsDir, files, turnNumber, "aud", "audio", audioExtensionForMediaType)
}

// moveFile relocates src to dst, falling back to a copy-and-delete when the two
// live on different filesystems (os.Rename fails with a cross-device error).
func moveFile(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.WriteFile(dst, data, 0644); err != nil {
		return err
	}
	os.Remove(src)
	return nil
}

func writeChatImageArtifacts(artifactsDir string, req ImageGenerateRequest, images []string, turnNumber int) ([]HistoryContent, error) {
	if err := os.MkdirAll(artifactsDir, 0755); err != nil {
		return nil, err
	}
	imageContents := make([]HistoryContent, 0, len(images))
	for index, image := range images {
		data, extension, err := decodeImagePayload(image)
		if err != nil {
			// Skip entries that aren't decodable image bytes (e.g. a stray http
			// URL that slipped past the harvest step) rather than aborting the
			// whole turn save and orphaning any artifacts already written.
			continue
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

const (
	// maxConversationList is the hard cap on how many conversations the sidebar
	// list returns. We intentionally never page beyond the most recent 100.
	maxConversationList = 100
	// listCandidateOverfetch is how many conversation.json files we shortlist by
	// file mtime (a cheap, no-parse proxy for recency) before parsing. The 50
	// slots of slack above maxConversationList absorb minor mtime/UpdatedAt skew
	// so the exact UpdatedAt re-sort below is not starved.
	listCandidateOverfetch = 150
)

type conversationCandidate struct {
	path  string
	mtime time.Time
}

// listConversations returns the most-recently-updated conversations, capped at
// maxConversationList. It avoids parsing every conversation.json on disk: it
// walks the tree collecting only (path, mtime) per conversation, shortlists the
// newest listCandidateOverfetch by mtime, parses just those, then orders the
// survivors by the exact UpdatedAt field and applies the cap.
func listConversations(storage ConfigStorage) ([]ConversationSummary, error) {
	root := filepath.Join(storage.History, "conversations")
	candidates := []conversationCandidate{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || filepath.Base(path) != "conversation.json" {
			return nil
		}
		// Skip soft-deleted conversations without parsing: deleteConversation
		// always writes a sibling tombstone.json. This keeps freshly-deleted
		// conversations (whose mtime is bumped to "now") from crowding real
		// entries out of the mtime shortlist below.
		if _, statErr := os.Stat(filepath.Join(filepath.Dir(path), "tombstone.json")); statErr == nil {
			return nil
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			return infoErr
		}
		candidates = append(candidates, conversationCandidate{path: path, mtime: info.ModTime()})
		return nil
	})
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []ConversationSummary{}, nil
		}
		return nil, err
	}

	// Shortlist the newest candidates by mtime, then parse only those.
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].mtime.After(candidates[j].mtime)
	})
	if len(candidates) > listCandidateOverfetch {
		candidates = candidates[:listCandidateOverfetch]
	}

	summaries := make([]ConversationSummary, 0, len(candidates))
	for _, candidate := range candidates {
		var conversation HistoryConversation
		if err := readJSONFile(candidate.path, &conversation); err != nil {
			return nil, err
		}
		// Belt-and-suspenders: a conversation may carry DeletedAt without a
		// tombstone (e.g. legacy records), so re-check after parsing.
		if conversation.DeletedAt != "" {
			continue
		}
		summaries = append(summaries, conversationSummaryFrom(conversation))
	}

	// Exact ordering by UpdatedAt (RFC3339 strings sort chronologically), then
	// apply the hard cap.
	sort.Slice(summaries, func(i, j int) bool {
		return summaries[i].UpdatedAt > summaries[j].UpdatedAt
	})
	if len(summaries) > maxConversationList {
		summaries = summaries[:maxConversationList]
	}
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

// hydrateHistoryContent resolves image content items to URLs the frontend can
// render. Instead of embedding the full base64 data URL (which can be several
// MB for generated images), it sets a relative URL that the Wails asset
// handler serves from disk. This avoids pushing large payloads through the
// JSON IPC boundary.
func hydrateHistoryContent(conversationDir string, contents []HistoryContent) []HistoryContent {
	hydrated := make([]HistoryContent, 0, len(contents))
	for _, content := range contents {
		// Image, video, and audio artifacts are all stored on disk and served by
		// the asset handler; resolve their relative path to an /atelier-artifact
		// URL.
		if (content.Type == "image" || content.Type == "video" || content.Type == "audio") && content.Path != "" && !strings.HasPrefix(content.Path, "data:") {
			absPath := filepath.Join(conversationDir, filepath.FromSlash(content.Path))
			if _, err := os.Stat(absPath); err == nil {
				content.Text = "/atelier-artifact" + absPath
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

// compactString truncates value to at most maxLength runes, appending "..." when
// it shortens. It works in runes (not bytes) so a multi-byte UTF-8 sequence —
// common in non-ASCII titles and prompts — is never split, which would leave
// invalid UTF-8 in history and the model's input.
func compactString(value string, maxLength int) string {
	if len(value) <= maxLength {
		return value
	}
	runes := []rune(value)
	if len(runes) <= maxLength {
		return value
	}
	return string(runes[:maxLength]) + "..."
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
	case ".mp4", ".m4v":
		return "video/mp4"
	case ".webm":
		return "video/webm"
	case ".mov":
		return "video/quicktime"
	case ".mp3":
		return "audio/mpeg"
	case ".wav":
		return "audio/wav"
	case ".ogg", ".opus":
		return "audio/ogg"
	case ".flac":
		return "audio/flac"
	case ".m4a", ".aac":
		return "audio/mp4"
	default:
		return "image/png"
	}
}

// audioExtensionForMediaType maps an audio MIME type to a file extension,
// defaulting to .mp3 (the format most fal audio endpoints return).
func audioExtensionForMediaType(mediaType string) string {
	switch strings.ToLower(strings.TrimSpace(mediaType)) {
	case "audio/wav", "audio/x-wav", "audio/wave":
		return ".wav"
	case "audio/ogg", "audio/opus":
		return ".ogg"
	case "audio/flac", "audio/x-flac":
		return ".flac"
	case "audio/mp4", "audio/aac", "audio/x-m4a":
		return ".m4a"
	default:
		return ".mp3"
	}
}

// isAudioBytes reports whether data looks like a container an <audio> tag can
// play: MP3 (ID3 tag or MPEG frame sync), WAV/RIFF, OGG, FLAC, or MP4/M4A
// (ISO base media "ftyp"). Returns false otherwise so a non-audio download is
// rejected rather than written as a broken artifact.
func isAudioBytes(data []byte) bool {
	if len(data) >= 3 && bytes.Equal(data[:3], []byte("ID3")) {
		return true
	}
	if len(data) >= 2 && data[0] == 0xFF && (data[1]&0xE0) == 0xE0 {
		return true // MPEG audio frame sync (MP3/AAC-ADTS)
	}
	if len(data) >= 12 && bytes.Equal(data[:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WAVE")) {
		return true
	}
	if len(data) >= 4 && bytes.Equal(data[:4], []byte("OggS")) {
		return true
	}
	if len(data) >= 4 && bytes.Equal(data[:4], []byte("fLaC")) {
		return true
	}
	if len(data) >= 12 && bytes.Equal(data[4:8], []byte("ftyp")) {
		return true // MP4/M4A audio container
	}
	return false
}

// videoExtensionForMediaType maps a video MIME type to a file extension. Unlike
// extensionForMediaType (image-oriented, defaults to .png), an unrecognized
// video type falls back to .mp4 — the format every fal video endpoint returns.
func videoExtensionForMediaType(mediaType string) string {
	switch strings.ToLower(strings.TrimSpace(mediaType)) {
	case "video/webm":
		return ".webm"
	case "video/quicktime":
		return ".mov"
	default:
		return ".mp4"
	}
}

// isVideoBytes reports whether data looks like a container format a <video> tag
// can play. It sniffs MP4/MOV (ISO base media, "ftyp" box at offset 4), WebM/
// Matroska (EBML magic), and returns false otherwise so a stray non-video
// download is rejected rather than written as a broken artifact.
func isVideoBytes(data []byte) bool {
	if len(data) >= 12 && bytes.Equal(data[4:8], []byte("ftyp")) {
		return true
	}
	if len(data) >= 4 && bytes.Equal(data[:4], []byte{0x1a, 0x45, 0xdf, 0xa3}) {
		return true
	}
	return false
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
