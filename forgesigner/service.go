// Package forgesigner runs outside the pool. Its trust source and key backend
// are provisioned by the signer operator, never selected by a signing request.
package forgesigner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/HawyUkr/open-progpow-pool/forgepool"
	"github.com/HawyUkr/open-progpow-pool/rpc"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// Source must obtain fresh templates from independently trusted, synchronized
// full nodes on the configured networks. Implementations must bound RPC latency.
// Request data must never be used to choose endpoints or supply these templates.
type Source interface {
	Templates(context.Context) (*rpc.BlockTemplateReply, *rpc.BlockTemplateReply, error)
}

// DigibyteSource and DogecoinSource allow V2 to validate participating
// external chains independently. V1 continues using Source.Templates exactly
// as before.
type DigibyteSource interface {
	DigibyteTemplate(context.Context) (*rpc.BlockTemplateReply, error)
}

type DogecoinSource interface {
	DogecoinTemplate(context.Context) (*rpc.BlockTemplateReply, error)
}

// ProposalSource must perform independent full-body/contextual validation,
// never merely acknowledge or deserialize requester data.
type ProposalSource interface {
	ValidateDogeProposal(context.Context, []byte) error
}

// Backend can be an HSM or an isolated signer. It returns canonical secp256k1
// [R || S || recovery-ID] signatures (V=0/1). Implementations must honor context.
// No private-key representation is part of the pool/service protocol.
type Backend interface {
	Address() common.Address
	SignDigest(context.Context, common.Hash) ([]byte, error)
}

type Policy struct {
	DogePayoutScript []byte
	MaxValidity      uint64 // seconds; independently configured, not supplied by pool
	MaxAge           uint64 // maximum age of ValidFrom and proposed DOGE time, seconds
}

type Service struct {
	source  Source
	backend Backend
	policy  Policy
	clock   func() time.Time

	// Health belongs to the independent signer, not to the requesting pool.
	// This prevents a pool from choosing reduced participation merely by
	// claiming an external chain is unavailable.
	dgbHealth  signerChainHealth
	dogeHealth signerChainHealth
}

func New(source Source, backend Backend, policy Policy, clock func() time.Time) (*Service, error) {
	if source == nil || backend == nil || backend.Address() == (common.Address{}) || len(policy.DogePayoutScript) == 0 || policy.MaxValidity == 0 || policy.MaxValidity > 300 || policy.MaxAge == 0 || policy.MaxAge > 300 {
		return nil, errors.New("invalid independent signer configuration")
	}
	policy.DogePayoutScript = append([]byte(nil), policy.DogePayoutScript...)
	if clock == nil {
		clock = time.Now
	}
	return &Service{source: source, backend: backend, policy: policy, clock: clock}, nil
}

func (s *Service) checkTime(r forgepool.TemplateSignRequest) error {
	now := s.clock().Unix()
	if now < 0 {
		return errors.New("invalid signer clock")
	}

	mask, err := forgepool.TemplateExternalMask(r.Template)
	if err != nil {
		return err
	}

	t := r.Template
	if t.ValidFrom == 0 ||
		t.ValidFrom > uint64(now) ||
		uint64(now)-t.ValidFrom > s.policy.MaxAge ||
		t.Expires <= uint64(now) ||
		t.Expires <= t.ValidFrom ||
		t.Expires-t.ValidFrom > s.policy.MaxValidity {
		return errors.New("invalid, stale or expired template window")
	}

	if mask&forgepool.ExternalDOGE != 0 {
		if r.Doge == nil ||
			r.Doge.Time == 0 ||
			uint64(r.Doge.Time) > uint64(now) ||
			uint64(now)-uint64(r.Doge.Time) > s.policy.MaxAge {
			return errors.New("missing, stale or future DOGE evidence")
		}
	} else if r.Doge != nil {
		return errors.New("DOGE evidence supplied while DOGE participation is disabled")
	}

	return nil
}

func validateSignerDigibyteTemplate(reply *rpc.BlockTemplateReply) (forgepool.ParentTemplate, error) {
	if reply == nil {
		return forgepool.ParentTemplate{}, errors.New("missing trusted Digibyte template")
	}

	p, err := reply.ToParentTemplate()
	if err != nil {
		return forgepool.ParentTemplate{}, err
	}
	if _, err := forgepool.CompactTarget(p.Bits); err != nil {
		return forgepool.ParentTemplate{}, err
	}
	if p.Previous == (forgepool.Hash{}) || p.Height <= 0 {
		return forgepool.ParentTemplate{}, errors.New("invalid trusted Digibyte template")
	}

	return p, nil
}

func validateSignerDogecoinTemplate(reply *rpc.BlockTemplateReply) (forgepool.ParentTemplate, error) {
	if reply == nil {
		return forgepool.ParentTemplate{}, errors.New("missing trusted Dogecoin template")
	}

	p, err := reply.ToParentTemplate()
	if err != nil {
		return forgepool.ParentTemplate{}, err
	}
	if _, err := forgepool.CompactTarget(p.Bits); err != nil {
		return forgepool.ParentTemplate{}, err
	}
	if p.Previous == (forgepool.Hash{}) ||
		p.Height <= 0 ||
		reply.MinTime <= 0 ||
		int64(p.CurTime) < reply.MinTime ||
		len(p.WitnessCommitment) != 0 ||
		len(p.MWEB) != 0 {
		return forgepool.ParentTemplate{}, errors.New("invalid trusted Dogecoin template")
	}

	return p, nil
}

// trustedV2State always probes BOTH signer-owned external sources.
//
// The requesting pool cannot choose which health checks run. updateHealth is
// true only for the pre-signing validation pass so the post-key revalidation
// does not double-count one signing request as two hysteresis observations.
func (s *Service) trustedV2State(ctx context.Context, updateHealth bool) (
	dgb *rpc.BlockTemplateReply,
	dgbParent forgepool.ParentTemplate,
	dgbOK bool,
	doge *rpc.BlockTemplateReply,
	dogeParent forgepool.ParentTemplate,
	dogeOK bool,
	requiredMask uint8,
	err error,
) {
	dgbSource, ok := s.source.(DigibyteSource)
	if !ok {
		err = errors.New("trusted source cannot independently fetch Digibyte")
		return
	}
	dogeSource, ok := s.source.(DogecoinSource)
	if !ok {
		err = errors.New("trusted source cannot independently fetch Dogecoin")
		return
	}

	if err = ctx.Err(); err != nil {
		return
	}

	var dgbErr error
	dgb, dgbErr = dgbSource.DigibyteTemplate(ctx)

	if err = ctx.Err(); err != nil {
		return
	}

	var dogeErr error
	doge, dogeErr = dogeSource.DogecoinTemplate(ctx)

	if err = ctx.Err(); err != nil {
		return
	}

	if dgbErr == nil {
		dgbParent, dgbErr = validateSignerDigibyteTemplate(dgb)
	}
	dgbOK = dgbErr == nil

	if dogeErr == nil {
		dogeParent, dogeErr = validateSignerDogecoinTemplate(doge)
	}
	dogeOK = dogeErr == nil

	if updateHealth {
		s.dgbHealth.observe(dgbOK)
		s.dogeHealth.observe(dogeOK)
	}

	if s.dgbHealth.required() {
		requiredMask |= forgepool.ExternalDGB
	}
	if s.dogeHealth.required() {
		requiredMask |= forgepool.ExternalDOGE
	}

	return
}

func (s *Service) validate(ctx context.Context, r forgepool.TemplateSignRequest, updateHealth bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.checkTime(r); err != nil {
		return err
	}

	mask, err := forgepool.TemplateExternalMask(r.Template)
	if err != nil {
		return err
	}

	// V1 remains exactly all-or-nothing DGB+DOGE.
	if r.Template.Version == forgepool.OGGTemplateVersion {
		if r.Doge == nil {
			return errors.New("missing DOGE evidence")
		}
		if r.Doge.Candidate != nil {
			if err := forgepool.ValidateDogeProposalSize(*r.Doge.Candidate); err != nil {
				return err
			}
		}

		dgb, doge, err := s.source.Templates(ctx)
		if err != nil {
			return fmt.Errorf("trusted templates unavailable: %w", err)
		}
		if dgb == nil || doge == nil {
			return errors.New("missing trusted templates")
		}

		lp, err := dgb.ToParentTemplate()
		if err != nil {
			return err
		}
		dp, err := doge.ToParentTemplate()
		if err != nil {
			return err
		}

		if _, err = forgepool.CompactTarget(lp.Bits); err != nil {
			return err
		}
		if _, err = forgepool.CompactTarget(dp.Bits); err != nil {
			return err
		}

		if lp.Previous == (forgepool.Hash{}) ||
			dp.Previous == (forgepool.Hash{}) ||
			lp.Height <= 0 ||
			dp.Height <= 0 ||
			doge.MinTime <= 0 ||
			uint64(r.Doge.Time) < uint64(doge.MinTime) ||
			len(dp.WitnessCommitment) != 0 ||
			len(dp.MWEB) != 0 {
			return errors.New("invalid trusted template or DOGE time below mintime")
		}

		if common.HexToHash(forgepool.DisplayHash(lp.Previous)) != r.Template.DGBPrevHash ||
			lp.Bits != r.Template.DGBBits {
			return errors.New("DGB tip or difficulty differs from trusted template")
		}

		return s.validateDoge(ctx, r, doge, dp)
	}

	// V2 participation is signer-controlled.
	//
	// The signer checks BOTH chains regardless of the requested mask and then
	// computes the only mask it is prepared to authorize.
	dgb, lp, dgbOK, doge, dp, dogeOK, requiredMask, err :=
		s.trustedV2State(ctx, updateHealth)
	if err != nil {
		return err
	}

	if mask != requiredMask {
		return fmt.Errorf(
			"FORGE V2 participation mask %#x rejected; independent signer requires %#x",
			mask,
			requiredMask,
		)
	}

	if mask&forgepool.ExternalDGB != 0 {
		if !dgbOK || dgb == nil {
			return errors.New("required Digibyte chain is unavailable")
		}

		if common.HexToHash(forgepool.DisplayHash(lp.Previous)) != r.Template.DGBPrevHash ||
			lp.Bits != r.Template.DGBBits {
			return errors.New("DGB tip or difficulty differs from trusted template")
		}
	}

	if mask&forgepool.ExternalDOGE != 0 {
		if !dogeOK || doge == nil {
			return errors.New("required Dogecoin chain is unavailable")
		}

		if r.Doge == nil ||
			doge.MinTime <= 0 ||
			uint64(r.Doge.Time) < uint64(doge.MinTime) {
			return errors.New("invalid DOGE signing evidence")
		}

		if err := s.validateDoge(ctx, r, doge, dp); err != nil {
			return err
		}
	}

	return s.checkTime(r)
}

func (s *Service) validateDoge(ctx context.Context, r forgepool.TemplateSignRequest, doge *rpc.BlockTemplateReply, dp forgepool.ParentTemplate) error {
	if r.Doge == nil {
		return errors.New("missing DOGE evidence")
	}

	if r.Doge.Candidate != nil {
		if err := forgepool.ValidateDogeProposalSize(*r.Doge.Candidate); err != nil {
			return err
		}

		validator, ok := s.source.(ProposalSource)
		if !ok {
			return errors.New("trusted source cannot validate DOGE proposals")
		}

		proposed := *r.Doge.Candidate
		if proposed.CoinbaseValue <= 0 {
			return errors.New("DOGE proposal must have a positive protocol payout")
		}
		if proposed.Previous != dp.Previous ||
			proposed.Height != dp.Height ||
			proposed.CurTime != r.Doge.Time {
			return errors.New("DOGE proposal does not match trusted tip/height or evidence time")
		}
		if _, err := forgepool.CompactTarget(proposed.Bits); err != nil {
			return err
		}

		candidate, err := forgepool.BuildDogeCandidate(
			proposed,
			s.policy.DogePayoutScript,
			r.Doge.JobNonce,
		)
		if err != nil {
			return err
		}
		if common.HexToHash(candidate.DisplayHash) != r.Template.DogeAuxHash {
			return errors.New("DOGE proposal hash or payout policy mismatch")
		}

		block, err := candidate.ValidationBlock()
		if err != nil {
			return err
		}
		if err := validator.ValidateDogeProposal(ctx, block); err != nil {
			return fmt.Errorf("independent DOGE proposal validation failed: %w", err)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		return s.checkTime(r)
	}

	// Exact trusted-template mode.
	if dp.CurTime != r.Doge.Time {
		return errors.New("DOGE time differs from trusted template")
	}

	candidate, err := forgepool.BuildDogeCandidate(
		dp,
		s.policy.DogePayoutScript,
		r.Doge.JobNonce,
	)
	if err != nil {
		return err
	}
	if common.HexToHash(candidate.DisplayHash) != r.Template.DogeAuxHash {
		return errors.New("DOGE candidate differs from independent reconstruction")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.checkTime(r)
}

func (s *Service) Sign(ctx context.Context, r forgepool.TemplateSignRequest) (*forgepool.TemplateSignResponse, error) {
	var workHash common.Hash

	if r.Template.Version&^forgepool.ExternalMask == forgepool.OGGTemplateVersionV2Base {
		if r.WorkHash == nil || *r.WorkHash == (common.Hash{}) {
			return nil, errors.New("V2 signing request missing exact OGG WorkHash")
		}
		workHash = *r.WorkHash
	} else if r.WorkHash != nil {
		return nil, errors.New("V1 signing request must not carry V2 WorkHash")
	}

	digest, err := forgepool.TemplateSigningDigest(r.Template, workHash)
	if err != nil {
		return nil, err
	}
	if r.Domain != forgepool.TemplateDomain(r.Template) || r.Digest != digest.Hex() {
		return nil, errors.New("wrong template domain or digest")
	}
	if err := s.validate(ctx, r, true); err != nil {
		return nil, err
	}
	sig, err := s.backend.SignDigest(ctx, digest)
	if err != nil {
		return nil, err
	}
	if len(sig) != 65 || sig[64] > 1 {
		return nil, errors.New("invalid backend signature")
	}
	pub, err := crypto.SigToPub(digest.Bytes(), sig)
	if err != nil || crypto.PubkeyToAddress(*pub) != s.backend.Address() {
		return nil, errors.New("backend signer mismatch")
	}
	// Revalidate after key-service latency, including the independent proposal
	// check when supplied. Template-body changes alone need not invalidate a
	// proposal. A tip change after the final check remains possible.
	if err := s.validate(ctx, r, false); err != nil {
		return nil, err
	}
	return &forgepool.TemplateSignResponse{Signer: s.backend.Address().Hex(), Signature: fmt.Sprintf("0x%x", sig)}, nil
}

// ServeHTTP is intended behind the signer's authenticated transport. It has a
// bounded strict proposal schema; it never accepts RPC URLs or private keys.
func (s *Service) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, forgepool.MaxSignRequestBytes)
	defer r.Body.Close()
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var proposal forgepool.TemplateSignRequest
	if err := decoder.Decode(&proposal); err != nil {
		http.Error(w, "invalid proposal", http.StatusBadRequest)
		return
	}
	var trailing interface{}
	if err := decoder.Decode(&trailing); err != io.EOF {
		http.Error(w, "invalid trailing data", http.StatusBadRequest)
		return
	}
	response, err := s.Sign(r.Context(), proposal)
	if err != nil {
		http.Error(w, "proposal rejected", http.StatusUnprocessableEntity)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}
