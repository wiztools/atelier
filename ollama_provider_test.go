package main

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestOllamaProviderStreamChatTranslatesChunksToEvents(t *testing.T) {
	client := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			body := strings.Join([]string{
				`{"model":"mistral","message":{"role":"assistant","content":"Hel","thinking":"thinking..."},"done":false}`,
				`{"model":"mistral","message":{"role":"assistant","content":"lo"},"done":true,"done_reason":"stop","eval_count":5}`,
			}, "\n")
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Body:       io.NopCloser(strings.NewReader(body)),
				Header:     http.Header{},
			}, nil
		}),
	}
	provider := newOllamaProvider(newOllamaClient(client, "http://ollama.test"))

	events, err := provider.StreamChat(context.Background(), ChatRequest{Model: "mistral"})
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
	if thinking != "thinking..." {
		t.Fatalf("thinking = %q, want %q", thinking, "thinking...")
	}
	if !done {
		t.Fatal("expected a Done event")
	}
	if tokens != 5 {
		t.Fatalf("tokens = %d, want 5", tokens)
	}
}

func TestOllamaProviderStreamChatPropagatesChunkError(t *testing.T) {
	client := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			body := `{"error":"model not found"}`
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Body:       io.NopCloser(strings.NewReader(body)),
				Header:     http.Header{},
			}, nil
		}),
	}
	provider := newOllamaProvider(newOllamaClient(client, "http://ollama.test"))

	events, err := provider.StreamChat(context.Background(), ChatRequest{Model: "missing"})
	if err != nil {
		t.Fatalf("StreamChat returned error: %v", err)
	}
	var gotErr error
	for event := range events {
		if event.Err != nil {
			gotErr = event.Err
		}
	}
	if gotErr == nil || gotErr.Error() != "model not found" {
		t.Fatalf("gotErr = %v, want %q", gotErr, "model not found")
	}
}

func TestOllamaProviderID(t *testing.T) {
	if got := newOllamaProvider(OllamaClient{}).ID(); got != "ollama" {
		t.Fatalf("ID() = %q, want ollama", got)
	}
}
