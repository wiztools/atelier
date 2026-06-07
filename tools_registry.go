package main

import (
	"context"
	"fmt"
	"strings"
)

type HarnessToolRisk string

const (
	HarnessToolRiskRead  HarnessToolRisk = "read"
	HarnessToolRiskWrite HarnessToolRisk = "write"
	HarnessToolRiskExec  HarnessToolRisk = "exec"
)

type HarnessToolDefinition struct {
	Name        string
	Title       string
	Description string
	Example     string
	Risk        HarnessToolRisk
	Validate    func(prefix string, call HarnessToolCall) []string
	Execute     func(ctx context.Context, layer *FilesystemToolLayer, call HarnessToolCall) (any, string, error)
	Permission  func(call HarnessToolCall) ToolPermissionRequestEvent
	Activity    func(result HarnessToolResult) HarnessToolActivity
}

type HarnessToolRegistry struct {
	definitions []HarnessToolDefinition
	byName      map[string]HarnessToolDefinition
}

func filesystemToolRegistry() HarnessToolRegistry {
	definitions := []HarnessToolDefinition{
		{
			Name:        "list_files",
			Title:       "List files",
			Description: "Use this to inspect files and directories in the configured workspace.",
			Example:     `{"name":"list_files","path":"optional relative directory"}`,
			Risk:        HarnessToolRiskRead,
			Execute: func(_ context.Context, layer *FilesystemToolLayer, call HarnessToolCall) (any, string, error) {
				output, err := layer.ListFiles(ToolFileListRequest{Path: call.Path})
				return output, fmt.Sprintf("listed %d entries", len(output.Entries)), err
			},
			Activity: func(result HarnessToolResult) HarnessToolActivity {
				activity := defaultHarnessToolActivity(result)
				if typed, ok := result.Result.(ToolFileListResult); ok {
					activity.Path = typed.Path
				}
				return activity
			},
		},
		{
			Name:        "read_file",
			Title:       "Read file",
			Description: "Use this to read a text file from the configured workspace.",
			Example:     `{"name":"read_file","path":"relative/path.txt","maxBytes":20000}`,
			Risk:        HarnessToolRiskRead,
			Validate: func(prefix string, call HarnessToolCall) []string {
				if strings.TrimSpace(call.Path) == "" {
					return []string{prefix + ".path is required for read_file"}
				}
				return nil
			},
			Execute: func(_ context.Context, layer *FilesystemToolLayer, call HarnessToolCall) (any, string, error) {
				output, err := layer.ReadFile(ToolFileReadRequest{
					Path:        call.Path,
					MaxBytes:    call.MaxBytes,
					AllowBinary: call.AllowBinary,
				})
				return output, fmt.Sprintf("read %d bytes", output.Bytes), err
			},
			Activity: func(result HarnessToolResult) HarnessToolActivity {
				activity := defaultHarnessToolActivity(result)
				if typed, ok := result.Result.(ToolFileReadResult); ok {
					activity.Path = typed.Path
				}
				return activity
			},
		},
		{
			Name:        "run_command",
			Title:       "Run command",
			Description: "Use this only when a shell command is needed inside the configured workspace.",
			Example:     `{"name":"run_command","command":"pwd","args":[],"cwd":"optional relative directory"}`,
			Risk:        HarnessToolRiskExec,
			Validate: func(prefix string, call HarnessToolCall) []string {
				if strings.TrimSpace(call.Command) == "" {
					return []string{prefix + ".command is required for run_command"}
				}
				return nil
			},
			Execute: func(ctx context.Context, layer *FilesystemToolLayer, call HarnessToolCall) (any, string, error) {
				output, err := layer.RunCommand(ctx, ToolCommandRequest{
					Command:   call.Command,
					Args:      call.Args,
					Cwd:       call.Cwd,
					Env:       call.Env,
					TimeoutMS: call.TimeoutMS,
				})
				return output, fmt.Sprintf("command exited with code %d", output.ExitCode), err
			},
			Permission: func(call HarnessToolCall) ToolPermissionRequestEvent {
				command := append([]string{call.Command}, call.Args...)
				summary := strings.TrimSpace(strings.Join(command, " "))
				if summary == "" {
					summary = "Run command"
				}
				return ToolPermissionRequestEvent{
					Command: command,
					Cwd:     call.Cwd,
					Summary: summary,
				}
			},
			Activity: func(result HarnessToolResult) HarnessToolActivity {
				activity := defaultHarnessToolActivity(result)
				if typed, ok := result.Result.(ToolCommandResult); ok {
					activity.Command = typed.Command
					activity.Path = typed.Cwd
					activity.ExitCode = typed.ExitCode
					activity.StdoutPreview = previewToolContent(typed.Stdout)
					activity.StderrPreview = previewToolContent(typed.Stderr)
					activity.DurationMS = typed.DurationMS
				}
				return activity
			},
		},
		{
			Name:        "write_file",
			Title:       "Write file",
			Description: "Use this only when the user clearly asks to create or modify a workspace file.",
			Example:     `{"name":"write_file","path":"relative/path.txt","content":"text","overwrite":false,"append":false}`,
			Risk:        HarnessToolRiskWrite,
			Validate: func(prefix string, call HarnessToolCall) []string {
				var errors []string
				if strings.TrimSpace(call.Path) == "" {
					errors = append(errors, prefix+".path is required for write_file")
				}
				if call.Content == "" {
					errors = append(errors, prefix+".content is required for write_file")
				}
				return errors
			},
			Execute: func(_ context.Context, layer *FilesystemToolLayer, call HarnessToolCall) (any, string, error) {
				output, err := layer.WriteFile(ToolFileWriteRequest{
					Path:      call.Path,
					Content:   call.Content,
					Append:    call.Append,
					Overwrite: call.Overwrite,
				})
				return output, fmt.Sprintf("wrote %d bytes", output.Bytes), err
			},
			Permission: func(call HarnessToolCall) ToolPermissionRequestEvent {
				summary := "Write file"
				if strings.TrimSpace(call.Path) != "" {
					summary = "Write " + call.Path
				}
				return ToolPermissionRequestEvent{
					Path:           call.Path,
					ContentPreview: previewToolContent(call.Content),
					Summary:        summary,
				}
			},
			Activity: func(result HarnessToolResult) HarnessToolActivity {
				activity := defaultHarnessToolActivity(result)
				if typed, ok := result.Result.(ToolFileWriteResult); ok {
					activity.Path = typed.Path
				}
				return activity
			},
		},
	}
	return newHarnessToolRegistry(definitions)
}

func newHarnessToolRegistry(definitions []HarnessToolDefinition) HarnessToolRegistry {
	byName := make(map[string]HarnessToolDefinition, len(definitions))
	for _, definition := range definitions {
		byName[definition.Name] = definition
	}
	return HarnessToolRegistry{definitions: definitions, byName: byName}
}

func (r HarnessToolRegistry) Get(name string) (HarnessToolDefinition, bool) {
	definition, ok := r.byName[strings.TrimSpace(name)]
	return definition, ok
}

func (r HarnessToolRegistry) NamesCSV() string {
	names := make([]string, 0, len(r.definitions))
	for _, definition := range r.definitions {
		names = append(names, definition.Name)
	}
	return strings.Join(names, ", ")
}

func (r HarnessToolRegistry) PromptCatalog() string {
	lines := make([]string, 0, len(r.definitions))
	for _, definition := range r.definitions {
		line := "- " + definition.Example
		if strings.TrimSpace(definition.Description) != "" {
			line += " - " + definition.Description
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func (definition HarnessToolDefinition) RequiresPermission() bool {
	return definition.Risk == HarnessToolRiskWrite || definition.Risk == HarnessToolRiskExec
}

func defaultHarnessToolActivity(result HarnessToolResult) HarnessToolActivity {
	return HarnessToolActivity{
		Name:   result.Name,
		Status: result.Status,
		Error:  result.Error,
	}
}
