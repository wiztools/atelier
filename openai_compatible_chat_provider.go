package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// openAICompatibleChatProvider adapts a local OpenAI-compatible server
// (LocalAI, ...) to the ChatProvider interface for both the primary and the
// harness roles. Chat rides POST /v1/chat/completions with the OpenAI wire
// format — the same adapters OpenRouterClient uses (openRouterChatBody,
// openRouterWireMessages, openRouterMessages), since both speak the OpenAI
// protocol; only the endpoint and auth differ (the key is optional for a
// local server).
type openAICompatibleChatProvider struct {
	client openAICompatibleClient
}

func newOpenAICompatibleChatProvider(client openAICompatibleClient) openAICompatibleChatProvider {
	return openAICompatibleChatProvider{client: client}
}

func (provider openAICompatibleChatProvider) ID() string { return "openai-compatible" }

func (provider openAICompatibleChatProvider) ListModels(ctx context.Context) ([]ModelInfo, error) {
	ids, err := provider.client.ListModels(ctx)
	if err != nil {
		return nil, err
	}
	models := make([]ModelInfo, 0, len(ids))
	for _, id := range ids {
		models = append(models, ModelInfo{Provider: provider.ID(), ID: id, DisplayName: id})
	}
	return models, nil
}

// postChatCompletions sends the request body to /v1/chat/completions and
// returns the raw response, converting non-2xx statuses to errors with the
// server's message attached.
func (provider openAICompatibleChatProvider) postChatCompletions(ctx context.Context, body map[string]any) (*http.Response, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, provider.client.endpoint("/v1/chat/completions"), bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	provider.client.applyAuth(req)
	resp, err := provider.client.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		message, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("openai-compatible server returned %s: %s", resp.Status, strings.TrimSpace(string(message)))
	}
	return resp, nil
}

// openAICompatibleMessage is one choice message in the non-stream response.
// LocalAI (and DeepSeek-style servers) expose the model's reasoning channel as
// "reasoning" or "reasoning_content"; either is surfaced as Thinking.
type openAICompatibleMessage struct {
	Content          string `json:"content"`
	Reasoning        string `json:"reasoning"`
	ReasoningContent string `json:"reasoning_content"`
}

func (message openAICompatibleMessage) thinking() string {
	if strings.TrimSpace(message.Reasoning) != "" {
		return message.Reasoning
	}
	return message.ReasoningContent
}

type openAICompatibleCompletionResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		Message      openAICompatibleMessage `json:"message"`
		FinishReason string                  `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
	Error *openRouterError `json:"error"`
}

type openAICompatibleStreamChunk struct {
	Model   string `json:"model"`
	Choices []struct {
		Delta        openAICompatibleMessage `json:"delta"`
		FinishReason string                  `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
	Error *openRouterError `json:"error"`
}

func (provider openAICompatibleChatProvider) CompleteChat(ctx context.Context, req ChatRequest) (ChatCompletionResult, error) {
	resp, err := provider.postChatCompletions(ctx, openRouterChatBody(req, false))
	if err != nil {
		return ChatCompletionResult{}, err
	}
	defer resp.Body.Close()

	var payload openAICompatibleCompletionResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return ChatCompletionResult{}, err
	}
	if payload.Error != nil && payload.Error.Message != "" {
		return ChatCompletionResult{}, errors.New(payload.Error.Message)
	}
	if len(payload.Choices) == 0 {
		return ChatCompletionResult{}, errors.New("openai-compatible server returned no choices")
	}
	choice := payload.Choices[0]
	// OpenRouter surfaces upstream rejections as finish_reason:"error" with the
	// explanation in the message content; LocalAI can do the same, so the same
	// error-instead-of-empty-completion rule applies (see the OpenRouter
	// counterpart in openrouter_client.go for the failure mode this prevents).
	if choice.FinishReason == "error" {
		msg := strings.TrimSpace(choice.Message.Content)
		if msg == "" {
			msg = "openai-compatible server returned an error (finish_reason: error) with no detail"
		}
		return ChatCompletionResult{}, errors.New(msg)
	}
	return ChatCompletionResult{
		Model:        payload.Model,
		Content:      choice.Message.Content,
		Thinking:     choice.Message.thinking(),
		Reason:       choice.FinishReason,
		EvalTokens:   payload.Usage.CompletionTokens,
		PromptTokens: payload.Usage.PromptTokens,
	}, nil
}

func (provider openAICompatibleChatProvider) StreamChat(ctx context.Context, req ChatRequest) (<-chan ChatEvent, error) {
	resp, err := provider.postChatCompletions(ctx, openRouterChatBody(req, true))
	if err != nil {
		return nil, err
	}

	events := make(chan ChatEvent)
	go func() {
		defer resp.Body.Close()
		defer close(events)

		streamLines(resp.Body, events, func(line string) (ChatEvent, bool, error) {
			// SSE frames are prefixed "data:"; skip everything else.
			if !strings.HasPrefix(line, "data:") {
				return ChatEvent{}, false, nil
			}
			payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if payload == "[DONE]" {
				return ChatEvent{Done: true}, true, nil
			}
			var chunk openAICompatibleStreamChunk
			if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
				return ChatEvent{}, false, err
			}
			if chunk.Error != nil && chunk.Error.Message != "" {
				return ChatEvent{}, false, errors.New(chunk.Error.Message)
			}
			event := ChatEvent{Model: chunk.Model}
			if len(chunk.Choices) > 0 {
				event.ContentDelta = chunk.Choices[0].Delta.Content
				event.Thinking = chunk.Choices[0].Delta.thinking()
				event.DoneReason = chunk.Choices[0].FinishReason
			}
			if chunk.Usage != nil {
				event.Usage = &TokenUsage{PromptTokens: chunk.Usage.PromptTokens, CompletionTokens: chunk.Usage.CompletionTokens}
			}
			return event, false, nil
		})
	}()
	return events, nil
}
