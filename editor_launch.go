package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// EditorLaunchResult names the conversation CreateEditorConversation persisted
// and the artifacts the editors open by. (conversationID, artifactID) is the
// one identity every editor entry point takes — the assets panel, the inline
// Edit buttons, and this launch path alike.
type EditorLaunchResult struct {
	ConversationID   string   `json:"conversationId"`
	ImageArtifactIDs []string `json:"imageArtifactIds,omitempty"`
	VideoArtifactIDs []string `json:"videoArtifactIds,omitempty"`
}

// editorLaunchEventName is the atelier:editor-launch payload's event: the
// harness's open_editor tool fired and the named conversation's asset should
// open in the matching built-in editor. The frontend opens only when the
// conversation is the active one and no editor is already on screen — a
// mid-stream launch must never steal focus from an edit in progress.
const editorLaunchEventName = "atelier:editor-launch"

type EditorLaunchEvent struct {
	ConversationID string `json:"conversationId"`
	ArtifactID     string `json:"artifactId"`
	Kind           string `json:"kind"`
}

// emitEditorLaunchEvent delivers the open_editor tool's UI directive and
// reports whether it was delivered — a false lets the caller fail the tool
// instead of leaving evidence that claims an editor no UI ever showed. Like
// emitChatEvent it no-ops without a Wails context (tests, early startup).
func (a *App) emitEditorLaunchEvent(event EditorLaunchEvent) bool {
	if a.ctx == nil {
		return false
	}
	runtime.EventsEmit(a.ctx, editorLaunchEventName, event)
	return true
}

// ToolOpenEditorResult is the open_editor tool's evidence payload: what was
// opened and where. It carries no media, no cost, and no model — the built-in
// editor is a UI surface, not a generation backend — so it lands as a bare
// tool_call activity (name/status only) in the ledger.
type ToolOpenEditorResult struct {
	Kind           string `json:"kind"`
	ConversationID string `json:"conversationId"`
	ArtifactID     string `json:"artifactId"`
}

// newestEditorArtifact finds the artifact an open_editor call should open:
// the newest turn's first content entry of the requested kind carrying an
// artifact id — the same newest-turn-first walk newestVisualMediaKind uses
// for the history fallback, so "the editor" and "the image" resolve to the
// same asset. An empty kind means the newest image-or-video of either.
// Attachments, @-mentions, and generated media all persist as artifacts, so
// every one of them is openable; text and transcripts are skipped.
func newestEditorArtifact(storage ConfigStorage, conversationID, kind string) (string, string, error) {
	if kind != "" && kind != "image" && kind != "video" {
		return "", "", fmt.Errorf("unknown editor kind %q", kind)
	}
	detail, err := getConversation(storage, conversationID)
	if err != nil {
		return "", "", err
	}
	for i := len(detail.Turns) - 1; i >= 0; i-- {
		for _, content := range detail.Turns[i].Content {
			if content.ArtifactID == "" {
				continue
			}
			if kind == "" && content.Type != "image" && content.Type != "video" {
				continue
			}
			if kind != "" && content.Type != kind {
				continue
			}
			if kind == "" {
				kind = content.Type
			}
			return kind, content.ArtifactID, nil
		}
	}
	if kind == "" {
		return "", "", errors.New("open_editor found no image or video in this conversation — ask the user to attach or generate one first")
	}
	return "", "", fmt.Errorf("open_editor found no %s in this conversation — ask the user to attach or generate one first", kind)
}

// executeOpenEditor resolves the artifact and delivers the launch event. The
// editors open by (conversationID, artifactID) — the same identity every other
// editor entry point takes — and the hook indirection keeps the definition
// testable without a Wails runtime, like every other capability hook on the
// tool execution context.
func executeOpenEditor(tools HarnessToolExecutionContext, mode string) (ToolOpenEditorResult, string, error) {
	if strings.TrimSpace(tools.ConversationID) == "" {
		return ToolOpenEditorResult{}, "editor launch is unavailable", errors.New("open_editor opens a conversation asset, and no conversation is attached to this call")
	}
	if tools.OpenEditor == nil {
		return ToolOpenEditorResult{}, "editor launch is unavailable", errors.New("open_editor is not available in this context")
	}
	kind, artifactID, err := newestEditorArtifact(tools.Storage, tools.ConversationID, mode)
	if err != nil {
		return ToolOpenEditorResult{}, "editor launch failed", err
	}
	if err := tools.OpenEditor(kind, tools.ConversationID, artifactID); err != nil {
		return ToolOpenEditorResult{}, "editor launch failed", err
	}
	summary := fmt.Sprintf("Opened %s in the built-in %s editor on the user's screen. The asset was opened as-is — nothing was generated or modified; do not describe an edit as done.", artifactID, kind)
	return ToolOpenEditorResult{Kind: kind, ConversationID: tools.ConversationID, ArtifactID: artifactID}, summary, nil
}

// openEditorToolDefinition is the harness's editor-launch capability — the AI
// tier beside the composer's deterministic detection (editorLaunch.ts). It is
// unconditional in the registry: the built-in editors need no keys, no CLI,
// and no model, so an editor-open ask never degrades to an install note or a
// from-knowledge text answer. The side effect is the atelier:editor-launch
// event; the tool's whole job is pointing the UI at the right artifact.
func openEditorToolDefinition() HarnessToolDefinition {
	return HarnessToolDefinition{
		Name:        "open_editor",
		Title:       "Open in editor",
		Description: "Opens one of the conversation's assets in Atelier's built-in editor on the user's screen. mode \"image\" opens the image editor, \"video\" the video editor; omit mode to open whichever editor matches the conversation's newest image-or-video artifact. The asset is the newest artifact of that kind — this turn's attachment, an @-mentioned asset, or one from conversation history — and it is opened as-is, never modified or regenerated. Call this ONLY when the user asks to open an asset in the editor (e.g. \"open this in the video editor\"); it does not perform edits — for a change the user described, use the edit or generation tools instead.",
		Example:     `{"name":"open_editor","mode":"video"}`,
		Risk:        HarnessToolRiskRead,
		ParamSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"mode": enumParam(`Which editor to open — "image" or "video". Omit to open the one matching the conversation's newest image-or-video artifact.`, "image", "video"),
			},
			"required": []string{},
		},
		Validate: func(prefix string, call HarnessToolCall) []string {
			switch strings.TrimSpace(call.Mode) {
			case "", "image", "video":
				return nil
			default:
				return []string{prefix + `.mode must be "image" or "video" for open_editor`}
			}
		},
		Execute: func(ctx context.Context, tools HarnessToolExecutionContext, call HarnessToolCall) (any, string, error) {
			return executeOpenEditor(tools, strings.TrimSpace(call.Mode))
		},
		Activity: func(result HarnessToolResult) HarnessToolActivity {
			activity := defaultHarnessToolActivity(result)
			// The launch consumes nothing — no media kind, no cost, no model —
			// so the bare projection carries just the verb and what it opened.
			if typed, ok := result.Result.(ToolOpenEditorResult); ok {
				activity.Command = []string{"open_editor", typed.Kind}
			}
			return activity
		},
	}
}

// CreateEditorConversation persists a brand-new conversation whose only turn is
// the user's editor-launch request — the prompt text plus an attached image or
// video — and names the persisted artifacts so the frontend can open the
// matching editor on them. This is StreamChat's turn-1 creation path without
// the stream: no harness engine is built and no model is called, but the record
// keeps the same lifecycle pinning (workspace, project, model overrides) as
// writePendingChatConversation, so a follow-up chat turn in the conversation
// behaves exactly like any other.
func (a *App) CreateEditorConversation(req ChatRequest) (EditorLaunchResult, error) {
	if len(req.Messages) == 0 {
		return EditorLaunchResult{}, errors.New("at least one message is required")
	}
	config, err := loadReadyConfig()
	if err != nil {
		return EditorLaunchResult{}, err
	}
	// Fail an unknown project before anything is persisted — the same
	// fail-early posture as StreamChat.
	if _, err := resolveTurnProject(config, req); err != nil {
		return EditorLaunchResult{}, err
	}
	workspaceRoot, err := resolveTurnWorkspace(config, req)
	if err != nil {
		return EditorLaunchResult{}, err
	}
	if strings.TrimSpace(workspaceRoot) != "" {
		config.Tools.Filesystem.Root = workspaceRoot
	}
	// A primary override rewrites req.Model, so the record's Defaults.ChatModel
	// pins the overridden selection — same as every first turn.
	config, req, err = applyConversationModelOverrides(config, req)
	if err != nil {
		return EditorLaunchResult{}, err
	}
	message := lastUserMessage(req.Messages)
	if len(message.Images) == 0 && len(message.Videos) == 0 {
		return EditorLaunchResult{}, errors.New("an attached image or video is required to open an editor")
	}
	// Opening an editor must not require a configured chat model — nothing is
	// called on this path — but stamp the selection when there is one so a
	// later follow-up turn starts from the same defaults as any conversation.
	if strings.TrimSpace(req.Model) == "" {
		req.Model = strings.TrimSpace(config.Providers.Ollama.Models.Primary)
	}
	conversationID, _, err := writePendingChatConversation(config, req)
	if err != nil {
		return EditorLaunchResult{}, err
	}
	// The editors open by artifact id, which the creation helper does not
	// return — read the turn back and collect the persisted attachments by
	// kind, in attachment order (mention references, if any, come after).
	detail, err := getConversation(config.Storage, conversationID)
	if err != nil {
		return EditorLaunchResult{}, err
	}
	result := EditorLaunchResult{ConversationID: conversationID}
	for _, turn := range detail.Turns {
		for _, content := range turn.Content {
			if content.ArtifactID == "" {
				continue
			}
			switch content.Type {
			case "image":
				result.ImageArtifactIDs = append(result.ImageArtifactIDs, content.ArtifactID)
			case "video":
				result.VideoArtifactIDs = append(result.VideoArtifactIDs, content.ArtifactID)
			}
		}
	}
	return result, nil
}
