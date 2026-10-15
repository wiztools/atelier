package main

// The markdown editor's Go surface — the subtitle editor's document sibling:
// resolve a md_ artifact into the editor's text, and commit an edited doc
// back into its conversation as a NEW artifact (the source is never
// overwritten, mirroring every other editor). Deliberately session-free and
// renderer-free: the source IS the artifact, so nothing is parsed or
// validated beyond "is text" — the review checklist (outline, warnings,
// stats) lives in the frontend's markdownModel.ts against the same bytes.

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// markdownArtifactExtension reports ".md" when a history content entry is a
// markdown artifact. The path extension is the direct identity
// (writeMarkdownAttachment always lands .md); the mime mapping covers any
// entry whose path is missing — the same two-step read as
// subtitleArtifactExtension.
func markdownArtifactExtension(content HistoryContent) string {
	if content.Type != "markdown" {
		return ""
	}
	if strings.ToLower(filepath.Ext(content.Path)) == ".md" {
		return ".md"
	}
	if content.MimeType == "text/markdown" {
		return ".md"
	}
	return ""
}

// markdownWordCount is the one fact the save path's summary turn states, kept
// in step with the frontend's markdownModel: whitespace-delimited fields.
func markdownWordCount(text string) int {
	return len(strings.Fields(text))
}

// writeMarkdownAttachment persists markdown bytes as a md_<hex>.md artifact —
// the identity the editor opens, the chat chips render, and the assets panel
// lists. Validation is loose (non-empty, valid UTF-8): unlike subtitles there
// is no format to sniff, and every attachment normalizes to the .md spelling
// because the data-URL mime (text/markdown) cannot distinguish .markdown.
func writeMarkdownAttachment(artifactsDir string, data []byte) (HistoryContent, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return HistoryContent{}, errors.New("markdown document is empty")
	}
	if !utf8.Valid(data) {
		return HistoryContent{}, errors.New("markdown document is not UTF-8 text")
	}
	artifactID := randomID("md")
	filename := artifactID + ".md"
	if err := os.WriteFile(filepath.Join(artifactsDir, filename), data, 0644); err != nil {
		return HistoryContent{}, err
	}
	return HistoryContent{
		Type:       "markdown",
		ArtifactID: artifactID,
		Path:       filepath.ToSlash(filepath.Join("artifacts", filename)),
		MimeType:   mediaTypeForExtension(".md"),
	}, nil
}

// MarkdownSourceInfo is what the editor loads: the document text plus the
// identity the reply chips and assets panel need. Unlike the subtitle
// resolve, there are no cues or server-computed warnings to carry — the text
// is the whole document, and the editor derives its review checklist from it.
type MarkdownSourceInfo struct {
	ConversationID    string `json:"conversationId"`
	ConversationTitle string `json:"conversationTitle,omitempty"`
	ArtifactID        string `json:"artifactId"`
	URL               string `json:"url"`
	MimeType          string `json:"mimeType,omitempty"`
	Text              string `json:"text"`
	WordCount         int    `json:"wordCount"`
}

// resolveMarkdownContent finds the markdown content entry an editor open
// names, plus the conversation title for provenance labels. artifactID is
// exact; the entry must be a markdown artifact — plain .txt transcripts and
// subtitle files are not editor material.
func resolveMarkdownContent(storage ConfigStorage, conversationID, artifactID string) (*HistoryContent, string, error) {
	artifactID = strings.TrimSpace(artifactID)
	if artifactID == "" {
		return nil, "", errors.New("markdown artifact id is empty")
	}
	detail, err := getConversation(storage, conversationID)
	if err != nil {
		return nil, "", err
	}
	for i := len(detail.Turns) - 1; i >= 0; i-- {
		for j := range detail.Turns[i].Content {
			content := detail.Turns[i].Content[j]
			if content.ArtifactID == artifactID && markdownArtifactExtension(content) != "" {
				return &content, detail.Conversation.Title, nil
			}
		}
	}
	return nil, "", fmt.Errorf("no markdown artifact with id %q in this conversation", artifactID)
}

// ResolveMarkdownEditSource loads a markdown artifact into the editor: the
// text plus the identity the UI needs. Read-only — nothing here mutates the
// conversation, matching the editor's local-until-save model.
func (a *App) ResolveMarkdownEditSource(conversationID, artifactID string) (MarkdownSourceInfo, error) {
	config, err := loadReadyConfig()
	if err != nil {
		return MarkdownSourceInfo{}, err
	}
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return MarkdownSourceInfo{}, errors.New("markdown editing opens a conversation asset, and no conversation was given")
	}
	content, title, err := resolveMarkdownContent(config.Storage, conversationID, artifactID)
	if err != nil {
		return MarkdownSourceInfo{}, err
	}
	path, err := contentArtifactPath(config.Storage, conversationID, *content)
	if err != nil {
		return MarkdownSourceInfo{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return MarkdownSourceInfo{}, fmt.Errorf("reading the markdown artifact failed: %w", err)
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		absPath = path
	}
	info := MarkdownSourceInfo{
		ConversationID:    conversationID,
		ConversationTitle: title,
		ArtifactID:        content.ArtifactID,
		URL:               artifactPrefix + absPath,
		MimeType:          content.MimeType,
		Text:              string(data),
		WordCount:         markdownWordCount(string(data)),
	}
	return info, nil
}

// SaveMarkdownEditRequest carries one commit of the editor's document back
// into its conversation — the same single door the subtitle editor has: a new
// md_ artifact plus a small assistant turn recording the edit. The source
// artifact is provenance for the turn's summary; the original file is never
// touched.
type SaveMarkdownEditRequest struct {
	ConversationID   string `json:"conversationId"`
	SourceArtifactID string `json:"sourceArtifactId,omitempty"`
	Text             string `json:"text"`
}

// SaveMarkdownEditResult names what the save produced: the new artifact's
// identity (the reply chips, the assets panel, and a later editor open all
// read it) plus what was written.
type SaveMarkdownEditResult struct {
	ArtifactID string `json:"artifactId"`
	URL        string `json:"url"`
	WordCount  int    `json:"wordCount"`
}

// SaveMarkdownEdit commits the edited document to the conversation: the text
// lands as a fresh md_<hex>.md artifact and a short assistant turn records
// the edit (source, word count). The editor's draft stays intact — every save
// is additive, mirroring how the other editors never overwrite their sources.
// Refused while the conversation streams, like every other sanctioned rewrite.
func (a *App) SaveMarkdownEdit(req SaveMarkdownEditRequest) (SaveMarkdownEditResult, error) {
	conversationID := strings.TrimSpace(req.ConversationID)
	if conversationID == "" {
		return SaveMarkdownEditResult{}, errors.New("saving a document to a conversation requires a conversation id")
	}
	a.streamsMu.Lock()
	for _, active := range a.streamConversations {
		if active == conversationID {
			a.streamsMu.Unlock()
			return SaveMarkdownEditResult{}, errors.New("the conversation is still streaming — wait for the turn to finish before saving")
		}
	}
	a.streamsMu.Unlock()

	config, err := loadReadyConfig()
	if err != nil {
		return SaveMarkdownEditResult{}, err
	}
	store := newHistoryStore(config.Storage)
	loaded, err := store.loadForAppend(conversationID, "chat", "a chat", config.Tools.Filesystem.Root)
	if err != nil {
		return SaveMarkdownEditResult{}, err
	}
	content, err := writeMarkdownAttachment(loaded.ArtifactsDir, []byte(req.Text))
	if err != nil {
		return SaveMarkdownEditResult{}, err
	}
	source := strings.TrimSpace(req.SourceArtifactID)
	if source == "" {
		source = "markdown"
	}
	words := markdownWordCount(req.Text)
	nowText := time.Now().Format(time.RFC3339)
	summary := fmt.Sprintf("Edited document from %s · %d words · saved as Markdown", source, words)
	assistantTurn := buildChatAssistantTurn(conversationID, loaded.NextTurnNumber, nowText, summary, "", "", "", "", 0, HarnessRun{}, "")
	assistantTurn.Content = append(assistantTurn.Content, content)

	loaded.Conversation.UpdatedAt = nowText
	loaded.Conversation.Stats.TurnCount++
	loaded.Conversation.Stats.ArtifactCount++
	if err := store.writeConversation(loaded.Path, loaded.Conversation); err != nil {
		return SaveMarkdownEditResult{}, err
	}
	if err := store.writeTurn(loaded.TurnsDir, assistantTurn); err != nil {
		return SaveMarkdownEditResult{}, err
	}
	absArtifactsDir, err := filepath.Abs(loaded.ArtifactsDir)
	if err != nil {
		absArtifactsDir = loaded.ArtifactsDir
	}
	return SaveMarkdownEditResult{
		ArtifactID: content.ArtifactID,
		URL:        artifactPrefix + filepath.Join(absArtifactsDir, filepath.Base(content.Path)),
		WordCount:  words,
	}, nil
}

// SaveMarkdown copies a markdown artifact to a user-chosen location — the
// document sibling of SaveTranscript: the same non-empty-text guard (there is
// no container magic to verify) and dialog ritual, with the .md filter.
func (a *App) SaveMarkdown(req SaveTranscriptRequest) (string, error) {
	sourcePath := strings.TrimSpace(req.Path)
	sourcePath = strings.TrimPrefix(sourcePath, artifactPrefix)
	if sourcePath == "" {
		return "", errors.New("markdown path is empty")
	}
	data, err := os.ReadFile(sourcePath)
	if err != nil {
		return "", err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return "", errors.New("markdown artifact is empty")
	}
	extension := strings.ToLower(filepath.Ext(sourcePath))
	if extension != ".md" {
		extension = ".md"
	}
	return a.saveArtifactDialog(data, req.SuggestedName, "atelier-document", extension, "Save document", []runtime.FileFilter{
		{DisplayName: "Markdown Files", Pattern: "*.md"},
		{DisplayName: "All Files", Pattern: "*.*"},
	})
}
