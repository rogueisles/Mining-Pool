package forgesigner

import (
	"context"
	"errors"
	"testing"

	"github.com/HawyUkr/open-progpow-pool/forgepool"
	"github.com/HawyUkr/open-progpow-pool/rpc"
	"github.com/ethereum/go-ethereum/common"
)

type v2IndependentSource struct {
	*testSource

	dgbReads  int
	dogeReads int

	dgbErr  error
	dogeErr error
}

func (s *v2IndependentSource) DigibyteTemplate(ctx context.Context) (*rpc.BlockTemplateReply, error) {
	s.dgbReads++
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.dgbErr != nil {
		return nil, s.dgbErr
	}
	return s.dgb, nil
}

func (s *v2IndependentSource) DogecoinTemplate(ctx context.Context) (*rpc.BlockTemplateReply, error) {
	s.dogeReads++
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.dogeErr != nil {
		return nil, s.dogeErr
	}
	return s.doge, nil
}

func (s *v2IndependentSource) ValidateDogeProposal(ctx context.Context, block []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(block) == 0 {
		return errors.New("empty DOGE proposal")
	}
	return nil
}

func makeV2SignerRequest(t *testing.T, src *testSource, mask uint8, now uint64) forgepool.TemplateSignRequest {
	t.Helper()

	ext := forgepool.ExternalTemplate{
		Version:   forgepool.OGGTemplateVersionV2Base | mask,
		ValidFrom: now,
		Expires:   now + 60,
	}

	var evidence *forgepool.DogeSigningEvidence

	if mask&forgepool.ExternalDGB != 0 {
		ext.DGBPrevHash = common.HexToHash(src.dgb.PreviousBlockHash)
		ext.DGBBits = 0x1d00ffff
	}

	if mask&forgepool.ExternalDOGE != 0 {
		dp, err := src.doge.ToParentTemplate()
		if err != nil {
			t.Fatal(err)
		}

		candidate, err := forgepool.BuildDogeCandidate(
			dp,
			[]byte{0x52},
			77,
		)
		if err != nil {
			t.Fatal(err)
		}

		ext.DogeAuxHash = common.HexToHash(candidate.DisplayHash)

		proposed := dp
		evidence = &forgepool.DogeSigningEvidence{
			JobNonce:  77,
			Time:      dp.CurTime,
			Candidate: &proposed,
		}
	}

	workHash := common.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111")

	req := forgepool.TemplateSignRequest{
		Domain:   forgepool.TemplateDomain(ext),
		Template: ext,
		WorkHash: &workHash,
		Doge:     evidence,
	}

	digest, err := forgepool.TemplateSigningDigest(ext, workHash)
	if err != nil {
		t.Fatal(err)
	}
	req.Digest = digest.Hex()
	return req
}

func setupV2Signer(t *testing.T) (*Service, *testSource, *v2IndependentSource, *devBackend, *int64) {
	t.Helper()

	service, src, backend, _, now := fixture(t)

	trusted := &v2IndependentSource{
		testSource: src,
	}
	service.source = trusted

	return service, src, trusted, backend, now
}

func TestV2SignerRequiresFullMaskWhenBothHealthy(t *testing.T) {
	service, src, trusted, backend, now := setupV2Signer(t)

	req := makeV2SignerRequest(
		t,
		src,
		forgepool.ExternalDGB|forgepool.ExternalDOGE,
		uint64(*now),
	)

	out, err := service.Sign(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if out == nil || backend.calls != 1 {
		t.Fatal("full healthy mask was not signed")
	}

	if trusted.dgbReads != 2 || trusted.dogeReads != 2 {
		t.Fatalf(
			"signer did not independently recheck both chains: DGB=%d DOGE=%d",
			trusted.dgbReads,
			trusted.dogeReads,
		)
	}
}

func TestV2SignerRejectsVoluntaryReducedMask(t *testing.T) {
	for _, mask := range []uint8{
		forgepool.ExternalDGB,
		forgepool.ExternalDOGE,
	} {
		t.Run(string(rune('0'+mask)), func(t *testing.T) {
			service, src, _, backend, now := setupV2Signer(t)

			req := makeV2SignerRequest(t, src, mask, uint64(*now))

			if out, err := service.Sign(context.Background(), req); err == nil || out != nil {
				t.Fatal("voluntary reduced mask accepted while both chains healthy")
			}
			if backend.calls != 0 {
				t.Fatal("unauthorized reduced mask reached signing key")
			}
		})
	}
}

func TestV2SignerAllowsDGBOnlyAfterThreeDOGEFailures(t *testing.T) {
	service, src, trusted, backend, now := setupV2Signer(t)

	trusted.dogeErr = errors.New("DOGE offline")

	req := makeV2SignerRequest(
		t,
		src,
		forgepool.ExternalDGB,
		uint64(*now),
	)

	for attempt := 1; attempt <= 2; attempt++ {
		if out, err := service.Sign(context.Background(), req); err == nil || out != nil {
			t.Fatalf("DOGE excluded after only %d failures", attempt)
		}
		if backend.calls != 0 {
			t.Fatal("premature reduced mask reached signing key")
		}
	}

	out, err := service.Sign(context.Background(), req)
	if err != nil {
		t.Fatalf("third independent DOGE failure did not permit DGB-only mode: %v", err)
	}
	if out == nil || backend.calls != 1 {
		t.Fatal("DGB-only degraded mode was not signed")
	}
}

func TestV2SignerAllowsDOGEOnlyAfterThreeDGBFailures(t *testing.T) {
	service, src, trusted, backend, now := setupV2Signer(t)

	trusted.dgbErr = errors.New("DGB offline")

	req := makeV2SignerRequest(
		t,
		src,
		forgepool.ExternalDOGE,
		uint64(*now),
	)

	for attempt := 1; attempt <= 2; attempt++ {
		if out, err := service.Sign(context.Background(), req); err == nil || out != nil {
			t.Fatalf("DGB excluded after only %d failures", attempt)
		}
		if backend.calls != 0 {
			t.Fatal("premature reduced mask reached signing key")
		}
	}

	out, err := service.Sign(context.Background(), req)
	if err != nil {
		t.Fatalf("third independent DGB failure did not permit DOGE-only mode: %v", err)
	}
	if out == nil || backend.calls != 1 {
		t.Fatal("DOGE-only degraded mode was not signed")
	}
}

func TestV2SignerRejectsOGGOnlyEvenWhenBothChainsFail(t *testing.T) {
	service, src, trusted, backend, now := setupV2Signer(t)

	trusted.dgbErr = errors.New("DGB offline")
	trusted.dogeErr = errors.New("DOGE offline")

	// Drive both independent signer health states across the failure threshold
	// using a syntactically valid request. No signature may be produced.
	full := makeV2SignerRequest(
		t,
		src,
		forgepool.ExternalDGB|forgepool.ExternalDOGE,
		uint64(*now),
	)

	for attempt := 1; attempt <= 3; attempt++ {
		if out, err := service.Sign(context.Background(), full); err == nil || out != nil {
			t.Fatalf("both-down request unexpectedly signed on attempt %d", attempt)
		}
	}

	if backend.calls != 0 {
		t.Fatal("both-down state reached signing key")
	}

	// Even after both chains cross the independent failure threshold, 0x20 is
	// forbidden. Both external chains down means FORGE pauses.
	workHash := common.HexToHash(
		"0x1111111111111111111111111111111111111111111111111111111111111111",
	)

	oggOnly := forgepool.TemplateSignRequest{
		Domain: forgepool.TemplateDomain(forgepool.ExternalTemplate{
			Version: forgepool.OGGTemplateVersionV2Base,
		}),
		Template: forgepool.ExternalTemplate{
			Version:   forgepool.OGGTemplateVersionV2Base,
			ValidFrom: uint64(*now),
			Expires:   uint64(*now) + 60,
		},
		WorkHash: &workHash,
	}

	if out, err := service.Sign(context.Background(), oggOnly); err == nil || out != nil {
		t.Fatal("OGG-only authorization accepted while both external chains down")
	}
	if backend.calls != 0 {
		t.Fatal("OGG-only request reached signing key")
	}
}

func TestV2SignerRestoresDOGEAfterTwoSuccesses(t *testing.T) {
	service, src, trusted, backend, now := setupV2Signer(t)

	trusted.dogeErr = errors.New("DOGE offline")

	reduced := makeV2SignerRequest(
		t,
		src,
		forgepool.ExternalDGB,
		uint64(*now),
	)

	// Establish signer-owned DOGE exclusion.
	for attempt := 1; attempt <= 3; attempt++ {
		out, err := service.Sign(context.Background(), reduced)
		if attempt < 3 {
			if err == nil || out != nil {
				t.Fatal("DOGE excluded too early")
			}
		} else if err != nil || out == nil {
			t.Fatalf("failed to enter degraded mode: %v", err)
		}
	}

	trusted.dogeErr = nil

	// First signer observation: still degraded by hysteresis.
	if out, err := service.Sign(context.Background(), reduced); err != nil || out == nil {
		t.Fatalf("first DOGE recovery observation should retain degraded mode: %v", err)
	}

	callsBefore := backend.calls

	// Second observation makes DOGE mandatory again, so the old reduced request
	// must be rejected before using the key.
	if out, err := service.Sign(context.Background(), reduced); err == nil || out != nil {
		t.Fatal("reduced mode remained authorized after DOGE recovery threshold")
	}
	if backend.calls != callsBefore {
		t.Fatal("obsolete reduced mask reached signing key after recovery")
	}

	full := makeV2SignerRequest(
		t,
		src,
		forgepool.ExternalDGB|forgepool.ExternalDOGE,
		uint64(*now),
	)

	if out, err := service.Sign(context.Background(), full); err != nil || out == nil {
		t.Fatalf("full mode not restored after DOGE recovery: %v", err)
	}
}

func TestV2SignerBothDownOneChainProbeNeverSigns(t *testing.T) {
	service, src, trusted, backend, now := setupV2Signer(t)

	trusted.dgbErr = errors.New("DGB offline")
	trusted.dogeErr = errors.New("DOGE offline")

	probe := makeV2SignerRequest(
		t,
		src,
		forgepool.ExternalDGB,
		uint64(*now),
	)

	for attempt := 1; attempt <= signerDisableFailures; attempt++ {
		if out, err := service.Sign(context.Background(), probe); err == nil || out != nil {
			t.Fatalf(
				"both-down health probe unexpectedly signed on attempt %d",
				attempt,
			)
		}
	}

	if backend.calls != 0 {
		t.Fatalf(
			"both-down health probe reached signing key %d times",
			backend.calls,
		)
	}

	if service.dgbHealth.required() || service.dogeHealth.required() {
		t.Fatal("both signer health states did not reach excluded state")
	}
}

func TestV2SignerRestartStartsFailClosed(t *testing.T) {
	// First signer instance reaches legitimate DOGE exclusion.
	service, src, trusted, backend, now := setupV2Signer(t)

	trusted.dogeErr = errors.New("DOGE offline")

	reduced := makeV2SignerRequest(
		t,
		src,
		forgepool.ExternalDGB,
		uint64(*now),
	)

	for attempt := 1; attempt <= signerDisableFailures; attempt++ {
		out, err := service.Sign(context.Background(), reduced)

		if attempt < signerDisableFailures {
			if err == nil || out != nil {
				t.Fatalf(
					"original signer excluded DOGE after only %d failures",
					attempt,
				)
			}
			continue
		}

		if err != nil || out == nil {
			t.Fatalf(
				"original signer failed to enter legitimate degraded mode: %v",
				err,
			)
		}
	}

	if backend.calls != 1 {
		t.Fatalf(
			"original degraded authorization key calls=%d want 1",
			backend.calls,
		)
	}

	// Simulate signer process restart while DOGE remains down.
	//
	// Health is intentionally not persistent authorization state. A fresh
	// signer must return to fail-closed full participation and gather three
	// NEW independent failure observations before allowing DGB-only again.
	restarted, src2, trusted2, backend2, now2 := setupV2Signer(t)

	trusted2.dogeErr = errors.New("DOGE still offline after restart")

	reducedAfterRestart := makeV2SignerRequest(
		t,
		src2,
		forgepool.ExternalDGB,
		uint64(*now2),
	)

	out, err := restarted.Sign(
		context.Background(),
		reducedAfterRestart,
	)
	if err == nil || out != nil {
		t.Fatal("fresh signer inherited degraded authorization after restart")
	}
	if backend2.calls != 0 {
		t.Fatal("post-restart premature reduced request reached signing key")
	}

	// Two additional failures complete three NEW observations since restart.
	for attempt := 2; attempt <= signerDisableFailures; attempt++ {
		out, err = restarted.Sign(
			context.Background(),
			reducedAfterRestart,
		)

		if attempt < signerDisableFailures {
			if err == nil || out != nil {
				t.Fatalf(
					"restarted signer excluded DOGE after only %d new failures",
					attempt,
				)
			}
			continue
		}

		if err != nil || out == nil {
			t.Fatalf(
				"restarted signer did not authorize after three new failures: %v",
				err,
			)
		}
	}

	if backend2.calls != 1 {
		t.Fatalf(
			"restarted signer key calls=%d want exactly 1",
			backend2.calls,
		)
	}
}
