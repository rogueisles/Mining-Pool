package proxy

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net"
	"testing"
	"time"

	"github.com/HawyUkr/open-progpow-pool/forgepool"
	"github.com/HawyUkr/open-progpow-pool/policy"
)

// A local TCP protocol regression, NOT a connection from NiceHash. The
// templates are fixtures and no difficulty-50000 winning share is fabricated.
func TestForgeNiceHashStratumTCP(t *testing.T) {
	s, _, job, writer, _, _ := testForgeServer(t)
	s.config.Proxy.Difficulty = 50000
	s.forge.shareTarget, _ = forgepool.StratumTargetForDifficulty(big.NewInt(50000))
	s.sessions = make(map[*Session]struct{})
	s.policy = &policy.PolicyServer{} // Login/handshake only: no Redis worker.
	s.timeout = 5 * time.Second
	var previous string
	for _, authorizeFirst := range []bool{false, true} {
		t.Run(fmt.Sprint(authorizeFirst), func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			done := make(chan struct{})
			go func() {
				defer close(done)
				c, err := listener.Accept()
				if err != nil {
					return
				}
				s.serveTCPClient(&Session{conn: c, ip: "127.0.0.1"})
			}()
			m := connectProcessMiner(t, listener.Addr().String())
			defer func() {
				m.conn.Close()
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Error("session did not stop")
				}
			}()
			login := "0x1111111111111111111111111111111111111111.nh"
			if authorizeFirst {
				m.call("mining.authorize", []string{login, "x"}, 0)
			}
			result := m.call("mining.subscribe", []string{"NiceHash/1.0.0"}, 0)
			var row []json.RawMessage
			var extra string
			var size int
			if json.Unmarshal(result, &row) != nil || len(row) != 3 || json.Unmarshal(row[1], &extra) != nil || json.Unmarshal(row[2], &size) != nil {
				t.Fatal("subscribe schema")
			}
			if len(extra) != 8 || size != 4 || extra == previous {
				t.Fatal("extranonce bounds or reconnect uniqueness", extra, size)
			}
			previous = extra
			m.extra1, m.extra2size = extra, size
			if string(m.call("mining.extranonce.subscribe", []interface{}{}, 0)) != "true" {
				t.Fatal("extension rejected")
			}
			if !authorizeFirst {
				m.call("mining.authorize", []string{login, "x"}, 0)
			}
			m.call("mining.extranonce.subscribe", []interface{}{}, 0) // Drain preceding pushes.
			if m.difficulty != 50000 || len(m.notify) != 9 {
				t.Fatal("wrong announced difficulty or missing job", m.difficulty)
			}
			header, id, _ := processNotifyHeader(t, m)
			if id != job.ID || len(header) != 80 {
				t.Fatal("nonstandard job")
			}
			// Unknown extension receives a standard tuple without disconnecting.
			m.call("mining.unsupported_extension", []interface{}{}, 20)
			m.call("mining.extranonce.subscribe", []interface{}{}, 0)
			resub := m.call("mining.subscribe", []string{"NiceHash/1.0.0"}, 0)
			if json.Unmarshal(resub, &row) != nil || json.Unmarshal(row[1], &extra) != nil || extra != m.extra1 {
				t.Fatal("same-session extranonce changed")
			}
			m.call("mining.extranonce.subscribe", []interface{}{}, 0)
			// Check real Scrypt rejection at the advertised target independently
			// of policy accounting. Never substitute a mock winning hash.
			extra1, _ := hex.DecodeString(m.extra1)
			cs := &Session{extraNonce1: extra1}
			foundLow := false
			for nonce := uint32(0); nonce < 100; nonce++ {
				h, err := job.Digibyte.Header(extra1, []byte{0, 0, 0, 0}, job.Digibyte.Template.CurTime, nonce)
				if err != nil {
					t.Fatal(err)
				}
				raw, err := forgepool.ScryptHash(h)
				if err != nil {
					t.Fatal(err)
				}
				if forgepool.HashValue(raw).Cmp(s.forge.shareTarget) <= 0 {
					continue
				}
				_, _, _, err = s.validateForgeShare(cs, job.ID, "00000000", fmt.Sprintf("%08x", job.Digibyte.Template.CurTime), fmt.Sprintf("%08x", nonce), time.Now())
				if !errors.Is(err, errForgeLowDifficulty) {
					t.Fatal("advertised target not enforced", err)
				}
				foundLow = true
				break
			}
			if !foundLow || writer.calls != 0 {
				t.Fatal("invalid share credited or low-hash fixture absent")
			}
		})
	}
}

func TestForgeNiceHashTargetCoversOGGDiffOne(t *testing.T) {
	share, err := forgepool.StratumTargetForDifficulty(big.NewInt(50000))
	if err != nil {
		t.Fatal(err)
	}
	network, err := forgepool.TargetForDifficulty(big.NewInt(1))
	if err != nil || share.Cmp(network) <= 0 {
		t.Fatal("OGG diff1 winners could be withheld at configured share target")
	}
	// Exact independent arithmetic: Scrypt share units are network units /65536.
	want := new(big.Int).Lsh(new(big.Int).Set(network), 16)
	want.Div(want, big.NewInt(50000))
	if share.Cmp(want) != 0 {
		t.Fatal("wrong Scrypt difficulty units")
	}
}
