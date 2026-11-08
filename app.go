package main

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

const defaultOllamaBaseURL = "http://localhost:11434"

// defaultOpenAICompatibleBaseURL is the endpoint of the OpenAI-compatible
// image server (LocalAI's default port; any /v1/images/generations server can
// be configured instead).
const defaultOpenAICompatibleBaseURL = "http://localhost:8080"

// defaultOllamaNumCtx is sent as num_ctx on every chat call so the context
// window is explicit and identical across calls (Ollama reloads the model
// when num_ctx changes between requests). 16K because the planner's prompt
// carries the tool catalog before any conversation or evidence — in native
// tool-calling mode that alone is ~5K tokens (conv_4fe9e638: a replan round
// hit 10,857 prompt tokens against 8,192, and Ollama's overflow behavior is
// to truncate from the FRONT, dropping the planner's instructions).
const defaultOllamaNumCtx = 16384

type App struct {
	ctx          context.Context
	client       *http.Client
	updateClient *http.Client
	baseURL      string
	configMu     sync.Mutex
	streams      map[string]context.CancelFunc
	// streamConversations mirrors streams keyed the same (requestID), holding
	// the conversationID each active stream is writing to — the signal the
	// library/project delete and move guards need, since a stream in flight
	// during a hard delete would resurrect turns into a removed directory.
	streamConversations map[string]string
	streamsMu           sync.Mutex
	permissions         map[string]chan bool
	permissionsMu       sync.Mutex
	toolPermission      func(context.Context, ToolPermissionRequestEvent) ToolPermissionDecision
	updatesMu           sync.Mutex
	lastUpdate          *updateManifest
	updaterQuitting     atomic.Bool
	// falPricing caches fal's per-endpoint unit pricing for media cost
	// estimation (fal_pricing.go). Created once here so every gateway rebuild
	// shares the TTL window; nil on bare &App{} literals, which the estimator
	// treats as "no pricing available" (fail-soft zero).
	falPricing *falPricingCache
	// mediaUploads tracks in-progress staged uploads (media_upload.go) keyed
	// by upload ID — the Begin/Append/Complete chunk lifecycle. A session that
	// never completes (bridge reload, crash) is simply unaddressable; the
	// uploads sweep reclaims the partial file by age.
	mediaUploads   map[string]*MediaUploadSession
	mediaUploadsMu sync.Mutex
	// editOps tracks the editors' in-flight operations keyed by
	// operation ID (edit_session.go): the one-op-per-session guard,
	// CancelImageEdit's handle, and the "is it live?" check orphaned
	// queued/running records consult. editOpsMu guards the map.
	editOps   map[string]*editOpRun
	editOpsMu sync.Mutex
	// compositionRenders tracks the video composer's in-flight render
	// operations keyed by operation ID (composition_render.go): the
	// one-render-per-conversation guard and CancelCompositionRender's
	// handle. compositionRendersMu guards the map.
	compositionRenders       map[string]*compositionRenderRun
	compositionRenderHistory map[string]CompositionRenderOperation
	compositionRendersMu     sync.Mutex
	videoPreviewMu           sync.Mutex
	videoPreviewJobs         map[string]*videoPreviewJob
	videoSourcePreviews      map[string]EditSourceInfo
	// editSubmitMu serializes submit-time validation, persistence, and
	// in-flight registration. Without it, two quick submits into the same
	// session could both pass the running check before either registered.
	editSubmitMu sync.Mutex
	// falCategoryCache caches one fetched fal model category per key (the
	// category id), shared by every Settings lister that reads it — the
	// screen walks ~a dozen categories on load, and re-paging all of them on
	// every open both tripled the request count and, under fal's /v1/models
	// rate limit, emptied the tail listers' pickers (the speech combobox
	// "didn't open"). A cached category also serves a rate-limited re-fetch
	// as the last good copy instead of a failure. Guarded by falCategoryMu;
	// falCategoryCacheTTL bounds freshness. falCategoryFetch is a test seam —
	// nil uses the real fetch.
	falCategoryMu    sync.Mutex
	falCategories    map[string]falCategoryEntry
	falCategoryFetch func(context.Context, string) ([]FalModel, error)
}

func NewApp() *App {
	app := &App{
		client: &http.Client{
			Timeout: 10 * time.Minute,
		},
		// The updater gets its own client so a hung check never queues behind
		// a long model stream on the shared one; each call still bounds
		// itself with a context deadline appropriate to its phase (seconds
		// for the manifest, the client timeout for the download).
		updateClient: &http.Client{
			Timeout: 10 * time.Minute,
		},
		baseURL:                  defaultOllamaBaseURL,
		streams:                  map[string]context.CancelFunc{},
		streamConversations:      map[string]string{},
		permissions:              map[string]chan bool{},
		editOps:                  map[string]*editOpRun{},
		compositionRenders:       map[string]*compositionRenderRun{},
		compositionRenderHistory: map[string]CompositionRenderOperation{},
		videoPreviewJobs:         map[string]*videoPreviewJob{},
		videoSourcePreviews:      map[string]EditSourceInfo{},
		falPricing:               newFalPricingCache(),
	}
	app.toolPermission = app.requestToolPermission
	return app
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	// Create the storage directories once at launch from the current config so
	// the first chat/tool/history operation doesn't pay for it. The per-call
	// ensureStorageDirs in loadReadyConfig stays as a no-op-ish backstop for a
	// SaveConfig that changed the storage root mid-session.
	if config, err := loadAppConfig(); err == nil {
		_ = ensureStorageDirs(config.Storage)
		a.baseURL = config.Providers.Ollama.BaseURL
		// A previous process may have died mid-edit: downgrade its orphaned
		// queued/running operations to failed so reopened sessions never show
		// a phantom in-flight edit (edit_session.go).
		sweepInterruptedEditOps(config.Storage)
	}
	cleanupEditSourcePreviews()
	cleanupVideoEditStaging()
	cleanupAudioEditStaging()
	cleanupCompositionPreviews()
	a.startUpdateScheduler(ctx)
}

// beforeClose is wired to Wails' OnBeforeClose hook. It prompts the user to
// confirm before the window closes; returning true prevents the close.
func (a *App) beforeClose(ctx context.Context) (prevent bool) {
	// The self-updater's own quit: the user already chose to install, and the
	// detached helper is waiting on this process to exit — skip the dialog.
	if a.updaterQuitting.Load() {
		return false
	}
	choice, err := runtime.MessageDialog(ctx, runtime.MessageDialogOptions{
		Type:          runtime.QuestionDialog,
		Title:         "Quit Atelier?",
		Message:       "Are you sure you want to quit Atelier?",
		Buttons:       []string{"Quit", "Cancel"},
		DefaultButton: "Quit",
		CancelButton:  "Cancel",
	})
	if err != nil {
		// If the dialog fails for any reason, don't trap the user in the app.
		return false
	}
	return choice != "Quit"
}

func (a *App) anyEditOperationActive() bool {
	a.editOpsMu.Lock()
	defer a.editOpsMu.Unlock()
	return len(a.editOps) > 0
}

// Stop local child processes before Wails exits. If persistence cannot finish
// before shutdown, the existing startup sweep marks the operation interrupted.
func (a *App) shutdown(_ context.Context) {
	a.videoPreviewMu.Lock()
	for _, job := range a.videoPreviewJobs {
		job.cancel()
	}
	a.videoPreviewMu.Unlock()
	a.editOpsMu.Lock()
	for _, operation := range a.editOps {
		operation.cancel()
	}
	a.editOpsMu.Unlock()
	a.compositionRendersMu.Lock()
	for _, render := range a.compositionRenders {
		render.cancel()
	}
	a.compositionRendersMu.Unlock()
	deadline := time.Now().Add(2 * time.Second)
	for a.anyEditOperationActive() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
}

// conversationStreaming reports whether this one conversation has an active
// stream.
func (a *App) conversationStreaming(conversationID string) bool {
	a.streamsMu.Lock()
	defer a.streamsMu.Unlock()
	for _, id := range a.streamConversations {
		if id == conversationID {
			return true
		}
	}
	return false
}

// CheckForUpdates fetches the release manifest and compares it with the
// running version. Manual by design — the automatic startup/ticker path runs
// the same core behind the 24h throttle — and raises the banner event when an
// update is found so the Settings status line and the banner agree.
func (a *App) CheckForUpdates() UpdateStatus {
	return a.checkForUpdates(a.ctx, true)
}

// menuCheckForUpdates is the app-menu entry point; the emitted event raises
// the same banner the automatic check does.
func (a *App) menuCheckForUpdates() {
	_ = a.checkForUpdates(a.ctx, true)
}

// The File-menu creation actions are pure frontend navigation: the menu click
// lands in Go (Wails menu handlers), so each emits a dedicated event and the
// UI drives its inline creation flow from it — no Go-side dialogs, no new
// bindings. menuNewConversation is context-aware on the frontend side: a
// pending in-project chat when the user is working inside a project (FCP's
// ⌘N-acts-on-the-selected-container), else a standalone chat.
func (a *App) menuNewConversation() { a.emitMenuAction("atelier:menu-new-conversation") }

// menuNewLibrary routes File → New Library… to the sidebar's inline input.
func (a *App) menuNewLibrary() { a.emitMenuAction("atelier:menu-new-library") }

// menuNewProject routes File → New Project… to the inline input under the
// currently expanded (or first) library; the frontend picks the target.
func (a *App) menuNewProject() { a.emitMenuAction("atelier:menu-new-project") }

func (a *App) emitMenuAction(event string) {
	if a.ctx != nil {
		runtime.EventsEmit(a.ctx, event)
	}
}

// InstallUpdate installs the update found by the last check: download,
// verify, stage the swap, hand off to the detached helper, then quit so the
// bundle can be replaced and relaunched. Refuses while a turn is streaming —
// the frontend queues and retries when idle; this gate stays authoritative —
// and refuses cleanly in development, where there is no bundle to swap.
func (a *App) InstallUpdate() error {
	if a.anyStreamActive() {
		return errors.New("a conversation is still running; wait for it to finish before updating")
	}
	manifest := a.cachedUpdateManifest()
	if manifest == nil {
		return errors.New("no update is available yet; check for updates first")
	}
	if err := applySelfUpdate(a.updateClient, manifest); err != nil {
		return err
	}
	// The helper is live and waiting on this PID: quit now, dialog-free.
	a.updaterQuitting.Store(true)
	if a.ctx != nil {
		runtime.Quit(a.ctx)
	}
	return nil
}

// anyStreamActive reports whether any chat turn is in flight. StreamChat
// registers the cancel func synchronously and the goroutine removes it only
// on completion, so a non-empty map is the reliable "turn running" signal.
func (a *App) anyStreamActive() bool {
	a.streamsMu.Lock()
	defer a.streamsMu.Unlock()
	return len(a.streams) > 0
}
