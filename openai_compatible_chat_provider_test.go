package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
)

func newOpenAICompatibleChatTestProvider(t *testing.T, transport http.RoundTripper, apiKey string) openAICompatibleChatProvider {
	t.Helper()
	return newOpenAICompatibleChatProvider(newOpenAICompatibleClient(&http.Client{Transport: transport}, "http://chat.test", apiKey))
}

func TestOpenAICompatibleChatProviderStreamChatTranslatesSSE(t *testing.T) {
	var capturedPath, capturedAuth string
	var capturedBody map[string]any
	provider := newOpenAICompatibleChatTestProvider(t, roundTripFunc(func(req *http.Request) (*http.Response, error) {
		capturedPath = req.URL.Path
		capturedAuth = req.Header.Get("Authorization")
		data, err := io.ReadAll(req.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		capturedBody = map[string]any{}
		if err := json.Unmarshal(data, &capturedBody); err != nil {
			t.Fatalf("request body is not JSON: %v", err)
		}
		body := strings.Join([]string{
			`data: {"model":"gemma-4-e2b-it","choices":[{"delta":{"reasoning":"pondering"}}]}`,
			`data: {"model":"gemma-4-e2b-it","choices":[{"delta":{"content":"Hel"}}]}`,
			`data: {"model":"gemma-4-e2b-it","choices":[{"delta":{"content":"lo"},"finish_reason":"stop"}]}`,
			`data: {"model":"gemma-4-e2b-it","choices":[{"delta":{}}],"usage":{"prompt_tokens":11,"completion_tokens":7}}`,
			`data: [DONE]`,
		}, "\n")
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     http.Header{},
		}, nil
	}), "local-secret")

	events, err := provider.StreamChat(context.Background(), ChatRequest{Model: "gemma-4-e2b-it"})
	if err != nil {
		t.Fatalf("StreamChat returned error: %v", err)
	}

	var content, thinking string
	var done bool
	var tokens, promptTokens int
	for event := range events {
		content += event.ContentDelta
		thinking += event.Thinking
		if event.Usage != nil {
			tokens = event.Usage.CompletionTokens
			promptTokens = event.Usage.PromptTokens
		}
		if event.Done {
			done = true
		}
	}
	if content != "Hello" {
		t.Fatalf("content = %q, want Hello", content)
	}
	// LocalAI surfaces the model's reasoning channel; it must stream as Thinking.
	if thinking != "pondering" {
		t.Fatalf("thinking = %q, want pondering", thinking)
	}
	if !done {
		t.Fatal("expected a Done event on the [DONE] sentinel")
	}
	if tokens != 7 || promptTokens != 11 {
		t.Fatalf("usage = %d/%d, want 7 completion / 11 prompt", tokens, promptTokens)
	}
	if capturedPath != "/v1/chat/completions" {
		t.Errorf("request path = %q, want /v1/chat/completions", capturedPath)
	}
	if capturedAuth != "Bearer local-secret" {
		t.Errorf("Authorization = %q, want Bearer local-secret", capturedAuth)
	}
	if capturedBody["stream"] != true {
		t.Errorf("body stream = %v, want true", capturedBody["stream"])
	}
	// Without this opt-in, spec-strict servers (vLLM, LM Studio, LocalAI) omit
	// usage from every chunk and the streamed answer records zero tokens.
	if options, ok := capturedBody["stream_options"].(map[string]any); !ok || options["include_usage"] != true {
		t.Errorf("stream_options = %+v, want include_usage:true", capturedBody["stream_options"])
	}
	if capturedBody["model"] != "gemma-4-e2b-it" {
		t.Errorf("body model = %v, want gemma-4-e2b-it", capturedBody["model"])
	}
}

func TestOpenAICompatibleChatProviderStreamChatPropagatesInlineError(t *testing.T) {
	provider := newOpenAICompatibleChatTestProvider(t, roundTripFunc(func(req *http.Request) (*http.Response, error) {
		body := `data: {"error":{"message":"model is loading"}}` + "\n" + `data: [DONE]`
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     http.Header{},
		}, nil
	}), "")

	events, err := provider.StreamChat(context.Background(), ChatRequest{Model: "gemma-4-e2b-it"})
	if err != nil {
		t.Fatalf("StreamChat returned error: %v", err)
	}
	var gotErr error
	for event := range events {
		if event.Err != nil {
			gotErr = event.Err
		}
	}
	if gotErr == nil || gotErr.Error() != "model is loading" {
		t.Fatalf("gotErr = %v, want %q", gotErr, "model is loading")
	}
}

func TestOpenAICompatibleChatProviderStreamChatSurfacesHTTPError(t *testing.T) {
	provider := newOpenAICompatibleChatTestProvider(t, roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusBadGateway,
			Status:     "502 Bad Gateway",
			Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"backend offline"}}`)),
			Header:     http.Header{"Content-Type": []string{"application/json"}},
		}, nil
	}), "")

	if _, err := provider.StreamChat(context.Background(), ChatRequest{Model: "gemma-4-e2b-it"}); err == nil || !strings.Contains(err.Error(), "backend offline") {
		t.Fatalf("StreamChat error = %v, want it to surface the server message", err)
	}
}

func TestOpenAICompatibleChatProviderCompleteChat(t *testing.T) {
	var capturedAuth string
	var capturedBody map[string]any
	provider := newOpenAICompatibleChatTestProvider(t, roundTripFunc(func(req *http.Request) (*http.Response, error) {
		capturedAuth = req.Header.Get("Authorization")
		data, _ := io.ReadAll(req.Body)
		capturedBody = map[string]any{}
		if err := json.Unmarshal(data, &capturedBody); err != nil {
			t.Fatalf("request body is not JSON: %v", err)
		}
		return jsonResp(`{"model":"gemma-4-e2b-it","choices":[{"message":{"content":"{\"needsTools\":false}","reasoning":"thinking it through"},"finish_reason":"stop"}],"usage":{"prompt_tokens":9,"completion_tokens":4}}`), nil
	}), "")

	result, err := provider.CompleteChat(context.Background(), ChatRequest{
		Model:    "gemma-4-e2b-it",
		System:   "be brief",
		Messages: []ChatMessage{{Role: "user", Content: "hi", Images: []string{"data:image/png;base64,AAAA"}}},
		Format:   map[string]any{"type": "object", "properties": map[string]any{"needsTools": map[string]any{"type": "boolean"}}},
		Options:  map[string]any{"temperature": 0, "num_predict": 64, "num_ctx": 8192},
	})
	if err != nil {
		t.Fatalf("CompleteChat returned error: %v", err)
	}
	if result.Content != `{"needsTools":false}` || result.Thinking != "thinking it through" {
		t.Fatalf("result = %+v, want content + reasoning", result)
	}
	if result.Reason != "stop" || result.EvalTokens != 4 || result.PromptTokens != 9 {
		t.Fatalf("result bookkeeping = %+v", result)
	}
	if capturedAuth != "" {
		t.Errorf("Authorization = %q, want empty for keyless server", capturedAuth)
	}
	// The OpenAI wire format: system message, image as image_url part, and the
	// Format schema translated to response_format with num_ctx dropped.
	messages := capturedBody["messages"].([]any)
	if len(messages) != 2 {
		t.Fatalf("messages = %v, want system + user", messages)
	}
	if messages[0].(map[string]any)["role"] != "system" {
		t.Errorf("first message role = %v, want system", messages[0])
	}
	userEntry := messages[1].(map[string]any)
	contentParts, ok := userEntry["content"].([]any)
	if !ok || len(contentParts) != 2 {
		t.Fatalf("user content = %v, want text + image_url parts", userEntry["content"])
	}
	if part := contentParts[1].(map[string]any); part["type"] != "image_url" {
		t.Errorf("second part type = %v, want image_url", part)
	}
	responseFormat, ok := capturedBody["response_format"].(map[string]any)
	if !ok || responseFormat["type"] != "json_schema" {
		t.Fatalf("response_format = %v, want json_schema translation of Format", capturedBody["response_format"])
	}
	if _, present := capturedBody["num_ctx"]; present {
		t.Errorf("num_ctx must not leak to the wire, got %v", capturedBody["num_ctx"])
	}
	if capturedBody["max_tokens"] != float64(64) {
		t.Errorf("max_tokens = %v, want num_predict translated to 64", capturedBody["max_tokens"])
	}
	if capturedBody["temperature"] != float64(0) {
		t.Errorf("temperature = %v, want 0 carried through", capturedBody["temperature"])
	}
}

func TestOpenAICompatibleChatProviderCompleteChatErrors(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantErr string
	}{
		{"embedded error", `{"error":{"message":"model not found"}}`, "model not found"},
		{"no choices", `{"choices":[]}`, "no choices"},
		{"finish reason error", `{"choices":[{"message":{"content":"boom"},"finish_reason":"error"}]}`, "boom"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := newOpenAICompatibleChatTestProvider(t, roundTripFunc(func(req *http.Request) (*http.Response, error) {
				return jsonResp(tc.body), nil
			}), "")
			_, err := provider.CompleteChat(context.Background(), ChatRequest{Model: "m"})
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}

func TestOpenAICompatibleChatProviderListModels(t *testing.T) {
	provider := newOpenAICompatibleChatTestProvider(t, roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/v1/models" {
			t.Fatalf("unexpected path %s", req.URL.Path)
		}
		return jsonResp(`{"data":[{"id":"gemma-4-e2b-it"},{"id":"moondream2"}]}`), nil
	}), "")

	models, err := provider.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels returned error: %v", err)
	}
	if len(models) != 2 || models[0].Provider != "openai-compatible" || models[0].ID != "gemma-4-e2b-it" || models[0].DisplayName != "gemma-4-e2b-it" {
		t.Fatalf("models = %+v, want two openai-compatible ModelInfo entries", models)
	}
}

func TestOpenAICompatibleChatProviderSatisfiesInterface(t *testing.T) {
	var provider ChatProvider = newOpenAICompatibleChatProvider(openAICompatibleClient{})
	if provider.ID() != "openai-compatible" {
		t.Fatalf("ID() = %q, want openai-compatible", provider.ID())
	}
}

// TestProviderRegistryResolvesOpenAICompatibleWithoutKey pins the keyless
// posture: the local server's bearer key is optional, and Resolve must never
// fail on its absence or harnessProviderUnavailable kills every turn.
func TestProviderRegistryResolvesOpenAICompatibleWithoutKey(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	keyring.MockInit()
	t.Cleanup(func() { _ = clearOpenAICompatibleAPIKey() })

	config := defaultAppConfig()
	config.Storage = ConfigStorage{
		Root:      filepath.Join(home, ".atelier"),
		History:   filepath.Join(home, ".atelier", "history"),
		Artifacts: filepath.Join(home, ".atelier", "history"),
	}
	config.Providers.OpenAICompatible.BaseURL = "http://chat.test"
	if err := writeAppConfig(config); err != nil {
		t.Fatalf("writeAppConfig: %v", err)
	}

	app := NewApp()
	provider, err := newProviderRegistry(app).Resolve("openai-compatible", "")
	if err != nil {
		t.Fatalf("Resolve without key returned error: %v", err)
	}
	if provider.ID() != "openai-compatible" {
		t.Fatalf("provider.ID() = %q, want openai-compatible", provider.ID())
	}
}

func TestResolvedPrimaryModelAndProviderOpenAICompatible(t *testing.T) {
	app := NewApp()
	config := defaultAppConfig()
	config.Models.PrimaryProvider = "openai-compatible"
	config.Providers.OpenAICompatible.Primary = "gemma-4-e2b-it"

	model, provider := app.resolvedPrimaryModelAndProvider(config)
	if model != "gemma-4-e2b-it" || provider != "openai-compatible" {
		t.Fatalf("resolved = (%q, %q), want (gemma-4-e2b-it, openai-compatible)", model, provider)
	}

	config.Models.PrimaryProvider = "some-unrecognized-provider"
	model, provider = app.resolvedPrimaryModelAndProvider(config)
	if provider != "ollama" || model != config.Providers.Ollama.Models.Primary {
		t.Fatalf("unknown provider resolved = (%q, %q), want ollama default", model, provider)
	}
}

func TestResolveHarnessTargetOpenAICompatible(t *testing.T) {
	engine := newHarnessEngine(AppConfig{
		Models: ConfigModels{HarnessProvider: "openai-compatible"},
		Providers: ConfigProviders{
			OpenAICompatible: ConfigOpenAICompatible{Harness: "gemma-4-e2b-it"},
			Ollama:           ConfigOllama{Models: ConfigOllamaModels{Harness: "local-model"}},
		},
	})
	got := engine.resolveHarnessTarget("primary-model", "ollama")
	if got.model != "gemma-4-e2b-it" || got.provider != "openai-compatible" {
		t.Fatalf("resolveHarnessTarget = (%q, %q), want (gemma-4-e2b-it, openai-compatible)", got.model, got.provider)
	}

	// Unset harness model follows the primary target as one unit.
	engine = newHarnessEngine(AppConfig{Models: ConfigModels{HarnessProvider: "openai-compatible"}})
	got = engine.resolveHarnessTarget("primary-model", "openai-compatible")
	if got.model != "primary-model" || got.provider != "openai-compatible" {
		t.Fatalf("unset harness = (%q, %q), want primary target", got.model, got.provider)
	}
}

func TestMergeAppConfigHarnessProviderAcceptsOpenAICompatible(t *testing.T) {
	config := defaultAppConfig()
	config.Models.HarnessProvider = " openai-compatible "
	merged := mergeAppConfig(config)
	if merged.Models.HarnessProvider != "openai-compatible" {
		t.Fatalf("harnessProvider = %q, want openai-compatible preserved", merged.Models.HarnessProvider)
	}
}

func TestRespondsWithHarnessModelUsesConfiguredImageProvider(t *testing.T) {
	config := defaultAppConfig()
	config.Models.ImageProvider = "openai-compatible"
	config.Providers.OpenAICompatible.Model = "flux.2-klein-4b"
	engine := newHarnessEngine(config)

	if !engine.respondsWithHarnessModel("text", "flux.2-klein-4b") {
		t.Fatal("primary == openai-compatible image model should respond with the harness model")
	}
	if engine.respondsWithHarnessModel("text", "gemma-4-e2b-it") {
		t.Fatal("a chat model must not trigger the image-model fallback")
	}
}

// TestHarnessRunChatStreamViaOpenAICompatible runs a full text turn with both
// the primary and the harness roles on the local OpenAI-compatible server:
// triage and the final response are served by the same mock host, with the
// planner bypassed (triage answers needsTools:false).
func TestHarnessRunChatStreamViaOpenAICompatible(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	keyring.MockInit()

	config := defaultAppConfig()
	config.Storage = ConfigStorage{
		Root:      filepath.Join(home, ".atelier"),
		History:   filepath.Join(home, ".atelier", "history"),
		Artifacts: filepath.Join(home, ".atelier", "history"),
	}
	config.Models.PrimaryProvider = "openai-compatible"
	config.Models.HarnessProvider = "openai-compatible"
	config.Providers.Ollama.BaseURL = "http://ollama.test" // unused this turn
	config.Providers.OpenAICompatible.BaseURL = "http://chat.test"
	config.Providers.OpenAICompatible.Primary = "gemma-4-e2b-it"
	config.Providers.OpenAICompatible.Harness = "gemma-4-e2b-it"
	if err := writeAppConfig(config); err != nil {
		t.Fatalf("writeAppConfig: %v", err)
	}

	app := NewApp()
	nonStreamCount := 0
	app.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host != "chat.test" {
			t.Fatalf("unexpected request to %s — the whole turn should stay on the local server", req.URL.Host)
		}
		if req.URL.Path != "/v1/chat/completions" {
			t.Fatalf("unexpected path %s", req.URL.Path)
		}
		data, _ := io.ReadAll(req.Body)
		body := map[string]any{}
		if err := json.Unmarshal(data, &body); err != nil {
			t.Fatalf("request body is not JSON: %v", err)
		}
		if body["stream"] == false {
			nonStreamCount++
			if nonStreamCount == 1 {
				return jsonResp(`{"model":"gemma-4-e2b-it","choices":[{"message":{"content":"{\"needsTools\":false,\"responseMode\":\"text\",\"reason\":\"plain question\"}"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":6}}`), nil
			}
			// Skill selection and title generation (both non-stream, both may run).
			return jsonResp(`{"model":"gemma-4-e2b-it","choices":[{"message":{"content":"local chat"},"finish_reason":"stop"}]}`), nil
		}
		sse := strings.Join([]string{
			`data: {"model":"gemma-4-e2b-it","choices":[{"delta":{"content":"Hello from the local server."}}]}`,
			`data: {"model":"gemma-4-e2b-it","choices":[{"delta":{"reasoning":"warming up"}}],"usage":{"prompt_tokens":21,"completion_tokens":9}}`,
			`data: [DONE]`,
		}, "\n")
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Body:       io.NopCloser(strings.NewReader(sse)),
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		}, nil
	})

	app.runChatStream(context.Background(), "request-openai-chat", ChatRequest{
		BaseURL:  "http://ollama.test",
		Provider: "openai-compatible",
		Model:    "gemma-4-e2b-it",
		Messages: []ChatMessage{
			{Role: "user", Content: "Say hello"},
		},
	})

	conversations, err := listConversations(config.Storage)
	if err != nil {
		t.Fatalf("listConversations: %v", err)
	}
	if len(conversations) != 1 {
		t.Fatalf("conversation count = %d, want 1", len(conversations))
	}
	detail, err := getConversation(config.Storage, conversations[0].ID)
	if err != nil {
		t.Fatalf("getConversation: %v", err)
	}
	if len(detail.Turns) != 2 {
		t.Fatalf("turn count = %d, want user + assistant", len(detail.Turns))
	}
	assistant := detail.Turns[1]
	if assistant.Provider != "openai-compatible" {
		t.Errorf("assistant provider = %q, want openai-compatible", assistant.Provider)
	}
	if assistant.Model != "gemma-4-e2b-it" {
		t.Errorf("assistant model = %q, want gemma-4-e2b-it", assistant.Model)
	}
	if !strings.Contains(assistantTextForTest(assistant), "Hello from the local server.") {
		t.Errorf("assistant content = %q, want the streamed text", assistantTextForTest(assistant))
	}
	if nonStreamCount < 1 {
		t.Errorf("non-stream calls = %d, want at least the triage call", nonStreamCount)
	}
}

func assistantTextForTest(turn HistoryTurn) string {
	var builder strings.Builder
	for _, entry := range turn.Content {
		if entry.Type == "text" {
			builder.WriteString(entry.Text)
		}
	}
	return builder.String()
}

// TestImageProviderIgnoresChatModelWhenBothOpenAICompatible pins the per-turn
// image-model override guard: with primary chat AND images both on the local
// server, the chat model id must NOT be forwarded to /v1/images/generations —
// the configured image model governs.
func TestImageProviderIgnoresChatModelWhenBothOpenAICompatible(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	keyring.MockInit()

	config := defaultAppConfig()
	config.Storage = ConfigStorage{
		Root:      filepath.Join(home, ".atelier"),
		History:   filepath.Join(home, ".atelier", "history"),
		Artifacts: filepath.Join(home, ".atelier", "history"),
	}
	config.Models.PrimaryProvider = "openai-compatible"
	config.Models.HarnessProvider = "openai-compatible"
	config.Models.ImageProvider = "openai-compatible"
	config.Providers.Ollama.BaseURL = "http://ollama.test"
	config.Providers.OpenAICompatible.BaseURL = "http://chat.test"
	config.Providers.OpenAICompatible.Primary = "gemma-4-e2b-it"
	config.Providers.OpenAICompatible.Harness = "gemma-4-e2b-it"
	config.Providers.OpenAICompatible.Model = "flux.2-klein-4b"
	if err := writeAppConfig(config); err != nil {
		t.Fatalf("writeAppConfig: %v", err)
	}

	app := NewApp()
	imageCalls := 0
	var imageBody map[string]any
	nonStreamCount := 0
	prepCalls := 0
	chatCompletion := func(content string) *http.Response {
		return jsonResp(`{"model":"gemma-4-e2b-it","choices":[{"message":{"content":` + strconv.Quote(content) + `},"finish_reason":"stop"}]}`)
	}
	app.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host != "chat.test" {
			t.Fatalf("unexpected request to %s", req.URL.Host)
		}
		switch req.URL.Path {
		case "/v1/images/generations":
			imageCalls++
			data, _ := io.ReadAll(req.Body)
			imageBody = map[string]any{}
			if err := json.Unmarshal(data, &imageBody); err != nil {
				t.Fatalf("image request body is not JSON: %v", err)
			}
			return jsonResp(`{"data":[{"b64_json":"` + tinyPNG + `"}]}`), nil
		case "/v1/chat/completions":
			data, _ := io.ReadAll(req.Body)
			body := map[string]any{}
			if err := json.Unmarshal(data, &body); err != nil {
				t.Fatalf("chat request body is not JSON: %v", err)
			}
			if body["stream"] == false {
				nonStreamCount++
				if nonStreamCount == 1 {
					return chatCompletion(`{"needsTools":true,"responseMode":"image","toolTask":"Generate an image.","reason":"The user asked for an image."}`), nil
				}
				prepCalls++
				if prepCalls > 1 {
					return chatCompletion(`{"brief":"Done.","needsTools":false,"reason":"done","toolCalls":[]}`), nil
				}
				return chatCompletion(`{"brief":"Generate the image.","needsTools":true,"reason":"image","toolCalls":[{"name":"generate_image","content":"a small house"}]}`), nil
			}
			sse := strings.Join([]string{
				`data: {"model":"gemma-4-e2b-it","choices":[{"delta":{"content":"Here is the house."}}]}`,
				`data: [DONE]`,
			}, "\n")
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Body:       io.NopCloser(strings.NewReader(sse)),
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			}, nil
		default:
			t.Fatalf("unexpected path %s", req.URL.Path)
			return nil, nil
		}
	})

	app.runChatStream(context.Background(), "request-openai-image-guard", ChatRequest{
		BaseURL:  "http://ollama.test",
		Provider: "openai-compatible",
		Model:    "gemma-4-e2b-it",
		Messages: []ChatMessage{
			{Role: "user", Content: "Create an image of a small house"},
		},
	})

	if imageCalls != 1 {
		t.Fatalf("image calls = %d, want 1", imageCalls)
	}
	if imageBody["model"] != "flux.2-klein-4b" {
		t.Fatalf("image model = %v, want the configured flux.2-klein-4b (not the chat model gemma-4-e2b-it)", imageBody["model"])
	}
	if imageBody["prompt"] != "a small house" {
		t.Errorf("image prompt = %v, want the planner's tool content", imageBody["prompt"])
	}
}
