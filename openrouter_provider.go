package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
)

// OpenRouterProvider adapts OpenRouterClient to the ChatProvider interface.
type OpenRouterProvider struct {
	client OpenRouterClient
}

func newOpenRouterProvider(client OpenRouterClient) OpenRouterProvider {
	return OpenRouterProvider{client: client}
}

func (provider OpenRouterProvider) ID() string { return "openrouter" }

func (provider OpenRouterProvider) ListModels(ctx context.Context) ([]ModelInfo, error) {
	return provider.client.ListModels(ctx)
}

func (provider OpenRouterProvider) CompleteChat(ctx context.Context, req ChatRequest) (ChatCompletionResult, error) {
	return provider.client.CompleteChat(ctx, req)
}

func (provider OpenRouterProvider) StreamChat(ctx context.Context, req ChatRequest) (<-chan ChatEvent, error) {
	resp, err := provider.client.OpenChatStream(ctx, req)
	if err != nil {
		return nil, err
	}

	events := make(chan ChatEvent)
	go func() {
		defer resp.Body.Close()
		defer close(events)

		streamLines(resp.Body, events, func(line string) (ChatEvent, bool, error) {
			// OpenRouter SSE frames are prefixed "data:"; skip everything else.
			if !strings.HasPrefix(line, "data:") {
				return ChatEvent{}, false, nil
			}
			payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if payload == "[DONE]" {
				return ChatEvent{Done: true}, true, nil
			}
			var chunk openRouterStreamChunk
			if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
				return ChatEvent{}, false, err
			}
			if chunk.Error != nil && chunk.Error.Message != "" {
				return ChatEvent{}, false, errors.New(chunk.Error.Message)
			}
			event := ChatEvent{Model: chunk.Model}
			if len(chunk.Choices) > 0 {
				event.ContentDelta = chunk.Choices[0].Delta.Content
				event.DoneReason = chunk.Choices[0].FinishReason
			}
			if chunk.Usage != nil {
				event.Usage = &TokenUsage{CompletionTokens: chunk.Usage.CompletionTokens}
			}
			return event, false, nil
		})
	}()
	return events, nil
}
