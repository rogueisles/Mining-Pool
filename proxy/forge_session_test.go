package proxy

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/HawyUkr/open-progpow-pool/policy"
)

func TestForgeStratumErrorTupleAndLegacyShape(t *testing.T) {
	var out bytes.Buffer
	cs := &Session{enc: json.NewEncoder(&out)}
	reply := &ErrorReply{Code: 22, Message: "Duplicate share"}
	if err := cs.sendStratumError(json.RawMessage("7"), reply); err != nil {
		t.Fatal(err)
	}
	if out.String() != "{\"id\":7,\"result\":null,\"error\":[22,\"Duplicate share\",null]}\n" {
		t.Fatal(out.String())
	}
	out.Reset()
	_ = cs.sendTCPError(json.RawMessage("8"), reply)
	var legacy struct {
		Version string     `json:"jsonrpc"`
		Error   ErrorReply `json:"error"`
	}
	if err := json.Unmarshal(out.Bytes(), &legacy); err != nil || legacy.Version != "2.0" || legacy.Error.Code != 22 {
		t.Fatal("legacy shape changed", out.String(), err)
	}
}

func TestForgeSessionAuthorizationDuringBroadcast(t *testing.T) {
	s, cs, _, _, _, _ := testForgeServer(t)
	s.sessions = make(map[*Session]struct{})
	// Empty in-memory blacklist; no Redis or policy worker is needed here.
	s.policy = &policy.PolicyServer{}
	cs.enc = json.NewEncoder(io.Discard)
	s.registerSession(cs)
	var wg sync.WaitGroup
	for n := 0; n < 2; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				s.broadcastForgeJobs()
			}
		}()
	}
	for i := 0; i < 100; i++ {
		params, _ := json.Marshal([]string{"0x1111111111111111111111111111111111111111.rig"})
		if err := cs.handleForgeTCPMessage(s, &StratumReq{JSONRpcReq: JSONRpcReq{Id: json.RawMessage("1"), Method: "mining.authorize", Params: params}}); err != nil {
			t.Fatal(err)
		}
		if err := cs.handleForgeTCPMessage(s, &StratumReq{JSONRpcReq: JSONRpcReq{Id: json.RawMessage("2"), Method: "mining.subscribe"}}); err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
}

func TestTCPSessionEOFClosesSocketAndRemovesRegistration(t *testing.T) {
	server, client := net.Pipe()
	defer client.Close()
	s := &ProxyServer{sessions: make(map[*Session]struct{}), timeout: time.Second}
	cs := &Session{conn: server}
	s.registerSession(cs)
	done := make(chan struct{})
	go func() { s.serveTCPClient(cs); close(done) }()
	client.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("EOF leaked handler")
	}
	s.sessionsMu.RLock()
	_, exists := s.sessions[cs]
	s.sessionsMu.RUnlock()
	if exists {
		t.Fatal("EOF retained session")
	}
	if _, err := server.Write([]byte("x")); err == nil {
		t.Fatal("socket remained open")
	}
}
