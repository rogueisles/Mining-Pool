package proxy

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/HawyUkr/open-progpow-pool/rpc"
)

func TestForgeRefreshSignalAfterAllAttemptsBeforeAccounting(t *testing.T) {
	f := newIntentFixture(t)
	rt := f.s.forge
	rt.refreshRequests = make(chan struct{}, 1)
	f.daemons["DGB"].reply = "lost"
	f.writer.err = errors.New("Redis offline")
	f.router.refresh = func() {
		for _, d := range f.daemons {
			d.mu.Lock()
			n := d.submits
			d.mu.Unlock()
			if n != 1 {
				t.Error("refresh preceded a chain submission")
			}
		}
		if f.writer.calls != 0 {
			t.Error("refresh waited for accounting")
		}
		rt.requestJobRefresh()
	}
	if err := f.submit(t); err == nil {
		t.Fatal("Redis failure missing")
	}
	if len(rt.refreshRequests) != 1 {
		t.Fatal("missing immediate refresh signal")
	}
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); rt.requestJobRefresh() }()
	}
	wg.Wait()
	if len(rt.refreshRequests) != 1 {
		t.Fatal("refresh burst not coalesced")
	}
}

func TestForgeImmediateRefreshAndBoundedSignerRetry(t *testing.T) {
	for _, failFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "immediate", true: "signer-retry"}[failFirst], func(t *testing.T) {
			s, _, fixture, _, _, _ := testForgeServer(t)
			rt := s.forge
			rt.refreshRequests = make(chan struct{}, 1)
			work := *fixture.OGG
			work.Difficulty = "0x1"
			node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				json.NewEncoder(w).Encode(map[string]interface{}{"id": 0, "result": work})
			}))
			defer node.Close()
			gbt := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				json.NewEncoder(w).Encode(map[string]interface{}{"id": 0, "result": rpc.BlockTemplateReply{PowAlgo: "scrypt", PowAlgoID: func() *int { n:=1; return &n }(),Version: 1, Height: 2000, CurTime: int64(fixture.Digibyte.Template.CurTime), PreviousBlockHash: strings.Repeat("aa", 32), Bits: "1d00ffff", CoinbaseValue: 5000000000}})
			}))
			defer gbt.Close()
			s.upstreams = []*rpc.RPCClient{rpc.NewRPCClient("issuer", node.URL, "1s", "", "")}
			rt.dgbRPC = rpc.NewRPCClient("dgb", gbt.URL, "1s", "", "")
			rt.dogeRPC = rpc.NewRPCClient("doge", gbt.URL, "1s", "", "")
			rt.dgbPayout = []byte{0x51}
			rt.dogePayout = []byte{0x52}
			signer, _ := templateTestSigner(t, rt, nil)
			target, _ := url.Parse(signer.URL)
			forward := httputil.NewSingleHostReverseProxy(target)
			var calls int32
			reject := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := atomic.AddInt32(&calls, 1)
				if failFirst && n == 1 {
					http.Error(w, "test template race", 422)
					return
				}
				forward.ServeHTTP(w, r)
			}))
			defer reject.Close()
			rt.signers[0].url = reject.URL
			rt.mu.Lock()
			rt.jobs = make(map[string]*forgeJob)
			rt.currentID = ""
			rt.order = nil
			rt.mu.Unlock()
			stop := make(chan struct{})
			done := make(chan struct{})
			go func() { defer close(done); s.runTemplateRefresh(time.Hour, stop) }()
			defer func() { close(stop); <-done }()
			start := time.Now()
			rt.requestJobRefresh()
			deadline := start.Add(3 * time.Second)
			for time.Now().Before(deadline) {
				job := rt.currentJob()
				if job != nil {
					if !job.Clean || job.Signed == nil {
						t.Fatal("changed tip published without clean signed job")
					}
					if err := job.Signed.validate(job, rt.authorized, rt.templateQuorum, time.Now()); err != nil {
						t.Fatal(err)
					}
					if failFirst && (atomic.LoadInt32(&calls) != 2 || time.Since(start) < 450*time.Millisecond) {
						t.Fatal("retry was not bounded")
					}
					if !failFirst && atomic.LoadInt32(&calls) != 1 {
						t.Fatal("duplicate signer call")
					}
					return
				}
				time.Sleep(5 * time.Millisecond)
			}
			t.Fatal("event waited for hourly poll or failed to retry")
		})
	}
}
