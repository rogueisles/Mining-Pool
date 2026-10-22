package proxy

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/HawyUkr/open-progpow-pool/forgepool"
	"github.com/HawyUkr/open-progpow-pool/forgesigner"
	"github.com/HawyUkr/open-progpow-pool/rpc"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

type d2dSignerSource struct {
	mu sync.RWMutex

	dgb  rpc.BlockTemplateReply
	doge rpc.BlockTemplateReply

	dgbOK  bool
	dogeOK bool

	dgbReads  int32
	dogeReads int32
}

func (s *d2dSignerSource) setHealthy(dgbOK, dogeOK bool) {
	s.mu.Lock()
	s.dgbOK = dgbOK
	s.dogeOK = dogeOK
	s.mu.Unlock()
}

func (s *d2dSignerSource) DigibyteTemplate(
	ctx context.Context,
) (*rpc.BlockTemplateReply, error) {
	atomic.AddInt32(&s.dgbReads, 1)

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	s.mu.RLock()
	ok := s.dgbOK
	reply := s.dgb
	s.mu.RUnlock()

	if !ok {
		return nil, errors.New("independent signer Digibyte unavailable")
	}

	return &reply, nil
}

func (s *d2dSignerSource) DogecoinTemplate(
	ctx context.Context,
) (*rpc.BlockTemplateReply, error) {
	atomic.AddInt32(&s.dogeReads, 1)

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	s.mu.RLock()
	ok := s.dogeOK
	reply := s.doge
	s.mu.RUnlock()

	if !ok {
		return nil, errors.New("independent signer Dogecoin unavailable")
	}

	return &reply, nil
}

func (s *d2dSignerSource) Templates(
	ctx context.Context,
) (*rpc.BlockTemplateReply, *rpc.BlockTemplateReply, error) {
	dgb, err := s.DigibyteTemplate(ctx)
	if err != nil {
		return nil, nil, err
	}

	doge, err := s.DogecoinTemplate(ctx)
	if err != nil {
		return nil, nil, err
	}

	return dgb, doge, nil
}

func (s *d2dSignerSource) ValidateDogeProposal(
	ctx context.Context,
	block []byte,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	s.mu.RLock()
	ok := s.dogeOK
	s.mu.RUnlock()

	if !ok {
		return errors.New("independent signer Dogecoin unavailable")
	}
	if len(block) == 0 {
		return errors.New("empty DOGE proposal")
	}

	return nil
}

type d2dSignerBackend struct {
	key   *ecdsa.PrivateKey
	calls int32
}

func (b *d2dSignerBackend) Address() common.Address {
	return crypto.PubkeyToAddress(b.key.PublicKey)
}

func (b *d2dSignerBackend) SignDigest(
	ctx context.Context,
	digest common.Hash,
) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	atomic.AddInt32(&b.calls, 1)
	return crypto.Sign(digest.Bytes(), b.key)
}

func (b *d2dSignerBackend) callCount() int32 {
	return atomic.LoadInt32(&b.calls)
}

func attachRealD2DSigner(
	t *testing.T,
	rt *forgeRuntime,
	dgbReply rpc.BlockTemplateReply,
	dogeReply rpc.BlockTemplateReply,
) (*d2dSignerSource, *d2dSignerBackend) {
	t.Helper()

	// DOGE trusted-template validation requires a positive mintime.
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

	server := httptest.NewServer(service)
	t.Cleanup(server.Close)

	client, err := newForgeSigner(ForgeSigner{
		Name:    "D2D-real-signer",
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

	return source, backend
}

func TestForgeRealSignerSingleChainConvergence(t *testing.T) {
	tests := []struct {
		name     string
		failDGB  bool
		wantMask uint8
	}{
		{
			name:     "DOGE-down-DGB-only",
			failDGB:  false,
			wantMask: forgepool.ExternalDGB,
		},
		{
			name:     "DGB-down-DOGE-only",
			failDGB:  true,
			wantMask: forgepool.ExternalDOGE,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, rt, poolDGB, poolDOGE :=
				newForgeTransitionHarness(t)

			source, backend := attachRealD2DSigner(
				t,
				rt,
				poolDGB.reply,
				poolDOGE.reply,
			)

			// Healthy start must be signed as full 0x23.
			s.fetchForgeBlockTemplate()

			requireForgeCurrentMask(
				t,
				rt,
				forgepool.ExternalMask,
			)

			baseCalls := backend.callCount()
			if baseCalls != 1 {
				t.Fatalf(
					"healthy full mode used signing key %d times; want 1",
					baseCalls,
				)
			}

			if tc.failDGB {
				poolDGB.setHealthy(false)
				source.setHealthy(false, true)
			} else {
				poolDOGE.setHealthy(false)
				source.setHealthy(true, false)
			}

			// Failure #1/#2:
			// pool probes reduced mode, but the real signer must reject it
			// before using its key because signer health has not crossed 3.
			for attempt := 1; attempt <= 2; attempt++ {
				s.fetchForgeBlockTemplate()

				requireForgeCurrentMask(
					t,
					rt,
					forgepool.ExternalMask,
				)

				if backend.callCount() != baseCalls {
					t.Fatalf(
						"premature reduced mode reached signing key on failure #%d",
						attempt,
					)
				}
			}

			// Failure #3:
			// both independent state machines now converge.
			s.fetchForgeBlockTemplate()

			reduced := requireForgeCurrentMask(
				t,
				rt,
				tc.wantMask,
			)

			if !reduced.Clean {
				t.Fatal("reduced-mode convergence was not clean")
			}
			if backend.callCount() != baseCalls+1 {
				t.Fatalf(
					"third-failure convergence key calls=%d want %d",
					backend.callCount(),
					baseCalls+1,
				)
			}

			// Restore failed chain.
			poolDGB.setHealthy(true)
			poolDOGE.setHealthy(true)
			source.setHealthy(true, true)

			// Recovery #1 retains reduced mode; signer still agrees.
			s.fetchForgeBlockTemplate()

			firstRecovery := requireForgeCurrentMask(
				t,
				rt,
				tc.wantMask,
			)

			if firstRecovery.Clean {
				t.Fatal("first recovery success unexpectedly changed mode")
			}
			if backend.callCount() != baseCalls+2 {
				t.Fatal("first recovery did not sign retained reduced mode")
			}

			// Recovery #2 restores full mode in both state machines.
			s.fetchForgeBlockTemplate()

			full := requireForgeCurrentMask(
				t,
				rt,
				forgepool.ExternalMask,
			)

			if !full.Clean {
				t.Fatal("second recovery did not cleanly restore 0x23")
			}
			if backend.callCount() != baseCalls+3 {
				t.Fatal("restored full mode did not reach signing key exactly once")
			}

			if atomic.LoadInt32(&source.dgbReads) == 0 ||
				atomic.LoadInt32(&source.dogeReads) == 0 {
				t.Fatal("real signer did not independently inspect both chains")
			}
		})
	}
}

func TestForgeRealSignerBothDownPauseAndRecovery(t *testing.T) {
	s, rt, poolDGB, poolDOGE :=
		newForgeTransitionHarness(t)

	source, backend := attachRealD2DSigner(
		t,
		rt,
		poolDGB.reply,
		poolDOGE.reply,
	)

	// Healthy 0x23.
	s.fetchForgeBlockTemplate()
	requireForgeCurrentMask(t, rt, forgepool.ExternalMask)

	baseCalls := backend.callCount()
	if baseCalls != 1 {
		t.Fatalf("healthy signing calls=%d want 1", baseCalls)
	}

	// Independently take both pool-observed and signer-observed chains down.
	poolDGB.setHealthy(false)
	poolDOGE.setHealthy(false)
	source.setHealthy(false, false)

	// Failure #1/#2 retain old full work.
	// Crucially, the real signer must reject all-unavailable probes BEFORE key use.
	for attempt := 1; attempt <= 2; attempt++ {
		s.fetchForgeBlockTemplate()

		requireForgeCurrentMask(
			t,
			rt,
			forgepool.ExternalMask,
		)

		if backend.callCount() != baseCalls {
			t.Fatalf(
				"both-down probe reached signing key on failure #%d",
				attempt,
			)
		}
	}

	// Failure #3 excludes both in both independent health machines.
	// The probe is still forbidden and must not reach the key.
	s.fetchForgeBlockTemplate()

	if rt.currentJob() != nil {
		t.Fatal("both-down threshold did not pause FORGE")
	}
	if rt.requiredExternalMask() != 0 {
		t.Fatalf(
			"both-down internal mask=%#x want 0",
			rt.requiredExternalMask(),
		)
	}
	if backend.callCount() != baseCalls {
		t.Fatal("both-down threshold probe reached signing key")
	}

	// DGB recovery #1: pool and signer both still require PAUSE.
	poolDGB.setHealthy(true)
	source.setHealthy(true, false)

	s.fetchForgeBlockTemplate()

	if rt.currentJob() != nil {
		t.Fatal("FORGE resumed after only one DGB success")
	}
	if backend.callCount() != baseCalls {
		t.Fatal("first DGB recovery probe reached signing key")
	}

	// DGB recovery #2: both independently authorize 0x21.
	s.fetchForgeBlockTemplate()

	dgbOnly := requireForgeCurrentMask(
		t,
		rt,
		forgepool.ExternalDGB,
	)

	if !dgbOnly.Clean {
		t.Fatal("pause -> 0x21 resume was not clean")
	}
	if backend.callCount() != baseCalls+1 {
		t.Fatal("valid 0x21 recovery was not signed exactly once")
	}

	// DOGE recovery #1 retains 0x21.
	poolDOGE.setHealthy(true)
	source.setHealthy(true, true)

	s.fetchForgeBlockTemplate()

	remainDGB := requireForgeCurrentMask(
		t,
		rt,
		forgepool.ExternalDGB,
	)

	if remainDGB.Clean {
		t.Fatal("first DOGE recovery unexpectedly restored full mode")
	}
	if backend.callCount() != baseCalls+2 {
		t.Fatal("retained 0x21 recovery job was not signed")
	}

	// DOGE recovery #2 -> clean 0x23.
	s.fetchForgeBlockTemplate()

	full := requireForgeCurrentMask(
		t,
		rt,
		forgepool.ExternalMask,
	)

	if !full.Clean {
		t.Fatal("0x21 -> 0x23 restoration was not clean")
	}
	if backend.callCount() != baseCalls+3 {
		t.Fatal("restored 0x23 was not signed exactly once")
	}

	if atomic.LoadInt32(&source.dgbReads) == 0 ||
		atomic.LoadInt32(&source.dogeReads) == 0 {
		t.Fatal("real signer did not independently inspect both chains")
	}
}
