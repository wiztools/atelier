package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

func (a *App) StreamChat(req ChatRequest) (*ChatStreamStart, error) {
	if len(req.Messages) == 0 {
		return nil, errors.New("at least one message is required")
	}

	config, err := loadReadyConfig()
	if err != nil {
		return nil, err
	}
	// Fail an unknown project before anything is persisted (see
	// resolveTurnProject) — same fail-early posture as the provider check.
	if _, err := resolveTurnProject(config, req); err != nil {
		return nil, err
	}
	// Resolve the conversation's immutable workspace and make it the engine's
	// filesystem-tool root before construction. The registry (cached per
	// engine), the filesystem layer, and the harness/triage prompts all read it
	// through h.config, so this single override scopes the whole turn. See
	// resolveTurnWorkspace for the turn-1-vs-turn-2+ rules.
	workspaceRoot, err := resolveTurnWorkspace(config, req)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(workspaceRoot) != "" {
		config.Tools.Filesystem.Root = workspaceRoot
	}
	// Apply the conversation's model-selection overrides the same way — one
	// rewrite of the per-stream config copy before engine construction scopes
	// harness routing, tool-registry gating, and every tool's default model
	// through h.config (see applyConversationModelOverrides). A primary
	// override also rewrites the request's model fields: the record wins,
	// like the workspace.
	config, req, err = applyConversationModelOverrides(config, req)
	if err != nil {
		return nil, err
	}
	// Resolve the project's instruction block onto the request — the same
	// seam as the workspace/model overrides, but read live every turn (see
	// resolveTurnProjectNotes). From here req.ProjectNotes reaches the triage
	// and planner prompts unchanged, so no further plumbing is needed.
	req.ProjectNotes = resolveTurnProjectNotes(config, req)
	engine := newHarnessEngine(config, a)
	if strings.TrimSpace(req.Model) == "" {
		req.Model = strings.TrimSpace(config.Providers.Ollama.Models.Primary)
	}
	if strings.TrimSpace(req.Model) == "" {
		return nil, errors.New("model is required")
	}

	requestID := strings.TrimSpace(req.RequestID)
	if requestID == "" {
		requestID = fmt.Sprintf("chat-%d", time.Now().UnixNano())
	}

	conversationID, userVideoURLs, userSubtitleURLs, userMarkdownURLs, err := engine.StartChatTurn(req)
	if err != nil {
		return nil, err
	}
	req.ConversationID = conversationID

	streamCtx, cancel := context.WithCancel(context.Background())
	a.streamsMu.Lock()
	a.streams[requestID] = cancel
	a.streamConversations[requestID] = conversationID
	a.streamsMu.Unlock()

	go func() {
		defer func() {
			a.streamsMu.Lock()
			delete(a.streams, requestID)
			delete(a.streamConversations, requestID)
			a.streamsMu.Unlock()
		}()
		engine.RunChatStream(streamCtx, requestID, req, true)
	}()

	return &ChatStreamStart{RequestID: requestID, ConversationID: conversationID, UserVideos: userVideoURLs, UserSubtitles: userSubtitleURLs, UserMarkdowns: userMarkdownURLs}, nil
}

func (a *App) CancelStream(requestID string) {
	a.streamsMu.Lock()
	cancel := a.streams[requestID]
	a.streamsMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (a *App) writeChatConversation(req ChatRequest, assistantContent, assistantThinking, model, reason string, tokens int) (string, error) {
	if strings.TrimSpace(assistantContent) == "" && strings.TrimSpace(assistantThinking) == "" {
		return "", nil
	}
	config, err := loadReadyConfig()
	if err != nil {
		return "", err
	}
	// Same fail-early project validation as StreamChat (see resolveTurnProject).
	if _, err := resolveTurnProject(config, req); err != nil {
		return "", err
	}
	// Apply the same per-conversation workspace override as StreamChat so this
	// non-streaming write path is consistent (see resolveTurnWorkspace).
	workspaceRoot, err := resolveTurnWorkspace(config, req)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(workspaceRoot) != "" {
		config.Tools.Filesystem.Root = workspaceRoot
	}
	// Same per-conversation model overrides as StreamChat so this
	// non-streaming write path is consistent (see
	// applyConversationModelOverrides).
	config, req, err = applyConversationModelOverrides(config, req)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(model) == "" {
		model = req.Model
	}
	title := ""
	if strings.TrimSpace(req.ConversationID) == "" {
		title = a.generateConversationTitle(config, req, assistantContent)
	}
	run := fallbackHarnessRun(req.Model, reason, tokens)
	return newHarnessEngine(config).SaveChatTurn(req, assistantContent, assistantThinking, model, resolvedProvider(req), reason, tokens, title, run)
}

// generateConversationTitle names a new conversation with the primary model,
// falling back to the truncated prompt whenever the call can't be made or
// comes back empty. It resolves through the provider layer rather than
// reaching for ollamaClient: req.Model is the *primary* chat model, so an
// OpenRouter primary would otherwise send an OpenRouter model name to the
// local Ollama endpoint and silently degrade every title to the fallback.
func (a *App) generateConversationTitle(config AppConfig, req ChatRequest, assistantContent string) string {
	userPrompt := lastUserPrompt(req.Messages)
	fallback := titleFromPrompt(userPrompt)
	if strings.TrimSpace(req.Model) == "" || strings.TrimSpace(userPrompt) == "" {
		return fallback
	}
	// Airplane mode: the title call would leave the machine whenever the
	// primary model is a cloud one — skip straight to the prompt-derived
	// fallback. Resolve would refuse the provider anyway; this keeps the skip
	// deliberate instead of relying on an error shaped like a failure.
	if airplaneModeActive(config) {
		return fallback
	}

	provider, err := a.providerFor(resolvedProvider(req), req.BaseURL)
	if err != nil {
		return fallback
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	completion, err := provider.CompleteChat(ctx, conversationTitleRequest(config, req, userPrompt, assistantContent))
	if err != nil {
		return fallback
	}
	return normalizeConversationTitle(completion.Content, userPrompt)
}

// conversationTitleRequest builds the title call for a finished turn. The
// options are Ollama tuning knobs; openRouterChatBody drops options entirely,
// so the same request is valid on either provider.
func conversationTitleRequest(config AppConfig, req ChatRequest, userPrompt, assistantContent string) ChatRequest {
	titlePrompt := "Generate a concise title for this chat conversation. Return only the title, no quotes, no punctuation wrapper, no explanation. Keep it under 8 words.\n\nUser:\n" +
		compactString(userPrompt, 1600) +
		"\n\nAssistant:\n" +
		compactString(assistantContent, 1600)
	options := map[string]any{
		"temperature": 0,
		"num_predict": 24,
	}
	for key, value := range req.Options {
		if _, exists := options[key]; !exists {
			options[key] = value
		}
	}
	return ChatRequest{
		BaseURL:  req.BaseURL,
		Provider: resolvedProvider(req),
		Model:    req.Model,
		System:   "You create short, specific conversation titles.",
		Messages: []ChatMessage{{Role: "user", Content: titlePrompt}},
		Options:  withNumCtx(options, ollamaNumCtx(config)),
	}
}

func (a *App) emitChatEvent(event ChatStreamEvent) {
	if a.ctx != nil {
		runtime.EventsEmit(a.ctx, "chat:chunk", event)
	}
}

// HarnessRunEvent is the atelier:harness-run payload: the full run snapshot
// after every step transition. The live panel and the persisted record render
// from the same shape, so the UI never fabricates in-flight state.
type HarnessRunEvent struct {
	RequestID      string     `json:"requestId"`
	ConversationID string     `json:"conversationId,omitempty"`
	Run            HarnessRun `json:"run"`
}

func (a *App) emitHarnessRunEvent(event HarnessRunEvent) {
	if a.ctx != nil {
		runtime.EventsEmit(a.ctx, "atelier:harness-run", event)
	}
}
