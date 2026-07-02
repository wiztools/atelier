package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"strings"
)

// OllamaProvider adapts OllamaClient to the ChatProvider interface.
type OllamaProvider struct {
	client OllamaClient
}

func newOllamaProvider(client OllamaClient) OllamaProvider {
	return OllamaProvider{client: client}
}

func (provider OllamaProvider) ID() string { return "ollama" }

func (provider OllamaProvider) ListModels(ctx context.Context) ([]ModelInfo, error) {
	models, err := provider.client.ListModels(ctx)
	if err != nil {
		return nil, err
	}
	infos := make([]ModelInfo, 0, len(models))
	for _, model := range models {
		infos = append(infos, ModelInfo{
			Provider:     "ollama",
			ID:           model.Name,
			DisplayName:  model.Name,
			Capabilities: model.Capabilities,
		})
	}
	return infos, nil
}

func (provider OllamaProvider) CompleteChat(ctx context.Context, req ChatRequest) (ChatCompletionResult, error) {
	return provider.client.CompleteChat(ctx, req)
}

func (provider OllamaProvider) StreamChat(ctx context.Context, req ChatRequest) (<-chan ChatEvent, error) {
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
			if line == "" {
				continue
			}

			var chunk ollamaChatChunk
			if err := json.Unmarshal([]byte(line), &chunk); err != nil {
				events <- ChatEvent{Err: err, Done: true}
				return
			}
			if chunk.Error != "" {
				events <- ChatEvent{Err: errors.New(chunk.Error), Done: true}
				return
			}

			event := ChatEvent{
				ContentDelta: chunk.Message.Content,
				Thinking:     chunk.Message.Thinking,
				Model:        chunk.Model,
				DoneReason:   chunk.DoneReason,
				Done:         chunk.Done,
			}
			if chunk.EvalCount > 0 {
				event.Usage = &TokenUsage{CompletionTokens: chunk.EvalCount}
			}
			events <- event
			if chunk.Done {
				return
			}
		}
		if err := scanner.Err(); err != nil {
			events <- ChatEvent{Err: err, Done: true}
		}
	}()
	return events, nil
}
