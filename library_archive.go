package main

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Library export/import archives a library — its project records, every member
// conversation (including soft-deleted ones, which a later hard delete would
// destroy), and all artifacts, uploaded and generated alike — into a single
// checksummed zip for cold storage, and restores it into the configured storage
// root. The per-turn model provenance (HistoryTurn.Model/Provider plus the
// persisted harness run) needs no special handling: turn files ride along
// verbatim, so an archive records exactly which models produced what.
//
// Layout (paths slash-separated, date buckets flattened — import re-derives
// them from each record's own CreatedAt):
//
//	manifest.json
//	library.json
//	projects/<projectID>/project.json
//	conversations/<conversationID>/{conversation.json,turns/*,artifacts/*}
//
// Portability: cross-conversation references and per-conversation workspaces
// persist as absolute paths on the exporting machine, so the manifest carries
// the exporter's history root and import rewrites those prefixes (falling back
// to the default tool workspace when an archived workspace doesn't exist on
// the target machine — the same sanctioned backfill as legacy SchemaVersion 1
// records). Everything else copies byte-verbatim.

// currentLibraryArchiveFormatVersion is bumped whenever the archive layout or
// manifest gains a field. Import refuses anything newer than itself; older
// manifests keep importing (fields are additive).
const currentLibraryArchiveFormatVersion = 1

// libraryArchiveExtension is the archive's document extension. The container
// is a plain zip, so archives stay inspectable regardless of extension.
const libraryArchiveExtension = ".atelierlib"

// archiveManifestPath is the manifest's archive-relative path. Every other
// entry is listed in the manifest; the manifest itself is not (it is the
// authority, not a payload).
const archiveManifestPath = "manifest.json"

// LibraryArchiveManifest is the archive's table of contents and integrity
// record. Files lists every payload entry with its size and SHA-256 so import
// can verify the whole archive before writing a single byte; Missing records
// referenced-but-absent assets so the gap survives the round trip honestly
// (imported turns keep referencing absent files — the existing dangling-asset
// semantics).
type LibraryArchiveManifest struct {
	FormatVersion int       `json:"formatVersion"`
	AppVersion    string    `json:"appVersion"`
	ExportedAt    string    `json:"exportedAt"`
	Library       Library   `json:"library"`
	Projects      []Project `json:"projects"`
	// OriginalHistoryRoot is the exporter's Storage.History — the prefix
	// absolute cross-conversation references (and any workspace under the
	// storage root) were written against, and the key import rewrites them by.
	OriginalHistoryRoot string                     `json:"originalHistoryRoot"`
	Counts              LibraryArchiveCounts       `json:"counts"`
	Files               []LibraryArchiveFile       `json:"files"`
	Missing             []LibraryArchiveMissingRef `json:"missing,omitempty"`
}

type LibraryArchiveCounts struct {
	Projects      int   `json:"projects"`
	Conversations int   `json:"conversations"`
	Assets        int   `json:"assets"`
	Bytes         int64 `json:"bytes"`
}

// LibraryArchiveFile is one payload entry's integrity record. Path is
// slash-separated and archive-relative.
type LibraryArchiveFile struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// LibraryArchiveMissingRef is one referenced artifact that was absent on disk
// at export time. The entry stays in the archived turns (it is still part of
// history) but has no payload file; after import it renders as a listed,
// unpreviewable asset.
type LibraryArchiveMissingRef struct {
	ConversationID string `json:"conversationId"`
	TurnID         string `json:"turnId,omitempty"`
	Path           string `json:"path"`
}

// LibraryExportPlan is the pre-flight answer for the export confirm UI: what
// the archive will contain, how big it is, and which referenced assets are
// missing on disk. MissingAssets never blocks export — the UI confirms, the
// manifest records.
type LibraryExportPlan struct {
	LibraryID     string                     `json:"libraryId"`
	LibraryName   string                     `json:"libraryName"`
	Projects      int                        `json:"projects"`
	Conversations int                        `json:"conversations"`
	Assets        int                        `json:"assets"`
	Bytes         int64                      `json:"bytes"`
	MissingAssets []LibraryArchiveMissingRef `json:"missingAssets,omitempty"`
}

type LibraryExportResult struct {
	Path          string `json:"path"`
	Conversations int    `json:"conversations"`
	Assets        int    `json:"assets"`
	Bytes         int64  `json:"bytes"`
}

type LibraryImportResult struct {
	Library       LibrarySummary `json:"library"`
	Projects      int            `json:"projects"`
	Conversations int            `json:"conversations"`
	Assets        int            `json:"assets"`
	Bytes         int64          `json:"bytes"`
	MissingAssets int            `json:"missingAssets"`
}

// libraryExportScan is the shared member walk behind inspect and export — one
// shape so the dry-run's counts can never drift from what the archive actually
// contains. Conversations are sorted by ID for deterministic archives.
type libraryExportScan struct {
	library       Library
	projects      []Project
	conversations []scannedConversation
	assets        int
	bytes         int64
	missing       []LibraryArchiveMissingRef
}

type scannedConversation struct {
	id     string
	dir    string
	record HistoryConversation
	files  []scannedFile
}

type scannedFile struct {
	rel  string // slash-separated, relative to the conversation directory
	size int64
}

// scanLibraryExport walks a library's members — soft-deleted conversations
// included, matching what deleteLibrary would destroy — and collects every
// regular file under each conversation directory (the whole directory is
// archived verbatim, not just referenced artifacts) plus the referenced-but-
// absent set. Hard-fails on unreadable records, mirroring the walker.
func scanLibraryExport(storage ConfigStorage, libraryID string) (libraryExportScan, error) {
	library, err := findLibrary(storage, libraryID)
	if err != nil {
		return libraryExportScan{}, err
	}
	projects, err := listProjectRecords(storage, libraryID)
	if err != nil {
		return libraryExportScan{}, err
	}
	projectIDs := make(map[string]bool, len(projects))
	for _, project := range projects {
		projectIDs[project.ID] = true
	}

	scan := libraryExportScan{library: library, projects: projects}
	err = forEachProjectConversationRecord(storage, projectIDs, true, func(path string, conversation HistoryConversation) error {
		dir := filepath.Dir(path)
		files, err := scanConversationFiles(dir)
		if err != nil {
			return err
		}
		scan.conversations = append(scan.conversations, scannedConversation{
			id:     conversation.ID,
			dir:    dir,
			record: conversation,
			files:  files,
		})
		for _, file := range files {
			scan.bytes += file.size
			if strings.HasPrefix(file.rel, "artifacts/") {
				scan.assets++
			}
		}
		return nil
	})
	if err != nil {
		return libraryExportScan{}, err
	}
	sort.Slice(scan.conversations, func(i, j int) bool { return scan.conversations[i].id < scan.conversations[j].id })

	// Missing-reference detection runs with the full member set in hand: an
	// absolute reference is only satisfiable when it points inside one of the
	// member directories, since @-mentions resolve within the library and the
	// archive never spills over into other libraries' files.
	memberDirs := make([]string, len(scan.conversations))
	for i, conversation := range scan.conversations {
		memberDirs[i] = conversation.dir
	}
	for _, conversation := range scan.conversations {
		missing, err := missingConversationRefs(conversation, memberDirs)
		if err != nil {
			return libraryExportScan{}, err
		}
		scan.missing = append(scan.missing, missing...)
	}
	return scan, nil
}

// scanConversationFiles lists every regular file under a conversation
// directory, slash-relative to it.
func scanConversationFiles(dir string) ([]scannedFile, error) {
	files := []scannedFile{}
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		files = append(files, scannedFile{rel: filepath.ToSlash(rel), size: info.Size()})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}

// missingConversationRefs finds the conversation's referenced artifacts that
// will not be in the archive: files absent on disk, or present but outside
// every member directory (only reachable through hand-edited or foreign
// records — mentions resolve within the library). Deleted turns are skipped,
// matching how assets are derived; their files still ride along in the copy.
func missingConversationRefs(conversation scannedConversation, memberDirs []string) ([]LibraryArchiveMissingRef, error) {
	missing := []LibraryArchiveMissingRef{}
	for _, file := range conversation.files {
		if !strings.HasPrefix(file.rel, "turns/") || !strings.HasSuffix(file.rel, ".json") {
			continue
		}
		var turn HistoryTurn
		if err := readJSONFile(filepath.Join(conversation.dir, filepath.FromSlash(file.rel)), &turn); err != nil {
			return nil, err
		}
		if turn.DeletedAt != "" {
			continue
		}
		for _, content := range turn.Content {
			path := strings.TrimSpace(content.Path)
			if path == "" || strings.HasPrefix(path, "data:") {
				continue
			}
			resolved := filepath.FromSlash(path)
			if !filepath.IsAbs(resolved) {
				resolved = filepath.Join(conversation.dir, resolved)
			}
			if _, err := os.Stat(resolved); err != nil {
				missing = append(missing, LibraryArchiveMissingRef{ConversationID: conversation.id, TurnID: turn.ID, Path: path})
				continue
			}
			if !pathInsideAnyDir(resolved, memberDirs) {
				missing = append(missing, LibraryArchiveMissingRef{ConversationID: conversation.id, TurnID: turn.ID, Path: path})
			}
		}
	}
	return missing, nil
}

func pathInsideAnyDir(path string, dirs []string) bool {
	for _, dir := range dirs {
		if strings.HasPrefix(path, dir+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// inspectLibraryExport is the export dry-run: everything scanLibraryExport
// sees, shaped for the confirm UI. No archive is written and no dialog opens.
func inspectLibraryExport(storage ConfigStorage, libraryID string) (LibraryExportPlan, error) {
	scan, err := scanLibraryExport(storage, libraryID)
	if err != nil {
		return LibraryExportPlan{}, err
	}
	return LibraryExportPlan{
		LibraryID:     scan.library.ID,
		LibraryName:   scan.library.Name,
		Projects:      len(scan.projects),
		Conversations: len(scan.conversations),
		Assets:        scan.assets,
		Bytes:         scan.bytes,
		MissingAssets: scan.missing,
	}, nil
}

// exportLibrary writes the library archive to destPath. onProgress, when set,
// fires once per conversation (phase "write") so the UI can show coarse
// progress on large libraries. The manifest is written last — it carries every
// payload entry's hash, computed while copying.
func exportLibrary(storage ConfigStorage, libraryID, destPath string, onProgress func(phase string, done, total int)) (LibraryExportResult, error) {
	scan, err := scanLibraryExport(storage, libraryID)
	if err != nil {
		return LibraryExportResult{}, err
	}
	if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
		return LibraryExportResult{}, err
	}
	file, err := os.Create(destPath)
	if err != nil {
		return LibraryExportResult{}, err
	}
	defer file.Close()

	zipWriter := zip.NewWriter(file)
	files := []LibraryArchiveFile{}
	addRaw := func(rel string, data []byte) error {
		entry, err := addArchiveBytes(zipWriter, rel, data)
		if err != nil {
			return err
		}
		files = append(files, entry)
		return nil
	}
	addFile := func(rel, source string) error {
		entry, err := addArchiveFile(zipWriter, rel, source)
		if err != nil {
			return err
		}
		files = append(files, entry)
		return nil
	}

	// The record entries are copied as raw disk bytes; the manifest's struct
	// copies exist for import-time rewriting, but the payload stays verbatim.
	libraryJSON, err := os.ReadFile(libraryPath(storage, libraryID))
	if err != nil {
		return LibraryExportResult{}, err
	}
	if err := addRaw("library.json", libraryJSON); err != nil {
		return LibraryExportResult{}, err
	}
	sortedProjects := append([]Project(nil), scan.projects...)
	sort.Slice(sortedProjects, func(i, j int) bool { return sortedProjects[i].ID < sortedProjects[j].ID })
	for _, project := range sortedProjects {
		projectJSON, err := os.ReadFile(projectPath(storage, libraryID, project.ID))
		if err != nil {
			return LibraryExportResult{}, err
		}
		if err := addRaw("projects/"+project.ID+"/project.json", projectJSON); err != nil {
			return LibraryExportResult{}, err
		}
	}
	for i, conversation := range scan.conversations {
		for _, scanned := range conversation.files {
			if err := addFile("conversations/"+conversation.id+"/"+scanned.rel, filepath.Join(conversation.dir, filepath.FromSlash(scanned.rel))); err != nil {
				return LibraryExportResult{}, err
			}
		}
		if onProgress != nil {
			onProgress("write", i+1, len(scan.conversations))
		}
	}

	manifest := LibraryArchiveManifest{
		FormatVersion:       currentLibraryArchiveFormatVersion,
		AppVersion:          version,
		ExportedAt:          time.Now().Format(time.RFC3339),
		Library:             scan.library,
		Projects:            scan.projects,
		OriginalHistoryRoot: storage.History,
		Counts: LibraryArchiveCounts{
			Projects:      len(scan.projects),
			Conversations: len(scan.conversations),
			Assets:        scan.assets,
			Bytes:         scan.bytes,
		},
		Files:   files,
		Missing: scan.missing,
	}
	manifestJSON, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return LibraryExportResult{}, err
	}
	manifestJSON = append(manifestJSON, '\n')
	if _, err := addArchiveBytes(zipWriter, archiveManifestPath, manifestJSON); err != nil {
		return LibraryExportResult{}, err
	}
	if err := zipWriter.Close(); err != nil {
		return LibraryExportResult{}, err
	}
	if err := file.Close(); err != nil {
		return LibraryExportResult{}, err
	}
	return LibraryExportResult{
		Path:          destPath,
		Conversations: len(scan.conversations),
		Assets:        scan.assets,
		Bytes:         scan.bytes,
	}, nil
}

// addArchiveBytes writes a byte payload as a deflated entry. JSON gets
// deflated; media goes through addArchiveFile's Store — artifacts are
// already-compressed formats, and deflate would only burn CPU on the way into
// a cold-storage archive.
func addArchiveBytes(zipWriter *zip.Writer, rel string, data []byte) (LibraryArchiveFile, error) {
	writer, err := zipWriter.CreateHeader(&zip.FileHeader{Name: rel, Method: zip.Deflate, Modified: time.Now()})
	if err != nil {
		return LibraryArchiveFile{}, err
	}
	if _, err := writer.Write(data); err != nil {
		return LibraryArchiveFile{}, err
	}
	sum := sha256.Sum256(data)
	return LibraryArchiveFile{Path: rel, Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:])}, nil
}

// addArchiveFile streams one on-disk file into the archive, hashing while
// copying so no second read pass is needed.
func addArchiveFile(zipWriter *zip.Writer, rel, source string) (LibraryArchiveFile, error) {
	info, err := os.Stat(source)
	if err != nil {
		return LibraryArchiveFile{}, err
	}
	method := uint16(zip.Store)
	if strings.EqualFold(filepath.Ext(rel), ".json") {
		method = zip.Deflate
	}
	header := &zip.FileHeader{
		Name:     rel,
		Method:   method,
		Modified: info.ModTime(),
	}
	writer, err := zipWriter.CreateHeader(header)
	if err != nil {
		return LibraryArchiveFile{}, err
	}
	sourceFile, err := os.Open(source)
	if err != nil {
		return LibraryArchiveFile{}, err
	}
	defer sourceFile.Close()
	hasher := sha256.New()
	if _, err := io.Copy(io.MultiWriter(writer, hasher), sourceFile); err != nil {
		return LibraryArchiveFile{}, err
	}
	return LibraryArchiveFile{Path: rel, Size: info.Size(), SHA256: hex.EncodeToString(hasher.Sum(nil))}, nil
}

// importLibraryArchive restores an archive into storage. defaultWorkspace is
// the importer's resolved tool-workspace root — what archived conversations
// fall back to when their pinned workspace doesn't exist on this machine.
// onProgress fires per verified entry (phase "verify") and per written
// conversation (phase "write").
//
// The whole archive is verified before the first write: a corrupt or doctored
// archive must fail with a named entry, never as a half-written history tree.
// Import is create-only — a library or conversation ID collision means the
// archive is already present and the import is refused wholesale.
func importLibraryArchive(storage ConfigStorage, defaultWorkspace, archivePath string, onProgress func(phase string, done, total int)) (LibraryImportResult, error) {
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return LibraryImportResult{}, err
	}
	defer reader.Close()

	entries := map[string]*zip.File{}
	for _, entry := range reader.File {
		if entry.FileInfo().IsDir() {
			continue
		}
		entries[entry.Name] = entry
	}
	manifestEntry, ok := entries[archiveManifestPath]
	if !ok {
		return LibraryImportResult{}, fmt.Errorf("%s is not an Atelier library archive (missing manifest)", filepath.Base(archivePath))
	}
	var manifest LibraryArchiveManifest
	if err := readArchiveEntryJSON(manifestEntry, &manifest); err != nil {
		return LibraryImportResult{}, err
	}
	if manifest.FormatVersion > currentLibraryArchiveFormatVersion {
		return LibraryImportResult{}, fmt.Errorf("archive format version %d is newer than this app supports (%d); update Atelier first", manifest.FormatVersion, currentLibraryArchiveFormatVersion)
	}
	if strings.TrimSpace(manifest.Library.ID) == "" {
		return LibraryImportResult{}, errors.New("archive manifest has no library record")
	}

	// Bidirectional cross-check: every manifest file must be present, and
	// every payload entry must be listed — a truncated or edited archive fails
	// here before any hashing. Path safety rides along: manifest paths become
	// filesystem joins, so they must be clean relatives.
	manifestPaths := make(map[string]bool, len(manifest.Files))
	for _, archived := range manifest.Files {
		if !safeArchiveRelPath(archived.Path) {
			return LibraryImportResult{}, fmt.Errorf("archive manifest lists an unsafe path %q", archived.Path)
		}
		if _, ok := entries[archived.Path]; !ok {
			return LibraryImportResult{}, fmt.Errorf("archive is missing manifest entry %q", archived.Path)
		}
		manifestPaths[archived.Path] = true
	}
	for name := range entries {
		if name != archiveManifestPath && !manifestPaths[name] {
			return LibraryImportResult{}, fmt.Errorf("archive entry %q is not listed in the manifest", name)
		}
	}
	for i, archived := range manifest.Files {
		sum, size, err := hashArchiveEntry(entries[archived.Path])
		if err != nil {
			return LibraryImportResult{}, err
		}
		if size != archived.Size || sum != archived.SHA256 {
			return LibraryImportResult{}, fmt.Errorf("archive entry %q failed its integrity check (corrupt archive)", archived.Path)
		}
		if onProgress != nil {
			onProgress("verify", i+1, len(manifest.Files))
		}
	}

	conversations, err := groupArchiveConversations(manifest.Files)
	if err != nil {
		return LibraryImportResult{}, err
	}
	if err := refuseArchiveCollisions(storage, manifest.Library.ID, conversations); err != nil {
		return LibraryImportResult{}, err
	}

	// A same-named library under a different ID is the only name conflict;
	// the imported record is suffixed rather than merged — import is
	// create-only. An unreadable libraries dir skips the suffix check: the
	// worst case is a duplicate name, cosmetic next to the ID refusal above.
	library := manifest.Library
	existingLibraries, _ := listLibraryRecords(storage)
	for _, existing := range existingLibraries {
		if existing.ID != library.ID && strings.EqualFold(existing.Name, library.Name) {
			library.Name = compactString(library.Name+" (imported)", 72)
			break
		}
	}
	if err := os.MkdirAll(filepath.Join(libraryDir(storage, library.ID), "projects"), 0755); err != nil {
		return LibraryImportResult{}, err
	}
	if err := writeJSONFile(libraryPath(storage, library.ID), library); err != nil {
		return LibraryImportResult{}, err
	}
	projectSummaries := []ProjectSummary{}
	for _, project := range manifest.Projects {
		project.LibraryID = library.ID
		if err := os.MkdirAll(projectDir(storage, library.ID, project.ID), 0755); err != nil {
			return LibraryImportResult{}, err
		}
		if err := writeJSONFile(projectPath(storage, library.ID, project.ID), project); err != nil {
			return LibraryImportResult{}, err
		}
		projectSummaries = append(projectSummaries, projectSummaryFrom(project))
	}

	ids := make([]string, 0, len(conversations))
	for id := range conversations {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for index, id := range ids {
		if err := importArchiveConversation(storage, defaultWorkspace, manifest.OriginalHistoryRoot, id, conversations[id], entries); err != nil {
			return LibraryImportResult{}, err
		}
		if onProgress != nil {
			onProgress("write", index+1, len(ids))
		}
	}
	sort.Slice(projectSummaries, func(i, j int) bool {
		return lessByName(projectSummaries[i].Name, projectSummaries[i].ID, projectSummaries[j].Name, projectSummaries[j].ID)
	})
	return LibraryImportResult{
		Library:       librarySummaryFrom(library, projectSummaries),
		Projects:      len(projectSummaries),
		Conversations: len(ids),
		Assets:        manifest.Counts.Assets,
		Bytes:         manifest.Counts.Bytes,
		MissingAssets: len(manifest.Missing),
	}, nil
}

// importArchiveConversation materializes one archived conversation at its
// re-derived date bucket. conversation.json is always re-marshalled (the
// workspace fallback applies to it); turn files are written byte-verbatim
// unless they contain the exporter's history root, in which case their
// absolute reference paths are rewritten.
func importArchiveConversation(storage ConfigStorage, defaultWorkspace, originalHistoryRoot, conversationID string, files []LibraryArchiveFile, entries map[string]*zip.File) error {
	var record HistoryConversation
	if err := readArchiveEntryJSON(entries["conversations/"+conversationID+"/conversation.json"], &record); err != nil {
		return err
	}
	if workspace := strings.TrimSpace(record.Workspace); workspace != "" {
		if rewritten := rewriteArchivePath(workspace, originalHistoryRoot, storage.History); rewritten != workspace {
			record.Workspace = rewritten
		} else if _, err := os.Stat(filepath.FromSlash(workspace)); err != nil {
			record.Workspace = defaultWorkspace
		}
	}
	createdAt, err := time.Parse(time.RFC3339, record.CreatedAt)
	if err != nil {
		return fmt.Errorf("conversation %s has an unreadable CreatedAt: %w", conversationID, err)
	}
	targetDir := conversationDir(storage, createdAt, conversationID)
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		return err
	}
	if err := writeJSONFile(filepath.Join(targetDir, "conversation.json"), record); err != nil {
		return err
	}
	for _, archived := range files {
		if archived.Path == "conversation.json" {
			continue
		}
		data, err := readArchiveEntryBytes(entries["conversations/"+conversationID+"/"+archived.Path])
		if err != nil {
			return err
		}
		if strings.HasPrefix(archived.Path, "turns/") {
			data, err = rewriteArchiveTurn(data, originalHistoryRoot, storage.History)
			if err != nil {
				return err
			}
		}
		target := filepath.Join(targetDir, filepath.FromSlash(archived.Path))
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		if err := os.WriteFile(target, data, 0644); err != nil {
			return err
		}
	}
	return nil
}

// rewriteArchiveTurn rewrites a turn's absolute content paths from the
// exporter's history root to the importer's. The fast path returns the
// original bytes untouched — most turns contain no absolute references and
// stay byte-identical to what was exported.
func rewriteArchiveTurn(data []byte, fromRoot, toRoot string) ([]byte, error) {
	if fromRoot == "" || !strings.Contains(string(data), fromRoot) {
		return data, nil
	}
	var turn HistoryTurn
	if err := json.Unmarshal(data, &turn); err != nil {
		return nil, err
	}
	changed := false
	for i := range turn.Content {
		if rewritten := rewriteArchivePath(turn.Content[i].Path, fromRoot, toRoot); rewritten != turn.Content[i].Path {
			turn.Content[i].Path = rewritten
			changed = true
		}
	}
	if !changed {
		return data, nil
	}
	rewritten, err := json.MarshalIndent(turn, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(rewritten, '\n'), nil
}

// rewriteArchivePath re-prefixes an absolute path recorded against the
// exporter's history root so it points at the same conversation tree on the
// importing machine. Anything else passes through untouched.
func rewriteArchivePath(path, fromRoot, toRoot string) string {
	if path == "" || fromRoot == "" {
		return path
	}
	normalized := filepath.FromSlash(path)
	prefix := fromRoot + string(filepath.Separator)
	if !strings.HasPrefix(normalized, prefix) {
		return path
	}
	return filepath.Join(toRoot, strings.TrimPrefix(normalized, prefix))
}

// groupArchiveConversations buckets the manifest's payload files by the
// conversation they belong to, keeping each bucket's internal order. Non-
// conversation payloads are validated to be exactly the record shapes the
// exporter writes (library.json and project.json), never imported from bytes —
// the manifest structs are the write-time source of truth.
func groupArchiveConversations(files []LibraryArchiveFile) (map[string][]LibraryArchiveFile, error) {
	conversations := map[string][]LibraryArchiveFile{}
	for _, archived := range files {
		if archived.Path == "library.json" {
			continue
		}
		if strings.HasPrefix(archived.Path, "projects/") && strings.HasSuffix(archived.Path, "/project.json") {
			continue
		}
		rest, ok := strings.CutPrefix(archived.Path, "conversations/")
		if !ok {
			return nil, fmt.Errorf("archive manifest lists an unexpected entry %q", archived.Path)
		}
		conversationID, remainder, found := strings.Cut(rest, "/")
		if !found || conversationID == "" || remainder == "" {
			return nil, fmt.Errorf("archive manifest lists a malformed conversation entry %q", archived.Path)
		}
		// conversation.json is part of the bucket but handled separately from
		// the payload loop; keep it in the slice so the cross-check stays
		// exhaustive and importArchiveConversation can skip it.
		conversations[conversationID] = append(conversations[conversationID], LibraryArchiveFile{Path: remainder, Size: archived.Size, SHA256: archived.SHA256})
	}
	return conversations, nil
}

// refuseArchiveCollisions blocks the already-imported case: same library
// directory or any shared conversation ID means the archive's content is (at
// least partly) present, and a wholesale refusal beats a silent partial merge.
func refuseArchiveCollisions(storage ConfigStorage, libraryID string, conversations map[string][]LibraryArchiveFile) error {
	if _, err := os.Stat(libraryDir(storage, libraryID)); err == nil {
		return fmt.Errorf("a library with id %s already exists — this archive appears to be already imported", libraryID)
	}
	archiveIDs := make(map[string]bool, len(conversations))
	for id := range conversations {
		archiveIDs[id] = true
	}
	collides := ""
	err := forEachConversationRecord(storage, func(path string, conversation HistoryConversation) error {
		if archiveIDs[conversation.ID] {
			collides = conversation.ID
		}
		return nil
	})
	if err != nil {
		return err
	}
	if collides != "" {
		return fmt.Errorf("conversation %s from this archive already exists in your history — this library appears to be already imported", collides)
	}
	return nil
}

func safeArchiveRelPath(rel string) bool {
	if rel == "" || strings.HasPrefix(rel, "/") || strings.Contains(rel, "\\") {
		return false
	}
	for _, segment := range strings.Split(rel, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
	}
	return true
}

func readArchiveEntryJSON(entry *zip.File, target any) error {
	data, err := readArchiveEntryBytes(entry)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, target)
}

func readArchiveEntryBytes(entry *zip.File) ([]byte, error) {
	if entry == nil {
		return nil, fmt.Errorf("archive entry is missing")
	}
	reader, err := entry.Open()
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	return io.ReadAll(reader)
}

func hashArchiveEntry(entry *zip.File) (string, int64, error) {
	reader, err := entry.Open()
	if err != nil {
		return "", 0, err
	}
	defer reader.Close()
	hasher := sha256.New()
	size, err := io.Copy(hasher, reader)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(hasher.Sum(nil)), size, nil
}
