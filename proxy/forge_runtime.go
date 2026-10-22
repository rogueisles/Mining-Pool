package proxy

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"math/big"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/HawyUkr/open-progpow-pool/forgepool"
	"github.com/HawyUkr/open-progpow-pool/rpc"
	"github.com/HawyUkr/open-progpow-pool/storage"
	"github.com/ethereum/go-ethereum/common"
)

var (
	errForgeStale         = errors.New("stale or unknown FORGE job")
	errForgeDuplicate     = errors.New("duplicate FORGE share")
	errForgeLowDifficulty = errors.New("low-difficulty FORGE share")
	errForgeMalformed     = errors.New("malformed FORGE share")
)

type forgeHitSubmitter interface {
	SubmitDigibyte(job *forgeJob, sol *forgeSolution) error
	SubmitDogecoin(job *forgeJob, sol *forgeSolution) error
	// Return the hash of the exact acknowledged OGGF envelope, never a nonce.
	SubmitOGG(job *forgeJob, sol *forgeSolution) (string, error)
}

type forgeJob struct {
	ProtocolV3   bool
	AuxSymbols   map[string]uint32
	ID           string
	Created      time.Time
	Clean        bool
	Height       uint64
	Difficulty   int64
	OGG          *rpc.OGGAuxBlock
	Digibyte     *forgepool.DigibyteJob
	ExternalMask uint8 // zero = legacy V1; V2 uses forgepool.ExternalDGB/DOGE bits
	Signed       *forgeSignedTemplate
	OGGRPC       *rpc.RPCClient // Issued-work caches belong to this specific node.
}

type forgeSolution struct {
	ShareDifficulty int64
	ExtraNonce1     []byte
	ExtraNonce2     []byte
	NTime           uint32
	Nonce           uint32
	Header          []byte
	Hash            forgepool.Hash
	HashValue       *big.Int
}

type forgeHits struct {
	Pepecoin  bool
	Dingocoin bool
	Bellscoin bool
	Digibyte  bool
	Dogecoin  bool
	OGG       bool
}

type forgeRuntime struct {
	allowedExternalMask uint8
	multi               bool
	multiHealth         map[uint8]*forgeChainHealth
	additionalAux       []forgeAuxRuntime
	mu                  sync.RWMutex
	refreshRequests     chan struct{} // Coalesced post-submission refresh signal.
	refreshMu           sync.Mutex    // A slow signer must not publish an older refresh last.

	jobs       map[string]*forgeJob
	order      []string
	currentID  string
	duplicates map[string]struct{}

	dgbRPC  *rpc.RPCClient
	dogeRPC *rpc.RPCClient

	// Pool-side health is independent from signer-side health.
	// Both start fail-closed with DGB+DOGE required.
	dgbHealth  forgeChainHealth
	dogeHealth forgeChainHealth

	dgbPayout  []byte
	dogePayout []byte

	extraNonce1Size    int
	extraNonce2Size    int
	jobBacklog         int
	retainExternalWork bool // Opt-in: keep external-compatible V2 work on OGG-only changes.
	maxNTimeDrift      time.Duration
	shareTarget        *big.Int
	signers            []*forgeSignerClient
	authorized         []common.Address
	templateQuorum     uint64
	templateValidity   time.Duration
	clock              func() time.Time

	extraNonceSeq uint64
	jobSeq        uint64

	account   *forgeAccountingQueue
	submitter forgeHitSubmitter
}

func (rt *forgeRuntime) requiredExternalMask() uint8 {
	var mask uint8

	if rt.dgbHealth.required() {
		mask |= forgepool.ExternalDGB
	}
	if rt.dogeHealth.required() {
		mask |= forgepool.ExternalDOGE
	}

	return mask
}

func forgeAvailableExternalMask(dgbOK, dogeOK bool) uint8 {
	var mask uint8

	if dgbOK {
		mask |= forgepool.ExternalDGB
	}
	if dogeOK {
		mask |= forgepool.ExternalDOGE
	}

	return mask
}

// forgeRefreshDecision separates pool health policy from RPC mechanics.
//
// publishMask != 0 means construct/sign/publish that mode.
// probeMask != 0 means ask the signer about that tentative mode but NEVER
// publish it.
// pause means all previously issued FORGE work must be cleared.
//
// If requiredMask is nonzero but unavailable, the existing fresh signed job may
// remain until the hysteresis threshold is reached.
func forgeRefreshDecision(requiredMask, availableMask uint8) (
	publishMask uint8,
	probeMask uint8,
	pause bool,
	err error,
) {
	if requiredMask&^forgepool.ExternalMask != 0 ||
		availableMask&^forgepool.ExternalMask != 0 {
		return 0, 0, false, errors.New("invalid FORGE external mask")
	}

	if requiredMask == 0 {
		// Both chains have crossed the pool-side exclusion threshold.
		// Never turn this into V2 version 0x20.
		return 0, availableMask, true, nil
	}

	// One or more chains are still mandatory but failed this observation.
	// Do not publish reduced work prematurely.
	if availableMask&requiredMask != requiredMask {
		return 0, availableMask, false, nil
	}

	// All currently mandatory chains are available.
	return requiredMask, 0, false, nil
}

// allUnavailableSignerProbe returns a deliberately non-publishable V2 request
// used only to let independent signers observe that BOTH chains are down.
//
// It declares DGB participation so it remains syntactically valid V2, but uses
// impossible placeholder external fields. Therefore it can advance signer
// health observations but can never pass trusted Digibyte comparison and reach
// a signing key.
func (rt *forgeRuntime) allUnavailableSignerProbe(
	ogg *rpc.OGGAuxBlock,
) (
	forgepool.ExternalTemplate,
	common.Hash,
	error,
) {
	stamp, limit, err := forgeWorkTime(ogg)
	if err != nil {
		return forgepool.ExternalTemplate{}, common.Hash{}, err
	}

	seconds := int64(rt.templateValidity / time.Second)
	if seconds <= 0 {
		return forgepool.ExternalTemplate{}, common.Hash{},
			errors.New("invalid configured template validity")
	}

	span := uint64(seconds)
	if span > limit {
		span = limit
	}
	if stamp > uint64(1<<63-1)-span {
		return forgepool.ExternalTemplate{}, common.Hash{},
			errors.New("template timestamp overflow")
	}

	raw := strings.TrimPrefix(strings.TrimSpace(ogg.WorkHash), "0x")
	if len(raw) != 64 {
		return forgepool.ExternalTemplate{}, common.Hash{},
			errors.New("invalid exact OGG WorkHash")
	}

	workHash := common.HexToHash("0x" + raw)
	if workHash == (common.Hash{}) {
		return forgepool.ExternalTemplate{}, common.Hash{},
			errors.New("zero OGG WorkHash")
	}

	tmpl := forgepool.ExternalTemplate{
		Version: forgepool.OGGTemplateVersionV2Base |
			forgepool.ExternalDGB,

		// Deliberately impossible trusted Digibyte identity. The signer will
		// always reject this before key use even if its local Digibyte view
		// unexpectedly becomes available during the request.
		DGBPrevHash: common.HexToHash("0x01"),
		DGBBits:     1,

		ValidFrom: stamp,
		Expires:   stamp + span,
	}

	return tmpl, workHash, nil
}

// probeAllUnavailableSigners performs one health observation per configured
// signer while both external chains are unavailable.
//
// No returned signature is ever used.
func (rt *forgeRuntime) probeAllUnavailableSigners(
	ogg *rpc.OGGAuxBlock,
) {
	tmpl, workHash, err := rt.allUnavailableSignerProbe(ogg)
	if err != nil {
		log.Printf("FORGE: cannot build all-unavailable signer health probe: %v", err)
		return
	}

	for _, signer := range rt.signers {
		sig, err := signer.sign(
			tmpl,
			nil,
			&workHash,
		)

		if err != nil {
			// Rejection is expected. Its purpose was the signer's independent
			// external health observation.
			continue
		}

		// This must never happen. Discard any response regardless.
		log.Printf(
			"FORGE SECURITY: signer %s unexpectedly authorized non-publishable all-unavailable health probe (%d bytes); discarded",
			signer.name,
			len(sig),
		)
	}
}

// clearExternalJobs invalidates every previously published FORGE job.
//
// This is used when both external revenue chains have crossed their exclusion
// threshold. It deliberately publishes nothing: internal mask 0 means PAUSE,
// never V2 template version 0x20.
func (rt *forgeRuntime) clearExternalJobs() bool {
	rt.mu.Lock()
	defer rt.mu.Unlock()

	hadWork := rt.currentID != "" || len(rt.jobs) != 0

	rt.jobs = make(map[string]*forgeJob)
	rt.order = nil
	rt.currentID = ""
	rt.duplicates = make(map[string]struct{})

	return hadWork
}

// forgeJobNeedsClean determines whether Stratum miners must discard the
// previous job immediately.
//
// Participation-mask transitions are always clean transitions. Active external
// parent-tip changes and OGG parent changes are also clean transitions.
func forgeJobNeedsClean(old, next *forgeJob) bool {
	if old == nil || next == nil {
		return true
	}

	if old.ExternalMask != next.ExternalMask {
		return true
	}

	if old.OGG == nil || next.OGG == nil {
		return true
	}
	if !sameHex(old.OGG.PreviousBlockHash, next.OGG.PreviousBlockHash) {
		return true
	}

	if old.Digibyte == nil || next.Digibyte == nil {
		return true
	}

	if next.ExternalMask&forgepool.ExternalDGB != 0 {
		if !old.Digibyte.DigibyteActive ||
			!next.Digibyte.DigibyteActive ||
			!sameRawHash(
				old.Digibyte.Template.Previous,
				next.Digibyte.Template.Previous,
			) {
			return true
		}
	}

	if next.ExternalMask&forgepool.ExternalDOGE != 0 {
		if old.Digibyte.DOGE == nil ||
			next.Digibyte.DOGE == nil ||
			!sameRawHash(
				old.Digibyte.DOGE.Template.Previous,
				next.Digibyte.DOGE.Template.Previous,
			) {
			return true
		}
	}

	return false
}

// buildV2RuntimeJob constructs one of the only permitted V2 runtime modes:
//
//   mask 0x03 -> OGG + DGB + DOGE
//   mask 0x01 -> OGG + DGB
//   mask 0x02 -> OGG + DOGE using a synthetic Scrypt carrier
//
// mask 0 is an internal pause state and can never become an OGG template.
func (rt *forgeRuntime) buildV2RuntimeJob(
	mask uint8,
	ogg *rpc.OGGAuxBlock,
	height uint64,
	difficulty int64,
	issuer *rpc.RPCClient,
	seq uint64,
	dgbTpl *forgepool.ParentTemplate,
	doge *forgepool.DogeCandidate,
) (*forgeJob, error) {
	if mask == 0 || mask&^forgepool.ExternalMask != 0 {
		return nil, fmt.Errorf("invalid FORGE V2 runtime mask %#x", mask)
	}
	if ogg == nil {
		return nil, errors.New("missing issued OGG work")
	}

	dgbActive := mask&forgepool.ExternalDGB != 0
	dogeActive := mask&forgepool.ExternalDOGE != 0

	if dogeActive {
		if doge == nil {
			return nil, errors.New("active Dogecoin mode has no DOGE candidate")
		}
	} else {
		doge = nil
	}

	var parent forgepool.ParentTemplate

	if dgbActive {
		if dgbTpl == nil {
			return nil, errors.New("active Digibyte mode has no Digibyte template")
		}
		parent = *dgbTpl
	} else {
		stamp, _, err := forgeWorkTime(ogg)
		if err != nil {
			return nil, err
		}
		if stamp > uint64(^uint32(0)) {
			return nil, errors.New("OGG timestamp exceeds synthetic parent range")
		}
		if height > uint64(^uint64(0)>>1) {
			return nil, errors.New("OGG height exceeds synthetic parent range")
		}

		parent, err = forgepool.SyntheticParentTemplate(
			int64(height),
			uint32(stamp),
		)
		if err != nil {
			return nil, err
		}
	}

	parentJob, err := forgepool.BuildParentJobV2(
		parent,
		ogg.WorkHash,
		doge,
		rt.dgbPayout,
		uint32(seq),
		rt.extraNonce1Size,
		rt.extraNonce2Size,
		dgbActive,
	)
	if err != nil {
		return nil, err
	}

	return &forgeJob{
		ID:           fmt.Sprintf("%08x", uint32(seq)),
		Created:      time.Now(),
		Height:       height,
		Difficulty:   difficulty,
		OGG:          ogg,
		Digibyte:     parentJob,
		ExternalMask: mask,
		OGGRPC:       issuer,
	}, nil
}

type rpcForgeSubmitter struct {
	auxClients map[string]*rpc.RPCClient
	refresh    func() // bounded refresh signal after all network attempts
	issuers    []*rpc.RPCClient
	dgb        *rpc.RPCClient
	doge       *rpc.RPCClient
	authorized []common.Address
	quorum     uint64
	clock      func() time.Time
}

func (r *rpcForgeSubmitter) SubmitDigibyte(job *forgeJob, sol *forgeSolution) error {
	if job == nil || job.Digibyte == nil || !job.Digibyte.DigibyteActive {
		return errors.New("Digibyte submission requested for inactive FORGE chain")
	}
	block, err := job.Digibyte.FullBlock(sol.ExtraNonce1, sol.ExtraNonce2, sol.NTime, sol.Nonce)
	if err != nil {
		return err
	}
	ok, err := r.dgb.SubmitRawBlock(hex.EncodeToString(block))
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("Digibyte submitblock rejected")
	}
	return nil
}

func (r *rpcForgeSubmitter) SubmitDogecoin(job *forgeJob, sol *forgeSolution) error {
	if job == nil || job.Digibyte == nil || job.Digibyte.DOGE == nil {
		return errors.New("Dogecoin submission requested for inactive FORGE chain")
	}
	proof, err := job.Digibyte.BuildDogeAuxPow(sol.ExtraNonce1, sol.ExtraNonce2, sol.NTime, sol.Nonce)
	if err != nil {
		return err
	}
	if err := job.Digibyte.ValidateDogeAuxPow(proof); err != nil {
		return err
	}
	block, err := job.Digibyte.DogeAuxPowBlock(sol.ExtraNonce1, sol.ExtraNonce2, sol.NTime, sol.Nonce)
	if err != nil {
		return err
	}
	ok, err := r.doge.SubmitRawBlock(hex.EncodeToString(block))
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("Dogecoin submitblock rejected AuxPoW block")
	}
	return nil
}

func (r *rpcForgeSubmitter) SubmitOGG(job *forgeJob, sol *forgeSolution) (string, error) {
	if job.OGGRPC == nil {
		return "", errors.New("missing issuing OGG RPC client")
	}
	if err := job.Signed.validate(job, r.authorized, r.quorum, forgeNow(r.clock)); err != nil {
		return "", err
	}
	blob, err := forgeEncodeJobProof(job, sol)
	if err != nil {
		return "", err
	}
	if !job.Signed.fresh(forgeNow(r.clock)) {
		return "", errors.New("OGG template expired before submission")
	}
	ok, err := job.OGGRPC.SubmitOGGAuxBlock(job.OGG.WorkHash, hex.EncodeToString(blob))
	if err != nil {
		return "", err
	}
	if !ok {
		return "", errors.New("OGG eth_submitAuxBlock rejected proof")
	}
	return storage.ForgeProofHash(blob), nil
}

func forgepoolReverse(h forgepool.Hash) forgepool.Hash {
	var out forgepool.Hash
	for i := range h {
		out[i] = h[len(h)-1-i]
	}
	return out
}

func parseForgeScript(s string) ([]byte, error) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "0x")
	if s == "" {
		return nil, errors.New("empty payout script")
	}
	out, err := hex.DecodeString(s)
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, errors.New("empty payout script")
	}
	return out, nil
}

func forgeEndpointDefaults(ep ForgeChainEndpoint, fallbackName string) ForgeChainEndpoint {
	if ep.Name == "" {
		ep.Name = fallbackName
	}
	if ep.Timeout == "" {
		ep.Timeout = "5s"
	}
	return ep
}

func (s *ProxyServer) initForge() error {
	cfg := &s.config.Proxy.Forge
	if !cfg.Enabled {
		return nil
	}
	allowedMask, policyErr := validateForgeExternalPolicy(*cfg)
	if policyErr != nil {
		return policyErr
	}
	if err := s.config.Proxy.Forge.VariableDifficulty.validate(s.config.Proxy.Difficulty); err != nil {
		return err
	}
	if s.config.Proxy.Difficulty <= 0 || s.config.Proxy.Difficulty > 1<<53-1 {
		return errors.New("FORGE requires proxy difficulty in [1, 2^53-1]")
	}
	if s.hashrateExpiration < time.Second || s.hashrateExpiration > 365*24*time.Hour {
		return errors.New("FORGE hashrate expiration must be between one second and one year")
	}
	if cfg.ExtraNonce1Size == 0 {
		cfg.ExtraNonce1Size = 4
	}
	if cfg.ExtraNonce2Size == 0 {
		cfg.ExtraNonce2Size = 4
	}
	if cfg.JobBacklog == 0 {
		cfg.JobBacklog = 8
	}
	if cfg.MaxNTimeDriftSec == 0 {
		cfg.MaxNTimeDriftSec = 7200
	}
	if cfg.TemplateValiditySec == 0 {
		cfg.TemplateValiditySec = 60
	}
	if cfg.TemplateValiditySec < 10 || cfg.TemplateValiditySec > 300 {
		return fmt.Errorf("invalid FORGE template validity %d", cfg.TemplateValiditySec)
	}
	if cfg.TemplateQuorum == 0 || cfg.TemplateQuorum > uint64(len(cfg.TemplateSigners)) {
		return errors.New("FORGE requires a valid external template signer quorum")
	}
	if len(cfg.TemplateSigners) > forgepool.MaxTemplateSigners {
		return errors.New("FORGE template signer set exceeds the certificate capacity")
	}
	var signers []*forgeSignerClient
	var authorized []common.Address
	seenSigner := make(map[common.Address]struct{})
	for _, c := range cfg.TemplateSigners {
		signer, err := newForgeSigner(c)
		if err != nil {
			return fmt.Errorf("invalid FORGE external signer: %v", err)
		}
		if _, exists := seenSigner[signer.address]; exists {
			return errors.New("duplicate FORGE signer address")
		}
		seenSigner[signer.address] = struct{}{}
		signers = append(signers, signer)
		authorized = append(authorized, signer.address)
	}
	if cfg.ExtraNonce1Size < 1 || cfg.ExtraNonce1Size > 8 {
		return fmt.Errorf("invalid FORGE extranonce1 size %d", cfg.ExtraNonce1Size)
	}
	if cfg.ExtraNonce2Size < 1 || cfg.ExtraNonce2Size > 16 {
		return fmt.Errorf("invalid FORGE extranonce2 size %d", cfg.ExtraNonce2Size)
	}
	if cfg.JobBacklog < 1 || cfg.JobBacklog > 64 {
		return fmt.Errorf("invalid FORGE job backlog %d", cfg.JobBacklog)
	}
	if cfg.MaxNTimeDriftSec < 1 || cfg.MaxNTimeDriftSec > 86400 {
		return fmt.Errorf("invalid FORGE max ntime drift %d", cfg.MaxNTimeDriftSec)
	}

	cfg.Digibyte = forgeEndpointDefaults(cfg.Digibyte, "digibyte")
	cfg.Dogecoin = forgeEndpointDefaults(cfg.Dogecoin, "dogecoin")
	if cfg.Digibyte.Url == "" || cfg.Dogecoin.Url == "" {
		return errors.New("FORGE requires Digibyte and Dogecoin RPC URLs")
	}
	dgbScript, err := parseForgeScript(cfg.Digibyte.PayoutScript)
	if err != nil {
		return fmt.Errorf("invalid Digibyte payout script: %v", err)
	}
	dogeScript, err := parseForgeScript(cfg.Dogecoin.PayoutScript)
	if err != nil {
		return fmt.Errorf("invalid Dogecoin payout script: %v", err)
	}
	shareTarget, err := forgepool.StratumTargetForDifficulty(big.NewInt(s.config.Proxy.Difficulty))
	if err != nil {
		return fmt.Errorf("invalid FORGE share difficulty: %v", err)
	}
	account, err := newForgeAccountingQueue(cfg.AccountingJournalDir, s.backend, cfg.Accounting)
	if err != nil {
		return err
	}

	rt := &forgeRuntime{
		allowedExternalMask: allowedMask,
		jobs:                make(map[string]*forgeJob),
		duplicates:          make(map[string]struct{}),
		refreshRequests:     make(chan struct{}, 1),
		dgbRPC:              rpc.NewRPCClient(cfg.Digibyte.Name, cfg.Digibyte.Url, cfg.Digibyte.Timeout, cfg.Digibyte.Username, cfg.Digibyte.Password),
		dogeRPC:             rpc.NewRPCClient(cfg.Dogecoin.Name, cfg.Dogecoin.Url, cfg.Dogecoin.Timeout, cfg.Dogecoin.Username, cfg.Dogecoin.Password),
		dgbPayout:           dgbScript,
		dogePayout:          dogeScript,
		extraNonce1Size:     cfg.ExtraNonce1Size,
		extraNonce2Size:     cfg.ExtraNonce2Size,
		jobBacklog:          cfg.JobBacklog,
		retainExternalWork:  cfg.RetainExternalWork,
		maxNTimeDrift:       time.Duration(cfg.MaxNTimeDriftSec) * time.Second,
		shareTarget:         shareTarget,
		signers:             signers,
		authorized:          authorized,
		templateQuorum:      cfg.TemplateQuorum,
		templateValidity:    time.Duration(cfg.TemplateValiditySec) * time.Second,
		account:             account,
	}
	submitter := &rpcForgeSubmitter{dgb: rt.dgbRPC, doge: rt.dogeRPC, issuers: s.upstreams, authorized: authorized, quorum: cfg.TemplateQuorum}
	aux, err := auxiliaryClients(cfg.AdditionalAux)
	if err != nil {
		return err
	}
	rt.additionalAux = aux
	if cfg.MultiChain {
		if cfg.ExtraNonce1Size != 4 || cfg.ExtraNonce2Size != 4 {
			return errors.New("V3 requires 4/4 extranonces")
		}
		rt.multi = true
		rt.multiHealth = map[uint8]*forgeChainHealth{}
		for _, bit := range []uint8{1, 2, 4, 8, 16} {
			rt.multiHealth[bit] = &forgeChainHealth{}
		}
	}
	submitter.auxClients = map[string]*rpc.RPCClient{}
	for _, a := range aux {
		submitter.auxClients[a.symbol] = a.client
	}
	submitter.refresh = rt.requestJobRefresh
	rt.submitter = submitter
	s.forge = rt
	if rt.retainExternalWork {
		log.Printf("FORGE: external work retention v1 enabled; signed expiry, external parents and OGG eligibility enforced")
	}
	// Replay is independent of miner retries or whether the old job survives.
	go func() {
		for {
			if err := account.recoverSubmissions(submitter); err != nil {
				log.Printf("FORGE submission journal pending: %v", err)
			}
			time.Sleep(5 * time.Second)
		}
	}()
	// Recovery, accounting and status make progress independently. Slow Redis
	// or RPC calls cannot suppress the monitor's heartbeat.
	go func() {
		for {
			if err := account.flush(); err != nil {
				log.Printf("FORGE accounting journal pending: %v", err)
			}
			time.Sleep(5 * time.Second)
		}
	}()
	go func() {
		for {
			if err := account.writeStatus(time.Now()); err != nil {
				log.Printf("FORGE operator status unavailable: %v", err)
			}
			time.Sleep(5 * time.Second)
		}
	}()
	return nil
}

func parseHexUint64(s string) (uint64, error) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "0x")
	if s == "" {
		return 0, errors.New("empty hexadecimal integer")
	}
	return strconv.ParseUint(s, 16, 64)
}

func parseHexTarget(s string) (*big.Int, error) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "0x")
	if s == "" {
		return nil, errors.New("empty target")
	}
	v, ok := new(big.Int).SetString(s, 16)
	if !ok || v.Sign() <= 0 || v.BitLen() > 256 {
		return nil, errors.New("invalid target")
	}
	return v, nil
}

func sameRawHash(a, b forgepool.Hash) bool {
	return a == b
}

func (s *ProxyServer) fetchForgeBlockTemplate() bool {
	if s.forge != nil && s.forge.multi {
		return s.fetchMultiTemplate()
	}
	rt := s.forge
	if rt == nil {
		return false
	}
	if rt.account.workPaused() {
		return false
	}

	rt.refreshMu.Lock()
	defer rt.refreshMu.Unlock()

	issuer := s.rpc()

	// ------------------------------------------------------------
	// OGG is the critical upstream.
	// ------------------------------------------------------------

	ogg, err := issuer.CreateOGGAuxBlock()
	if err != nil {
		log.Printf(
			"FORGE: failed OGG eth_createAuxBlock on %s: %v",
			issuer.Name,
			err,
		)
		s.markSick()
		return false
	}

	height, err := parseHexUint64(ogg.Height)
	if _, _, metaErr := forgeWorkTime(ogg); metaErr != nil {
		log.Printf("FORGE: incompatible OGG work metadata: %v", metaErr)
		s.markSick()
		return false
	}
	if err != nil || height == 0 || height > 1<<53-1 {
		log.Printf("FORGE: invalid OGG aux height %q: %v", ogg.Height, err)
		s.markSick()
		return false
	}

	difficulty, err := parseHexUint64(ogg.Difficulty)
	if err != nil ||
		difficulty == 0 ||
		difficulty > uint64(^uint64(0)>>1) {
		log.Printf("FORGE: invalid OGG difficulty %q", ogg.Difficulty)
		s.markSick()
		return false
	}

	// OGG itself is healthy. External failures below are not allowed to mark
	// the OGG upstream sick.
	s.markOk()

	seq := atomic.AddUint64(&rt.jobSeq, 1)

	// ------------------------------------------------------------
	// Digibyte observation.
	// ------------------------------------------------------------

	var (
		dgbTpl *forgepool.ParentTemplate
		dgbOK  bool
	)

	dgbReply, dgbErr := rt.dgbRPC.GetDigibyteBlockTemplate()
	if dgbErr != nil {
		log.Printf("FORGE: Digibyte health observation failed: %v", dgbErr)
	} else {
		converted, convErr := dgbReply.ToParentTemplate()
		if convErr != nil {
			dgbErr = convErr
			log.Printf("FORGE: invalid Digibyte template: %v", convErr)
		} else {
			dgbTpl = &converted
			dgbOK = true
		}
	}

	if dgbOK {
		if rt.dgbHealth.success() {
			log.Printf(
				"FORGE: Digibyte restored after %d consecutive successes",
				forgeChainEnableSuccesses,
			)
		}
	} else if rt.dgbHealth.failure() {
		log.Printf(
			"FORGE: Digibyte excluded after %d consecutive failures",
			forgeChainDisableFailures,
		)
	}

	// ------------------------------------------------------------
	// DOGE observation.
	// ------------------------------------------------------------

	var (
		doge   *forgepool.DogeCandidate
		dogeOK bool
	)

	dogeReply, dogeErr := rt.dogeRPC.GetDogecoinBlockTemplate()
	if dogeErr != nil {
		log.Printf("FORGE: DOGE health observation failed: %v", dogeErr)
	} else {
		dogeTpl, convErr := dogeReply.ToParentTemplate()
		if convErr != nil {
			dogeErr = convErr
			log.Printf("FORGE: invalid DOGE template: %v", convErr)
		} else {
			doge, dogeErr = forgepool.BuildDogeCandidate(
				dogeTpl,
				rt.dogePayout,
				uint32(seq),
			)
			if dogeErr != nil {
				log.Printf(
					"FORGE: DOGE candidate construction failed: %v",
					dogeErr,
				)
			} else {
				dogeOK = true
			}
		}
	}

	if dogeOK {
		if rt.dogeHealth.success() {
			log.Printf(
				"FORGE: DOGE restored after %d consecutive successes",
				forgeChainEnableSuccesses,
			)
		}
	} else if rt.dogeHealth.failure() {
		log.Printf(
			"FORGE: DOGE excluded after %d consecutive failures",
			forgeChainDisableFailures,
		)
	}

	availableMask := forgeAvailableExternalMask(dgbOK, dogeOK)
	requiredMask := rt.requiredExternalMask()

	publishMask, probeMask, pause, err := forgeRefreshDecision(
		requiredMask,
		availableMask,
	)
	if err != nil {
		log.Printf("FORGE: invalid runtime health decision: %v", err)
		return false
	}

	// ------------------------------------------------------------
	// Signer convergence probe.
	//
	// Pool health never grants reduced authorization. The signer independently
	// probes the same chains and must independently converge to the same mask.
	// ------------------------------------------------------------

	if publishMask == 0 {
		if probeMask != 0 {
			probeJob, buildErr := rt.buildV2RuntimeJob(
				probeMask,
				ogg,
				height,
				int64(difficulty),
				issuer,
				seq,
				dgbTpl,
				doge,
			)

			if buildErr != nil {
				log.Printf(
					"FORGE: reduced-mode signer probe build failed mask=%#x: %v",
					probeMask,
					buildErr,
				)
			} else if signErr := rt.signJob(probeJob); signErr != nil {
				log.Printf(
					"FORGE: signer health convergence probe mask=%#x rejected: %v",
					probeMask,
					signErr,
				)
			} else {
				// Safe to discard. Pool hysteresis has not authorized publication.
				log.Printf(
					"FORGE: signer independently authorized tentative mask=%#x; discarded until pool health agrees",
					probeMask,
				)
			}
		} else {
			// Both external chains are absent. A syntactically valid but
			// impossible-to-sign request lets signer-owned health advance too.
			rt.probeAllUnavailableSigners(ogg)
		}

		if pause {
			if rt.clearExternalJobs() {
				disconnected := s.disconnectForgeMinersForPause()
				log.Printf(
					"FORGE: PAUSED â€” both Digibyte and Dogecoin excluded; previous jobs invalidated; disconnected %d active Stratum miners",
					disconnected,
				)
			} else {
				log.Printf(
					"FORGE: PAUSED â€” both Digibyte and Dogecoin excluded",
				)
			}
		} else {
			log.Printf(
				"FORGE: no fresh job â€” required mask=%#x available=%#x; retaining only existing fresh signed work",
				requiredMask,
				availableMask,
			)
		}

		return false
	}

	// ------------------------------------------------------------
	// Build/sign the exact pool-authorized V2 mode.
	// ------------------------------------------------------------

	job, err := rt.buildV2RuntimeJob(
		publishMask,
		ogg,
		height,
		int64(difficulty),
		issuer,
		seq,
		dgbTpl,
		doge,
	)
	if err != nil {
		log.Printf(
			"FORGE: failed V2 runtime construction mask=%#x: %v",
			publishMask,
			err,
		)
		return false
	}

	if err := rt.attachAdditionalAux(job, uint32(seq)); err != nil {
		log.Printf("FORGE additional auxiliary work unavailable: %v", err)
		return false
	}
	if err := rt.signJob(job); err != nil {
		// This commonly means signer-side health has not independently reached
		// the same state yet. Never weaken the requested mask and never mark
		// the OGG upstream sick.
		log.Printf(
			"FORGE: signer refused V2 mask=%#x; no fresh job published: %v",
			publishMask,
			err,
		)
		return false
	}

	// ------------------------------------------------------------
	// Publish.
	// ------------------------------------------------------------

	rt.mu.Lock()

	if !job.Signed.fresh(forgeNow(rt.clock)) ||
		rt.account.workPaused() {
		rt.mu.Unlock()
		log.Printf("FORGE: signed V2 job became stale before publication")
		return false
	}

	old := rt.jobs[rt.currentID]

	if old == nil ||
		old.Signed == nil ||
		!old.Signed.fresh(forgeNow(rt.clock)) ||
		rt.jobNeedsClean(old, job) {
		job.Clean = true
		rt.jobs = make(map[string]*forgeJob)
		rt.order = nil
		rt.duplicates = make(map[string]struct{})
	}

	rt.jobs[job.ID] = job
	rt.order = append(rt.order, job.ID)
	rt.currentID = job.ID

	for len(rt.order) > rt.jobBacklog {
		drop := rt.order[0]
		rt.order = rt.order[1:]
		delete(rt.jobs, drop)

		prefix := drop + ":"
		for k := range rt.duplicates {
			if strings.HasPrefix(k, prefix) {
				delete(rt.duplicates, k)
			}
		}
	}

	rt.mu.Unlock()

	log.Printf(
		"FORGE: new V2 Scrypt job %s OGG height=%d mask=%#x clean=%v",
		job.ID,
		job.Height,
		job.ExternalMask,
		job.Clean,
	)

	if s.config.Proxy.Stratum.Enabled {
		go s.broadcastForgeJobs()
	}
	return true
}

func (rt *forgeRuntime) currentJob() *forgeJob {
	if rt.account != nil && rt.account.workPaused() {
		return nil
	}
	rt.mu.RLock()
	defer rt.mu.RUnlock()
	job := rt.jobs[rt.currentID]
	if job == nil || !job.Signed.fresh(forgeNow(rt.clock)) {
		return nil
	}
	return job
}

func (rt *forgeRuntime) job(id string) *forgeJob {
	rt.mu.RLock()
	defer rt.mu.RUnlock()
	return rt.jobs[id]
}

func (rt *forgeRuntime) nextExtraNonce1() []byte {
	seq := atomic.AddUint64(&rt.extraNonceSeq, 1)
	var full [8]byte
	binary.BigEndian.PutUint64(full[:], seq)
	out := make([]byte, rt.extraNonce1Size)
	copy(out, full[8-rt.extraNonce1Size:])
	return out
}

// stratumPrevHash converts raw/internal Bitcoin hash bytes into the historical
// Stratum V1 prevhash form: 32-bit words are reversed in order while bytes
// inside each displayed word are kept. Equivalently, reverse each 4-byte word
// of the raw little-endian hash.
func stratumPrevHash(raw forgepool.Hash) string {
	out := make([]byte, 32)
	for i := 0; i < 32; i += 4 {
		out[i+0] = raw[i+3]
		out[i+1] = raw[i+2]
		out[i+2] = raw[i+1]
		out[i+3] = raw[i+0]
	}
	return hex.EncodeToString(out)
}

func forgeNotifyParams(job *forgeJob) []interface{} {
	branches := make([]string, len(job.Digibyte.MerkleBranch))
	for i, h := range job.Digibyte.MerkleBranch {
		branches[i] = hex.EncodeToString(h[:])
	}
	return []interface{}{
		job.ID,
		stratumPrevHash(job.Digibyte.Template.Previous),
		hex.EncodeToString(job.Digibyte.Coinbase.Coinb1),
		hex.EncodeToString(job.Digibyte.Coinbase.Coinb2),
		branches,
		fmt.Sprintf("%08x", uint32(job.Digibyte.Template.Version)),
		fmt.Sprintf("%08x", job.Digibyte.Template.Bits),
		fmt.Sprintf("%08x", job.Digibyte.Template.CurTime),
		job.Clean,
	}
}

func decodeExactHex(s string, n int) ([]byte, error) {
	if len(s) != n*2 {
		return nil, fmt.Errorf("hex length %d want %d", len(s), n*2)
	}
	out, err := hex.DecodeString(s)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func parseUint32Hex(s string) (uint32, error) {
	if len(s) != 8 {
		return 0, errors.New("uint32 must be 8 hex chars")
	}
	v, err := strconv.ParseUint(s, 16, 32)
	return uint32(v), err
}

func (rt *forgeRuntime) duplicateKey(jobID string, sol *forgeSolution) string {
	return jobID + ":" + hex.EncodeToString(sol.ExtraNonce1) + ":" +
		hex.EncodeToString(sol.ExtraNonce2) + ":" +
		fmt.Sprintf("%08x:%08x", sol.NTime, sol.Nonce)
}

func (rt *forgeRuntime) claimDuplicate(key string) bool {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if _, ok := rt.duplicates[key]; ok {
		return false
	}
	rt.duplicates[key] = struct{}{}
	return true
}

func (s *ProxyServer) validateForgeShare(cs *Session, jobID, extra2Hex, ntimeHex, nonceHex string, now time.Time) (*forgeJob, *forgeSolution, forgeHits, error) {
	var hits forgeHits
	if s.forge == nil {
		return nil, nil, hits, errors.New("FORGE is disabled")
	}
	job, current := s.forge.shareJobs(jobID)
	if job == nil || !job.Signed.fresh(now) {
		return nil, nil, hits, errForgeStale
	}
	nativeEligible := true
	if s.forge.retainExternalWork {
		if current == nil || !current.Signed.fresh(now) {
			return nil, nil, hits, errForgeStale
		}
		if job != current {
			if !forgeExternalWorkCompatible(job, current) {
				return nil, nil, hits, errForgeStale
			}
			nativeEligible = job.Height == current.Height && sameHex(job.OGG.PreviousBlockHash, current.OGG.PreviousBlockHash)
		}
	}
	if len(cs.extraNonce1) != s.forge.extraNonce1Size {
		return nil, nil, hits, fmt.Errorf("%w: missing extranonce1", errForgeMalformed)
	}
	extra2, err := decodeExactHex(extra2Hex, s.forge.extraNonce2Size)
	if err != nil {
		return nil, nil, hits, fmt.Errorf("%w: extranonce2: %v", errForgeMalformed, err)
	}
	ntime, err := parseUint32Hex(ntimeHex)
	if err != nil {
		return nil, nil, hits, fmt.Errorf("%w: ntime: %v", errForgeMalformed, err)
	}
	nonce, err := parseUint32Hex(nonceHex)
	if err != nil {
		return nil, nil, hits, fmt.Errorf("%w: nonce: %v", errForgeMalformed, err)
	}
	if ntime < job.Digibyte.Template.CurTime {
		return nil, nil, hits, fmt.Errorf("%w: ntime before template", errForgeMalformed)
	}
	maxTime := now.Add(s.forge.maxNTimeDrift).Unix()
	if int64(ntime) > maxTime {
		return nil, nil, hits, fmt.Errorf("%w: ntime too far in future", errForgeMalformed)
	}

	header, err := job.Digibyte.Header(cs.extraNonce1, extra2, ntime, nonce)
	if err != nil {
		return nil, nil, hits, fmt.Errorf("%w: header reconstruction: %v", errForgeMalformed, err)
	}
	raw, err := forgepool.ScryptHash(header)
	if err != nil {
		return nil, nil, hits, err
	}
	value := forgepool.HashValue(raw)
	poolTarget := s.forge.shareTarget
	shareDifficulty := s.config.Proxy.Difficulty
	if s.forgeVarDiffEnabled() {
		assignment, issued := cs.issuedForgeAssignment(jobID)
		if !issued {
			return nil, nil, hits, errForgeStale
		}
		poolTarget, shareDifficulty = assignment.target, assignment.difficulty
	}
	if poolTarget == nil || poolTarget.Sign() <= 0 {
		return nil, nil, hits, errors.New("invalid configured FORGE share target")
	}
	if value.Cmp(poolTarget) > 0 {
		return nil, nil, hits, errForgeLowDifficulty
	}

	if job.Signed == nil {
		return nil, nil, hits, errors.New("FORGE job missing signed participation template")
	}

	mask, err := forgepool.TemplateExternalMask(job.Signed.Template)
	if job.Signed.Multi != nil {
		mask = job.Signed.Multi.Mask
		err = forgepool.MultiTemplateValid(*job.Signed.Multi)
	}
	if err != nil {
		return nil, nil, hits, err
	}

	dgbActive := mask&forgepool.ExternalDGB != 0
	dogeActive := mask&forgepool.ExternalDOGE != 0

	if job.Digibyte == nil {
		return nil, nil, hits, errors.New("FORGE job missing Scrypt parent")
	}
	if job.Digibyte.DigibyteActive != dgbActive {
		return nil, nil, hits, errors.New("FORGE Digibyte runtime state does not match signed mask")
	}
	if (job.Digibyte.DOGE != nil) != dogeActive {
		return nil, nil, hits, errors.New("FORGE Dogecoin runtime state does not match signed mask")
	}

	if dgbActive {
		dgbTarget, err := forgepool.CompactTarget(job.Digibyte.Template.Bits)
		if err != nil {
			return nil, nil, hits, err
		}
		hits.Digibyte = value.Cmp(dgbTarget) <= 0
	}

	if dogeActive {
		dogeTarget, err := forgepool.CompactTarget(job.Digibyte.DOGE.Bits)
		if err != nil {
			return nil, nil, hits, err
		}
		hits.Dogecoin = value.Cmp(dogeTarget) <= 0
	}

	for symbol, id := range job.AuxSymbols {
		c := job.Digibyte.AdditionalAux[id]
		target, e := forgepool.CompactTarget(c.Bits)
		if e != nil {
			return nil, nil, hits, e
		}
		hit := value.Cmp(target) <= 0
		switch symbol {
		case "PEP":
			hits.Pepecoin = hit
		case "DINGO":
			hits.Dingocoin = hit
		case "BELLS":
			hits.Bellscoin = hit
		}
	}
	oggTarget, err := parseHexTarget(job.OGG.Target)
	if err != nil {
		return nil, nil, hits, fmt.Errorf("invalid OGG target: %v", err)
	}
	hits.OGG = nativeEligible && value.Cmp(oggTarget) <= 0

	sol := &forgeSolution{
		ShareDifficulty: shareDifficulty,
		ExtraNonce1:     append([]byte(nil), cs.extraNonce1...),
		ExtraNonce2:     extra2,
		NTime:           ntime,
		Nonce:           nonce,
		Header:          header,
		Hash:            raw,
		HashValue:       value,
	}
	return job, sol, hits, nil
}

func (s *ProxyServer) processForgeShare(cs *Session, login, worker, jobID, extra2Hex, ntimeHex, nonceHex string, now time.Time) (valid bool, duplicate bool, resultHits forgeHits, resultErr error) {
	var noHits forgeHits
	job, sol, hits, err := s.validateForgeShare(cs, jobID, extra2Hex, ntimeHex, nonceHex, now)
	if err != nil {
		return false, false, noHits, err
	}
	if !hits.Digibyte && !hits.Dogecoin && !hits.OGG && !hits.extra() && s.forge.account.rejectOrdinaryIfPaused() {
		return false, false, hits, errForgeBackpressure
	}

	key := s.forge.duplicateKey(jobID, sol)
	if !s.forge.claimDuplicate(key) {
		return false, true, noHits, errForgeDuplicate
	}
	defer func() {
		if errors.Is(resultErr, errForgeBackpressure) {
			s.forge.mu.Lock()
			delete(s.forge.duplicates, key)
			s.forge.mu.Unlock()
		}
	}()

	// Route all network-target hits BEFORE Redis accounting. A slow or failed
	// accounting backend therefore cannot cost the pool a valid block solution.
	id := sha256.Sum256(sol.Header)
	receipt := storage.ForgeShare{
		ID: hex.EncodeToString(id[:]), Login: login, Worker: worker,
		Height: int64(job.Height), Difficulty: sol.ShareDifficulty,
		RoundDifficulty: job.Difficulty, TimestampMS: now.UnixNano() / int64(time.Millisecond),
		WindowSeconds: int64(s.hashrateExpiration / time.Second), WorkHash: job.OGG.WorkHash,
	}
	if submitter, ok := s.forge.submitter.(*rpcForgeSubmitter); ok {
		return s.processForgeIntent(submitter, job, sol, hits, receipt)
	}
	if hits.Digibyte {
		blockHash := sha256.Sum256(id[:])
		receipt.Digibyte = storage.ForgeSubmission{Attempted: true, BlockHash: "0x" + forgepool.DisplayHash(forgepool.Hash(blockHash))}
		if err := s.forge.submitter.SubmitDigibyte(job, sol); err != nil {
			log.Printf("FORGE DGB block submission for job %s failed: %v", jobID, err)
			receipt.Digibyte.Error = err.Error()
		} else {
			receipt.Digibyte.Accepted = true
		}
	}
	if hits.Dogecoin {
		receipt.Dogecoin = storage.ForgeSubmission{Attempted: true, BlockHash: "0x" + job.Digibyte.DOGE.DisplayHash}
		if err := s.forge.submitter.SubmitDogecoin(job, sol); err != nil {
			log.Printf("FORGE DOGE block submission for job %s failed: %v", jobID, err)
			receipt.Dogecoin.Error = err.Error()
		} else {
			receipt.Dogecoin.Accepted = true
		}
	}
	if hits.OGG {
		if proofHash, err := s.forge.submitter.SubmitOGG(job, sol); err != nil {
			log.Printf("FORGE OGG block submission for job %s failed: %v", jobID, err)
			receipt.OGGError = err.Error()
		} else {
			receipt.ProofHash = proofHash
		}
	}

	exist, err := s.forge.account.enqueue(receipt)
	if err != nil {
		// Keep the duplicate claim: re-signing/re-submitting an accepted proof
		// can lose its identity. The journal retries the immutable receipt.
		return false, false, hits, err
	}
	if exist {
		return false, true, hits, errForgeDuplicate
	}
	return true, false, hits, nil
}
