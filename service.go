package main

import (
	"bufio"
	"encoding/json"
	"net"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows/svc"
)

// pipeSDDL grants access to SYSTEM, Administrators, and interactive users.
// This is a single-user machine; tighten to a specific SID before ever
// shipping to other people.
const pipeSDDL = "D:P(A;;GA;;;SY)(A;;GA;;;BA)(A;;GA;;;IU)"

type pipeRequest struct {
	Cmd string `json:"cmd"`
	Arg string `json:"arg,omitempty"`
}

type pipeReply struct {
	Error  string        `json:"error,omitempty"`
	Status *TunnelStatus `json:"status,omitempty"`
}

// runService is the /service entry point, run by the SCM as SYSTEM.
func runService() {
	_ = svc.Run(svcName, &managerService{})
}

type managerService struct{}

func (m *managerService) Execute(args []string, req <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	status <- svc.Status{State: svc.StartPending}

	listener, err := winio.ListenPipe(pipeName, &winio.PipeConfig{
		SecurityDescriptor: pipeSDDL,
		MessageMode:        false,
	})
	if err != nil {
		return false, 1
	}
	defer listener.Close()

	go acceptLoop(listener)

	status <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	for c := range req {
		switch c.Cmd {
		case svc.Interrogate:
			status <- c.CurrentStatus
		case svc.Stop, svc.Shutdown:
			status <- svc.Status{State: svc.StopPending}
			return false, 0
		}
	}
	return false, 0
}

func acceptLoop(listener net.Listener) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		go handleConn(conn)
	}
}

func handleConn(conn net.Conn) {
	defer conn.Close()
	reader := bufio.NewReader(conn)
	line, err := reader.ReadBytes('\n')
	if err != nil {
		return
	}
	var req pipeRequest
	if err := json.Unmarshal(line, &req); err != nil {
		return
	}

	var reply pipeReply
	switch req.Cmd {
	case "status":
		s := tunnelStatus()
		s.ServiceInstalled = true
		reply.Status = &s
	case "connect":
		reply.Error = tunnelConnect()
	case "disconnect":
		reply.Error = tunnelDisconnect()
	case "setmode":
		reply.Error = tunnelSetMode(req.Arg)
	default:
		reply.Error = "unknown command"
	}

	out, _ := json.Marshal(reply)
	out = append(out, '\n')
	_, _ = conn.Write(out)
}
