package main

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

type HarnessToolRisk string

const (
	HarnessToolRiskRead  HarnessToolRisk = "read"
	HarnessToolRiskWrite HarnessToolRisk = "write"
	HarnessToolRiskExec  HarnessToolRisk = "exec"
)

type HarnessToolDefinition struct {
	Name            string
	Title           string
	Description     string
	Example         string
	Risk            HarnessToolRisk
	Validate        func(prefix string, call HarnessToolCall) []string
	Execute         func(ctx context.Context, tools HarnessToolExecutionContext, call HarnessToolCall) (any, string, error)
	NeedsPermission func(call HarnessToolCall) bool
	Permission      func(call HarnessToolCall) ToolPermissionRequestEvent
	Activity        func(result HarnessToolResult) HarnessToolActivity
}

type HarnessToolExecutionContext struct {
	Config        AppConfig
	Filesystem    *FilesystemToolLayer
	GenerateImage func(ctx context.Context, req ImageGenerateRequest) (ollamaGenerateResponse, []byte, error)
}

// ToolImageResult carries generated images as data URLs. The Images field is
// stripped before the result is rendered into a tool message so base64 data
// never enters a model context; the harness extracts it for the UI and history.
type ToolImageResult struct {
	Model  string   `json:"model"`
	Prompt string   `json:"prompt"`
	Count  int      `json:"count"`
	Images []string `json:"images,omitempty"`
}

type HarnessToolRegistry struct {
	definitions []HarnessToolDefinition
	byName      map[string]HarnessToolDefinition
}

func newHarnessToolExecutionContext(config AppConfig) HarnessToolExecutionContext {
	return HarnessToolExecutionContext{
		Config:     config,
		Filesystem: newFilesystemToolLayer(config.Tools.Filesystem),
	}
}

func defaultHarnessToolRegistry(config AppConfig) HarnessToolRegistry {
	definitions := filesystemToolDefinitions(config.Tools.Filesystem)
	if strings.TrimSpace(config.Providers.Ollama.Models.Image) != "" {
		definitions = append(definitions, imageGenerationToolDefinition())
	}
	return newHarnessToolRegistry(definitions)
}

func imageGenerationToolDefinition() HarnessToolDefinition {
	return HarnessToolDefinition{
		Name:        "generate_image",
		Title:       "Generate image",
		Description: "Use this when the user asks to create, draw, paint, or render an image. The configured image model generates it and the image is attached to the assistant reply.",
		Example:     `{"name":"generate_image","content":"a watercolor of a lighthouse at dusk"}`,
		Risk:        HarnessToolRiskRead,
		Validate: func(prefix string, call HarnessToolCall) []string {
			if strings.TrimSpace(call.Content) == "" {
				return []string{prefix + ".content is required for generate_image (the image prompt)"}
			}
			return nil
		},
		Execute: func(ctx context.Context, tools HarnessToolExecutionContext, call HarnessToolCall) (any, string, error) {
			if tools.GenerateImage == nil {
				return nil, "image generation unavailable", errors.New("image generation is not available in this context")
			}
			model := strings.TrimSpace(tools.Config.Providers.Ollama.Models.Image)
			if model == "" {
				return nil, "image generation unavailable", errors.New("no image model is configured")
			}
			imageReq := ImageGenerateRequest{
				Model:  model,
				Prompt: strings.TrimSpace(call.Content),
				Width:  tools.Config.Generation.Image.Width,
				Height: tools.Config.Generation.Image.Height,
				Steps:  tools.Config.Generation.Image.Steps,
			}
			payload, raw, err := tools.GenerateImage(ctx, imageReq)
			if err != nil {
				return nil, "image generation failed", err
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
			if len(images) == 0 {
				return nil, "image generation returned no image", errors.New("image model returned no image data")
			}
			output := ToolImageResult{Model: model, Prompt: imageReq.Prompt, Count: len(images), Images: images}
			return output, fmt.Sprintf("generated %d image%s with %s", len(images), pluralSuffix(len(images)), model), nil
		},
		Activity: func(result HarnessToolResult) HarnessToolActivity {
			activity := defaultHarnessToolActivity(result)
			if typed, ok := result.Result.(ToolImageResult); ok {
				activity.Command = []string{"ollama", "generate", typed.Model}
			}
			return activity
		},
	}
}

func filesystemToolRegistry() HarnessToolRegistry {
	return defaultHarnessToolRegistry(defaultAppConfig())
}

// workspaceRootPhrase describes the filesystem boundary in concrete terms.
// The tools operate on real files on the host machine, confined to a real
// directory — not an abstract or simulated "workspace". Naming the actual
// root keeps a planning model from concluding it cannot observe the machine.
func workspaceRootPhrase(fsConfig ConfigFilesystemTool) string {
	if root := strings.TrimSpace(fsConfig.Root); root != "" {
		return "the Atelier filesystem root (" + root + ")"
	}
	return "the Atelier filesystem root"
}

// runCommandDescription builds the run_command tool description from the live
// filesystem config so the model is told exactly which commands it may run.
// The command list is read from the same ConfigFilesystemTool.AllowedCommands
// that fs_tools.go enforces, so the prompt and the allowlist cannot drift.
func runCommandDescription(fsConfig ConfigFilesystemTool) string {
	base := "Use this to run an allowlisted command on this machine. Commands run for real; the working directory is confined to " + workspaceRootPhrase(fsConfig) + " and its subdirectories. Use it when the user or a skill provides a command, or when a command is the direct way to gather evidence such as searching text, listing with filters, counting, or checking status."
	allowed := make([]string, 0, len(fsConfig.AllowedCommands))
	for _, cmd := range fsConfig.AllowedCommands {
		if trimmed := strings.TrimSpace(cmd); trimmed != "" {
			allowed = append(allowed, trimmed)
		}
	}
	if len(allowed) == 0 {
		return base + " No commands are currently permitted by the allowlist."
	}
	return base + " Allowed commands (nothing else will run): " + strings.Join(allowed, ", ") + "."
}

func filesystemToolDefinitions(fsConfig ConfigFilesystemTool) []HarnessToolDefinition {
	definitions := []HarnessToolDefinition{
		{
			Name:        "list_files",
			Title:       "List files",
			Description: "Use this to inspect real files and directories under " + workspaceRootPhrase(fsConfig) + " on this machine.",
			Example:     `{"name":"list_files","path":"optional relative directory"}`,
			Risk:        HarnessToolRiskRead,
			Execute: func(_ context.Context, tools HarnessToolExecutionContext, call HarnessToolCall) (any, string, error) {
				output, err := tools.Filesystem.ListFiles(ToolFileListRequest{Path: call.Path})
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
			Description: "Use this to read a real text file from under " + workspaceRootPhrase(fsConfig) + " on this machine.",
			Example:     `{"name":"read_file","path":"relative/path.txt","maxBytes":20000}`,
			Risk:        HarnessToolRiskRead,
			Validate: func(prefix string, call HarnessToolCall) []string {
				if strings.TrimSpace(call.Path) == "" {
					return []string{prefix + ".path is required for read_file"}
				}
				return nil
			},
			Execute: func(_ context.Context, tools HarnessToolExecutionContext, call HarnessToolCall) (any, string, error) {
				output, err := tools.Filesystem.ReadFile(ToolFileReadRequest{
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
			Description: runCommandDescription(fsConfig),
			Example:     `{"name":"run_command","command":"rg","args":["-n","Atelier","."],"cwd":"optional relative directory"}`,
			Risk:        HarnessToolRiskExec,
			NeedsPermission: func(call HarnessToolCall) bool {
				return !isReadOnlyCommandCall(call)
			},
			Validate: func(prefix string, call HarnessToolCall) []string {
				if strings.TrimSpace(call.Command) == "" {
					return []string{prefix + ".command is required for run_command"}
				}
				return nil
			},
			Execute: func(ctx context.Context, tools HarnessToolExecutionContext, call HarnessToolCall) (any, string, error) {
				output, err := tools.Filesystem.RunCommand(ctx, ToolCommandRequest{
					Command:   call.Command,
					Args:      call.Args,
					Cwd:       call.Cwd,
					Env:       call.Env,
					TimeoutMS: call.TimeoutMS,
				})
				return output, commandResultSummary(output), err
			},
			Permission: func(call HarnessToolCall) ToolPermissionRequestEvent {
				command := append([]string{call.Command}, call.Args...)
				summary := formatCommandSummary(command)
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
			Description: "Use this only when the user clearly asks to create or modify a real file under " + workspaceRootPhrase(fsConfig) + " on this machine.",
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
			Execute: func(_ context.Context, tools HarnessToolExecutionContext, call HarnessToolCall) (any, string, error) {
				output, err := tools.Filesystem.WriteFile(ToolFileWriteRequest{
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
	return definitions
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

func (r HarnessToolRegistry) Names() []string {
	names := make([]string, 0, len(r.definitions))
	for _, definition := range r.definitions {
		names = append(names, definition.Name)
	}
	return names
}

func (r HarnessToolRegistry) NamesCSV() string {
	return strings.Join(r.Names(), ", ")
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

func (definition HarnessToolDefinition) RequiresPermissionFor(call HarnessToolCall) bool {
	if definition.NeedsPermission != nil {
		return definition.NeedsPermission(call)
	}
	return definition.RequiresPermission()
}

func defaultHarnessToolActivity(result HarnessToolResult) HarnessToolActivity {
	return HarnessToolActivity{
		Name:   result.Name,
		Status: result.Status,
		Error:  result.Error,
	}
}

func formatCommandSummary(command []string) string {
	parts := make([]string, 0, len(command))
	for _, arg := range command {
		if strings.TrimSpace(arg) == "" {
			continue
		}
		parts = append(parts, strconv.Quote(arg))
	}
	return strings.Join(parts, " ")
}

func commandResultSummary(result ToolCommandResult) string {
	return fmt.Sprintf("command exited with code %d", result.ExitCode)
}

func isReadOnlyCommandCall(call HarnessToolCall) bool {
	if len(reqEnvWithoutBlanks(call.Env)) > 0 {
		return false
	}
	name := normalizedCommandName(call.Command)
	readOnlyCommands := map[string]bool{
		"cat": true, "df": true, "du": true, "echo": true, "find": true,
		"grep": true, "head": true, "ls": true, "pwd": true, "rg": true,
		"tail": true, "wc": true,
	}
	if !readOnlyCommands[name] {
		return false
	}
	for _, arg := range call.Args {
		if commandFlagDenied(name, commandFlagName(strings.TrimSpace(arg))) {
			return false
		}
	}
	return true
}
