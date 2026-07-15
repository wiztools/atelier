package main

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestOpenRouterClientListModels(t *testing.T) {
	client := newOpenRouterClient(&http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.Path != "/api/v1/models" {
				t.Fatalf("path = %q, want /api/v1/models", req.URL.Path)
			}
			if got := req.Header.Get("Authorization"); got != "Bearer sk-or-test" {
				t.Fatalf("Authorization header = %q, want Bearer sk-or-test", got)
			}
			body := `{"data":[{"id":"anthropic/claude-3.5-sonnet","name":"Claude 3.5 Sonnet","context_length":200000}]}`
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Body:       io.NopCloser(strings.NewReader(body)),
				Header:     http.Header{},
			}, nil
		}),
	}, "sk-or-test")

	models, err := client.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels returned error: %v", err)
	}
	if len(models) != 1 || models[0].ID != "anthropic/claude-3.5-sonnet" || models[0].ContextLen != 200000 {
		t.Fatalf("models = %+v, want one anthropic/claude-3.5-sonnet entry with context 200000", models)
	}
}

func TestOpenRouterClientCompleteChat(t *testing.T) {
	client := newOpenRouterClient(&http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			body := `{"model":"anthropic/claude-3.5-sonnet","choices":[{"message":{"content":"Hello"},"finish_reason":"stop"}],"usage":{"completion_tokens":4}}`
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Body:       io.NopCloser(strings.NewReader(body)),
				Header:     http.Header{},
			}, nil
		}),
	}, "sk-or-test")

	result, err := client.CompleteChat(context.Background(), ChatRequest{Model: "anthropic/claude-3.5-sonnet"})
	if err != nil {
		t.Fatalf("CompleteChat returned error: %v", err)
	}
	if result.Content != "Hello" || result.Reason != "stop" || result.EvalTokens != 4 {
		t.Fatalf("result = %+v, want Content=Hello Reason=stop EvalTokens=4", result)
	}
}

func TestOpenRouterClientMissingAPIKey(t *testing.T) {
	client := newOpenRouterClient(&http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			t.Fatal("request should not be sent without an API key")
			return nil, nil
		}),
	}, "")

	if _, err := client.CompleteChat(context.Background(), ChatRequest{Model: "anthropic/claude-3.5-sonnet"}); err == nil {
		t.Fatal("expected an error when the API key is empty")
	}
}

func TestOpenRouterClientMapsUnauthorized(t *testing.T) {
	client := newOpenRouterClient(&http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusUnauthorized,
				Status:     "401 Unauthorized",
				Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"invalid key"}}`)),
				Header:     http.Header{},
			}, nil
		}),
	}, "sk-or-bad")

	_, err := client.CompleteChat(context.Background(), ChatRequest{Model: "anthropic/claude-3.5-sonnet"})
	if err == nil || !strings.Contains(err.Error(), "authentication failed") {
		t.Fatalf("err = %v, want an authentication-failed error", err)
	}
}

func TestOpenRouterClientMapsRateLimit(t *testing.T) {
	client := newOpenRouterClient(&http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusTooManyRequests,
				Status:     "429 Too Many Requests",
				Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"rate limit exceeded"}}`)),
				Header:     http.Header{},
			}, nil
		}),
	}, "sk-or-test")

	_, err := client.CompleteChat(context.Background(), ChatRequest{Model: "anthropic/claude-3.5-sonnet"})
	if err == nil || !strings.Contains(err.Error(), "rate limited") {
		t.Fatalf("err = %v, want a rate-limited error", err)
	}
}

func TestStrictJSONSchemaStripsUnsupportedKeywords(t *testing.T) {
	schema := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"toolCalls"},
		"properties": map[string]any{
			"toolCalls": map[string]any{
				"type":     "array",
				"maxItems": 3,
				"items": map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"required":             []string{"name"},
					"properties": map[string]any{
						"name":      map[string]any{"type": "string", "enum": []string{"read_file"}},
						"timeoutMs": map[string]any{"type": "integer", "minimum": 1},
					},
				},
			},
		},
	}

	got, ok := strictJSONSchema(schema).(map[string]any)
	if !ok {
		t.Fatalf("strictJSONSchema returned %T, want map[string]any", strictJSONSchema(schema))
	}

	toolCalls := got["properties"].(map[string]any)["toolCalls"].(map[string]any)
	if _, present := toolCalls["maxItems"]; present {
		t.Errorf("maxItems survived the strip pass: %+v", toolCalls)
	}
	items := toolCalls["items"].(map[string]any)
	timeout := items["properties"].(map[string]any)["timeoutMs"].(map[string]any)
	if _, present := timeout["minimum"]; present {
		t.Errorf("minimum survived the strip pass in a nested item: %+v", timeout)
	}
}

func TestStrictJSONSchemaPromotesAndWidensOptionalProperties(t *testing.T) {
	schema := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"name"},
		"properties": map[string]any{
			"name": map[string]any{"type": "string", "enum": []string{"read_file", "write_file"}},
			"path": map[string]any{"type": "string"},
		},
	}

	got := strictJSONSchema(schema).(map[string]any)

	required, _ := got["required"].([]string)
	if len(required) != 2 {
		t.Fatalf("required = %v, want both name and path promoted", required)
	}

	// name was already required: it must NOT be widened to nullable, and its
	// enum must survive untouched.
	name := got["properties"].(map[string]any)["name"].(map[string]any)
	if name["type"] != "string" {
		t.Errorf("required property widened to %v, want plain string", name["type"])
	}
	if enum, ok := name["enum"].([]string); !ok || len(enum) != 2 {
		t.Errorf("enum mangled: %+v", name["enum"])
	}

	// path was optional: it must become nullable so strict mode can require it.
	path := got["properties"].(map[string]any)["path"].(map[string]any)
	union, ok := path["type"].([]string)
	if !ok || len(union) != 2 || union[0] != "string" || union[1] != "null" {
		t.Errorf("optional property type = %v, want [string null]", path["type"])
	}
}

func TestStrictJSONSchemaLeavesAlreadyStrictSchemaIntact(t *testing.T) {
	// triageResponseSchema is already strict-compatible; it must round-trip
	// with no nullable widening, since every property is already required.
	got := strictJSONSchema(triageResponseSchema()).(map[string]any)

	for name, raw := range got["properties"].(map[string]any) {
		prop := raw.(map[string]any)
		if _, widened := prop["type"].([]string); widened {
			t.Errorf("property %q widened to a nullable union, want plain type", name)
		}
	}
	if len(got["required"].([]string)) != 4 {
		t.Errorf("required = %v, want the original four", got["required"])
	}
}

func TestOpenRouterChatBodyEmitsStrictResponseFormat(t *testing.T) {
	body := openRouterChatBody(ChatRequest{
		Model:  "anthropic/claude-3.5-sonnet",
		Format: triageResponseSchema(),
	}, false)

	format, ok := body["response_format"].(map[string]any)
	if !ok {
		t.Fatalf("response_format = %+v, want a map", body["response_format"])
	}
	if format["type"] != "json_schema" {
		t.Errorf("response_format.type = %v, want json_schema", format["type"])
	}
	schema, ok := format["json_schema"].(map[string]any)
	if !ok {
		t.Fatalf("json_schema = %+v, want a map", format["json_schema"])
	}
	if schema["strict"] != true {
		t.Errorf("strict = %v, want true", schema["strict"])
	}
	if name, _ := schema["name"].(string); name == "" {
		t.Error("json_schema.name is empty; OpenRouter requires a name")
	}
	if _, ok := schema["schema"].(map[string]any); !ok {
		t.Errorf("json_schema.schema = %+v, want the derived schema", schema["schema"])
	}
}

func TestOpenRouterChatBodyDerivesStrictVariantOfPlanSchema(t *testing.T) {
	// The planner schema carries maxItems and partial required, both of which
	// strict mode rejects. The body must send the derived variant, not the raw
	// schema the harness authored for Ollama.
	raw := harnessToolPlanSchema(defaultHarnessToolRegistry(defaultAppConfig()))
	body := openRouterChatBody(ChatRequest{Model: "m", Format: raw}, false)

	sent := body["response_format"].(map[string]any)["json_schema"].(map[string]any)["schema"].(map[string]any)
	toolCalls := sent["properties"].(map[string]any)["toolCalls"].(map[string]any)
	if _, present := toolCalls["maxItems"]; present {
		t.Errorf("maxItems reached OpenRouter: %+v", toolCalls)
	}

	// The harness's own schema must be untouched — Ollama still needs maxItems.
	if _, present := raw["properties"].(map[string]any)["toolCalls"].(map[string]any)["maxItems"]; !present {
		t.Error("strictJSONSchema mutated the caller's schema; Ollama's path must stay byte-identical")
	}
}

func TestOpenRouterChatBodyOmitsResponseFormatWithoutSchema(t *testing.T) {
	body := openRouterChatBody(ChatRequest{Model: "m"}, false)
	if _, present := body["response_format"]; present {
		t.Errorf("response_format sent for a schema-less request: %+v", body)
	}
}

func TestOpenRouterChatBodyMapsPortableSamplingOptions(t *testing.T) {
	body := openRouterChatBody(ChatRequest{
		Model: "m",
		Options: map[string]any{
			"temperature": 0,
			"num_predict": 4096,
			"num_ctx":     8192,
		},
	}, false)

	if body["temperature"] != 0 {
		t.Errorf("temperature = %v, want 0 — the harness relies on deterministic JSON", body["temperature"])
	}
	if body["max_tokens"] != 4096 {
		t.Errorf("max_tokens = %v, want num_predict mapped to 4096", body["max_tokens"])
	}
	if _, present := body["num_ctx"]; present {
		t.Errorf("num_ctx leaked to OpenRouter: %+v", body)
	}
	if _, present := body["num_predict"]; present {
		t.Errorf("num_predict leaked to OpenRouter unmapped: %+v", body)
	}
}

// TestOpenRouterChatBodyRewritesPlannerToolEvidence reproduces the 400 seen on
// the first OpenRouter planner turn that needed tools:
//
//	tool message has no preceding assistant tool_calls
//
// The format-schema planner emits an assistant message with no tool_calls
// followed by role:"tool" observations. Ollama accepts that; the OpenAI wire
// format does not.
func TestOpenRouterChatBodyRewritesPlannerToolEvidence(t *testing.T) {
	engine := newHarnessEngine(defaultAppConfig())
	results := []HarnessToolResult{
		{Name: "list_files", Status: "completed", Result: map[string]any{"files": []string{"a.go"}}},
	}

	// Exactly what prepareChatTurnLoop appends on the format-schema path.
	messages := []ChatMessage{{Role: "user", Content: "List all files in my workspace."}}
	messages = append(messages, engine.plannerAssistantMessage(false, ChatCompletionResult{Content: `{"brief":"list the files"}`}))
	messages = append(messages, toolResultMessages(results)...)

	body := openRouterChatBody(ChatRequest{Model: "anthropic/claude-3.5-sonnet", Messages: messages}, false)

	sent := body["messages"].([]ChatMessage)
	for i, msg := range sent {
		if msg.Role == "tool" {
			t.Fatalf("message %d kept role %q with no preceding assistant tool_calls — OpenRouter rejects this with 400", i, msg.Role)
		}
	}
	// The observation itself must survive the rewrite, or the planner re-plans blind.
	last := sent[len(sent)-1]
	if last.Role != "user" || !strings.Contains(last.Content, "list_files") {
		t.Fatalf("tool observation lost in rewrite: %+v", last)
	}
}

func TestOpenRouterChatBodyKeepsToolMessagesBackedByToolCalls(t *testing.T) {
	// When the assistant genuinely made native tool calls, role:"tool" is the
	// correct shape and must survive untouched.
	body := openRouterChatBody(ChatRequest{
		Model: "m",
		Messages: []ChatMessage{
			{Role: "assistant", ToolCalls: []ToolCall{{Type: "function", Function: ToolFunction{Name: "list_files"}}}},
			{Role: "tool", Content: `{"name":"list_files","status":"completed"}`},
		},
	}, false)

	sent := body["messages"].([]ChatMessage)
	if sent[1].Role != "tool" {
		t.Fatalf("tool message backed by tool_calls was rewritten to %q; native tool-calling would break", sent[1].Role)
	}
}

func TestOpenRouterChatBodyMergesConsecutiveToolObservations(t *testing.T) {
	// Converting each observation to its own user message would emit
	// consecutive user roles, which some providers also reject.
	body := openRouterChatBody(ChatRequest{
		Model: "m",
		Messages: []ChatMessage{
			{Role: "assistant", Content: "{}"},
			{Role: "tool", Content: `{"name":"list_files"}`},
			{Role: "tool", Content: `{"name":"read_file"}`},
		},
	}, false)

	sent := body["messages"].([]ChatMessage)
	if len(sent) != 2 {
		t.Fatalf("got %d messages, want the two observations merged into one user message: %+v", len(sent), sent)
	}
	if !strings.Contains(sent[1].Content, "list_files") || !strings.Contains(sent[1].Content, "read_file") {
		t.Fatalf("merged message dropped an observation: %q", sent[1].Content)
	}
}
