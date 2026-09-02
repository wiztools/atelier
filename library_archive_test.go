package main

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// archiveConversationFixture is libraryConversationFixture plus the
// per-conversation workspace, the two record fields the archive round trip
// exercises.
func archiveConversationFixture(id, projectID, workspace, createdAt string) HistoryConversation {
	conversation := libraryConversationFixture(id, "Conversation "+id, createdAt, projectID)
	conversation.Workspace = workspace
	return conversation
}

// writeArchiveConversation persists a member conversation the way the app
// does — date-bucketed dir, conversation.json, turns/, artifacts/ — and
// returns the conversation directory (the anchor for absolute references).
func writeArchiveConversation(t *testing.T, storage ConfigStorage, conversation HistoryConversation, turns []HistoryTurn, artifacts map[string][]byte) string {
	t.Helper()
	createdAt, err := time.Parse(time.RFC3339, conversation.CreatedAt)
	if err != nil {
		t.Fatalf("fixture CreatedAt %q is not RFC3339: %v", conversation.CreatedAt, err)
	}
	dir := conversationDir(storage, createdAt, conversation.ID)
	if err := os.MkdirAll(filepath.Join(dir, "turns"), 0755); err != nil {
		t.Fatalf("MkdirAll turns: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "artifacts"), 0755); err != nil {
		t.Fatalf("MkdirAll artifacts: %v", err)
	}
	if err := writeJSONFile(filepath.Join(dir, "conversation.json"), conversation); err != nil {
		t.Fatalf("write conversation.json: %v", err)
	}
	for _, turn := range turns {
		if err := writeJSONFile(filepath.Join(dir, "turns", turn.ID+".json"), turn); err != nil {
			t.Fatalf("write %s: %v", turn.ID, err)
		}
	}
	for name, data := range artifacts {
		if err := os.WriteFile(filepath.Join(dir, "artifacts", name), data, 0644); err != nil {
			t.Fatalf("write artifact %s: %v", name, err)
		}
	}
	return dir
}

func readImportedConversation(t *testing.T, storage ConfigStorage, conversationID string) HistoryConversation {
	t.Helper()
	path, err := findConversationPath(storage, conversationID)
	if err != nil {
		t.Fatalf("findConversationPath(%s): %v", conversationID, err)
	}
	var conversation HistoryConversation
	if err := readJSONFile(path, &conversation); err != nil {
		t.Fatalf("read conversation.json: %v", err)
	}
	return conversation
}

func readImportedTurn(t *testing.T, storage ConfigStorage, conversationID, turnID string) HistoryTurn {
	t.Helper()
	path, err := findConversationPath(storage, conversationID)
	if err != nil {
		t.Fatalf("findConversationPath(%s): %v", conversationID, err)
	}
	var turn HistoryTurn
	if err := readJSONFile(filepath.Join(filepath.Dir(path), "turns", turnID+".json"), &turn); err != nil {
		t.Fatalf("read turn %s: %v", turnID, err)
	}
	return turn
}

// readArchiveManifest loads a manifest straight out of an archive, for
// asserting what export actually recorded.
func readArchiveManifest(t *testing.T, archivePath string) LibraryArchiveManifest {
	t.Helper()
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		t.Fatalf("OpenReader: %v", err)
	}
	defer reader.Close()
	for _, entry := range reader.File {
		if entry.Name != archiveManifestPath {
			continue
		}
		var manifest LibraryArchiveManifest
		if err := readArchiveEntryJSON(entry, &manifest); err != nil {
			t.Fatalf("read manifest: %v", err)
		}
		return manifest
	}
	t.Fatalf("archive %s has no manifest", archivePath)
	return LibraryArchiveManifest{}
}

func archiveEntryNames(t *testing.T, archivePath string) []string {
	t.Helper()
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		t.Fatalf("OpenReader: %v", err)
	}
	defer reader.Close()
	names := []string{}
	for _, entry := range reader.File {
		if !entry.FileInfo().IsDir() {
			names = append(names, entry.Name)
		}
	}
	return names
}

// rewriteArchiveEntries rebuilds an archive from its decompressed entries,
// applying mutate to the name→bytes map first. Content hashes in the manifest
// still describe the original bytes, which is exactly the corruption the
// import tests need.
func rewriteArchiveEntries(t *testing.T, archivePath string, mutate func(map[string][]byte)) {
	t.Helper()
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		t.Fatalf("OpenReader: %v", err)
	}
	order := []string{}
	data := map[string][]byte{}
	for _, entry := range reader.File {
		if entry.FileInfo().IsDir() {
			continue
		}
		order = append(order, entry.Name)
		data[entry.Name], err = readArchiveEntryBytes(entry)
		if err != nil {
			t.Fatalf("read entry %s: %v", entry.Name, err)
		}
	}
	reader.Close()

	mutate(data)
	writer, err := os.Create(archivePath)
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}
	zipWriter := zip.NewWriter(writer)
	written := map[string]bool{}
	write := func(name string, bytes []byte) {
		if _, err := addArchiveBytes(zipWriter, name, bytes); err != nil {
			t.Fatalf("rewrite entry %s: %v", name, err)
		}
		written[name] = true
	}
	for _, name := range order {
		if bytes, ok := data[name]; ok {
			write(name, bytes)
		}
	}
	// Entries the mutation smuggled in past the original order — the
	// unlisted-entry case relies on them actually being written.
	for name, bytes := range data {
		if !written[name] {
			write(name, bytes)
		}
	}
	if err := zipWriter.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close archive: %v", err)
	}
}

func mustExportLibrary(t *testing.T, storage ConfigStorage, libraryID string) string {
	t.Helper()
	archivePath := filepath.Join(t.TempDir(), "export.atelierlib")
	if _, err := exportLibrary(storage, libraryID, archivePath, nil); err != nil {
		t.Fatalf("exportLibrary returned error: %v", err)
	}
	return archivePath
}

func mustImportLibrary(t *testing.T, storage ConfigStorage, archivePath string) LibraryImportResult {
	t.Helper()
	result, err := importLibraryArchive(storage, "/default/workspace", archivePath, nil)
	if err != nil {
		t.Fatalf("importLibraryArchive returned error: %v", err)
	}
	return result
}

func TestLibraryArchiveRoundTrip(t *testing.T) {
	source := libraryTestStorage(t)
	libraryID := mustCreateLibrary(t, source)
	if _, err := renameLibrary(source, libraryID, "Round Trip"); err != nil {
		t.Fatalf("renameLibrary returned error: %v", err)
	}
	projectA, err := createProject(source, libraryID, "Project A")
	if err != nil {
		t.Fatalf("createProject returned error: %v", err)
	}
	projectB, err := createProject(source, libraryID, "Project B")
	if err != nil {
		t.Fatalf("createProject returned error: %v", err)
	}

	createdAt := "2025-11-02T10:00:00Z"
	turnA := searchTurnFixture("turn_000001", "user", createdAt,
		HistoryContent{Type: "text", Text: "make me an image"},
		HistoryContent{Type: "image", Path: "artifacts/img_one.png", MimeType: "image/png", ArtifactID: "img_one"},
	)
	conversationA := archiveConversationFixture("conv_aaa", projectA.ID, "/srv/workspaces/aaa", createdAt)
	dirA := writeArchiveConversation(t, source, conversationA, []HistoryTurn{turnA}, map[string][]byte{
		"img_one.png": []byte("png-bytes-one"),
		"img_two.png": []byte("png-bytes-two"),
	})
	turnB := searchTurnFixture("turn_000001", "assistant", createdAt,
		HistoryContent{Type: "image", Path: "artifacts/img_b.png", ArtifactID: "img_b"},
	)
	conversationB := archiveConversationFixture("conv_bbb", projectB.ID, "", createdAt)
	conversationB.DeletedAt = "2025-12-01T00:00:00Z"
	dirB := writeArchiveConversation(t, source, conversationB, []HistoryTurn{turnB}, map[string][]byte{
		"img_b.png": []byte("png-bytes-b"),
	})
	// A standalone conversation is not the library's to archive.
	writeArchiveConversation(t, source, archiveConversationFixture("conv_standalone", "", "", createdAt), nil,
		map[string][]byte{"img_standalone.png": []byte("nope")})

	var expectedBytes int64
	for _, dir := range []string{dirA, dirB} {
		err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
			if err != nil || entry.IsDir() {
				return err
			}
			info, infoErr := entry.Info()
			if infoErr != nil {
				return infoErr
			}
			expectedBytes += info.Size()
			return nil
		})
		if err != nil {
			t.Fatalf("walk fixture dir: %v", err)
		}
	}

	plan, err := inspectLibraryExport(source, libraryID)
	if err != nil {
		t.Fatalf("inspectLibraryExport returned error: %v", err)
	}
	if plan.LibraryName != "Round Trip" || plan.Projects != 2 || plan.Conversations != 2 || plan.Assets != 3 || plan.Bytes != expectedBytes {
		t.Fatalf("plan = %+v", plan)
	}
	if len(plan.MissingAssets) != 0 {
		t.Fatalf("plan.MissingAssets = %+v, want none", plan.MissingAssets)
	}

	archivePath := filepath.Join(t.TempDir(), "round-trip.atelierlib")
	result, err := exportLibrary(source, libraryID, archivePath, nil)
	if err != nil {
		t.Fatalf("exportLibrary returned error: %v", err)
	}
	if result.Path != archivePath || result.Conversations != 2 || result.Assets != 3 || result.Bytes != expectedBytes {
		t.Fatalf("result = %+v", result)
	}

	names := archiveEntryNames(t, archivePath)
	has := map[string]bool{}
	for _, name := range names {
		has[name] = true
		if strings.Contains(name, "conv_standalone") {
			t.Fatalf("archive contains standalone conversation entry %q", name)
		}
	}
	expectedEntries := []string{
		archiveManifestPath,
		"library.json",
		"projects/" + projectA.ID + "/project.json",
		"projects/" + projectB.ID + "/project.json",
		"conversations/conv_aaa/conversation.json",
		"conversations/conv_aaa/turns/turn_000001.json",
		"conversations/conv_aaa/artifacts/img_one.png",
		"conversations/conv_aaa/artifacts/img_two.png",
		"conversations/conv_bbb/conversation.json",
		"conversations/conv_bbb/turns/turn_000001.json",
		"conversations/conv_bbb/artifacts/img_b.png",
	}
	for _, expected := range expectedEntries {
		if !has[expected] {
			t.Fatalf("archive is missing entry %q; got %v", expected, names)
		}
	}

	target := libraryTestStorage(t)
	imported := mustImportLibrary(t, target, archivePath)
	if imported.Library.Name != "Round Trip" || imported.Projects != 2 || imported.Conversations != 2 || imported.Assets != 3 || imported.Bytes != expectedBytes || imported.MissingAssets != 0 {
		t.Fatalf("imported = %+v", imported)
	}
	if len(imported.Library.Projects) != 2 || imported.Library.Projects[0].Name != "Project A" {
		t.Fatalf("imported library projects = %+v", imported.Library.Projects)
	}

	libraries, err := listLibraries(target)
	if err != nil {
		t.Fatalf("listLibraries returned error: %v", err)
	}
	if len(libraries) != 1 || libraries[0].ID != libraryID || libraries[0].Name != "Round Trip" {
		t.Fatalf("imported libraries = %+v", libraries)
	}

	// The imported conversation lands at its re-derived date bucket with
	// byte-identical artifacts and its workspace preserved (it exists on this
	// machine only in the sense that stat decides — here it doesn't, so the
	// default root applies).
	importedA := readImportedConversation(t, target, "conv_aaa")
	if importedA.ProjectID != projectA.ID {
		t.Fatalf("imported conversation projectId = %q", importedA.ProjectID)
	}
	if importedA.Workspace != "/default/workspace" {
		t.Fatalf("imported conversation workspace = %q, want the default root backfill", importedA.Workspace)
	}
	pathA, err := findConversationPath(target, "conv_aaa")
	if err != nil {
		t.Fatalf("findConversationPath returned error: %v", err)
	}
	if !strings.HasSuffix(filepath.ToSlash(pathA), "conversations/2025/11/conv_aaa/conversation.json") {
		t.Fatalf("imported conversation path = %q, want the 2025/11 bucket", pathA)
	}
	artifact, err := os.ReadFile(filepath.Join(filepath.Dir(pathA), "artifacts", "img_one.png"))
	if err != nil {
		t.Fatalf("read imported artifact: %v", err)
	}
	if string(artifact) != "png-bytes-one" {
		t.Fatalf("imported artifact bytes = %q", artifact)
	}

	// Soft-deleted members ride along with their DeletedAt intact.
	importedB := readImportedConversation(t, target, "conv_bbb")
	if importedB.DeletedAt != "2025-12-01T00:00:00Z" {
		t.Fatalf("imported soft-deleted conversation deletedAt = %q", importedB.DeletedAt)
	}

	if _, err := findConversationPath(target, "conv_standalone"); err == nil {
		t.Fatal("standalone conversation must not be imported")
	}
}

func TestLibraryArchiveRewritesAbsoluteReferences(t *testing.T) {
	source := libraryTestStorage(t)
	libraryID := mustCreateLibrary(t, source)
	project, err := createProject(source, libraryID, "P")
	if err != nil {
		t.Fatalf("createProject returned error: %v", err)
	}
	createdAt := "2025-06-15T09:30:00Z"

	existingWorkspace := t.TempDir() // exists on the target (same machine), so it is kept
	ownerTurn := searchTurnFixture("turn_000001", "user", createdAt,
		HistoryContent{Type: "image", Path: "artifacts/shared.png", ArtifactID: "img_shared"},
	)
	owner := archiveConversationFixture("conv_owner", project.ID, existingWorkspace, createdAt)
	ownerDir := writeArchiveConversation(t, source, owner, []HistoryTurn{ownerTurn}, map[string][]byte{
		"shared.png": []byte("shared-bytes"),
	})
	// A cross-conversation @-mention reference: an absolute path into the
	// owner's artifacts dir, exactly as resolveReferencedAssets persists it.
	refTurn := searchTurnFixture("turn_000001", "user", createdAt,
		HistoryContent{Type: "image", Path: filepath.ToSlash(filepath.Join(ownerDir, "artifacts", "shared.png")), ArtifactID: "img_shared"},
	)
	ref := archiveConversationFixture("conv_ref", project.ID, "/definitely/not/on/target", createdAt)
	writeArchiveConversation(t, source, ref, []HistoryTurn{refTurn}, nil)

	archivePath := mustExportLibrary(t, source, libraryID)
	if manifest := readArchiveManifest(t, archivePath); manifest.OriginalHistoryRoot != source.History {
		t.Fatalf("manifest.OriginalHistoryRoot = %q, want %q", manifest.OriginalHistoryRoot, source.History)
	}

	target := libraryTestStorage(t)
	mustImportLibrary(t, target, archivePath)

	importedRef := readImportedTurn(t, target, "conv_ref", "turn_000001")
	expectedPath := filepath.Join(conversationDir(target, mustParseTime(t, createdAt), "conv_owner"), "artifacts", "shared.png")
	if importedRef.Content[0].Path != filepath.ToSlash(expectedPath) {
		t.Fatalf("rewritten reference = %q, want %q", importedRef.Content[0].Path, expectedPath)
	}
	if _, err := os.Stat(expectedPath); err != nil {
		t.Fatalf("rewritten reference does not resolve: %v", err)
	}

	// The archived workspace that doesn't exist here adopts the default root;
	// the one that exists is kept verbatim.
	if workspace := readImportedConversation(t, target, "conv_ref").Workspace; workspace != "/default/workspace" {
		t.Fatalf("conv_ref workspace = %q, want default backfill", workspace)
	}
	if workspace := readImportedConversation(t, target, "conv_owner").Workspace; workspace != existingWorkspace {
		t.Fatalf("conv_owner workspace = %q, want %q", workspace, existingWorkspace)
	}

	// Turns without absolute references stay byte-identical to the source.
	sourceTurnBytes, err := os.ReadFile(filepath.Join(ownerDir, "turns", "turn_000001.json"))
	if err != nil {
		t.Fatalf("read source turn: %v", err)
	}
	targetPath, err := findConversationPath(target, "conv_owner")
	if err != nil {
		t.Fatalf("findConversationPath returned error: %v", err)
	}
	targetTurnBytes, err := os.ReadFile(filepath.Join(filepath.Dir(targetPath), "turns", "turn_000001.json"))
	if err != nil {
		t.Fatalf("read target turn: %v", err)
	}
	if string(sourceTurnBytes) != string(targetTurnBytes) {
		t.Fatalf("relative-only turn was rewritten:\nsource: %s\ntarget: %s", sourceTurnBytes, targetTurnBytes)
	}
}

func TestLibraryArchiveRecordsMissingAssets(t *testing.T) {
	source := libraryTestStorage(t)
	libraryID := mustCreateLibrary(t, source)
	project, err := createProject(source, libraryID, "P")
	if err != nil {
		t.Fatalf("createProject returned error: %v", err)
	}
	createdAt := "2025-03-10T12:00:00Z"

	// Referenced but never written on disk.
	external := filepath.Join(t.TempDir(), "elsewhere.png")
	if err := os.WriteFile(external, []byte("external"), 0644); err != nil {
		t.Fatalf("write external artifact: %v", err)
	}
	turn := searchTurnFixture("turn_000001", "user", createdAt,
		HistoryContent{Type: "image", Path: "artifacts/gone.png", ArtifactID: "img_gone"},
		HistoryContent{Type: "image", Path: "artifacts/present.png", ArtifactID: "img_present"},
		HistoryContent{Type: "image", Path: filepath.ToSlash(external), ArtifactID: "img_external"},
	)
	conversation := archiveConversationFixture("conv_gaps", project.ID, "", createdAt)
	writeArchiveConversation(t, source, conversation, []HistoryTurn{turn}, map[string][]byte{
		"present.png": []byte("present-bytes"),
	})

	plan, err := inspectLibraryExport(source, libraryID)
	if err != nil {
		t.Fatalf("inspectLibraryExport returned error: %v", err)
	}
	if len(plan.MissingAssets) != 2 {
		t.Fatalf("plan.MissingAssets = %+v, want the absent and the external reference", plan.MissingAssets)
	}
	missingPaths := map[string]bool{}
	for _, missing := range plan.MissingAssets {
		if missing.ConversationID != "conv_gaps" || missing.TurnID != "turn_000001" {
			t.Fatalf("missing ref = %+v, want conv_gaps/turn_000001 provenance", missing)
		}
		missingPaths[missing.Path] = true
	}
	if !missingPaths["artifacts/gone.png"] || !missingPaths[filepath.ToSlash(external)] {
		t.Fatalf("missing paths = %v", missingPaths)
	}

	archivePath := mustExportLibrary(t, source, libraryID)
	if manifest := readArchiveManifest(t, archivePath); len(manifest.Missing) != 2 {
		t.Fatalf("manifest.Missing = %+v, want two entries", manifest.Missing)
	}

	target := libraryTestStorage(t)
	imported := mustImportLibrary(t, target, archivePath)
	if imported.MissingAssets != 2 || imported.Assets != 1 {
		t.Fatalf("imported = %+v", imported)
	}
	// The dangling references survive verbatim — the imported turns keep
	// pointing at absent files, which is the existing missing-asset semantics.
	importedTurn := readImportedTurn(t, target, "conv_gaps", "turn_000001")
	if importedTurn.Content[0].Path != "artifacts/gone.png" {
		t.Fatalf("dangling reference = %q", importedTurn.Content[0].Path)
	}
}

func TestLibraryArchiveImportVerifiesIntegrity(t *testing.T) {
	build := func(t *testing.T) (ConfigStorage, string) {
		source := libraryTestStorage(t)
		libraryID := mustCreateLibrary(t, source)
		project, err := createProject(source, libraryID, "P")
		if err != nil {
			t.Fatalf("createProject returned error: %v", err)
		}
		createdAt := "2025-01-20T08:00:00Z"
		turn := searchTurnFixture("turn_000001", "user", createdAt,
			HistoryContent{Type: "image", Path: "artifacts/img.png", ArtifactID: "img"},
		)
		conversation := archiveConversationFixture("conv_hash", project.ID, "", createdAt)
		writeArchiveConversation(t, source, conversation, []HistoryTurn{turn}, map[string][]byte{"img.png": []byte("hash-me")})
		return source, mustExportLibrary(t, source, libraryID)
	}

	tampered := "conversations/conv_hash/artifacts/img.png"

	t.Run("tampered entry", func(t *testing.T) {
		_, archivePath := build(t)
		rewriteArchiveEntries(t, archivePath, func(data map[string][]byte) {
			data[tampered] = []byte("tampered")
		})
		target := libraryTestStorage(t)
		_, err := importLibraryArchive(target, "/default/workspace", archivePath, nil)
		if err == nil || !strings.Contains(err.Error(), tampered) {
			t.Fatalf("err = %v, want integrity failure naming %q", err, tampered)
		}
		mustBeUntouched(t, target)
	})

	t.Run("missing entry", func(t *testing.T) {
		_, archivePath := build(t)
		rewriteArchiveEntries(t, archivePath, func(data map[string][]byte) {
			delete(data, tampered)
		})
		target := libraryTestStorage(t)
		_, err := importLibraryArchive(target, "/default/workspace", archivePath, nil)
		if err == nil || !strings.Contains(err.Error(), "missing manifest entry") {
			t.Fatalf("err = %v, want the cross-check to name the missing entry", err)
		}
		mustBeUntouched(t, target)
	})

	t.Run("unlisted entry", func(t *testing.T) {
		_, archivePath := build(t)
		rewriteArchiveEntries(t, archivePath, func(data map[string][]byte) {
			data["sneaky.txt"] = []byte("not in the manifest")
		})
		target := libraryTestStorage(t)
		_, err := importLibraryArchive(target, "/default/workspace", archivePath, nil)
		if err == nil || !strings.Contains(err.Error(), "not listed in the manifest") {
			t.Fatalf("err = %v, want the cross-check to reject the unlisted entry", err)
		}
		mustBeUntouched(t, target)
	})

	t.Run("future format version", func(t *testing.T) {
		archivePath := filepath.Join(t.TempDir(), "future.atelierlib")
		writeTestArchive(t, archivePath, map[string][]byte{archiveManifestPath: []byte(`{"formatVersion":99}`)})
		target := libraryTestStorage(t)
		_, err := importLibraryArchive(target, "/default/workspace", archivePath, nil)
		if err == nil || !strings.Contains(err.Error(), "newer") {
			t.Fatalf("err = %v, want a format-version refusal", err)
		}
		mustBeUntouched(t, target)
	})

	t.Run("not an archive", func(t *testing.T) {
		archivePath := filepath.Join(t.TempDir(), "plain.atelierlib")
		writeTestArchive(t, archivePath, map[string][]byte{"README": []byte("hello")})
		target := libraryTestStorage(t)
		_, err := importLibraryArchive(target, "/default/workspace", archivePath, nil)
		if err == nil || !strings.Contains(err.Error(), "not an Atelier library archive") {
			t.Fatalf("err = %v, want a manifest-missing refusal", err)
		}
		mustBeUntouched(t, target)
	})
}

// mustBeUntouched asserts the verify-then-write contract: a failed import
// leaves no library record and no conversation behind.
func mustBeUntouched(t *testing.T, storage ConfigStorage) {
	t.Helper()
	entries, err := os.ReadDir(storage.Libraries)
	if err != nil {
		t.Fatalf("ReadDir libraries: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("failed import left %d library entries behind", len(entries))
	}
	conversations := 0
	err = filepath.WalkDir(filepath.Join(storage.History, "conversations"), func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && filepath.Base(path) == "conversation.json" {
			conversations++
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk conversations: %v", err)
	}
	if conversations != 0 {
		t.Fatalf("failed import left %d conversations behind", conversations)
	}
}

func writeTestArchive(t *testing.T, archivePath string, data map[string][]byte) {
	t.Helper()
	writer, err := os.Create(archivePath)
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}
	zipWriter := zip.NewWriter(writer)
	for name, bytes := range data {
		if _, err := addArchiveBytes(zipWriter, name, bytes); err != nil {
			t.Fatalf("write entry %s: %v", name, err)
		}
	}
	if err := zipWriter.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close archive: %v", err)
	}
}

func TestLibraryArchiveRefusesDoubleImport(t *testing.T) {
	source := libraryTestStorage(t)
	libraryID := mustCreateLibrary(t, source)
	project, err := createProject(source, libraryID, "P")
	if err != nil {
		t.Fatalf("createProject returned error: %v", err)
	}
	createdAt := "2025-02-05T16:00:00Z"
	conversation := archiveConversationFixture("conv_twice", project.ID, "", createdAt)
	writeArchiveConversation(t, source, conversation, nil, nil)

	archivePath := mustExportLibrary(t, source, libraryID)
	target := libraryTestStorage(t)
	mustImportLibrary(t, target, archivePath)
	if _, err := importLibraryArchive(target, "/default/workspace", archivePath, nil); err == nil {
		t.Fatal("second import of the same archive must be refused")
	}
	libraries, err := listLibraries(target)
	if err != nil {
		t.Fatalf("listLibraries returned error: %v", err)
	}
	if len(libraries) != 1 {
		t.Fatalf("libraries after refused import = %d, want 1", len(libraries))
	}
}

func TestLibraryArchiveImportNameCollision(t *testing.T) {
	source := libraryTestStorage(t)
	sourceLibrary := mustCreateLibrary(t, source)
	if _, err := renameLibrary(source, sourceLibrary, "Same Name"); err != nil {
		t.Fatalf("renameLibrary returned error: %v", err)
	}
	archivePath := mustExportLibrary(t, source, sourceLibrary)

	target := libraryTestStorage(t)
	if _, err := createLibrary(target, "Same Name"); err != nil {
		t.Fatalf("createLibrary returned error: %v", err)
	}
	imported := mustImportLibrary(t, target, archivePath)
	if imported.Library.Name != "Same Name (imported)" {
		t.Fatalf("imported library name = %q", imported.Library.Name)
	}
	libraries, err := listLibraries(target)
	if err != nil {
		t.Fatalf("listLibraries returned error: %v", err)
	}
	if len(libraries) != 2 {
		t.Fatalf("libraries = %d, want the original plus the import", len(libraries))
	}
}

func TestExportLibraryRefusedWhileMemberStreams(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	config := defaultAppConfig()
	if err := writeAppConfig(config); err != nil {
		t.Fatalf("writeAppConfig returned error: %v", err)
	}
	storage := config.Storage
	if err := ensureStorageDirs(storage); err != nil {
		t.Fatalf("ensureStorageDirs returned error: %v", err)
	}
	libraryID := mustCreateLibrary(t, storage)
	project, err := createProject(storage, libraryID, "P")
	if err != nil {
		t.Fatalf("createProject returned error: %v", err)
	}
	writeArchiveConversation(t, storage, archiveConversationFixture("conv_streaming", project.ID, "", "2026-01-05T08:00:00Z"), nil, nil)

	app := NewApp()
	app.streamsMu.Lock()
	app.streams["req-test"] = func() {}
	app.streamConversations["req-test"] = "conv_streaming"
	app.streamsMu.Unlock()

	if _, err := app.ExportLibrary(libraryID); err == nil || !strings.Contains(err.Error(), "still running") {
		t.Fatalf("ExportLibrary err = %v, want the streaming refusal", err)
	}

	// The pre-flight inspect is read-only and safe mid-stream.
	plan, err := app.InspectLibraryExport(libraryID)
	if err != nil {
		t.Fatalf("InspectLibraryExport returned error: %v", err)
	}
	if plan.Conversations != 1 || plan.Projects != 1 {
		t.Fatalf("plan = %+v", plan)
	}
}

func mustParseTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatalf("parse %q: %v", value, err)
	}
	return parsed
}
