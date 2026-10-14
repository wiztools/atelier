package main

// The subtitle editor's Go surface: resolve a trc_ artifact into editable
// cues, and export an edited cue set to disk. Deliberately smaller than the
// media editors — edits live in the editor (local until export, per the HITL
// model), so there is no edit session, no op ledger, and nothing here ever
// writes to the conversation. subtitle.go owns the format logic; this file is
// the binding layer around it.

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// SubtitleCue is one editable caption block on the wire — seconds since the
// start, and the caption text (multi-line, tags included).
type SubtitleCue struct {
	StartSeconds float64 `json:"startSeconds"`
	EndSeconds   float64 `json:"endSeconds"`
	Text         string  `json:"text"`
}

// SubtitleSourceInfo is what the editor loads: the parsed cues plus the
// identity the linked-media picker and reply chips need. Format is detected
// from CONTENT (parseSubtitles), never from the file extension.
type SubtitleSourceInfo struct {
	ConversationID    string        `json:"conversationId"`
	ConversationTitle string        `json:"conversationTitle,omitempty"`
	ArtifactID        string        `json:"artifactId"`
	URL               string        `json:"url"`
	MimeType          string        `json:"mimeType,omitempty"`
	Format            string        `json:"format"`
	Cues              []SubtitleCue `json:"cues"`
	Warnings          []string      `json:"warnings,omitempty"`
}

// subtitleCueWarnings renders the review checklist the editor surfaces: the
// deterministic defects a human reviewer looks for. Shared by the source
// resolution (fresh from disk) and kept semantically in step with the
// frontend's subtitleModel copy.
func subtitleCueWarnings(cues []SubtitleCue) []string {
	warnings := []string{}
	for i, cue := range cues {
		label := fmt.Sprintf("cue %d", i+1)
		if cue.EndSeconds < cue.StartSeconds {
			warnings = append(warnings, fmt.Sprintf("%s: end is before its start", label))
			continue
		}
		if cue.StartSeconds < 0 {
			warnings = append(warnings, fmt.Sprintf("%s: negative start time", label))
		}
		duration := cue.EndSeconds - cue.StartSeconds
		runes := len([]rune(strings.TrimSpace(cue.Text)))
		if duration > 0 && float64(runes)/duration > 20 {
			warnings = append(warnings, fmt.Sprintf("%s: reading speed is high (%d chars in %.1fs)", label, runes, duration))
		}
		if duration >= 0 && duration < 0.5 {
			warnings = append(warnings, fmt.Sprintf("%s: shorter than half a second", label))
		}
		if i > 0 && cue.StartSeconds < cues[i-1].EndSeconds {
			warnings = append(warnings, fmt.Sprintf("%s: overlaps the previous cue", label))
		}
	}
	return warnings
}

// resolveSubtitleContent finds the subtitle content entry an editor open
// names, plus the conversation title for provenance labels. artifactID is
// exact; the entry must be a transcript artifact carrying VTT/SRT content —
// plain .txt transcripts are not subtitle files.
func resolveSubtitleContent(storage ConfigStorage, conversationID, artifactID string) (*HistoryContent, string, error) {
	artifactID = strings.TrimSpace(artifactID)
	if artifactID == "" {
		return nil, "", errors.New("subtitle artifact id is empty")
	}
	detail, err := getConversation(storage, conversationID)
	if err != nil {
		return nil, "", err
	}
	for i := len(detail.Turns) - 1; i >= 0; i-- {
		for j := range detail.Turns[i].Content {
			content := detail.Turns[i].Content[j]
			if content.ArtifactID == artifactID && subtitleArtifactExtension(content) != "" {
				return &content, detail.Conversation.Title, nil
			}
		}
	}
	return nil, "", fmt.Errorf("no subtitle artifact with id %q in this conversation", artifactID)
}

// ResolveSubtitleEditSource loads a subtitle artifact into the editor: the
// parsed cues, the content-detected format, and the identity the UI needs
// (reply chips, linked-media default). Read-only — nothing here mutates the
// conversation, matching the editor's local-until-export model.
func (a *App) ResolveSubtitleEditSource(conversationID, artifactID string) (SubtitleSourceInfo, error) {
	config, err := loadReadyConfig()
	if err != nil {
		return SubtitleSourceInfo{}, err
	}
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return SubtitleSourceInfo{}, errors.New("subtitle editing opens a conversation asset, and no conversation was given")
	}
	content, title, err := resolveSubtitleContent(config.Storage, conversationID, artifactID)
	if err != nil {
		return SubtitleSourceInfo{}, err
	}
	path, err := contentArtifactPath(config.Storage, conversationID, *content)
	if err != nil {
		return SubtitleSourceInfo{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return SubtitleSourceInfo{}, fmt.Errorf("reading the subtitle artifact failed: %w", err)
	}
	cues, format, _, err := parseSubtitles(data)
	if err != nil {
		return SubtitleSourceInfo{}, err
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		absPath = path
	}
	wire := make([]SubtitleCue, len(cues))
	for i, cue := range cues {
		wire[i] = SubtitleCue{StartSeconds: cue.Start, EndSeconds: cue.End, Text: cue.Text}
	}
	info := SubtitleSourceInfo{
		ConversationID:    conversationID,
		ConversationTitle: title,
		ArtifactID:        content.ArtifactID,
		URL:               artifactPrefix + absPath,
		MimeType:          content.MimeType,
		Format:            format,
		Cues:              wire,
		Warnings:          subtitleCueWarnings(wire),
	}
	return info, nil
}

// renderSubtitlesForSave is the save path's pure half: validate the cue doc
// and render it in the requested format. Returns the bytes and the file
// extension the artifact lands with.
func renderSubtitlesForSave(format string, wire []SubtitleCue) ([]byte, string, error) {
	format = strings.ToLower(strings.TrimSpace(format))
	if format != subtitleFormatVTT && format != subtitleFormatSRT {
		return nil, "", fmt.Errorf(`subtitle format must be "vtt" or "srt", got %q`, format)
	}
	if len(wire) == 0 {
		return nil, "", errors.New("no cues to save")
	}
	for i, cue := range wire {
		if math.IsNaN(cue.StartSeconds) || math.IsNaN(cue.EndSeconds) || math.IsInf(cue.StartSeconds, 0) || math.IsInf(cue.EndSeconds, 0) {
			return nil, "", fmt.Errorf("cue %d has a non-finite timestamp", i+1)
		}
		if cue.EndSeconds < cue.StartSeconds {
			return nil, "", fmt.Errorf("cue %d ends before it starts", i+1)
		}
	}
	cues := make([]subtitleCue, len(wire))
	for i, cue := range wire {
		cues[i] = subtitleCue{Start: cue.StartSeconds, End: cue.EndSeconds, Text: cue.Text}
	}
	var rendered string
	if format == subtitleFormatSRT {
		rendered = renderSubtitlesSRT(cues)
	} else {
		rendered = renderSubtitlesVTT(cues)
	}
	extension := map[string]string{subtitleFormatVTT: ".vtt", subtitleFormatSRT: ".srt"}[format]
	return []byte(rendered), extension, nil
}

// SaveSubtitleEditRequest carries one commit of the editor's cue doc back
// into its conversation — the middle door between local edits and disk
// export: a new trc_ artifact plus a small assistant turn recording the edit.
// The source artifact is provenance for the turn's summary; the original
// file is never touched.
type SaveSubtitleEditRequest struct {
	ConversationID   string        `json:"conversationId"`
	SourceArtifactID string        `json:"sourceArtifactId,omitempty"`
	Format           string        `json:"format"`
	Cues             []SubtitleCue `json:"cues"`
}

// SaveSubtitleEditResult names what the save produced: the new artifact's
// identity (the reply chips, the assets panel, and a later editor open all
// read it) plus what was written.
type SaveSubtitleEditResult struct {
	ArtifactID string `json:"artifactId"`
	URL        string `json:"url"`
	Format     string `json:"format"`
	CueCount   int    `json:"cueCount"`
}

// SaveSubtitleEdit commits the edited cue doc to the conversation: the
// rendered file lands as a fresh trc_<hex>.<ext> transcript artifact and a
// short assistant turn records the edit (source, cue count, target format).
// The machine draft stays intact — every save is additive, mirroring how the
// media editors never overwrite their sources. Refused while the
// conversation streams, like every other sanctioned rewrite.
func (a *App) SaveSubtitleEdit(req SaveSubtitleEditRequest) (SaveSubtitleEditResult, error) {
	// The render half validates and serializes; the extension it reports is
	// already baked into the artifact path by writeSubtitleAttachment.
	data, _, err := renderSubtitlesForSave(req.Format, req.Cues)
	if err != nil {
		return SaveSubtitleEditResult{}, err
	}
	format := strings.ToLower(strings.TrimSpace(req.Format))
	conversationID := strings.TrimSpace(req.ConversationID)
	if conversationID == "" {
		return SaveSubtitleEditResult{}, errors.New("saving subtitles to a conversation requires a conversation id")
	}
	a.streamsMu.Lock()
	for _, active := range a.streamConversations {
		if active == conversationID {
			a.streamsMu.Unlock()
			return SaveSubtitleEditResult{}, errors.New("the conversation is still streaming — wait for the turn to finish before saving")
		}
	}
	a.streamsMu.Unlock()

	config, err := loadReadyConfig()
	if err != nil {
		return SaveSubtitleEditResult{}, err
	}
	store := newHistoryStore(config.Storage)
	loaded, err := store.loadForAppend(conversationID, "chat", "a chat", config.Tools.Filesystem.Root)
	if err != nil {
		return SaveSubtitleEditResult{}, err
	}
	content, err := writeSubtitleAttachment(loaded.ArtifactsDir, data)
	if err != nil {
		return SaveSubtitleEditResult{}, err
	}
	source := strings.TrimSpace(req.SourceArtifactID)
	if source == "" {
		source = "subtitles"
	}
	nowText := time.Now().Format(time.RFC3339)
	summary := fmt.Sprintf("Edited subtitles from %s · %d cues · saved as %s", source, len(req.Cues), strings.ToUpper(format))
	assistantTurn := buildChatAssistantTurn(conversationID, loaded.NextTurnNumber, nowText, summary, "", "", "", "", 0, HarnessRun{}, "")
	assistantTurn.Content = append(assistantTurn.Content, content)

	loaded.Conversation.UpdatedAt = nowText
	loaded.Conversation.Stats.TurnCount++
	loaded.Conversation.Stats.ArtifactCount++
	if err := store.writeConversation(loaded.Path, loaded.Conversation); err != nil {
		return SaveSubtitleEditResult{}, err
	}
	if err := store.writeTurn(loaded.TurnsDir, assistantTurn); err != nil {
		return SaveSubtitleEditResult{}, err
	}
	absArtifactsDir, err := filepath.Abs(loaded.ArtifactsDir)
	if err != nil {
		absArtifactsDir = loaded.ArtifactsDir
	}
	return SaveSubtitleEditResult{
		ArtifactID: content.ArtifactID,
		URL:        artifactPrefix + filepath.Join(absArtifactsDir, filepath.Base(content.Path)),
		Format:     format,
		CueCount:   len(req.Cues),
	}, nil
}
