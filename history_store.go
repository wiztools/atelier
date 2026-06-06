package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type HistoryStore struct {
	storage ConfigStorage
}

type conversationWorkspace struct {
	ID           string
	Dir          string
	TurnsDir     string
	ArtifactsDir string
}

type loadedConversation struct {
	Path           string
	TurnsDir       string
	ArtifactsDir   string
	Conversation   HistoryConversation
	NextTurnNumber int
}

func newHistoryStore(storage ConfigStorage) HistoryStore {
	return HistoryStore{storage: storage}
}

func (store HistoryStore) newWorkspace(createdAt time.Time) (conversationWorkspace, error) {
	conversationID := randomID("conv")
	conversationDir := conversationDir(store.storage, createdAt, conversationID)
	workspace := conversationWorkspace{
		ID:           conversationID,
		Dir:          conversationDir,
		TurnsDir:     filepath.Join(conversationDir, "turns"),
		ArtifactsDir: filepath.Join(conversationDir, "artifacts"),
	}
	if err := os.MkdirAll(workspace.TurnsDir, 0755); err != nil {
		return conversationWorkspace{}, err
	}
	if err := os.MkdirAll(workspace.ArtifactsDir, 0755); err != nil {
		return conversationWorkspace{}, err
	}
	return workspace, nil
}

func (store HistoryStore) writeSnapshot(workspace conversationWorkspace, conversation HistoryConversation, turns ...HistoryTurn) error {
	if err := writeJSONFile(filepath.Join(workspace.Dir, "conversation.json"), conversation); err != nil {
		return err
	}
	for _, turn := range turns {
		if err := writeJSONFile(filepath.Join(workspace.TurnsDir, turn.ID+".json"), turn); err != nil {
			return err
		}
	}
	return nil
}

func (store HistoryStore) loadForAppend(conversationID, expectedKind, displayKind string) (loadedConversation, error) {
	conversationPath, err := findConversationPath(store.storage, conversationID)
	if err != nil {
		return loadedConversation{}, err
	}

	var conversation HistoryConversation
	if err := readJSONFile(conversationPath, &conversation); err != nil {
		return loadedConversation{}, err
	}
	if conversation.DeletedAt != "" {
		return loadedConversation{}, fmt.Errorf("conversation %s is deleted", conversationID)
	}
	if conversation.Kind != expectedKind {
		return loadedConversation{}, fmt.Errorf("conversation %s is not %s conversation", conversationID, displayKind)
	}

	detail, err := getConversation(store.storage, conversationID)
	if err != nil {
		return loadedConversation{}, err
	}
	conversationDir := filepath.Dir(conversationPath)
	return loadedConversation{
		Path:           conversationPath,
		TurnsDir:       filepath.Join(conversationDir, "turns"),
		ArtifactsDir:   filepath.Join(conversationDir, "artifacts"),
		Conversation:   conversation,
		NextTurnNumber: len(detail.Turns) + 1,
	}, nil
}

func (store HistoryStore) writeConversation(path string, conversation HistoryConversation) error {
	return writeJSONFile(path, conversation)
}

func (store HistoryStore) writeTurn(turnsDir string, turn HistoryTurn) error {
	if err := os.MkdirAll(turnsDir, 0755); err != nil {
		return err
	}
	return writeJSONFile(filepath.Join(turnsDir, turn.ID+".json"), turn)
}

func conversationSummaryFrom(conversation HistoryConversation) ConversationSummary {
	return ConversationSummary{
		ID:            conversation.ID,
		Kind:          conversation.Kind,
		Title:         conversation.Title,
		CreatedAt:     conversation.CreatedAt,
		UpdatedAt:     conversation.UpdatedAt,
		TurnCount:     conversation.Stats.TurnCount,
		ArtifactCount: conversation.Stats.ArtifactCount,
	}
}
