package main

import (
	"embed"
	"net/http"
	"strings"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/menu"
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

func main() {
	app := NewApp()

	appMenu := menu.NewMenuFromItems(
		menu.AppMenu(),
		menu.EditMenu(),
		menu.WindowMenu(),
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
		Bind: []interface{}{
			app,
		},
	})

	if err != nil {
		println("Error:", err.Error())
	}
}
