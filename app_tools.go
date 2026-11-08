package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

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

func (a *App) requestToolPermission(ctx context.Context, event ToolPermissionRequestEvent) ToolPermissionDecision {
	if a.ctx == nil {
		// No UI is attached, so nobody can approve: fail closed.
		return ToolPermissionDecision{Outcome: permissionOutcomeDenied}
	}
	if strings.TrimSpace(event.ID) == "" {
		event.ID = randomID("permission")
	}
	requestedAt := time.Now()
	decisionAt := func(approved bool, outcome string) ToolPermissionDecision {
		return ToolPermissionDecision{Approved: approved, Outcome: outcome, WaitMS: time.Since(requestedAt).Milliseconds()}
	}
	response := make(chan bool, 1)
	a.permissionsMu.Lock()
	a.permissions[event.ID] = response
	a.permissionsMu.Unlock()
	runtime.EventsEmit(a.ctx, "atelier:tool-permission", event)

	select {
	case approved := <-response:
		if approved {
			return decisionAt(true, permissionOutcomeApproved)
		}
		return decisionAt(false, permissionOutcomeDenied)
	case <-ctx.Done():
		a.permissionsMu.Lock()
		delete(a.permissions, event.ID)
		a.permissionsMu.Unlock()
		return decisionAt(false, permissionOutcomeCancelled)
	case <-time.After(2 * time.Minute):
		a.permissionsMu.Lock()
		delete(a.permissions, event.ID)
		a.permissionsMu.Unlock()
		return decisionAt(false, permissionOutcomeTimeout)
	}
}

// DetectLocalTools reports the locally installed CLI media tools (whisper
// today — see local_tools.go) for the Settings Transcription section: which
// binaries were found, where, and which transcription provider the current
// config resolves to. overrides carries unsaved binary overrides keyed by
// tool id ("whisper") so the UI can reflect an edit before it is persisted.
// Detection is LookPath-only — no process is spawned — so this is cheap to
// call on every settings render.
func (a *App) DetectLocalTools(overrides LocalToolOverrides) (LocalToolsReport, error) {
	config, err := loadAppConfig()
	if err != nil {
		return LocalToolsReport{}, err
	}
	return detectLocalTools(config, overrides), nil
}
