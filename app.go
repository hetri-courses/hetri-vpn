package main

import (
	"context"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// App is the Wails binding surface for the UI. All privileged work goes
// through the manager service pipe; a direct fallback exists only when the
// process itself is elevated (dev convenience, pre-service parity).
type App struct {
	ctx       context.Context
	mu        sync.Mutex
	cachedIP  string
	ipFetched time.Time
}

func NewApp() *App {
	return &App{}
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	// Manual-start manager: bring it up for this app session (service ACL
	// grants interactive users start/stop, so no elevation).
	go func() { _ = hiddenCmd(exec.Command("sc.exe", "start", svcName)).Run() }()
}

// shutdown stops the manager so no Hetri processes outlive the window.
// The tunnel service (the connection itself) is deliberately untouched.
func (a *App) shutdown(ctx context.Context) {
	_ = hiddenCmd(exec.Command("sc.exe", "stop", svcName)).Run()
}

func (a *App) Connect() string {
	if reply, err := pipeCall("connect", ""); err == nil {
		a.invalidateIP()
		return reply.Error
	}
	if isElevated() {
		defer a.invalidateIP()
		return tunnelConnect()
	}
	return "Background service is not running. Enable it below."
}

func (a *App) Disconnect() string {
	if reply, err := pipeCall("disconnect", ""); err == nil {
		a.invalidateIP()
		return reply.Error
	}
	if isElevated() {
		defer a.invalidateIP()
		return tunnelDisconnect()
	}
	return "Background service is not running. Enable it below."
}

func (a *App) SetMode(mode string) string {
	if reply, err := pipeCall("setmode", mode); err == nil {
		a.invalidateIP()
		return reply.Error
	}
	if isElevated() {
		defer a.invalidateIP()
		return tunnelSetMode(mode)
	}
	return "Background service is not running. Enable it below."
}

func (a *App) GetStatus() TunnelStatus {
	var s TunnelStatus
	if reply, err := pipeCall("status", ""); err == nil && reply.Status != nil {
		s = *reply.Status
	} else {
		s = tunnelStatus()
		s.ServiceInstalled = false
		if isElevated() {
			// Direct mode works while elevated; report it as available.
			s.ServiceInstalled = true
		}
	}
	s.PublicIP = a.publicIP()
	return s
}

// EnableService triggers the one-time elevated self-install of the manager
// service. Returns immediately; the UI polls until the pipe appears.
func (a *App) EnableService() string {
	if serviceReachable() {
		return ""
	}
	// Installed but stopped? A plain start needs no elevation thanks to the
	// service ACL set at install time.
	_ = hiddenCmd(exec.Command("sc.exe", "start", svcName)).Run()
	for i := 0; i < 12; i++ {
		if serviceReachable() {
			return ""
		}
		time.Sleep(250 * time.Millisecond)
	}
	if isElevated() {
		return copyFileErr(installService())
	}
	if err := relaunchElevated("/install-service"); err != nil {
		if strings.Contains(err.Error(), "cancelled") || strings.Contains(err.Error(), "canceled") {
			return "Elevation was declined."
		}
		return err.Error()
	}
	return ""
}

func (a *App) invalidateIP() {
	a.mu.Lock()
	a.cachedIP = ""
	a.ipFetched = time.Time{}
	a.mu.Unlock()
}

func (a *App) publicIP() string {
	a.mu.Lock()
	if a.cachedIP != "" && time.Since(a.ipFetched) < 10*time.Second {
		ip := a.cachedIP
		a.mu.Unlock()
		return ip
	}
	a.mu.Unlock()

	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("https://api.ipify.org")
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64))
	ip := strings.TrimSpace(string(body))

	a.mu.Lock()
	a.cachedIP = ip
	a.ipFetched = time.Now()
	a.mu.Unlock()
	return ip
}
