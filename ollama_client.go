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

type OllamaClient struct {
	httpClient *http.Client
	baseURL    string
}

type ChatCompletionResult struct {
	Model      string
	Content    string
	Thinking   string
	Reason     string
	EvalTokens int
}

func newOllamaClient(httpClient *http.Client, baseURL string) OllamaClient {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return OllamaClient{httpClient: httpClient, baseURL: strings.TrimRight(baseURL, "/")}
}

func (client OllamaClient) Check(ctx context.Context) OllamaStatus {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, client.endpoint("/api/version"), nil)
	if err != nil {
		return OllamaStatus{Online: false, BaseURL: client.baseURL, Error: err.Error()}
	}

	resp, err := client.httpClient.Do(req)
	if err != nil {
		return OllamaStatus{Online: false, BaseURL: client.baseURL, Error: err.Error()}
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return OllamaStatus{Online: false, BaseURL: client.baseURL, Error: fmt.Sprintf("Ollama returned %s", resp.Status)}
	}

	var payload struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return OllamaStatus{Online: false, BaseURL: client.baseURL, Error: err.Error()}
	}
	return OllamaStatus{Online: true, Version: payload.Version, BaseURL: client.baseURL}
}

func (client OllamaClient) ListModels(ctx context.Context) ([]OllamaModel, error) {
	resp, err := client.getJSON(ctx, "/api/tags")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var tags ollamaTagsResponse
	if err := json.NewDecoder(resp.Body).Decode(&tags); err != nil {
		return nil, err
	}

	models := make([]OllamaModel, 0, len(tags.Models))
	for _, model := range tags.Models {
		item := OllamaModel{
			Name:       model.Name,
			ModifiedAt: model.ModifiedAt,
			Size:       model.Size,
			Family:     model.Details.Family,
			Parameter:  model.Details.ParameterSize,
		}
		client.enrichModelCapabilities(ctx, &item)
		models = append(models, item)
	}
	return models, nil
}

func (client OllamaClient) enrichModelCapabilities(ctx context.Context, model *OllamaModel) {
	if model == nil || strings.TrimSpace(model.Name) == "" {
		return
	}
	show, err := client.ShowModel(ctx, model.Name)
	if err != nil {
		model.ImageGeneration = likelyImageGenerationModelName(model.Name)
		return
	}

	if len(show.Capabilities) > 0 {
		model.Capabilities = show.Capabilities
	}
	if model.Family == "" {
		model.Family = show.Details.Family
	}
	if model.Parameter == "" {
		model.Parameter = show.Details.ParameterSize
	}
	model.ImageGeneration = show.SupportsImageGeneration(model.Name)
}

func (client OllamaClient) ShowModel(ctx context.Context, name string) (ollamaShowResponse, error) {
	body := map[string]any{"model": name}
	resp, err := client.postJSON(ctx, "/api/show", body)
	if err != nil {
		return ollamaShowResponse{}, err
	}
	defer resp.Body.Close()

	var show ollamaShowResponse
	if err := json.NewDecoder(resp.Body).Decode(&show); err != nil {
		return ollamaShowResponse{}, err
	}
	return show, nil
}

func (client OllamaClient) IsImageGenerationModel(ctx context.Context, name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	show, err := client.ShowModel(ctx, name)
	if err != nil {
		return likelyImageGenerationModelName(name)
	}
	return show.SupportsImageGeneration(name)
}

func (show ollamaShowResponse) SupportsImageGeneration(modelName string) bool {
	return hasImageGenerationCapability(show.Capabilities) ||
		hasImageGenerationModelInfo(show.ModelInfo) ||
		likelyImageGenerationModelName(modelName)
}

func hasImageGenerationCapability(capabilities []string) bool {
	for _, capability := range capabilities {
		normalized := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(capability), "_", "-"))
		if normalized == "image-generation" || normalized == "images" || normalized == "image" {
			return true
		}
		if strings.Contains(normalized, "image") && strings.Contains(normalized, "generation") {
			return true
		}
	}
	return false
}

func hasImageGenerationModelInfo(modelInfo map[string]any) bool {
	for key, value := range modelInfo {
		text := strings.ToLower(key + " " + fmt.Sprint(value))
		if strings.Contains(text, "diffusion") || strings.Contains(text, "text-to-image") ||
			strings.Contains(text, "image-generation") || strings.Contains(text, "image_generation") ||
			strings.Contains(text, "unet") {
			return true
		}
	}
	return false
}

func likelyImageGenerationModelName(name string) bool {
	normalized := strings.ToLower(name)
	imageModelHints := []string{
		"flux",
		"z-image",
		"qwen-image",
		"stable-diffusion",
		"sdxl",
		"diffusion",
		"imagen",
		"sana",
		"hidream",
	}
	for _, hint := range imageModelHints {
		if strings.Contains(normalized, hint) {
			return true
		}
	}
	return false
}

func (client OllamaClient) OpenChatStream(ctx context.Context, req ChatRequest) (*http.Response, error) {
	body := map[string]any{
		"model":    req.Model,
		"messages": req.Messages,
		"stream":   true,
	}
	if req.System != "" {
		body["messages"] = append([]ChatMessage{{Role: "system", Content: req.System}}, req.Messages...)
	}
	if req.Think != nil {
		body["think"] = req.Think
	}
	if req.Options != nil {
		body["options"] = req.Options
	}
	if req.Format != nil {
		body["format"] = req.Format
	}
	return client.postJSON(ctx, "/api/chat", body)
}

func (client OllamaClient) CompleteChat(ctx context.Context, req ChatRequest) (ChatCompletionResult, error) {
	body := map[string]any{
		"model":    req.Model,
		"messages": req.Messages,
		"stream":   false,
	}
	if req.System != "" {
		body["messages"] = append([]ChatMessage{{Role: "system", Content: req.System}}, req.Messages...)
	}
	if req.Think != nil {
		body["think"] = req.Think
	}
	if req.Options != nil {
		body["options"] = req.Options
	}
	if req.Format != nil {
		body["format"] = req.Format
	}

	resp, err := client.postJSON(ctx, "/api/chat", body)
	if err != nil {
		return ChatCompletionResult{}, err
	}
	defer resp.Body.Close()

	var payload ollamaChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return ChatCompletionResult{}, err
	}
	if payload.Error != "" {
		return ChatCompletionResult{}, errors.New(payload.Error)
	}
	content := payload.Message.Content
	if strings.TrimSpace(content) == "" {
		content = payload.Response
	}
	return ChatCompletionResult{
		Model:      payload.Model,
		Content:    content,
		Thinking:   payload.Message.Thinking,
		Reason:     payload.DoneReason,
		EvalTokens: payload.EvalCount,
	}, nil
}

func (client OllamaClient) GenerateImage(ctx context.Context, req ImageGenerateRequest) (ollamaGenerateResponse, []byte, error) {
	body := map[string]any{
		"model":  req.Model,
		"prompt": req.Prompt,
		"stream": false,
	}
	if req.Width > 0 {
		body["width"] = req.Width
	}
	if req.Height > 0 {
		body["height"] = req.Height
	}
	if req.Steps > 0 {
		body["steps"] = req.Steps
	}
	if len(req.Images) > 0 {
		body["images"] = req.Images
	}

	resp, err := client.postJSON(ctx, "/api/generate", body)
	if err != nil {
		return ollamaGenerateResponse{}, nil, err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return ollamaGenerateResponse{}, nil, err
	}

	var payload ollamaGenerateResponse
	if err := json.Unmarshal(raw, &payload); err != nil {
		return ollamaGenerateResponse{}, nil, err
	}
	if payload.Error != "" {
		return ollamaGenerateResponse{}, nil, errors.New(payload.Error)
	}
	return payload, raw, nil
}

func (client OllamaClient) GenerateChatTitle(ctx context.Context, req ChatRequest, userPrompt string, assistantContent string) (string, error) {
	titlePrompt := "Generate a concise title for this chat conversation. Return only the title, no quotes, no punctuation wrapper, no explanation. Keep it under 8 words.\n\nUser:\n" +
		compactString(userPrompt, 1600) +
		"\n\nAssistant:\n" +
		compactString(assistantContent, 1600)
	body := map[string]any{
		"model":  req.Model,
		"stream": false,
		"messages": []ChatMessage{
			{Role: "system", Content: "You create short, specific conversation titles."},
			{Role: "user", Content: titlePrompt},
		},
		"options": map[string]any{
			"temperature": 0,
			"num_predict": 24,
		},
	}

	resp, err := client.postJSON(ctx, "/api/chat", body)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var payload ollamaChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", err
	}
	if payload.Error != "" {
		return "", errors.New(payload.Error)
	}
	title := cleanGeneratedTitle(payload.Message.Content)
	if title == "" {
		title = cleanGeneratedTitle(payload.Response)
	}
	if title == "" {
		return "", errors.New("empty title")
	}
	return title, nil
}

func (client OllamaClient) getJSON(ctx context.Context, path string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, client.endpoint(path), nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		message, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("ollama returned %s: %s", resp.Status, strings.TrimSpace(string(message)))
	}
	return resp, nil
}

func (client OllamaClient) postJSON(ctx context.Context, path string, body map[string]any) (*http.Response, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, client.endpoint(path), bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := client.httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		message, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("ollama returned %s: %s", resp.Status, strings.TrimSpace(string(message)))
	}
	return resp, nil
}

func (client OllamaClient) endpoint(path string) string {
	return client.baseURL + path
}
