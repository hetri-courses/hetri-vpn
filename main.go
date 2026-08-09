package main

import (
	"embed"
	"fmt"
	"os"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/windows"
)

//go:embed all:frontend/dist
var assets embed.FS

// One binary, many roles, selected by flag — same model as upstream
// wireguard.exe (/managerservice, /tunnelservice) vs its default UI.
func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "/service":
			runService()
			return
		case "/install-service":
			if err := installService(); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			return
		case "/uninstall-service":
			if err := uninstallService(); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			return
		}
	}

	app := NewApp()

	err := wails.Run(&options.App{
		Title:     "Hetri VPN",
		Width:     420,
		Height:    680,
		MinWidth:  380,
		MinHeight: 620,
		MaxWidth:  520,
		MaxHeight: 820,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		BackgroundColour:   &options.RGBA{R: 18, G: 26, B: 21, A: 1},
		OnStartup:          app.startup,
		HideWindowOnClose:  true,
		SingleInstanceLock: &options.SingleInstanceLock{UniqueId: "hetri-vpn-2f6a"},
		Bind: []interface{}{
			app,
		},
		Windows: &windows.Options{},
	})

	if err != nil {
		println("Error:", err.Error())
	}
}
