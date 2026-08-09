package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc/mgr"
)

// hiddenCmd suppresses the console window when shelling out to sc.exe etc.
func hiddenCmd(cmd *exec.Cmd) *exec.Cmd {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return cmd
}

func pipeCall(cmd, arg string) (*pipeReply, error) {
	timeout := 2 * time.Second
	conn, err := winio.DialPipe(pipeName, &timeout)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	req, _ := json.Marshal(pipeRequest{Cmd: cmd, Arg: arg})
	req = append(req, '\n')
	if _, err := conn.Write(req); err != nil {
		return nil, err
	}
	_ = conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		return nil, err
	}
	var reply pipeReply
	if err := json.Unmarshal(line, &reply); err != nil {
		return nil, err
	}
	return &reply, nil
}

func serviceReachable() bool {
	timeout := 500 * time.Millisecond
	conn, err := winio.DialPipe(pipeName, &timeout)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

func isElevated() bool {
	return windows.GetCurrentProcessToken().IsElevated()
}

// relaunchElevated runs this same exe with the given flag via the UAC
// consent dialog. Fire and forget; callers poll for the result.
func relaunchElevated(flag string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	verb, _ := windows.UTF16PtrFromString("runas")
	file, _ := windows.UTF16PtrFromString(exe)
	params, _ := windows.UTF16PtrFromString(flag)
	return windows.ShellExecute(0, verb, file, params, nil, windows.SW_HIDE)
}

// installService registers this binary as the auto-start manager service and
// seeds the machine config. Must run elevated (the /install-service flag).
func installService() error {
	if err := seedMachineConfig(); err != nil {
		return err
	}

	exe, err := os.Executable()
	if err != nil {
		return err
	}
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()

	var s *mgr.Service
	if s, err = m.OpenService(svcName); err == nil {
		// Upgrade path: enforce current config on the existing service.
		if cfg, err := s.Config(); err == nil {
			cfg.StartType = mgr.StartManual
			cfg.BinaryPathName = `"` + exe + `" /service`
			_ = s.UpdateConfig(cfg)
		}
	} else {
		s, err = m.CreateService(svcName, exe, mgr.Config{
			StartType:   mgr.StartManual,
			DisplayName: "Hetri VPN Manager",
			Description: "Runs tunnel operations for the Hetri VPN app while it is open.",
		}, "/service")
		if err != nil {
			return err
		}
	}
	defer s.Close()
	// Let interactive users start/stop the manager without elevation, so the
	// UI can run it only while the app is open (no resident processes, no
	// per-launch UAC).
	_ = hiddenCmd(exec.Command("sc.exe", "sdset", svcName,
		"D:(A;;CCLCSWRPWPDTLOCRRC;;;SY)(A;;CCDCLCSWRPWPDTLOCRSDRCWDWO;;;BA)(A;;CCLCSWRPWPDTLOCRRC;;;IU)(A;;CCLCSWLOCRRC;;;SU)")).Run()
	return s.Start()
}

func uninstallService() error {
	// Take the tunnel down first so no orphaned WireGuardTunnel$ service
	// keeps routing traffic after the app is gone.
	if tunnelIsUp() {
		_ = tunnelDisconnect()
	}
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := m.OpenService(svcName)
	if err != nil {
		return nil
	}
	defer s.Close()
	_, _ = s.Control(1) // SERVICE_CONTROL_STOP
	return s.Delete()
}

// seedMachineConfig copies the user's existing tunnel config into the
// service-owned ProgramData location on first install.
func seedMachineConfig() error {
	if _, err := os.Stat(machineConfPath()); err == nil {
		return nil
	}
	if err := os.MkdirAll(machineConfDir(), 0o700); err != nil {
		return err
	}
	home, _ := os.UserHomeDir()
	appData := os.Getenv("APPDATA")
	candidates := []string{
		filepath.Join(appData, "HetriVPN", tunnelName+".conf"),
		filepath.Join(home, "wireguard", tunnelName+".conf"),
	}
	for _, c := range candidates {
		if data, err := os.ReadFile(c); err == nil {
			return os.WriteFile(machineConfPath(), data, 0o600)
		}
	}
	return fmt.Errorf("no existing %s.conf found to adopt", tunnelName)
}

func copyFileErr(err error) string {
	if err == nil {
		return ""
	}
	if err == io.EOF {
		return "service closed the connection"
	}
	return err.Error()
}
