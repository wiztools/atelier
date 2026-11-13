package main

import (
	"context"
	"strings"
	"time"
)

func (a *App) SetOllamaBaseURL(baseURL string) error {
	normalized, err := normalizeBaseURL(baseURL)
	if err != nil {
		return err
	}
	a.baseURL = normalized
	return nil
}

func (a *App) CheckOllama(baseURL string) OllamaStatus {
	return a.ollamaClient(baseURL).Check(context.Background())
}

func (a *App) ListModels(baseURL string) ([]OllamaModel, error) {
	return a.ollamaClient(baseURL).ListModels(context.Background())
}

func (a *App) resolveBaseURL(baseURL string) string {
	normalized, err := normalizeBaseURL(baseURL)
	if err != nil {
		return a.cachedBaseURL()
	}
	if normalized == "" {
		return a.cachedBaseURL()
	}
	return normalized
}

// cachedBaseURL reads the runtime Ollama base URL set by GetConfig, SaveConfig,
// or SetOllamaBaseURL. Those writers hold configMu; this read takes the same
// lock so a config save during a concurrent chat stream can't race the field.
func (a *App) cachedBaseURL() string {
	a.configMu.Lock()
	defer a.configMu.Unlock()
	return a.baseURL
}

func (a *App) ollamaClient(baseURL string) OllamaClient {
	return newOllamaClient(a.client, a.resolveBaseURL(baseURL))
}

// openRouterClient builds an OpenRouterClient from the saved API key, mirroring
// ollamaClient's role as a direct client accessor for harness capability checks
// (ModelCapabilities). Returns ok=false when no key is configured so callers can
// skip capability detection without a key-load error — matching how Resolve
// treats an absent key as a config state rather than a hard failure.
func (a *App) openRouterClient() (OpenRouterClient, bool) {
	apiKey, err := loadOpenRouterAPIKey()
	if err != nil || strings.TrimSpace(apiKey) == "" {
		return OpenRouterClient{}, false
	}
	return newOpenRouterClient(a.client, apiKey), true
}

func (a *App) providerFor(providerID, baseURL string) (ChatProvider, error) {
	return newProviderRegistry(a).Resolve(providerID, baseURL)
}

// ListPrimaryModels lists models available for the primary chat role from
// the given provider. Harness/image model lists still go through the
// existing Ollama-only ListModels method — this is deliberately separate so
// the primary-role picker doesn't disturb the harness/image UI's capability
// detection (image-generation flags, family/parameter metadata), which only
// OllamaModel carries today.
func (a *App) ListPrimaryModels(provider, baseURL string) ([]ModelInfo, error) {
	resolved, err := a.providerFor(provider, baseURL)
	if err != nil {
		return nil, err
	}
	return resolved.ListModels(context.Background())
}

func (a *App) SaveOpenRouterAPIKey(apiKey string) error {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return clearOpenRouterAPIKey()
	}
	return saveOpenRouterAPIKey(apiKey)
}

func (a *App) HasOpenRouterAPIKey() (bool, error) {
	key, err := loadOpenRouterAPIKey()
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(key) != "", nil
}

// CheckOpenRouterConnection validates the stored OpenRouter API key with a
// cheap authenticated ping (GET /api/v1/key, no tokens spent), mirroring
// CheckFalConnection. The public /models list cannot serve as this check — it
// returns 200 even for an invalid key, so a green model-list fetch proved
// reachability, never the key.
func (a *App) CheckOpenRouterConnection() error {
	key, err := loadOpenRouterAPIKey()
	if err != nil {
		return err
	}
	if strings.TrimSpace(key) == "" {
		return errOpenRouterKeyNotConfigured
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return newOpenRouterClient(a.client, key).VerifyKey(ctx)
}

func (a *App) SaveFalAPIKey(apiKey string) error {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return clearFalAPIKey()
	}
	return saveFalAPIKey(apiKey)
}

func (a *App) HasFalAPIKey() (bool, error) {
	key, err := loadFalAPIKey()
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(key) != "", nil
}

// CheckFalConnection validates the stored fal.ai API key with a cheap
// authenticated ping (no generation). Returns an error describing why the key
// is rejected, or nil when it resolves. Used by the Settings "Check Connection"
// button and the startup key check, like its OpenRouter and Replicate
// counterparts — the model catalogs are discovery aids and never double as
// key checks.
func (a *App) CheckFalConnection() error {
	key, err := loadFalAPIKey()
	if err != nil {
		return err
	}
	if strings.TrimSpace(key) == "" {
		return errFalKeyNotConfigured
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return newFalClient(a.client, key).VerifyKey(ctx)
}

func (a *App) SaveReplicateAPIKey(apiKey string) error {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return clearReplicateAPIKey()
	}
	return saveReplicateAPIKey(apiKey)
}

func (a *App) HasReplicateAPIKey() (bool, error) {
	key, err := loadReplicateAPIKey()
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(key) != "", nil
}

// CheckReplicateConnection validates the stored Replicate API token with a
// cheap authenticated ping (GET /v1/account, no generation). Returns an error
// describing why the token is rejected, or nil when it resolves. Used by the
// Settings "Check Connection" button, mirroring CheckFalConnection.
func (a *App) CheckReplicateConnection() error {
	key, err := loadReplicateAPIKey()
	if err != nil {
		return err
	}
	if strings.TrimSpace(key) == "" {
		return errReplicateKeyNotConfigured
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return newReplicateClient(a.client, key).VerifyKey(ctx)
}

// ListOpenAICompatibleModels returns the model ids advertised by the local
// OpenAI-compatible image server (GET /v1/models) for the Settings model
// picker. Takes the endpoint explicitly because the user may still be editing
// the baseURL field; the optional bearer key is attached when one is stored.
// The picker also accepts free text, so a server without /v1/models only loses
// the suggestions, not the field.
func (a *App) ListOpenAICompatibleModels(baseURL string) ([]string, error) {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = defaultOpenAICompatibleBaseURL
	}
	apiKey, err := loadOpenAICompatibleAPIKey()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	client := newOpenAICompatibleClient(a.client, baseURL, apiKey)
	return client.ListModels(ctx)
}

// SaveOpenAICompatibleAPIKey stores the optional bearer key for the local
// OpenAI-compatible image server. Saving an empty key clears it — a local
// server with no auth is the common case and must send no Authorization header.
func (a *App) SaveOpenAICompatibleAPIKey(apiKey string) error {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return clearOpenAICompatibleAPIKey()
	}
	return saveOpenAICompatibleAPIKey(apiKey)
}

func (a *App) HasOpenAICompatibleAPIKey() (bool, error) {
	key, err := loadOpenAICompatibleAPIKey()
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(key) != "", nil
}

// ClearOpenAICompatibleAPIKey removes the optional bearer key so requests to
// the local image server go out unauthenticated again.
func (a *App) ClearOpenAICompatibleAPIKey() error {
	return clearOpenAICompatibleAPIKey()
}

// resolvedPrimaryModelAndProvider returns which model/provider the primary
// chat role should use when a request doesn't specify one explicitly.
func (a *App) resolvedPrimaryModelAndProvider(config AppConfig) (model, provider string) {
	provider = strings.TrimSpace(config.Models.PrimaryProvider)
	if provider != "openrouter" && provider != "openai-compatible" {
		provider = "ollama"
	}
	if provider == "openrouter" {
		return strings.TrimSpace(config.Providers.OpenRouter.Primary), provider
	}
	if provider == "openai-compatible" {
		return strings.TrimSpace(config.Providers.OpenAICompatible.Primary), provider
	}
	return strings.TrimSpace(config.Providers.Ollama.Models.Primary), provider
}

func (a *App) SaveRunwareAPIKey(apiKey string) error {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return clearRunwareAPIKey()
	}
	return saveRunwareAPIKey(apiKey)
}

func (a *App) HasRunwareAPIKey() (bool, error) {
	key, err := loadRunwareAPIKey()
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(key) != "", nil
}

// CheckRunwareConnection validates the stored Runware key with an
// authentication-only task (no inference), mirroring CheckReplicateConnection.
func (a *App) CheckRunwareConnection() error {
	key, err := loadRunwareAPIKey()
	if err != nil {
		return err
	}
	if strings.TrimSpace(key) == "" {
		return errRunwareKeyNotConfigured
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return newRunwareClient(a.client, key).VerifyKey(ctx)
}

// ListRunwareImageModels returns the Runware models that generate images
// (text-to-image or edit) for the Settings image-model picker. The index is
// public, so no key is needed to browse it.
func (a *App) ListRunwareImageModels() ([]RunwareModel, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	models, err := newRunwareClient(a.client, "").ListModels(ctx)
	if err != nil {
		return nil, err
	}
	filtered := make([]RunwareModel, 0, len(models))
	for _, m := range models {
		if runwareModelHasCapability(m, "text-to-image", "edit") {
			filtered = append(filtered, m)
		}
	}
	return filtered, nil
}
