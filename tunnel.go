package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.zx2c4.com/wireguard/windows/driver"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

const (
	tunnelName = "paw-us"
	fullCIDR   = "0.0.0.0/0"
	splitCIDR  = "10.100.0.0/24"
	pipeName   = `\\.\pipe\HetriVPN`
	svcName    = "HetriVPNManager"
	// Our own service name. The embedded engine passes its internal
	// "WireGuardTunnel$..." name to the SCM dispatcher, but Windows ignores
	// that string for own-process services, so the registered name is ours.
	tunnelSvcName = "HetriVPNTunnel$" + tunnelName
	// Name used by older installs and the official client; cleaned up on connect.
	legacyTunnelSvcName = "WireGuardTunnel$" + tunnelName
	// Windows FILETIME epoch (1601) to Unix epoch offset, in 100ns units.
	filetimeToUnix = 116444736000000000
)

func machineConfDir() string {
	pd := os.Getenv("ProgramData")
	if pd == "" {
		pd = `C:\ProgramData`
	}
	return filepath.Join(pd, "HetriVPN")
}

func machineConfPath() string {
	return filepath.Join(machineConfDir(), tunnelName+".conf")
}

// TunnelStatus is shared between the service (producer) and UI (consumer).
type TunnelStatus struct {
	Connected        bool   `json:"connected"`
	Tunnel           string `json:"tunnel"`
	Endpoint         string `json:"endpoint"`
	HandshakeAge     int64  `json:"handshakeAge"`
	RxBytes          int64  `json:"rxBytes"`
	TxBytes          int64  `json:"txBytes"`
	PublicIP         string `json:"publicIP"`
	Mode             string `json:"mode"`
	HasConfig        bool   `json:"hasConfig"`
	ServiceInstalled bool   `json:"serviceInstalled"`
	Error            string `json:"error,omitempty"`
}

func tunnelIsUp() bool {
	m, err := mgr.Connect()
	if err != nil {
		return false
	}
	defer m.Disconnect()
	s, err := m.OpenService(tunnelSvcName)
	if err != nil {
		return false
	}
	defer s.Close()
	st, err := s.Query()
	return err == nil && st.State == svc.Running
}

// tunnelConnect registers (if needed) and starts the tunnel service, which
// is this same binary running the embedded engine via /tunnelservice.
func tunnelConnect() string {
	if _, err := os.Stat(machineConfPath()); err != nil {
		return "No tunnel config at " + machineConfPath()
	}
	m, err := mgr.Connect()
	if err != nil {
		return err.Error()
	}
	defer m.Disconnect()

	// Remove a legacy-named tunnel service left by older installs so two
	// services never fight over the same adapter.
	if legacy, err := m.OpenService(legacyTunnelSvcName); err == nil {
		_, _ = legacy.Control(svc.Stop)
		for i := 0; i < 40; i++ {
			st, err := legacy.Query()
			if err != nil || st.State == svc.Stopped {
				break
			}
			time.Sleep(250 * time.Millisecond)
		}
		_ = legacy.Delete()
		legacy.Close()
	}

	if s, err := m.OpenService(tunnelSvcName); err == nil {
		defer s.Close()
		if st, err := s.Query(); err == nil && st.State == svc.Running {
			return ""
		}
		if err := s.Start(); err != nil {
			return err.Error()
		}
		return ""
	}

	exe, err := os.Executable()
	if err != nil {
		return err.Error()
	}
	s, err := m.CreateService(tunnelSvcName, exe, mgr.Config{
		StartType:    mgr.StartAutomatic,
		DisplayName:  "Hetri VPN Tunnel: " + tunnelName,
		Dependencies: []string{"Nsi"},
	}, "/tunnelservice", machineConfPath())
	if err != nil {
		return err.Error()
	}
	defer s.Close()
	if err := s.Start(); err != nil {
		return err.Error()
	}
	return ""
}

func tunnelDisconnect() string {
	m, err := mgr.Connect()
	if err != nil {
		return err.Error()
	}
	defer m.Disconnect()
	s, err := m.OpenService(tunnelSvcName)
	if err != nil {
		return "" // nothing to disconnect
	}
	defer s.Close()
	_, _ = s.Control(svc.Stop)
	for i := 0; i < 40; i++ {
		st, err := s.Query()
		if err != nil || st.State == svc.Stopped {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if err := s.Delete(); err != nil && !strings.Contains(err.Error(), "marked for deletion") {
		return err.Error()
	}
	return ""
}

func tunnelSetMode(mode string) string {
	target := fullCIDR
	if mode == "split" {
		target = splitCIDR
	}
	data, err := os.ReadFile(machineConfPath())
	if err != nil {
		return "Config not found"
	}
	re := regexp.MustCompile(`(?m)^AllowedIPs\s*=.*$`)
	updated := re.ReplaceAllString(string(data), "AllowedIPs = "+target)
	if err := os.WriteFile(machineConfPath(), []byte(updated), 0o600); err != nil {
		return "Could not write config: " + err.Error()
	}
	if tunnelIsUp() {
		if msg := tunnelDisconnect(); msg != "" {
			return msg
		}
		if msg := tunnelConnect(); msg != "" {
			return msg
		}
	}
	return ""
}

func tunnelStatus() TunnelStatus {
	s := TunnelStatus{Tunnel: tunnelName, HandshakeAge: -1, Mode: "full"}

	if data, err := os.ReadFile(machineConfPath()); err == nil {
		s.HasConfig = true
		conf := string(data)
		if strings.Contains(conf, "AllowedIPs = "+splitCIDR) {
			s.Mode = "split"
		}
		if m := regexp.MustCompile(`(?m)^Endpoint\s*=\s*(.+)$`).FindStringSubmatch(conf); m != nil {
			s.Endpoint = strings.TrimSpace(m[1])
		}
	}

	adapter, err := driver.OpenAdapter(tunnelName)
	if err != nil {
		return s
	}
	defer adapter.Close()
	cfg, err := adapter.Configuration()
	if err != nil {
		return s
	}
	s.Connected = true
	if peer := cfg.FirstPeer(); peer != nil {
		if peer.LastHandshake > 0 {
			unix := (int64(peer.LastHandshake) - filetimeToUnix) / 10_000_000
			s.HandshakeAge = time.Now().Unix() - unix
		}
		s.RxBytes = int64(peer.RxBytes)
		s.TxBytes = int64(peer.TxBytes)
	}
	return s
}

// errText kept for pipe/client call sites.
func errText(out string, err error) string {
	if out != "" {
		return out
	}
	return err.Error()
}

var _ = strconv.Itoa // retained import guard during refactors
