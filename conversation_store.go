package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	// maxConversationList is the hard cap on how many conversations the sidebar
	// list returns. We intentionally never page beyond the most recent 100.
	maxConversationList = 100
	// listCandidateOverfetch is how many conversation.json files we shortlist by
	// file mtime (a cheap, no-parse proxy for recency) before parsing. The 50
	// slots of slack above maxConversationList absorb minor mtime/UpdatedAt skew
	// so the exact UpdatedAt re-sort below is not starved.
	listCandidateOverfetch = 150
)

type conversationCandidate struct {
	path  string
	mtime time.Time
}

// listConversations returns the most-recently-updated conversations, capped at
// maxConversationList. It avoids parsing every conversation.json on disk: it
// walks the tree collecting only (path, mtime) per conversation, shortlists the
// newest listCandidateOverfetch by mtime, parses just those, then orders the
// survivors by the exact UpdatedAt field and applies the cap.
func listConversations(storage ConfigStorage) ([]ConversationSummary, error) {
	root := filepath.Join(storage.History, "conversations")
	candidates := []conversationCandidate{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || filepath.Base(path) != "conversation.json" {
			return nil
		}
		// Skip soft-deleted conversations without parsing: deleteConversation
		// always writes a sibling tombstone.json. This keeps freshly-deleted
		// conversations (whose mtime is bumped to "now") from crowding real
		// entries out of the mtime shortlist below.
		if _, statErr := os.Stat(filepath.Join(filepath.Dir(path), "tombstone.json")); statErr == nil {
			return nil
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			return infoErr
		}
		candidates = append(candidates, conversationCandidate{path: path, mtime: info.ModTime()})
		return nil
	})
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []ConversationSummary{}, nil
		}
		return nil, err
	}

	// Shortlist the newest candidates by mtime, then parse only those.
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].mtime.Equal(candidates[j].mtime) {
			return candidates[i].path < candidates[j].path
		}
		return candidates[i].mtime.After(candidates[j].mtime)
	})
	if len(candidates) > listCandidateOverfetch {
		candidates = candidates[:listCandidateOverfetch]
	}

	summaries := make([]ConversationSummary, 0, len(candidates))
	for _, candidate := range candidates {
		var conversation HistoryConversation
		if err := readJSONFile(candidate.path, &conversation); err != nil {
			return nil, err
		}
		// Belt-and-suspenders: a conversation may carry DeletedAt without a
		// tombstone (e.g. legacy records), so re-check after parsing.
		if conversation.DeletedAt != "" {
			continue
		}
		summaries = append(summaries, conversationSummaryFrom(conversation))
	}

	// Exact ordering by UpdatedAt (RFC3339 strings sort chronologically), then
	// apply the hard cap.
	sort.Slice(summaries, func(i, j int) bool {
		if summaries[i].UpdatedAt == summaries[j].UpdatedAt {
			return summaries[i].ID < summaries[j].ID
		}
		return summaries[i].UpdatedAt > summaries[j].UpdatedAt
	})
	if len(summaries) > maxConversationList {
		summaries = summaries[:maxConversationList]
	}
	return summaries, nil
}

func getConversation(storage ConfigStorage, conversationID string) (ConversationDetail, error) {
	conversationPath, err := findConversationPath(storage, conversationID)
	if err != nil {
		return ConversationDetail{}, err
	}
	return getConversationAt(conversationPath)
}

// getConversationAt is getConversation for a caller that already knows the
// conversation.json path (the library asset fold walks the tree and hands
// paths straight through, skipping the per-conversation find walk).
func getConversationAt(conversationPath string) (ConversationDetail, error) {
	var conversation HistoryConversation
	if err := readJSONFile(conversationPath, &conversation); err != nil {
		return ConversationDetail{}, err
	}
	if conversation.DeletedAt != "" {
		return ConversationDetail{}, fmt.Errorf("conversation %s is deleted", conversation.ID)
	}

	turnsDir := filepath.Join(filepath.Dir(conversationPath), "turns")
	turns := []HistoryTurn{}
	err := filepath.WalkDir(turnsDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || filepath.Ext(path) != ".json" {
			return nil
		}
		var turn HistoryTurn
		if err := readJSONFile(path, &turn); err != nil {
			return err
		}
		if turn.DeletedAt != "" {
			return nil
		}
		turn.Content = hydrateHistoryContent(filepath.Dir(conversationPath), turn.Content)
		turns = append(turns, turn)
		return nil
	})
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return ConversationDetail{}, err
		}
	}
	sort.Slice(turns, func(i, j int) bool {
		return turns[i].ID < turns[j].ID
	})
	return ConversationDetail{Conversation: conversation, Turns: turns}, nil
}

func deleteConversation(storage ConfigStorage, conversationID string) error {
	conversationPath, err := findConversationPath(storage, conversationID)
	if err != nil {
		return err
	}
	now := time.Now().Format(time.RFC3339)
	var conversation HistoryConversation
	if err := readJSONFile(conversationPath, &conversation); err != nil {
		return err
	}
	conversation.DeletedAt = now
	if err := writeJSONFile(conversationPath, conversation); err != nil {
		return err
	}
	tombstone := map[string]any{
		"schemaVersion":  1,
		"conversationId": conversationID,
		"deletedAt":      now,
		"reason":         "user_deleted",
	}
	return writeJSONFile(filepath.Join(filepath.Dir(conversationPath), "tombstone.json"), tombstone)
}

func purgeArchivedConversations(storage ConfigStorage) (PurgeArchivedResult, error) {
	root := filepath.Join(storage.History, "conversations")
	var archivedDirs []string
	deletedAssets := 0
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || filepath.Base(path) != "conversation.json" {
			return nil
		}
		var conversation HistoryConversation
		if err := readJSONFile(path, &conversation); err != nil {
			return err
		}
		if conversation.DeletedAt != "" {
			archivedDirs = append(archivedDirs, filepath.Dir(path))
			deletedAssets += countFiles(filepath.Join(filepath.Dir(path), "artifacts"))
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return PurgeArchivedResult{}, nil
		}
		return PurgeArchivedResult{}, err
	}

	for _, dir := range archivedDirs {
		if err := os.RemoveAll(dir); err != nil {
			return PurgeArchivedResult{}, err
		}
	}
	return PurgeArchivedResult{DeletedConversations: len(archivedDirs), DeletedAssets: deletedAssets}, nil
}

func countFiles(root string) int {
	count := 0
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return nil
		}
		count++
		return nil
	})
	return count
}

func updateConversationTitle(storage ConfigStorage, conversationID string, title string) (ConversationSummary, error) {
	conversationPath, err := findConversationPath(storage, conversationID)
	if err != nil {
		return ConversationSummary{}, err
	}

	var conversation HistoryConversation
	if err := readJSONFile(conversationPath, &conversation); err != nil {
		return ConversationSummary{}, err
	}
	if conversation.DeletedAt != "" {
		return ConversationSummary{}, fmt.Errorf("conversation %s is deleted", conversationID)
	}
	conversation.Title = normalizeConversationTitle(title, conversation.Title)
	conversation.UpdatedAt = time.Now().Format(time.RFC3339)
	if err := writeJSONFile(conversationPath, conversation); err != nil {
		return ConversationSummary{}, err
	}
	return conversationSummaryFrom(conversation), nil
}

func findConversationPath(storage ConfigStorage, conversationID string) (string, error) {
	root := filepath.Join(storage.History, "conversations")
	// Conversation directories are named by ID (conversationDir), so the
	// lookup normally resolves with one glob + one file read. The tree walk
	// below stays as the fallback for records whose directory name is not
	// their ID and for IDs containing glob metacharacters.
	if matches, globErr := filepath.Glob(filepath.Join(root, "*", "*", conversationID, "conversation.json")); globErr == nil && len(matches) > 0 {
		var conversation HistoryConversation
		if readErr := readJSONFile(matches[0], &conversation); readErr == nil && conversation.ID == conversationID {
			return matches[0], nil
		}
	}
	var found string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || filepath.Base(path) != "conversation.json" {
			return nil
		}
		var conversation HistoryConversation
		if err := readJSONFile(path, &conversation); err != nil {
			return err
		}
		if conversation.ID == conversationID {
			found = path
			return filepath.SkipAll
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if found == "" {
		return "", fmt.Errorf("conversation %s not found", conversationID)
	}
	return found, nil
}

func conversationDir(storage ConfigStorage, createdAt time.Time, conversationID string) string {
	return filepath.Join(
		storage.History,
		"conversations",
		createdAt.Format("2006"),
		createdAt.Format("01"),
		conversationID,
	)
}

func writeJSONFile(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tempPath := path + ".tmp"
	if err := os.WriteFile(tempPath, data, 0644); err != nil {
		return err
	}
	return os.Rename(tempPath, path)
}

func readJSONFile(path string, target any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, target)
}

// hydrateHistoryContent resolves image content items to URLs the frontend can
// render. Instead of embedding the full base64 data URL (which can be several
// MB for generated images), it sets a relative URL that the Wails asset
// handler serves from disk. This avoids pushing large payloads through the
// JSON IPC boundary.
func hydrateHistoryContent(conversationDir string, contents []HistoryContent) []HistoryContent {
	hydrated := make([]HistoryContent, 0, len(contents))
	for _, content := range contents {
		// Image, video, audio, transcript, markdown, composition, and comic
		// artifacts are all stored on disk and served by the asset handler;
		// resolve their path to an /atelier-artifact URL. Library-asset
		// references carry absolute paths; local artifacts are relative to the
		// conversation directory.
		if (content.Type == "image" || content.Type == "video" || content.Type == "audio" || content.Type == "transcript" || content.Type == "markdown" || content.Type == "composition" || content.Type == "comic") && content.Path != "" && !strings.HasPrefix(content.Path, "data:") {
			resolved := filepath.FromSlash(content.Path)
			if !filepath.IsAbs(resolved) {
				resolved = filepath.Join(conversationDir, resolved)
			}
			if _, err := os.Stat(resolved); err == nil {
				content.Text = "/atelier-artifact" + resolved
			}
		}
		hydrated = append(hydrated, content)
	}
	return hydrated
}

func randomID(prefix string) string {
	var bytes [12]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
	}
	return prefix + "_" + hex.EncodeToString(bytes[:])
}

func titleFromPrompt(prompt string) string {
	prompt = strings.Join(strings.Fields(prompt), " ")
	if prompt == "" {
		return "Untitled"
	}
	return compactString(prompt, 72)
}

func cleanGeneratedTitle(title string) string {
	title = strings.TrimSpace(title)
	title = strings.TrimPrefix(title, "#")
	title = strings.TrimSpace(title)
	for _, prefix := range []string{"Title:", "title:", "Conversation title:", "conversation title:"} {
		title = strings.TrimSpace(strings.TrimPrefix(title, prefix))
	}
	title = strings.Trim(title, "\"'`“”‘’")
	title = strings.Join(strings.Fields(title), " ")
	return compactString(title, 72)
}

func normalizeConversationTitle(title string, fallback string) string {
	title = cleanGeneratedTitle(title)
	if title != "" {
		return title
	}
	return titleFromPrompt(fallback)
}
