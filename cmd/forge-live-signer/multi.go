package main

import (
	"context"
	"crypto/ecdsa"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/HawyUkr/open-progpow-pool/forgepool"
	"github.com/HawyUkr/open-progpow-pool/rpc"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"io"
	"log"
	"net/http"
	"sync"
	"time"
)

type multiEndpoint struct {
	Symbol       string `json:"symbol"`
	ChainID      uint32 `json:"chainId"`
	PayoutScript string `json:"payoutScript"`
	endpoint
}
type multiView struct {
	ep     multiEndpoint
	client *rpc.RPCClient
	reply  *rpc.BlockTemplateReply
	tpl    forgepool.ParentTemplate
	err    error
}
type multiHealth struct {
	good, bad int
	excluded  bool
}

func (h *multiHealth) observe(ok bool) {
	if ok {
		h.bad = 0
		h.good++
		if h.good >= 2 {
			h.excluded = false
		}
	} else {
		h.good = 0
		h.bad++
		if h.bad >= 3 {
			h.excluded = true
		}
	}
}

type multiService struct {
	cfg           config
	key           *ecdsa.PrivateKey
	mu            sync.Mutex
	health        map[string]*multiHealth
	admissionOnce sync.Once
	admission     chan struct{}
}

const multiRequestTimeout = 45 * time.Second

func (s *multiService) admit() bool {
	s.admissionOnce.Do(func() { s.admission = make(chan struct{}, 1) })
	select {
	case s.admission <- struct{}{}:
		return true
	default:
		return false
	}
}

var multiBits = map[string]uint8{"DGB": 1, "DOGE": 2, "PEP": 4, "DINGO": 8, "BELLS": 16}
var multiIDs = map[string]uint32{"DGB": 0, "DOGE": 98, "PEP": 63, "DINGO": 50, "BELLS": 16}
var multiGenesis = map[string]string{"DGB": "7497ea1b465eb39f1c8f507bc877078fe016d6fcb6dfad3a64c98dcc6e1e8496", "DOGE": "1a91e3dace36e2be3bf030a65679fe821aa1d6ef92e7c9902eb318182c355691", "PEP": "37981c0c48b8d48965376c8a42ece9a0838daadb93ff975cb091f57f8c2a5faa", "DINGO": "1a91e3dace36e2be3bf030a65679fe821aa1d6ef92e7c9902eb318182c355691", "BELLS": "e5be24df57c43a82d15c2f06bda961296948f8f8eb48501bed1efb929afe0698"}

func validateMultiConfig(c config) error {
	allowed, err := forgepool.ExternalChainPolicyMask(c.ExternalChainPolicy)
	if err != nil {
		return err
	}
	expected := 5
	if allowed == 3 {
		expected = 2
	}
	if len(c.Multi) != expected {
		return errors.New("configured treasury chains differ from the selected policy")
	}
	seen := map[string]bool{}
	var configured uint8
	for _, ep := range c.Multi {
		if seen[ep.Symbol] || multiBits[ep.Symbol] == 0 || multiBits[ep.Symbol]&allowed == 0 || ep.ChainID != multiIDs[ep.Symbol] || ep.Genesis != multiGenesis[ep.Symbol] {
			return errors.New("invalid multi-chain identity")
		}
		seen[ep.Symbol] = true
		configured |= multiBits[ep.Symbol]
		if e := ep.endpoint.validate(); e != nil {
			return e
		}
		script, e := hex.DecodeString(ep.PayoutScript)
		if e != nil || len(script) < 4 || len(script) > 100 || script[0] == 0x6a {
			return errors.New("invalid multi-chain treasury script")
		}
	}
	if configured != allowed {
		return errors.New("incomplete treasury chain policy")
	}
	return nil
}
func (s *multiService) views(ctx context.Context) map[string]multiView {
	out := map[string]multiView{}
	results := make(chan multiView, len(s.cfg.Multi))
	for _, ep := range s.cfg.Multi {
		go func(ep multiEndpoint) {
			v := multiView{ep: ep}
			v.client, v.err = rpc.NewValidationRPCClient(ctx, ep.Symbol, ep.URL, "4s", ep.Username, ep.Password)
			if v.err == nil {
				v.err = v.client.CheckAuxiliaryMainnet(ep.Genesis)
			}
			if v.err == nil && ep.Symbol == "DOGE" {
				v.err = v.client.CheckDogecoinIdentity()
			}
			if v.err == nil && ep.Symbol == "DINGO" {
				v.err = v.client.CheckDingocoinIdentity()
			}
			if v.err == nil {
				if ep.Symbol == "DGB" {
					v.reply, v.err = v.client.GetDigibyteBlockTemplate()
				} else {
					v.reply, v.err = v.client.GetAuxiliaryBlockTemplate(ep.Symbol == "BELLS")
				}
			}
			if v.err == nil {
				v.tpl, v.err = v.reply.ToParentTemplate()
			}
			if v.err == nil {
				script, _ := hex.DecodeString(ep.PayoutScript)
				v.err = v.client.CheckTreasuryProposal(ctx, v.tpl, script, ep.ChainID)
			}
			results <- v
		}(ep)
	}
	for range s.cfg.Multi {
		v := <-results
		out[v.ep.Symbol] = v
	}
	return out
}
func (s *multiService) validateAndSign(ctx context.Context, req forgepool.MultiSignRequest) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	views := s.views(ctx)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	required := uint8(0)
	for symbol, v := range views {
		h := s.health[symbol]
		h.observe(v.err == nil)
		if !h.excluded {
			required |= multiBits[symbol]
		}
	}
	// Health observations happen even for an intentionally invalid zero-mask
	// probe. Nothing reaches the signing key without a nonempty valid template.
	if e := forgepool.MultiTemplateValid(req.Template); e != nil {
		return nil, e
	}
	t := req.Template
	allowed, err := forgepool.ExternalChainPolicyMask(s.cfg.ExternalChainPolicy)
	if err != nil || t.Mask & ^allowed != 0 {
		return nil, errors.New("proposal contains a chain excluded by the configured policy")
	}
	now := uint64(time.Now().Unix())
	if !forgepool.RevenueAnchorPresent(required) || !forgepool.RevenueAnchorPresent(t.Mask) || t.Mask != required || t.ValidFrom > now || now-t.ValidFrom > 300 || t.Expires <= now || req.WorkHash == (common.Hash{}) {
		return nil, errors.New("independent signer health or work window differs")
	}
	if len(req.Aux) > 4 {
		return nil, errors.New("too many auxiliary proposals")
	}
	evidence := map[uint32]forgepool.ParentTemplate{}
	for _, e := range req.Aux {
		if forgepool.MultiChainBit(e.ChainID) == 0 || len(e.Candidate.Transactions) > 20000 {
			return nil, errors.New("unsupported evidence")
		}
		if _, ok := evidence[e.ChainID]; ok {
			return nil, errors.New("duplicate evidence")
		}
		evidence[e.ChainID] = e.Candidate
	}
	if len(evidence) != len(t.Aux) {
		return nil, errors.New("evidence participation mismatch")
	}
	candidates := map[uint32]*forgepool.DogeCandidate{}
	for _, ref := range t.Aux {
		var view multiView
		for _, v := range views {
			if v.ep.ChainID == ref.ChainID {
				view = v
			}
		}
		if view.err != nil || view.reply == nil {
			return nil, errors.New("participating native node unavailable")
		}
		p, ok := evidence[ref.ChainID]
		if !ok || p.Previous != view.tpl.Previous || p.Bits != view.tpl.Bits || p.Height != view.tpl.Height || p.Version&0xffff != view.tpl.Version&0xffff || int64(p.CurTime) < view.reply.MinTime || uint64(p.CurTime) > now+120 || now > uint64(p.CurTime)+300 {
			return nil, errors.New("candidate differs from trusted native tip")
		}
		script, _ := hex.DecodeString(view.ep.PayoutScript)
		candidate, e := forgepool.BuildAuxCandidate(p, script, req.AuxNonce, ref.ChainID)
		if e != nil {
			return nil, e
		}
		if common.HexToHash(candidate.DisplayHash) != ref.Hash {
			return nil, errors.New("candidate does not bind treasury output")
		}
		proposal, e := candidate.AuxValidationBlock()
		if e != nil {
			return nil, e
		}
		incremented, e := candidate.TreasuryClaimProbe(ref.ChainID, script)
		if e != nil {
			return nil, e
		}
		if e = view.client.ValidateFullTreasuryClaim(ctx, proposal, incremented); e != nil {
			return nil, e
		}
		candidates[ref.ChainID] = candidate
	}
	var parent forgepool.ParentTemplate
	var script []byte
	if t.Mask&1 != 0 {
		v := views["DGB"]
		if v.err != nil || req.DGB == nil {
			return nil, errors.New("native DGB parent unavailable")
		}
		parent = *req.DGB
		if parent.Previous != v.tpl.Previous || parent.Bits != v.tpl.Bits || parent.Height != v.tpl.Height || parent.Version != v.tpl.Version || int64(parent.CurTime) < v.reply.MinTime || uint64(parent.CurTime) > now+120 || now > uint64(parent.CurTime)+300 {
			return nil, errors.New("DGB parent differs from trusted tip")
		}
		script, _ = hex.DecodeString(v.ep.PayoutScript)
	} else {
		if req.DGB != nil {
			return nil, errors.New("inactive DGB evidence")
		}
		var e error
		parent, e = forgepool.SyntheticParentTemplate(1, uint32(t.ValidFrom))
		if e != nil {
			return nil, e
		}
		script = []byte{0x6a}
	}
	// Synthetic carrier height is normalized to one; it is not an external
	// reward-bearing chain. The OGG work leaf provides its native height binding.
	builder := forgepool.BuildMultiParent
	if t.Version == 4 {
		builder = forgepool.BuildMultiParentV4
	}
	job, e := builder(parent, req.WorkHash.Hex(), candidates, script, req.AuxNonce, 4, 4, t.Mask&1 != 0)
	if e != nil {
		return nil, e
	}
	rebuilt, e := job.MultiTemplate(t.ValidFrom, t.Expires)
	if e != nil || !forgepool.MultiTemplatesEqual(rebuilt, t) {
		return nil, errors.New("parent body or tree differs from validated job")
	}
	if t.Mask&1 != 0 {
		block, e := job.FullBlock(make([]byte, 4), make([]byte, 4), parent.CurTime, 0)
		if e != nil {
			return nil, e
		}
		incremented, e := job.TreasuryClaimProbe(make([]byte, 4), make([]byte, 4), parent.CurTime, 0)
		if e != nil {
			return nil, e
		}
		if e = views["DGB"].client.ValidateFullTreasuryClaim(ctx, block, incremented); e != nil {
			return nil, e
		}
	}
	digest, e := forgepool.MultiDigest(t, req.WorkHash)
	if e != nil {
		return nil, e
	}
	if e = ctx.Err(); e != nil {
		return nil, e
	}
	signature, e := crypto.Sign(digest.Bytes(), s.key)
	if e != nil {
		return nil, e
	}
	// Revalidate the participating tips after key use; a signature generated
	// across a native tip change is discarded, never returned to the pool.
	for symbol, v := range views {
		if t.Mask&multiBits[symbol] == 0 {
			continue
		}
		var tip string
		if e = v.ep.call(ctx, "getbestblockhash", []interface{}{}, &tip); e != nil {
			return nil, e
		}
		if tip != forgepool.DisplayHash(v.tpl.Previous) {
			return nil, errors.New("native tip changed during authorization")
		}
	}
	return signature, nil
}
func (s *multiService) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "POST required", 405)
		return
	}
	if r.Context().Err() != nil {
		http.Error(w, "request canceled", http.StatusRequestTimeout)
		return
	}
	if !s.admit() {
		w.Header().Set("Retry-After", "1")
		http.Error(w, "signer busy", http.StatusServiceUnavailable)
		return
	}
	defer func() { <-s.admission }()
	ctx, cancel := context.WithTimeout(r.Context(), multiRequestTimeout)
	defer cancel()
	r.Body = http.MaxBytesReader(w, r.Body, 16*1024*1024)
	var req forgepool.MultiSignRequest
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if d.Decode(&req) != nil {
		http.Error(w, "invalid multi-chain request", 400)
		return
	}
	var extra interface{}
	if d.Decode(&extra) != io.EOF {
		http.Error(w, "trailing request", 400)
		return
	}
	now := uint64(time.Now().Unix())
	if ctx.Err() != nil || forgepool.MultiTemplateValid(req.Template) != nil || req.WorkHash == (common.Hash{}) ||
		req.Template.ValidFrom > now || now-req.Template.ValidFrom > 300 || req.Template.Expires <= now ||
		len(req.Aux) > 4 || (req.Template.Mask&1 != 0 && req.DGB == nil) ||
		(req.DGB != nil && len(req.DGB.Transactions) > 20000) {
		http.Error(w, "invalid multi-chain request", http.StatusBadRequest)
		return
	}
	for _, evidence := range req.Aux {
		if len(evidence.Candidate.Transactions) > 20000 {
			http.Error(w, "invalid multi-chain request", http.StatusBadRequest)
			return
		}
	}
	sig, e := s.validateAndSign(ctx, req)
	if e != nil {
		http.Error(w, "multi-chain authorization refused", 422)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(forgepool.TemplateSignResponse{Signer: crypto.PubkeyToAddress(s.key.PublicKey).Hex(), Signature: hex.EncodeToString(sig)})
}
func runMulti(c config, key *ecdsa.PrivateKey, check bool) error {
	if e := validateMultiConfig(c); e != nil {
		return e
	}
	s := &multiService{cfg: c, key: key, health: map[string]*multiHealth{}}
	for symbol := range multiBits {
		s.health[symbol] = &multiHealth{}
	}
	if check {
		healthy := 0
		ctx, cancel := context.WithTimeout(context.Background(), multiRequestTimeout)
		defer cancel()
		for symbol, v := range s.views(ctx) {
			if v.err == nil {
				healthy++
				fmt.Println(symbol + " ready")
			} else {
				fmt.Println(symbol + " inactive")
			}
		}
		if healthy == 0 {
			return errors.New("no eligible external mainnet source")
		}
		fmt.Println("PASS: signer identity and at least one native mainnet source; no rewards claimed")
		return nil
	}
	server := &http.Server{Addr: c.Listen, Handler: s, ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 60 * time.Second, IdleTimeout: 15 * time.Second, MaxHeaderBytes: 8192}
	log.Printf("FORGE V3 signer %s; nonempty external participation and all treasury destinations enforced", crypto.PubkeyToAddress(key.PublicKey).Hex())
	return server.ListenAndServe()
}
