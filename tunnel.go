package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	tunnelName = "paw-us"
	wgDir      = `C:\Program Files\WireGuard`
	fullCIDR   = "0.0.0.0/0"
	splitCIDR  = "10.100.0.0/24"
	pipeName   = `\\.\pipe\HetriVPN`
	svcName    = "HetriVPNManager"
)

// machineConfDir is the service-owned config location, readable regardless
// of which user session asks (the service runs as SYSTEM).
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

func hiddenCmd(cmd *exec.Cmd) *exec.Cmd {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return cmd
}

func runWG(args ...string) (string, error) {
	cmd := hiddenCmd(exec.Command(filepath.Join(wgDir, "wg.exe"), args...))
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func runWireGuard(args ...string) (string, error) {
	cmd := hiddenCmd(exec.Command(filepath.Join(wgDir, "wireguard.exe"), args...))
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func tunnelIsUp() bool {
	_, err := runWG("show", tunnelName)
	return err == nil
}

func tunnelConnect() string {
	if _, err := os.Stat(machineConfPath()); err != nil {
		return "No tunnel config at " + machineConfPath()
	}
	if out, err := runWireGuard("/installtunnelservice", machineConfPath()); err != nil {
		return errText(out, err)
	}
	return ""
}

func tunnelDisconnect() string {
	if out, err := runWireGuard("/uninstalltunnelservice", tunnelName); err != nil {
		return errText(out, err)
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
		if out, err := runWireGuard("/uninstalltunnelservice", tunnelName); err != nil {
			return errText(out, err)
		}
		for i := 0; i < 20 && tunnelIsUp(); i++ {
			time.Sleep(250 * time.Millisecond)
		}
		if out, err := runWireGuard("/installtunnelservice", machineConfPath()); err != nil {
			return errText(out, err)
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

	dump, err := runWG("show", tunnelName, "dump")
	if err == nil {
		s.Connected = true
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
	return s
}

func errText(out string, err error) string {
	if out != "" {
		return out
	}
	return err.Error()
}
