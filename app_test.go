package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func waitForStreamCleanup(t *testing.T, app *App, requestID string) {
	t.Helper()
	for range 100 {
		app.streamsMu.Lock()
		_, exists := app.streams[requestID]
		app.streamsMu.Unlock()
		if !exists {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("stream %q did not clean up", requestID)
}

func harnessStepByKind(t *testing.T, steps []any, kind string) map[string]any {
	t.Helper()
	for _, step := range steps {
		typed, ok := step.(map[string]any)
		if ok && typed["kind"] == kind {
			return typed
		}
	}
	t.Fatalf("harness steps missing %q: %+v", kind, steps)
	return nil
}

func historyImagesForTest(contents []HistoryContent) []HistoryContent {
	images := []HistoryContent{}
	for _, content := range contents {
		if content.Type == "image" {
			images = append(images, content)
		}
	}
	return images
}

func historyTextForTest(contents []HistoryContent, contentType string) string {
	for _, content := range contents {
		if content.Type == contentType {
			return content.Text
		}
	}
	return ""
}

func TestNormalizeBaseURL(t *testing.T) {
	got, err := normalizeBaseURL("localhost:11434/")
	if err != nil {
		t.Fatalf("normalizeBaseURL returned error: %v", err)
	}
	if got != "http://localhost:11434" {
		t.Fatalf("normalizeBaseURL = %q, want %q", got, "http://localhost:11434")
	}
}

func TestNormalizeImagePayload(t *testing.T) {
	payload := normalizeImagePayload("iVBORw0KGgo=")
	if payload != "data:image/png;base64,iVBORw0KGgo=" {
		t.Fatalf("normalizeImagePayload = %q", payload)
	}

	dataURL := "data:image/png;base64,iVBORw0KGgo="
	if normalizeImagePayload(dataURL) != dataURL {
		t.Fatal("data URL should pass through unchanged")
	}
}

func TestCollectImagesFromSingularImageField(t *testing.T) {
	raw := []byte(`{"model":"x/z-image-turbo:latest","response":"","done_reason":"stop","image":"iVBORw0KGgo="}`)
	images := collectImagesFromJSON(raw)
	if len(images) != 1 {
		t.Fatalf("collectImagesFromJSON returned %d images, want 1", len(images))
	}
	if images[0] != "data:image/png;base64,iVBORw0KGgo=" {
		t.Fatalf("image = %q", images[0])
	}
}

func TestCompactRawResponseRedactsImageData(t *testing.T) {
	raw := []byte(`{"image":"iVBORw0KGgo="}`)
	compact := compactRawResponse(raw)
	if compact == `{"image":"iVBORw0KGgo="}` {
		t.Fatal("compact raw response should redact image data")
	}
}

func TestDecodeImagePayload(t *testing.T) {
	data, extension, err := decodeImagePayload("data:image/png;base64,iVBORw0KGgo=")
	if err != nil {
		t.Fatalf("decodeImagePayload returned error: %v", err)
	}
	if len(data) != 8 {
		t.Fatalf("decoded data length = %d, want 8", len(data))
	}
	if extension != ".png" {
		t.Fatalf("extension = %q, want .png", extension)
	}
}

func TestNormalizeImagePayloadRejectsNonImageBase64(t *testing.T) {
	if normalizeImagePayload("stop") != "" {
		t.Fatal("non-image base64 should not be treated as a renderable image")
	}
}

func TestSanitizeFilename(t *testing.T) {
	got := sanitizeFilename(`bad/name:image?.png`)
	if got != "bad-name-image-.png" {
		t.Fatalf("sanitizeFilename = %q", got)
	}
}

func TestMergeAppConfigFillsDefaults(t *testing.T) {
	config := mergeAppConfig(AppConfig{})
	if config.Version != 1 {
		t.Fatalf("version = %d, want 1", config.Version)
	}
	if config.Providers.Ollama.BaseURL != defaultOllamaBaseURL {
		t.Fatalf("baseURL = %q", config.Providers.Ollama.BaseURL)
	}
	if config.Storage.History == "" {
		t.Fatal("history storage should default")
	}
	if config.Providers.Ollama.Models.Tools != config.Providers.Ollama.Models.Chat {
		t.Fatalf("tools model = %q, want chat default %q", config.Providers.Ollama.Models.Tools, config.Providers.Ollama.Models.Chat)
	}
	if config.Prompts.System == "" {
		t.Fatal("system prompt should default")
	}
	if config.Generation.Image.Width != 768 || config.Generation.Image.Steps != 24 {
		t.Fatalf("image generation defaults = %+v", config.Generation.Image)
	}
	if config.Providers.Ollama.NumCtx != defaultOllamaNumCtx {
		t.Fatalf("numCtx = %d, want default %d", config.Providers.Ollama.NumCtx, defaultOllamaNumCtx)
	}
	if config.Tools.Filesystem.Root == "" {
		t.Fatal("filesystem tool root should default")
	}
	if config.Tools.Filesystem.MaxOutputBytes <= 0 {
		t.Fatal("filesystem tool output limit should default")
	}
	if config.Tools.Filesystem.TimeoutMS <= 0 {
		t.Fatal("filesystem tool timeout should default")
	}
}

func TestMergeAppConfigDefaultsToolsModelToChatModel(t *testing.T) {
	merged := mergeAppConfig(AppConfig{
		Providers: ConfigProviders{Ollama: ConfigOllama{
			Models: ConfigOllamaModels{Chat: "chat-model"},
		}},
	})
	if merged.Providers.Ollama.Models.Tools != "chat-model" {
		t.Fatalf("tools model = %q, want chat model fallback", merged.Providers.Ollama.Models.Tools)
	}
}

func TestMergeAppConfigNormalizesOllamaEndpoint(t *testing.T) {
	config := mergeAppConfig(AppConfig{
		Providers: ConfigProviders{
			Ollama: ConfigOllama{
				BaseURL: "localhost:11434/",
				Models: ConfigOllamaModels{
					Chat:  "chat-model",
					Tools: "harness-model",
					Image: "image-model",
				},
			},
		},
		Prompts: ConfigPrompts{
			System: "custom",
		},
		Generation: ConfigGeneration{
			Image: ConfigImageGeneration{Width: 512, Height: 512, Steps: 8},
		},
		UI: ConfigUI{Mode: "image"},
	})
	if config.Providers.Ollama.BaseURL != "http://localhost:11434" {
		t.Fatalf("baseURL = %q", config.Providers.Ollama.BaseURL)
	}
	if config.UI.Mode != "chat" {
		t.Fatalf("mode = %q, want chat", config.UI.Mode)
	}
	if config.Providers.Ollama.Models.Tools != "harness-model" {
		t.Fatalf("tools model = %q", config.Providers.Ollama.Models.Tools)
	}
}

func TestMergeStorageConfigExpandsHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	storage := mergeStorageConfig(ConfigStorage{Root: "~/.atelier"}, defaultAppConfig().Storage)
	if storage.Root != filepath.Join(home, ".atelier") {
		t.Fatalf("root = %q", storage.Root)
	}
	if storage.History != filepath.Join(home, ".atelier", "history") {
		t.Fatalf("history = %q", storage.History)
	}
}

func TestDefaultDocumentsRootUsesHomeDocuments(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", "")
	t.Setenv("XDG_DOCUMENTS_DIR", "")

	if got := defaultDocumentsRoot(); got != filepath.Join(home, "Documents") {
		t.Fatalf("defaultDocumentsRoot = %q, want home Documents", got)
	}
	config := mergeAppConfig(AppConfig{})
	if config.Tools.Filesystem.Root != filepath.Join(home, "Documents") {
		t.Fatalf("filesystem root = %q, want Documents default", config.Tools.Filesystem.Root)
	}
}

func TestDefaultDocumentsRootUsesXDGDocumentsDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DOCUMENTS_DIR", "$HOME/Docs")

	if got := defaultDocumentsRoot(); got != filepath.Join(home, "Docs") {
		t.Fatalf("defaultDocumentsRoot = %q, want XDG documents dir", got)
	}
}

func TestFilesystemToolRunCommandCapturesOutput(t *testing.T) {
	root := t.TempDir()
	tool := newFilesystemToolLayer(ConfigFilesystemTool{
		Root:           root,
		MaxOutputBytes: 1024,
		TimeoutMS:      1000,
	})

	result, err := tool.RunCommand(context.Background(), ToolCommandRequest{
		Command: "/bin/echo",
		Args:    []string{"hello atelier"},
	})
	if err != nil {
		t.Fatalf("RunCommand returned error: %v", err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("exit code = %d, stderr = %q", result.ExitCode, result.Stderr)
	}
	if strings.TrimSpace(result.Stdout) != "hello atelier" {
		t.Fatalf("stdout = %q", result.Stdout)
	}
	if result.Cwd != root {
		t.Fatalf("cwd = %q, want %q", result.Cwd, root)
	}
}

func TestFilesystemToolRejectsCwdOutsideRoot(t *testing.T) {
	tool := newFilesystemToolLayer(ConfigFilesystemTool{Root: t.TempDir()})
	_, err := tool.RunCommand(context.Background(), ToolCommandRequest{
		Command: "/bin/echo",
		Cwd:     "/",
	})
	if err == nil {
		t.Fatal("RunCommand should reject cwd outside root")
	}
}

func TestFilesystemToolTruncatesCommandOutput(t *testing.T) {
	root := t.TempDir()
	tool := newFilesystemToolLayer(ConfigFilesystemTool{
		Root:           root,
		MaxOutputBytes: 4,
		TimeoutMS:      1000,
	})

	result, err := tool.RunCommand(context.Background(), ToolCommandRequest{
		Command: "/bin/echo",
		Args:    []string{"abcdef"},
	})
	if err != nil {
		t.Fatalf("RunCommand returned error: %v", err)
	}
	if !result.Truncated {
		t.Fatal("result should be marked truncated")
	}
	if result.Stdout != "abcd" {
		t.Fatalf("stdout = %q, want truncated output", result.Stdout)
	}
}

func TestFilesystemToolTreatsProcessExitAsCommandResult(t *testing.T) {
	if _, err := exec.LookPath("false"); err != nil {
		t.Skip("false is not available")
	}
	tool := newFilesystemToolLayer(ConfigFilesystemTool{
		Root:            t.TempDir(),
		AllowedCommands: []string{"false"},
	})

	result, err := tool.RunCommand(context.Background(), ToolCommandRequest{
		Command: "false",
	})
	if err != nil {
		t.Fatalf("RunCommand returned error: %v", err)
	}
	if result.ExitCode != 1 || result.Error != "" {
		t.Fatalf("result = %+v, want process exit code without tool error", result)
	}
}

func TestFilesystemToolRejectsCommandOutsideAllowlist(t *testing.T) {
	tool := newFilesystemToolLayer(ConfigFilesystemTool{Root: t.TempDir()})
	_, err := tool.RunCommand(context.Background(), ToolCommandRequest{
		Command: "sh",
		Args:    []string{"-c", "echo nope"},
	})
	if err == nil {
		t.Fatal("RunCommand should reject commands outside the allowlist")
	}
}

func TestFilesystemToolRejectsSpoofedAbsoluteAllowedCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("executable path semantics differ on Windows")
	}
	root := t.TempDir()
	fakeEcho := filepath.Join(root, "echo")
	if err := os.WriteFile(fakeEcho, []byte("#!/bin/sh\necho spoofed\n"), 0755); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	tool := newFilesystemToolLayer(ConfigFilesystemTool{Root: root})

	_, err := tool.RunCommand(context.Background(), ToolCommandRequest{Command: fakeEcho})
	if err == nil {
		t.Fatal("RunCommand should reject absolute paths that spoof allowed command names")
	}
}

func TestFilesystemToolRejectsCommandAbsolutePathArgOutsideRoot(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	tool := newFilesystemToolLayer(ConfigFilesystemTool{Root: t.TempDir()})

	_, err := tool.RunCommand(context.Background(), ToolCommandRequest{
		Command: "cat",
		Args:    []string{outside},
	})
	if err == nil {
		t.Fatal("RunCommand should reject path arguments outside root")
	}
}

func TestFilesystemToolRejectsCommandSymlinkArgOutsideRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink permissions vary on Windows")
	}
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(root, "secret-link.txt")); err != nil {
		t.Fatalf("Symlink returned error: %v", err)
	}
	tool := newFilesystemToolLayer(ConfigFilesystemTool{Root: root})

	_, err := tool.RunCommand(context.Background(), ToolCommandRequest{
		Command: "cat",
		Args:    []string{"secret-link.txt"},
	})
	if err == nil {
		t.Fatal("RunCommand should reject symlink path arguments outside root")
	}
}

func TestFilesystemToolRejectsRecursiveDelete(t *testing.T) {
	tool := newFilesystemToolLayer(ConfigFilesystemTool{Root: t.TempDir()})
	_, err := tool.RunCommand(context.Background(), ToolCommandRequest{
		Command: "rm",
		Args:    []string{"-rf", "anything"},
	})
	if err == nil {
		t.Fatal("RunCommand should reject recursive delete")
	}
}

func TestFilesystemToolRejectsFindExecAndWritePrimaries(t *testing.T) {
	tool := newFilesystemToolLayer(ConfigFilesystemTool{Root: t.TempDir()})
	for _, args := range [][]string{
		{".", "-ok", "echo", "{}", ";"},
		{".", "-okdir", "echo", "{}", ";"},
		{".", "-fprint", "out.txt"},
	} {
		_, err := tool.RunCommand(context.Background(), ToolCommandRequest{
			Command: "find",
			Args:    args,
		})
		if err == nil {
			t.Fatalf("RunCommand should reject find args %v", args)
		}
	}
}

func TestFilesystemToolRejectsRipgrepPreprocessor(t *testing.T) {
	tool := newFilesystemToolLayer(ConfigFilesystemTool{Root: t.TempDir()})
	_, err := tool.RunCommand(context.Background(), ToolCommandRequest{
		Command: "rg",
		Args:    []string{"--pre", "cat", "needle"},
	})
	if err == nil {
		t.Fatal("RunCommand should reject rg --pre")
	}
}

func TestFilesystemToolRejectsCommandEnvOverrides(t *testing.T) {
	tool := newFilesystemToolLayer(ConfigFilesystemTool{Root: t.TempDir()})
	_, err := tool.RunCommand(context.Background(), ToolCommandRequest{
		Command: "pwd",
		Env:     map[string]string{"RG_CONFIG_PATH": "/tmp/atelier-rg-config"},
	})
	if err == nil {
		t.Fatal("RunCommand should reject env overrides")
	}
}

func TestFilesystemToolRejectsPatternFileOutsideRoot(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "patterns.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	tool := newFilesystemToolLayer(ConfigFilesystemTool{Root: t.TempDir()})

	_, err := tool.RunCommand(context.Background(), ToolCommandRequest{
		Command: "rg",
		Args:    []string{"-f", outside},
	})
	if err == nil {
		t.Fatal("RunCommand should reject pattern files outside root")
	}
}

func TestFilesystemToolClampsCommandOutputLimit(t *testing.T) {
	tool := newFilesystemToolLayer(ConfigFilesystemTool{
		Root:           t.TempDir(),
		MaxOutputBytes: maxToolOutputBytes * 10,
	})
	if got := tool.outputLimit(); got != maxToolOutputBytes {
		t.Fatalf("outputLimit = %d, want cap %d", got, maxToolOutputBytes)
	}
}

func TestBuildToolEnvStripsRiskyEnvironment(t *testing.T) {
	t.Setenv("RG_CONFIG_PATH", "/tmp/atelier-rg-config")
	t.Setenv("ATELIER_SECRET", "nope")
	t.Setenv("PATH", "/bin")

	env := strings.Join(buildToolEnv(), "\n")
	if strings.Contains(env, "RG_CONFIG_PATH=") {
		t.Fatal("buildToolEnv should strip RG_CONFIG_PATH")
	}
	if strings.Contains(env, "ATELIER_SECRET=") {
		t.Fatal("buildToolEnv should strip arbitrary env vars")
	}
	if !strings.Contains(env, "PATH=/bin") {
		t.Fatal("buildToolEnv should preserve PATH")
	}
}

func TestFormatCommandSummaryQuotesArguments(t *testing.T) {
	got := formatCommandSummary([]string{"echo", "hello atelier", "line\nbreak"})
	want := `"echo" "hello atelier" "line\nbreak"`
	if got != want {
		t.Fatalf("formatCommandSummary = %q, want %q", got, want)
	}
}

func TestFilesystemToolReadWriteAndListFiles(t *testing.T) {
	root := t.TempDir()
	tool := newFilesystemToolLayer(ConfigFilesystemTool{Root: root})

	writeResult, err := tool.WriteFile(ToolFileWriteRequest{
		Path:    "notes/todo.txt",
		Content: "ship tools",
	})
	if err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	if writeResult.Bytes != len("ship tools") {
		t.Fatalf("written bytes = %d", writeResult.Bytes)
	}

	readResult, err := tool.ReadFile(ToolFileReadRequest{Path: "notes/todo.txt"})
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	if readResult.Content != "ship tools" {
		t.Fatalf("content = %q", readResult.Content)
	}

	listResult, err := tool.ListFiles(ToolFileListRequest{Path: "notes"})
	if err != nil {
		t.Fatalf("ListFiles returned error: %v", err)
	}
	if len(listResult.Entries) != 1 || listResult.Entries[0].Name != "todo.txt" {
		t.Fatalf("entries = %+v", listResult.Entries)
	}
}

func TestFilesystemToolRejectsFileOutsideRoot(t *testing.T) {
	tool := newFilesystemToolLayer(ConfigFilesystemTool{Root: t.TempDir()})
	_, err := tool.WriteFile(ToolFileWriteRequest{
		Path:    "../outside.txt",
		Content: "nope",
	})
	if err == nil {
		t.Fatal("WriteFile should reject paths outside root")
	}
}

func TestFilesystemToolRejectsReadThroughSymlinkOutsideRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink permissions vary on Windows")
	}
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(root, "secret-link.txt")); err != nil {
		t.Fatalf("Symlink returned error: %v", err)
	}

	tool := newFilesystemToolLayer(ConfigFilesystemTool{Root: root})
	_, err := tool.ReadFile(ToolFileReadRequest{Path: "secret-link.txt"})
	if err == nil {
		t.Fatal("ReadFile should reject symlinks outside root")
	}
}

func TestFilesystemToolRejectsWriteThroughSymlinkOutsideRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink permissions vary on Windows")
	}
	root := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("secret"), 0644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	if err := os.Symlink(secret, filepath.Join(root, "secret-link.txt")); err != nil {
		t.Fatalf("Symlink returned error: %v", err)
	}

	tool := newFilesystemToolLayer(ConfigFilesystemTool{Root: root})
	_, err := tool.WriteFile(ToolFileWriteRequest{Path: "secret-link.txt", Content: "changed", Overwrite: true})
	if err == nil {
		t.Fatal("WriteFile should reject symlink targets outside root")
	}
	content, readErr := os.ReadFile(secret)
	if readErr != nil {
		t.Fatalf("ReadFile returned error: %v", readErr)
	}
	if string(content) != "secret" {
		t.Fatalf("outside file = %q, want unchanged", string(content))
	}
}

func TestFilesystemToolRejectsWriteUnderSymlinkedDirectoryOutsideRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink permissions vary on Windows")
	}
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "outside-dir")); err != nil {
		t.Fatalf("Symlink returned error: %v", err)
	}

	tool := newFilesystemToolLayer(ConfigFilesystemTool{Root: root})
	_, err := tool.WriteFile(ToolFileWriteRequest{Path: "outside-dir/new.txt", Content: "nope"})
	if err == nil {
		t.Fatal("WriteFile should reject writes under symlinked directories outside root")
	}
	if _, statErr := os.Stat(filepath.Join(outside, "new.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("outside file was created, stat err = %v", statErr)
	}
}

func TestToolGatewayDeniesWriteFileBeforeExecution(t *testing.T) {
	root := t.TempDir()
	var permissionEvent ToolPermissionRequestEvent
	gateway := ToolGateway{
		registry: filesystemToolRegistry(),
		tools:    newHarnessToolExecutionContext(AppConfig{Tools: ConfigTools{Filesystem: ConfigFilesystemTool{Root: root}}}),
		permissionRequester: func(_ context.Context, event ToolPermissionRequestEvent) bool {
			permissionEvent = event
			return false
		},
	}

	result := gateway.Execute(context.Background(), ToolExecutionRequest{
		Name: "write_file",
		Call: HarnessToolCall{
			Name:    "write_file",
			Path:    "blocked.txt",
			Content: "nope",
		},
		RequestID:      "request-1",
		ConversationID: "conversation-1",
		Source:         "test",
	})
	if result.Status != "denied" {
		t.Fatalf("gateway result = %+v, want denied", result)
	}
	if permissionEvent.ToolName != "write_file" || permissionEvent.RequestID != "request-1" || permissionEvent.ConversationID != "conversation-1" {
		t.Fatalf("permission event = %+v, want write_file request metadata", permissionEvent)
	}
	if _, err := os.Stat(filepath.Join(root, "blocked.txt")); !os.IsNotExist(err) {
		t.Fatalf("blocked write touched disk, stat err = %v", err)
	}
}

func TestToolGatewayDoesNotRequestPermissionForReadFile(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "status.txt"), []byte("green"), 0644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	permissionCalled := false
	gateway := ToolGateway{
		registry: filesystemToolRegistry(),
		tools:    newHarnessToolExecutionContext(AppConfig{Tools: ConfigTools{Filesystem: ConfigFilesystemTool{Root: root}}}),
		permissionRequester: func(context.Context, ToolPermissionRequestEvent) bool {
			permissionCalled = true
			return false
		},
	}

	result := gateway.Execute(context.Background(), ToolExecutionRequest{
		Name: "read_file",
		Call: HarnessToolCall{Name: "read_file", Path: "status.txt"},
	})
	if result.Status != "completed" {
		t.Fatalf("gateway result = %+v, want completed", result)
	}
	if permissionCalled {
		t.Fatal("read_file should not request permission")
	}
	output, ok := result.Result.(ToolFileReadResult)
	if !ok || output.Content != "green" {
		t.Fatalf("read result = %+v, want file content", result.Result)
	}
}

func TestToolGatewayDoesNotRequestPermissionForReadOnlyCommand(t *testing.T) {
	root := t.TempDir()
	permissionCalled := false
	gateway := ToolGateway{
		registry: filesystemToolRegistry(),
		tools: newHarnessToolExecutionContext(AppConfig{Tools: ConfigTools{Filesystem: ConfigFilesystemTool{
			Root:            root,
			AllowedCommands: []string{"pwd"},
		}}}),
		permissionRequester: func(context.Context, ToolPermissionRequestEvent) bool {
			permissionCalled = true
			return false
		},
	}

	result := gateway.Execute(context.Background(), ToolExecutionRequest{
		Name: "run_command",
		Call: HarnessToolCall{Name: "run_command", Command: "pwd"},
	})
	if result.Status != "completed" {
		t.Fatalf("gateway result = %+v, want completed", result)
	}
	if permissionCalled {
		t.Fatal("read-only run_command should not request permission")
	}
	output, ok := result.Result.(ToolCommandResult)
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("EvalSymlinks returned error: %v", err)
	}
	realStdout, err := filepath.EvalSymlinks(strings.TrimSpace(output.Stdout))
	if err != nil {
		t.Fatalf("EvalSymlinks returned error: %v", err)
	}
	if !ok || output.ExitCode != 0 || realStdout != realRoot {
		t.Fatalf("command result = %+v, want pwd output", result.Result)
	}
}

func TestToolGatewayTreatsProcessExitAsCompletedCommandResult(t *testing.T) {
	if _, err := exec.LookPath("false"); err != nil {
		t.Skip("false is not available")
	}
	gateway := ToolGateway{
		registry: filesystemToolRegistry(),
		tools: newHarnessToolExecutionContext(AppConfig{Tools: ConfigTools{Filesystem: ConfigFilesystemTool{
			Root:            t.TempDir(),
			AllowedCommands: []string{"false"},
		}}}),
		permissionRequester: func(context.Context, ToolPermissionRequestEvent) bool {
			return true
		},
	}

	result := gateway.Execute(context.Background(), ToolExecutionRequest{
		Name: "run_command",
		Call: HarnessToolCall{Name: "run_command", Command: "false"},
	})
	if result.Status != "completed" || result.Error != "" || result.Summary != "command exited with code 1" {
		t.Fatalf("gateway result = %+v, want completed command result", result)
	}
}

func TestToolGatewayTreatsSpawnFailureAsFailedToolResult(t *testing.T) {
	gateway := ToolGateway{
		registry: filesystemToolRegistry(),
		tools: newHarnessToolExecutionContext(AppConfig{Tools: ConfigTools{Filesystem: ConfigFilesystemTool{
			Root:            t.TempDir(),
			AllowedCommands: []string{"atelier-command-that-does-not-exist"},
		}}}),
		permissionRequester: func(context.Context, ToolPermissionRequestEvent) bool {
			return true
		},
	}

	result := gateway.Execute(context.Background(), ToolExecutionRequest{
		Name: "run_command",
		Call: HarnessToolCall{Name: "run_command", Command: "atelier-command-that-does-not-exist"},
	})
	if result.Status != "failed" || result.Error == "" {
		t.Fatalf("gateway result = %+v, want failed spawn error", result)
	}
}

func TestRunCommandPermissionClassifierTreatsRipgrepAsReadOnly(t *testing.T) {
	call := HarnessToolCall{Name: "run_command", Command: "rg", Args: []string{"-n", "Atelier", "."}}
	if !isReadOnlyCommandCall(call) {
		t.Fatalf("isReadOnlyCommandCall(%+v) = false, want true", call)
	}
	call.Args = []string{"--pre", "cat", "Atelier"}
	if isReadOnlyCommandCall(call) {
		t.Fatalf("isReadOnlyCommandCall(%+v) = true, want false", call)
	}
}

func TestToolGatewayRequestsPermissionForCustomCommand(t *testing.T) {
	permissionCalled := false
	gateway := ToolGateway{
		registry: filesystemToolRegistry(),
		tools: newHarnessToolExecutionContext(AppConfig{Tools: ConfigTools{Filesystem: ConfigFilesystemTool{
			Root:            t.TempDir(),
			AllowedCommands: []string{"git"},
		}}}),
		permissionRequester: func(context.Context, ToolPermissionRequestEvent) bool {
			permissionCalled = true
			return false
		},
	}

	result := gateway.Execute(context.Background(), ToolExecutionRequest{
		Name: "run_command",
		Call: HarnessToolCall{Name: "run_command", Command: "git", Args: []string{"status"}},
	})
	if result.Status != "denied" {
		t.Fatalf("gateway result = %+v, want denied", result)
	}
	if !permissionCalled {
		t.Fatal("custom run_command should request permission")
	}
}

func TestToolGatewayRunsUnlistedCommandWithLongFlagValue(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	commandPath := filepath.Join(bin, "notesctl")
	if err := os.WriteFile(commandPath, []byte("#!/bin/sh\nprintf 'stored via notesctl\\n'\n"), 0755); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	permissionCalled := false
	gateway := ToolGateway{
		registry: filesystemToolRegistry(),
		tools: newHarnessToolExecutionContext(AppConfig{Tools: ConfigTools{Filesystem: ConfigFilesystemTool{
			Root:            root,
			AllowedCommands: []string{"cat"},
		}}}),
		permissionRequester: func(context.Context, ToolPermissionRequestEvent) bool {
			permissionCalled = true
			return true
		},
	}

	result := gateway.Execute(context.Background(), ToolExecutionRequest{
		Name: "run_command",
		Call: HarnessToolCall{Name: "run_command", Command: "notesctl", Args: []string{"post", "--content", "A URL like http://example.test/path should not be treated as a local path.", "--wait"}},
	})
	if result.Status != "completed" || result.Error != "" {
		t.Fatalf("gateway result = %+v, want completed unlisted command", result)
	}
	if !permissionCalled {
		t.Fatal("unlisted command should request permission")
	}
	output := result.Result.(ToolCommandResult)
	if strings.TrimSpace(output.Stdout) != "stored via notesctl" {
		t.Fatalf("command stdout = %q, want fake command output", output.Stdout)
	}
}

func TestDefaultCommandToolTimeoutIsThreeMinutes(t *testing.T) {
	if defaultToolTimeoutMS != 3*60*1000 {
		t.Fatalf("defaultToolTimeoutMS = %d, want 180000", defaultToolTimeoutMS)
	}
	if got := defaultAppConfig().Tools.Filesystem.TimeoutMS; got != defaultToolTimeoutMS {
		t.Fatalf("default config timeout = %d, want %d", got, defaultToolTimeoutMS)
	}
}

func TestToolGatewayDeniesUnlistedCommandWithoutPermission(t *testing.T) {
	root := t.TempDir()
	gateway := ToolGateway{
		registry: filesystemToolRegistry(),
		tools: newHarnessToolExecutionContext(AppConfig{Tools: ConfigTools{Filesystem: ConfigFilesystemTool{
			Root:            root,
			AllowedCommands: []string{"cat"},
		}}}),
		permissionRequester: func(context.Context, ToolPermissionRequestEvent) bool {
			return false
		},
	}

	result := gateway.Execute(context.Background(), ToolExecutionRequest{
		Name: "run_command",
		Call: HarnessToolCall{Name: "run_command", Command: "notesctl", Args: []string{"post", "--content", "hello", "--wait"}},
	})
	if result.Status != "denied" {
		t.Fatalf("gateway result = %+v, want denied unlisted command", result)
	}
}

func TestToolGatewayExecutesToolFromSuppliedRegistry(t *testing.T) {
	gateway := ToolGateway{
		registry: newHarnessToolRegistry([]HarnessToolDefinition{
			{
				Name:        "skill_echo",
				Title:       "Skill echo",
				Description: "Test-only skill-backed tool.",
				Example:     `{"name":"skill_echo","content":"hello"}`,
				Risk:        HarnessToolRiskRead,
				Execute: func(_ context.Context, _ HarnessToolExecutionContext, call HarnessToolCall) (any, string, error) {
					return ToolFileReadResult{Path: "skill", Content: call.Content, Bytes: len(call.Content)}, "echoed skill content", nil
				},
				Activity: func(result HarnessToolResult) HarnessToolActivity {
					activity := defaultHarnessToolActivity(result)
					activity.Path = "skill"
					return activity
				},
			},
		}),
	}

	result := gateway.Execute(context.Background(), ToolExecutionRequest{
		Name: "skill_echo",
		Call: HarnessToolCall{Name: "skill_echo", Content: "hello from skill"},
	})
	if result.Status != "completed" || result.Summary != "echoed skill content" {
		t.Fatalf("gateway result = %+v, want completed skill tool", result)
	}
	output, ok := result.Result.(ToolFileReadResult)
	if !ok || output.Content != "hello from skill" {
		t.Fatalf("skill output = %+v, want echoed content", result.Result)
	}
}

func TestToolGatewayFailsClosedWithoutPermissionRequester(t *testing.T) {
	root := t.TempDir()
	gateway := ToolGateway{
		registry: filesystemToolRegistry(),
		tools:    newHarnessToolExecutionContext(AppConfig{Tools: ConfigTools{Filesystem: ConfigFilesystemTool{Root: root}}}),
	}

	result := gateway.Execute(context.Background(), ToolExecutionRequest{
		Name: "write_file",
		Call: HarnessToolCall{Name: "write_file", Path: "blocked.txt", Content: "nope"},
	})
	if result.Status != "denied" {
		t.Fatalf("gateway result = %+v, want denied when no approver is wired", result)
	}
	if _, err := os.Stat(filepath.Join(root, "blocked.txt")); !os.IsNotExist(err) {
		t.Fatalf("blocked write touched disk, stat err = %v", err)
	}
}

func TestToolGatewayRejectsEmptyToolName(t *testing.T) {
	gateway := ToolGateway{
		registry: filesystemToolRegistry(),
		tools:    newHarnessToolExecutionContext(AppConfig{Tools: ConfigTools{Filesystem: ConfigFilesystemTool{Root: t.TempDir()}}}),
		permissionRequester: func(context.Context, ToolPermissionRequestEvent) bool {
			return true
		},
	}

	result := gateway.Execute(context.Background(), ToolExecutionRequest{
		Call: HarnessToolCall{Command: "rm", Args: []string{"-rf", "."}},
	})
	if result.Status != "failed" || result.Error != "tool name is required" {
		t.Fatalf("gateway result = %+v, want failed empty tool name", result)
	}
}

func TestRequestToolPermissionFailsClosedWithoutUI(t *testing.T) {
	app := NewApp()
	if app.requestToolPermission(context.Background(), ToolPermissionRequestEvent{Summary: "Run command"}) {
		t.Fatal("requestToolPermission approved without an attached UI, want fail closed")
	}
}

func TestResolveToolPermissionSignalsDecision(t *testing.T) {
	app := NewApp()
	response := make(chan bool, 1)
	app.permissionsMu.Lock()
	app.permissions["permission-1"] = response
	app.permissionsMu.Unlock()

	if err := app.ResolveToolPermission("permission-1", true); err != nil {
		t.Fatalf("ResolveToolPermission returned error: %v", err)
	}
	select {
	case approved := <-response:
		if !approved {
			t.Fatal("permission decision = false, want true")
		}
	case <-time.After(time.Second):
		t.Fatal("permission decision was not signaled")
	}
	app.permissionsMu.Lock()
	_, exists := app.permissions["permission-1"]
	app.permissionsMu.Unlock()
	if exists {
		t.Fatal("permission should be removed after decision")
	}
}

func TestResolveToolPermissionRejectsMissingRequest(t *testing.T) {
	app := NewApp()
	if err := app.ResolveToolPermission("missing", true); err == nil {
		t.Fatal("ResolveToolPermission should reject missing request")
	}
}

func TestParseHarnessToolPlanAcceptsNoToolDecision(t *testing.T) {
	plan, errors := parseHarnessToolPlan("```json\n{\"brief\":\"Answer from conversation.\",\"needsTools\":false,\"reason\":\"No filesystem context is needed.\",\"toolCalls\":[]}\n```")
	if len(errors) > 0 {
		t.Fatalf("validation errors = %+v", errors)
	}
	if plan.NeedsTools {
		t.Fatal("needsTools = true, want false")
	}
	if len(plan.ToolCalls) != 0 {
		t.Fatalf("toolCalls = %+v, want empty", plan.ToolCalls)
	}
}

func TestParseHarnessToolPlanAcceptsToolDecision(t *testing.T) {
	plan, errors := parseHarnessToolPlan("```json\n{\"brief\":\"Read status.\",\"needsTools\":true,\"reason\":\"Need current file contents.\",\"toolCalls\":[{\"name\":\"read_file\",\"path\":\"status.txt\",\"maxBytes\":20000}]}\n```")
	if len(errors) > 0 {
		t.Fatalf("validation errors = %+v", errors)
	}
	if !plan.NeedsTools || len(plan.ToolCalls) != 1 {
		t.Fatalf("plan = %+v, want one tool call", plan)
	}
	if plan.ToolCalls[0].Name != "read_file" || plan.ToolCalls[0].Path != "status.txt" {
		t.Fatalf("tool call = %+v", plan.ToolCalls[0])
	}
}

func TestParseHarnessToolPlanAcceptsBareStructuredOutput(t *testing.T) {
	plan, errors := parseHarnessToolPlan(`{"brief":"Read status.","needsTools":true,"reason":"Need current file contents.","toolCalls":[{"name":"read_file","path":"status.txt"}]}`)
	if len(errors) > 0 {
		t.Fatalf("validation errors = %+v", errors)
	}
	if !plan.NeedsTools || len(plan.ToolCalls) != 1 {
		t.Fatalf("plan = %+v, want one tool call", plan)
	}
	if plan.ToolCalls[0].Name != "read_file" || plan.ToolCalls[0].Path != "status.txt" {
		t.Fatalf("tool call = %+v", plan.ToolCalls[0])
	}
}

func TestParseHarnessToolPlanRejectsProse(t *testing.T) {
	_, errors := parseHarnessToolPlan("Plan: Call notesctl post --content hello, then report success.")
	if !containsSubstring(errors, "plan JSON could not be parsed") {
		t.Fatalf("validation errors = %+v, want parse failure", errors)
	}
}

func TestStripJSONFenceHandlesFencedAndBareContent(t *testing.T) {
	bare := `{"brief":"x"}`
	if got := stripJSONFence(bare); got != bare {
		t.Fatalf("stripJSONFence(bare) = %q, want unchanged", got)
	}
	if got := stripJSONFence("```json\n" + bare + "\n```"); got != bare {
		t.Fatalf("stripJSONFence(fenced) = %q, want %q", got, bare)
	}
	if got := stripJSONFence("```\n" + bare + "\n```"); got != bare {
		t.Fatalf("stripJSONFence(plain fence) = %q, want %q", got, bare)
	}
}

func TestTruncateChatHistoryKeepsNewestAndMarksOmission(t *testing.T) {
	messages := []ChatMessage{
		{Role: "user", Content: strings.Repeat("a", 400)},
		{Role: "assistant", Content: strings.Repeat("b", 400)},
		{Role: "user", Content: strings.Repeat("c", 400)},
	}

	unchanged := truncateChatHistory(messages, 2000)
	if len(unchanged) != 3 || strings.Contains(unchanged[0].Content, contextOmittedMarker) {
		t.Fatalf("under-budget history = %+v, want unchanged", unchanged)
	}

	truncated := truncateChatHistory(messages, 900)
	if len(truncated) != 2 {
		t.Fatalf("truncated history length = %d, want oldest message dropped", len(truncated))
	}
	if !strings.HasPrefix(truncated[0].Content, contextOmittedMarker) {
		t.Fatalf("oldest kept message = %q, want omission marker prefix", truncated[0].Content)
	}
	if !strings.HasSuffix(truncated[1].Content, "c") || len(truncated[1].Content) != 400 {
		t.Fatalf("newest message = %q, want untouched", truncated[1].Content)
	}

	single := truncateChatHistory([]ChatMessage{{Role: "user", Content: strings.Repeat("d", 5000)}}, 900)
	if len(single) != 1 || strings.Contains(single[0].Content, contextOmittedMarker) {
		t.Fatalf("single oversized message = %+v, want kept without marker", single)
	}
}

func TestToolResultMessagesCapOversizedResults(t *testing.T) {
	huge := HarnessToolResult{
		Name:    "run_command",
		Status:  "completed",
		Summary: "command exited with code 0",
		Result: ToolCommandResult{
			Command: []string{"rg", "-n", "Atelier"},
			Stdout:  strings.Repeat("match line\n", 10000),
		},
	}
	small := HarnessToolResult{Name: "read_file", Status: "completed", Summary: "read 5 bytes", Result: ToolFileReadResult{Path: "a.txt", Content: "green", Bytes: 5}}

	messages := toolResultMessages([]HarnessToolResult{huge, small})
	if len(messages) != 2 || messages[0].Role != "tool" || messages[1].Role != "tool" {
		t.Fatalf("tool messages = %+v, want two tool messages", messages)
	}
	if len(messages[0].Content) > toolResultMessageMaxChars+1024 {
		t.Fatalf("oversized tool message length = %d, want capped near %d", len(messages[0].Content), toolResultMessageMaxChars)
	}
	if !strings.Contains(messages[0].Content, "truncated to fit the model context") {
		t.Fatalf("oversized tool message = %q, want truncation note", messages[0].Content[:200])
	}
	if !strings.Contains(messages[1].Content, "green") {
		t.Fatalf("small tool message = %q, want full result", messages[1].Content)
	}
}

func TestLoadFullSkillTruncatesOversizedBody(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "SKILL.md")
	body := "---\nname: big\ndescription: Oversized skill.\n---\n\n" + strings.Repeat("instruction line\n", 4096)
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}

	loaded, err := loadFullSkill(SkillIndexEntry{Name: "big", Description: "Oversized skill.", Path: path})
	if err != nil {
		t.Fatalf("loadFullSkill returned error: %v", err)
	}
	if len(loaded.Body) > skillBodyReadLimit+256 {
		t.Fatalf("skill body length = %d, want capped near %d", len(loaded.Body), skillBodyReadLimit)
	}
	if !strings.Contains(loaded.Body, "[SKILL.md truncated") {
		t.Fatal("oversized skill body missing truncation marker")
	}
}

func TestParseHarnessToolPlanRejectsInvalidToolName(t *testing.T) {
	_, errors := parseHarnessToolPlan("```json\n{\"brief\":\"Do it.\",\"needsTools\":true,\"reason\":\"Need a tool.\",\"toolCalls\":[{\"name\":\"delete_all\",\"path\":\".\"}]}\n```")
	if !containsSubstring(errors, "name must be one of") {
		t.Fatalf("validation errors = %+v, want invalid tool name", errors)
	}
}

func TestParseHarnessToolPlanReportsPerElementDecodeErrors(t *testing.T) {
	tests := []struct {
		name      string
		plan      string
		wantError string
	}{
		{
			name:      "args as object",
			plan:      `{"brief":"Run it.","needsTools":true,"reason":"Need a command.","toolCalls":[{"name":"run_command","command":"ls","args":{"recursive":true}}]}`,
			wantError: "toolCalls[0].args must be an array of strings",
		},
		{
			name:      "params nested under args",
			plan:      `{"brief":"List files.","needsTools":true,"reason":"Need a listing.","toolCalls":[{"name":"list_files","args":{"path":""}}]}`,
			wantError: "tool parameters like path go directly on the call object, not nested under args",
		},
		{
			name:      "element not an object",
			plan:      `{"brief":"List files.","needsTools":true,"reason":"Need a listing.","toolCalls":["list_files"]}`,
			wantError: "toolCalls[0] must be a tool call object",
		},
		{
			name:      "toolCalls not an array",
			plan:      `{"brief":"List files.","needsTools":true,"reason":"Need a listing.","toolCalls":"list_files"}`,
			wantError: "toolCalls must be an array of tool call objects",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, errors := parseHarnessToolPlan(tt.plan)
			if !containsSubstring(errors, tt.wantError) {
				t.Fatalf("validation errors = %+v, want %q", errors, tt.wantError)
			}
		})
	}
}

func TestParseHarnessToolPlanRejectsMissingRequiredFields(t *testing.T) {
	_, errors := parseHarnessToolPlan("```json\n{\"brief\":\"Read it.\",\"needsTools\":true,\"reason\":\"Need file.\",\"toolCalls\":[{\"name\":\"read_file\"}]}\n```")
	if !containsSubstring(errors, "path is required") {
		t.Fatalf("validation errors = %+v, want missing path", errors)
	}
}

func TestParseHarnessToolPlanRejectsInconsistentToolDecision(t *testing.T) {
	_, errors := parseHarnessToolPlan("```json\n{\"brief\":\"No tools.\",\"needsTools\":false,\"reason\":\"No tools needed.\",\"toolCalls\":[{\"name\":\"list_files\",\"path\":\".\"}]}\n```")
	if !containsSubstring(errors, "toolCalls must be empty") {
		t.Fatalf("validation errors = %+v, want inconsistent needsTools error", errors)
	}
}

func TestFilesystemToolRegistryProjectsPromptAndValidationNames(t *testing.T) {
	registry := filesystemToolRegistry()
	catalog := registry.PromptCatalog()
	names := registry.NamesCSV()
	for _, definition := range registry.definitions {
		if !strings.Contains(catalog, definition.Name) {
			t.Fatalf("prompt catalog = %q, want tool name %q", catalog, definition.Name)
		}
		if !strings.Contains(catalog, definition.Example) {
			t.Fatalf("prompt catalog = %q, want example %q", catalog, definition.Example)
		}
		if !strings.Contains(names, definition.Name) {
			t.Fatalf("names = %q, want tool name %q", names, definition.Name)
		}
		if definition.Execute == nil {
			t.Fatalf("tool %q missing executor", definition.Name)
		}
		if definition.RequiresPermission() && definition.Permission == nil {
			t.Fatalf("tool %q requires permission but has no permission presenter", definition.Name)
		}
		if definition.Activity == nil {
			t.Fatalf("tool %q missing activity projector", definition.Name)
		}
	}
	_, errors := parseHarnessToolPlan("```json\n{\"brief\":\"Do it.\",\"needsTools\":true,\"reason\":\"Need a tool.\",\"toolCalls\":[{\"name\":\"delete_all\",\"path\":\".\"}]}\n```")
	if !containsSubstring(errors, "name must be one of "+names) {
		t.Fatalf("validation errors = %+v, want registry names %q", errors, names)
	}
}

func TestHarnessToolPlanValidationUsesSuppliedRegistry(t *testing.T) {
	registry := newHarnessToolRegistry([]HarnessToolDefinition{
		{
			Name:        "skill_echo",
			Title:       "Skill echo",
			Description: "Test-only skill-backed tool.",
			Example:     `{"name":"skill_echo","content":"hello"}`,
			Risk:        HarnessToolRiskRead,
		},
	})
	plan, errors := parseHarnessToolPlanWithRegistry("```json\n{\"brief\":\"Use the skill.\",\"needsTools\":true,\"reason\":\"The active skill exposes this tool.\",\"toolCalls\":[{\"name\":\"skill_echo\",\"content\":\"hello\"}]}\n```", registry)
	if len(errors) > 0 {
		t.Fatalf("validation errors = %+v, want supplied registry to accept skill_echo", errors)
	}
	if len(plan.ToolCalls) != 1 || plan.ToolCalls[0].Name != "skill_echo" {
		t.Fatalf("plan = %+v, want skill_echo tool call", plan)
	}

	_, defaultErrors := parseHarnessToolPlan("```json\n{\"brief\":\"Use the skill.\",\"needsTools\":true,\"reason\":\"The default registry does not expose this tool.\",\"toolCalls\":[{\"name\":\"skill_echo\",\"content\":\"hello\"}]}\n```")
	if !containsSubstring(defaultErrors, "name must be one of") {
		t.Fatalf("default validation errors = %+v, want default registry rejection", defaultErrors)
	}
}

func TestRunCommandCatalogSupportsRequestedCommands(t *testing.T) {
	definition, ok := filesystemToolRegistry().Get("run_command")
	if !ok {
		t.Fatal("run_command missing from registry")
	}
	description := strings.ToLower(definition.Description)
	for _, want := range []string{"user", "skill", "provides a command"} {
		if !strings.Contains(description, want) {
			t.Fatalf("run_command description = %q, want %q", definition.Description, want)
		}
	}
	if !strings.Contains(definition.Example, `"command":"rg"`) {
		t.Fatalf("run_command example = %q, want search command example", definition.Example)
	}
}

func TestLoadSkillIndexReadsStandardSkillFrontmatter(t *testing.T) {
	root := t.TempDir()
	skillDir := filepath.Join(root, "cleanup")
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	skillPath := filepath.Join(skillDir, "SKILL.md")
	if err := os.WriteFile(skillPath, []byte("---\nname: cleanup\ndescription: Use when the user asks to clean or refactor code.\n---\n\n# Cleanup\n\nFull body should not be needed for the index."), 0644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}

	index, err := loadSkillIndex([]string{root})
	if err != nil {
		t.Fatalf("loadSkillIndex returned error: %v", err)
	}
	if len(index) != 1 {
		t.Fatalf("index = %+v, want one skill", index)
	}
	if index[0].Name != "cleanup" || index[0].Description != "Use when the user asks to clean or refactor code." || index[0].Path != skillPath {
		t.Fatalf("index entry = %+v, want parsed frontmatter", index[0])
	}
	loaded, err := loadFullSkill(index[0])
	if err != nil {
		t.Fatalf("loadFullSkill returned error: %v", err)
	}
	if !strings.Contains(loaded.Body, "Full body should not be needed for the index.") {
		t.Fatalf("loaded skill body = %q, want full SKILL.md", loaded.Body)
	}
}

func TestLoadSkillIndexFollowsSymlinkedSkillDirectories(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink permissions vary on Windows")
	}
	root := t.TempDir()
	targetRoot := t.TempDir()
	targetSkill := filepath.Join(targetRoot, "memorybank")
	if err := os.MkdirAll(targetSkill, 0755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	skillPath := filepath.Join(targetSkill, "SKILL.md")
	if err := os.WriteFile(skillPath, []byte("---\nname: memorybank\ndescription: Store and retrieve notes.\n---\n\n# Memorybank\n"), 0644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	if err := os.Symlink(targetSkill, filepath.Join(root, "memorybank")); err != nil {
		t.Fatalf("Symlink returned error: %v", err)
	}

	index, err := loadSkillIndex([]string{root})
	if err != nil {
		t.Fatalf("loadSkillIndex returned error: %v", err)
	}
	entry, ok := findSkillByName(index, "memorybank")
	if !ok {
		t.Fatalf("skill index = %+v, want symlinked memorybank skill", index)
	}
	if entry.Path != filepath.Join(root, "memorybank", "SKILL.md") {
		t.Fatalf("skill path = %q, want symlink path", entry.Path)
	}
}

func TestExplicitSkillSelectionMatchesDirectNameMention(t *testing.T) {
	index := []SkillIndexEntry{{Name: "memorybank", Description: "Store and retrieve notes.", Path: "/tmp/memorybank/SKILL.md"}}
	entry, reason, ok := explicitSkillSelection(index, "Post the above information to memorybank.")
	if !ok {
		t.Fatal("explicitSkillSelection did not match direct skill name mention")
	}
	if entry.Name != "memorybank" || !strings.Contains(reason, "mentioned") {
		t.Fatalf("selection = %+v reason %q, want direct memorybank selection", entry, reason)
	}
}

func containsSubstring(values []string, substring string) bool {
	for _, value := range values {
		if strings.Contains(value, substring) {
			return true
		}
	}
	return false
}

func TestOllamaClientChecksStatusAndListsModels(t *testing.T) {
	client := newOllamaClient(&http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			switch req.URL.Path {
			case "/api/version":
				return &http.Response{
					StatusCode: http.StatusOK,
					Status:     "200 OK",
					Body:       io.NopCloser(strings.NewReader(`{"version":"1.2.3"}`)),
					Header:     http.Header{"Content-Type": []string{"application/json"}},
				}, nil
			case "/api/tags":
				return &http.Response{
					StatusCode: http.StatusOK,
					Status:     "200 OK",
					Body: io.NopCloser(strings.NewReader(`{"models":[{
						"name":"llava:7b",
						"modified_at":"2026-01-01T00:00:00Z",
						"size":123,
						"details":{"family":"llama","parameter_size":"7B"}
					},{
						"name":"x/flux2-klein:4b",
						"modified_at":"2026-01-02T00:00:00Z",
						"size":456,
						"details":{"family":"flux","parameter_size":"4B"}
					}]}`)),
					Header: http.Header{"Content-Type": []string{"application/json"}},
				}, nil
			case "/api/show":
				data, _ := io.ReadAll(req.Body)
				if strings.Contains(string(data), "x/flux2-klein:4b") {
					return &http.Response{
						StatusCode: http.StatusOK,
						Status:     "200 OK",
						Body:       io.NopCloser(strings.NewReader(`{"capabilities":["completion","image-generation"],"model_info":{"architecture":"diffusion"}}`)),
						Header:     http.Header{"Content-Type": []string{"application/json"}},
					}, nil
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Status:     "200 OK",
					Body:       io.NopCloser(strings.NewReader(`{"capabilities":["completion","vision"],"model_info":{"gemma3.mm.tokens_per_image":256}}`)),
					Header:     http.Header{"Content-Type": []string{"application/json"}},
				}, nil
			default:
				return &http.Response{
					StatusCode: http.StatusNotFound,
					Status:     "404 Not Found",
					Body:       io.NopCloser(strings.NewReader("not found")),
					Header:     http.Header{},
				}, nil
			}
		}),
	}, "http://ollama.test")

	status := client.Check(context.Background())
	if !status.Online || status.Version != "1.2.3" || status.BaseURL != "http://ollama.test" {
		t.Fatalf("status = %+v, want online 1.2.3 at test endpoint", status)
	}

	models, err := client.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels returned error: %v", err)
	}
	if len(models) != 2 || models[0].Name != "llava:7b" || models[0].Family != "llama" || models[0].Parameter != "7B" {
		t.Fatalf("models = %+v, want parsed ollama model details", models)
	}
	if models[0].ImageGeneration {
		t.Fatalf("vision model should not be marked as image generation: %+v", models[0])
	}
	if !models[1].ImageGeneration {
		t.Fatalf("image-generation model should be marked: %+v", models[1])
	}
}

func TestEnsureStorageDirs(t *testing.T) {
	root := t.TempDir()
	storage := ConfigStorage{
		Root:      filepath.Join(root, ".atelier"),
		History:   filepath.Join(root, ".atelier", "history"),
		Artifacts: filepath.Join(root, ".atelier", "history"),
	}
	if err := ensureStorageDirs(storage); err != nil {
		t.Fatalf("ensureStorageDirs returned error: %v", err)
	}
	for _, path := range []string{
		storage.Root,
		filepath.Join(storage.History, "conversations"),
		filepath.Join(storage.History, "indexes"),
	} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("expected %s to exist: %v", path, err)
		}
		if !info.IsDir() {
			t.Fatalf("%s is not a directory", path)
		}
	}
}

func TestChatConversationWithGeneratedImagesLifecycle(t *testing.T) {
	root := t.TempDir()
	storage := ConfigStorage{
		Root:      filepath.Join(root, ".atelier"),
		History:   filepath.Join(root, ".atelier", "history"),
		Artifacts: filepath.Join(root, ".atelier", "history"),
	}
	config := defaultAppConfig()
	config.Storage = storage
	if err := ensureStorageDirs(storage); err != nil {
		t.Fatalf("ensureStorageDirs returned error: %v", err)
	}

	conversationID, err := writePendingChatConversation(config, ChatRequest{
		Model: "chat-model",
		Messages: []ChatMessage{
			{Role: "user", Content: "Paint a small house"},
		},
	})
	if err != nil {
		t.Fatalf("writePendingChatConversation returned error: %v", err)
	}

	err = appendChatAssistantTurnWithImages(
		config,
		conversationID,
		"Here is the generated image.",
		"",
		"chat-model",
		"stop",
		[]string{"data:image/png;base64,iVBORw0KGgo="},
		"{}",
		fallbackHarnessRun("chat-model", "stop", 0),
		ImageGenerateRequest{Model: "image-model", Prompt: "Paint a small house", Width: 64, Height: 64, Steps: 2},
	)
	if err != nil {
		t.Fatalf("appendChatAssistantTurnWithImages returned error: %v", err)
	}

	conversations, err := listConversations(storage)
	if err != nil {
		t.Fatalf("listConversations returned error: %v", err)
	}
	if len(conversations) != 1 {
		t.Fatalf("conversation count = %d, want 1", len(conversations))
	}
	if conversations[0].ID != conversationID {
		t.Fatalf("conversation id = %q, want %q", conversations[0].ID, conversationID)
	}
	if conversations[0].Kind != "chat" {
		t.Fatalf("conversation kind = %q, want chat", conversations[0].Kind)
	}

	if err := deleteConversation(storage, conversationID); err != nil {
		t.Fatalf("deleteConversation returned error: %v", err)
	}
	conversations, err = listConversations(storage)
	if err != nil {
		t.Fatalf("listConversations after delete returned error: %v", err)
	}
	if len(conversations) != 0 {
		t.Fatalf("conversation count after delete = %d, want 0", len(conversations))
	}
}

func TestChatImageAssistantTurnStoresToolMetadata(t *testing.T) {
	root := t.TempDir()
	storage := ConfigStorage{
		Root:      filepath.Join(root, ".atelier"),
		History:   filepath.Join(root, ".atelier", "history"),
		Artifacts: filepath.Join(root, ".atelier", "history"),
	}
	config := defaultAppConfig()
	config.Storage = storage
	if err := ensureStorageDirs(storage); err != nil {
		t.Fatalf("ensureStorageDirs returned error: %v", err)
	}

	conversationID, err := writePendingChatConversation(config, ChatRequest{
		Model: "chat-model",
		Messages: []ChatMessage{
			{Role: "user", Content: "Paint early"},
		},
	})
	if err != nil {
		t.Fatalf("writePendingChatConversation returned error: %v", err)
	}
	conversations, err := listConversations(storage)
	if err != nil {
		t.Fatalf("listConversations returned error: %v", err)
	}
	if len(conversations) != 1 || conversations[0].ID != conversationID {
		t.Fatalf("conversations = %+v, want pending chat conversation %s", conversations, conversationID)
	}
	detail, err := getConversation(storage, conversationID)
	if err != nil {
		t.Fatalf("getConversation returned error: %v", err)
	}
	if len(detail.Turns) != 1 || detail.Turns[0].Role != "user" {
		t.Fatalf("turns after pending write = %+v, want one user turn", detail.Turns)
	}

	err = appendChatAssistantTurnWithImages(
		config,
		conversationID,
		"Generated it.",
		"",
		"chat-model",
		"stop",
		[]string{"data:image/png;base64,iVBORw0KGgo="},
		"{}",
		fallbackHarnessRun("chat-model", "stop", 0),
		ImageGenerateRequest{Model: "image-model", Prompt: "Paint early", Width: 64, Height: 64, Steps: 2},
	)
	if err != nil {
		t.Fatalf("appendChatAssistantTurnWithImages returned error: %v", err)
	}
	detail, err = getConversation(storage, conversationID)
	if err != nil {
		t.Fatalf("getConversation after result returned error: %v", err)
	}
	if len(detail.Turns) != 2 || detail.Turns[1].Role != "assistant" {
		t.Fatalf("turns after result = %+v, want user and assistant", detail.Turns)
	}
	if detail.Conversation.Stats.ArtifactCount != 1 {
		t.Fatalf("artifact count = %d, want 1", detail.Conversation.Stats.ArtifactCount)
	}
	tool, ok := detail.Turns[1].ProviderResponse["tool"].(map[string]any)
	if !ok || tool["name"] != "image_generation" || tool["model"] != "image-model" {
		t.Fatalf("assistant tool metadata = %+v, want image_generation via image-model", detail.Turns[1].ProviderResponse["tool"])
	}
}

func TestPurgeArchivedConversationsRemovesOnlySoftDeletedFolders(t *testing.T) {
	root := t.TempDir()
	storage := ConfigStorage{
		Root:      filepath.Join(root, ".atelier"),
		History:   filepath.Join(root, ".atelier", "history"),
		Artifacts: filepath.Join(root, ".atelier", "history"),
	}
	config := defaultAppConfig()
	config.Storage = storage
	if err := ensureStorageDirs(storage); err != nil {
		t.Fatalf("ensureStorageDirs returned error: %v", err)
	}

	archivedID, err := writePendingChatConversation(config, ChatRequest{
		Model: "chat-model",
		Messages: []ChatMessage{
			{Role: "user", Content: "Archive me", Images: []string{"data:image/png;base64,iVBORw0KGgo="}},
		},
	})
	if err != nil {
		t.Fatalf("write archived conversation returned error: %v", err)
	}
	activeID, err := writePendingChatConversation(config, ChatRequest{
		Model: "chat-model",
		Messages: []ChatMessage{
			{Role: "user", Content: "Keep me", Images: []string{"data:image/png;base64,iVBORw0KGgo="}},
		},
	})
	if err != nil {
		t.Fatalf("write active conversation returned error: %v", err)
	}
	archivedPath, err := findConversationPath(storage, archivedID)
	if err != nil {
		t.Fatalf("find archived conversation returned error: %v", err)
	}
	activePath, err := findConversationPath(storage, activeID)
	if err != nil {
		t.Fatalf("find active conversation returned error: %v", err)
	}
	archivedDir := filepath.Dir(archivedPath)
	activeDir := filepath.Dir(activePath)

	if err := deleteConversation(storage, archivedID); err != nil {
		t.Fatalf("deleteConversation returned error: %v", err)
	}
	result, err := purgeArchivedConversations(storage)
	if err != nil {
		t.Fatalf("purgeArchivedConversations returned error: %v", err)
	}
	if result.DeletedConversations != 1 {
		t.Fatalf("deleted conversations = %d, want 1", result.DeletedConversations)
	}
	if result.DeletedAssets != 1 {
		t.Fatalf("deleted assets = %d, want 1", result.DeletedAssets)
	}
	if _, err := os.Stat(archivedDir); !os.IsNotExist(err) {
		t.Fatalf("archived dir still exists or stat failed differently: %v", err)
	}
	if _, err := os.Stat(activeDir); err != nil {
		t.Fatalf("active dir stat returned error: %v", err)
	}
}

func TestChatConversationLifecycle(t *testing.T) {
	root := t.TempDir()
	storage := ConfigStorage{
		Root:      filepath.Join(root, ".atelier"),
		History:   filepath.Join(root, ".atelier", "history"),
		Artifacts: filepath.Join(root, ".atelier", "history"),
	}
	config := defaultAppConfig()
	config.Storage = storage
	if err := ensureStorageDirs(storage); err != nil {
		t.Fatalf("ensureStorageDirs returned error: %v", err)
	}

	conversationID, err := writeChatConversation(
		config,
		ChatRequest{
			Model:  "chat-model",
			System: "Be useful.",
			Messages: []ChatMessage{
				{Role: "user", Content: "Explain markdown tables"},
			},
		},
		"Here is a table.",
		"",
		"chat-model",
		"stop",
		12,
		"Markdown Tables",
	)
	if err != nil {
		t.Fatalf("writeChatConversation returned error: %v", err)
	}

	conversations, err := listConversations(storage)
	if err != nil {
		t.Fatalf("listConversations returned error: %v", err)
	}
	if len(conversations) != 1 {
		t.Fatalf("conversation count = %d, want 1", len(conversations))
	}
	if conversations[0].ID != conversationID {
		t.Fatalf("conversation id = %q, want %q", conversations[0].ID, conversationID)
	}
	if conversations[0].Kind != "chat" {
		t.Fatalf("conversation kind = %q, want chat", conversations[0].Kind)
	}
	if conversations[0].Title != "Markdown Tables" {
		t.Fatalf("conversation title = %q, want Markdown Tables", conversations[0].Title)
	}

	updated, err := updateConversationTitle(storage, conversationID, "Edited Title")
	if err != nil {
		t.Fatalf("updateConversationTitle returned error: %v", err)
	}
	if updated.Title != "Edited Title" {
		t.Fatalf("updated title = %q, want Edited Title", updated.Title)
	}

	detail, err := getConversation(storage, conversationID)
	if err != nil {
		t.Fatalf("getConversation returned error: %v", err)
	}
	if detail.Conversation.ID != conversationID {
		t.Fatalf("detail conversation id = %q, want %q", detail.Conversation.ID, conversationID)
	}
	if len(detail.Turns) != 2 {
		t.Fatalf("detail turn count = %d, want 2", len(detail.Turns))
	}
	if detail.Turns[0].Role != "user" || detail.Turns[1].Role != "assistant" {
		t.Fatalf("turn roles = %q/%q, want user/assistant", detail.Turns[0].Role, detail.Turns[1].Role)
	}
	harnessRun, ok := detail.Turns[1].ProviderResponse["harnessRun"].(map[string]any)
	if !ok {
		t.Fatalf("assistant provider response missing harness run: %+v", detail.Turns[1].ProviderResponse)
	}
	if harnessRun["mode"] != "chat" || harnessRun["status"] != "completed" {
		t.Fatalf("harness run = %+v, want completed chat run", harnessRun)
	}
	loop, ok := harnessRun["loop"].(map[string]any)
	if !ok {
		t.Fatalf("harness run missing loop metadata: %+v", harnessRun)
	}
	if loop["maxSteps"] != float64(3) || loop["iterations"] != float64(1) || loop["stopReason"] != "final" {
		t.Fatalf("harness loop = %+v, want final single-iteration loop", loop)
	}
	steps, ok := harnessRun["steps"].([]any)
	if !ok || len(steps) != 1 {
		t.Fatalf("harness steps = %+v, want single honest saved step for a turn without live telemetry", harnessRun["steps"])
	}
	savedStep := harnessStepByKind(t, steps, "saved")
	if savedStep["status"] != "completed" || savedStep["tokens"] != float64(12) || savedStep["model"] != "chat-model" {
		t.Fatalf("saved step = %+v, want completed save metadata", savedStep)
	}
	if len(detail.Turns[0].Content) != 1 || detail.Turns[0].Content[0].Text != "Explain markdown tables" {
		t.Fatalf("initial user content = %+v, want text prompt", detail.Turns[0].Content)
	}

	appendedID, err := appendChatConversation(
		config,
		ChatRequest{
			ConversationID: conversationID,
			Model:          "chat-model",
			Messages: []ChatMessage{
				{Role: "user", Content: "Add one more example"},
			},
		},
		"Here is another example.",
		"",
		"chat-model",
		"stop",
		8,
	)
	if err != nil {
		t.Fatalf("appendChatConversation returned error: %v", err)
	}
	if appendedID != conversationID {
		t.Fatalf("appended id = %q, want %q", appendedID, conversationID)
	}
	detail, err = getConversation(storage, conversationID)
	if err != nil {
		t.Fatalf("getConversation after append returned error: %v", err)
	}
	if len(detail.Turns) != 4 {
		t.Fatalf("detail turn count after append = %d, want 4", len(detail.Turns))
	}
	if detail.Conversation.Stats.TurnCount != 4 {
		t.Fatalf("conversation turn count after append = %d, want 4", detail.Conversation.Stats.TurnCount)
	}
}

func TestHarnessRunChatStreamRecordsHistory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	config := defaultAppConfig()
	config.Storage = ConfigStorage{
		Root:      filepath.Join(home, ".atelier"),
		History:   filepath.Join(home, ".atelier", "history"),
		Artifacts: filepath.Join(home, ".atelier", "history"),
	}
	config.Providers.Ollama.BaseURL = "http://ollama.test"
	config.Providers.Ollama.Models.Chat = "chat-box-model"
	config.Providers.Ollama.Models.Tools = "harness-model"
	if err := writeAppConfig(config); err != nil {
		t.Fatalf("writeAppConfig returned error: %v", err)
	}

	var requestedModels []string
	app := NewApp()
	app.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/api/chat" {
			return &http.Response{
				StatusCode: http.StatusNotFound,
				Status:     "404 Not Found",
				Body:       io.NopCloser(strings.NewReader("not found")),
				Header:     http.Header{},
			}, nil
		}
		var payload map[string]any
		data, _ := io.ReadAll(req.Body)
		if err := json.Unmarshal(data, &payload); err != nil {
			t.Fatalf("provider request body is not JSON: %v", err)
		}
		requestedModel, _ := payload["model"].(string)
		requestedModels = append(requestedModels, requestedModel)
		options, _ := payload["options"].(map[string]any)
		if options == nil || options["num_ctx"] != float64(defaultOllamaNumCtx) {
			t.Fatalf("request options = %+v, want num_ctx %d on every call", payload["options"], defaultOllamaNumCtx)
		}
		if requestedModel == "harness-model" {
			t.Fatalf("tool model should never run on the direct-answer path, got request for %q", requestedModel)
		}
		const wantSystem = "You are Atelier, a precise local AI collaborator."
		if payload["stream"] == false {
			if requestedModel != "chat-box-model" {
				t.Fatalf("triage model = %q, want chat-box-model", requestedModel)
			}
			if payload["format"] == nil {
				t.Fatal("triage request missing structured output format")
			}
			decision := `{"needsTools":false,"toolTask":"","reason":"General knowledge answer."}`
			body := `{"model":"chat-box-model","message":{"role":"assistant","content":` + strconv.Quote(decision) + `},"done":true,"done_reason":"stop","eval_count":2}`
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Body:       io.NopCloser(strings.NewReader(body)),
				Header:     http.Header{"Content-Type": []string{"application/json"}},
			}, nil
		}
		if requestedModel != "chat-box-model" {
			t.Fatalf("response stream model = %q, want chat-box-model", requestedModel)
		}
		// Assert the direct path leaves the system prompt untouched: the Ollama
		// client prepends req.System as messages[0] with role "system".
		msgs, _ := payload["messages"].([]any)
		if len(msgs) == 0 {
			t.Fatalf("streaming call messages is empty, want at least a system message")
		}
		firstMsg, _ := msgs[0].(map[string]any)
		if firstMsg["role"] != "system" || firstMsg["content"] != wantSystem {
			t.Fatalf("streaming call messages[0] = %+v, want role=system content=%q", firstMsg, wantSystem)
		}
		body := fmt.Sprintln(`{"model":"chat-box-model","message":{"role":"assistant","content":"Hello from selected chat model.","thinking":"Final model thought."},"done":false}`) +
			fmt.Sprintln(`{"model":"chat-box-model","done":true,"done_reason":"stop","eval_count":3}`)
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     http.Header{"Content-Type": []string{"application/x-ndjson"}},
		}, nil
	})
	app.runChatStream(context.Background(), "request-1", ChatRequest{
		BaseURL: "http://ollama.test",
		Model:   "chat-box-model",
		System:  "You are Atelier, a precise local AI collaborator.",
		Messages: []ChatMessage{
			{Role: "user", Content: "Say hello"},
		},
	})
	if strings.Join(requestedModels, ",") != "chat-box-model,chat-box-model" {
		t.Fatalf("provider request models = %v, want triage then chat model stream", requestedModels)
	}

	conversations, err := listConversations(config.Storage)
	if err != nil {
		t.Fatalf("listConversations returned error: %v", err)
	}
	if len(conversations) != 1 {
		t.Fatalf("conversation count = %d, want 1", len(conversations))
	}
	detail, err := getConversation(config.Storage, conversations[0].ID)
	if err != nil {
		t.Fatalf("getConversation returned error: %v", err)
	}
	if got := detail.Turns[1].Content[0].Text; got != "Hello from selected chat model." {
		t.Fatalf("assistant content = %q, want streamed content", got)
	}
	if got := detail.Turns[0].Request["model"]; got != "chat-box-model" {
		t.Fatalf("user turn request model = %q, want chat-box-model", got)
	}
	if got := detail.Turns[1].Model; got != "chat-box-model" {
		t.Fatalf("assistant turn model = %q, want chat-box-model", got)
	}
	if thinking := historyTextForTest(detail.Turns[1].Content, "thinking"); !strings.Contains(thinking, "Final model thought.") || strings.Contains(thinking, "Harness preparation") {
		t.Fatalf("assistant thinking = %q, want final model thinking and no harness prep", thinking)
	}
	harnessRun, ok := detail.Turns[1].ProviderResponse["harnessRun"].(map[string]any)
	if !ok {
		t.Fatalf("assistant provider response missing harness run: %+v", detail.Turns[1].ProviderResponse)
	}
	triage, ok := harnessRun["triage"].(map[string]any)
	if !ok || triage["needsTools"] != false {
		t.Fatalf("harness run triage = %+v, want needsTools false", harnessRun["triage"])
	}
	loop, ok := harnessRun["loop"].(map[string]any)
	if !ok {
		t.Fatalf("harness run missing loop metadata: %+v", harnessRun)
	}
	if loop["iterations"] != float64(0) {
		t.Fatalf("harness loop iterations = %v, want 0 on direct-answer path (tool planner never ran)", loop["iterations"])
	}
	steps, ok := harnessRun["steps"].([]any)
	if !ok || len(steps) != 6 {
		t.Fatalf("harness steps = %+v, want full lifecycle timeline", harnessRun["steps"])
	}
	triageStep := harnessStepByKind(t, steps, "triage")
	if triageStep["status"] != "completed" || triageStep["model"] != "chat-box-model" {
		t.Fatalf("triage step = %+v, want completed triage on chat model", triageStep)
	}
	streaming := harnessStepByKind(t, steps, "streaming")
	if streaming["status"] != "completed" || streaming["tokens"] != float64(3) {
		t.Fatalf("streaming step = %+v, want completed streaming metadata", streaming)
	}
}

func TestTriageFailureStillRunsToolPlannerAndAnswers(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	config := defaultAppConfig()
	config.Storage = ConfigStorage{
		Root:      filepath.Join(home, ".atelier"),
		History:   filepath.Join(home, ".atelier", "history"),
		Artifacts: filepath.Join(home, ".atelier", "history"),
	}
	config.Providers.Ollama.BaseURL = "http://ollama.test"
	config.Providers.Ollama.Models.Chat = "chat-box-model"
	config.Providers.Ollama.Models.Tools = "harness-model"
	if err := writeAppConfig(config); err != nil {
		t.Fatalf("writeAppConfig returned error: %v", err)
	}

	var requestedModels []string
	app := NewApp()
	app.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/api/chat" {
			return &http.Response{
				StatusCode: http.StatusNotFound,
				Status:     "404 Not Found",
				Body:       io.NopCloser(strings.NewReader("not found")),
				Header:     http.Header{},
			}, nil
		}
		var payload map[string]any
		data, _ := io.ReadAll(req.Body)
		if err := json.Unmarshal(data, &payload); err != nil {
			t.Fatalf("provider request body is not JSON: %v", err)
		}
		requestedModel, _ := payload["model"].(string)
		requestedModels = append(requestedModels, requestedModel)
		if payload["stream"] == false {
			if requestedModel == "chat-box-model" {
				return &http.Response{
					StatusCode: http.StatusInternalServerError,
					Status:     "500 Internal Server Error",
					Body:       io.NopCloser(strings.NewReader(`{"error":"triage model unavailable"}`)),
					Header:     http.Header{"Content-Type": []string{"application/json"}},
				}, nil
			}
			plan := `{"brief":"Answer directly.","needsTools":false,"reason":"No tools needed.","toolCalls":[]}`
			body := `{"model":"harness-model","message":{"role":"assistant","content":` + strconv.Quote(plan) + `},"done":true,"done_reason":"stop","eval_count":2}`
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Body:       io.NopCloser(strings.NewReader(body)),
				Header:     http.Header{"Content-Type": []string{"application/json"}},
			}, nil
		}
		body := fmt.Sprintln(`{"model":"chat-box-model","message":{"role":"assistant","content":"Answer despite triage failure."},"done":false}`) +
			fmt.Sprintln(`{"model":"chat-box-model","done":true,"done_reason":"stop","eval_count":3}`)
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     http.Header{"Content-Type": []string{"application/x-ndjson"}},
		}, nil
	})

	app.runChatStream(context.Background(), "request-triage-fail", ChatRequest{
		BaseURL: "http://ollama.test",
		Model:   "chat-box-model",
		Messages: []ChatMessage{
			{Role: "user", Content: "What can you do?"},
		},
	})

	if strings.Join(requestedModels, ",") != "chat-box-model,harness-model,chat-box-model" {
		t.Fatalf("provider request models = %v, want triage, planner, then chat stream", requestedModels)
	}

	conversations, err := listConversations(config.Storage)
	if err != nil {
		t.Fatalf("listConversations returned error: %v", err)
	}
	detail, err := getConversation(config.Storage, conversations[0].ID)
	if err != nil {
		t.Fatalf("getConversation returned error: %v", err)
	}
	if got := detail.Turns[1].Content[0].Text; got != "Answer despite triage failure." {
		t.Fatalf("assistant content = %q, want streamed answer after fail-safe", got)
	}
	harnessRun, ok := detail.Turns[1].ProviderResponse["harnessRun"].(map[string]any)
	if !ok {
		t.Fatalf("assistant provider response missing harness run: %+v", detail.Turns[1].ProviderResponse)
	}
	triage, ok := harnessRun["triage"].(map[string]any)
	if !ok {
		t.Fatalf("harness run missing triage: %+v", harnessRun["triage"])
	}
	if errText, _ := triage["error"].(string); strings.TrimSpace(errText) == "" {
		t.Fatalf("triage error = %q, want recorded triage failure", errText)
	}
	if triage["needsTools"] != true {
		t.Fatalf("triage needsTools = %v, want fail-safe to the tool path", triage["needsTools"])
	}
	steps, ok := harnessRun["steps"].([]any)
	if !ok {
		t.Fatalf("harness run missing steps: %+v", harnessRun)
	}
	triageStep := harnessStepByKind(t, steps, "triage")
	if triageStep["status"] != "failed" {
		t.Fatalf("triage step status = %q, want failed when triage errored", triageStep["status"])
	}
}

func TestHarnessSelectsSkillLoadsBodyAndPersistsMetadata(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	skillDir := filepath.Join(home, ".atelier", "skills", "cleanup")
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	skillPath := filepath.Join(skillDir, "SKILL.md")
	skillBody := "---\nname: cleanup\ndescription: Use when the user asks to clean or refactor code.\n---\n\n# Cleanup\n\nSKILL BODY UNIQUE: preserve behavior and run tests first."
	if err := os.WriteFile(skillPath, []byte(skillBody), 0644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}

	config := defaultAppConfig()
	config.Storage = ConfigStorage{
		Root:      filepath.Join(home, ".atelier"),
		History:   filepath.Join(home, ".atelier", "history"),
		Artifacts: filepath.Join(home, ".atelier", "history"),
	}
	config.Providers.Ollama.BaseURL = "http://ollama.test"
	config.Providers.Ollama.Models.Chat = "chat-box-model"
	config.Providers.Ollama.Models.Tools = "harness-model"
	if err := writeAppConfig(config); err != nil {
		t.Fatalf("writeAppConfig returned error: %v", err)
	}

	app := NewApp()
	var prepSystem string
	var requestedModels []string
	app.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/api/chat" {
			return &http.Response{
				StatusCode: http.StatusNotFound,
				Status:     "404 Not Found",
				Body:       io.NopCloser(strings.NewReader("not found")),
				Header:     http.Header{},
			}, nil
		}
		var payload map[string]any
		data, _ := io.ReadAll(req.Body)
		if err := json.Unmarshal(data, &payload); err != nil {
			t.Fatalf("provider request body is not JSON: %v", err)
		}
		requestedModel, _ := payload["model"].(string)
		requestedModels = append(requestedModels, requestedModel)
		if payload["stream"] == false {
			if requestedModel != "harness-model" {
				t.Fatalf("explicit skill turn should skip triage; got non-stream call for %q", requestedModel)
			}
			messages, _ := payload["messages"].([]any)
			lastMessage := messages[len(messages)-1].(map[string]any)
			content, _ := lastMessage["content"].(string)
			if strings.Contains(content, "Skill index:") {
				t.Fatal("explicit skill match should skip the LLM skill selector")
			}
			firstMessage := messages[0].(map[string]any)
			prepSystem, _ = firstMessage["content"].(string)
			body := "```json\n{\"brief\":\"Use the selected cleanup skill.\",\"needsTools\":false,\"reason\":\"Skill instructions are enough.\",\"toolCalls\":[]}\n```"
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Body:       io.NopCloser(strings.NewReader(`{"model":"harness-model","message":{"role":"assistant","content":` + strconv.Quote(body) + `},"done":true,"done_reason":"stop","eval_count":3}`)),
				Header:     http.Header{"Content-Type": []string{"application/json"}},
			}, nil
		}
		body := fmt.Sprintln(`{"model":"chat-box-model","message":{"role":"assistant","content":"Cleanup plan ready."},"done":false}`) +
			fmt.Sprintln(`{"model":"chat-box-model","done":true,"done_reason":"stop","eval_count":4}`)
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     http.Header{"Content-Type": []string{"application/x-ndjson"}},
		}, nil
	})

	app.runChatStream(context.Background(), "request-skill", ChatRequest{
		BaseURL: "http://ollama.test",
		Model:   "chat-box-model",
		Messages: []ChatMessage{
			{Role: "user", Content: "Please cleanup this code"},
		},
	})
	if strings.Join(requestedModels, ",") != "harness-model,chat-box-model" {
		t.Fatalf("provider request models = %v, want harness prep, final model", requestedModels)
	}
	if !strings.Contains(prepSystem, "SKILL BODY UNIQUE") {
		t.Fatalf("prep system = %q, want selected SKILL.md body", prepSystem)
	}

	conversations, err := listConversations(config.Storage)
	if err != nil {
		t.Fatalf("listConversations returned error: %v", err)
	}
	detail, err := getConversation(config.Storage, conversations[0].ID)
	if err != nil {
		t.Fatalf("getConversation returned error: %v", err)
	}
	skill, ok := detail.Turns[1].ProviderResponse["skill"].(map[string]any)
	if !ok {
		t.Fatalf("assistant provider response missing skill metadata: %+v", detail.Turns[1].ProviderResponse)
	}
	if skill["name"] != "cleanup" || skill["path"] != skillPath || skill["selected"] != true {
		t.Fatalf("skill metadata = %+v, want selected cleanup", skill)
	}
	harnessRun := detail.Turns[1].ProviderResponse["harnessRun"].(map[string]any)
	runSkill := harnessRun["skill"].(map[string]any)
	if runSkill["name"] != "cleanup" {
		t.Fatalf("harness skill metadata = %+v, want cleanup", runSkill)
	}
	triage, ok := harnessRun["triage"].(map[string]any)
	if !ok || triage["needsTools"] != true || triage["reason"] != "user explicitly referenced a skill" {
		t.Fatalf("harness run triage = %+v, want explicit-skill reason with needsTools true", harnessRun["triage"])
	}
}

func TestHarnessExecutesSkillCommandInsteadOfDelegatingToFinalModel(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	bin := filepath.Join(home, "bin")
	if err := os.MkdirAll(bin, 0755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	commandLog := filepath.Join(home, "notesctl-args.txt")
	commandPath := filepath.Join(bin, "notesctl")
	commandScript := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + strconv.Quote(commandLog) + "\nprintf 'stored/path.md\\n'\n"
	if err := os.WriteFile(commandPath, []byte(commandScript), 0755); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	skillDir := filepath.Join(home, ".agents", "skills", "memorybank")
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: memorybank\ndescription: Store notes using notesctl.\n---\n\nUse notesctl post --content TEXT --wait to store content."), 0644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}

	config := defaultAppConfig()
	config.Storage = ConfigStorage{
		Root:      filepath.Join(home, ".atelier"),
		History:   filepath.Join(home, ".atelier", "history"),
		Artifacts: filepath.Join(home, ".atelier", "history"),
	}
	config.Tools.Filesystem.Root = filepath.Join(home, "workspace")
	config.Providers.Ollama.BaseURL = "http://ollama.test"
	config.Providers.Ollama.Models.Chat = "chat-box-model"
	config.Providers.Ollama.Models.Tools = "harness-model"
	if err := os.MkdirAll(config.Tools.Filesystem.Root, 0755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	if err := writeAppConfig(config); err != nil {
		t.Fatalf("writeAppConfig returned error: %v", err)
	}

	app := NewApp()
	var approvedCommands [][]string
	app.toolPermission = func(_ context.Context, event ToolPermissionRequestEvent) bool {
		approvedCommands = append(approvedCommands, event.Command)
		return true
	}
	prepCalls := 0
	var responseSystem string
	var repairPrompt string
	var streamMessages []any
	app.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/api/chat" {
			return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader("not found"))}, nil
		}
		var payload map[string]any
		data, _ := io.ReadAll(req.Body)
		if err := json.Unmarshal(data, &payload); err != nil {
			t.Fatalf("provider request body is not JSON: %v", err)
		}
		requestedModel, _ := payload["model"].(string)
		if payload["stream"] == false {
			if requestedModel != "harness-model" {
				t.Fatalf("explicit skill turn should skip triage; got non-stream call for %q", requestedModel)
			}
			prepCalls++
			if prepCalls == 1 {
				body := "The user wants to store the previous answer using the selected skill: notesctl post --content \"...\" --wait."
				return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: io.NopCloser(strings.NewReader(`{"model":"harness-model","message":{"role":"assistant","content":` + strconv.Quote(body) + `},"done":true,"done_reason":"stop","eval_count":2}`)), Header: http.Header{"Content-Type": []string{"application/json"}}}, nil
			}
			if prepCalls == 2 {
				messages := payload["messages"].([]any)
				lastMessage := messages[len(messages)-1].(map[string]any)
				repairPrompt, _ = lastMessage["content"].(string)
				body := `{"brief":"Report the stored note path from the tool output.","needsTools":true,"reason":"The selected skill requires running notesctl post.","toolCalls":[{"name":"run_command","command":"notesctl","args":["post","--content","Blue can indicate clear shallow water. Green often means algae. Brown often means suspended sediment.","--wait"]}]}`
				return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: io.NopCloser(strings.NewReader(`{"model":"harness-model","message":{"role":"assistant","content":` + strconv.Quote(body) + `},"done":true,"done_reason":"stop","eval_count":2}`)), Header: http.Header{"Content-Type": []string{"application/json"}}}, nil
			}
			body := `{"brief":"Report that the selected skill stored the content using the tool output.","needsTools":false,"reason":"The command result is sufficient.","toolCalls":[]}`
			return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: io.NopCloser(strings.NewReader(`{"model":"harness-model","message":{"role":"assistant","content":` + strconv.Quote(body) + `},"done":true,"done_reason":"stop","eval_count":2}`)), Header: http.Header{"Content-Type": []string{"application/json"}}}, nil
		}
		streamMessages = payload["messages"].([]any)
		responseSystem, _ = streamMessages[0].(map[string]any)["content"].(string)
		body := fmt.Sprintln(`{"model":"chat-box-model","message":{"role":"assistant","content":"Stored by the selected skill."},"done":false}`) +
			fmt.Sprintln(`{"model":"chat-box-model","done":true,"done_reason":"stop","eval_count":3}`)
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{"Content-Type": []string{"application/x-ndjson"}}}, nil
	})

	app.runChatStream(context.Background(), "request-memorybank", ChatRequest{
		BaseURL: "http://ollama.test",
		Model:   "chat-box-model",
		Messages: []ChatMessage{
			{Role: "user", Content: "What does different sea color mean?"},
			{Role: "assistant", Content: "Blue can indicate clear shallow water. Green often means algae. Brown often means suspended sediment."},
			{Role: "user", Content: "Post the above information to memorybank."},
		},
	})

	commandArgs, err := os.ReadFile(commandLog)
	if err != nil {
		t.Fatalf("expected selected skill command to run: %v", err)
	}
	if !strings.Contains(string(commandArgs), "post\n--content\nBlue can indicate clear shallow water.") {
		t.Fatalf("command args = %q, want post with previous assistant content", string(commandArgs))
	}
	if len(approvedCommands) != 1 || approvedCommands[0][0] != "notesctl" {
		t.Fatalf("approved commands = %+v, want one notesctl permission request", approvedCommands)
	}
	if prepCalls != 3 {
		t.Fatalf("prepCalls = %d, want invalid plan, corrected plan, and closing round", prepCalls)
	}
	if !strings.Contains(repairPrompt, "not a valid tool plan") {
		t.Fatalf("repair prompt = %q, want validation feedback for the planner", repairPrompt)
	}
	if strings.Contains(responseSystem, "Tell the final model to run") {
		t.Fatalf("response system delegated tool call to final model: %q", responseSystem)
	}
	if strings.Contains(responseSystem, "stored/path.md") {
		t.Fatalf("response system embeds tool output: %q", responseSystem)
	}
	lastStreamMessage := streamMessages[len(streamMessages)-1].(map[string]any)
	if lastStreamMessage["role"] != "tool" {
		t.Fatalf("last stream message = %+v, want tool observation message", lastStreamMessage)
	}
	if content, _ := lastStreamMessage["content"].(string); !strings.Contains(content, "stored/path.md") {
		t.Fatalf("tool observation = %q, want command output", content)
	}

	conversations, err := listConversations(config.Storage)
	if err != nil {
		t.Fatalf("listConversations returned error: %v", err)
	}
	detail, err := getConversation(config.Storage, conversations[0].ID)
	if err != nil {
		t.Fatalf("getConversation returned error: %v", err)
	}
	harnessRun := detail.Turns[len(detail.Turns)-1].ProviderResponse["harnessRun"].(map[string]any)
	triage, ok := harnessRun["triage"].(map[string]any)
	if !ok || triage["needsTools"] != true || triage["reason"] != "user explicitly referenced a skill" {
		t.Fatalf("harness run triage = %+v, want explicit-skill reason with needsTools true", harnessRun["triage"])
	}
}

func TestHarnessModelPlansKnowledgedPost(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	bin := filepath.Join(home, "bin")
	if err := os.MkdirAll(bin, 0755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	commandLog := filepath.Join(home, "kc-args.txt")
	commandPath := filepath.Join(bin, "kc")
	commandScript := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + strconv.Quote(commandLog) + "\nprintf 'job id: 12345\\n'\n"
	if err := os.WriteFile(commandPath, []byte(commandScript), 0755); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	skillDir := filepath.Join(home, ".agents", "skills", "knowledged")
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: knowledged\ndescription: Store knowledge using kc.\n---\n\nUse kc post --title TITLE --content CONTENT to store knowledge."), 0644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}

	config := defaultAppConfig()
	config.Storage = ConfigStorage{
		Root:      filepath.Join(home, ".atelier"),
		History:   filepath.Join(home, ".atelier", "history"),
		Artifacts: filepath.Join(home, ".atelier", "history"),
	}
	config.Tools.Filesystem.Root = filepath.Join(home, "workspace")
	config.Providers.Ollama.BaseURL = "http://ollama.test"
	config.Providers.Ollama.Models.Chat = "chat-box-model"
	config.Providers.Ollama.Models.Tools = "harness-model"
	if err := os.MkdirAll(config.Tools.Filesystem.Root, 0755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	if err := writeAppConfig(config); err != nil {
		t.Fatalf("writeAppConfig returned error: %v", err)
	}

	app := NewApp()
	app.toolPermission = func(context.Context, ToolPermissionRequestEvent) bool {
		return true
	}
	prepCalls := 0
	var prepSystem string
	var nonStreamPrompts []string
	var nonStreamRoles []string
	app.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/api/chat" {
			return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader("not found"))}, nil
		}
		var payload map[string]any
		data, _ := io.ReadAll(req.Body)
		if err := json.Unmarshal(data, &payload); err != nil {
			t.Fatalf("provider request body is not JSON: %v", err)
		}
		requestedModel, _ := payload["model"].(string)
		if payload["stream"] == false {
			if requestedModel != "harness-model" {
				t.Fatalf("explicit skill turn should skip triage; got non-stream call for %q", requestedModel)
			}
			prepCalls++
			messages := payload["messages"].([]any)
			lastMessage := messages[len(messages)-1].(map[string]any)
			content, _ := lastMessage["content"].(string)
			nonStreamPrompts = append(nonStreamPrompts, content)
			role, _ := lastMessage["role"].(string)
			nonStreamRoles = append(nonStreamRoles, role)
			if prepCalls == 1 {
				firstMessage := messages[0].(map[string]any)
				prepSystem, _ = firstMessage["content"].(string)
				if !strings.Contains(prepSystem, "Use kc post") {
					t.Fatalf("prep system = %q, want active knowledged skill instructions", prepSystem)
				}
				body := "```json\n{\"brief\":\"Post the previous assistant response to knowledged with kc and report the result.\",\"needsTools\":true,\"reason\":\"The selected knowledged skill says to use kc post, and the user asked to post the previous answer.\",\"toolCalls\":[{\"name\":\"run_command\",\"command\":\"kc\",\"args\":[\"post\",\"--title\",\"Sapiens: A Brief History of Humankind\",\"--content\",\"Sapiens: A Brief History of Humankind\\nYuval Noah Harari argues that shared myths let humans cooperate at scale.\"]}]}\n```"
				return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: io.NopCloser(strings.NewReader(`{"model":"harness-model","message":{"role":"assistant","content":` + strconv.Quote(body) + `},"done":true,"done_reason":"stop","eval_count":2}`)), Header: http.Header{"Content-Type": []string{"application/json"}}}, nil
			}
			body := "```json\n{\"brief\":\"Report that knowledged stored the content from the kc output.\",\"needsTools\":false,\"reason\":\"The command result is sufficient.\",\"toolCalls\":[]}\n```"
			return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: io.NopCloser(strings.NewReader(`{"model":"harness-model","message":{"role":"assistant","content":` + strconv.Quote(body) + `},"done":true,"done_reason":"stop","eval_count":2}`)), Header: http.Header{"Content-Type": []string{"application/json"}}}, nil
		}
		body := fmt.Sprintln(`{"model":"chat-box-model","message":{"role":"assistant","content":"Stored in knowledged."},"done":false}`) +
			fmt.Sprintln(`{"model":"chat-box-model","done":true,"done_reason":"stop","eval_count":3}`)
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{"Content-Type": []string{"application/x-ndjson"}}}, nil
	})

	app.runChatStream(context.Background(), "request-knowledged", ChatRequest{
		BaseURL: "http://ollama.test",
		Model:   "chat-box-model",
		Messages: []ChatMessage{
			{Role: "user", Content: "Give me a note about Sapiens."},
			{Role: "assistant", Content: "Sapiens: A Brief History of Humankind\nYuval Noah Harari argues that shared myths let humans cooperate at scale."},
			{Role: "user", Content: "Post this to knowledged."},
		},
	})

	commandArgs, err := os.ReadFile(commandLog)
	if err != nil {
		t.Fatalf("expected knowledged command to run: %v", err)
	}
	args := string(commandArgs)
	if !strings.Contains(args, "post\n--title\nSapiens: A Brief History of Humankind\n--content\nSapiens: A Brief History of Humankind") {
		t.Fatalf("command args = %q, want kc post with derived title and previous assistant content", args)
	}
	if prepCalls != 2 {
		t.Fatalf("prepCalls = %d, want planning round with tools then closing round", prepCalls)
	}
	if len(nonStreamRoles) != 2 || nonStreamRoles[1] != "tool" {
		t.Fatalf("non-stream roles = %+v, want tool result message in second planning round", nonStreamRoles)
	}
	if !strings.Contains(nonStreamPrompts[1], "job id: 12345") {
		t.Fatalf("second planning prompt = %q, want kc command output as tool observation", nonStreamPrompts[1])
	}

	conversations, err := listConversations(config.Storage)
	if err != nil {
		t.Fatalf("listConversations returned error: %v", err)
	}
	detail, err := getConversation(config.Storage, conversations[0].ID)
	if err != nil {
		t.Fatalf("getConversation returned error: %v", err)
	}
	harnessRun := detail.Turns[len(detail.Turns)-1].ProviderResponse["harnessRun"].(map[string]any)
	triage, ok := harnessRun["triage"].(map[string]any)
	if !ok || triage["needsTools"] != true || triage["reason"] != "user explicitly referenced a skill" {
		t.Fatalf("harness run triage = %+v, want explicit-skill reason with needsTools true", harnessRun["triage"])
	}
}

func TestHarnessExecutesFilesystemToolBeforeSelectedModel(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	config := defaultAppConfig()
	config.Storage = ConfigStorage{
		Root:      filepath.Join(home, ".atelier"),
		History:   filepath.Join(home, ".atelier", "history"),
		Artifacts: filepath.Join(home, ".atelier", "history"),
	}
	config.Tools.Filesystem.Root = filepath.Join(home, "tool-root")
	config.Providers.Ollama.BaseURL = "http://ollama.test"
	config.Providers.Ollama.Models.Chat = "chat-box-model"
	config.Providers.Ollama.Models.Tools = "harness-model"
	if err := os.MkdirAll(config.Tools.Filesystem.Root, 0755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(config.Tools.Filesystem.Root, "status.txt"), []byte("Project status: green"), 0644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	if err := writeAppConfig(config); err != nil {
		t.Fatalf("writeAppConfig returned error: %v", err)
	}

	app := NewApp()
	var responseSystem string
	var plannerSystem string
	var streamMessages []any
	prepCalls := 0
	app.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/api/chat" {
			return &http.Response{
				StatusCode: http.StatusNotFound,
				Status:     "404 Not Found",
				Body:       io.NopCloser(strings.NewReader("not found")),
				Header:     http.Header{},
			}, nil
		}
		var payload map[string]any
		data, _ := io.ReadAll(req.Body)
		if err := json.Unmarshal(data, &payload); err != nil {
			t.Fatalf("provider request body is not JSON: %v", err)
		}
		requestedModel, _ := payload["model"].(string)
		if payload["stream"] == false {
			if requestedModel == "chat-box-model" {
				decision := `{"needsTools":true,"toolTask":"Read status.txt to answer the status question.","reason":"The status lives in the workspace."}`
				return &http.Response{
					StatusCode: http.StatusOK,
					Status:     "200 OK",
					Body:       io.NopCloser(strings.NewReader(`{"model":"chat-box-model","message":{"role":"assistant","content":` + strconv.Quote(decision) + `},"done":true,"done_reason":"stop","eval_count":2}`)),
					Header:     http.Header{"Content-Type": []string{"application/json"}},
				}, nil
			}
			prepCalls++
			if messages, ok := payload["messages"].([]any); ok && len(messages) > 0 {
				if firstMessage, ok := messages[0].(map[string]any); ok {
					if system, _ := firstMessage["content"].(string); system != "" {
						plannerSystem = system
					}
				}
			}
			body := "{\"model\":\"harness-model\",\"message\":{\"role\":\"assistant\",\"content\":\"```json\\n{\\\"brief\\\":\\\"Use the status file to answer.\\\",\\\"needsTools\\\":true,\\\"reason\\\":\\\"The user asks for project status from the workspace.\\\",\\\"toolCalls\\\":[{\\\"name\\\":\\\"read_file\\\",\\\"path\\\":\\\"status.txt\\\"}]}\\n```\"},\"done\":true,\"done_reason\":\"stop\",\"eval_count\":2}"
			if prepCalls > 1 {
				body = "{\"model\":\"harness-model\",\"message\":{\"role\":\"assistant\",\"content\":\"```json\\n{\\\"brief\\\":\\\"Answer that the project status is green based on the status file.\\\",\\\"needsTools\\\":false,\\\"reason\\\":\\\"The status file provided enough context.\\\",\\\"toolCalls\\\":[]}\\n```\"},\"done\":true,\"done_reason\":\"stop\",\"eval_count\":2}"
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Body:       io.NopCloser(strings.NewReader(body)),
				Header:     http.Header{"Content-Type": []string{"application/json"}},
			}, nil
		}
		messages, ok := payload["messages"].([]any)
		if !ok || len(messages) == 0 {
			t.Fatalf("stream request messages = %+v, want system handoff", payload["messages"])
		}
		systemMessage, ok := messages[0].(map[string]any)
		if !ok || systemMessage["role"] != "system" {
			t.Fatalf("first message = %+v, want system handoff", messages[0])
		}
		responseSystem, _ = systemMessage["content"].(string)
		streamMessages = messages
		body := fmt.Sprintln(`{"model":"chat-box-model","message":{"role":"assistant","content":"The project is green."},"done":false}`) +
			fmt.Sprintln(`{"model":"chat-box-model","done":true,"done_reason":"stop","eval_count":3}`)
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     http.Header{"Content-Type": []string{"application/x-ndjson"}},
		}, nil
	})

	app.runChatStream(context.Background(), "request-tools", ChatRequest{
		BaseURL: "http://ollama.test",
		Model:   "chat-box-model",
		Messages: []ChatMessage{
			{Role: "user", Content: "What is the project status?"},
		},
	})
	if !strings.Contains(responseSystem, "observations appear as tool messages") {
		t.Fatalf("response system handoff = %q, want tool evidence note", responseSystem)
	}
	if strings.Contains(responseSystem, "Use the status file to answer") {
		t.Fatalf("response system contains planner brief: %q", responseSystem)
	}
	if strings.Contains(responseSystem, "Project status: green") {
		t.Fatalf("response system embeds tool output: %q", responseSystem)
	}
	lastStreamMessage := streamMessages[len(streamMessages)-1].(map[string]any)
	if lastStreamMessage["role"] != "tool" {
		t.Fatalf("last stream message = %+v, want tool observation message", lastStreamMessage)
	}
	if content, _ := lastStreamMessage["content"].(string); !strings.Contains(content, "Project status: green") {
		t.Fatalf("tool observation = %q, want status file content", content)
	}
	if prepCalls != 2 {
		t.Fatalf("harness prep calls = %d, want planning round with tools then closing round", prepCalls)
	}
	if !strings.Contains(plannerSystem, "Read status.txt to answer the status question.") {
		t.Fatalf("planner system = %q, want triage tool task seeded", plannerSystem)
	}

	conversations, err := listConversations(config.Storage)
	if err != nil {
		t.Fatalf("listConversations returned error: %v", err)
	}
	detail, err := getConversation(config.Storage, conversations[0].ID)
	if err != nil {
		t.Fatalf("getConversation returned error: %v", err)
	}
	if got := detail.Turns[1].Content[0].Text; got != "The project is green." {
		t.Fatalf("assistant content = %q, want final model answer", got)
	}
	if thinking := historyTextForTest(detail.Turns[1].Content, "thinking"); !strings.Contains(thinking, "Tool results") || !strings.Contains(thinking, "Project status: green") {
		t.Fatalf("assistant thinking = %q, want tool results", thinking)
	}
	harnessRun, ok := detail.Turns[1].ProviderResponse["harnessRun"].(map[string]any)
	if !ok {
		t.Fatalf("assistant provider response missing harness run: %+v", detail.Turns[1].ProviderResponse)
	}
	steps, ok := harnessRun["steps"].([]any)
	if !ok {
		t.Fatalf("harness steps = %+v, want timeline", harnessRun["steps"])
	}
	toolStep := harnessStepByKind(t, steps, "tool_call")
	if toolStep["status"] != "completed" || toolStep["provider"] != "tools" {
		t.Fatalf("tool step = %+v, want completed tool call", toolStep)
	}
	activities, ok := toolStep["tools"].([]any)
	if !ok || len(activities) != 1 {
		t.Fatalf("tool activities = %+v, want one activity", toolStep["tools"])
	}
	activity, ok := activities[0].(map[string]any)
	if !ok || activity["name"] != "read_file" || activity["status"] != "completed" {
		t.Fatalf("tool activity = %+v, want completed read_file", activities[0])
	}
	if path, _ := activity["path"].(string); !strings.HasSuffix(path, "status.txt") {
		t.Fatalf("tool activity path = %q, want status.txt", path)
	}
}

func TestHarnessFeedsToolFailureBackToPlanner(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	config := defaultAppConfig()
	config.Storage = ConfigStorage{
		Root:      filepath.Join(home, ".atelier"),
		History:   filepath.Join(home, ".atelier", "history"),
		Artifacts: filepath.Join(home, ".atelier", "history"),
	}
	config.Tools.Filesystem.Root = filepath.Join(home, "tool-root")
	config.Providers.Ollama.BaseURL = "http://ollama.test"
	config.Providers.Ollama.Models.Chat = "chat-box-model"
	config.Providers.Ollama.Models.Tools = "harness-model"
	if err := os.MkdirAll(config.Tools.Filesystem.Root, 0755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	if err := writeAppConfig(config); err != nil {
		t.Fatalf("writeAppConfig returned error: %v", err)
	}

	app := NewApp()
	prepCalls := 0
	streamCalls := 0
	var failureObservationPrompt string
	var failureObservationRole string
	var streamMessages []any
	app.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/api/chat" {
			return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader("not found"))}, nil
		}
		var payload map[string]any
		data, _ := io.ReadAll(req.Body)
		if err := json.Unmarshal(data, &payload); err != nil {
			t.Fatalf("provider request body is not JSON: %v", err)
		}
		requestedModel, _ := payload["model"].(string)
		if payload["stream"] == false {
			if requestedModel == "chat-box-model" {
				decision := `{"needsTools":true,"toolTask":"Read missing-status.txt and report its contents.","reason":"The answer depends on a workspace file."}`
				return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: io.NopCloser(strings.NewReader(`{"model":"chat-box-model","message":{"role":"assistant","content":` + strconv.Quote(decision) + `},"done":true,"done_reason":"stop","eval_count":2}`)), Header: http.Header{"Content-Type": []string{"application/json"}}}, nil
			}
			prepCalls++
			if prepCalls == 1 {
				body := `{"brief":"Read the missing status file before answering.","needsTools":true,"reason":"The answer depends on the actual file content.","toolCalls":[{"name":"read_file","path":"missing-status.txt"}]}`
				return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: io.NopCloser(strings.NewReader(`{"model":"harness-model","message":{"role":"assistant","content":` + strconv.Quote(body) + `},"done":true,"done_reason":"stop","eval_count":2}`)), Header: http.Header{"Content-Type": []string{"application/json"}}}, nil
			}
			messages := payload["messages"].([]any)
			lastMessage := messages[len(messages)-1].(map[string]any)
			failureObservationPrompt, _ = lastMessage["content"].(string)
			failureObservationRole, _ = lastMessage["role"].(string)
			body := `{"brief":"The status file could not be read. Tell the user plainly that the read failed and why; do not claim success.","needsTools":false,"reason":"The failed read is the answer-relevant evidence.","toolCalls":[]}`
			return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: io.NopCloser(strings.NewReader(`{"model":"harness-model","message":{"role":"assistant","content":` + strconv.Quote(body) + `},"done":true,"done_reason":"stop","eval_count":2}`)), Header: http.Header{"Content-Type": []string{"application/json"}}}, nil
		}
		streamCalls++
		streamMessages = payload["messages"].([]any)
		body := fmt.Sprintln(`{"model":"chat-box-model","message":{"role":"assistant","content":"I couldn't read the missing status file."},"done":false}`) +
			fmt.Sprintln(`{"model":"chat-box-model","done":true,"done_reason":"stop","eval_count":3}`)
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{"Content-Type": []string{"application/x-ndjson"}}}, nil
	})

	app.runChatStream(context.Background(), "request-missing-tool", ChatRequest{
		BaseURL: "http://ollama.test",
		Model:   "chat-box-model",
		Messages: []ChatMessage{
			{Role: "user", Content: "Read the missing status file and tell me what it says."},
		},
	})

	if prepCalls != 2 {
		t.Fatalf("prepCalls = %d, want failure fed back for a second planning round", prepCalls)
	}
	if streamCalls != 1 {
		t.Fatalf("streamCalls = %d, want final model called with failure observation", streamCalls)
	}
	if failureObservationRole != "tool" || !strings.Contains(failureObservationPrompt, `"status":"failed"`) {
		t.Fatalf("planner follow-up message role=%q content=%q, want failed tool observation", failureObservationRole, failureObservationPrompt)
	}
	lastStreamMessage := streamMessages[len(streamMessages)-1].(map[string]any)
	if lastStreamMessage["role"] != "tool" {
		t.Fatalf("last stream message = %+v, want tool observation message", lastStreamMessage)
	}
	if content, _ := lastStreamMessage["content"].(string); !strings.Contains(content, `"status":"failed"`) {
		t.Fatalf("final model observation = %q, want failed tool result", content)
	}
	conversations, err := listConversations(config.Storage)
	if err != nil {
		t.Fatalf("listConversations returned error: %v", err)
	}
	detail, err := getConversation(config.Storage, conversations[0].ID)
	if err != nil {
		t.Fatalf("getConversation returned error: %v", err)
	}
	assistant := detail.Turns[1]
	if text := assistant.Content[0].Text; text != "I couldn't read the missing status file." {
		t.Fatalf("assistant text = %q, want final model failure report", text)
	}
	harnessRun := assistant.ProviderResponse["harnessRun"].(map[string]any)
	if harnessRun["status"] != "completed" {
		t.Fatalf("harness status = %q, want completed run that reported the failure", harnessRun["status"])
	}
	toolStep := harnessStepByKind(t, harnessRun["steps"].([]any), "tool_call")
	activities := toolStep["tools"].([]any)
	activity := activities[0].(map[string]any)
	if activity["name"] != "read_file" || activity["status"] != "failed" {
		t.Fatalf("tool activity = %+v, want failed read_file", activity)
	}
}

func TestHarnessCautionsFinalModelAfterRepeatedInvalidPlans(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	skillDir := filepath.Join(home, ".agents", "skills", "knowledged")
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: knowledged\ndescription: Store knowledge using kc.\n---\n\nUse kc post --title TITLE --content CONTENT to store knowledge."), 0644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}

	config := defaultAppConfig()
	config.Storage = ConfigStorage{
		Root:      filepath.Join(home, ".atelier"),
		History:   filepath.Join(home, ".atelier", "history"),
		Artifacts: filepath.Join(home, ".atelier", "history"),
	}
	config.Tools.Filesystem.Root = filepath.Join(home, "tool-root")
	config.Providers.Ollama.BaseURL = "http://ollama.test"
	config.Providers.Ollama.Models.Chat = "chat-box-model"
	config.Providers.Ollama.Models.Tools = "harness-model"
	if err := os.MkdirAll(config.Tools.Filesystem.Root, 0755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	if err := writeAppConfig(config); err != nil {
		t.Fatalf("writeAppConfig returned error: %v", err)
	}

	app := NewApp()
	prepCalls := 0
	streamCalls := 0
	var responseSystem string
	var retryPrompt string
	app.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/api/chat" {
			return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader("not found"))}, nil
		}
		var payload map[string]any
		data, _ := io.ReadAll(req.Body)
		if err := json.Unmarshal(data, &payload); err != nil {
			t.Fatalf("provider request body is not JSON: %v", err)
		}
		if payload["stream"] == false {
			prepCalls++
			if prepCalls > 1 {
				messages := payload["messages"].([]any)
				lastMessage := messages[len(messages)-1].(map[string]any)
				retryPrompt, _ = lastMessage["content"].(string)
			}
			body := "I should use the knowledged command to post this, but I cannot fit the full JSON plan before the output limit."
			return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: io.NopCloser(strings.NewReader(`{"model":"harness-model","message":{"role":"assistant","content":` + strconv.Quote(body) + `},"done":true,"done_reason":"length","eval_count":1024}`)), Header: http.Header{"Content-Type": []string{"application/json"}}}, nil
		}
		streamCalls++
		messages := payload["messages"].([]any)
		responseSystem, _ = messages[0].(map[string]any)["content"].(string)
		body := fmt.Sprintln(`{"model":"chat-box-model","message":{"role":"assistant","content":"I couldn't post this to knowledged because the harness could not prepare the command."},"done":false}`) +
			fmt.Sprintln(`{"model":"chat-box-model","done":true,"done_reason":"stop","eval_count":3}`)
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{"Content-Type": []string{"application/x-ndjson"}}}, nil
	})

	app.runChatStream(context.Background(), "request-invalid-skill-plan", ChatRequest{
		BaseURL: "http://ollama.test",
		Model:   "chat-box-model",
		Messages: []ChatMessage{
			{Role: "user", Content: "Tell me the story behind Nataraja."},
			{Role: "assistant", Content: "Nataraja represents Shiva's cosmic dance of creation, preservation, and dissolution."},
			{Role: "user", Content: "Post this to knowledged."},
		},
	})

	if prepCalls != harnessChatMaxSteps {
		t.Fatalf("prepCalls = %d, want validation feedback retries up to the step cap", prepCalls)
	}
	if streamCalls != 1 {
		t.Fatalf("streamCalls = %d, want final model called once with the invalid-plan note", streamCalls)
	}
	if !strings.Contains(responseSystem, "no tools ran") {
		t.Fatalf("response system = %q, want invalid-plan note", responseSystem)
	}
	if responseSystem != invalidPlanSystemNote {
		t.Fatalf("response system = %q, want only the invalid-plan note (no planner brief)", responseSystem)
	}
	if !strings.Contains(retryPrompt, "hit the output token limit") {
		t.Fatalf("retry prompt = %q, want truncated-plan feedback", retryPrompt)
	}
	conversations, err := listConversations(config.Storage)
	if err != nil {
		t.Fatalf("listConversations returned error: %v", err)
	}
	detail, err := getConversation(config.Storage, conversations[0].ID)
	if err != nil {
		t.Fatalf("getConversation returned error: %v", err)
	}
	assistant := detail.Turns[1]
	if text := assistant.Content[0].Text; !strings.Contains(text, "couldn't post this to knowledged") {
		t.Fatalf("assistant text = %q, want honest failure report from final model", text)
	}
	harnessRun := assistant.ProviderResponse["harnessRun"].(map[string]any)
	if harnessRun["status"] != "completed" {
		t.Fatalf("harness status = %q, want completed run with the invalid-plan note", harnessRun["status"])
	}
	loop := harnessRun["loop"].(map[string]any)
	if loop["iterations"] != float64(harnessChatMaxSteps) {
		t.Fatalf("loop = %+v, want iterations at the step cap", loop)
	}
}

func TestBlankFinalModelProducesHarnessNotice(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	config := defaultAppConfig()
	config.Storage = ConfigStorage{
		Root:      filepath.Join(home, ".atelier"),
		History:   filepath.Join(home, ".atelier", "history"),
		Artifacts: filepath.Join(home, ".atelier", "history"),
	}
	config.Tools.Filesystem.Root = filepath.Join(home, "tool-root")
	config.Providers.Ollama.BaseURL = "http://ollama.test"
	config.Providers.Ollama.Models.Chat = "chat-box-model"
	config.Providers.Ollama.Models.Tools = "harness-model"
	if err := os.MkdirAll(config.Tools.Filesystem.Root, 0755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(config.Tools.Filesystem.Root, "status.txt"), []byte("Queued as job-123"), 0644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	if err := writeAppConfig(config); err != nil {
		t.Fatalf("writeAppConfig returned error: %v", err)
	}

	app := NewApp()
	prepCalls := 0
	app.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/api/chat" {
			return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader("not found"))}, nil
		}
		var payload map[string]any
		data, _ := io.ReadAll(req.Body)
		if err := json.Unmarshal(data, &payload); err != nil {
			t.Fatalf("provider request body is not JSON: %v", err)
		}
		requestedModel, _ := payload["model"].(string)
		if payload["stream"] == false {
			if requestedModel == "chat-box-model" {
				decision := `{"needsTools":true,"toolTask":"Read status.txt and confirm the status.","reason":"Need the actual status file."}`
				return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: io.NopCloser(strings.NewReader(`{"model":"chat-box-model","message":{"role":"assistant","content":` + strconv.Quote(decision) + `},"done":true,"done_reason":"stop","eval_count":2}`)), Header: http.Header{"Content-Type": []string{"application/json"}}}, nil
			}
			prepCalls++
			if prepCalls == 1 {
				body := "```json\n{\"brief\":\"Read status, then confirm the result.\",\"needsTools\":true,\"reason\":\"Need the actual status file.\",\"toolCalls\":[{\"name\":\"read_file\",\"path\":\"status.txt\"}]}\n```"
				return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: io.NopCloser(strings.NewReader(`{"model":"harness-model","message":{"role":"assistant","content":` + strconv.Quote(body) + `},"done":true,"done_reason":"stop","eval_count":2}`)), Header: http.Header{"Content-Type": []string{"application/json"}}}, nil
			}
			body := "```json\n{\"brief\":\"Status file was read successfully; confirm completion.\",\"needsTools\":false,\"reason\":\"Existing result is sufficient.\",\"toolCalls\":[]}\n```"
			return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: io.NopCloser(strings.NewReader(`{"model":"harness-model","message":{"role":"assistant","content":` + strconv.Quote(body) + `},"done":true,"done_reason":"stop","eval_count":2}`)), Header: http.Header{"Content-Type": []string{"application/json"}}}, nil
		}
		body := fmt.Sprintln(`{"model":"chat-box-model","message":{"role":"assistant","thinking":"I should answer, but only thinking is emitted."},"done":false}`) +
			fmt.Sprintln(`{"model":"chat-box-model","done":true,"done_reason":"stop","eval_count":3}`)
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{"Content-Type": []string{"application/x-ndjson"}}}, nil
	})

	app.runChatStream(context.Background(), "request-blank-final", ChatRequest{
		BaseURL: "http://ollama.test",
		Model:   "chat-box-model",
		Messages: []ChatMessage{
			{Role: "user", Content: "Read the status and confirm it."},
		},
	})

	conversations, err := listConversations(config.Storage)
	if err != nil {
		t.Fatalf("listConversations returned error: %v", err)
	}
	detail, err := getConversation(config.Storage, conversations[0].ID)
	if err != nil {
		t.Fatalf("getConversation returned error: %v", err)
	}
	got := detail.Turns[1].Content[0].Text
	if !strings.Contains(got, "Atelier harness notice: the response model returned no content") {
		t.Fatalf("assistant content = %q, want harness notice in the harness's own voice", got)
	}
	if !strings.Contains(got, "`read_file` completed") || !strings.Contains(got, "status.txt") {
		t.Fatalf("assistant content = %q, want read_file outcome in the notice", got)
	}
}

func TestHarnessCanRequestSecondToolRound(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	config := defaultAppConfig()
	config.Storage = ConfigStorage{
		Root:      filepath.Join(home, ".atelier"),
		History:   filepath.Join(home, ".atelier", "history"),
		Artifacts: filepath.Join(home, ".atelier", "history"),
	}
	config.Tools.Filesystem.Root = filepath.Join(home, "tool-root")
	config.Providers.Ollama.BaseURL = "http://ollama.test"
	config.Providers.Ollama.Models.Chat = "chat-box-model"
	config.Providers.Ollama.Models.Tools = "harness-model"
	if err := os.MkdirAll(config.Tools.Filesystem.Root, 0755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(config.Tools.Filesystem.Root, "notes.txt"), []byte("Second round found this."), 0644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	if err := writeAppConfig(config); err != nil {
		t.Fatalf("writeAppConfig returned error: %v", err)
	}

	app := NewApp()
	prepCalls := 0
	var responseSystem string
	var streamMessages []any
	app.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/api/chat" {
			return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader("not found"))}, nil
		}
		var payload map[string]any
		data, _ := io.ReadAll(req.Body)
		if err := json.Unmarshal(data, &payload); err != nil {
			t.Fatalf("provider request body is not JSON: %v", err)
		}
		requestedModel, _ := payload["model"].(string)
		if payload["stream"] == false {
			if requestedModel == "chat-box-model" {
				decision := `{"needsTools":true,"toolTask":"Discover and read the workspace notes.","reason":"The user asked to use workspace notes."}`
				return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: io.NopCloser(strings.NewReader(`{"model":"chat-box-model","message":{"role":"assistant","content":` + strconv.Quote(decision) + `},"done":true,"done_reason":"stop","eval_count":2}`)), Header: http.Header{"Content-Type": []string{"application/json"}}}, nil
			}
			prepCalls++
			body := "{\"model\":\"harness-model\",\"message\":{\"role\":\"assistant\",\"content\":\"```json\\n{\\\"brief\\\":\\\"List workspace files first.\\\",\\\"needsTools\\\":true,\\\"reason\\\":\\\"Need to discover file names.\\\",\\\"toolCalls\\\":[{\\\"name\\\":\\\"list_files\\\",\\\"path\\\":\\\".\\\"}]}\\n```\"},\"done\":true,\"done_reason\":\"stop\",\"eval_count\":2}"
			if prepCalls == 2 {
				body = "{\"model\":\"harness-model\",\"message\":{\"role\":\"assistant\",\"content\":\"```json\\n{\\\"brief\\\":\\\"Read notes.txt before answering.\\\",\\\"needsTools\\\":true,\\\"reason\\\":\\\"The file list revealed notes.txt.\\\",\\\"toolCalls\\\":[{\\\"name\\\":\\\"read_file\\\",\\\"path\\\":\\\"notes.txt\\\"}]}\\n```\"},\"done\":true,\"done_reason\":\"stop\",\"eval_count\":2}"
			}
			if prepCalls >= 3 {
				body = "{\"model\":\"harness-model\",\"message\":{\"role\":\"assistant\",\"content\":\"```json\\n{\\\"brief\\\":\\\"Answer from the notes.txt content in the tool observations.\\\",\\\"needsTools\\\":false,\\\"reason\\\":\\\"The notes content is sufficient.\\\",\\\"toolCalls\\\":[]}\\n```\"},\"done\":true,\"done_reason\":\"stop\",\"eval_count\":2}"
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Body:       io.NopCloser(strings.NewReader(body)),
				Header:     http.Header{"Content-Type": []string{"application/json"}},
			}, nil
		}
		messages := payload["messages"].([]any)
		streamMessages = messages
		systemMessage := messages[0].(map[string]any)
		responseSystem, _ = systemMessage["content"].(string)
		body := fmt.Sprintln(`{"model":"chat-box-model","message":{"role":"assistant","content":"Second round answer."},"done":false}`) +
			fmt.Sprintln(`{"model":"chat-box-model","done":true,"done_reason":"stop","eval_count":3}`)
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     http.Header{"Content-Type": []string{"application/x-ndjson"}},
		}, nil
	})

	app.runChatStream(context.Background(), "request-second-round", ChatRequest{
		BaseURL: "http://ollama.test",
		Model:   "chat-box-model",
		Messages: []ChatMessage{
			{Role: "user", Content: "Use the workspace notes"},
		},
	})
	if prepCalls != 3 {
		t.Fatalf("harness prep calls = %d, want two tool rounds and a closing round", prepCalls)
	}
	if strings.Contains(responseSystem, "Second round found this.") {
		t.Fatalf("response system embeds tool output: %q", responseSystem)
	}
	observations := ""
	for _, message := range streamMessages {
		typed := message.(map[string]any)
		if typed["role"] == "tool" {
			content, _ := typed["content"].(string)
			observations += content
		}
	}
	if !strings.Contains(observations, "Second round found this.") {
		t.Fatalf("tool observations = %q, want second round file content", observations)
	}
	conversations, err := listConversations(config.Storage)
	if err != nil {
		t.Fatalf("listConversations returned error: %v", err)
	}
	detail, err := getConversation(config.Storage, conversations[0].ID)
	if err != nil {
		t.Fatalf("getConversation returned error: %v", err)
	}
	harnessRun := detail.Turns[1].ProviderResponse["harnessRun"].(map[string]any)
	loop := harnessRun["loop"].(map[string]any)
	if loop["iterations"] != float64(3) {
		t.Fatalf("loop = %+v, want 3 iterations", loop)
	}
	var toolSteps []map[string]any
	for _, raw := range harnessRun["steps"].([]any) {
		step := raw.(map[string]any)
		if step["kind"] == "tool_call" {
			toolSteps = append(toolSteps, step)
		}
	}
	if len(toolSteps) != 2 {
		t.Fatalf("tool steps = %+v, want one step per tool round", toolSteps)
	}
	if toolSteps[0]["iteration"] != float64(1) || toolSteps[1]["iteration"] != float64(2) {
		t.Fatalf("tool step iterations = %v and %v, want rounds 1 and 2", toolSteps[0]["iteration"], toolSteps[1]["iteration"])
	}
	firstActivities := toolSteps[0]["tools"].([]any)
	secondActivities := toolSteps[1]["tools"].([]any)
	if len(firstActivities) != 1 || len(secondActivities) != 1 {
		t.Fatalf("tool activities per round = %d and %d, want one each", len(firstActivities), len(secondActivities))
	}
	if name, _ := firstActivities[0].(map[string]any)["name"].(string); name != "list_files" {
		t.Fatalf("first round activity = %+v, want list_files", firstActivities[0])
	}
	if name, _ := secondActivities[0].(map[string]any)["name"].(string); name != "read_file" {
		t.Fatalf("second round activity = %+v, want read_file", secondActivities[0])
	}
}

func TestHarnessGeneratesImageViaPlannedTool(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	config := defaultAppConfig()
	config.Storage = ConfigStorage{
		Root:      filepath.Join(home, ".atelier"),
		History:   filepath.Join(home, ".atelier", "history"),
		Artifacts: filepath.Join(home, ".atelier", "history"),
	}
	config.Providers.Ollama.BaseURL = "http://ollama.test"
	config.Providers.Ollama.Models.Chat = "chat-box-model"
	config.Providers.Ollama.Models.Tools = "harness-model"
	config.Providers.Ollama.Models.Image = "image-model"
	if err := writeAppConfig(config); err != nil {
		t.Fatalf("writeAppConfig returned error: %v", err)
	}

	app := NewApp()
	prepCalls := 0
	imageCalls := 0
	var streamMessages []any
	app.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/api/generate":
			imageCalls++
			var payload map[string]any
			data, _ := io.ReadAll(req.Body)
			if err := json.Unmarshal(data, &payload); err != nil {
				t.Fatalf("image request body is not JSON: %v", err)
			}
			if payload["model"] != "image-model" {
				t.Fatalf("image request model = %q, want configured image-model", payload["model"])
			}
			if payload["prompt"] != "a small house with a red roof" {
				t.Fatalf("image request prompt = %q, want planner tool prompt", payload["prompt"])
			}
			body := `{"model":"image-model","image":"iVBORw0KGgo=","done":true}`
			return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{"Content-Type": []string{"application/json"}}}, nil
		case "/api/chat":
			var payload map[string]any
			data, _ := io.ReadAll(req.Body)
			if err := json.Unmarshal(data, &payload); err != nil {
				t.Fatalf("provider request body is not JSON: %v", err)
			}
			requestedModel, _ := payload["model"].(string)
			if payload["stream"] == false {
				if requestedModel == "chat-box-model" {
					decision := `{"needsTools":true,"toolTask":"Generate an image of a small house.","reason":"The user asked for an image."}`
					return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: io.NopCloser(strings.NewReader(`{"model":"chat-box-model","message":{"role":"assistant","content":` + strconv.Quote(decision) + `},"done":true,"done_reason":"stop","eval_count":2}`)), Header: http.Header{"Content-Type": []string{"application/json"}}}, nil
				}
				prepCalls++
				body := `{"brief":"Generate the requested image and confirm it briefly.","needsTools":true,"reason":"The user asked for an image, which requires the image tool.","toolCalls":[{"name":"generate_image","content":"a small house with a red roof"}]}`
				if prepCalls > 1 {
					body = `{"brief":"The image was generated; confirm it briefly for the user.","needsTools":false,"reason":"The image tool already produced the artifact.","toolCalls":[]}`
				}
				return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: io.NopCloser(strings.NewReader(`{"model":"harness-model","message":{"role":"assistant","content":` + strconv.Quote(body) + `},"done":true,"done_reason":"stop","eval_count":2}`)), Header: http.Header{"Content-Type": []string{"application/json"}}}, nil
			}
			streamMessages = payload["messages"].([]any)
			body := fmt.Sprintln(`{"model":"chat-box-model","message":{"role":"assistant","content":"Here is the small house you asked for."},"done":false}`) +
				fmt.Sprintln(`{"model":"chat-box-model","done":true,"done_reason":"stop","eval_count":3}`)
			return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{"Content-Type": []string{"application/x-ndjson"}}}, nil
		default:
			t.Fatalf("unexpected provider path %q", req.URL.Path)
		}
		return nil, nil
	})

	app.runChatStream(context.Background(), "request-image-tool", ChatRequest{
		BaseURL: "http://ollama.test",
		Model:   "chat-box-model",
		Messages: []ChatMessage{
			{Role: "user", Content: "Create an image of a small house"},
		},
	})

	if prepCalls != 2 || imageCalls != 1 {
		t.Fatalf("provider calls prep=%d image=%d, want planner round, image tool, closing round", prepCalls, imageCalls)
	}
	lastStreamMessage := streamMessages[len(streamMessages)-1].(map[string]any)
	if lastStreamMessage["role"] != "tool" {
		t.Fatalf("last stream message = %+v, want tool observation", lastStreamMessage)
	}
	observation, _ := lastStreamMessage["content"].(string)
	if !strings.Contains(observation, "generate_image") || !strings.Contains(observation, `"count":1`) {
		t.Fatalf("tool observation = %q, want image generation summary", observation)
	}
	if strings.Contains(observation, "iVBOR") {
		t.Fatalf("tool observation leaked base64 image data: %q", observation)
	}

	conversations, err := listConversations(config.Storage)
	if err != nil {
		t.Fatalf("listConversations returned error: %v", err)
	}
	detail, err := getConversation(config.Storage, conversations[0].ID)
	if err != nil {
		t.Fatalf("getConversation returned error: %v", err)
	}
	if len(detail.Turns) != 2 {
		t.Fatalf("turn count = %d, want user and assistant", len(detail.Turns))
	}
	assistant := detail.Turns[1]
	if got := assistant.Content[0].Text; got != "Here is the small house you asked for." {
		t.Fatalf("assistant text = %q, want final model confirmation", got)
	}
	images := historyImagesForTest(assistant.Content)
	if len(images) != 1 {
		t.Fatalf("assistant image content = %+v, want one image artifact", assistant.Content)
	}
	if assistant.Model != "chat-box-model" {
		t.Fatalf("assistant turn model = %q, want final chat model", assistant.Model)
	}
	tool, ok := assistant.ProviderResponse["tool"].(map[string]any)
	if !ok || tool["name"] != "image_generation" || tool["model"] != "image-model" {
		t.Fatalf("assistant provider tool = %+v, want image_generation via image-model", assistant.ProviderResponse["tool"])
	}
	harnessRun := assistant.ProviderResponse["harnessRun"].(map[string]any)
	toolStep := harnessStepByKind(t, harnessRun["steps"].([]any), "tool_call")
	activities := toolStep["tools"].([]any)
	activity := activities[0].(map[string]any)
	if activity["name"] != "generate_image" || activity["status"] != "completed" {
		t.Fatalf("tool activity = %+v, want completed generate_image", activity)
	}
}

func TestStreamChatReturnsConversationAfterPendingTurn(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	config := defaultAppConfig()
	config.Storage = ConfigStorage{
		Root:      filepath.Join(home, ".atelier"),
		History:   filepath.Join(home, ".atelier", "history"),
		Artifacts: filepath.Join(home, ".atelier", "history"),
	}
	config.Providers.Ollama.BaseURL = "http://ollama.test"
	config.Providers.Ollama.Models.Chat = "chat-model"
	config.Providers.Ollama.Models.Tools = "chat-model"
	if err := writeAppConfig(config); err != nil {
		t.Fatalf("writeAppConfig returned error: %v", err)
	}

	app := NewApp()
	app.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		var payload map[string]any
		data, _ := io.ReadAll(req.Body)
		if err := json.Unmarshal(data, &payload); err != nil {
			t.Fatalf("provider request body is not JSON: %v", err)
		}
		if payload["stream"] == false {
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Body:       io.NopCloser(strings.NewReader(`{"model":"chat-model","message":{"role":"assistant","content":"Proceed normally."},"done":true}`)),
				Header:     http.Header{"Content-Type": []string{"application/json"}},
			}, nil
		}
		body := fmt.Sprintln(`{"model":"chat-model","message":{"role":"assistant","content":"Later."},"done":false}`) +
			fmt.Sprintln(`{"model":"chat-model","done":true,"done_reason":"stop","eval_count":1}`)
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     http.Header{"Content-Type": []string{"application/x-ndjson"}},
		}, nil
	})

	start, err := app.StreamChat(ChatRequest{
		RequestID: "request-immediate",
		BaseURL:   "http://ollama.test",
		Model:     "chat-model",
		Messages: []ChatMessage{
			{Role: "user", Content: "Start now"},
		},
	})
	if err != nil {
		t.Fatalf("StreamChat returned error: %v", err)
	}
	if start.RequestID != "request-immediate" {
		t.Fatalf("request id = %q, want request-immediate", start.RequestID)
	}
	if strings.TrimSpace(start.ConversationID) == "" {
		t.Fatal("StreamChat returned empty conversation id")
	}

	detail, err := getConversation(config.Storage, start.ConversationID)
	if err != nil {
		t.Fatalf("getConversation returned error: %v", err)
	}
	if len(detail.Turns) == 0 {
		t.Fatal("conversation has no pending user turn")
	}
	if got := detail.Turns[0].Content[0].Text; got != "Start now" {
		t.Fatalf("pending user text = %q, want Start now", got)
	}
	waitForStreamCleanup(t, app, start.RequestID)
}

func TestHarnessStartChatTurnRecordsUserBeforeAssistant(t *testing.T) {
	root := t.TempDir()
	storage := ConfigStorage{
		Root:      filepath.Join(root, ".atelier"),
		History:   filepath.Join(root, ".atelier", "history"),
		Artifacts: filepath.Join(root, ".atelier", "history"),
	}
	config := defaultAppConfig()
	config.Storage = storage
	if err := ensureStorageDirs(storage); err != nil {
		t.Fatalf("ensureStorageDirs returned error: %v", err)
	}

	engine := newHarnessEngine(config)
	req := ChatRequest{
		Model: "chat-model",
		Messages: []ChatMessage{
			{Role: "user", Content: "Start immediately"},
		},
	}
	conversationID, err := engine.StartChatTurn(req)
	if err != nil {
		t.Fatalf("StartChatTurn returned error: %v", err)
	}
	conversations, err := listConversations(storage)
	if err != nil {
		t.Fatalf("listConversations returned error: %v", err)
	}
	if len(conversations) != 1 || conversations[0].ID != conversationID {
		t.Fatalf("conversations = %+v, want started conversation %s", conversations, conversationID)
	}
	detail, err := getConversation(storage, conversationID)
	if err != nil {
		t.Fatalf("getConversation returned error: %v", err)
	}
	if len(detail.Turns) != 1 || detail.Turns[0].Role != "user" {
		t.Fatalf("turns after start = %+v, want one user turn", detail.Turns)
	}

	run := fallbackHarnessRun("chat-model", "stop", 2)
	if err := engine.SaveAssistantTurn(conversationID, "Done later.", "", "chat-model", "stop", 2, run); err != nil {
		t.Fatalf("SaveAssistantTurn returned error: %v", err)
	}
	detail, err = getConversation(storage, conversationID)
	if err != nil {
		t.Fatalf("getConversation after assistant returned error: %v", err)
	}
	if len(detail.Turns) != 2 || detail.Turns[1].Role != "assistant" {
		t.Fatalf("turns after assistant = %+v, want user and assistant", detail.Turns)
	}
}

func TestWriteChatConversationPersistsInputImages(t *testing.T) {
	root := t.TempDir()
	storage := ConfigStorage{
		Root:      filepath.Join(root, ".atelier"),
		History:   filepath.Join(root, ".atelier", "history"),
		Artifacts: filepath.Join(root, ".atelier", "history"),
	}
	config := defaultAppConfig()
	config.Storage = storage
	if err := ensureStorageDirs(storage); err != nil {
		t.Fatalf("ensureStorageDirs returned error: %v", err)
	}

	const pngDataURL = "data:image/png;base64,iVBORw0KGgo="
	conversationID, err := writeChatConversation(
		config,
		ChatRequest{
			Model: "chat-model",
			Messages: []ChatMessage{
				{Role: "user", Content: "Describe this image", Images: []string{pngDataURL}},
			},
		},
		"It is a tiny png.",
		"",
		"chat-model",
		"stop",
		4,
		"Image Description",
	)
	if err != nil {
		t.Fatalf("writeChatConversation returned error: %v", err)
	}

	detail, err := getConversation(storage, conversationID)
	if err != nil {
		t.Fatalf("getConversation returned error: %v", err)
	}
	if detail.Conversation.Stats.ArtifactCount != 1 {
		t.Fatalf("artifact count = %d, want 1", detail.Conversation.Stats.ArtifactCount)
	}
	if len(detail.Turns) != 2 {
		t.Fatalf("turn count = %d, want 2", len(detail.Turns))
	}
	userContent := detail.Turns[0].Content
	if len(userContent) != 2 {
		t.Fatalf("user content count = %d, want text and image", len(userContent))
	}
	imageContent := userContent[1]
	if imageContent.Type != "image" {
		t.Fatalf("image content type = %q, want image", imageContent.Type)
	}
	if imageContent.Path != "artifacts/input_000001_000001.png" {
		t.Fatalf("image path = %q, want artifact path", imageContent.Path)
	}
	if imageContent.MimeType != "image/png" {
		t.Fatalf("image mime type = %q, want image/png", imageContent.MimeType)
	}
	if imageContent.Text != pngDataURL {
		t.Fatalf("hydrated image text = %q, want data URL", imageContent.Text)
	}
}

func TestGenerateImageSendsAttachedImages(t *testing.T) {
	client := newOllamaClient(&http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.Path != "/api/generate" {
				t.Fatalf("unexpected provider path %q", req.URL.Path)
			}
			var payload map[string]any
			data, _ := io.ReadAll(req.Body)
			if err := json.Unmarshal(data, &payload); err != nil {
				t.Fatalf("image request body is not JSON: %v", err)
			}
			images, ok := payload["images"].([]any)
			if !ok || len(images) != 2 || images[0] != "source-one" || images[1] != "source-two" {
				t.Fatalf("image request images = %+v, want attached source images", payload["images"])
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Body:       io.NopCloser(strings.NewReader(`{"model":"image-model","image":"iVBORw0KGgo=","done":true}`)),
				Header:     http.Header{"Content-Type": []string{"application/json"}},
			}, nil
		}),
	}, "http://ollama.test")

	if _, _, err := client.GenerateImage(t.Context(), ImageGenerateRequest{
		Model:  "image-model",
		Prompt: "Use these references",
		Images: []string{"source-one", "source-two"},
	}); err != nil {
		t.Fatalf("GenerateImage returned error: %v", err)
	}
}

func TestDecodeTriageDecisionAcceptsBareAndFencedJSON(t *testing.T) {
	decision, err := decodeTriageDecision("```json\n{\"needsTools\":true,\"toolTask\":\"Read status.txt\",\"reason\":\"workspace question\"}\n```")
	if err != nil || !decision.NeedsTools || decision.ToolTask != "Read status.txt" {
		t.Fatalf("decision = %+v, err = %v, want fenced JSON accepted", decision, err)
	}
	decision, err = decodeTriageDecision(`{"needsTools":false,"toolTask":"","reason":"general knowledge"}`)
	if err != nil || decision.NeedsTools || decision.Reason != "general knowledge" {
		t.Fatalf("decision = %+v, err = %v, want bare JSON accepted", decision, err)
	}
	if _, err = decodeTriageDecision("I think tools are needed."); err == nil {
		t.Fatal("prose triage response must be rejected")
	}
}

func TestTriageSystemPromptListsToolsSkillsAndRoot(t *testing.T) {
	registry := filesystemToolRegistry()
	prompt := triageSystemPrompt(registry, []SkillIndexEntry{{Name: "cleanup", Description: "Tidy the workspace"}}, "/tmp/workspace")
	for _, want := range []string{"read_file", "run_command", "cleanup: Tidy the workspace", "/tmp/workspace", "needsTools"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("triage prompt missing %q:\n%s", want, prompt)
		}
	}
	if prompt2 := triageSystemPrompt(registry, nil, "/tmp/workspace"); !strings.Contains(prompt2, "(none)") {
		t.Fatalf("triage prompt without skills should list (none):\n%s", prompt2)
	}
}

func TestTriageChatTurnParsesDecision(t *testing.T) {
	app := NewApp()
	app.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		var payload map[string]any
		data, _ := io.ReadAll(req.Body)
		_ = json.Unmarshal(data, &payload)
		if payload["model"] != "chat-box-model" {
			t.Fatalf("triage model = %v, want chat-box-model", payload["model"])
		}
		if payload["format"] == nil {
			t.Fatal("triage request missing structured output format")
		}
		decision := `{"needsTools":true,"toolTask":"Read status.txt","reason":"workspace question"}`
		body := `{"model":"chat-box-model","message":{"role":"assistant","content":` + strconv.Quote(decision) + `},"done":true,"done_reason":"stop","eval_count":2}`
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
	})
	engine := newHarnessEngine(defaultAppConfig(), app)
	decision, completion := engine.triageChatTurn(context.Background(), ChatRequest{
		BaseURL:  "http://ollama.test",
		Messages: []ChatMessage{{Role: "user", Content: "What is the project status?"}},
	}, "chat-box-model", nil)
	if !decision.NeedsTools || decision.ToolTask != "Read status.txt" || decision.Error != "" {
		t.Fatalf("decision = %+v, want parsed tool request", decision)
	}
	if completion.EvalTokens != 2 {
		t.Fatalf("completion tokens = %d, want telemetry from provider", completion.EvalTokens)
	}
}

func TestTriageChatTurnFailsSafeToToolPath(t *testing.T) {
	app := NewApp()
	app.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusInternalServerError, Status: "500 Internal Server Error", Body: io.NopCloser(strings.NewReader("boom")), Header: http.Header{}}, nil
	})
	engine := newHarnessEngine(defaultAppConfig(), app)
	decision, _ := engine.triageChatTurn(context.Background(), ChatRequest{
		BaseURL:  "http://ollama.test",
		Messages: []ChatMessage{{Role: "user", Content: "anything"}},
	}, "chat-box-model", nil)
	if !decision.NeedsTools {
		t.Fatal("triage failure must fail safe to the tool path (planner can still decline tools)")
	}
	if decision.Error == "" {
		t.Fatal("triage failure must record the error for telemetry")
	}
}

func TestTriageChatTurnStripsImagesFromRequest(t *testing.T) {
	app := NewApp()
	app.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		var payload map[string]any
		data, _ := io.ReadAll(req.Body)
		_ = json.Unmarshal(data, &payload)
		messages, _ := payload["messages"].([]any)
		for _, m := range messages {
			msg, _ := m.(map[string]any)
			if images, ok := msg["images"]; ok && images != nil {
				if imgs, ok := images.([]any); ok && len(imgs) > 0 {
					t.Fatalf("triage request must not include images, got: %v", images)
				}
			}
		}
		decision := `{"needsTools":false,"toolTask":"","reason":"general knowledge"}`
		body := `{"model":"chat-box-model","message":{"role":"assistant","content":` + strconv.Quote(decision) + `},"done":true,"done_reason":"stop","eval_count":1}`
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
	})
	engine := newHarnessEngine(defaultAppConfig(), app)
	decision, _ := engine.triageChatTurn(context.Background(), ChatRequest{
		BaseURL:  "http://ollama.test",
		Messages: []ChatMessage{{Role: "user", Content: "describe this", Images: []string{"data:image/png;base64,AAAA"}}},
	}, "chat-box-model", nil)
	if decision.Error != "" {
		t.Fatalf("decision = %+v, want clean decision with images stripped", decision)
	}
}

func TestTriageChatTurnDecodeErrorFailsSafe(t *testing.T) {
	app := NewApp()
	app.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		body := `{"model":"chat-box-model","message":{"role":"assistant","content":"tools sound useful here"},"done":true,"done_reason":"stop","eval_count":3}`
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
	})
	engine := newHarnessEngine(defaultAppConfig(), app)
	decision, completion := engine.triageChatTurn(context.Background(), ChatRequest{
		BaseURL:  "http://ollama.test",
		Messages: []ChatMessage{{Role: "user", Content: "anything"}},
	}, "chat-box-model", nil)
	if !decision.NeedsTools || decision.Error == "" {
		t.Fatalf("decision = %+v, want fail-safe with recorded decode error", decision)
	}
	if completion.EvalTokens != 3 {
		t.Fatalf("completion tokens = %d, want telemetry preserved on decode failure", completion.EvalTokens)
	}
}

func TestAppendToolEvidenceToSystemUsesFixedNotesOnly(t *testing.T) {
	if got := appendToolEvidenceToSystem("base prompt", HarnessPreparedTurn{}); got != "base prompt" {
		t.Fatalf("system with no tool evidence = %q, want untouched base prompt", got)
	}
	withResults := appendToolEvidenceToSystem("base prompt", HarnessPreparedTurn{
		ToolResults: []HarnessToolResult{{Name: "read_file", Status: "completed"}},
	})
	if !strings.Contains(withResults, toolEvidenceSystemNote) {
		t.Fatalf("system = %q, want tool evidence note appended", withResults)
	}
	withInvalidPlan := appendToolEvidenceToSystem("", HarnessPreparedTurn{PlanValidationErrors: []string{"bad plan"}})
	if withInvalidPlan != invalidPlanSystemNote {
		t.Fatalf("system = %q, want only the invalid-plan note", withInvalidPlan)
	}
	withBoth := appendToolEvidenceToSystem("base prompt", HarnessPreparedTurn{
		ToolResults:          []HarnessToolResult{{Name: "read_file", Status: "completed"}},
		PlanValidationErrors: []string{"bad plan"},
	})
	if !strings.Contains(withBoth, invalidPlanAfterToolsSystemNote) {
		t.Fatalf("system = %q, want mixed tools-ran-but-plan-invalid note", withBoth)
	}
}
