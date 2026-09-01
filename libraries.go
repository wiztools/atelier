package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// currentLibrarySchemaVersion is bumped whenever Library or Project gains a
// field or changes shape. There is no backfill path yet — records at version 1
// are the original shape.
const currentLibrarySchemaVersion = 1

// Library is the top-level container in the Final Cut-inspired organization
// model: libraries hold projects, projects hold conversations. Records live at
// <Storage.Libraries>/<id>/library.json. A library never lists its projects'
// conversations — membership is a ProjectID on the conversation record, so the
// conversation tree stays the single source of truth for what a project (and
// therefore a library) contains. Library-level assets are derived the same way
// (see listLibraryAssets in assets.go); no media is ever copied or moved into
// library storage.
type Library struct {
	SchemaVersion int    `json:"schemaVersion"`
	ID            string `json:"id"`
	Name          string `json:"name"`
	CreatedAt     string `json:"createdAt"`
	UpdatedAt     string `json:"updatedAt"`
}

// Project is a conversation container within a library. Records live at
// <Storage.Libraries>/<libraryId>/projects/<id>/project.json. A conversation
// belongs to a project via HistoryConversation.ProjectID, pinned at creation
// and immutable afterwards (like the per-conversation workspace).
type Project struct {
	SchemaVersion int    `json:"schemaVersion"`
	ID            string `json:"id"`
	LibraryID     string `json:"libraryId"`
	Name          string `json:"name"`
	CreatedAt     string `json:"createdAt"`
	UpdatedAt     string `json:"updatedAt"`
}

// ProjectSummary is the API-facing project record for the sidebar tree.
type ProjectSummary struct {
	ID        string `json:"id"`
	LibraryID string `json:"libraryId"`
	Name      string `json:"name"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
}

// LibrarySummary is a library plus its projects, the shape ListLibraries
// returns so the sidebar can render the whole tree from one call.
type LibrarySummary struct {
	ID        string           `json:"id"`
	Name      string           `json:"name"`
	CreatedAt string           `json:"createdAt"`
	UpdatedAt string           `json:"updatedAt"`
	Projects  []ProjectSummary `json:"projects"`
}

// DeleteProjectResult reports what a hard project delete removed. Deleting a
// project is destructive by design — Final Cut's trash — so the counts give
// the confirm UI something honest to say.
type DeleteProjectResult struct {
	DeletedConversations int `json:"deletedConversations"`
	DeletedAssets        int `json:"deletedAssets"`
}

// DeleteLibraryResult is the library-level sibling of DeleteProjectResult.
type DeleteLibraryResult struct {
	DeletedProjects      int `json:"deletedProjects"`
	DeletedConversations int `json:"deletedConversations"`
	DeletedAssets        int `json:"deletedAssets"`
}

func libraryDir(storage ConfigStorage, libraryID string) string {
	return filepath.Join(storage.Libraries, libraryID)
}

func libraryPath(storage ConfigStorage, libraryID string) string {
	return filepath.Join(libraryDir(storage, libraryID), "library.json")
}

func projectDir(storage ConfigStorage, libraryID, projectID string) string {
	return filepath.Join(libraryDir(storage, libraryID), "projects", projectID)
}

func projectPath(storage ConfigStorage, libraryID, projectID string) string {
	return filepath.Join(projectDir(storage, libraryID, projectID), "project.json")
}

// normalizeContainerName trims and collapses whitespace and caps the length —
// the shared normalization for library and project names. Unlike conversation
// titles there is no generated-text cleanup ("Title:" prefixes and the like);
// names are always user-authored, and empty is the caller's error to raise.
func normalizeContainerName(name string) string {
	return compactString(strings.Join(strings.Fields(name), " "), 72)
}

// requireContainerName is the create/rename entry check shared by libraries
// and projects: normalize, and reject empty naming the container kind.
func requireContainerName(name, kind string) (string, error) {
	normalizedName := normalizeContainerName(name)
	if normalizedName == "" {
		return "", fmt.Errorf("%s name is required", kind)
	}
	return normalizedName, nil
}

// lessByName orders name-bearing records (libraries, projects) for
// deterministic sidebar rendering: case-insensitive name, then ID.
func lessByName(nameI, idI, nameJ, idJ string) bool {
	if strings.ToLower(nameI) == strings.ToLower(nameJ) {
		return idI < idJ
	}
	return strings.ToLower(nameI) < strings.ToLower(nameJ)
}

// findLibrary loads one library record by ID. The ID is the directory name, so
// this is a direct read — no scan.
func findLibrary(storage ConfigStorage, libraryID string) (Library, error) {
	var library Library
	if strings.TrimSpace(libraryID) == "" {
		return Library{}, errors.New("library id is required")
	}
	if err := readJSONFile(libraryPath(storage, libraryID), &library); err != nil {
		return Library{}, fmt.Errorf("library %s not found", libraryID)
	}
	return library, nil
}

// listLibraryRecords reads every library record under Storage.Libraries,
// skipping unreadable entries (a half-written or foreign directory must not
// take the whole sidebar down). Ordering is applied by the summary builder.
func listLibraryRecords(storage ConfigStorage) ([]Library, error) {
	entries, err := os.ReadDir(storage.Libraries)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []Library{}, nil
		}
		return nil, err
	}
	libraries := []Library{}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		var library Library
		if err := readJSONFile(libraryPath(storage, entry.Name()), &library); err != nil {
			continue
		}
		if library.ID == "" {
			library.ID = entry.Name()
		}
		libraries = append(libraries, library)
	}
	return libraries, nil
}

// listProjectRecords reads every project record of one library, tolerant of
// unreadable entries like listLibraryRecords.
func listProjectRecords(storage ConfigStorage, libraryID string) ([]Project, error) {
	entries, err := os.ReadDir(filepath.Join(libraryDir(storage, libraryID), "projects"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []Project{}, nil
		}
		return nil, err
	}
	projects := []Project{}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		var project Project
		if err := readJSONFile(projectPath(storage, libraryID, entry.Name()), &project); err != nil {
			continue
		}
		if project.ID == "" {
			project.ID = entry.Name()
		}
		if project.LibraryID == "" {
			project.LibraryID = libraryID
		}
		projects = append(projects, project)
	}
	return projects, nil
}

// findProject locates a project by ID across all libraries. Project IDs are
// globally unique (proj_<hex>), but the record layout nests them under their
// library, so finding one without knowing its library means scanning — a
// bounded read of every project.json, the same walk-and-fold idiom the
// conversation tree uses.
func findProject(storage ConfigStorage, projectID string) (Project, string, error) {
	if strings.TrimSpace(projectID) == "" {
		return Project{}, "", errors.New("project id is required")
	}
	libraries, err := listLibraryRecords(storage)
	if err != nil {
		return Project{}, "", err
	}
	for _, library := range libraries {
		projects, err := listProjectRecords(storage, library.ID)
		if err != nil {
			continue
		}
		for _, project := range projects {
			if project.ID == projectID {
				return project, projectPath(storage, library.ID, project.ID), nil
			}
		}
	}
	return Project{}, "", fmt.Errorf("project %s not found", projectID)
}

// libraryProjectIDs returns the set of project IDs belonging to a library —
// the membership filter for library-scoped conversation walks.
func libraryProjectIDs(storage ConfigStorage, libraryID string) (map[string]bool, error) {
	if _, err := findLibrary(storage, libraryID); err != nil {
		return nil, err
	}
	projects, err := listProjectRecords(storage, libraryID)
	if err != nil {
		return nil, err
	}
	ids := make(map[string]bool, len(projects))
	for _, project := range projects {
		ids[project.ID] = true
	}
	return ids, nil
}

func librarySummaryFrom(library Library, projects []ProjectSummary) LibrarySummary {
	if projects == nil {
		projects = []ProjectSummary{}
	}
	return LibrarySummary{
		ID:        library.ID,
		Name:      library.Name,
		CreatedAt: library.CreatedAt,
		UpdatedAt: library.UpdatedAt,
		Projects:  projects,
	}
}

func projectSummaryFrom(project Project) ProjectSummary {
	return ProjectSummary{
		ID:        project.ID,
		LibraryID: project.LibraryID,
		Name:      project.Name,
		CreatedAt: project.CreatedAt,
		UpdatedAt: project.UpdatedAt,
	}
}

// sortLibrarySummaries orders libraries for deterministic sidebar rendering.
func sortLibrarySummaries(libraries []LibrarySummary) {
	sort.Slice(libraries, func(i, j int) bool {
		return lessByName(libraries[i].Name, libraries[i].ID, libraries[j].Name, libraries[j].ID)
	})
}

func createLibrary(storage ConfigStorage, name string) (LibrarySummary, error) {
	normalizedName, err := requireContainerName(name, "library")
	if err != nil {
		return LibrarySummary{}, err
	}
	now := time.Now().Format(time.RFC3339)
	library := Library{
		SchemaVersion: currentLibrarySchemaVersion,
		ID:            randomID("lib"),
		Name:          normalizedName,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if err := os.MkdirAll(filepath.Join(libraryDir(storage, library.ID), "projects"), 0755); err != nil {
		return LibrarySummary{}, err
	}
	if err := writeJSONFile(libraryPath(storage, library.ID), library); err != nil {
		return LibrarySummary{}, err
	}
	return librarySummaryFrom(library, nil), nil
}

func renameLibrary(storage ConfigStorage, libraryID, name string) (LibrarySummary, error) {
	library, err := findLibrary(storage, libraryID)
	if err != nil {
		return LibrarySummary{}, err
	}
	normalizedName, err := requireContainerName(name, "library")
	if err != nil {
		return LibrarySummary{}, err
	}
	library.Name = normalizedName
	library.UpdatedAt = time.Now().Format(time.RFC3339)
	if err := writeJSONFile(libraryPath(storage, library.ID), library); err != nil {
		return LibrarySummary{}, err
	}
	return librarySummary(storage, library)
}

func createProject(storage ConfigStorage, libraryID, name string) (ProjectSummary, error) {
	if _, err := findLibrary(storage, libraryID); err != nil {
		return ProjectSummary{}, err
	}
	normalizedName, err := requireContainerName(name, "project")
	if err != nil {
		return ProjectSummary{}, err
	}
	now := time.Now().Format(time.RFC3339)
	project := Project{
		SchemaVersion: currentLibrarySchemaVersion,
		ID:            randomID("proj"),
		LibraryID:     libraryID,
		Name:          normalizedName,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if err := os.MkdirAll(projectDir(storage, libraryID, project.ID), 0755); err != nil {
		return ProjectSummary{}, err
	}
	if err := writeJSONFile(projectPath(storage, libraryID, project.ID), project); err != nil {
		return ProjectSummary{}, err
	}
	return projectSummaryFrom(project), nil
}

func renameProject(storage ConfigStorage, projectID, name string) (ProjectSummary, error) {
	project, path, err := findProject(storage, projectID)
	if err != nil {
		return ProjectSummary{}, err
	}
	normalizedName, err := requireContainerName(name, "project")
	if err != nil {
		return ProjectSummary{}, err
	}
	project.Name = normalizedName
	project.UpdatedAt = time.Now().Format(time.RFC3339)
	if err := writeJSONFile(path, project); err != nil {
		return ProjectSummary{}, err
	}
	return projectSummaryFrom(project), nil
}

// librarySummary builds the full nested summary (library + sorted projects).
func librarySummary(storage ConfigStorage, library Library) (LibrarySummary, error) {
	projects, err := listProjectRecords(storage, library.ID)
	if err != nil {
		return LibrarySummary{}, err
	}
	summaries := make([]ProjectSummary, 0, len(projects))
	for _, project := range projects {
		summaries = append(summaries, projectSummaryFrom(project))
	}
	sort.Slice(summaries, func(i, j int) bool {
		return lessByName(summaries[i].Name, summaries[i].ID, summaries[j].Name, summaries[j].ID)
	})
	return librarySummaryFrom(library, summaries), nil
}

func listLibraries(storage ConfigStorage) ([]LibrarySummary, error) {
	libraries, err := listLibraryRecords(storage)
	if err != nil {
		return nil, err
	}
	summaries := make([]LibrarySummary, 0, len(libraries))
	for _, library := range libraries {
		summary, err := librarySummary(storage, library)
		if err != nil {
			return nil, err
		}
		summaries = append(summaries, summary)
	}
	sortLibrarySummaries(summaries)
	return summaries, nil
}

// forEachConversationRecord walks every stored conversation record — including
// soft-deleted ones, which still occupy disk — and calls visit for each.
// Parsing every conversation.json is the same walk-and-fold idiom
// findConversationPath and searchHistory use; the mtime-shortlisted
// listConversations fast path stays separate because it serves a different
// (recency-capped, unfiltered) query.
func forEachConversationRecord(storage ConfigStorage, visit func(path string, conversation HistoryConversation) error) error {
	root := filepath.Join(storage.History, "conversations")
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
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
		return visit(path, conversation)
	})
}

// forEachProjectConversationRecord walks every conversation record belonging
// to one of projectIDs — the shared shape behind the project listing, the
// stream guard, the hard-delete sweep, and the library asset fold.
// includeDeleted covers soft-deleted conversations too (they still occupy
// disk and go with a hard delete); the read paths pass false.
func forEachProjectConversationRecord(storage ConfigStorage, projectIDs map[string]bool, includeDeleted bool, visit func(path string, conversation HistoryConversation) error) error {
	if len(projectIDs) == 0 {
		return nil
	}
	return forEachConversationRecord(storage, func(path string, conversation HistoryConversation) error {
		if !projectIDs[conversation.ProjectID] || (!includeDeleted && conversation.DeletedAt != "") {
			return nil
		}
		return visit(path, conversation)
	})
}

// listProjectConversations returns the non-deleted conversations belonging to a
// project, newest-updated first. Unlike the sidebar's main list this is not
// recency-capped: a project is a curated set, and hiding older members would
// make them unreachable (they are filtered out of the standalone list).
func listProjectConversations(storage ConfigStorage, projectID string) ([]ConversationSummary, error) {
	if _, _, err := findProject(storage, projectID); err != nil {
		return nil, err
	}
	summaries := []ConversationSummary{}
	err := forEachProjectConversationRecord(storage, map[string]bool{projectID: true}, false, func(path string, conversation HistoryConversation) error {
		summaries = append(summaries, conversationSummaryFrom(conversation))
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(summaries, func(i, j int) bool {
		if summaries[i].UpdatedAt == summaries[j].UpdatedAt {
			return summaries[i].ID < summaries[j].ID
		}
		return summaries[i].UpdatedAt > summaries[j].UpdatedAt
	})
	return summaries, nil
}

// projectConversationIDs returns the IDs of every non-deleted conversation
// belonging to one of projectIDs — the stream guard's cheaper question (no
// artifact counting; deleted conversations can't be streaming).
func projectConversationIDs(storage ConfigStorage, projectIDs map[string]bool) ([]string, error) {
	ids := []string{}
	err := forEachProjectConversationRecord(storage, projectIDs, false, func(path string, conversation HistoryConversation) error {
		ids = append(ids, conversation.ID)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return ids, nil
}

// projectConversationDirs locates every on-disk conversation directory whose
// record belongs to one of projectIDs — including soft-deleted conversations,
// which still occupy disk and go with the project. The artifact count is the
// number of files removed, reported by the delete bindings.
func projectConversationDirs(storage ConfigStorage, projectIDs map[string]bool) ([]string, int, error) {
	dirs := []string{}
	deletedAssets := 0
	err := forEachProjectConversationRecord(storage, projectIDs, true, func(path string, conversation HistoryConversation) error {
		dir := filepath.Dir(path)
		dirs = append(dirs, dir)
		deletedAssets += countFiles(filepath.Join(dir, "artifacts"))
		return nil
	})
	if err != nil {
		return nil, 0, err
	}
	return dirs, deletedAssets, nil
}

// deleteProject hard-deletes a project and every conversation in it, Final
// Cut-style: the project record and each member conversation directory are
// removed from disk immediately. Callers must first refuse while any member
// conversation is streaming (App.DeleteProject).
func deleteProject(storage ConfigStorage, projectID string) (DeleteProjectResult, error) {
	project, _, err := findProject(storage, projectID)
	if err != nil {
		return DeleteProjectResult{}, err
	}
	dirs, deletedAssets, err := projectConversationDirs(storage, map[string]bool{projectID: true})
	if err != nil {
		return DeleteProjectResult{}, err
	}
	if err := removeConversationDirs(dirs); err != nil {
		return DeleteProjectResult{}, err
	}
	if err := os.RemoveAll(projectDir(storage, project.LibraryID, projectID)); err != nil {
		return DeleteProjectResult{}, err
	}
	return DeleteProjectResult{DeletedConversations: len(dirs), DeletedAssets: deletedAssets}, nil
}

// deleteLibrary hard-deletes a library, its projects, and every conversation
// in them. The library directory holds only library/project records (media
// stays in conversation dirs), so one RemoveAll clears the records after the
// conversations are gone.
func deleteLibrary(storage ConfigStorage, libraryID string) (DeleteLibraryResult, error) {
	if _, err := findLibrary(storage, libraryID); err != nil {
		return DeleteLibraryResult{}, err
	}
	projectIDs, err := libraryProjectIDs(storage, libraryID)
	if err != nil {
		return DeleteLibraryResult{}, err
	}
	dirs, deletedAssets, err := projectConversationDirs(storage, projectIDs)
	if err != nil {
		return DeleteLibraryResult{}, err
	}
	if err := removeConversationDirs(dirs); err != nil {
		return DeleteLibraryResult{}, err
	}
	if err := os.RemoveAll(libraryDir(storage, libraryID)); err != nil {
		return DeleteLibraryResult{}, err
	}
	return DeleteLibraryResult{
		DeletedProjects:      len(projectIDs),
		DeletedConversations: len(dirs),
		DeletedAssets:        deletedAssets,
	}, nil
}

// removeConversationDirs removes conversation directories and drops their
// library-asset cache entries so a stale fold can't resurrect deleted media.
func removeConversationDirs(dirs []string) error {
	for _, dir := range dirs {
		if err := os.RemoveAll(dir); err != nil {
			return err
		}
	}
	invalidateConversationAssetCache(dirs)
	return nil
}

// moveConversationToProject reassigns a conversation between projects, or
// detaches it to standalone when projectID is empty. This is the one sanctioned
// mutation of ProjectID after creation — organizational, like a title rename:
// only conversation.json is rewritten, artifacts never move. Callers must
// refuse while the conversation is streaming (App.MoveConversationToProject).
func moveConversationToProject(storage ConfigStorage, conversationID, projectID string) (ConversationSummary, error) {
	projectID = strings.TrimSpace(projectID)
	if projectID != "" {
		if _, _, err := findProject(storage, projectID); err != nil {
			return ConversationSummary{}, err
		}
	}
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
	conversation.ProjectID = projectID
	conversation.UpdatedAt = time.Now().Format(time.RFC3339)
	if err := writeJSONFile(conversationPath, conversation); err != nil {
		return ConversationSummary{}, err
	}
	return conversationSummaryFrom(conversation), nil
}
