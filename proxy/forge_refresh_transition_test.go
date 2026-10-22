package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/HawyUkr/open-progpow-pool/forgepool"
	"github.com/HawyUkr/open-progpow-pool/rpc"
)

type forgeTransitionRPC struct {
	mu      sync.RWMutex
	healthy bool
	reply   rpc.BlockTemplateReply
}

func (r *forgeTransitionRPC) setHealthy(healthy bool) {
	r.mu.Lock()
	r.healthy = healthy
	r.mu.Unlock()
}

func (r *forgeTransitionRPC) handler(w http.ResponseWriter, req *http.Request) {
	r.mu.RLock()
	healthy := r.healthy
	reply := r.reply
	r.mu.RUnlock()

	if !healthy {
		http.Error(w, "external chain unavailable", http.StatusServiceUnavailable)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      0,
		"result":  reply,
	})
}

func newForgeTransitionHarness(
	t *testing.T,
) (
	*ProxyServer,
	*forgeRuntime,
	*forgeTransitionRPC,
	*forgeTransitionRPC,
) {
	t.Helper()

	s, _, fixture, _, _, _ := testForgeServer(t)

	work := *fixture.OGG
	work.Difficulty = "0x1"

	nodeServer := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, req *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      0,
				"result":  work,
			})
		},
	))
	t.Cleanup(nodeServer.Close)

	reply := rpc.BlockTemplateReply{PowAlgo: "scrypt", PowAlgoID: func() *int { n:=1; return &n }(),
		Version:           1,
		Height:            2000,
		CurTime:           int64(fixture.Digibyte.Template.CurTime),
		PreviousBlockHash: strings.Repeat("22", 32),
		Bits:              "1d00ffff",
		CoinbaseValue:     5000000000,
	}

	dgbState := &forgeTransitionRPC{
		healthy: true,
		reply:   reply,
	}
	dogeState := &forgeTransitionRPC{
		healthy: true,
		reply:   reply,
	}

	dgbServer := httptest.NewServer(
		http.HandlerFunc(dgbState.handler),
	)
	dogeServer := httptest.NewServer(
		http.HandlerFunc(dogeState.handler),
	)

	t.Cleanup(dgbServer.Close)
	t.Cleanup(dogeServer.Close)

	rt := s.forge

	rt.jobs = make(map[string]*forgeJob)
	rt.order = nil
	rt.currentID = ""
	rt.duplicates = make(map[string]struct{})

	s.upstreams = []*rpc.RPCClient{
		rpc.NewRPCClient(
			"issuer",
			nodeServer.URL,
			"2s",
			"",
			"",
		),
	}

	rt.dgbRPC = rpc.NewRPCClient(
		"dgb",
		dgbServer.URL,
		"2s",
		"",
		"",
	)
	rt.dogeRPC = rpc.NewRPCClient(
		"doge",
		dogeServer.URL,
		"2s",
		"",
		"",
	)

	rt.dgbPayout = []byte{0x51}
	rt.dogePayout = []byte{0x52}

	templateTestSigner(t, rt, nil)

	return s, rt, dgbState, dogeState
}

func requireForgeCurrentMask(
	t *testing.T,
	rt *forgeRuntime,
	want uint8,
) *forgeJob {
	t.Helper()

	job := rt.currentJob()
	if job == nil {
		t.Fatalf("missing current FORGE job; want mask %#x", want)
	}
	if job.ExternalMask != want {
		t.Fatalf(
			"current mask=%#x want %#x",
			job.ExternalMask,
			want,
		)
	}

	return job
}

func TestForgeRefreshDOGEFailureAndRecoveryTransition(t *testing.T) {
	s, rt, _, doge := newForgeTransitionHarness(t)

	// Healthy startup publishes full DGB+DOGE participation.
	s.fetchForgeBlockTemplate()

	full := requireForgeCurrentMask(
		t,
		rt,
		forgepool.ExternalMask,
	)
	fullID := full.ID

	if full.Digibyte == nil ||
		!full.Digibyte.DigibyteActive ||
		full.Digibyte.DOGE == nil {
		t.Fatal("healthy 0x23 job does not contain DGB+DOGE")
	}

	// DOGE failure #1 and #2 must NOT prematurely publish DGB-only work.
	doge.setHealthy(false)

	for attempt := 1; attempt <= 2; attempt++ {
		s.fetchForgeBlockTemplate()

		job := requireForgeCurrentMask(
			t,
			rt,
			forgepool.ExternalMask,
		)

		if job.ID != fullID {
			t.Fatalf(
				"DOGE failure #%d replaced full job before threshold: %s -> %s",
				attempt,
				fullID,
				job.ID,
			)
		}

		if !rt.dogeHealth.required() {
			t.Fatalf(
				"DOGE became optional after only %d failures",
				attempt,
			)
		}
	}

	// Failure #3 crosses pool hysteresis and must publish clean 0x21.
	s.fetchForgeBlockTemplate()

	dgbOnly := requireForgeCurrentMask(
		t,
		rt,
		forgepool.ExternalDGB,
	)

	if rt.dogeHealth.required() {
		t.Fatal("DOGE still required after third failure")
	}
	if !dgbOnly.Clean {
		t.Fatal("0x23 -> 0x21 transition was not a clean Stratum job")
	}
	if dgbOnly.Digibyte == nil || !dgbOnly.Digibyte.DigibyteActive {
		t.Fatal("0x21 job does not use the real Digibyte parent")
	}
	if dgbOnly.Digibyte.DOGE != nil {
		t.Fatal("0x21 job retained DOGE candidate")
	}

	// Recovery success #1 keeps DOGE excluded.
	doge.setHealthy(true)
	s.fetchForgeBlockTemplate()

	stillDGBOnly := requireForgeCurrentMask(
		t,
		rt,
		forgepool.ExternalDGB,
	)

	if rt.dogeHealth.required() {
		t.Fatal("DOGE restored after only one success")
	}
	if stillDGBOnly.Clean {
		t.Fatal("unchanged 0x21 mode unexpectedly forced clean transition")
	}

	// Recovery success #2 restores full 0x23 and must be clean.
	s.fetchForgeBlockTemplate()

	restored := requireForgeCurrentMask(
		t,
		rt,
		forgepool.ExternalMask,
	)

	if !rt.dogeHealth.required() {
		t.Fatal("DOGE not restored after second success")
	}
	if !restored.Clean {
		t.Fatal("0x21 -> 0x23 recovery was not a clean Stratum job")
	}
	if restored.Digibyte == nil ||
		!restored.Digibyte.DigibyteActive ||
		restored.Digibyte.DOGE == nil {
		t.Fatal("restored 0x23 job does not contain DGB+DOGE")
	}
}

func TestForgeRefreshDGBFailureAndRecoveryTransition(t *testing.T) {
	s, rt, dgb, _ := newForgeTransitionHarness(t)

	// Healthy startup -> full 0x23.
	s.fetchForgeBlockTemplate()

	full := requireForgeCurrentMask(
		t,
		rt,
		forgepool.ExternalMask,
	)
	fullID := full.ID

	// DGB failure #1/#2 must retain the existing full job.
	dgb.setHealthy(false)

	for attempt := 1; attempt <= 2; attempt++ {
		s.fetchForgeBlockTemplate()

		job := requireForgeCurrentMask(
			t,
			rt,
			forgepool.ExternalMask,
		)

		if job.ID != fullID {
			t.Fatalf(
				"DGB failure #%d replaced full job before threshold: %s -> %s",
				attempt,
				fullID,
				job.ID,
			)
		}

		if !rt.dgbHealth.required() {
			t.Fatalf(
				"DGB became optional after only %d failures",
				attempt,
			)
		}
	}

	// DGB failure #3 -> clean DOGE-only external mode (0x22).
	s.fetchForgeBlockTemplate()

	dogeOnly := requireForgeCurrentMask(
		t,
		rt,
		forgepool.ExternalDOGE,
	)

	if rt.dgbHealth.required() {
		t.Fatal("DGB still required after third failure")
	}
	if !dogeOnly.Clean {
		t.Fatal("0x23 -> 0x22 transition was not clean")
	}
	if dogeOnly.Digibyte == nil {
		t.Fatal("0x22 job has no Scrypt carrier")
	}
	if dogeOnly.Digibyte.DigibyteActive {
		t.Fatal("0x22 synthetic parent incorrectly marked Digibyte-active")
	}
	if dogeOnly.Digibyte.DOGE == nil {
		t.Fatal("0x22 job omitted DOGE candidate")
	}
	if dogeOnly.Digibyte.Template.Previous != (forgepool.Hash{}) {
		t.Fatal("0x22 synthetic carrier unexpectedly has Digibyte previous hash")
	}
	if dogeOnly.Signed == nil ||
		dogeOnly.Signed.Template.Version ==
			forgepool.OGGTemplateVersionV2Base {
		t.Fatal("0x22 transition produced forbidden OGG-only template")
	}

	// DGB recovery #1 -> remain 0x22.
	dgb.setHealthy(true)
	s.fetchForgeBlockTemplate()

	stillDOGEOnly := requireForgeCurrentMask(
		t,
		rt,
		forgepool.ExternalDOGE,
	)

	if rt.dgbHealth.required() {
		t.Fatal("DGB restored after only one success")
	}
	if stillDOGEOnly.Clean {
		t.Fatal("unchanged 0x22 mode unexpectedly forced clean transition")
	}

	// DGB recovery #2 -> clean full 0x23.
	s.fetchForgeBlockTemplate()

	restored := requireForgeCurrentMask(
		t,
		rt,
		forgepool.ExternalMask,
	)

	if !rt.dgbHealth.required() {
		t.Fatal("DGB not restored after second success")
	}
	if !restored.Clean {
		t.Fatal("0x22 -> 0x23 recovery was not clean")
	}
	if restored.Digibyte == nil ||
		!restored.Digibyte.DigibyteActive ||
		restored.Digibyte.DOGE == nil {
		t.Fatal("restored 0x23 job does not contain DGB+DOGE")
	}
}

func TestForgeRefreshBothDownPausesAndRecovers(t *testing.T) {
	s, rt, dgb, doge := newForgeTransitionHarness(t)

	// Establish healthy full mode.
	s.fetchForgeBlockTemplate()

	full := requireForgeCurrentMask(
		t,
		rt,
		forgepool.ExternalMask,
	)
	fullID := full.ID

	dgb.setHealthy(false)
	doge.setHealthy(false)

	// Both failing #1/#2: still required, retain the previously signed job.
	for attempt := 1; attempt <= 2; attempt++ {
		s.fetchForgeBlockTemplate()

		job := requireForgeCurrentMask(
			t,
			rt,
			forgepool.ExternalMask,
		)

		if job.ID != fullID {
			t.Fatalf(
				"both-down failure #%d replaced full job before threshold",
				attempt,
			)
		}

		if !rt.dgbHealth.required() ||
			!rt.dogeHealth.required() {
			t.Fatalf(
				"external chain excluded before third both-down failure #%d",
				attempt,
			)
		}
	}

	// Failure #3 excludes BOTH. No 0x20 fallback is allowed.
	s.fetchForgeBlockTemplate()

	if rt.currentJob() != nil {
		t.Fatal("FORGE retained current work after both chains were excluded")
	}
	if rt.currentID != "" {
		t.Fatalf(
			"FORGE pause retained currentID %q",
			rt.currentID,
		)
	}
	if len(rt.jobs) != 0 || len(rt.order) != 0 {
		t.Fatal("FORGE pause retained stale jobs")
	}
	if rt.requiredExternalMask() != 0 {
		t.Fatalf(
			"both-down required mask=%#x want internal pause mask 0",
			rt.requiredExternalMask(),
		)
	}

	// Recover DGB once. Hysteresis still requires pause.
	dgb.setHealthy(true)
	s.fetchForgeBlockTemplate()

	if rt.currentJob() != nil {
		t.Fatal("FORGE resumed after only one DGB recovery success")
	}
	if rt.dgbHealth.required() {
		t.Fatal("DGB restored after only one success from both-down state")
	}
	if rt.requiredExternalMask() != 0 {
		t.Fatalf(
			"first recovery changed required mask to %#x",
			rt.requiredExternalMask(),
		)
	}

	// Second DGB success resumes valid DGB-only 0x21.
	s.fetchForgeBlockTemplate()

	dgbOnly := requireForgeCurrentMask(
		t,
		rt,
		forgepool.ExternalDGB,
	)

	if !rt.dgbHealth.required() {
		t.Fatal("DGB not restored after second success")
	}
	if rt.dogeHealth.required() {
		t.Fatal("DOGE unexpectedly restored while still unavailable")
	}
	if !dgbOnly.Clean {
		t.Fatal("resume from FORGE pause was not clean")
	}
	if dgbOnly.Signed == nil ||
		dgbOnly.Signed.Template.Version ==
			forgepool.OGGTemplateVersionV2Base {
		t.Fatal("pause recovery produced forbidden OGG-only template")
	}
	if !dgbOnly.Digibyte.DigibyteActive ||
		dgbOnly.Digibyte.DOGE != nil {
		t.Fatal("pause recovery did not produce valid 0x21 job")
	}

	// DOGE recovery #1 retains 0x21.
	doge.setHealthy(true)
	s.fetchForgeBlockTemplate()

	stillDGBOnly := requireForgeCurrentMask(
		t,
		rt,
		forgepool.ExternalDGB,
	)

	if rt.dogeHealth.required() {
		t.Fatal("DOGE restored after only one success")
	}
	if stillDGBOnly.Clean {
		t.Fatal("first DOGE recovery unexpectedly forced clean job")
	}

	// DOGE recovery #2 restores full mode.
	s.fetchForgeBlockTemplate()

	restored := requireForgeCurrentMask(
		t,
		rt,
		forgepool.ExternalMask,
	)

	if !rt.dogeHealth.required() {
		t.Fatal("DOGE not restored after second success")
	}
	if !restored.Clean {
		t.Fatal("0x21 -> 0x23 restoration was not clean")
	}
	if restored.Signed == nil ||
		restored.Signed.Template.Version ==
			forgepool.OGGTemplateVersionV2Base {
		t.Fatal("restored work became forbidden OGG-only template")
	}
}
