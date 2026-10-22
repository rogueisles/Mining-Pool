package proxy

import (
	"net"
	"testing"
	"time"
)

func TestForgePauseDisconnectsActiveMinerSession(t *testing.T) {
	serverConn, minerConn := net.Pipe()
	defer minerConn.Close()

	idleServer, idleClient := net.Pipe()
	defer idleServer.Close()
	defer idleClient.Close()

	active := &Session{
		conn:       serverConn,
		login:      "0x1111111111111111111111111111111111111111.rig",
		subscribed: true,
		authorized: true,
	}

	idle := &Session{
		conn:       idleServer,
		subscribed: true,
		authorized: false,
	}

	s := &ProxyServer{
		sessions: map[*Session]struct{}{
			active: struct{}{},
			idle:   struct{}{},
		},
	}

	// Arm the deadline while the pipe is known to be open. After the pause
	// disconnect, the read below must fail because the server end was closed.
	if err := minerConn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("set miner read deadline before disconnect: %v", err)
	}

	if got := s.disconnectForgeMinersForPause(); got != 1 {
		t.Fatalf("expected 1 active miner disconnected, got %d", got)
	}

	s.sessionsMu.RLock()
	_, activeStillPresent := s.sessions[active]
	_, idleStillPresent := s.sessions[idle]
	s.sessionsMu.RUnlock()

	if activeStillPresent {
		t.Fatal("active FORGE miner remained registered after pause disconnect")
	}
	if !idleStillPresent {
		t.Fatal("non-authorized session should remain registered")
	}

	var one [1]byte
	if _, err := minerConn.Read(one[:]); err == nil {
		t.Fatal("active miner socket remained open after full FORGE pause")
	}
}
