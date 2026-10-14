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

	"github.com/wailsapp/wails/v2/pkg/runtime"
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

// ExportSubtitlesRequest carries one editor export: the current cue doc and
// the target format (free choice — a VTT-derived doc can export as SRT).
type ExportSubtitlesRequest struct {
	Format        string        `json:"format"`
	Cues          []SubtitleCue `json:"cues"`
	SuggestedName string        `json:"suggestedName,omitempty"`
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

// ExportSubtitles renders the editor's current cue doc in the requested
// format and opens the save dialog. The conversation is not touched — export
// is client-state to disk, the one door the local-until-export model has.
func (a *App) ExportSubtitles(req ExportSubtitlesRequest) (string, error) {
	data, extension, err := renderSubtitlesExport(req)
	if err != nil {
		return "", err
	}
	return a.saveArtifactDialog(data, req.SuggestedName, "atelier-subtitles", extension, "Save subtitles", []runtime.FileFilter{
		{DisplayName: "Subtitle Files", Pattern: "*.vtt;*.srt"},
		{DisplayName: "All Files", Pattern: "*.*"},
	})
}

// renderSubtitlesExport is ExportSubtitles' pure half: validate the doc and
// render it in the requested format. Returns the bytes and the file
// extension the dialog must enforce.
func renderSubtitlesExport(req ExportSubtitlesRequest) ([]byte, string, error) {
	format := strings.ToLower(strings.TrimSpace(req.Format))
	if format != subtitleFormatVTT && format != subtitleFormatSRT {
		return nil, "", fmt.Errorf(`export format must be "vtt" or "srt", got %q`, req.Format)
	}
	if len(req.Cues) == 0 {
		return nil, "", errors.New("no cues to export")
	}
	for i, cue := range req.Cues {
		if math.IsNaN(cue.StartSeconds) || math.IsNaN(cue.EndSeconds) || math.IsInf(cue.StartSeconds, 0) || math.IsInf(cue.EndSeconds, 0) {
			return nil, "", fmt.Errorf("cue %d has a non-finite timestamp", i+1)
		}
		if cue.EndSeconds < cue.StartSeconds {
			return nil, "", fmt.Errorf("cue %d ends before it starts", i+1)
		}
	}
	cues := make([]subtitleCue, len(req.Cues))
	for i, cue := range req.Cues {
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
