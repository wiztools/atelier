package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// TokenUsage reports completion token counts. Some providers (OpenRouter)
// only populate this on the final streamed event; Ollama populates it on
// every event once eval_count is nonzero.
type TokenUsage struct {
	CompletionTokens int
}

// ChatEvent is the provider-agnostic shape every ChatProvider streams.
// Fields a given provider doesn't support (e.g. Thinking on OpenRouter)
// are simply left zero-valued.
type ChatEvent struct {
	ContentDelta string
	Thinking     string
	Model        string
	DoneReason   string
	Done         bool
	Usage        *TokenUsage
	Err          error
}

// ModelInfo describes one model available from a provider's catalog.
type ModelInfo struct {
	Provider     string   `json:"provider"`
	ID           string   `json:"id"`
	DisplayName  string   `json:"displayName"`
	ContextLen   int      `json:"contextLength,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
}

// ChatProvider is implemented once per backend (Ollama, OpenRouter, ...).
// The harness and App code depend only on this interface, never on a
// provider's wire format.
type ChatProvider interface {
	ID() string
	ListModels(ctx context.Context) ([]ModelInfo, error)
	StreamChat(ctx context.Context, req ChatRequest) (<-chan ChatEvent, error)
	CompleteChat(ctx context.Context, req ChatRequest) (ChatCompletionResult, error)
}

// resolvedProvider returns the provider ID a ChatRequest targets, defaulting
// to "ollama" for requests that predate multi-provider support (or simply
// didn't set it).
func resolvedProvider(req ChatRequest) string {
	provider := strings.TrimSpace(req.Provider)
	if provider == "" {
		return "ollama"
	}
	return provider
}

var errUnknownProvider = errors.New("unknown provider")

// ProviderRegistry resolves a provider ID to a live ChatProvider.
type ProviderRegistry struct {
	app *App
}

func newProviderRegistry(app *App) ProviderRegistry {
	return ProviderRegistry{app: app}
}

func (registry ProviderRegistry) Resolve(providerID, baseURL string) (ChatProvider, error) {
	switch resolvedProvider(ChatRequest{Provider: providerID}) {
	case "ollama":
		return newOllamaProvider(registry.app.ollamaClient(baseURL)), nil
	case "openrouter":
		apiKey, err := loadOpenRouterAPIKey()
		if err != nil {
			return nil, fmt.Errorf("openrouter api key is not available: %w", err)
		}
		if strings.TrimSpace(apiKey) == "" {
			return nil, errors.New("openrouter api key is not configured")
		}
		return newOpenRouterProvider(newOpenRouterClient(registry.app.client, apiKey)), nil
	default:
		return nil, fmt.Errorf("%w: %q", errUnknownProvider, providerID)
	}
}
