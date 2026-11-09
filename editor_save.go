package main

// The editors' conversation-birth path. A composer or comic workspace can
// open DETACHED — from the assets panel's ＋ actions on a brand-new
// conversation, which has no id until its first send — and materialize its
// host conversation at the FIRST SAVE: the save's summary turn is turn 1, an
// assistant turn by design (the store layer is role-agnostic; only the
// chat-creation helpers assumed a human first turn). Nothing persists until
// that save, so an abandoned draft leaves no empty shell behind — the same
// discipline that lets a mid-stream editor launch open the workspace.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// EditorSaveLifecycle carries the creation context a DETACHED editor draft
// captured when it opened — the turn-1 fields a first chat send pins onto a
// new record (project, workspace, chat defaults, model overrides), read from
// the pending "New chat" state of the moment. The save request carries it
// back so the materialized conversation behaves exactly like any other:
// follow-up chat turns keep their project anchoring, library widening, and
// model selection. Ignored when the save targets an existing conversation —
// the record wins there, the same rule ChatRequest's turn-1 fields follow.
type EditorSaveLifecycle struct {
	ProjectID      string                      `json:"projectId,omitempty"`
	Workspace      string                      `json:"workspace,omitempty"`
	ChatModel      string                      `json:"chatModel,omitempty"`
	ChatSystem     string                      `json:"chatSystem,omitempty"`
	ModelOverrides *ConversationModelOverrides `json:"modelOverrides,omitempty"`
}

// chatRequest shapes the lifecycle into the ChatRequest the turn-1
// resolution helpers read (resolveTurnProject, resolveTurnWorkspace,
// requestModelOverrides) — CreateEditorConversation's reuse pattern.
func (l *EditorSaveLifecycle) chatRequest() ChatRequest {
	if l == nil {
		return ChatRequest{}
	}
	return ChatRequest{
		Model:          l.ChatModel,
		System:         l.ChatSystem,
		Workspace:      l.Workspace,
		ProjectID:      l.ProjectID,
		ModelOverrides: l.ModelOverrides,
	}
}

// beginEditorConversation validates the lifecycle and mints the on-disk
// workspace plus record a conversation born from an editor save starts
// from. This is writePendingChatConversation's creation half without the
// user turn: the caller appends its summary turn (turn 1, the assistant's)
// and snapshots. The title comes from the document being saved — no model
// call, so a detached save names its conversation in airplane mode too
// (chat-created conversations generate theirs from the first prompt; this
// path has no prompt, and the document's own name is the better name
// anyway). An unknown project fails here, before anything is written — the
// same fail-early posture as StreamChat: the project may have been deleted
// while the draft sat open, and silently filing the conversation standalone
// would hide that.
func beginEditorConversation(config AppConfig, title string, lifecycle *EditorSaveLifecycle) (conversationWorkspace, HistoryConversation, error) {
	if lifecycle == nil {
		return conversationWorkspace{}, HistoryConversation{}, errors.New("saving a detached editor draft requires its creation context — close and reopen the editor, then save again")
	}
	req := lifecycle.chatRequest()
	if _, err := resolveTurnProject(config, req); err != nil {
		return conversationWorkspace{}, HistoryConversation{}, err
	}
	workspaceRoot, err := resolveTurnWorkspace(config, req)
	if err != nil {
		return conversationWorkspace{}, HistoryConversation{}, err
	}
	if strings.TrimSpace(workspaceRoot) != "" {
		config.Tools.Filesystem.Root = workspaceRoot
	}
	now := time.Now()
	store := newHistoryStore(config.Storage)
	workspace, err := store.newWorkspace(now)
	if err != nil {
		return conversationWorkspace{}, HistoryConversation{}, err
	}
	nowText := now.Format(time.RFC3339)
	conversation := HistoryConversation{
		SchemaVersion: currentConversationSchemaVersion,
		ID:            workspace.ID,
		Kind:          "chat",
		Title:         normalizeConversationTitle(strings.TrimSpace(title), "Untitled"),
		CreatedAt:     nowText,
		UpdatedAt:     nowText,
		// The record's provider stamp is the same legacy constant both chat
		// creation paths write; the primary pairing a conversation actually
		// runs on lives in ModelOverrides and the global config.
		Provider: HistoryProvider{
			ID:      "ollama",
			BaseURL: config.Providers.Ollama.BaseURL,
		},
		Defaults: HistoryDefaults{
			ChatModel: lifecycle.ChatModel,
			System:    lifecycle.ChatSystem,
		},
		Workspace:      config.Tools.Filesystem.Root,
		ProjectID:      strings.TrimSpace(lifecycle.ProjectID),
		ModelOverrides: requestModelOverrides(req),
	}
	return workspace, conversation, nil
}

// editorSaveTarget is where one editor save lands: an existing conversation
// loaded for append, or the conversation this save is giving birth to. The
// save writes its artifact into ArtifactsDir, builds its summary turn as
// TurnNumber, then commits — so both editors share one branch instead of
// each carrying the append path and the birth path side by side.
type editorSaveTarget struct {
	ConversationID string
	ArtifactsDir   string
	TurnNumber     int
	// Created reports a birth: the frontend adopts ConversationID as the
	// draft's host and reveals the new row.
	Created bool
	commit  func(turn HistoryTurn, nowText string) error
	discard func()
}

// openEditorSaveTarget resolves a save's destination. With a conversation id
// it is the sanctioned-rewrite append (refused while the conversation
// streams). Without one it is a detached draft's first save: the lifecycle
// mints the record, and nothing is on disk until commit except the
// workspace directory, which discard removes if the save fails midway — an
// abandoned birth must not leave a half-written conversation behind.
func (a *App) openEditorSaveTarget(config AppConfig, conversationID, title string, lifecycle *EditorSaveLifecycle) (editorSaveTarget, error) {
	store := newHistoryStore(config.Storage)
	if conversationID != "" {
		if a.conversationStreaming(conversationID) {
			return editorSaveTarget{}, errors.New("the conversation is still streaming — wait for the turn to finish before saving")
		}
		loaded, err := store.loadForAppend(conversationID, "chat", "a chat", config.Tools.Filesystem.Root)
		if err != nil {
			return editorSaveTarget{}, err
		}
		return editorSaveTarget{
			ConversationID: conversationID,
			ArtifactsDir:   loaded.ArtifactsDir,
			TurnNumber:     loaded.NextTurnNumber,
			commit: func(turn HistoryTurn, nowText string) error {
				loaded.Conversation.UpdatedAt = nowText
				loaded.Conversation.Stats.TurnCount++
				loaded.Conversation.Stats.ArtifactCount++
				if err := store.writeConversation(loaded.Path, loaded.Conversation); err != nil {
					return err
				}
				return store.writeTurn(loaded.TurnsDir, turn)
			},
			discard: func() {},
		}, nil
	}
	workspace, conversation, err := beginEditorConversation(config, title, lifecycle)
	if err != nil {
		return editorSaveTarget{}, err
	}
	return editorSaveTarget{
		ConversationID: workspace.ID,
		ArtifactsDir:   workspace.ArtifactsDir,
		TurnNumber:     1,
		Created:        true,
		commit: func(turn HistoryTurn, nowText string) error {
			conversation.UpdatedAt = nowText
			conversation.Stats.TurnCount++
			conversation.Stats.ArtifactCount++
			return store.writeSnapshot(workspace, conversation, turn)
		},
		discard: func() {
			_ = os.RemoveAll(workspace.Dir)
		},
	}, nil
}

// artifactURL is the hydrated /atelier-artifact URL of a file the save wrote
// into the target's artifacts directory.
func (target editorSaveTarget) artifactURL(contentPath string) string {
	dir, err := filepath.Abs(target.ArtifactsDir)
	if err != nil {
		dir = target.ArtifactsDir
	}
	return artifactPrefix + filepath.Join(dir, filepath.Base(contentPath))
}
