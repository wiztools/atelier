package main

import (
	"errors"
	mrand "math/rand/v2"
	"strings"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// EmptyStatePrompt is a heading/subtext pair shown on the empty chat screen.
type EmptyStatePrompt struct {
	Heading string `json:"heading"`
	Sub     string `json:"sub"`
}

// emptyStatePrompts is the pool of greetings for the empty chat screen; one is
// picked at random each time the transcript is empty.
var emptyStatePrompts = []EmptyStatePrompt{
	{"What are we making today?", "Describe an image, video, or sound to begin."},
	{"Start something.", "Pick a model, or just describe what you want to create."},
	{"The studio's open.", "Type a prompt to generate images, video, audio, and more."},
	{"Describe what you want to create.", "Works across image, video, audio, and motion models."},
	{"What do you want to generate?", "Enter a prompt and choose a model to get started."},
	{"Type a prompt to begin.", "Or paste a reference to guide the generation."},
	{"Begin with a blank canvas.", "Describe your idea and pick a model."},
	{"Imagine it here.", "Turn a prompt into images, video, or sound."},
	{"A blank page, endless models.", "Start typing to bring an idea to life."},
	{"Conjure something.", "Describe it — we'll find the right model."},
	{"Feed the machines.", "A prompt is all it takes."},
}

// RandomEmptyStatePrompt returns a random greeting for the empty chat screen.
func (a *App) RandomEmptyStatePrompt() EmptyStatePrompt {
	return emptyStatePrompts[mrand.IntN(len(emptyStatePrompts))]
}

func (a *App) ListConversations() ([]ConversationSummary, error) {
	config, err := loadReadyConfig()
	if err != nil {
		return nil, err
	}
	return listConversations(config.Storage)
}

func (a *App) GetConversation(conversationID string) (ConversationDetail, error) {
	config, err := loadAppConfig()
	if err != nil {
		return ConversationDetail{}, err
	}
	return getConversation(config.Storage, conversationID)
}

// ListConversationAssets derives the conversation's referable media assets —
// every image/audio/video artifact in its history, oldest first — for the
// assets panel and the @-mention composer.
func (a *App) ListConversationAssets(conversationID string) ([]ConversationAsset, error) {
	config, err := loadAppConfig()
	if err != nil {
		return nil, err
	}
	return listConversationAssets(config.Storage, conversationID)
}

// SearchConversations greps stored conversation history — turn text and
// titles — beyond what the sidebar list shows, returning per-conversation
// matches with context snippets.
func (a *App) SearchConversations(query string, options SearchOptions) (SearchResponse, error) {
	config, err := loadReadyConfig()
	if err != nil {
		return SearchResponse{}, err
	}
	return searchHistory(config.Storage, query, options)
}

func (a *App) DeleteConversation(conversationID string) error {
	a.editSubmitMu.Lock()
	defer a.editSubmitMu.Unlock()
	if a.editOpRunning(conversationID) {
		return errors.New("an edit is still running in this conversation; finish or cancel it before deleting")
	}
	config, err := loadAppConfig()
	if err != nil {
		return err
	}
	return deleteConversation(config.Storage, conversationID)
}

func (a *App) PurgeArchivedConversations() (PurgeArchivedResult, error) {
	a.editSubmitMu.Lock()
	defer a.editSubmitMu.Unlock()
	if a.anyEditOperationActive() {
		return PurgeArchivedResult{}, errors.New("an edit is still running; finish or cancel it before purging archived conversations")
	}
	config, err := loadAppConfig()
	if err != nil {
		return PurgeArchivedResult{}, err
	}
	return purgeArchivedConversations(config.Storage)
}

func (a *App) UpdateConversationTitle(conversationID string, title string) (ConversationSummary, error) {
	a.editSubmitMu.Lock()
	defer a.editSubmitMu.Unlock()
	if a.editOpRunning(conversationID) {
		return ConversationSummary{}, errors.New("an edit is still running in this conversation; finish or cancel it before renaming")
	}
	config, err := loadAppConfig()
	if err != nil {
		return ConversationSummary{}, err
	}
	return updateConversationTitle(config.Storage, conversationID, title)
}

// CreateLibrary creates a top-level library container (see libraries.go).
func (a *App) CreateLibrary(name string) (LibrarySummary, error) {
	config, err := loadReadyConfig()
	if err != nil {
		return LibrarySummary{}, err
	}
	return createLibrary(config.Storage, name)
}

// ListLibraries returns every library with its projects, most-recently-active
// first, for the sidebar's Libraries tree. Activity is derived from the member
// conversations (see newestConversationActivity), not stored on the records.
func (a *App) ListLibraries() ([]LibrarySummary, error) {
	config, err := loadReadyConfig()
	if err != nil {
		return nil, err
	}
	return listLibraries(config.Storage)
}

func (a *App) RenameLibrary(libraryID string, name string) (LibrarySummary, error) {
	config, err := loadReadyConfig()
	if err != nil {
		return LibrarySummary{}, err
	}
	return renameLibrary(config.Storage, libraryID, name)
}

// DeleteLibrary hard-deletes a library, its projects, and every conversation
// in them — Final Cut's trash, no recovery. Refused while any member
// conversation is streaming: an in-flight append would write turns into a
// directory this call just removed.
func (a *App) DeleteLibrary(libraryID string) (DeleteLibraryResult, error) {
	a.editSubmitMu.Lock()
	defer a.editSubmitMu.Unlock()
	config, err := loadReadyConfig()
	if err != nil {
		return DeleteLibraryResult{}, err
	}
	projectIDs, err := libraryProjectIDs(config.Storage, libraryID)
	if err != nil {
		return DeleteLibraryResult{}, err
	}
	if a.conversationsStreamingInProjects(config.Storage, projectIDs) {
		return DeleteLibraryResult{}, errors.New("a conversation in this library is still running; wait for it to finish before deleting")
	}
	return deleteLibrary(config.Storage, libraryID)
}

func (a *App) CreateProject(libraryID string, name string) (ProjectSummary, error) {
	config, err := loadReadyConfig()
	if err != nil {
		return ProjectSummary{}, err
	}
	return createProject(config.Storage, libraryID, name)
}

func (a *App) RenameProject(projectID string, name string) (ProjectSummary, error) {
	config, err := loadReadyConfig()
	if err != nil {
		return ProjectSummary{}, err
	}
	return renameProject(config.Storage, projectID, name)
}

// SetProjectNotes replaces a project's instruction block — standing guidance
// the harness injects into its planner prompt for every conversation in the
// project (resolveTurnProjectNotes). An empty string clears it.
func (a *App) SetProjectNotes(projectID string, notes string) (ProjectSummary, error) {
	config, err := loadReadyConfig()
	if err != nil {
		return ProjectSummary{}, err
	}
	return setProjectNotes(config.Storage, projectID, notes)
}

// DeleteProject hard-deletes a project and every conversation in it, with the
// same streaming refusal as DeleteLibrary.
func (a *App) DeleteProject(projectID string) (DeleteProjectResult, error) {
	a.editSubmitMu.Lock()
	defer a.editSubmitMu.Unlock()
	config, err := loadReadyConfig()
	if err != nil {
		return DeleteProjectResult{}, err
	}
	if a.conversationsStreamingInProjects(config.Storage, map[string]bool{projectID: true}) {
		return DeleteProjectResult{}, errors.New("a conversation in this project is still running; wait for it to finish before deleting")
	}
	return deleteProject(config.Storage, projectID)
}

// ListProjectConversations returns the project's conversations, newest-first,
// for the sidebar's project rows. Not recency-capped — these conversations are
// filtered out of the standalone list, so this is their only listing.
func (a *App) ListProjectConversations(projectID string) ([]ConversationSummary, error) {
	config, err := loadReadyConfig()
	if err != nil {
		return nil, err
	}
	return listProjectConversations(config.Storage, projectID)
}

// ListLibraryAssets folds every conversation in the library's projects into
// one asset list — uploads and generated media alike — for the library assets
// panel and the @-mention picker.
func (a *App) ListLibraryAssets(libraryID string) ([]ConversationAsset, error) {
	config, err := loadReadyConfig()
	if err != nil {
		return nil, err
	}
	return listLibraryAssets(config.Storage, libraryID)
}

// MoveConversationToProject reassigns a conversation to a project, or detaches
// it to standalone when projectID is empty. Refused while the conversation is
// streaming — the append path rewrites conversation.json from its own load,
// and a concurrent move would clobber one of the two writes.
func (a *App) MoveConversationToProject(conversationID string, projectID string) (ConversationSummary, error) {
	a.editSubmitMu.Lock()
	defer a.editSubmitMu.Unlock()
	config, err := loadReadyConfig()
	if err != nil {
		return ConversationSummary{}, err
	}
	if a.conversationStreaming(conversationID) || a.editOpRunning(conversationID) {
		return ConversationSummary{}, errors.New("this conversation is still running; wait for it to finish before moving it")
	}
	return moveConversationToProject(config.Storage, conversationID, projectID)
}

// InspectLibraryExport is the export pre-flight: what an archive of this
// library would contain, how big it is, and which referenced assets are
// missing on disk. No dialog, no writes — the confirm UI runs it first, and
// missing assets never block (the manifest records them).
func (a *App) InspectLibraryExport(libraryID string) (LibraryExportPlan, error) {
	config, err := loadReadyConfig()
	if err != nil {
		return LibraryExportPlan{}, err
	}
	return inspectLibraryExport(config.Storage, libraryID)
}

// ExportLibrary archives a library — projects, member conversations (soft-
// deleted included), and all artifacts — into a user-chosen .atelierlib file.
// Refused while any member conversation is streaming: the archive must
// snapshot settled turns, not a directory an in-flight append is still
// writing.
func (a *App) ExportLibrary(libraryID string) (LibraryExportResult, error) {
	config, err := loadReadyConfig()
	if err != nil {
		return LibraryExportResult{}, err
	}
	projectIDs, err := libraryProjectIDs(config.Storage, libraryID)
	if err != nil {
		return LibraryExportResult{}, err
	}
	if a.conversationsStreamingInProjects(config.Storage, projectIDs) {
		return LibraryExportResult{}, errors.New("a conversation in this library is still running; wait for it to finish before exporting")
	}
	plan, err := inspectLibraryExport(config.Storage, libraryID)
	if err != nil {
		return LibraryExportResult{}, err
	}
	filename := sanitizeFilename(plan.LibraryName)
	if filename == "" {
		filename = libraryID
	}
	path, err := runtime.SaveFileDialog(a.ctx, runtime.SaveDialogOptions{
		Title:           "Export Library",
		DefaultFilename: filename + libraryArchiveExtension,
		Filters:         []runtime.FileFilter{{DisplayName: "Atelier Library Archive (*.atelierlib)", Pattern: "*.atelierlib"}},
	})
	if err != nil {
		return LibraryExportResult{}, err
	}
	if path == "" {
		return LibraryExportResult{}, nil
	}
	if !strings.HasSuffix(strings.ToLower(path), libraryArchiveExtension) {
		path += libraryArchiveExtension
	}
	return exportLibrary(config.Storage, libraryID, path, func(phase string, done, total int) {
		a.emitLibraryArchiveEvent(LibraryArchiveEvent{Op: "export", LibraryID: libraryID, Phase: phase, Done: done, Total: total})
	})
}

// ImportLibrary restores a .atelierlib archive as a new library in storage.
// The archive is verified whole (per-file SHA-256 against its manifest) before
// anything is written, and an archive whose library or conversations already
// exist is refused — import never merges. Model names in the archived history
// are provenance, not capabilities: nothing checks model availability.
func (a *App) ImportLibrary() (LibraryImportResult, error) {
	config, err := loadReadyConfig()
	if err != nil {
		return LibraryImportResult{}, err
	}
	path, err := runtime.OpenFileDialog(a.ctx, runtime.OpenDialogOptions{
		Title: "Import Library",
		Filters: []runtime.FileFilter{
			{DisplayName: "Atelier Library Archive (*.atelierlib)", Pattern: "*.atelierlib"},
			{DisplayName: "Zip Archive (*.zip)", Pattern: "*.zip"},
		},
	})
	if err != nil {
		return LibraryImportResult{}, err
	}
	if path == "" {
		return LibraryImportResult{}, nil
	}
	return importLibraryArchive(config.Storage, config.Tools.Filesystem.Root, path, func(phase string, done, total int) {
		a.emitLibraryArchiveEvent(LibraryArchiveEvent{Op: "import", Phase: phase, Done: done, Total: total})
	})
}

// conversationsStreamingInProjects reports whether any conversation belonging
// to one of projectIDs has an active stream or edit. On a walk error it checks
// all active work — deletion is destructive, so uncertainty blocks it.
func (a *App) conversationsStreamingInProjects(storage ConfigStorage, projectIDs map[string]bool) bool {
	ids, err := projectConversationIDs(storage, projectIDs)
	if err != nil {
		return a.anyStreamActive() || a.anyEditOperationActive()
	}
	if len(ids) == 0 {
		return false
	}
	a.streamsMu.Lock()
	active := make(map[string]bool, len(a.streamConversations))
	for _, conversationID := range a.streamConversations {
		active[conversationID] = true
	}
	a.streamsMu.Unlock()
	a.editOpsMu.Lock()
	for _, operation := range a.editOps {
		active[operation.conversationID] = true
	}
	a.editOpsMu.Unlock()
	for _, id := range ids {
		if active[id] {
			return true
		}
	}
	return false
}

// LibraryArchiveEvent is the atelier:library-archive payload: coarse progress
// for a library export or import — one tick per written conversation (export)
// and per verified entry / written conversation (import).
type LibraryArchiveEvent struct {
	Op        string `json:"op"` // "export" | "import"
	LibraryID string `json:"libraryId,omitempty"`
	Phase     string `json:"phase"` // "verify" | "write"
	Done      int    `json:"done"`
	Total     int    `json:"total"`
}

func (a *App) emitLibraryArchiveEvent(event LibraryArchiveEvent) {
	if a.ctx != nil {
		runtime.EventsEmit(a.ctx, "atelier:library-archive", event)
	}
}
