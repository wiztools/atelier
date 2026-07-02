package main

import (
	"context"
	"errors"
	"testing"
)

type fakeProvider struct {
	id     string
	events []ChatEvent
}

func (p fakeProvider) ID() string { return p.id }

func (p fakeProvider) ListModels(ctx context.Context) ([]ModelInfo, error) {
	return []ModelInfo{{Provider: p.id, ID: "fake-model"}}, nil
}

func (p fakeProvider) CompleteChat(ctx context.Context, req ChatRequest) (ChatCompletionResult, error) {
	return ChatCompletionResult{Model: req.Model, Content: "fake completion"}, nil
}

func (p fakeProvider) StreamChat(ctx context.Context, req ChatRequest) (<-chan ChatEvent, error) {
	out := make(chan ChatEvent, len(p.events))
	for _, event := range p.events {
		out <- event
	}
	close(out)
	return out, nil
}

func TestChatProviderInterfaceIsSatisfiedByFakeProvider(t *testing.T) {
	var provider ChatProvider = fakeProvider{
		id: "fake",
		events: []ChatEvent{
			{ContentDelta: "hel"},
			{ContentDelta: "lo", Done: true, Usage: &TokenUsage{CompletionTokens: 2}},
		},
	}

	events, err := provider.StreamChat(context.Background(), ChatRequest{Model: "fake-model"})
	if err != nil {
		t.Fatalf("StreamChat returned error: %v", err)
	}

	var content string
	var sawUsage *TokenUsage
	for event := range events {
		content += event.ContentDelta
		if event.Usage != nil {
			sawUsage = event.Usage
		}
	}
	if content != "hello" {
		t.Fatalf("streamed content = %q, want %q", content, "hello")
	}
	if sawUsage == nil || sawUsage.CompletionTokens != 2 {
		t.Fatalf("usage = %+v, want CompletionTokens=2", sawUsage)
	}
}

func TestResolvedProviderDefaultsToOllama(t *testing.T) {
	cases := []struct {
		name string
		req  ChatRequest
		want string
	}{
		{"empty provider defaults to ollama", ChatRequest{}, "ollama"},
		{"explicit ollama", ChatRequest{Provider: "ollama"}, "ollama"},
		{"explicit openrouter", ChatRequest{Provider: "openrouter"}, "openrouter"},
		{"whitespace-only defaults to ollama", ChatRequest{Provider: "  "}, "ollama"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := resolvedProvider(testCase.req); got != testCase.want {
				t.Fatalf("resolvedProvider(%+v) = %q, want %q", testCase.req, got, testCase.want)
			}
		})
	}
}

func TestUnknownProviderIDIsAnError(t *testing.T) {
	_, err := (ProviderRegistry{}).unknownProviderError("carrier-pigeon")
	if err == nil || !errors.Is(err, errUnknownProvider) {
		t.Fatalf("expected errUnknownProvider, got %v", err)
	}
}
