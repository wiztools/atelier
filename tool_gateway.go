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

func newToolGateway(app *App, config AppConfig) ToolGateway {
	gateway := ToolGateway{
		app:      app,
		registry: defaultHarnessToolRegistry(config),
		tools:    newHarnessToolExecutionContext(config),
	}
	if app != nil {
		gateway.permissionRequester = app.requestToolPermission
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
		name = "run_command"
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
	if definition.RequiresPermissionFor(call) && !g.requestPermission(ctx, req, definition, call) {
		return HarnessToolResult{Name: name, Status: "denied", Summary: definition.Title + " was not approved", Error: "permission denied"}
	}
	output, summary, err := definition.Execute(ctx, g.tools, call)
	result.Result = output
	result.Summary = summary
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

func (g ToolGateway) requestPermission(ctx context.Context, req ToolExecutionRequest, definition HarnessToolDefinition, call HarnessToolCall) bool {
	if g.permissionRequester == nil {
		return true
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
