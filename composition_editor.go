package main

// The video composer's Go surface — the third session-free editor beside
// subtitles and markdown: resolve a cmp_ artifact into the editor's document,
// resolve and measure one media artifact into a ref the picker can add, seed
// a draft from referenced media (the "open the video composer" intent's
// seeded-only contract), and commit an edited document back into its
// conversation as a NEW artifact. The source is never overwritten; every save
// is a revision. Nothing here renders — composition_render.go owns the
// ffmpeg side, and a draft that was never saved never touches the
// conversation, which is what lets a mid-stream editor launch open the
// workspace without writing a turn into a streaming conversation.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// compositionProbeTimeout bounds the measurement of one media ref — ffprobe
// on a local file is fast, and a ref that cannot be measured in this window
// is better refused than left hanging the picker.
const compositionProbeTimeout = 60 * time.Second

// CompositionSourceInfo is what the editor loads: the document plus the
// identity the reply chips and the picker's provenance labels need. For a
// seeded draft ArtifactID and URL are empty — nothing is persisted until the
// first save — and the EditorHeader shows its "unsaved draft" breadcrumb.
// MissingRefs names the referenced artifacts whose files are gone on disk
// (PR-06, identify-only): the timeline keeps them, flagged; a render refuses
// while any remain.
type CompositionSourceInfo struct {
	ConversationID    string         `json:"conversationId"`
	ConversationTitle string         `json:"conversationTitle,omitempty"`
	ArtifactID        string         `json:"artifactId,omitempty"`
	URL               string         `json:"url,omitempty"`
	MimeType          string         `json:"mimeType,omitempty"`
	Doc               CompositionDoc `json:"doc"`
	MissingRefs       []string       `json:"missingRefs"`
	Warnings          []string       `json:"warnings"`
}

// resolveCompositionContent finds the composition content entry an editor
// open names, plus the conversation title for provenance labels. artifactID
// is exact; the entry must be a composition artifact.
func resolveCompositionContent(storage ConfigStorage, conversationID, artifactID string) (*HistoryContent, string, error) {
	artifactID = strings.TrimSpace(artifactID)
	if artifactID == "" {
		return nil, "", errors.New("composition artifact id is empty")
	}
	detail, err := getConversation(storage, conversationID)
	if err != nil {
		return nil, "", err
	}
	for i := len(detail.Turns) - 1; i >= 0; i-- {
		for j := range detail.Turns[i].Content {
			content := detail.Turns[i].Content[j]
			if content.ArtifactID == artifactID && compositionArtifactExtension(content) != "" {
				return &content, detail.Conversation.Title, nil
			}
		}
	}
	return nil, "", fmt.Errorf("no composition artifact with id %q in this conversation", artifactID)
}

// ResolveCompositionEditSource loads a saved composition revision into the
// editor. Read-only — nothing here mutates the conversation. The document
// is validated on the way in, so a hand-edited artifact that drifted from
// the rules fails here with the rule named rather than inside the editor.
func (a *App) ResolveCompositionEditSource(conversationID, artifactID string) (CompositionSourceInfo, error) {
	config, err := loadReadyConfig()
	if err != nil {
		return CompositionSourceInfo{}, err
	}
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return CompositionSourceInfo{}, errors.New("the video composer opens a conversation asset, and no conversation was given")
	}
	content, title, err := resolveCompositionContent(config.Storage, conversationID, artifactID)
	if err != nil {
		return CompositionSourceInfo{}, err
	}
	path, err := contentArtifactPath(config.Storage, conversationID, *content)
	if err != nil {
		return CompositionSourceInfo{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return CompositionSourceInfo{}, fmt.Errorf("reading the composition artifact failed: %w", err)
	}
	doc, err := parseCompositionDoc(data)
	if err != nil {
		return CompositionSourceInfo{}, err
	}
	if err := validateCompositionDoc(&doc); err != nil {
		return CompositionSourceInfo{}, fmt.Errorf("the saved composition is not valid: %w", err)
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		absPath = path
	}
	return CompositionSourceInfo{
		ConversationID:    conversationID,
		ConversationTitle: title,
		ArtifactID:        content.ArtifactID,
		URL:               artifactPrefix + absPath,
		MimeType:          content.MimeType,
		Doc:               doc,
		MissingRefs:       compositionMissingRefs(doc),
		Warnings:          []string{},
	}, nil
}

// compositionMediaRefFor resolves an artifact id into an unmeasured ref —
// conversation-locally first, then across the conversation's library when it
// belongs to a project (the picker's dual scope, and resolveReferencedAssets'
// widening rule). Only video and audio artifacts qualify; the file must exist
// on disk, since a ref that is missing at add time is a mistake rather than a
// later loss.
func compositionMediaRefFor(storage ConfigStorage, conversationID, artifactID string) (CompositionMediaRef, error) {
	artifactID = strings.TrimSpace(artifactID)
	if artifactID == "" {
		return CompositionMediaRef{}, errors.New("media artifact id is empty")
	}
	detail, err := getConversation(storage, conversationID)
	if err != nil {
		return CompositionMediaRef{}, err
	}
	var ref CompositionMediaRef
	if content, ok := assetContentIndex(detail)[artifactID]; ok {
		if content.Type != "video" && content.Type != "audio" {
			return CompositionMediaRef{}, fmt.Errorf("%s is %s media — the composer takes video and audio", artifactID, compositionRefKindPhrase(content.Type))
		}
		path, err := contentArtifactPath(storage, conversationID, content)
		if err != nil {
			return CompositionMediaRef{}, err
		}
		ref = CompositionMediaRef{
			ArtifactID:     content.ArtifactID,
			ConversationID: conversationID,
			Path:           path,
			MimeType:       content.MimeType,
			Kind:           content.Type,
		}
		if ref.ArtifactID == "" {
			ref.ArtifactID = artifactID
		}
	} else {
		projectID := detail.Conversation.ProjectID
		if projectID == "" {
			return CompositionMediaRef{}, fmt.Errorf("no video or audio asset with id %q in this conversation", artifactID)
		}
		lookup, err := libraryAssetLookup(storage, projectID)
		if err != nil {
			return CompositionMediaRef{}, err
		}
		asset, found := lookup[artifactID]
		if !found {
			return CompositionMediaRef{}, fmt.Errorf("no video or audio asset with id %q in this conversation or its library", artifactID)
		}
		if asset.Kind != "video" && asset.Kind != "audio" {
			return CompositionMediaRef{}, fmt.Errorf("%s is %s media — the composer takes video and audio", artifactID, compositionRefKindPhrase(asset.Kind))
		}
		if asset.URL == "" {
			return CompositionMediaRef{}, fmt.Errorf("the file for %s is missing on disk", artifactID)
		}
		ref = CompositionMediaRef{
			ArtifactID:     asset.ID,
			ConversationID: asset.ConversationID,
			Path:           strings.TrimPrefix(asset.URL, artifactPrefix),
			MimeType:       asset.MimeType,
			Kind:           asset.Kind,
		}
	}
	absPath, err := filepath.Abs(ref.Path)
	if err == nil {
		ref.Path = absPath
	}
	if _, err := os.Stat(ref.Path); err != nil {
		return CompositionMediaRef{}, fmt.Errorf("the file for %s is missing on disk", ref.ArtifactID)
	}
	return ref, nil
}

// probeCompositionMediaRef fills a ref's facts snapshot: ffprobe when it
// resolves, else the pure-Go container sniffs (MP4 boxes for video, the
// WAV/MP3/FLAC/OGG/M4A headers for audio) — the join tools' measurement
// ladder. A clip the ladder cannot measure is refused: the timeline cannot
// lay out a clip of unknown length, and guessing would make every later
// trim wrong.
func probeCompositionMediaRef(ctx context.Context, config AppConfig, ref CompositionMediaRef) (CompositionMediaRef, error) {
	artifactURL := artifactPrefix + ref.Path
	if ref.Kind == "audio" {
		fact, ok := joinAudioClipFactFor(ctx, config, ref.Path, artifactURL)
		if !ok || fact.Duration <= 0 {
			return CompositionMediaRef{}, fmt.Errorf("could not measure %s — the composer needs its duration; install ffmpeg (ffprobe) to add this format", ref.ArtifactID)
		}
		ref.DurationSeconds = fact.Duration
		ref.HasAudio = true
		return ref, nil
	}
	fact, ok := joinClipFactFor(ctx, config, ref.Path, artifactURL)
	if !ok || fact.Duration <= 0 {
		return CompositionMediaRef{}, fmt.Errorf("could not measure %s — the composer needs its duration and frame size; install ffmpeg (ffprobe) to add this format", ref.ArtifactID)
	}
	ref.DurationSeconds = fact.Duration
	ref.Width = fact.Width
	ref.Height = fact.Height
	ref.FPS = fact.FPS
	ref.HasAudio = fact.HasAudio
	return ref, nil
}

// resolveCompositionMedia is the shared resolve-and-measure the picker's add
// and the seed path both run.
func resolveCompositionMedia(config AppConfig, conversationID, artifactID string) (CompositionMediaRef, error) {
	ref, err := compositionMediaRefFor(config.Storage, conversationID, artifactID)
	if err != nil {
		return CompositionMediaRef{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), compositionProbeTimeout)
	defer cancel()
	return probeCompositionMediaRef(ctx, config, ref)
}

// ResolveCompositionMedia resolves and measures one video or audio artifact
// into a ref the editor appends to its document — the picker's add path.
// conversationID is the composition's OWNING conversation (its picker scope),
// not necessarily the artifact's: a library sibling's asset resolves through
// the widening rule and carries its own conversation on the ref.
func (a *App) ResolveCompositionMedia(conversationID, artifactID string) (CompositionMediaRef, error) {
	config, err := loadReadyConfig()
	if err != nil {
		return CompositionMediaRef{}, err
	}
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return CompositionMediaRef{}, errors.New("adding media to a composition requires its conversation id")
	}
	return resolveCompositionMedia(config, conversationID, artifactID)
}

// SeedCompositionRequest asks for a fresh draft seeded with referenced media
// — the editor-launch intent's payload and the "New composition" action's
// (with no ids). ArtifactIDs are in reference order, which IS the seeded
// order.
type SeedCompositionRequest struct {
	ConversationID string   `json:"conversationId"`
	Name           string   `json:"name,omitempty"`
	ArtifactIDs    []string `json:"artifactIds"`
}

// SeedComposition builds an unsaved draft from referenced media, in order —
// the seeded-only contract: the result is indistinguishable from the user
// having picked those assets from the picker in that order. Read-only;
// nothing is persisted until the editor's first save. An id that will not
// resolve or measure is skipped with a warning rather than failing the open
// (the picker is right there to add it by hand), so the workspace always
// opens.
func (a *App) SeedComposition(req SeedCompositionRequest) (CompositionSourceInfo, error) {
	config, err := loadReadyConfig()
	if err != nil {
		return CompositionSourceInfo{}, err
	}
	conversationID := strings.TrimSpace(req.ConversationID)
	if conversationID == "" {
		return CompositionSourceInfo{}, errors.New("a composition belongs to a conversation, and no conversation was given")
	}
	detail, err := getConversation(config.Storage, conversationID)
	if err != nil {
		return CompositionSourceInfo{}, err
	}
	refs := make([]CompositionMediaRef, 0, len(req.ArtifactIDs))
	warnings := []string{}
	for _, id := range req.ArtifactIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		ref, err := resolveCompositionMedia(config, conversationID, id)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("Skipped %s: %v", id, err))
			continue
		}
		refs = append(refs, ref)
	}
	doc := seedCompositionDoc(req.Name, refs)
	if err := validateCompositionDoc(&doc); err != nil {
		return CompositionSourceInfo{}, err
	}
	return CompositionSourceInfo{
		ConversationID:    conversationID,
		ConversationTitle: detail.Conversation.Title,
		Doc:               doc,
		MissingRefs:       []string{},
		Warnings:          warnings,
	}, nil
}

// SaveCompositionEditRequest carries one commit of the editor's document back
// into its conversation — the single door the other editors have: a new
// cmp_ artifact plus a small assistant turn recording the save. The source
// artifact is provenance for the turn's summary; the original is never
// touched. Saving under a new name from an older revision IS duplication.
type SaveCompositionEditRequest struct {
	ConversationID   string         `json:"conversationId"`
	SourceArtifactID string         `json:"sourceArtifactId,omitempty"`
	Doc              CompositionDoc `json:"doc"`
}

// SaveCompositionEditResult names what the save produced: the new artifact's
// identity (the reply chips, the assets panel, a later open, and an export
// all read it) plus the normalized document, so the editor re-baselines on
// exactly what landed.
type SaveCompositionEditResult struct {
	ArtifactID string         `json:"artifactId"`
	URL        string         `json:"url"`
	Doc        CompositionDoc `json:"doc"`
	Summary    string         `json:"summary"`
}

// SaveCompositionEdit commits the edited document to the conversation: the
// document lands as a fresh cmp_<hex>.json artifact and a short assistant
// turn records the save (name, clip count, lanes, length). Every save is
// additive. Refused while the conversation streams, like every other
// sanctioned rewrite.
func (a *App) SaveCompositionEdit(req SaveCompositionEditRequest) (SaveCompositionEditResult, error) {
	conversationID := strings.TrimSpace(req.ConversationID)
	if conversationID == "" {
		return SaveCompositionEditResult{}, errors.New("saving a composition to a conversation requires a conversation id")
	}
	doc := req.Doc
	if err := validateCompositionDoc(&doc); err != nil {
		return SaveCompositionEditResult{}, err
	}
	a.streamsMu.Lock()
	for _, active := range a.streamConversations {
		if active == conversationID {
			a.streamsMu.Unlock()
			return SaveCompositionEditResult{}, errors.New("the conversation is still streaming — wait for the turn to finish before saving")
		}
	}
	a.streamsMu.Unlock()

	config, err := loadReadyConfig()
	if err != nil {
		return SaveCompositionEditResult{}, err
	}
	store := newHistoryStore(config.Storage)
	loaded, err := store.loadForAppend(conversationID, "chat", "a chat", config.Tools.Filesystem.Root)
	if err != nil {
		return SaveCompositionEditResult{}, err
	}
	content, err := writeCompositionArtifact(loaded.ArtifactsDir, doc)
	if err != nil {
		return SaveCompositionEditResult{}, err
	}
	source := strings.TrimSpace(req.SourceArtifactID)
	summary := fmt.Sprintf("Saved composition “%s” · %s", doc.Name, compositionDocSummary(doc))
	if source != "" {
		summary = fmt.Sprintf("Saved composition “%s” from %s · %s", doc.Name, source, compositionDocSummary(doc))
	}
	nowText := time.Now().Format(time.RFC3339)
	assistantTurn := buildChatAssistantTurn(conversationID, loaded.NextTurnNumber, nowText, summary, "", "", "", "", 0, HarnessRun{}, "")
	assistantTurn.Content = append(assistantTurn.Content, content)

	loaded.Conversation.UpdatedAt = nowText
	loaded.Conversation.Stats.TurnCount++
	loaded.Conversation.Stats.ArtifactCount++
	if err := store.writeConversation(loaded.Path, loaded.Conversation); err != nil {
		return SaveCompositionEditResult{}, err
	}
	if err := store.writeTurn(loaded.TurnsDir, assistantTurn); err != nil {
		return SaveCompositionEditResult{}, err
	}
	absArtifactsDir, err := filepath.Abs(loaded.ArtifactsDir)
	if err != nil {
		absArtifactsDir = loaded.ArtifactsDir
	}
	return SaveCompositionEditResult{
		ArtifactID: content.ArtifactID,
		URL:        artifactPrefix + filepath.Join(absArtifactsDir, filepath.Base(content.Path)),
		Doc:        doc,
		Summary:    summary,
	}, nil
}

// SaveComposition copies a composition document to a user-chosen location —
// the SaveTranscript twin for the project file itself (a backup, or a hand
// edit outside Atelier). The bytes must parse as a composition before the
// dialog opens.
func (a *App) SaveComposition(req SaveTranscriptRequest) (string, error) {
	sourcePath := strings.TrimSpace(req.Path)
	sourcePath = strings.TrimPrefix(sourcePath, artifactPrefix)
	if sourcePath == "" {
		return "", errors.New("composition path is empty")
	}
	data, err := os.ReadFile(sourcePath)
	if err != nil {
		return "", err
	}
	if _, err := parseCompositionDoc(data); err != nil {
		return "", err
	}
	return a.saveArtifactDialog(data, req.SuggestedName, "atelier-composition", ".json", "Save composition", []runtime.FileFilter{
		{DisplayName: "Composition Files", Pattern: "*.json"},
		{DisplayName: "All Files", Pattern: "*.*"},
	})
}
