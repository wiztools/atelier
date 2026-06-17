package main

import (
	"embed"

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
