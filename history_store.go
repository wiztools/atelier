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

	conversationDir := filepath.Dir(conversationPath)
	turnsDir := filepath.Join(conversationDir, "turns")
	return loadedConversation{
		Path:           conversationPath,
		TurnsDir:       turnsDir,
		ArtifactsDir:   filepath.Join(conversationDir, "artifacts"),
		Conversation:   conversation,
		NextTurnNumber: countTurnFiles(turnsDir) + 1,
	}, nil
}

// countTurnFiles returns the number of turn files in a conversation's turns
// directory. Turn files are always "turn_NNNNNN.json" with a monotonically
// increasing number, so this count yields the next turn number (1-based).
//
// It counts directory entries rather than reading them so the append path
// does not unmarshal every turn — and unlike the previous approach (which
// went through getConversation, itself walking the whole conversation tree a
// second time), this never re-scans history or parses turn bodies just to
// pick a number. Returns 0 when the directory does not exist yet.
func countTurnFiles(turnsDir string) int {
	entries, err := os.ReadDir(turnsDir)
	if err != nil {
		return 0
	}
	count := 0
	for _, entry := range entries {
		if !entry.IsDir() && filepath.Ext(entry.Name()) == ".json" {
			count++
		}
	}
	return count
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
