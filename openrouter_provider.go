package main

import (
	"bufio"
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

		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" || !strings.HasPrefix(line, "data:") {
				continue
			}
			payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if payload == "[DONE]" {
				events <- ChatEvent{Done: true}
				return
			}

			var chunk openRouterStreamChunk
			if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
				events <- ChatEvent{Err: err, Done: true}
				return
			}
			if chunk.Error != nil && chunk.Error.Message != "" {
				events <- ChatEvent{Err: errors.New(chunk.Error.Message), Done: true}
				return
			}

			event := ChatEvent{Model: chunk.Model}
			if len(chunk.Choices) > 0 {
				event.ContentDelta = chunk.Choices[0].Delta.Content
				event.DoneReason = chunk.Choices[0].FinishReason
			}
			if chunk.Usage != nil {
				event.Usage = &TokenUsage{CompletionTokens: chunk.Usage.CompletionTokens}
			}
			events <- event
		}
		if err := scanner.Err(); err != nil {
			events <- ChatEvent{Err: err, Done: true}
		}
	}()
	return events, nil
}
