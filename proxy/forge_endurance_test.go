package proxy

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/HawyUkr/open-progpow-pool/forgepool"
	"github.com/HawyUkr/open-progpow-pool/forgesigner"
	"github.com/HawyUkr/open-progpow-pool/rpc"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

type d2eSignerGate struct {
	mu      sync.RWMutex
	up      bool
	service *forgesigner.Service
}

func (g *d2eSignerGate) setUp(up bool) {
	g.mu.Lock()
	g.up = up
	g.mu.Unlock()
}

func (g *d2eSignerGate) ServeHTTP(
	w http.ResponseWriter,
	r *http.Request,
) {
	g.mu.RLock()
	up := g.up
	g.mu.RUnlock()

	if !up {
		http.Error(
			w,
			"independent signer unavailable",
			http.StatusServiceUnavailable,
		)
		return
	}

	g.service.ServeHTTP(w, r)
}

func attachGatedD2ESigner(
	t *testing.T,
	rt *forgeRuntime,
	dgbReply rpc.BlockTemplateReply,
	dogeReply rpc.BlockTemplateReply,
) (*d2dSignerSource, *d2dSignerBackend, *d2eSignerGate) {
	t.Helper()

	if dogeReply.MinTime <= 0 {
		dogeReply.MinTime = dogeReply.CurTime
	}

	source := &d2dSignerSource{
		dgb:    dgbReply,
		doge:   dogeReply,
		dgbOK:  true,
		dogeOK: true,
	}

	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}

	backend := &d2dSignerBackend{key: key}

	service, err := forgesigner.New(
		source,
		backend,
		forgesigner.Policy{
			DogePayoutScript: []byte{0x52},
			MaxValidity:      300,
			MaxAge:           300,
		},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}

	gate := &d2eSignerGate{
		up:      true,
		service: service,
	}

	server := httptest.NewServer(gate)
	t.Cleanup(server.Close)

	client, err := newForgeSigner(ForgeSigner{
		Name:    "D2E-real-signer",
		URL:     server.URL,
		Address: backend.Address().Hex(),
		Timeout: "2s",
	})
	if err != nil {
		t.Fatal(err)
	}

	rt.signers = []*forgeSignerClient{client}
	rt.authorized = []common.Address{backend.Address()}
	rt.templateQuorum = 1

	return source, backend, gate
}

func TestForgeSignerDisconnectReconnectFailClosed(t *testing.T) {
	s, rt, poolDGB, poolDOGE :=
		newForgeTransitionHarness(t)

	_, backend, gate := attachGatedD2ESigner(
		t,
		rt,
		poolDGB.reply,
		poolDOGE.reply,
	)

	// Establish real-signer-authorized 0x23.
	s.fetchForgeBlockTemplate()

	first := requireForgeCurrentMask(
		t,
		rt,
		forgepool.ExternalMask,
	)
	firstID := first.ID

	baseCalls := backend.callCount()
	if baseCalls != 1 {
		t.Fatalf(
			"initial signing key calls=%d want 1",
			baseCalls,
		)
	}

	// Signer network disappears while external chains remain healthy.
	gate.setUp(false)

	s.fetchForgeBlockTemplate()

	retained := requireForgeCurrentMask(
		t,
		rt,
		forgepool.ExternalMask,
	)

	if retained.ID != firstID {
		t.Fatal("signer outage replaced previously authorized fresh work")
	}
	if backend.callCount() != baseCalls {
		t.Fatal("disconnected signer unexpectedly reached signing key")
	}
	if rt.requiredExternalMask() != forgepool.ExternalMask {
		t.Fatal("signer outage incorrectly degraded external-chain health")
	}

	// Once old signed work expires, signer outage must leave NO mineable job.
	first.Signed.Template.Expires = 1

	if rt.currentJob() != nil {
		t.Fatal("expired signed work remained mineable")
	}

	s.fetchForgeBlockTemplate()

	if rt.currentJob() != nil {
		t.Fatal("signer outage published unsigned or stale replacement work")
	}
	if backend.callCount() != baseCalls {
		t.Fatal("disconnected signer reached key after old work expired")
	}

	// Same signer/key comes back. Fresh 0x23 work may resume.
	gate.setUp(true)

	s.fetchForgeBlockTemplate()

	restored := requireForgeCurrentMask(
		t,
		rt,
		forgepool.ExternalMask,
	)

	if restored.ID == firstID {
		t.Fatal("reconnect did not replace expired work")
	}
	if restored.Signed == nil {
		t.Fatal("reconnected signer produced unsigned work")
	}
	if !restored.Clean {
		t.Fatal("resume after stale signer outage was not a clean job")
	}
	if backend.callCount() != baseCalls+1 {
		t.Fatalf(
			"reconnect signing calls=%d want %d",
			backend.callCount(),
			baseCalls+1,
		)
	}
}

func TestForgeRepeatedHealthTransitionsEndurance(t *testing.T) {
	s, rt, poolDGB, poolDOGE :=
		newForgeTransitionHarness(t)

	source, _ := attachRealD2DSigner(
		t,
		rt,
		poolDGB.reply,
		poolDOGE.reply,
	)

	s.fetchForgeBlockTemplate()
	requireForgeCurrentMask(t, rt, forgepool.ExternalMask)

	const cycles = 5

	for cycle := 1; cycle <= cycles; cycle++ {
		// DOGE exclusion: 23 -> 21.
		poolDOGE.setHealthy(false)
		source.setHealthy(true, false)

		for i := 0; i < 3; i++ {
			s.fetchForgeBlockTemplate()
		}

		requireForgeCurrentMask(
			t,
			rt,
			forgepool.ExternalDGB,
		)

		// DOGE restore: 21 -> 23.
		poolDOGE.setHealthy(true)
		source.setHealthy(true, true)

		for i := 0; i < 2; i++ {
			s.fetchForgeBlockTemplate()
		}

		requireForgeCurrentMask(
			t,
			rt,
			forgepool.ExternalMask,
		)

		// DGB exclusion: 23 -> 22.
		poolDGB.setHealthy(false)
		source.setHealthy(false, true)

		for i := 0; i < 3; i++ {
			s.fetchForgeBlockTemplate()
		}

		requireForgeCurrentMask(
			t,
			rt,
			forgepool.ExternalDOGE,
		)

		// DGB restore: 22 -> 23.
		poolDGB.setHealthy(true)
		source.setHealthy(true, true)

		for i := 0; i < 2; i++ {
			s.fetchForgeBlockTemplate()
		}

		requireForgeCurrentMask(
			t,
			rt,
			forgepool.ExternalMask,
		)

		// Both down: full -> PAUSE.
		poolDGB.setHealthy(false)
		poolDOGE.setHealthy(false)
		source.setHealthy(false, false)

		for i := 0; i < 3; i++ {
			s.fetchForgeBlockTemplate()
		}

		if rt.currentJob() != nil {
			t.Fatalf(
				"cycle %d: both-down state failed to pause",
				cycle,
			)
		}
		if rt.requiredExternalMask() != 0 {
			t.Fatalf(
				"cycle %d: paused mask=%#x want 0",
				cycle,
				rt.requiredExternalMask(),
			)
		}

		// Both recover: two successes restore full 0x23.
		poolDGB.setHealthy(true)
		poolDOGE.setHealthy(true)
		source.setHealthy(true, true)

		for i := 0; i < 2; i++ {
			s.fetchForgeBlockTemplate()
		}

		job := requireForgeCurrentMask(
			t,
			rt,
			forgepool.ExternalMask,
		)

		if !job.Clean {
			t.Fatalf(
				"cycle %d: pause -> full recovery was not clean",
				cycle,
			)
		}
	}

	// Final invariant: no retained job may ever be V2 mask 0.
	rt.mu.RLock()
	defer rt.mu.RUnlock()

	for id, job := range rt.jobs {
		if job == nil {
			continue
		}
		if job.ExternalMask == 0 {
			t.Fatalf(
				"job %s retained forbidden OGG-only external mask",
				id,
			)
		}
	}
}

func TestForgeConcurrentRefreshRaceSafety(t *testing.T) {
	s, rt, poolDGB, poolDOGE :=
		newForgeTransitionHarness(t)

	_, _ = attachRealD2DSigner(
		t,
		rt,
		poolDGB.reply,
		poolDOGE.reply,
	)

	s.fetchForgeBlockTemplate()
	requireForgeCurrentMask(t, rt, forgepool.ExternalMask)

	const goroutines = 8
	const refreshesPerGoroutine = 10

	var wg sync.WaitGroup

	for g := 0; g < goroutines; g++ {
		wg.Add(1)

		go func() {
			defer wg.Done()

			for i := 0; i < refreshesPerGoroutine; i++ {
				s.fetchForgeBlockTemplate()
			}
		}()
	}

	wg.Wait()

	job := requireForgeCurrentMask(
		t,
		rt,
		forgepool.ExternalMask,
	)

	if job.ExternalMask == 0 {
		t.Fatal("concurrent refresh produced forbidden OGG-only work")
	}

	rt.mu.RLock()
	defer rt.mu.RUnlock()

	if len(rt.jobs) > rt.jobBacklog {
		t.Fatalf(
			"concurrent refresh exceeded job backlog: have %d max %d",
			len(rt.jobs),
			rt.jobBacklog,
		)
	}

	for id, stored := range rt.jobs {
		if stored == nil {
			continue
		}
		if stored.ExternalMask == 0 {
			t.Fatalf(
				"concurrent refresh stored forbidden mask on job %s",
				id,
			)
		}
	}
}
