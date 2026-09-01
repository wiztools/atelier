package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// libraryTestStorage builds storage with history and libraries dirs under a
// temp root, both created on disk.
func libraryTestStorage(t *testing.T) ConfigStorage {
	t.Helper()
	root := t.TempDir()
	storage := ConfigStorage{
		Root:      root,
		History:   filepath.Join(root, "history"),
		Artifacts: filepath.Join(root, "history"),
		Libraries: filepath.Join(root, "libraries"),
	}
	if err := ensureStorageDirs(storage); err != nil {
		t.Fatalf("ensureStorageDirs returned error: %v", err)
	}
	return storage
}

// libraryConversationFixture is searchConversationFixture plus a project.
func libraryConversationFixture(id, title, updatedAt, projectID string) HistoryConversation {
	conversation := searchConversationFixture(id, title, updatedAt)
	conversation.ProjectID = projectID
	return conversation
}

func TestLibraryProjectCRUDRoundTrip(t *testing.T) {
	storage := libraryTestStorage(t)

	created, err := createLibrary(storage, "My Library")
	if err != nil {
		t.Fatalf("createLibrary returned error: %v", err)
	}
	if created.ID == "" || created.Name != "My Library" || len(created.Projects) != 0 {
		t.Fatalf("created summary = %+v", created)
	}

	project, err := createProject(storage, created.ID, "Second Project")
	if err != nil {
		t.Fatalf("createProject returned error: %v", err)
	}
	if _, err := createProject(storage, created.ID, "A First Project"); err != nil {
		t.Fatalf("createProject returned error: %v", err)
	}

	// findProject resolves by ID without knowing the library.
	found, path, err := findProject(storage, project.ID)
	if err != nil {
		t.Fatalf("findProject returned error: %v", err)
	}
	if found.LibraryID != created.ID || !strings.HasSuffix(path, "project.json") {
		t.Fatalf("found project = %+v path = %q", found, path)
	}

	// Listing nests projects under their library, name-sorted.
	libraries, err := listLibraries(storage)
	if err != nil {
		t.Fatalf("listLibraries returned error: %v", err)
	}
	if len(libraries) != 1 || len(libraries[0].Projects) != 2 {
		t.Fatalf("libraries = %+v", libraries)
	}
	if libraries[0].Projects[0].Name != "A First Project" || libraries[0].Projects[1].Name != "Second Project" {
		t.Fatalf("projects not name-sorted: %+v", libraries[0].Projects)
	}

	// Rename flows through to the nested listing.
	renamed, err := renameProject(storage, project.ID, "Renamed Project")
	if err != nil {
		t.Fatalf("renameProject returned error: %v", err)
	}
	if renamed.Name != "Renamed Project" {
		t.Fatalf("renamed = %+v", renamed)
	}
	renamedLibrary, err := renameLibrary(storage, created.ID, "Renamed Library")
	if err != nil {
		t.Fatalf("renameLibrary returned error: %v", err)
	}
	if renamedLibrary.Name != "Renamed Library" || len(renamedLibrary.Projects) != 2 {
		t.Fatalf("renamed library = %+v", renamedLibrary)
	}
}

func TestLibraryNameValidation(t *testing.T) {
	storage := libraryTestStorage(t)

	cases := []struct {
		name    string
		want    string
		wantErr bool
	}{
		{name: "  Trimmed   and  collapsed  ", want: "Trimmed and collapsed"},
		{name: "", wantErr: true},
		{name: "   ", wantErr: true},
	}
	for _, tc := range cases {
		library, err := createLibrary(storage, tc.name)
		if tc.wantErr {
			if err == nil {
				t.Fatalf("createLibrary(%q) expected an error", tc.name)
			}
			continue
		}
		if err != nil {
			t.Fatalf("createLibrary(%q) returned error: %v", tc.name, err)
		}
		if library.Name != tc.want {
			t.Fatalf("createLibrary(%q).Name = %q, want %q", tc.name, library.Name, tc.want)
		}
	}

	long := strings.Repeat("x", 200)
	project, err := createProject(storage, mustCreateLibrary(t, storage), long)
	if err != nil {
		t.Fatalf("createProject returned error: %v", err)
	}
	// compactString truncates to 72 runes and appends "...".
	if len(project.Name) != 75 {
		t.Fatalf("project name length = %d, want capped at 72 runes + ellipsis", len(project.Name))
	}

	if _, err := renameLibrary(storage, "lib_missing", "x"); err == nil {
		t.Fatal("renameLibrary on an unknown library must fail")
	}
	if _, err := createProject(storage, "lib_missing", "p"); err == nil {
		t.Fatal("createProject under an unknown library must fail")
	}
	if _, _, err := findProject(storage, "proj_missing"); err == nil {
		t.Fatal("findProject on an unknown project must fail")
	}
}

func mustCreateLibrary(t *testing.T, storage ConfigStorage) string {
	t.Helper()
	library, err := createLibrary(storage, "Library "+randomID("t"))
	if err != nil {
		t.Fatalf("createLibrary returned error: %v", err)
	}
	return library.ID
}

func TestListProjectConversationsFiltersMembership(t *testing.T) {
	storage := libraryTestStorage(t)
	libraryID := mustCreateLibrary(t, storage)
	projectA, err := createProject(storage, libraryID, "A")
	if err != nil {
		t.Fatalf("createProject returned error: %v", err)
	}
	projectB, err := createProject(storage, libraryID, "B")
	if err != nil {
		t.Fatalf("createProject returned error: %v", err)
	}

	writeSearchConversation(t, storage, "2026/08/conv_in_a",
		libraryConversationFixture("conv_in_a", "In A", "2026-08-01T10:00:00Z", projectA.ID))
	writeSearchConversation(t, storage, "2026/08/conv_in_a_old",
		libraryConversationFixture("conv_in_a_old", "In A older", "2026-07-01T10:00:00Z", projectA.ID))
	writeSearchConversation(t, storage, "2026/08/conv_in_b",
		libraryConversationFixture("conv_in_b", "In B", "2026-08-02T10:00:00Z", projectB.ID))
	writeSearchConversation(t, storage, "2026/08/conv_alone",
		libraryConversationFixture("conv_alone", "Standalone", "2026-08-03T10:00:00Z", ""))
	deleted := libraryConversationFixture("conv_deleted", "Deleted", "2026-08-04T10:00:00Z", projectA.ID)
	deleted.DeletedAt = "2026-08-05T10:00:00Z"
	writeSearchConversation(t, storage, "2026/08/conv_deleted", deleted)

	got, err := listProjectConversations(storage, projectA.ID)
	if err != nil {
		t.Fatalf("listProjectConversations returned error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("project A conversation count = %d, want 2 (B's member, standalone, and soft-deleted excluded): %+v", len(got), got)
	}
	if got[0].ID != "conv_in_a" || got[1].ID != "conv_in_a_old" {
		t.Fatalf("not newest-first: %+v", got)
	}
	if got[0].ProjectID != projectA.ID {
		t.Fatalf("summary ProjectID = %q, want %q", got[0].ProjectID, projectA.ID)
	}
	if _, err := listProjectConversations(storage, "proj_missing"); err == nil {
		t.Fatal("unknown project must fail")
	}
}

func TestDeleteProjectHardDeletesConversations(t *testing.T) {
	storage := libraryTestStorage(t)
	libraryID := mustCreateLibrary(t, storage)
	project, err := createProject(storage, libraryID, "Doomed")
	if err != nil {
		t.Fatalf("createProject returned error: %v", err)
	}
	keepProject, err := createProject(storage, libraryID, "Kept")
	if err != nil {
		t.Fatalf("createProject returned error: %v", err)
	}

	writeSearchConversation(t, storage, "2026/08/conv_doomed",
		libraryConversationFixture("conv_doomed", "Doomed", "2026-08-01T10:00:00Z", project.ID),
		searchTurnFixture("turn_000001", "user", "2026-08-01T10:00:00Z",
			HistoryContent{Type: "image", ArtifactID: "img_doomed", Path: "artifacts/img_doomed.png", MimeType: "image/png"},
		),
	)
	writeAssetFile(t, storage, "2026/08/conv_doomed", "artifacts/img_doomed.png", string(minimalPNG))
	writeSearchConversation(t, storage, "2026/08/conv_kept",
		libraryConversationFixture("conv_kept", "Kept", "2026-08-01T10:00:00Z", keepProject.ID))

	doomedDir := filepath.Join(storage.History, "conversations", "2026", "08", "conv_doomed")

	// Warm the library asset cache first so the delete must invalidate it.
	if _, err := listLibraryAssets(storage, libraryID); err != nil {
		t.Fatalf("listLibraryAssets returned error: %v", err)
	}

	result, err := deleteProject(storage, project.ID)
	if err != nil {
		t.Fatalf("deleteProject returned error: %v", err)
	}
	if result.DeletedConversations != 1 || result.DeletedAssets != 1 {
		t.Fatalf("result = %+v, want 1 conversation / 1 asset", result)
	}
	if _, err := os.Stat(doomedDir); !os.IsNotExist(err) {
		t.Fatalf("conversation dir %s still on disk (stat err = %v)", doomedDir, err)
	}
	if _, _, err := findProject(storage, project.ID); err == nil {
		t.Fatal("project record still resolvable after delete")
	}
	if _, err := findLibrary(storage, libraryID); err != nil {
		t.Fatalf("library record must survive a project delete: %v", err)
	}

	// The kept project's conversation survives, and the folded assets no
	// longer include the deleted conversation's image.
	members, err := listProjectConversations(storage, keepProject.ID)
	if err != nil {
		t.Fatalf("listProjectConversations returned error: %v", err)
	}
	if len(members) != 1 || members[0].ID != "conv_kept" {
		t.Fatalf("kept project members = %+v", members)
	}
	assets, err := listLibraryAssets(storage, libraryID)
	if err != nil {
		t.Fatalf("listLibraryAssets returned error: %v", err)
	}
	if len(assets) != 0 {
		t.Fatalf("library assets after delete = %+v, want none (cache must not resurrect deleted media)", assets)
	}
	if _, err := deleteProject(storage, project.ID); err == nil {
		t.Fatal("deleting an already-deleted project must fail")
	}
}

func TestDeleteLibraryHardDeletesEverything(t *testing.T) {
	storage := libraryTestStorage(t)
	doomedLibrary := mustCreateLibrary(t, storage)
	otherLibrary := mustCreateLibrary(t, storage)
	doomedProject, err := createProject(storage, doomedLibrary, "Doomed")
	if err != nil {
		t.Fatalf("createProject returned error: %v", err)
	}
	otherProject, err := createProject(storage, otherLibrary, "Other")
	if err != nil {
		t.Fatalf("createProject returned error: %v", err)
	}
	writeSearchConversation(t, storage, "2026/08/conv_lib_doomed",
		libraryConversationFixture("conv_lib_doomed", "Doomed", "2026-08-01T10:00:00Z", doomedProject.ID))
	writeSearchConversation(t, storage, "2026/08/conv_lib_other",
		libraryConversationFixture("conv_lib_other", "Other", "2026-08-01T10:00:00Z", otherProject.ID))

	result, err := deleteLibrary(storage, doomedLibrary)
	if err != nil {
		t.Fatalf("deleteLibrary returned error: %v", err)
	}
	if result.DeletedProjects != 1 || result.DeletedConversations != 1 {
		t.Fatalf("result = %+v", result)
	}
	if _, err := findLibrary(storage, doomedLibrary); err == nil {
		t.Fatal("library record still resolvable after delete")
	}
	if _, err := getConversation(storage, "conv_lib_doomed"); err == nil {
		t.Fatal("member conversation still resolvable after library delete")
	}
	if _, err := getConversation(storage, "conv_lib_other"); err != nil {
		t.Fatalf("unrelated library's conversation must survive: %v", err)
	}
	if _, err := deleteLibrary(storage, doomedLibrary); err == nil {
		t.Fatal("deleting an already-deleted library must fail")
	}
}

func TestMoveConversationToProject(t *testing.T) {
	storage := libraryTestStorage(t)
	libraryID := mustCreateLibrary(t, storage)
	project, err := createProject(storage, libraryID, "Target")
	if err != nil {
		t.Fatalf("createProject returned error: %v", err)
	}
	writeSearchConversation(t, storage, "2026/08/conv_mover",
		libraryConversationFixture("conv_mover", "Mover", "2026-08-01T10:00:00Z", ""))

	moved, err := moveConversationToProject(storage, "conv_mover", project.ID)
	if err != nil {
		t.Fatalf("moveConversationToProject returned error: %v", err)
	}
	if moved.ProjectID != project.ID {
		t.Fatalf("moved summary ProjectID = %q, want %q", moved.ProjectID, project.ID)
	}
	detail, err := getConversation(storage, "conv_mover")
	if err != nil {
		t.Fatalf("getConversation returned error: %v", err)
	}
	if detail.Conversation.ProjectID != project.ID {
		t.Fatalf("record ProjectID = %q, want %q", detail.Conversation.ProjectID, project.ID)
	}

	// Empty project detaches back to standalone.
	detached, err := moveConversationToProject(storage, "conv_mover", "")
	if err != nil {
		t.Fatalf("detach returned error: %v", err)
	}
	if detached.ProjectID != "" {
		t.Fatalf("detached ProjectID = %q, want empty", detached.ProjectID)
	}
	if _, err := moveConversationToProject(storage, "conv_mover", "proj_missing"); err == nil {
		t.Fatal("moving to an unknown project must fail")
	}
	if _, err := moveConversationToProject(storage, "conv_missing", project.ID); err == nil {
		t.Fatal("moving an unknown conversation must fail")
	}
}

func TestResolveTurnProjectRules(t *testing.T) {
	storage := libraryTestStorage(t)
	libraryID := mustCreateLibrary(t, storage)
	project, err := createProject(storage, libraryID, "P")
	if err != nil {
		t.Fatalf("createProject returned error: %v", err)
	}
	config := defaultAppConfig()
	config.Storage = storage

	cases := []struct {
		name    string
		req     ChatRequest
		want    string
		wantErr bool
	}{
		{name: "turn 1 with a valid project pins it", req: ChatRequest{ProjectID: project.ID}, want: project.ID},
		{name: "turn 1 without a project is standalone", req: ChatRequest{}, want: ""},
		{name: "turn 1 with an unknown project fails", req: ChatRequest{ProjectID: "proj_missing"}, wantErr: true},
		{name: "turn 2+ ignores the request project entirely", req: ChatRequest{ConversationID: "conv_anything", ProjectID: "proj_missing"}, want: ""},
	}
	for _, tc := range cases {
		got, err := resolveTurnProject(config, tc.req)
		if tc.wantErr {
			if err == nil {
				t.Fatalf("%s: expected an error", tc.name)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: returned error: %v", tc.name, err)
		}
		if got != tc.want {
			t.Fatalf("%s: = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestTurn1PinsProjectIDAndTurn2IgnoresRequest(t *testing.T) {
	home := t.TempDir()
	config, _ := workspaceTestConfig(t, home)
	libraryID := mustCreateLibrary(t, config.Storage)
	project, err := createProject(config.Storage, libraryID, "Pinned")
	if err != nil {
		t.Fatalf("createProject returned error: %v", err)
	}

	if _, err := resolveTurnProject(config, ChatRequest{ProjectID: project.ID}); err != nil {
		t.Fatalf("resolveTurnProject returned error: %v", err)
	}
	id, err := writePendingChatConversation(config, ChatRequest{
		Model:     "m",
		ProjectID: project.ID,
		Messages:  []ChatMessage{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("writePendingChatConversation returned error: %v", err)
	}
	detail, err := getConversation(config.Storage, id)
	if err != nil {
		t.Fatalf("getConversation returned error: %v", err)
	}
	if detail.Conversation.ProjectID != project.ID {
		t.Fatalf("record ProjectID = %q, want %q", detail.Conversation.ProjectID, project.ID)
	}

	// Turn 2+ sends a bogus project on the request; the record must not move.
	if _, err := appendChatUserTurn(config, ChatRequest{
		ConversationID: id,
		ProjectID:      "proj_elsewhere",
		Model:          "m",
		Messages:       []ChatMessage{{Role: "user", Content: "again"}},
	}); err != nil {
		t.Fatalf("appendChatUserTurn returned error: %v", err)
	}
	detail, err = getConversation(config.Storage, id)
	if err != nil {
		t.Fatalf("getConversation returned error: %v", err)
	}
	if detail.Conversation.ProjectID != project.ID {
		t.Fatalf("turn-2 record ProjectID = %q, want still %q", detail.Conversation.ProjectID, project.ID)
	}

	// The summary carries membership so the sidebar can filter.
	summaries, err := listConversations(config.Storage)
	if err != nil {
		t.Fatalf("listConversations returned error: %v", err)
	}
	if len(summaries) != 1 || summaries[0].ProjectID != project.ID {
		t.Fatalf("summary = %+v, want ProjectID %q", summaries, project.ID)
	}
}

func TestListLibraryAssetsFoldsMembershipAndOrder(t *testing.T) {
	storage := libraryTestStorage(t)
	libraryID := mustCreateLibrary(t, storage)
	projectA, err := createProject(storage, libraryID, "A")
	if err != nil {
		t.Fatalf("createProject returned error: %v", err)
	}
	projectB, err := createProject(storage, libraryID, "B")
	if err != nil {
		t.Fatalf("createProject returned error: %v", err)
	}
	otherLibrary := mustCreateLibrary(t, storage)
	otherProject, err := createProject(storage, otherLibrary, "Other")
	if err != nil {
		t.Fatalf("createProject returned error: %v", err)
	}

	// Upload in A (older), generated in B (newer), plus non-members.
	writeSearchConversation(t, storage, "2026/08/conv_lib_a",
		libraryConversationFixture("conv_lib_a", "Uploads", "2026-08-01T10:00:00Z", projectA.ID),
		searchTurnFixture("turn_000001", "user", "2026-08-01T10:00:00Z",
			HistoryContent{Type: "image", ArtifactID: "img_up1", Path: "artifacts/img_up1.png", MimeType: "image/png"},
		),
	)
	writeAssetFile(t, storage, "2026/08/conv_lib_a", "artifacts/img_up1.png", string(minimalPNG))
	writeSearchConversation(t, storage, "2026/08/conv_lib_b",
		libraryConversationFixture("conv_lib_b", "Generated", "2026-08-02T10:00:00Z", projectB.ID),
		searchTurnFixture("turn_000001", "assistant", "2026-08-02T10:00:00Z",
			HistoryContent{Type: "video", ArtifactID: "vid_gen1", Path: "artifacts/vid_gen1.mp4", MimeType: "video/mp4"},
		),
	)
	writeAssetFile(t, storage, "2026/08/conv_lib_b", "artifacts/vid_gen1.mp4", string([]byte{0x00, 0x00, 0x00, 0x08, 'f', 't', 'y', 'p', 'm', 'p', '4', '2'}))
	writeSearchConversation(t, storage, "2026/08/conv_lib_standalone",
		libraryConversationFixture("conv_lib_standalone", "Outside", "2026-08-03T10:00:00Z", ""),
		searchTurnFixture("turn_000001", "assistant", "2026-08-03T10:00:00Z",
			HistoryContent{Type: "image", ArtifactID: "img_outside", Path: "artifacts/img_outside.png", MimeType: "image/png"},
		),
	)
	writeAssetFile(t, storage, "2026/08/conv_lib_standalone", "artifacts/img_outside.png", string(minimalPNG))
	writeSearchConversation(t, storage, "2026/08/conv_lib_other",
		libraryConversationFixture("conv_lib_other", "Elsewhere", "2026-08-04T10:00:00Z", otherProject.ID),
		searchTurnFixture("turn_000001", "assistant", "2026-08-04T10:00:00Z",
			HistoryContent{Type: "image", ArtifactID: "img_elsewhere", Path: "artifacts/img_elsewhere.png", MimeType: "image/png"},
		),
	)
	writeAssetFile(t, storage, "2026/08/conv_lib_other", "artifacts/img_elsewhere.png", string(minimalPNG))

	assets, err := listLibraryAssets(storage, libraryID)
	if err != nil {
		t.Fatalf("listLibraryAssets returned error: %v", err)
	}
	if len(assets) != 2 {
		t.Fatalf("asset count = %d, want 2 (standalone and other-library excluded): %+v", len(assets), assets)
	}
	// Newest-first display order.
	if assets[0].ID != "vid_gen1" || assets[1].ID != "img_up1" {
		t.Fatalf("order = [%s, %s], want [vid_gen1, img_up1]", assets[0].ID, assets[1].ID)
	}
	if assets[0].ConversationTitle != "Generated" || assets[1].ConversationTitle != "Uploads" {
		t.Fatalf("conversation titles = [%q, %q]", assets[0].ConversationTitle, assets[1].ConversationTitle)
	}
	if !strings.HasPrefix(assets[0].URL, "/atelier-artifact/") {
		t.Fatalf("URL = %q, want hydrated", assets[0].URL)
	}
	if _, err := listLibraryAssets(storage, "lib_missing"); err == nil {
		t.Fatal("unknown library must fail")
	}
}

func TestListLibraryAssetsRefoldsOnChange(t *testing.T) {
	storage := libraryTestStorage(t)
	libraryID := mustCreateLibrary(t, storage)
	project, err := createProject(storage, libraryID, "P")
	if err != nil {
		t.Fatalf("createProject returned error: %v", err)
	}
	conversation := libraryConversationFixture("conv_refold", "Refold", "2026-08-01T10:00:00Z", project.ID)
	writeSearchConversation(t, storage, "2026/08/conv_refold", conversation,
		searchTurnFixture("turn_000001", "user", "2026-08-01T10:00:00Z",
			HistoryContent{Type: "image", ArtifactID: "img_first", Path: "artifacts/img_first.png", MimeType: "image/png"},
		),
	)
	writeAssetFile(t, storage, "2026/08/conv_refold", "artifacts/img_first.png", string(minimalPNG))

	first, err := listLibraryAssets(storage, libraryID)
	if err != nil {
		t.Fatalf("listLibraryAssets returned error: %v", err)
	}
	if len(first) != 1 {
		t.Fatalf("first fold = %+v", first)
	}

	// Append a turn (and bump UpdatedAt) — the cache must not serve the stale fold.
	conversation.UpdatedAt = "2026-08-02T10:00:00Z"
	if err := writeJSONFile(filepath.Join(storage.History, "conversations", "2026", "08", "conv_refold", "conversation.json"), conversation); err != nil {
		t.Fatalf("writeJSONFile returned error: %v", err)
	}
	writeJSONFile(filepath.Join(storage.History, "conversations", "2026", "08", "conv_refold", "turns", "turn_000002.json"),
		searchTurnFixture("turn_000002", "assistant", "2026-08-02T10:00:00Z",
			HistoryContent{Type: "image", ArtifactID: "img_second", Path: "artifacts/img_second.png", MimeType: "image/png"},
		))
	writeAssetFile(t, storage, "2026/08/conv_refold", "artifacts/img_second.png", string(minimalPNG))

	second, err := listLibraryAssets(storage, libraryID)
	if err != nil {
		t.Fatalf("listLibraryAssets returned error: %v", err)
	}
	if len(second) != 2 {
		t.Fatalf("refold = %+v, want the new artifact (cache must key on UpdatedAt)", second)
	}
}

func TestResolveReferencedAssetsAcrossLibrary(t *testing.T) {
	storage := libraryTestStorage(t)
	libraryID := mustCreateLibrary(t, storage)
	project, err := createProject(storage, libraryID, "Shared")
	if err != nil {
		t.Fatalf("createProject returned error: %v", err)
	}

	// Conversation A holds the shared image; conversation B (same project)
	// cites it. Simulate B's turn-1 build: B's record is NOT on disk yet, so
	// only the fallback project ID can widen the resolution.
	writeSearchConversation(t, storage, "2026/08/conv_shared_a",
		libraryConversationFixture("conv_shared_a", "Source", "2026-08-01T10:00:00Z", project.ID),
		searchTurnFixture("turn_000001", "user", "2026-08-01T10:00:00Z",
			HistoryContent{Type: "image", ArtifactID: "img_shared", Path: "artifacts/img_shared.png", MimeType: "image/png", Width: 64, Height: 32},
		),
	)
	writeAssetFile(t, storage, "2026/08/conv_shared_a", "artifacts/img_shared.png", string(minimalPNG))

	got := resolveReferencedAssets(storage, "conv_shared_new", project.ID, []string{"img_shared"})
	if len(got.images) != 1 || len(got.entries) != 1 {
		t.Fatalf("cross-library mention resolved img:%d entries:%d, want 1/1", len(got.images), len(got.entries))
	}
	if !strings.HasPrefix(got.images[0], "data:image/") {
		t.Fatalf("resolved media = %q, want a data URL", got.images[0])
	}
	entry := got.entries[0]
	if !filepath.IsAbs(filepath.FromSlash(entry.Path)) {
		t.Fatalf("persisted entry Path = %q, want absolute (points into the owning conversation)", entry.Path)
	}
	if !strings.Contains(filepath.ToSlash(entry.Path), "conv_shared_a/artifacts/img_shared.png") {
		t.Fatalf("entry Path = %q, want the owning conversation's artifact", entry.Path)
	}
	if entry.ArtifactID != "img_shared" || entry.Width != 64 || entry.Height != 32 {
		t.Fatalf("entry = %+v, want provenance carried", entry)
	}

	// A standalone conversation (no project) must not widen: the same ID
	// resolves to nothing when there is no library context.
	standalone := resolveReferencedAssets(storage, "conv_shared_new", "", []string{"img_shared"})
	if len(standalone.entries) != 0 {
		t.Fatalf("standalone conversation resolved %+v, want nothing", standalone.entries)
	}

	// A project conversation with a local artifact sharing the ID wins locally:
	// the library pass is only a fallback for IDs the conversation doesn't have.
	writeSearchConversation(t, storage, "2026/08/conv_shared_b",
		libraryConversationFixture("conv_shared_b", "Citer", "2026-08-02T10:00:00Z", project.ID),
		searchTurnFixture("turn_000001", "user", "2026-08-02T10:00:00Z",
			HistoryContent{Type: "image", ArtifactID: "img_shared", Path: "artifacts/img_shared.png", MimeType: "image/png"},
		),
	)
	writeAssetFile(t, storage, "2026/08/conv_shared_b", "artifacts/img_shared.png", string(minimalPNG))
	localWins := resolveReferencedAssets(storage, "conv_shared_b", "", []string{"img_shared"})
	if len(localWins.entries) != 1 {
		t.Fatalf("local resolution entries = %+v", localWins.entries)
	}
	// Local entries keep their stored relative Path — the library pass never
	// touches assets the conversation already holds.
	if localWins.entries[0].Path != "artifacts/img_shared.png" || filepath.IsAbs(localWins.entries[0].Path) {
		t.Fatalf("local entry Path = %q, want the conversation's own relative path", localWins.entries[0].Path)
	}
}

func TestCrossConversationReferenceStaysReadable(t *testing.T) {
	storage := libraryTestStorage(t)
	libraryID := mustCreateLibrary(t, storage)
	project, err := createProject(storage, libraryID, "Flow")
	if err != nil {
		t.Fatalf("createProject returned error: %v", err)
	}
	writeSearchConversation(t, storage, "2026/08/conv_flow_a",
		libraryConversationFixture("conv_flow_a", "Source", "2026-08-01T10:00:00Z", project.ID),
		searchTurnFixture("turn_000001", "user", "2026-08-01T10:00:00Z",
			HistoryContent{Type: "image", ArtifactID: "img_flow", Path: "artifacts/img_flow.png", MimeType: "image/png"},
		),
	)
	writeAssetFile(t, storage, "2026/08/conv_flow_a", "artifacts/img_flow.png", string(minimalPNG))

	// B cites A's image; the persisted absolute-path entry must re-read and
	// hydrate from B's own record on the next turn — the cross-conversation
	// reference becomes part of B's walkable media history.
	entry := resolveReferencedAssets(storage, "conv_flow_b", project.ID, []string{"img_flow"}).entries[0]
	writeSearchConversation(t, storage, "2026/08/conv_flow_b",
		libraryConversationFixture("conv_flow_b", "Citer", "2026-08-02T10:00:00Z", project.ID),
		searchTurnFixture("turn_000001", "user", "2026-08-02T10:00:00Z",
			HistoryContent{Type: "text", Text: "use @img_flow"},
			entry,
		),
	)

	detail, err := getConversation(storage, "conv_flow_b")
	if err != nil {
		t.Fatalf("getConversation returned error: %v", err)
	}
	found := false
	for _, content := range detail.Turns[0].Content {
		if content.ArtifactID == "img_flow" {
			found = true
			if !strings.HasPrefix(content.Text, "/atelier-artifact/") {
				t.Fatalf("hydrated text = %q, want an /atelier-artifact URL", content.Text)
			}
			// The next-turn fallback walker reads it through B's conversation
			// ID even though the bytes live in A's directory.
			dataURL, err := readArtifactAsDataURL(storage, "conv_flow_b", content)
			if err != nil {
				t.Fatalf("readArtifactAsDataURL on the absolute-path entry: %v", err)
			}
			if !strings.HasPrefix(dataURL, "data:image/") {
				t.Fatalf("re-read = %q, want a data URL", dataURL)
			}
		}
	}
	if !found {
		t.Fatalf("absolute-path reference not persisted on the citing turn: %+v", detail.Turns[0].Content)
	}

	// The library fold sees it exactly once (ID dedupe across A and B).
	assets, err := listLibraryAssets(storage, libraryID)
	if err != nil {
		t.Fatalf("listLibraryAssets returned error: %v", err)
	}
	if len(assets) != 1 {
		t.Fatalf("library fold = %+v, want the shared image exactly once", assets)
	}
}
