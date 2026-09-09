package main

import (
	"embed"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/menu"
	"github.com/wailsapp/wails/v2/pkg/menu/keys"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
)

//go:embed all:frontend/dist
var assets embed.FS

// version is injected at link time by release.sh (-ldflags "-X main.version=...").
// It is shown in the macOS "About Atelier" dialog.
var version = "dev"

// artifactPrefix is the URL path prefix that the asset handler uses to serve
// image artifacts from disk. hydrateHistoryContent generates URLs of the form
// /atelier-artifact/absolute/path/to/file.png — the handler strips the prefix
// and serves the file at the remaining absolute path.
const artifactPrefix = "/atelier-artifact"

// artifactHandler serves image artifacts from disk so the frontend can render
// them via <img src="/atelier-artifact/path/to/file.png"> without embedding
// multi-MB base64 data URLs in the JSON IPC payload.
func artifactHandler(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.URL.Path, artifactPrefix) {
		http.NotFound(w, r)
		return
	}
	filePath := strings.TrimPrefix(r.URL.Path, artifactPrefix)
	if filePath == "" || strings.Contains(filePath, "..") {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, filePath)
}

// guiPathPrefixes are the directories a GUI-launched app's PATH usually lacks.
// A Dock/Finder launch inherits launchd's minimal PATH (/usr/bin:/bin:/usr/sbin/
// /sbin), which excludes Homebrew — so a Homebrew-installed ffmpeg or whisper
// looks "not installed" to local-binary detection on first run even though the
// user just installed it. Prepending these (existing directories only, never
// duplicating what's already there) makes detection see what a
// terminal-launched app would see. Apple Silicon Homebrew lives in
// /opt/homebrew/bin; Intel (and Rosetta) installs in /usr/local/bin.
var guiPathPrefixes = []string{"/opt/homebrew/bin", "/usr/local/bin"}

// augmentGUIPath prepends guiPathPrefixes to the process PATH. Must run before
// anything calls exec.LookPath — i.e. first thing in main.
func augmentGUIPath() {
	current := os.Getenv("PATH")
	updated := prependMissingPathDirs(current, guiPathPrefixes, func(dir string) bool {
		info, err := os.Stat(dir)
		return err == nil && info.IsDir()
	})
	if updated != current {
		os.Setenv("PATH", updated)
	}
}

// prependMissingPathDirs returns path with every dir that exists and is not
// already on it prepended, in order. Pure apart from the injected existence
// check so tests can pin the merge without touching the filesystem. On
// Windows the Homebrew directories never exist, so the exists check filters
// them out and PATH is returned unchanged.
func prependMissingPathDirs(path string, dirs []string, exists func(string) bool) string {
	if len(dirs) == 0 {
		return path
	}
	present := make(map[string]bool)
	for _, entry := range filepath.SplitList(path) {
		if entry != "" {
			present[filepath.Clean(entry)] = true
		}
	}
	var add []string
	for _, dir := range dirs {
		cleaned := filepath.Clean(strings.TrimSpace(dir))
		if cleaned == "" || cleaned == "." || present[cleaned] || !exists(cleaned) {
			continue
		}
		present[cleaned] = true
		add = append(add, cleaned)
	}
	if len(add) == 0 {
		return path
	}
	if path == "" {
		return strings.Join(add, string(os.PathListSeparator))
	}
	return strings.Join(add, string(os.PathListSeparator)) + string(os.PathListSeparator) + path
}

func main() {
	augmentGUIPath()
	app := NewApp()

	appMenu := menu.NewMenuFromItems(
		menu.AppMenu(),
		// File holds the creation actions (new conversation, library, project).
		// The clicks emit atelier:menu-new-* events; the frontend owns the
		// flows — see the menuNew* handlers in app.go.
		menu.SubMenu("File", menu.NewMenuFromItems(
			&menu.MenuItem{
				Label:       "New Conversation",
				Accelerator: keys.CmdOrCtrl("n"),
				Click:       func(*menu.CallbackData) { app.menuNewConversation() },
			},
			&menu.MenuItem{
				Label:       "New Library…",
				Accelerator: keys.Combo("l", keys.ShiftKey, keys.CmdOrCtrlKey),
				Click:       func(*menu.CallbackData) { app.menuNewLibrary() },
			},
			&menu.MenuItem{
				Label:       "New Project…",
				Accelerator: keys.Combo("p", keys.ShiftKey, keys.CmdOrCtrlKey),
				Click:       func(*menu.CallbackData) { app.menuNewProject() },
			},
		)),
		menu.EditMenu(),
		menu.WindowMenu(),
		// "Check for Updates…" lives under Help rather than the app menu:
		// Wails' AppMenu() is a native role with no Go-side submenu to append
		// into. Clicking runs the same check as the Settings button; the
		// emitted event raises the update banner either way.
		menu.SubMenu("Help", menu.NewMenuFromItems(
			&menu.MenuItem{
				Label: "Check for Updates…",
				Click: func(*menu.CallbackData) { app.menuCheckForUpdates() },
			},
		)),
	)

	err := wails.Run(&options.App{
		Title:  "Atelier",
		Width:  1320,
		Height: 860,
		Menu:   appMenu,
		Mac: &mac.Options{
			About: &mac.AboutInfo{
				Title:   "Atelier",
				Message: "Version " + version,
			},
			// FullscreenEnabled turns on WKWebView's elementFullscreenEnabled,
			// which gates HTML5 fullscreen — without it the native fullscreen
			// button on the <video controls> bar is a no-op (Wails leaves it
			// off by default). Required so generated/attached videos can go
			// fullscreen from their own control bar. macOS 12.3+ only.
			Preferences: &mac.Preferences{
				FullscreenEnabled: mac.Enabled,
			},
		},
		AssetServer: &assetserver.Options{
			Assets: assets,
			Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasPrefix(r.URL.Path, artifactPrefix) {
					artifactHandler(w, r)
					return
				}
				http.NotFound(w, r)
			}),
		},
		BackgroundColour: &options.RGBA{R: 27, G: 38, B: 54, A: 1},
		OnStartup:        app.startup,
		OnBeforeClose:    app.beforeClose,
		Bind: []interface{}{
			app,
		},
	})

	if err != nil {
		println("Error:", err.Error())
	}
}
