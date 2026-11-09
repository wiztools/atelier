package main

// The comic workspace's Go surface — the fourth session-free editor beside
// subtitles, markdown, and the video composer: resolve a cbc_ artifact into
// the editor's document, resolve and measure one image artifact into a ref
// the picker can add, seed a draft from referenced images (the "open the
// comic editor" intent's seeded-only contract), and commit an edited book
// back into its conversation as a NEW artifact. The source is never
// overwritten; every save is a revision. Nothing here renders — the editor
// previews live in SVG on the frontend, and a draft that was never saved
// never touches the conversation, which is what lets a mid-stream editor
// launch open the workspace without writing a turn into a streaming
// conversation.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"atelier/internal/mediasniff"
)

// ComicSourceInfo is what the editor loads: the document plus the identity
// the reply chips and the picker's provenance labels need. For a seeded draft
// ArtifactID and URL are empty — nothing is persisted until the first save —
// and the EditorHeader shows its "unsaved draft" breadcrumb. MissingRefs
// names the referenced images whose files are gone on disk (the composer's
// identify-only report): the book keeps them, flagged.
type ComicSourceInfo struct {
	ConversationID    string   `json:"conversationId"`
	ConversationTitle string   `json:"conversationTitle,omitempty"`
	ArtifactID        string   `json:"artifactId,omitempty"`
	URL               string   `json:"url,omitempty"`
	MimeType          string   `json:"mimeType,omitempty"`
	Doc               ComicDoc `json:"doc"`
	MissingRefs       []string `json:"missingRefs"`
	Warnings          []string `json:"warnings"`
}

// resolveComicContent finds the comic content entry an editor open names,
// plus the conversation title for provenance labels. artifactID is exact;
// the entry must be a comic artifact.
func resolveComicContent(storage ConfigStorage, conversationID, artifactID string) (*HistoryContent, string, error) {
	artifactID = strings.TrimSpace(artifactID)
	if artifactID == "" {
		return nil, "", errors.New("comic artifact id is empty")
	}
	detail, err := getConversation(storage, conversationID)
	if err != nil {
		return nil, "", err
	}
	for i := len(detail.Turns) - 1; i >= 0; i-- {
		for j := range detail.Turns[i].Content {
			content := detail.Turns[i].Content[j]
			if content.ArtifactID == artifactID && comicArtifactExtension(content) != "" {
				return &content, detail.Conversation.Title, nil
			}
		}
	}
	return nil, "", fmt.Errorf("no comic artifact with id %q in this conversation", artifactID)
}

// ResolveComicEditSource loads a saved comic revision into the editor.
// Read-only — nothing here mutates the conversation. The document is
// validated on the way in, so a hand-edited artifact that drifted from the
// rules fails here with the rule named rather than inside the editor.
func (a *App) ResolveComicEditSource(conversationID, artifactID string) (ComicSourceInfo, error) {
	config, err := loadReadyConfig()
	if err != nil {
		return ComicSourceInfo{}, err
	}
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return ComicSourceInfo{}, errors.New("the comic editor opens a conversation asset, and no conversation was given")
	}
	content, title, err := resolveComicContent(config.Storage, conversationID, artifactID)
	if err != nil {
		return ComicSourceInfo{}, err
	}
	path, err := contentArtifactPath(config.Storage, conversationID, *content)
	if err != nil {
		return ComicSourceInfo{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ComicSourceInfo{}, fmt.Errorf("reading the comic artifact failed: %w", err)
	}
	doc, err := parseComicDoc(data)
	if err != nil {
		return ComicSourceInfo{}, err
	}
	if err := validateComicDoc(&doc); err != nil {
		return ComicSourceInfo{}, fmt.Errorf("the saved comic is not valid: %w", err)
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		absPath = path
	}
	return ComicSourceInfo{
		ConversationID:    conversationID,
		ConversationTitle: title,
		ArtifactID:        content.ArtifactID,
		URL:               artifactPrefix + absPath,
		MimeType:          content.MimeType,
		Doc:               doc,
		MissingRefs:       comicMissingRefs(doc),
		Warnings:          []string{},
	}, nil
}

// comicMediaRefFor resolves an artifact id into an unmeasured image ref —
// conversation-locally first, then across the conversation's library when it
// belongs to a project (the picker's dual scope, and resolveReferencedAssets'
// widening rule). Only image artifacts qualify; the file must exist on disk,
// since a ref that is missing at add time is a mistake rather than a later
// loss.
func comicMediaRefFor(storage ConfigStorage, conversationID, artifactID string) (ComicMediaRef, error) {
	artifactID = strings.TrimSpace(artifactID)
	if artifactID == "" {
		return ComicMediaRef{}, errors.New("media artifact id is empty")
	}
	detail, err := getConversation(storage, conversationID)
	if err != nil {
		return ComicMediaRef{}, err
	}
	var ref ComicMediaRef
	if content, ok := assetContentIndex(detail)[artifactID]; ok {
		if content.Type != "image" {
			return ComicMediaRef{}, fmt.Errorf("%s is %s media — the comic takes images", artifactID, comicRefKindPhrase(content.Type))
		}
		path, err := contentArtifactPath(storage, conversationID, content)
		if err != nil {
			return ComicMediaRef{}, err
		}
		ref = ComicMediaRef{
			ArtifactID:     content.ArtifactID,
			ConversationID: conversationID,
			Path:           path,
			MimeType:       content.MimeType,
			Kind:           content.Type,
		}
		if ref.ArtifactID == "" {
			ref.ArtifactID = artifactID
		}
		ref.Width = content.Width
		ref.Height = content.Height
	} else {
		projectID := detail.Conversation.ProjectID
		if projectID == "" {
			return ComicMediaRef{}, fmt.Errorf("no image asset with id %q in this conversation", artifactID)
		}
		lookup, err := libraryAssetLookup(storage, projectID)
		if err != nil {
			return ComicMediaRef{}, err
		}
		asset, found := lookup[artifactID]
		if !found {
			return ComicMediaRef{}, fmt.Errorf("no image asset with id %q in this conversation or its library", artifactID)
		}
		ref, err = comicRefFromLibraryAsset(asset)
		if err != nil {
			return ComicMediaRef{}, err
		}
	}
	return finishComicMediaRef(ref)
}

// comicRefFromLibraryAsset maps a library asset onto an unmeasured image ref —
// the asset keeps its OWN conversation, which is what lets a comic cite art
// from a sibling chat.
func comicRefFromLibraryAsset(asset ConversationAsset) (ComicMediaRef, error) {
	if asset.Kind != "image" {
		return ComicMediaRef{}, fmt.Errorf("%s is %s media — the comic takes images", asset.ID, comicRefKindPhrase(asset.Kind))
	}
	if asset.URL == "" {
		return ComicMediaRef{}, fmt.Errorf("the file for %s is missing on disk", asset.ID)
	}
	return ComicMediaRef{
		ArtifactID:     asset.ID,
		ConversationID: asset.ConversationID,
		Path:           strings.TrimPrefix(asset.URL, artifactPrefix),
		MimeType:       asset.MimeType,
		Kind:           "image",
		Width:          asset.Width,
		Height:         asset.Height,
	}, nil
}

// finishComicMediaRef absolutizes a ref's path and requires the file — a ref
// missing at add time is a mistake rather than a later loss.
func finishComicMediaRef(ref ComicMediaRef) (ComicMediaRef, error) {
	absPath, err := filepath.Abs(ref.Path)
	if err == nil {
		ref.Path = absPath
	}
	if _, err := os.Stat(ref.Path); err != nil {
		return ComicMediaRef{}, fmt.Errorf("the file for %s is missing on disk", ref.ArtifactID)
	}
	return ref, nil
}

// comicLibraryMediaRefFor resolves an artifact id through a LIBRARY's assets
// alone — the detached draft's scope (compositionLibraryMediaRefFor's twin).
func comicLibraryMediaRefFor(storage ConfigStorage, libraryID, artifactID string) (ComicMediaRef, error) {
	libraryID = strings.TrimSpace(libraryID)
	if libraryID == "" {
		return ComicMediaRef{}, errors.New("a detached comic picks from its library, and no library was given")
	}
	lookup, err := libraryAssetIndex(storage, libraryID)
	if err != nil {
		return ComicMediaRef{}, err
	}
	asset, found := lookup[strings.TrimSpace(artifactID)]
	if !found {
		return ComicMediaRef{}, fmt.Errorf("no image asset with id %q in this library", artifactID)
	}
	ref, err := comicRefFromLibraryAsset(asset)
	if err != nil {
		return ComicMediaRef{}, err
	}
	return finishComicMediaRef(ref)
}

// probeComicMediaRef fills a ref's facts snapshot — the pixel size, sniffed
// from the file's magic bytes (PNG/JPEG/WebP/GIF; mediasniff.ImagePixelDimensions needs
// no CLI and no codec import). A ref the sniff cannot measure is refused:
// the editor crops by percentage of the source, and a wrong guess would
// misplace every crop — an exotic container (HEIC, AVIF) should go through
// convert_image first.
func probeComicMediaRef(config AppConfig, ref ComicMediaRef) (ComicMediaRef, error) {
	if ref.Width > 0 && ref.Height > 0 {
		return ref, nil
	}
	data, err := os.ReadFile(ref.Path)
	if err != nil {
		return ComicMediaRef{}, fmt.Errorf("reading %s failed: %w", ref.ArtifactID, err)
	}
	width, height, ok := mediasniff.ImagePixelDimensions(data)
	if !ok {
		return ComicMediaRef{}, fmt.Errorf("could not measure %s — the comic needs its pixel size; convert the file to PNG or JPEG (convert_image) to add it", ref.ArtifactID)
	}
	ref.Width = int(width)
	ref.Height = int(height)
	return ref, nil
}

// resolveComicMedia resolves an artifact id against the draft's scope — a
// conversation (widened to its library) or, when the conversation is empty, a
// bare library (the detached draft). The shared resolve-and-measure the
// picker's add and the seed path both run.
func resolveComicMedia(config AppConfig, conversationID, libraryID, artifactID string) (ComicMediaRef, error) {
	var ref ComicMediaRef
	var err error
	if strings.TrimSpace(conversationID) != "" {
		ref, err = comicMediaRefFor(config.Storage, conversationID, artifactID)
	} else {
		ref, err = comicLibraryMediaRefFor(config.Storage, libraryID, artifactID)
	}
	if err != nil {
		return ComicMediaRef{}, err
	}
	return probeComicMediaRef(config, ref)
}

// ResolveComicMedia resolves and measures one image artifact into a ref the
// editor appends to its document — the picker's add path. conversationID is
// the comic's OWNING conversation (its picker scope), not necessarily the
// artifact's: a library sibling's asset resolves through the widening rule
// and carries its own conversation on the ref. For a DETACHED draft the
// conversation is empty and libraryID is the scope the draft opened with.
func (a *App) ResolveComicMedia(conversationID, artifactID, libraryID string) (ComicMediaRef, error) {
	config, err := loadReadyConfig()
	if err != nil {
		return ComicMediaRef{}, err
	}
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" && strings.TrimSpace(libraryID) == "" {
		return ComicMediaRef{}, errors.New("adding media to a comic requires a conversation or library scope")
	}
	return resolveComicMedia(config, conversationID, libraryID, artifactID)
}

// SeedComicRequest asks for a fresh draft seeded with referenced images —
// the editor-launch intent's payload and the "New comic" action's (with no
// ids). ArtifactIDs are in reference order, which IS the seeded order. A
// DETACHED draft passes no conversation and carries LibraryID as its picker
// scope; it starts empty.
type SeedComicRequest struct {
	ConversationID string   `json:"conversationId"`
	LibraryID      string   `json:"libraryId,omitempty"`
	Name           string   `json:"name,omitempty"`
	ArtifactIDs    []string `json:"artifactIds"`
}

// SeedComic builds an unsaved draft from referenced images, in order — the
// seeded-only contract: the result is indistinguishable from the user having
// picked those assets from the picker in that order, and nothing is arranged
// (no panels are built — the images wait in the unplaced tray; arranging the
// frames is the human's half of the loop). Read-only; nothing is persisted
// until the editor's first save. An id that will not resolve or measure is
// skipped with a warning rather than failing the open (the picker is right
// there to add it by hand), so the workspace always opens. A detached draft
// opens empty — the picker is the way in.
func (a *App) SeedComic(req SeedComicRequest) (ComicSourceInfo, error) {
	config, err := loadReadyConfig()
	if err != nil {
		return ComicSourceInfo{}, err
	}
	conversationID := strings.TrimSpace(req.ConversationID)
	if conversationID == "" {
		for _, id := range req.ArtifactIDs {
			if strings.TrimSpace(id) != "" {
				return ComicSourceInfo{}, errors.New("a detached comic starts empty — seeding needs the conversation whose images the ids belong to")
			}
		}
		doc := seedComicDoc(req.Name, nil)
		if err := validateComicDoc(&doc); err != nil {
			return ComicSourceInfo{}, err
		}
		return ComicSourceInfo{
			ConversationID: "",
			Doc:            doc,
			MissingRefs:    []string{},
			Warnings:       []string{},
		}, nil
	}
	detail, err := getConversation(config.Storage, conversationID)
	if err != nil {
		return ComicSourceInfo{}, err
	}
	refs := make([]ComicMediaRef, 0, len(req.ArtifactIDs))
	warnings := []string{}
	for _, id := range req.ArtifactIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		ref, err := resolveComicMedia(config, conversationID, req.LibraryID, id)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("Skipped %s: %v", id, err))
			continue
		}
		refs = append(refs, ref)
	}
	doc := seedComicDoc(req.Name, refs)
	if err := validateComicDoc(&doc); err != nil {
		return ComicSourceInfo{}, err
	}
	return ComicSourceInfo{
		ConversationID:    conversationID,
		ConversationTitle: detail.Conversation.Title,
		Doc:               doc,
		MissingRefs:       []string{},
		Warnings:          warnings,
	}, nil
}

// SaveComicEditRequest carries one commit of the editor's book back into its
// conversation — the single door the other editors have: a new cbc_ artifact
// plus a small assistant turn recording the save. The source artifact is
// provenance for the turn's summary; the original is never touched. Saving
// under a new name from an older revision IS duplication. A DETACHED draft
// passes no conversation and carries its Lifecycle, so this save gives birth
// to the host conversation.
type SaveComicEditRequest struct {
	ConversationID   string               `json:"conversationId"`
	SourceArtifactID string               `json:"sourceArtifactId,omitempty"`
	Doc              ComicDoc             `json:"doc"`
	Lifecycle        *EditorSaveLifecycle `json:"lifecycle,omitempty"`
}

// SaveComicEditResult names what the save produced: the new artifact's
// identity (the reply chips, the assets panel, a later open all read it)
// plus the normalized document, so the editor re-baselines on exactly what
// landed. ConversationID is the host — for a materializing save, the
// conversation this save created.
type SaveComicEditResult struct {
	ConversationID string   `json:"conversationId"`
	ArtifactID     string   `json:"artifactId"`
	URL            string   `json:"url"`
	Doc            ComicDoc `json:"doc"`
	Summary        string   `json:"summary"`
}

// SaveComicEdit commits the edited book to the conversation: the document
// lands as a fresh cbc_<hex>.json artifact and a short assistant turn
// records the save (name, pages, panels). Every save is additive. Refused
// while the conversation streams, like every other sanctioned rewrite. For a
// detached draft the save CREATES the conversation, titled from the book.
func (a *App) SaveComicEdit(req SaveComicEditRequest) (SaveComicEditResult, error) {
	doc := req.Doc
	if err := validateComicDoc(&doc); err != nil {
		return SaveComicEditResult{}, err
	}
	conversationID := strings.TrimSpace(req.ConversationID)
	if conversationID == "" && req.Lifecycle == nil {
		return SaveComicEditResult{}, errors.New("saving a comic requires a conversation id, or the draft's creation context")
	}
	config, err := loadReadyConfig()
	if err != nil {
		return SaveComicEditResult{}, err
	}
	target, err := a.openEditorSaveTarget(config, conversationID, doc.Name, req.Lifecycle)
	if err != nil {
		return SaveComicEditResult{}, err
	}
	content, err := writeComicArtifact(target.ArtifactsDir, doc)
	if err != nil {
		if target.Created {
			target.discard()
		}
		return SaveComicEditResult{}, err
	}
	source := strings.TrimSpace(req.SourceArtifactID)
	summary := fmt.Sprintf("Saved comic “%s” · %s", doc.Name, comicDocSummary(doc))
	if source != "" {
		summary = fmt.Sprintf("Saved comic “%s” from %s · %s", doc.Name, source, comicDocSummary(doc))
	}
	nowText := time.Now().Format(time.RFC3339)
	assistantTurn := buildChatAssistantTurn(target.ConversationID, target.TurnNumber, nowText, summary, "", "", "", "", 0, HarnessRun{}, "")
	assistantTurn.Content = append(assistantTurn.Content, content)
	if err := target.commit(assistantTurn, nowText); err != nil {
		if target.Created {
			target.discard()
		}
		return SaveComicEditResult{}, err
	}
	return SaveComicEditResult{
		ConversationID: target.ConversationID,
		ArtifactID:     content.ArtifactID,
		URL:            target.artifactURL(content.Path),
		Doc:            doc,
		Summary:        summary,
	}, nil
}

// SaveComic copies a comic document to a user-chosen location — the
// SaveComposition twin for the project file itself (a backup, or a hand edit
// outside Atelier). The bytes must parse as a comic before the dialog opens.
func (a *App) SaveComic(req SaveTranscriptRequest) (string, error) {
	sourcePath := strings.TrimSpace(req.Path)
	sourcePath = strings.TrimPrefix(sourcePath, artifactPrefix)
	if sourcePath == "" {
		return "", errors.New("comic path is empty")
	}
	data, err := os.ReadFile(sourcePath)
	if err != nil {
		return "", err
	}
	if _, err := parseComicDoc(data); err != nil {
		return "", err
	}
	return a.saveArtifactDialog(data, req.SuggestedName, "atelier-comic", ".json", "Save comic", []runtime.FileFilter{
		{DisplayName: "Comic Files", Pattern: "*.json"},
		{DisplayName: "All Files", Pattern: "*.*"},
	})
}
