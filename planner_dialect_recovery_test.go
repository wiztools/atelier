package main

import (
	"context"
	"testing"

	"github.com/zalando/go-keyring"
)

// registryWithVideoGen builds a tool registry that includes generate_video, so
// dialect-recovery tests can assert real tool names are recovered.
func registryWithVideoGen(t *testing.T) HarnessToolRegistry {
	t.Helper()
	keyring.MockInit()
	if err := saveFalAPIKey("fal-test-key"); err != nil {
		t.Fatalf("saveFalAPIKey: %v", err)
	}
	t.Cleanup(func() { _ = clearFalAPIKey() })
	config := defaultAppConfig()
	config.Providers.Fal.VideoModel = "fal-ai/some/video-model"
	registry := defaultHarnessToolRegistry(context.Background(), config, nil)
	if _, ok := registry.Get("generate_video"); !ok {
		t.Fatal("test setup: generate_video should be registered")
	}
	return registry
}

// TestRecoverBraceShorthandDialect is the regression for conv_ae48b36d52278f08e88e9bee:
// gemma-4-31b (OpenRouter, native tools) returned zero tool_calls and wrote the
// call into content as a curly-brace shorthand with colon-separated kwargs. The
// call is correct — only the serialization is off — so it must be recovered.
func TestRecoverBraceShorthandDialect(t *testing.T) {
	registry := registryWithVideoGen(t)
	content := `call:generate_video{content: "The girl continues running towards the city, maintaining her pace and direction from the previous scene.",duration: 3}`

	calls := toolCodeDialectToToolCalls(content, registry)
	if len(calls) != 1 {
		t.Fatalf("recovered %d calls, want 1: %+v", len(calls), calls)
	}
	if calls[0].Name != "generate_video" {
		t.Errorf("name = %q, want generate_video", calls[0].Name)
	}
	if calls[0].Duration != "3" {
		t.Errorf("duration = %q, want 3", calls[0].Duration)
	}
	if calls[0].Content == "" {
		t.Errorf("content should be recovered, got empty")
	}
}

// TestRecoverClaudeXMLDialect is the regression for conv_6016bcaee42eaf191fdb62bd:
// the planner emitted the correct generate_video call in the Anthropic
// <function_calls><invoke name=…><parameter…> XML dialect instead of the plan
// JSON. The call must be recovered from that markup.
func TestRecoverClaudeXMLDialect(t *testing.T) {
	registry := registryWithVideoGen(t)
	content := `I'll extend the video.
<function_calls>
<invoke name="generate_video">
<parameter name="prompt">The girl continues running towards the city, maintaining her momentum and speed as she moves forward across the landscape</parameter>
<parameter name="duration">3</parameter>
<parameter name="attached_media">vid_e2009446</parameter>
</invoke>
</function_calls>`

	calls := toolCodeDialectToToolCalls(content, registry)
	if len(calls) != 1 {
		t.Fatalf("recovered %d calls, want 1: %+v", len(calls), calls)
	}
	if calls[0].Name != "generate_video" {
		t.Errorf("name = %q, want generate_video", calls[0].Name)
	}
	if calls[0].Duration != "3" {
		t.Errorf("duration = %q, want 3", calls[0].Duration)
	}
	if calls[0].Content == "" {
		t.Errorf("prompt should map to content, got empty")
	}
}

// TestRecoverDialectIgnoresUnknownToolNames guards against false recoveries: a
// brace or XML block naming a tool that is not registered must not produce a call.
func TestRecoverDialectIgnoresUnknownToolNames(t *testing.T) {
	registry := registryWithVideoGen(t)
	if calls := toolCodeDialectToToolCalls(`call:extend_video{content: "x", duration: 2}`, registry); len(calls) != 0 {
		t.Fatalf("unknown tool name must not be recovered, got %+v", calls)
	}
	if calls := toolCodeDialectToToolCalls(`<invoke name="teleport"><parameter name="x">1</parameter></invoke>`, registry); len(calls) != 0 {
		t.Fatalf("unknown XML tool name must not be recovered, got %+v", calls)
	}
}

// TestNativePlannerRecoversBraceShorthand exercises the native planner path end
// to end: an empty tool_calls response whose content carries the brace shorthand
// must yield a needsTools plan with the recovered call, not "no tools needed".
func TestNativePlannerRecoversBraceShorthand(t *testing.T) {
	registry := registryWithVideoGen(t)
	completion := ChatCompletionResult{
		Content: `call:generate_video{content: "girl keeps running", duration: 3}`,
	}
	plan, errs := parseNativePlannerResponse(completion, registry)
	if len(errs) != 0 {
		t.Fatalf("unexpected validation errors: %v", errs)
	}
	if !plan.NeedsTools || len(plan.ToolCalls) != 1 || plan.ToolCalls[0].Name != "generate_video" {
		t.Fatalf("plan did not recover the call: %+v", plan)
	}
}

// TestFormatPlannerRecoversClaudeXML exercises the format-schema path: schema-
// valid JSON is not required — a response that is the Claude XML dialect must be
// recovered into a plan instead of failing as unparseable.
func TestFormatPlannerRecoversClaudeXML(t *testing.T) {
	registry := registryWithVideoGen(t)
	content := `<function_calls><invoke name="generate_video"><parameter name="prompt">girl runs on</parameter><parameter name="duration">3</parameter></invoke></function_calls>`
	plan, errs := parseHarnessToolPlanWithRegistry(content, registry)
	if len(errs) != 0 {
		t.Fatalf("unexpected validation errors: %v", errs)
	}
	if !plan.NeedsTools || len(plan.ToolCalls) != 1 || plan.ToolCalls[0].Name != "generate_video" {
		t.Fatalf("format planner did not recover the XML call: %+v", plan)
	}
}
