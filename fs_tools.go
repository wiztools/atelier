package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	defaultToolTimeoutMS      = 30 * 1000
	maxToolTimeoutMS          = 5 * 60 * 1000
	defaultToolMaxOutputBytes = 64 * 1024
	maxToolOutputBytes        = 512 * 1024
	maxToolReadBytes          = 512 * 1024
)

type FilesystemToolLayer struct {
	config ConfigFilesystemTool
	root   string
}

type ToolCommandRequest struct {
	Command   string            `json:"command"`
	Args      []string          `json:"args,omitempty"`
	Cwd       string            `json:"cwd,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	TimeoutMS int               `json:"timeoutMs,omitempty"`
}

type ToolCommandResult struct {
	Command    []string `json:"command"`
	Cwd        string   `json:"cwd"`
	ExitCode   int      `json:"exitCode"`
	Stdout     string   `json:"stdout,omitempty"`
	Stderr     string   `json:"stderr,omitempty"`
	DurationMS int64    `json:"durationMs"`
	Truncated  bool     `json:"truncated,omitempty"`
	Error      string   `json:"error,omitempty"`
}

type ToolFileListRequest struct {
	Path string `json:"path,omitempty"`
}

type ToolFileListResult struct {
	Root    string          `json:"root"`
	Path    string          `json:"path"`
	Entries []ToolFileEntry `json:"entries"`
}

type ToolFileEntry struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	IsDir bool   `json:"isDir"`
	Size  int64  `json:"size"`
}

type ToolFileReadRequest struct {
	Path        string `json:"path"`
	MaxBytes    int    `json:"maxBytes,omitempty"`
	AllowBinary bool   `json:"allowBinary,omitempty"`
}

type ToolFileReadResult struct {
	Path      string `json:"path"`
	Content   string `json:"content"`
	Bytes     int    `json:"bytes"`
	Truncated bool   `json:"truncated,omitempty"`
}

type ToolFileWriteRequest struct {
	Path      string `json:"path"`
	Content   string `json:"content"`
	Append    bool   `json:"append,omitempty"`
	Overwrite bool   `json:"overwrite,omitempty"`
}

type ToolFileWriteResult struct {
	Path  string `json:"path"`
	Bytes int    `json:"bytes"`
}

type limitedBuffer struct {
	limit     int
	buffer    bytes.Buffer
	truncated bool
}

func newFilesystemToolLayer(config ConfigFilesystemTool) *FilesystemToolLayer {
	config = mergeToolsConfig(ConfigTools{Filesystem: config}, defaultAppConfig().Tools).Filesystem
	root := normalizeStoragePath(config.Root)
	if absolute, err := filepath.Abs(root); err == nil {
		root = absolute
	}
	config.Root = root
	return &FilesystemToolLayer{config: config, root: root}
}

func (t *FilesystemToolLayer) RunCommand(ctx context.Context, req ToolCommandRequest) (ToolCommandResult, error) {
	command := strings.TrimSpace(req.Command)
	if command == "" {
		return ToolCommandResult{}, errors.New("command is required")
	}
	cwd, err := t.resolvePath(req.Cwd)
	if err != nil {
		return ToolCommandResult{}, err
	}
	if err := os.MkdirAll(cwd, 0755); err != nil {
		return ToolCommandResult{}, err
	}
	if err := t.validateCommandPolicy(command, req.Args, req.Env, cwd); err != nil {
		return ToolCommandResult{}, err
	}

	timeoutMS := req.TimeoutMS
	if timeoutMS <= 0 {
		timeoutMS = t.config.TimeoutMS
	}
	if timeoutMS <= 0 {
		timeoutMS = defaultToolTimeoutMS
	}
	if timeoutMS > maxToolTimeoutMS {
		timeoutMS = maxToolTimeoutMS
	}

	runCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutMS)*time.Millisecond)
	defer cancel()

	cmd := exec.CommandContext(runCtx, command, req.Args...)
	cmd.Dir = cwd
	cmd.Env = buildToolEnv()

	stdout := &limitedBuffer{limit: t.outputLimit()}
	stderr := &limitedBuffer{limit: t.outputLimit()}
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	startedAt := time.Now()
	err = cmd.Run()
	duration := time.Since(startedAt)

	result := ToolCommandResult{
		Command:    append([]string{command}, req.Args...),
		Cwd:        cwd,
		ExitCode:   0,
		Stdout:     stdout.String(),
		Stderr:     stderr.String(),
		DurationMS: duration.Milliseconds(),
		Truncated:  stdout.truncated || stderr.truncated,
	}
	if runCtx.Err() == context.DeadlineExceeded {
		result.ExitCode = -1
		result.Error = fmt.Sprintf("command timed out after %dms", timeoutMS)
		return result, nil
	}
	if err != nil {
		result.ExitCode = 1
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			result.ExitCode = exitErr.ExitCode()
		} else {
			result.Error = err.Error()
		}
	}
	return result, nil
}

func (t *FilesystemToolLayer) ListFiles(req ToolFileListRequest) (ToolFileListResult, error) {
	dir, err := t.resolvePath(req.Path)
	if err != nil {
		return ToolFileListResult{}, err
	}
	if dir == t.root {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return ToolFileListResult{}, err
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ToolFileListResult{}, err
	}
	result := ToolFileListResult{
		Root:    t.root,
		Path:    dir,
		Entries: []ToolFileEntry{},
	}
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			return ToolFileListResult{}, err
		}
		path := filepath.Join(dir, entry.Name())
		result.Entries = append(result.Entries, ToolFileEntry{
			Name:  entry.Name(),
			Path:  path,
			IsDir: entry.IsDir(),
			Size:  info.Size(),
		})
	}
	return result, nil
}

func (t *FilesystemToolLayer) ReadFile(req ToolFileReadRequest) (ToolFileReadResult, error) {
	path, err := t.resolvePath(req.Path)
	if err != nil {
		return ToolFileReadResult{}, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return ToolFileReadResult{}, err
	}
	if info.IsDir() {
		return ToolFileReadResult{}, fmt.Errorf("%q is a directory", path)
	}
	maxBytes := req.MaxBytes
	if maxBytes <= 0 || maxBytes > maxToolReadBytes {
		maxBytes = maxToolReadBytes
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ToolFileReadResult{}, err
	}
	truncated := false
	if len(data) > maxBytes {
		data = data[:maxBytes]
		truncated = true
	}
	if !req.AllowBinary && bytes.IndexByte(data, 0) >= 0 {
		return ToolFileReadResult{}, fmt.Errorf("%q appears to be binary", path)
	}
	return ToolFileReadResult{
		Path:      path,
		Content:   string(data),
		Bytes:     len(data),
		Truncated: truncated,
	}, nil
}

func (t *FilesystemToolLayer) WriteFile(req ToolFileWriteRequest) (ToolFileWriteResult, error) {
	path, err := t.resolvePath(req.Path)
	if err != nil {
		return ToolFileWriteResult{}, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return ToolFileWriteResult{}, err
	}
	flags := os.O_CREATE | os.O_WRONLY
	if req.Append {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
	}
	if !req.Overwrite && !req.Append {
		flags |= os.O_EXCL
	}
	file, err := os.OpenFile(path, flags, 0644)
	if err != nil {
		return ToolFileWriteResult{}, err
	}
	defer file.Close()
	written, err := file.WriteString(req.Content)
	if err != nil {
		return ToolFileWriteResult{}, err
	}
	return ToolFileWriteResult{Path: path, Bytes: written}, nil
}

func (t *FilesystemToolLayer) resolvePath(path string) (string, error) {
	root := t.root
	if root == "" {
		root = defaultAppConfig().Tools.Filesystem.Root
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	realRoot, err := resolveExistingPathForBoundary(root)
	if err != nil {
		return "", err
	}
	target := strings.TrimSpace(path)
	if target == "" {
		target = root
	} else if target == "~" || strings.HasPrefix(target, "~/") {
		target = normalizeStoragePath(target)
	} else if filepath.IsAbs(target) {
		target = normalizeStoragePath(target)
	} else {
		target = filepath.Join(root, target)
	}
	target, err = filepath.Abs(target)
	if err != nil {
		return "", err
	}
	realTarget, err := resolveExistingPathForBoundary(target)
	if err != nil {
		return "", err
	}
	if !pathWithinRoot(realRoot, realTarget) {
		return "", fmt.Errorf("%q is outside filesystem tool root %q", target, root)
	}
	return target, nil
}

func (t *FilesystemToolLayer) outputLimit() int {
	if t.config.MaxOutputBytes <= 0 {
		return defaultToolMaxOutputBytes
	}
	if t.config.MaxOutputBytes > maxToolOutputBytes {
		return maxToolOutputBytes
	}
	return t.config.MaxOutputBytes
}

func pathWithinRoot(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func resolveExistingPathForBoundary(target string) (string, error) {
	if realTarget, err := filepath.EvalSymlinks(target); err == nil {
		return realTarget, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}

	candidate := target
	missing := []string{}
	for {
		if candidate == "" || candidate == "." {
			return target, nil
		}
		if realCandidate, err := filepath.EvalSymlinks(candidate); err == nil {
			for index := len(missing) - 1; index >= 0; index-- {
				realCandidate = filepath.Join(realCandidate, missing[index])
			}
			return realCandidate, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(candidate)
		if parent == candidate {
			return target, nil
		}
		missing = append(missing, filepath.Base(candidate))
		candidate = parent
	}
}

func (t *FilesystemToolLayer) validateCommandPolicy(command string, args []string, env map[string]string, cwd string) error {
	name := normalizedCommandName(command)
	if !commandAllowed(name, t.config.AllowedCommands) {
		return fmt.Errorf("%q is not in the filesystem tool command allowlist", name)
	}
	if err := validateCommandPath(command, name); err != nil {
		return err
	}
	if len(reqEnvWithoutBlanks(env)) > 0 {
		return errors.New("command environment overrides are not allowed by the filesystem tool")
	}
	if err := t.validateCommandSpecificArgs(name, args, cwd); err != nil {
		return err
	}
	if err := t.validateCommandArgs(args, cwd); err != nil {
		return err
	}
	return nil
}

func normalizedCommandName(command string) string {
	name := strings.ToLower(filepath.Base(strings.TrimSpace(command)))
	if runtime.GOOS == "windows" && strings.HasSuffix(name, ".exe") {
		name = strings.TrimSuffix(name, ".exe")
	}
	return name
}

func commandAllowed(name string, allowed []string) bool {
	for _, command := range allowed {
		if normalizedCommandName(command) == name {
			return true
		}
	}
	return false
}

func validateCommandPath(command, name string) error {
	if !strings.ContainsAny(command, `/\`) {
		return nil
	}
	resolvedCommand, err := filepath.EvalSymlinks(command)
	if err != nil {
		return err
	}
	pathCommand, err := exec.LookPath(name)
	if err != nil {
		return fmt.Errorf("%q is allowed, but no trusted executable was found on PATH: %w", name, err)
	}
	resolvedPathCommand, err := filepath.EvalSymlinks(pathCommand)
	if err != nil {
		return err
	}
	if resolvedCommand != resolvedPathCommand {
		return fmt.Errorf("%q resolves to %q, not trusted executable %q", command, resolvedCommand, resolvedPathCommand)
	}
	return nil
}

func (t *FilesystemToolLayer) validateCommandSpecificArgs(name string, args []string, cwd string) error {
	realRoot, err := resolveExistingPathForBoundary(t.root)
	if err != nil {
		return err
	}
	for index, arg := range args {
		trimmed := strings.TrimSpace(arg)
		flag := commandFlagName(trimmed)
		if flag == "" {
			continue
		}
		if commandFlagDenied(name, flag) {
			return fmt.Errorf("%s argument %q is not allowed by the filesystem tool", name, trimmed)
		}
		if commandFlagRequiresWorkspacePath(name, flag) {
			value := ""
			if _, after, ok := strings.Cut(trimmed, "="); ok {
				value = after
			} else if index < len(args)-1 {
				value = args[index+1]
			}
			if strings.TrimSpace(value) == "" {
				return fmt.Errorf("%s argument %q requires a workspace-scoped path", name, trimmed)
			}
			if err := validateCommandArgPathWithinRoot(realRoot, cwd, value); err != nil {
				return err
			}
		}
	}
	return nil
}

func commandFlagName(arg string) string {
	if !strings.HasPrefix(arg, "-") || arg == "-" {
		return ""
	}
	if before, _, ok := strings.Cut(arg, "="); ok {
		return before
	}
	return arg
}

func commandFlagDenied(name, flag string) bool {
	denied := map[string]map[string]bool{
		"find": {
			"-delete": true, "-exec": true, "-execdir": true, "-ok": true, "-okdir": true,
			"-fls": true, "-fprint": true, "-fprintf": true,
		},
		"grep": {
			"--include-from": true, "--exclude-from": true,
		},
		"rg": {
			"--pre": true, "--pre-glob": true, "--config": true,
		},
		"rm": {
			"-r": true, "-R": true, "--recursive": true,
		},
		"rmdir": {
			"-r": true, "-R": true, "--recursive": true,
		},
	}
	if (name == "rm" || name == "rmdir") && strings.HasPrefix(flag, "-") && strings.Contains(flag, "r") {
		return true
	}
	return denied[name][flag]
}

func commandFlagRequiresWorkspacePath(name, flag string) bool {
	pathFlags := map[string]map[string]bool{
		"grep": {"-f": true, "--file": true},
		"rg":   {"-f": true, "--file": true},
	}
	return pathFlags[name][flag]
}

func (t *FilesystemToolLayer) validateCommandArgs(args []string, cwd string) error {
	realRoot, err := resolveExistingPathForBoundary(t.root)
	if err != nil {
		return err
	}
	for _, arg := range args {
		for _, candidate := range commandArgPathCandidates(arg) {
			if err := validateCommandArgPathWithinRoot(realRoot, cwd, candidate); err != nil {
				return err
			}
		}
		if commandArgMayBeBarePath(arg) {
			target := filepath.Join(cwd, strings.TrimSpace(arg))
			if _, err := os.Lstat(target); err == nil {
				if err := validateCommandArgPathWithinRoot(realRoot, cwd, arg); err != nil {
					return err
				}
			} else if !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	}
	return nil
}

func commandArgPathCandidates(arg string) []string {
	arg = strings.TrimSpace(arg)
	if arg == "" || arg == "-" {
		return nil
	}
	if strings.HasPrefix(arg, "-") {
		if before, after, ok := strings.Cut(arg, "="); ok && before != "" && looksLikeCommandPath(after) {
			return []string{after}
		}
		return nil
	}
	if looksLikeCommandPath(arg) {
		return []string{arg}
	}
	return nil
}

func looksLikeCommandPath(arg string) bool {
	return arg == "." || arg == ".." || strings.HasPrefix(arg, "~/") || strings.ContainsAny(arg, `/\`)
}

func commandArgMayBeBarePath(arg string) bool {
	arg = strings.TrimSpace(arg)
	return arg != "" && arg != "-" && !strings.HasPrefix(arg, "-") && !looksLikeCommandPath(arg)
}

func validateCommandArgPathWithinRoot(realRoot, cwd, candidate string) error {
	target := candidate
	if target == "~" || strings.HasPrefix(target, "~/") {
		target = normalizeStoragePath(target)
	} else if !filepath.IsAbs(target) {
		target = filepath.Join(cwd, target)
	}
	target, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	realTarget, err := resolveExistingPathForBoundary(target)
	if err != nil {
		return err
	}
	if !pathWithinRoot(realRoot, realTarget) {
		return fmt.Errorf("command argument %q resolves outside filesystem tool root", candidate)
	}
	return nil
}

func buildToolEnv() []string {
	allowed := map[string]bool{
		"PATH": true, "HOME": true, "TMPDIR": true, "TMP": true, "TEMP": true,
		"LANG": true, "LC_ALL": true, "LC_CTYPE": true, "TERM": true, "NO_COLOR": true,
		"SystemRoot": true, "WINDIR": true, "PATHEXT": true,
	}
	result := []string{}
	for _, item := range os.Environ() {
		key, value, ok := strings.Cut(item, "=")
		if ok && allowed[key] {
			result = append(result, key+"="+value)
		}
	}
	return result
}

func reqEnvWithoutBlanks(env map[string]string) map[string]string {
	result := map[string]string{}
	for key, value := range env {
		if strings.TrimSpace(key) != "" || strings.TrimSpace(value) != "" {
			result[key] = value
		}
	}
	return result
}

func (b *limitedBuffer) Write(data []byte) (int, error) {
	if b.limit <= 0 {
		b.limit = defaultToolMaxOutputBytes
	}
	remaining := b.limit - b.buffer.Len()
	if remaining <= 0 {
		b.truncated = true
		return len(data), nil
	}
	if len(data) > remaining {
		_, _ = b.buffer.Write(data[:remaining])
		b.truncated = true
		return len(data), nil
	}
	_, _ = b.buffer.Write(data)
	return len(data), nil
}

func (b *limitedBuffer) String() string {
	return b.buffer.String()
}
