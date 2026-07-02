package main

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestOpenRouterProviderStreamChatTranslatesSSEToEvents(t *testing.T) {
	client := newOpenRouterClient(&http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			body := strings.Join([]string{
				`data: {"model":"anthropic/claude-3.5-sonnet","choices":[{"delta":{"content":"Hel"}}]}`,
				`data: {"model":"anthropic/claude-3.5-sonnet","choices":[{"delta":{"content":"lo"},"finish_reason":"stop"}]}`,
				`data: {"model":"anthropic/claude-3.5-sonnet","choices":[{"delta":{}}],"usage":{"completion_tokens":7}}`,
				`data: [DONE]`,
			}, "\n")
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Body:       io.NopCloser(strings.NewReader(body)),
				Header:     http.Header{},
			}, nil
		}),
	}, "sk-or-test")
	provider := newOpenRouterProvider(client)

	events, err := provider.StreamChat(context.Background(), ChatRequest{Model: "anthropic/claude-3.5-sonnet"})
	if err != nil {
		t.Fatalf("StreamChat returned error: %v", err)
	}

	var content, thinking string
	var done bool
	var tokens int
	for event := range events {
		content += event.ContentDelta
		thinking += event.Thinking
		if event.Usage != nil {
			tokens = event.Usage.CompletionTokens
		}
		if event.Done {
			done = true
		}
	}
	if content != "Hello" {
		t.Fatalf("content = %q, want Hello", content)
	}
	if thinking != "" {
		t.Fatalf("thinking = %q, want empty (OpenRouter has no thinking field)", thinking)
	}
	if !done {
		t.Fatal("expected a Done event on [DONE] sentinel")
	}
	// OpenRouter only reports usage on the final chunk before [DONE].
	if tokens != 7 {
		t.Fatalf("tokens = %d, want 7 (reported only once, at stream end)", tokens)
	}
}

func TestOpenRouterProviderStreamChatPropagatesInlineError(t *testing.T) {
	client := newOpenRouterClient(&http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			body := `data: {"error":{"message":"rate limited"}}` + "\n" + `data: [DONE]`
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Body:       io.NopCloser(strings.NewReader(body)),
				Header:     http.Header{},
			}, nil
		}),
	}, "sk-or-test")
	provider := newOpenRouterProvider(client)

	events, err := provider.StreamChat(context.Background(), ChatRequest{Model: "anthropic/claude-3.5-sonnet"})
	if err != nil {
		t.Fatalf("StreamChat returned error: %v", err)
	}
	var gotErr error
	for event := range events {
		if event.Err != nil {
			gotErr = event.Err
		}
	}
	if gotErr == nil || gotErr.Error() != "rate limited" {
		t.Fatalf("gotErr = %v, want %q", gotErr, "rate limited")
	}
}

func TestOpenRouterProviderID(t *testing.T) {
	if got := newOpenRouterProvider(OpenRouterClient{}).ID(); got != "openrouter" {
		t.Fatalf("ID() = %q, want openrouter", got)
	}
}
