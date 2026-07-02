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
