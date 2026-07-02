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

func openRouterChatBody(req ChatRequest, stream bool) map[string]any {
	messages := req.Messages
	if req.System != "" {
		messages = append([]ChatMessage{{Role: "system", Content: req.System}}, messages...)
	}
	return map[string]any{
		"model":    req.Model,
		"messages": messages,
		"stream":   stream,
	}
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
