package main

import (
	"context"
	"testing"
)

// TestOpenRouterHarnessAvoidsNativeTools guards the planner routing decision:
// native tool-calling is not wired through the OpenRouter client (openRouterChatBody
// drops req.Tools and CompleteChat ignores tool_calls), so claiming native support
// would send neither tools nor a response_format schema — a plain request the model
// free-forms (conv_ae48b36d). supportsNativeTools must return false for OpenRouter so
// the planner uses the strict json_schema format path instead.
func TestOpenRouterHarnessAvoidsNativeTools(t *testing.T) {
	app := NewApp()
	h := newHarnessEngine(defaultAppConfig(), app)
	if h.supportsNativeTools(context.Background(), "", harnessTarget{model: "google/gemma-4-31b-it", provider: "openrouter"}) {
		t.Fatal("OpenRouter must not use native tools — the client cannot send tool specs or parse tool_calls")
	}
}

// TestOpenRouterBodyRequiresParameters asserts every OpenRouter request pins
// provider routing to endpoints that honor the parameters sent (json_schema
// structured outputs), so a request is not silently routed to an endpoint that
// drops response_format and free-forms the answer.
func TestOpenRouterBodyRequiresParameters(t *testing.T) {
	body := openRouterChatBody(ChatRequest{
		Model:    "google/gemma-4-31b-it",
		Messages: []ChatMessage{{Role: "user", Content: "hi"}},
	}, false)
	provider, ok := body["provider"].(map[string]any)
	if !ok {
		t.Fatalf("body has no provider routing block: %v", body["provider"])
	}
	if provider["require_parameters"] != true {
		t.Fatalf("provider.require_parameters = %v, want true", provider["require_parameters"])
	}
}
