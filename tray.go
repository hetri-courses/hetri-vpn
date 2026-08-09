package main

import (
	_ "embed"

	"github.com/energye/systray"
)

//go:embed build/windows/icon.ico
var trayIcon []byte

// startTray runs the notification-area icon. Closing the main window only
// hides it (HideWindowOnClose); the tray is how the app stays resident.
func startTray(app *App) {
	systray.Run(func() {
		systray.SetIcon(trayIcon)
		systray.SetTitle("Hetri VPN")
		systray.SetTooltip("Hetri VPN")
		systray.SetOnClick(func(menu systray.IMenu) {
			app.ShowWindow()
		})

		mOpen := systray.AddMenuItem("Open", "Show Hetri VPN")
		mConnect := systray.AddMenuItem("Connect", "Bring the tunnel up")
		mDisconnect := systray.AddMenuItem("Disconnect", "Take the tunnel down")
		systray.AddSeparator()
		mQuit := systray.AddMenuItem("Quit", "Exit Hetri VPN")

		mOpen.Click(func() { app.ShowWindow() })
		mConnect.Click(func() { go app.Connect() })
		mDisconnect.Click(func() { go app.Disconnect() })
		mQuit.Click(func() { app.QuitApp() })
	}, nil)
}
