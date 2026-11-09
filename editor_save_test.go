package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// detachedEditorLibraryFixture builds a library with one project and a
// sibling conversation (assetsID) holding a clip and an image — the library a
// detached editor draft picks from before any conversation of its own exists.
// Each test names its own assetsID: the asset fold cache is process-wide and
// keyed by conversation id, so a shared fixture id would leak one test's
// temp paths into the next.
func detachedEditorLibraryFixture(t *testing.T, config AppConfig, assetsID string) (libraryID, projectID string) {
	t.Helper()
	if err := ensureStorageDirs(config.Storage); err != nil {
		t.Fatalf("ensureStorageDirs: %v", err)
	}
	library, err := createLibrary(config.Storage, "Films")
	if err != nil {
		t.Fatalf("createLibrary: %v", err)
	}
	project, err := createProject(config.Storage, library.ID, "Trailer")
	if err != nil {
		t.Fatalf("createProject: %v", err)
	}
	artifacts := filepath.Join(config.Storage.History, "conversations", "2026/10/"+assetsID, "artifacts")
	if err := os.MkdirAll(artifacts, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(artifacts, "vid_lib.mp4"), compositionMP4Fixture(6, 1920, 1080, true), 0644); err != nil {
		t.Fatalf("write vid_lib: %v", err)
	}
	if err := os.WriteFile(filepath.Join(artifacts, "img_lib.png"), comicPNG(t), 0644); err != nil {
		t.Fatalf("write img_lib: %v", err)
	}
	writeSearchConversation(t, config.Storage, "2026/10/"+assetsID,
		libraryConversationFixture(assetsID, "Asset making", "2026-10-01T10:00:00Z", project.ID),
		searchTurnFixture("turn_000001", "assistant", "2026-10-01T10:00:00Z",
			HistoryContent{Type: "video", ArtifactID: "vid_lib", Path: "artifacts/vid_lib.mp4", MimeType: "video/mp4"},
			HistoryContent{Type: "image", ArtifactID: "img_lib", Path: "artifacts/img_lib.png", MimeType: "image/png"}),
	)
	return library.ID, project.ID
}

// The detached draft's whole arc: it opens with no conversation, picks media
// through its library alone, and its first save gives birth to the host
// conversation — filed in the draft's project, titled from the document,
// with the save's summary as turn 1. A second save appends like any other.
func TestSaveCompositionEditMaterializesConversation(t *testing.T) {
	config := editTestHome(t)
	libraryID, projectID := detachedEditorLibraryFixture(t, config, "conv_assets_cmp")
	app := NewApp()

	seed, err := app.SeedComposition(SeedCompositionRequest{LibraryID: libraryID, Name: "Launch reel"})
	if err != nil {
		t.Fatalf("SeedComposition(detached) returned error: %v", err)
	}
	if seed.ConversationID != "" || len(seed.Doc.Video) != 0 {
		t.Fatalf("detached seed = %+v, want an empty draft with no conversation", seed)
	}
	ref, err := app.ResolveCompositionMedia("", "vid_lib", libraryID)
	if err != nil {
		t.Fatalf("ResolveCompositionMedia(detached) returned error: %v", err)
	}
	if ref.ConversationID != "conv_assets_cmp" || ref.DurationSeconds != 6 {
		t.Fatalf("detached ref = %+v, want conv_assets_cmp's 6s clip", ref)
	}
	doc := seedCompositionDoc("Launch reel", []CompositionMediaRef{ref})

	workspace := t.TempDir()
	saved, err := app.SaveCompositionEdit(SaveCompositionEditRequest{
		Doc: doc,
		Lifecycle: &EditorSaveLifecycle{
			ProjectID:      projectID,
			Workspace:      workspace,
			ChatModel:      "llama3",
			ChatSystem:     "be brief",
			ModelOverrides: &ConversationModelOverrides{PrimaryModel: "qwen3"},
		},
	})
	if err != nil {
		t.Fatalf("SaveCompositionEdit(detached) returned error: %v", err)
	}
	if !strings.HasPrefix(saved.ConversationID, "conv_") || !strings.HasPrefix(saved.ArtifactID, "cmp_") || !strings.Contains(saved.URL, saved.ConversationID) {
		t.Fatalf("saved = %+v, want a new conversation holding a cmp_ artifact", saved)
	}

	detail, err := getConversation(config.Storage, saved.ConversationID)
	if err != nil {
		t.Fatalf("getConversation(new) returned error: %v", err)
	}
	record := detail.Conversation
	if record.Kind != "chat" || record.Title != "Launch reel" || record.ProjectID != projectID || record.Workspace != workspace {
		t.Fatalf("record = %+v, want a chat titled from the document, in the draft's project and workspace", record)
	}
	if record.Defaults.ChatModel != "llama3" || record.Defaults.System != "be brief" {
		t.Fatalf("record defaults = %+v, want the draft's chat selection pinned", record.Defaults)
	}
	if record.ModelOverrides == nil || record.ModelOverrides.PrimaryModel != "qwen3" {
		t.Fatalf("record overrides = %+v, want the draft's pending override pinned", record.ModelOverrides)
	}
	if record.Stats.TurnCount != 1 || record.Stats.ArtifactCount != 1 {
		t.Fatalf("record stats = %+v, want one turn holding one artifact", record.Stats)
	}
	if len(detail.Turns) != 1 || detail.Turns[0].Role != "assistant" || detail.Turns[0].ID != "turn_000001" {
		t.Fatalf("turns = %+v, want the save summary as the assistant's turn 1", detail.Turns)
	}
	turnText := ""
	for _, content := range detail.Turns[0].Content {
		turnText += content.Text
	}
	if !strings.Contains(turnText, "Saved composition “Launch reel”") {
		t.Fatalf("turn 1 text = %q", turnText)
	}

	// The revision reopens from its new home; a follow-up save appends.
	reopened, err := app.ResolveCompositionEditSource(saved.ConversationID, saved.ArtifactID)
	if err != nil {
		t.Fatalf("ResolveCompositionEditSource(new) returned error: %v", err)
	}
	if len(reopened.Doc.Video) != 1 || reopened.ConversationTitle != "Launch reel" {
		t.Fatalf("reopened = %+v", reopened)
	}
	second, err := app.SaveCompositionEdit(SaveCompositionEditRequest{ConversationID: saved.ConversationID, SourceArtifactID: saved.ArtifactID, Doc: reopened.Doc})
	if err != nil {
		t.Fatalf("second SaveCompositionEdit returned error: %v", err)
	}
	if second.ConversationID != saved.ConversationID {
		t.Fatalf("second save landed in %q, want the materialized %q", second.ConversationID, saved.ConversationID)
	}
	detail, err = getConversation(config.Storage, saved.ConversationID)
	if err != nil {
		t.Fatalf("getConversation after append returned error: %v", err)
	}
	if len(detail.Turns) != 2 || detail.Conversation.Stats.TurnCount != 2 {
		t.Fatalf("after append: %d turns, stats %+v — want 2", len(detail.Turns), detail.Conversation.Stats)
	}
}

// A detached draft's seeds and saves refuse the states that cannot work —
// and a refused birth leaves nothing on disk.
func TestDetachedCompositionRefusals(t *testing.T) {
	config := editTestHome(t)
	libraryID, _ := detachedEditorLibraryFixture(t, config, "conv_assets_refuse")
	app := NewApp()

	if _, err := app.SeedComposition(SeedCompositionRequest{LibraryID: libraryID, ArtifactIDs: []string{"vid_lib"}}); err == nil || !strings.Contains(err.Error(), "starts empty") {
		t.Fatalf("SeedComposition(detached with ids) = %v, want the starts-empty refusal", err)
	}
	if _, err := app.ResolveCompositionMedia("", "vid_lib", ""); err == nil || !strings.Contains(err.Error(), "conversation or library scope") {
		t.Fatalf("ResolveCompositionMedia(no scope) = %v", err)
	}
	if _, err := app.ResolveCompositionMedia("", "vid_absent", libraryID); err == nil || !strings.Contains(err.Error(), "in this library") {
		t.Fatalf("ResolveCompositionMedia(unknown in library) = %v", err)
	}

	doc := seedCompositionDoc("Orphan", nil)
	if _, err := app.SaveCompositionEdit(SaveCompositionEditRequest{Doc: doc}); err == nil || !strings.Contains(err.Error(), "creation context") {
		t.Fatalf("SaveCompositionEdit(no conversation, no lifecycle) = %v", err)
	}
	before := countConversationDirs(t, config.Storage)
	if _, err := app.SaveCompositionEdit(SaveCompositionEditRequest{Doc: doc, Lifecycle: &EditorSaveLifecycle{ProjectID: "proj_gone"}}); err == nil {
		t.Fatalf("SaveCompositionEdit(unknown project) succeeded, want the fail-early refusal")
	}
	if after := countConversationDirs(t, config.Storage); after != before {
		t.Fatalf("a refused birth left %d conversation dirs, want %d", after, before)
	}
}

// A standalone new chat (no project) still materializes — filed outside any
// project, in the configured default workspace.
func TestSaveComicEditMaterializesStandaloneConversation(t *testing.T) {
	config := editTestHome(t)
	libraryID, _ := detachedEditorLibraryFixture(t, config, "conv_assets_comic")
	app := NewApp()

	seed, err := app.SeedComic(SeedComicRequest{Name: "Issue #1"})
	if err != nil {
		t.Fatalf("SeedComic(detached) returned error: %v", err)
	}
	if seed.ConversationID != "" || len(seed.Doc.Unplaced) != 0 {
		t.Fatalf("detached comic seed = %+v", seed)
	}
	ref, err := app.ResolveComicMedia("", "img_lib", libraryID)
	if err != nil {
		t.Fatalf("ResolveComicMedia(detached) returned error: %v", err)
	}
	if ref.ConversationID != "conv_assets_comic" || ref.Width != 4 || ref.Height != 3 {
		t.Fatalf("detached comic ref = %+v", ref)
	}
	if _, err := app.ResolveComicMedia("", "vid_lib", libraryID); err == nil || !strings.Contains(err.Error(), "comic takes images") {
		t.Fatalf("ResolveComicMedia(video in library) = %v", err)
	}

	seed.Doc.Unplaced = []ComicMediaRef{ref}
	saved, err := app.SaveComicEdit(SaveComicEditRequest{Doc: seed.Doc, Lifecycle: &EditorSaveLifecycle{}})
	if err != nil {
		t.Fatalf("SaveComicEdit(detached) returned error: %v", err)
	}
	if !strings.HasPrefix(saved.ConversationID, "conv_") || !strings.HasPrefix(saved.ArtifactID, "cbc_") {
		t.Fatalf("saved comic = %+v", saved)
	}
	detail, err := getConversation(config.Storage, saved.ConversationID)
	if err != nil {
		t.Fatalf("getConversation(new comic) returned error: %v", err)
	}
	if detail.Conversation.Title != "Issue #1" || detail.Conversation.ProjectID != "" || detail.Conversation.Workspace != config.Tools.Filesystem.Root {
		t.Fatalf("standalone record = %+v, want the book's title, no project, the default workspace", detail.Conversation)
	}
	if len(detail.Turns) != 1 || detail.Turns[0].Role != "assistant" {
		t.Fatalf("comic turns = %+v, want the save summary as turn 1", detail.Turns)
	}
}

// countConversationDirs counts every conversation directory under history —
// a refused birth must not leave one behind.
func countConversationDirs(t *testing.T, storage ConfigStorage) int {
	t.Helper()
	count := 0
	root := filepath.Join(storage.History, "conversations")
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err == nil && info.IsDir() && strings.HasPrefix(info.Name(), "conv_") {
			count++
		}
		return nil
	})
	return count
}
