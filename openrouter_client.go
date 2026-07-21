package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
)

const openRouterBaseURL = "https://openrouter.ai/api/v1"

type OpenRouterClient struct {
	httpClient *http.Client
	apiKey     string
}

func newOpenRouterClient(httpClient *http.Client, apiKey string) OpenRouterClient {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return OpenRouterClient{httpClient: httpClient, apiKey: strings.TrimSpace(apiKey)}
}

type openRouterModel struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Context int    `json:"context_length"`
}

type openRouterModelsResponse struct {
	Data []openRouterModel `json:"data"`
}

func (client OpenRouterClient) ListModels(ctx context.Context) ([]ModelInfo, error) {
	resp, err := client.do(ctx, http.MethodGet, "/models", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var payload openRouterModelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}

	models := make([]ModelInfo, 0, len(payload.Data))
	for _, model := range payload.Data {
		models = append(models, ModelInfo{
			Provider:    "openrouter",
			ID:          model.ID,
			DisplayName: model.Name,
			ContextLen:  model.Context,
		})
	}
	return models, nil
}

// strictJSONSchemaRejectedKeywords are JSON Schema keywords that OpenAI-style
// strict structured outputs reject. Ollama accepts them, so they are stripped
// only from the derived variant sent to OpenRouter; the schema the harness
// authors (and Ollama receives) keeps them.
var strictJSONSchemaRejectedKeywords = map[string]bool{
	"maxItems":    true,
	"minItems":    true,
	"uniqueItems": true,
	"minimum":     true,
	"maximum":     true,
	"multipleOf":  true,
	"pattern":     true,
	"minLength":   true,
	"maxLength":   true,
}

// strictJSONSchema rewrites an Ollama-shaped JSON Schema into one OpenAI's
// strict mode accepts: unsupported keywords are stripped, every property is
// promoted into "required", and properties that were previously optional are
// widened to a nullable union so requiring them stays semantically honest.
//
// Go's json.Unmarshal maps a JSON null to the field's zero value, and every
// tool's Validate func already treats an empty value as absent, so the widened
// schema decodes into the same plan shape the Ollama path produces.
func strictJSONSchema(schema any) any {
	node, ok := schema.(map[string]any)
	if !ok {
		return schema
	}

	out := make(map[string]any, len(node))
	for key, value := range node {
		if strictJSONSchemaRejectedKeywords[key] {
			continue
		}
		out[key] = value
	}

	if items, present := out["items"]; present {
		out["items"] = strictJSONSchema(items)
	}
	for _, branch := range []string{"anyOf", "oneOf", "allOf"} {
		list, present := out[branch].([]any)
		if !present {
			continue
		}
		rewritten := make([]any, len(list))
		for i, item := range list {
			rewritten[i] = strictJSONSchema(item)
		}
		out[branch] = rewritten
	}

	properties, present := out["properties"].(map[string]any)
	if !present {
		return out
	}

	alreadyRequired := requiredPropertySet(out["required"])
	rewritten := make(map[string]any, len(properties))
	names := make([]string, 0, len(properties))
	for name, raw := range properties {
		child := strictJSONSchema(raw)
		if !alreadyRequired[name] {
			child = widenToNullable(child)
		}
		rewritten[name] = child
		names = append(names, name)
	}
	sort.Strings(names)

	out["properties"] = rewritten
	out["required"] = names
	return out
}

// requiredPropertySet reads a schema's "required" list, which the harness
// authors as []string but which may arrive as []any after a JSON round-trip.
func requiredPropertySet(raw any) map[string]bool {
	set := map[string]bool{}
	switch list := raw.(type) {
	case []string:
		for _, name := range list {
			set[name] = true
		}
	case []any:
		for _, name := range list {
			if text, ok := name.(string); ok {
				set[text] = true
			}
		}
	}
	return set
}

// widenToNullable turns {"type": "string"} into {"type": ["string", "null"]}.
// Schemas without a scalar "type" (or already a union) are returned unchanged.
func widenToNullable(schema any) any {
	node, ok := schema.(map[string]any)
	if !ok {
		return schema
	}
	scalar, ok := node["type"].(string)
	if !ok || scalar == "null" {
		return node
	}
	node["type"] = []string{scalar, "null"}
	return node
}

// openRouterStructuredOutputName labels the derived schema. OpenRouter requires
// a name; it is otherwise unused, so one constant serves every harness call.
const openRouterStructuredOutputName = "atelier_structured_output"

// openRouterMessages renders the harness's canonical message list into shapes
// the OpenAI wire format accepts.
//
// A role:"tool" message is only legal there when the preceding assistant
// message carries tool_calls. The format-schema planner emits neither — it gets
// its plan from a response schema, not native tool-calling — so its tool
// observations arrive unbacked. Ollama accepts them; OpenRouter rejects the
// request with "tool message has no preceding assistant tool_calls".
//
// Render those observations as a user message instead: the same container
// toolEvidenceUserMessage already uses for the final model, for the same
// reason. Without native tool-calling they are evidence, not a protocol reply.
// Consecutive observations merge into one message, since consecutive same-role
// messages are their own portability hazard. Tool messages that *are* backed by
// tool_calls pass through untouched.
func openRouterMessages(messages []ChatMessage) []ChatMessage {
	rendered := make([]ChatMessage, 0, len(messages))
	toolCallsOpen := false // the preceding assistant message carried tool_calls
	mergeInto := -1        // index of the user message consecutive observations join

	for _, msg := range messages {
		if msg.Role == "tool" {
			if toolCallsOpen {
				rendered = append(rendered, msg)
				continue
			}
			if mergeInto >= 0 {
				rendered[mergeInto].Content += "\n\n" + msg.Content
				continue
			}
			rendered = append(rendered, ChatMessage{Role: "user", Content: toolObservationsPrefix + msg.Content})
			mergeInto = len(rendered) - 1
			continue
		}
		mergeInto = -1
		toolCallsOpen = msg.Role == "assistant" && len(msg.ToolCalls) > 0
		rendered = append(rendered, msg)
	}
	return rendered
}

// openRouterImageURL normalizes a single ChatMessage.Images entry into the
// data URL the OpenAI image_url content part requires. The frontend sends bare
// base64 and data URLs interchangeably (the prior-turn re-injection path in
// latestAttachedImageForTurn also produces data URLs), so both are accepted:
//
//   - a data: URL is returned as-is after validating its payload decodes;
//   - bare base64 is wrapped as data:<sniffed-mime>;base64,<original> using
//     http.DetectContentType on the decoded bytes;
//   - anything else — an /atelier-artifact/ history URL, an http(s) URL, a file
//     path, garbage — returns "" so a single malformed entry cannot poison the
//     whole request. This mirrors sanitizeOllamaImages' fail-closed posture.
//
// This is deliberately distinct from normalizeOllamaImage, which strips the
// data: wrapper to give Ollama its bare-base64 wire shape; OpenRouter needs the
// opposite — a fully-formed data URL inside an image_url content part.
func openRouterImageURL(image string) string {
	image = strings.TrimSpace(image)
	if image == "" {
		return ""
	}
	if strings.HasPrefix(image, "data:") {
		payload := image
		if comma := strings.Index(image, ","); comma >= 0 {
			payload = image[comma+1:]
		}
		if _, err := base64.StdEncoding.DecodeString(payload); err != nil {
			return ""
		}
		return image
	}
	decoded, err := base64.StdEncoding.DecodeString(image)
	if err != nil {
		return ""
	}
	return "data:" + http.DetectContentType(decoded) + ";base64," + image
}

// openRouterWireMessages renders the harness's canonical message list into the
// OpenAI chat-completions wire shape. It is the sole place a ChatMessage's
// Images field becomes OpenAI content parts: a message carrying images is
// serialized as content:[{type:"text",...},{type:"image_url",...},...],
// matching OpenRouter's documented multimodal format.
//
// Messages without images stay byte-identical to before — {role, content} with
// string content — so the tool-evidence rewrite (openRouterMessages), native
// tool-calling history (assistant messages carrying tool_calls), and the
// strict-schema planner path all serialize exactly as they did. Only the
// image-attachment case changes: today it is silently dropped (OpenRouter does
// not recognize a top-level "images" key), after this it is a real content part
// the model can see.
//
// Malformed image entries (openRouterImageURL returns "") are dropped; if every
// image on a message drops, the message reverts to plain string content so we
// never emit an empty content array.
func openRouterWireMessages(messages []ChatMessage) []map[string]any {
	rendered := make([]map[string]any, 0, len(messages))
	for _, msg := range messages {
		imageURLs := make([]string, 0, len(msg.Images))
		for _, image := range msg.Images {
			if url := openRouterImageURL(image); url != "" {
				imageURLs = append(imageURLs, url)
			}
		}

		if len(imageURLs) == 0 {
			entry := map[string]any{"role": msg.Role, "content": msg.Content}
			if len(msg.ToolCalls) > 0 {
				entry["tool_calls"] = msg.ToolCalls
			}
			rendered = append(rendered, entry)
			continue
		}

		parts := make([]map[string]any, 0, len(imageURLs)+1)
		if msg.Content != "" {
			parts = append(parts, map[string]any{"type": "text", "text": msg.Content})
		}
		for _, url := range imageURLs {
			parts = append(parts, map[string]any{
				"type":      "image_url",
				"image_url": map[string]any{"url": url},
			})
		}
		entry := map[string]any{"role": msg.Role, "content": parts}
		if len(msg.ToolCalls) > 0 {
			entry["tool_calls"] = msg.ToolCalls
		}
		rendered = append(rendered, entry)
	}
	return rendered
}

func openRouterChatBody(req ChatRequest, stream bool) map[string]any {
	messages := openRouterMessages(req.Messages)
	if req.System != "" {
		messages = append([]ChatMessage{{Role: "system", Content: req.System}}, messages...)
	}
	rendered := openRouterWireMessages(messages)
	body := map[string]any{
		"model":    req.Model,
		"messages": rendered,
		"stream":   stream,
	}

	if req.Format != nil {
		body["response_format"] = map[string]any{
			"type": "json_schema",
			"json_schema": map[string]any{
				"name":   openRouterStructuredOutputName,
				"strict": true,
				"schema": strictJSONSchema(req.Format),
			},
		}
	}

	// Only temperature and num_predict have portable OpenAI equivalents.
	// temperature carries the harness's determinism requirement and num_predict
	// bounds the plan response, which the "length" truncation retry depends on.
	// num_ctx is Ollama-specific and deliberately dropped.
	if temperature, present := req.Options["temperature"]; present {
		body["temperature"] = temperature
	}
	if numPredict, present := req.Options["num_predict"]; present {
		body["max_tokens"] = numPredict
	}
	return body
}

func (client OpenRouterClient) OpenChatStream(ctx context.Context, req ChatRequest) (*http.Response, error) {
	return client.do(ctx, http.MethodPost, "/chat/completions", openRouterChatBody(req, true))
}

type openRouterCompletionResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
	Error *openRouterError `json:"error"`
}

type openRouterError struct {
	Message string `json:"message"`
}

type openRouterStreamChunk struct {
	Model   string `json:"model"`
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
	Error *openRouterError `json:"error"`
}

func (client OpenRouterClient) CompleteChat(ctx context.Context, req ChatRequest) (ChatCompletionResult, error) {
	resp, err := client.do(ctx, http.MethodPost, "/chat/completions", openRouterChatBody(req, false))
	if err != nil {
		return ChatCompletionResult{}, err
	}
	defer resp.Body.Close()

	var payload openRouterCompletionResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return ChatCompletionResult{}, err
	}
	if payload.Error != nil && payload.Error.Message != "" {
		return ChatCompletionResult{}, errors.New(payload.Error.Message)
	}
	if len(payload.Choices) == 0 {
		return ChatCompletionResult{}, errors.New("openrouter returned no choices")
	}
	choice := payload.Choices[0]
	return ChatCompletionResult{
		Model:      payload.Model,
		Content:    choice.Message.Content,
		Reason:     choice.FinishReason,
		EvalTokens: payload.Usage.CompletionTokens,
	}, nil
}

func (client OpenRouterClient) do(ctx context.Context, method, path string, body map[string]any) (*http.Response, error) {
	if strings.TrimSpace(client.apiKey) == "" {
		return nil, errors.New("openrouter api key is not configured")
	}

	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(data)
	}
	httpReq, err := http.NewRequestWithContext(ctx, method, openRouterBaseURL+path, reader)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+client.apiKey)
	if body != nil {
		httpReq.Header.Set("Content-Type", "application/json")
	}

	resp, err := client.httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		message, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		trimmed := strings.TrimSpace(string(message))
		switch resp.StatusCode {
		case http.StatusUnauthorized:
			return nil, fmt.Errorf("openrouter authentication failed: %s", trimmed)
		case http.StatusTooManyRequests:
			return nil, fmt.Errorf("openrouter rate limited: %s", trimmed)
		default:
			return nil, fmt.Errorf("openrouter returned %s: %s", resp.Status, trimmed)
		}
	}
	return resp, nil
}
