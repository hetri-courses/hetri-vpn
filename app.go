package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	tunnelName = "paw-us"
	wgDir      = `C:\Program Files\WireGuard`
	fullCIDR   = "0.0.0.0/0"
	splitCIDR  = "10.100.0.0/24"
)

// App struct
type App struct {
	ctx        context.Context
	mu         sync.Mutex
	cachedIP   string
	ipFetched  time.Time
	confDirMem string
}

// NewApp creates a new App application struct
func NewApp() *App {
	return &App{}
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	a.ensureConfig()
}

// Status is the single payload the UI polls.
type Status struct {
	Connected    bool   `json:"connected"`
	Tunnel       string `json:"tunnel"`
	Endpoint     string `json:"endpoint"`
	HandshakeAge int64  `json:"handshakeAge"` // seconds since last handshake, -1 if none
	RxBytes      int64  `json:"rxBytes"`
	TxBytes      int64  `json:"txBytes"`
	PublicIP     string `json:"publicIP"`
	Mode         string `json:"mode"` // "full" | "split"
	HasConfig    bool   `json:"hasConfig"`
	Error        string `json:"error,omitempty"`
}

func (a *App) confDir() string {
	if a.confDirMem != "" {
		return a.confDirMem
	}
	appData := os.Getenv("APPDATA")
	if appData == "" {
		home, _ := os.UserHomeDir()
		appData = filepath.Join(home, "AppData", "Roaming")
	}
	a.confDirMem = filepath.Join(appData, "HetriVPN")
	return a.confDirMem
}

func (a *App) confPath() string {
	return filepath.Join(a.confDir(), tunnelName+".conf")
}

// ensureConfig adopts an existing hand-made config on first run.
func (a *App) ensureConfig() {
	if _, err := os.Stat(a.confPath()); err == nil {
		return
	}
	_ = os.MkdirAll(a.confDir(), 0o700)
	home, _ := os.UserHomeDir()
	seed := filepath.Join(home, "wireguard", "paw-us.conf")
	if data, err := os.ReadFile(seed); err == nil {
		_ = os.WriteFile(a.confPath(), data, 0o600)
	}
}

func hidden(cmd *exec.Cmd) *exec.Cmd {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return cmd
}

func (a *App) wg(args ...string) (string, error) {
	cmd := hidden(exec.Command(filepath.Join(wgDir, "wg.exe"), args...))
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func (a *App) wireguard(args ...string) (string, error) {
	cmd := hidden(exec.Command(filepath.Join(wgDir, "wireguard.exe"), args...))
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// Connect installs the tunnel service (requires the app to run elevated).
func (a *App) Connect() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, err := os.Stat(a.confPath()); err != nil {
		return "No tunnel config found. Restore " + a.confPath()
	}
	out, err := a.wireguard("/installtunnelservice", a.confPath())
	if err != nil {
		return friendlyErr(out, err)
	}
	a.invalidateIP()
	return ""
}

// Disconnect removes the tunnel service.
func (a *App) Disconnect() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	out, err := a.wireguard("/uninstalltunnelservice", tunnelName)
	if err != nil {
		return friendlyErr(out, err)
	}
	a.invalidateIP()
	return ""
}

// SetMode switches AllowedIPs between full and split tunnel. If the tunnel
// is up it is reinstalled so the change applies immediately.
func (a *App) SetMode(mode string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	target := fullCIDR
	if mode == "split" {
		target = splitCIDR
	}
	data, err := os.ReadFile(a.confPath())
	if err != nil {
		return "Config not found"
	}
	re := regexp.MustCompile(`(?m)^AllowedIPs\s*=.*$`)
	updated := re.ReplaceAllString(string(data), "AllowedIPs = "+target)
	if err := os.WriteFile(a.confPath(), []byte(updated), 0o600); err != nil {
		return "Could not write config: " + err.Error()
	}
	if a.isUp() {
		if out, err := a.wireguard("/uninstalltunnelservice", tunnelName); err != nil {
			return friendlyErr(out, err)
		}
		// The service takes a moment to release the adapter.
		for i := 0; i < 20 && a.isUp(); i++ {
			time.Sleep(250 * time.Millisecond)
		}
		if out, err := a.wireguard("/installtunnelservice", a.confPath()); err != nil {
			return friendlyErr(out, err)
		}
	}
	a.invalidateIP()
	return ""
}

func (a *App) isUp() bool {
	_, err := a.wg("show", tunnelName)
	return err == nil
}

// GetStatus returns the current tunnel state for the UI poll loop.
func (a *App) GetStatus() Status {
	s := Status{Tunnel: tunnelName, HandshakeAge: -1, Mode: "full"}

	if data, err := os.ReadFile(a.confPath()); err == nil {
		s.HasConfig = true
		conf := string(data)
		if strings.Contains(conf, "AllowedIPs = "+splitCIDR) {
			s.Mode = "split"
		}
		if m := regexp.MustCompile(`(?m)^Endpoint\s*=\s*(.+)$`).FindStringSubmatch(conf); m != nil {
			s.Endpoint = strings.TrimSpace(m[1])
		}
	}

	dump, err := a.wg("show", tunnelName, "dump")
	if err == nil {
		s.Connected = true
		// dump: first line = interface, subsequent lines = peers:
		// pubkey psk endpoint allowed-ips latest-handshake rx tx keepalive
		lines := strings.Split(dump, "\n")
		if len(lines) > 1 {
			fields := strings.Fields(lines[1])
			if len(fields) >= 7 {
				if hs, err := strconv.ParseInt(fields[4], 10, 64); err == nil && hs > 0 {
					s.HandshakeAge = time.Now().Unix() - hs
				}
				s.RxBytes, _ = strconv.ParseInt(fields[5], 10, 64)
				s.TxBytes, _ = strconv.ParseInt(fields[6], 10, 64)
			}
		}
	}

	s.PublicIP = a.publicIP()
	return s
}

func (a *App) invalidateIP() {
	a.cachedIP = ""
	a.ipFetched = time.Time{}
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

func friendlyErr(out string, err error) string {
	if strings.Contains(out, "Access is denied") || strings.Contains(err.Error(), "Access is denied") {
		return "Hetri VPN needs to run as administrator to manage the tunnel."
	}
	if out != "" {
		return out
	}
	return fmt.Sprintf("%v", err)
}
