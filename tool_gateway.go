package main

import (
	"context"
	"fmt"
	"strings"
)

type ToolExecutionRequest struct {
	Name           string          `json:"name"`
	Call           HarnessToolCall `json:"call"`
	RequestID      string          `json:"requestId,omitempty"`
	ConversationID string          `json:"conversationId,omitempty"`
	Source         string          `json:"source,omitempty"`
}

type ToolGateway struct {
	app                 *App
	registry            HarnessToolRegistry
	tools               HarnessToolExecutionContext
	permissionRequester func(context.Context, ToolPermissionRequestEvent) bool
}

func newToolGateway(app *App, config AppConfig, registry ...HarnessToolRegistry) ToolGateway {
	gw := ToolGateway{
		app:   app,
		tools: newHarnessToolExecutionContext(config),
	}
	if len(registry) > 0 {
		gw.registry = registry[0]
	} else {
		gw.registry = defaultHarnessToolRegistry(config)
	}
	gateway := gw
	if app != nil {
		gateway.permissionRequester = app.toolPermission
		gateway.tools.GenerateImage = func(ctx context.Context, req ImageGenerateRequest) (ollamaGenerateResponse, []byte, error) {
			if strings.TrimSpace(config.Models.ImageProvider) == "fal" {
				apiKey, err := loadFalAPIKey()
				if err != nil {
					return ollamaGenerateResponse{}, nil, err
				}
				if strings.TrimSpace(apiKey) == "" {
					return ollamaGenerateResponse{}, nil, errFalKeyNotConfigured
				}
				return newFalClient(app.client, apiKey).GenerateImage(ctx, req)
			}
			return app.ollamaClient(config.Providers.Ollama.BaseURL).GenerateImage(ctx, req)
		}
		gateway.tools.GenerateVideo = func(ctx context.Context, req VideoGenerateRequest) (GeneratedVideo, error) {
			apiKey, err := loadFalAPIKey()
			if err != nil {
				return GeneratedVideo{}, err
			}
			if strings.TrimSpace(apiKey) == "" {
				return GeneratedVideo{}, errFalKeyNotConfigured
			}
			return newFalClient(app.client, apiKey).GenerateVideo(ctx, req)
		}
		gateway.tools.UpscaleImage = func(ctx context.Context, req ImageUpscaleRequest) (ollamaGenerateResponse, error) {
			apiKey, err := loadFalAPIKey()
			if err != nil {
				return ollamaGenerateResponse{}, err
			}
			if strings.TrimSpace(apiKey) == "" {
				return ollamaGenerateResponse{}, errFalKeyNotConfigured
			}
			return newFalClient(app.client, apiKey).UpscaleImage(ctx, req)
		}
		schemaCache := newFalSchemaCache(app.client, config.Storage.Root)
		audioOverrides := loadFalOverrides(config.Storage.Root)
		gateway.tools.GenerateAudio = func(ctx context.Context, req AudioGenerateRequest) (GeneratedAudio, error) {
			apiKey, err := loadFalAPIKey()
			if err != nil {
				return GeneratedAudio{}, err
			}
			if strings.TrimSpace(apiKey) == "" {
				return GeneratedAudio{}, errFalKeyNotConfigured
			}
			schema := schemaCache.Get(ctx, req.Model)
			body, notices := resolveAudioBody(schema, req, audioOverrides)
			generated, err := newFalClient(app.client, apiKey).GenerateAudio(ctx, req.Model, body)
			generated.Notices = notices
			return generated, err
		}
	}
	return gateway
}

func (g ToolGateway) Execute(ctx context.Context, req ToolExecutionRequest) HarnessToolResult {
	call := req.Call
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = strings.TrimSpace(call.Name)
	}
	if name == "" {
		return HarnessToolResult{Status: "failed", Summary: "tool not recognized", Error: "tool name is required"}
	}
	call.Name = name

	result := HarnessToolResult{Name: name, Status: "completed"}
	definition, ok := g.registry.Get(name)
	if !ok {
		result.Status = "failed"
		result.Error = fmt.Sprintf("unknown tool %q", name)
		result.Summary = "tool not recognized"
		return result
	}
	requiresPermission := definition.RequiresPermissionFor(call) || g.requiresUnlistedCommandPermission(call)
	if requiresPermission && !g.requestPermission(ctx, req, definition, call) {
		return HarnessToolResult{Name: name, Status: "denied", Summary: definition.Title + " was not approved", Error: "permission denied"}
	}
	tools := g.tools
	if g.requiresUnlistedCommandPermission(call) {
		tools.Filesystem = tools.Filesystem.withApprovedUnlistedCommand(call.Command)
	}
	output, summary, err := definition.Execute(ctx, tools, call)
	result.Result = output
	result.Summary = summary
	if np, ok := output.(NoticeProvider); ok {
		result.Notices = np.ToolNotices()
	}
	if err != nil {
		result.Status = "failed"
		result.Error = err.Error()
		result.Summary = name + " failed"
	} else if toolError := harnessToolOutputError(output); toolError != "" {
		result.Status = "failed"
		result.Error = toolError
	}
	return result
}

func (g ToolGateway) requiresUnlistedCommandPermission(call HarnessToolCall) bool {
	if strings.TrimSpace(call.Name) != "run_command" {
		return false
	}
	if g.tools.Filesystem == nil {
		return false
	}
	name := normalizedCommandName(call.Command)
	return name != "" && !commandAllowed(name, g.tools.Filesystem.config.AllowedCommands)
}

func (g ToolGateway) requestPermission(ctx context.Context, req ToolExecutionRequest, definition HarnessToolDefinition, call HarnessToolCall) bool {
	if g.permissionRequester == nil {
		// Nobody can approve: fail closed.
		return false
	}
	event := ToolPermissionRequestEvent{}
	if definition.Permission != nil {
		event = definition.Permission(call)
	}
	if strings.TrimSpace(event.Summary) == "" {
		event.Summary = definition.Title
	}
	event.ID = randomID("permission")
	event.RequestID = req.RequestID
	event.ConversationID = req.ConversationID
	event.ToolName = call.Name
	event.Action = call.Name
	return g.permissionRequester(ctx, event)
}
