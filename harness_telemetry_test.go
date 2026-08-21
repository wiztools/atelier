package main

// Tests for harness telemetry: token accounting (prompt + completion per
// model-call step), time-to-first-token on streaming steps, and the rule that
// bookkeeping steps carry no token counts (so per-model usage aggregation
// counts each model call exactly once).

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// telemetryTestConfig builds an isolated config with distinct harness and
// primary models against a mocked Ollama endpoint.
func telemetryTestConfig(t *testing.T) AppConfig {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)

	config := defaultAppConfig()
	config.Storage = ConfigStorage{
		Root:      filepath.Join(home, ".atelier"),
		History:   filepath.Join(home, ".atelier", "history"),
		Artifacts: filepath.Join(home, ".atelier", "history"),
	}
	config.Providers.Ollama.BaseURL = "http://ollama.test"
	config.Providers.Ollama.Models.Primary = "chat-box-model"
	config.Providers.Ollama.Models.Harness = "harness-model"
	if err := writeAppConfig(config); err != nil {
		t.Fatalf("writeAppConfig returned error: %v", err)
	}
	return config
}

func TestHarnessStepTokenAccountingDirectPath(t *testing.T) {
	config := telemetryTestConfig(t)

	app := NewApp()
	app.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/api/chat" {
			return notFoundResponse(), nil
		}
		var payload map[string]any
		data, _ := io.ReadAll(req.Body)
		if err := json.Unmarshal(data, &payload); err != nil {
			t.Fatalf("provider request body is not JSON: %v", err)
		}
		if payload["stream"] == false {
			decision := `{"needsTools":false,"responseMode":"text","toolTask":"","reason":"General knowledge answer."}`
			body := `{"model":"harness-model","message":{"role":"assistant","content":` + strconv.Quote(decision) + `},"done":true,"done_reason":"stop","prompt_eval_count":11,"eval_count":2}`
			return jsonResponse(body), nil
		}
		// Delay the first line so time-to-first-token is deterministically
		// nonzero: the streaming step's FirstTokenMS is measured from stream
		// open to the first visible delta.
		pr, pw := io.Pipe()
		go func() {
			time.Sleep(20 * time.Millisecond)
			fmt.Fprintln(pw, `{"model":"chat-box-model","message":{"role":"assistant","content":"Hello."},"done":false}`)
			fmt.Fprintln(pw, `{"model":"chat-box-model","done":true,"done_reason":"stop","prompt_eval_count":22,"eval_count":3}`)
			pw.Close()
		}()
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Body:       pr,
			Header:     http.Header{"Content-Type": []string{"application/x-ndjson"}},
		}, nil
	})

	app.runChatStream(context.Background(), "request-tokens", ChatRequest{
		BaseURL: "http://ollama.test",
		Model:   "chat-box-model",
		Messages: []ChatMessage{
			{Role: "user", Content: "Say hello"},
		},
	})

	run := persistedHarnessRun(t, config, 1)
	steps, _ := run["steps"].([]any)

	triageStep := harnessStepByKind(t, steps, "triage")
	if got := triageStep["promptTokens"]; got != float64(11) {
		t.Fatalf("triage step promptTokens = %v, want 11", got)
	}
	if got := triageStep["tokens"]; got != float64(2) {
		t.Fatalf("triage step tokens = %v, want 2", got)
	}

	streamingStep := harnessStepByKind(t, steps, "streaming")
	if got := streamingStep["promptTokens"]; got != float64(22) {
		t.Fatalf("streaming step promptTokens = %v, want 22", got)
	}
	if got := streamingStep["tokens"]; got != float64(3) {
		t.Fatalf("streaming step tokens = %v, want 3", got)
	}
	firstToken, ok := streamingStep["firstTokenMs"].(float64)
	if !ok || firstToken < 5 {
		t.Fatalf("streaming step firstTokenMs = %v, want >= 5ms after the delayed first chunk", streamingStep["firstTokenMs"])
	}

	savedStep := harnessStepByKind(t, steps, "saved")
	if _, exists := savedStep["tokens"]; exists {
		t.Fatalf("saved step carries tokens (%v); bookkeeping steps must not duplicate model-call usage", savedStep["tokens"])
	}
	if _, exists := savedStep["promptTokens"]; exists {
		t.Fatalf("saved step carries promptTokens (%v); bookkeeping steps must not duplicate model-call usage", savedStep["promptTokens"])
	}
}

func TestHarnessStepTokenAccountingTooledPath(t *testing.T) {
	config := telemetryTestConfig(t)
	root := filepath.Join(filepath.Dir(config.Storage.Root), "tool-root")
	config.Tools.Filesystem.Root = root
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
	nonStreamCount := 0
	app.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/api/chat" {
			return notFoundResponse(), nil
		}
		var payload map[string]any
		data, _ := io.ReadAll(req.Body)
		if err := json.Unmarshal(data, &payload); err != nil {
			t.Fatalf("provider request body is not JSON: %v", err)
		}
		if payload["stream"] == false {
			nonStreamCount++
			if nonStreamCount == 1 {
				decision := `{"needsTools":true,"responseMode":"text","toolTask":"Read status.txt to answer.","reason":"The status lives in the workspace."}`
				body := `{"model":"harness-model","message":{"role":"assistant","content":` + strconv.Quote(decision) + `},"done":true,"done_reason":"stop","prompt_eval_count":11,"eval_count":2}`
				return jsonResponse(body), nil
			}
			body := "{\"model\":\"harness-model\",\"message\":{\"role\":\"assistant\",\"content\":\"```json\\n{\\\"brief\\\":\\\"Use the status file.\\\",\\\"needsTools\\\":true,\\\"reason\\\":\\\"Need the file.\\\",\\\"toolCalls\\\":[{\\\"name\\\":\\\"read_file\\\",\\\"path\\\":\\\"status.txt\\\"}]}\\n```\"},\"done\":true,\"done_reason\":\"stop\",\"prompt_eval_count\":33,\"eval_count\":4}"
			if nonStreamCount > 2 {
				body = "{\"model\":\"harness-model\",\"message\":{\"role\":\"assistant\",\"content\":\"```json\\n{\\\"brief\\\":\\\"Answer from the file.\\\",\\\"needsTools\\\":false,\\\"reason\\\":\\\"Enough context.\\\",\\\"toolCalls\\\":[]}\\n```\"},\"done\":true,\"done_reason\":\"stop\",\"prompt_eval_count\":44,\"eval_count\":5}"
			}
			return jsonResponse(body), nil
		}
		body := fmt.Sprintln(`{"model":"chat-box-model","message":{"role":"assistant","content":"The project is green."},"done":false}`) +
			fmt.Sprintln(`{"model":"chat-box-model","done":true,"done_reason":"stop","prompt_eval_count":22,"eval_count":3}`)
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

	run := persistedHarnessRun(t, config, 1)
	steps, _ := run["steps"].([]any)

	planningSteps := harnessStepsByKind(t, steps, "planning")
	if len(planningSteps) != 2 {
		t.Fatalf("planning steps = %d, want 2 rounds", len(planningSteps))
	}
	if got := planningSteps[0]["promptTokens"]; got != float64(33) {
		t.Fatalf("planning round 1 promptTokens = %v, want 33", got)
	}
	if got := planningSteps[1]["promptTokens"]; got != float64(44) {
		t.Fatalf("planning round 2 promptTokens = %v, want 44", got)
	}

	streamingStep := harnessStepByKind(t, steps, "streaming")
	if got := streamingStep["promptTokens"]; got != float64(22) {
		t.Fatalf("streaming step promptTokens = %v, want 22", got)
	}
}

func TestHarnessFailedTurnPersistsRun(t *testing.T) {
	config := telemetryTestConfig(t)

	app := NewApp()
	app.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/api/chat" {
			return notFoundResponse(), nil
		}
		var payload map[string]any
		data, _ := io.ReadAll(req.Body)
		if err := json.Unmarshal(data, &payload); err != nil {
			t.Fatalf("provider request body is not JSON: %v", err)
		}
		if payload["stream"] == false {
			decision := `{"needsTools":false,"responseMode":"text","toolTask":"","reason":"General knowledge answer."}`
			body := `{"model":"harness-model","message":{"role":"assistant","content":` + strconv.Quote(decision) + `},"done":true,"done_reason":"stop","prompt_eval_count":11,"eval_count":2}`
			return jsonResponse(body), nil
		}
		return &http.Response{
			StatusCode: http.StatusInternalServerError,
			Status:     "500 Internal Server Error",
			Body:       io.NopCloser(strings.NewReader("model exploded")),
			Header:     http.Header{},
		}, nil
	})

	app.runChatStream(context.Background(), "request-fail", ChatRequest{
		BaseURL: "http://ollama.test",
		Model:   "chat-box-model",
		Messages: []ChatMessage{
			{Role: "user", Content: "Say hello"},
		},
	})

	turn := persistedAssistantTurn(t, config, 1)
	errText, _ := turn.ProviderResponse["error"].(string)
	if !strings.Contains(errText, "500") {
		t.Fatalf("failed turn providerResponse.error = %q, want the stream error text", errText)
	}
	if text := historyTextForTest(turn.Content, "text"); !strings.Contains(text, "500") {
		t.Fatalf("failed turn content = %q, want the error text rendered as content", text)
	}
	run, ok := turn.ProviderResponse["harnessRun"].(map[string]any)
	if !ok {
		t.Fatalf("failed turn missing harness run: %+v", turn.ProviderResponse)
	}
	if run["status"] != "failed" {
		t.Fatalf("failed turn run status = %v, want failed", run["status"])
	}
	steps, _ := run["steps"].([]any)
	// The stream never opened, so the model_call step itself failed.
	modelCallStep := harnessStepByKind(t, steps, "model_call")
	if modelCallStep["status"] != "failed" {
		t.Fatalf("model_call step status = %v, want failed", modelCallStep["status"])
	}
	// The triage model call really happened before the stream failed, so its
	// tokens must survive in the persisted ledger.
	if got := harnessStepByKind(t, steps, "triage")["tokens"]; got != float64(2) {
		t.Fatalf("triage step tokens = %v, want 2 on the failed turn", got)
	}
}

func TestHarnessRequestSnapshotsRecordTruncation(t *testing.T) {
	config := telemetryTestConfig(t)

	app := NewApp()
	app.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/api/chat" {
			return notFoundResponse(), nil
		}
		var payload map[string]any
		data, _ := io.ReadAll(req.Body)
		if err := json.Unmarshal(data, &payload); err != nil {
			t.Fatalf("provider request body is not JSON: %v", err)
		}
		if payload["stream"] == false {
			decision := `{"needsTools":false,"responseMode":"text","toolTask":"","reason":"General knowledge answer."}`
			return jsonResponse(`{"model":"harness-model","message":{"role":"assistant","content":` + strconv.Quote(decision) + `},"done":true,"done_reason":"stop","eval_count":2}`), nil
		}
		body := fmt.Sprintln(`{"model":"chat-box-model","message":{"role":"assistant","content":"Hello."},"done":false}`) +
			fmt.Sprintln(`{"model":"chat-box-model","done":true,"done_reason":"stop","eval_count":3}`)
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     http.Header{"Content-Type": []string{"application/x-ndjson"}},
		}, nil
	})

	// Oversized first message forces truncateChatHistory to drop it on every
	// model call (triage and final response both budget against num_ctx).
	messages := []ChatMessage{
		{Role: "user", Content: strings.Repeat("history ", 6000)},
		{Role: "assistant", Content: "earlier reply"},
		{Role: "user", Content: "Say hello"},
	}
	app.runChatStream(context.Background(), "request-snap", ChatRequest{
		BaseURL:  "http://ollama.test",
		Model:    "chat-box-model",
		Messages: messages,
	})

	run := persistedHarnessRun(t, config, 1)
	steps, _ := run["steps"].([]any)

	triageRequest, ok := harnessStepByKind(t, steps, "triage")["request"].(map[string]any)
	if !ok {
		t.Fatalf("triage step missing request snapshot: %+v", harnessStepByKind(t, steps, "triage"))
	}
	if hash, _ := triageRequest["promptHash"].(string); len(hash) != 12 {
		t.Fatalf("triage promptHash = %q, want 12 hex chars", hash)
	}
	if got := triageRequest["numCtx"]; got != float64(defaultOllamaNumCtx) {
		t.Fatalf("triage snapshot numCtx = %v, want %d", got, defaultOllamaNumCtx)
	}
	if got := triageRequest["toolMode"]; got != "format" {
		t.Fatalf("triage snapshot toolMode = %v, want format (triage uses the JSON schema)", got)
	}
	if got := triageRequest["truncatedMessages"]; got != float64(1) {
		t.Fatalf("triage snapshot truncatedMessages = %v, want 1 (oversized oldest message dropped)", got)
	}
	if _, ok := triageRequest["toolsHash"].(string); !ok || triageRequest["toolsHash"] == "" {
		t.Fatalf("triage snapshot toolsHash = %v, want the schema hash", triageRequest["toolsHash"])
	}

	modelCallRequest, ok := harnessStepByKind(t, steps, "model_call")["request"].(map[string]any)
	if !ok {
		t.Fatalf("model_call step missing request snapshot: %+v", harnessStepByKind(t, steps, "model_call"))
	}
	if got, exists := modelCallRequest["toolMode"]; exists && got != "" {
		t.Fatalf("model_call snapshot toolMode = %v, want absent/empty (final responses are tool-free)", got)
	}
	if got := modelCallRequest["truncatedMessages"]; got != float64(1) {
		t.Fatalf("model_call snapshot truncatedMessages = %v, want 1", got)
	}
	if chars, _ := modelCallRequest["promptChars"].(float64); chars < 50 {
		t.Fatalf("model_call snapshot promptChars = %v, want the trimmed history (marker + survivors)", modelCallRequest["promptChars"])
	}
}

// TestHarnessTelemetryCoversEveryModelCall pins the "model-visible ⟺
// logged" convention at the HTTP boundary: the mocked transport counts every
// provider call the harness actually made, and the persisted run must carry
// exactly one model-call step per counted call, each identifying its
// provider+model. A new provider call site that forgets to record a step (or
// a step that claims a call that never ran) breaks this test.
func TestHarnessTelemetryCoversEveryModelCall(t *testing.T) {
	config := telemetryTestConfig(t)
	root := filepath.Join(filepath.Dir(config.Storage.Root), "tool-root")
	config.Tools.Filesystem.Root = root
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
	nonStreamCalls := 0
	streamCalls := 0
	app.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/api/chat" {
			return notFoundResponse(), nil
		}
		var payload map[string]any
		data, _ := io.ReadAll(req.Body)
		if err := json.Unmarshal(data, &payload); err != nil {
			t.Fatalf("provider request body is not JSON: %v", err)
		}
		if payload["stream"] == false {
			nonStreamCalls++
			if nonStreamCalls == 1 {
				decision := `{"needsTools":true,"responseMode":"text","toolTask":"Read status.txt to answer.","reason":"The status lives in the workspace."}`
				return jsonResponse(`{"model":"harness-model","message":{"role":"assistant","content":` + strconv.Quote(decision) + `},"done":true,"done_reason":"stop","eval_count":2}`), nil
			}
			body := "{\"model\":\"harness-model\",\"message\":{\"role\":\"assistant\",\"content\":\"```json\\n{\\\"brief\\\":\\\"Use the status file.\\\",\\\"needsTools\\\":true,\\\"reason\\\":\\\"Need the file.\\\",\\\"toolCalls\\\":[{\\\"name\\\":\\\"read_file\\\",\\\"path\\\":\\\"status.txt\\\"}]}\\n```\"},\"done\":true,\"done_reason\":\"stop\",\"eval_count\":4}"
			if nonStreamCalls > 2 {
				body = "{\"model\":\"harness-model\",\"message\":{\"role\":\"assistant\",\"content\":\"```json\\n{\\\"brief\\\":\\\"Answer from the file.\\\",\\\"needsTools\\\":false,\\\"reason\\\":\\\"Enough context.\\\",\\\"toolCalls\\\":[]}\\n```\"},\"done\":true,\"done_reason\":\"stop\",\"eval_count\":5}"
			}
			return jsonResponse(body), nil
		}
		streamCalls++
		body := fmt.Sprintln(`{"model":"chat-box-model","message":{"role":"assistant","content":"The project is green."},"done":false}`) +
			fmt.Sprintln(`{"model":"chat-box-model","done":true,"done_reason":"stop","eval_count":3}`)
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     http.Header{"Content-Type": []string{"application/x-ndjson"}},
		}, nil
	})

	app.runChatStream(context.Background(), "request-coverage", ChatRequest{
		BaseURL: "http://ollama.test",
		Model:   "chat-box-model",
		Messages: []ChatMessage{
			{Role: "user", Content: "What is the project status?"},
		},
	})

	if nonStreamCalls < 3 || streamCalls != 1 {
		t.Fatalf("provider calls = %d non-stream / %d stream, want 3+ non-stream (triage + 2 planning rounds) and exactly 1 stream", nonStreamCalls, streamCalls)
	}

	run := persistedHarnessRun(t, config, 1)
	steps, _ := run["steps"].([]any)

	// Every non-stream harness call (triage + planning rounds) has a step.
	recordedHarnessCalls := len(harnessStepsByKind(t, steps, "triage")) + len(harnessStepsByKind(t, steps, "planning"))
	if recordedHarnessCalls != nonStreamCalls {
		t.Fatalf("recorded harness-call steps = %d, but %d non-stream provider calls ran — a model call is missing from the ledger", recordedHarnessCalls, nonStreamCalls)
	}
	// The single streamed response is covered by its model_call + streaming pair.
	if got := len(harnessStepsByKind(t, steps, "model_call")); got != streamCalls {
		t.Fatalf("model_call steps = %d, want %d", got, streamCalls)
	}
	if got := len(harnessStepsByKind(t, steps, "streaming")); got != streamCalls {
		t.Fatalf("streaming steps = %d, want %d", got, streamCalls)
	}

	// Every model-call step identifies which provider+model it talked to.
	modelCallKinds := map[string]bool{"triage": true, "skill": true, "planning": true, "model_call": true, "streaming": true}
	for _, step := range steps {
		typed, _ := step.(map[string]any)
		if typed == nil || !modelCallKinds[typed["kind"].(string)] {
			continue
		}
		if provider, _ := typed["provider"].(string); provider == "" {
			t.Fatalf("step %v carries no provider — model-call steps must identify their target", typed)
		}
		if model, _ := typed["model"].(string); model == "" {
			t.Fatalf("step %v carries no model — model-call steps must identify their target", typed)
		}
	}
}

func TestToolActivitiesRecordPermissionDecision(t *testing.T) {
	engine := newHarnessEngine(defaultAppConfig())
	results := []HarnessToolResult{
		{Name: "write_file", Status: "denied", Error: "permission denied", Permission: &ToolPermissionDecision{Outcome: permissionOutcomeTimeout, WaitMS: 120000}},
		{Name: "write_file", Status: "completed", Permission: &ToolPermissionDecision{Approved: true, Outcome: permissionOutcomeApproved, WaitMS: 1500}},
	}

	activities := engine.toolActivities(results, []HarnessToolCall{
		{Name: "write_file", Path: "a.txt", Content: "x"},
		{Name: "write_file", Path: "b.txt", Content: "y"},
	})

	if activities[0].Permission != permissionOutcomeTimeout || activities[0].PermissionWaitMS != 120000 {
		t.Fatalf("denied activity permission = %q/%dms, want timeout/120000ms", activities[0].Permission, activities[0].PermissionWaitMS)
	}
	if activities[1].Permission != permissionOutcomeApproved || activities[1].PermissionWaitMS != 1500 {
		t.Fatalf("approved activity permission = %q/%dms, want approved/1500ms", activities[1].Permission, activities[1].PermissionWaitMS)
	}
	// The planner's Call params stay zipped alongside the permission telemetry.
	if activities[0].Call.Path != "a.txt" {
		t.Fatalf("denied activity call path = %q, want a.txt", activities[0].Call.Path)
	}
}

func TestToolActivitiesRecordMediaModelConsumption(t *testing.T) {
	engine := newHarnessEngine(defaultAppConfig())
	results := []HarnessToolResult{
		{Name: "generate_video", Status: "completed", Result: ToolVideoResult{Model: "bytedance/seedance-2.0/reference-to-video", Count: 1}},
		{Name: "generate_video", Status: "failed", Error: "fal queue unavailable"},
	}

	activities := engine.toolActivities(results, []HarnessToolCall{
		{Name: "generate_video", Model: "planner-requested-model", Content: "a banana runs", Duration: "15"},
		{Name: "generate_video", Content: "a banana runs"},
	})

	// The completed call records the resolved model — the model that actually
	// ran after defaulting, not the planner's requested override — plus kind
	// and count. Media models burn no tokens, so these fields are the only
	// consumption record the usage fold can read (conv_a27d6008).
	if activities[0].Model != "bytedance/seedance-2.0/reference-to-video" || activities[0].MediaKind != "video" || activities[0].MediaCount != 1 {
		t.Fatalf("video activity media = %q/%q/%d, want bytedance/seedance-2.0/reference-to-video/video/1", activities[0].Model, activities[0].MediaKind, activities[0].MediaCount)
	}
	if activities[0].Provider != "fal" {
		t.Fatalf("video activity provider = %q, want fal", activities[0].Provider)
	}
	if activities[0].Call.Duration != "15" {
		t.Fatalf("video activity call duration = %q, want the planner's requested 15", activities[0].Call.Duration)
	}
	// A failed call produced no media, so it carries no media fields and drops
	// out of usage folds instead of counting phantom consumption.
	if activities[1].Model != "" || activities[1].MediaKind != "" || activities[1].MediaCount != 0 || activities[1].Provider != "" {
		t.Fatalf("failed activity recorded media %q/%q/%d/%q, want all empty", activities[1].Model, activities[1].MediaKind, activities[1].MediaCount, activities[1].Provider)
	}
}

func TestToolActivitiesRecordMediaProviderAttribution(t *testing.T) {
	// Video and audio generation are fal-only, whatever the image routing says.
	engine := newHarnessEngine(defaultAppConfig())
	activities := engine.toolActivities([]HarnessToolResult{
		{Name: "generate_video", Status: "completed", Result: ToolVideoResult{Model: "bytedance/seedance-2.0/reference-to-video", Count: 1}},
	}, []HarnessToolCall{{Name: "generate_video", Content: "x"}})
	if activities[0].Provider != "fal" {
		t.Fatalf("video activity provider = %q, want fal regardless of image routing", activities[0].Provider)
	}

	// Images route by config.Models.ImageProvider — the same field the tool
	// gateway reads — with an unset value meaning local Ollama.
	for _, tc := range []struct {
		imageProvider string
		want          string
	}{
		{"", "ollama"},
		{"fal", "fal"},
		{"openai-compatible", "openai-compatible"},
	} {
		config := defaultAppConfig()
		config.Models.ImageProvider = tc.imageProvider
		activities := newHarnessEngine(config).toolActivities([]HarnessToolResult{
			{Name: "generate_image", Status: "completed", Result: ToolImageResult{Model: "fal-ai/flux/schnell", Count: 1}},
		}, []HarnessToolCall{{Name: "generate_image", Content: "x"}})
		if activities[0].Provider != tc.want {
			t.Fatalf("image activity provider with ImageProvider=%q = %q, want %q", tc.imageProvider, activities[0].Provider, tc.want)
		}
	}
}

// persistedHarnessRun reads the harness run off the Nth (1-based) assistant
// turn's ProviderResponse in the conversation's sole record.
func persistedHarnessRun(t *testing.T, config AppConfig, assistantIndex int) map[string]any {
	t.Helper()
	turn := persistedAssistantTurn(t, config, assistantIndex)
	run, ok := turn.ProviderResponse["harnessRun"].(map[string]any)
	if !ok {
		t.Fatalf("assistant provider response missing harness run: %+v", turn.ProviderResponse)
	}
	return run
}

// persistedAssistantTurn returns the Nth (1-based) assistant turn in the
// conversation's sole record.
func persistedAssistantTurn(t *testing.T, config AppConfig, assistantIndex int) HistoryTurn {
	t.Helper()
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
	seen := 0
	for _, turn := range detail.Turns {
		if turn.Role != "assistant" {
			continue
		}
		seen++
		if seen == assistantIndex {
			return turn
		}
	}
	t.Fatalf("assistant turn #%d not found in %d turns", assistantIndex, len(detail.Turns))
	return HistoryTurn{}
}

// harnessStepsByKind returns every step of a kind, in order.
func harnessStepsByKind(t *testing.T, steps []any, kind string) []map[string]any {
	t.Helper()
	var matched []map[string]any
	for _, step := range steps {
		typed, ok := step.(map[string]any)
		if ok && typed["kind"] == kind {
			matched = append(matched, typed)
		}
	}
	return matched
}

func notFoundResponse() *http.Response {
	return &http.Response{
		StatusCode: http.StatusNotFound,
		Status:     "404 Not Found",
		Body:       io.NopCloser(strings.NewReader("not found")),
		Header:     http.Header{},
	}
}
