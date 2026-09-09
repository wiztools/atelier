package main

import (
	"context"
	"os"
	"os/exec"
	"testing"
)

// This file holds helpers used only by tests, kept out of the production build
// so they cannot be mistaken for live code paths. They reach into App/harness
// internals that the public entry points already cover; tests use them to drive
// a turn synchronously without the Wails streaming goroutine.

// TestMain pins local-binary detection OFF for the whole package: any test
// that builds a tool registry must not register transcribe_audio just because
// the machine running `go test` happens to have a whisper installed — tool
// gates would become machine-dependent. Tests that exercise the local path
// restore the real lookup via withRealLocalLookup/stubLocalLookup
// (local_tools_test.go).
func TestMain(m *testing.M) {
	localBinaryLookPath = func(string) (string, error) { return "", exec.ErrNotFound }
	os.Exit(m.Run())
}

// runChatStream drives a chat turn synchronously for tests: it loads config,
// builds an engine, and runs the full harness pipeline inline (no goroutine,
// no request-id registration). Tests call this instead of the async StreamChat
// so they can assert on emitted events deterministically.
func (a *App) runChatStream(ctx context.Context, requestID string, req ChatRequest) {
	config, err := loadReadyConfig()
	if err != nil {
		a.emitChatEvent(ChatStreamEvent{RequestID: requestID, Error: err.Error(), Done: true})
		return
	}
	newHarnessEngine(config, a).RunChatStream(ctx, requestID, req, false)
}

// parseHarnessToolPlan parses a planner response using the default filesystem
// tool registry. Production uses parseHarnessToolPlanWithRegistry with the
// live registry; tests use this default-registry convenience wrapper.
func parseHarnessToolPlan(content string) (HarnessToolPlan, []string) {
	return parseHarnessToolPlanWithRegistry(content, filesystemToolRegistry())
}
