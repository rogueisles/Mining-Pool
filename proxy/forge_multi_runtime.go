package proxy

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/HawyUkr/open-progpow-pool/forgepool"
	"github.com/HawyUkr/open-progpow-pool/rpc"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"io"
	"log"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

type forgeMultiView struct {
	symbol  string
	bit     uint8
	id      uint32
	genesis string
	client  *rpc.RPCClient
	script  []byte
	tpl     forgepool.ParentTemplate
	err     error
}

func (rt *forgeRuntime) multiViews() []forgeMultiView {
	views := []forgeMultiView{
		{symbol: "DGB", bit: 1, genesis: "7497ea1b465eb39f1c8f507bc877078fe016d6fcb6dfad3a64c98dcc6e1e8496", client: rt.dgbRPC, script: rt.dgbPayout},
		{symbol: "DOGE", bit: 2, id: 98, genesis: "1a91e3dace36e2be3bf030a65679fe821aa1d6ef92e7c9902eb318182c355691", client: rt.dogeRPC, script: rt.dogePayout},
	}
	for _, a := range rt.additionalAux {
		views = append(views, forgeMultiView{symbol: a.symbol, bit: forgepool.MultiChainBit(a.chainID), id: a.chainID, genesis: a.genesis, client: a.client, script: a.script})
	}
	done := make(chan forgeMultiView, len(views))
	for _, view := range views {
		go func(v forgeMultiView) {
			c := rpc.NewRPCClient(v.symbol, v.client.Url, "3s", v.client.Username, v.client.Password)
			v.err = c.CheckAuxiliaryMainnet(v.genesis)
			if v.err == nil && v.symbol == "DOGE" {
				v.err = c.CheckDogecoinIdentity()
			}
			if v.err == nil && v.symbol == "DINGO" {
				v.err = c.CheckDingocoinIdentity()
			}
			var reply *rpc.BlockTemplateReply
			if v.err == nil {
				if v.bit == 1 {
					reply, v.err = c.GetDigibyteBlockTemplate()
				} else {
					reply, v.err = c.GetAuxiliaryBlockTemplate(v.symbol == "BELLS")
				}
			}
			if v.err == nil {
				v.tpl, v.err = reply.ToParentTemplate()
			}
			if v.err == nil {
				v.err = c.CheckTreasuryProposal(context.Background(), v.tpl, v.script, v.id)
			}
			done <- v
		}(view)
	}
	out := make([]forgeMultiView, 0, len(views))
	for range views {
		out = append(out, <-done)
	}
	return out
}

func (s *forgeSignerClient) signMulti(req forgepool.MultiSignRequest) ([]byte, error) {
	body, e := json.Marshal(req)
	if e != nil {
		return nil, e
	}
	if len(body) > 16*1024*1024 {
		return nil, errors.New("multi-chain proposal too large")
	}
	r, e := http.NewRequest("POST", s.url, bytes.NewReader(body))
	if e != nil {
		return nil, e
	}
	r.Header.Set("Content-Type", "application/json")
	resp, e := s.client.Do(r)
	if e != nil {
		return nil, e
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("signer HTTP %d", resp.StatusCode)
	}
	var out forgepool.TemplateSignResponse
	d := json.NewDecoder(io.LimitReader(resp.Body, 4097))
	if e = d.Decode(&out); e != nil {
		return nil, e
	}
	var extra interface{}
	if d.Decode(&extra) != io.EOF {
		return nil, errors.New("invalid signer response")
	}
	if !common.IsHexAddress(out.Signer) || common.HexToAddress(out.Signer) != s.address {
		return nil, errors.New("signer address mismatch")
	}
	sig, e := hex.DecodeString(strings.TrimPrefix(out.Signature, "0x"))
	if e != nil || len(sig) != 65 {
		return nil, errors.New("invalid signer signature")
	}
	digest, e := forgepool.MultiDigest(req.Template, req.WorkHash)
	if e != nil {
		return nil, e
	}
	pub, e := crypto.SigToPub(digest.Bytes(), sig)
	if e != nil || crypto.PubkeyToAddress(*pub) != s.address {
		return nil, errors.New("multi signature identity mismatch")
	}
	return sig, nil
}

func (rt *forgeRuntime) signMultiJob(job *forgeJob) error {
	if rt.allowedExternalMask != 0 && job.ExternalMask & ^rt.allowedExternalMask != 0 {
		return errors.New("job contains a chain excluded by the configured policy")
	}
	if !rt.multi || !forgepool.RevenueAnchorPresent(job.ExternalMask) || rt.templateQuorum == 0 {
		return errors.New("invalid multi-chain signing policy")
	}
	stamp, limit, e := forgeWorkTime(job.OGG)
	if e != nil {
		return e
	}
	span := uint64(rt.templateValidity / time.Second)
	if span > limit {
		span = limit
	}
	if span == 0 || stamp > uint64(1<<63-1)-span {
		return errors.New("invalid multi-chain validity")
	}
	t, e := job.Digibyte.MultiTemplate(stamp, stamp+span)
	if e != nil {
		return e
	}
	req := forgepool.MultiSignRequest{WorkHash: common.HexToHash(job.OGG.WorkHash), Template: t, AuxNonce: job.Digibyte.AuxNonce}
	if job.ExternalMask&1 != 0 {
		parent := job.Digibyte.Template
		req.DGB = &parent
	}
	for _, ref := range t.Aux {
		c := job.Digibyte.AdditionalAux[ref.ChainID]
		if ref.ChainID == 98 {
			c = job.Digibyte.DOGE
		}
		if c == nil {
			return errors.New("missing native evidence")
		}
		req.Aux = append(req.Aux, forgepool.MultiEvidence{ChainID: ref.ChainID, Candidate: c.Template})
	}
	signed := &forgeSignedTemplate{Multi: &t, Template: forgepool.ExternalTemplate{ValidFrom: stamp, Expires: stamp + span}, WorkHash: job.OGG.WorkHash, Timestamp: stamp, MaxValidity: limit}
	for _, signer := range rt.signers {
		if !signed.fresh(forgeNow(rt.clock)) {
			return errors.New("multi-chain authorization expired")
		}
		sig, err := signer.signMulti(req)
		if err == nil {
			signed.Signatures = append(signed.Signatures, sig)
		}
		if uint64(len(signed.Signatures)) >= rt.templateQuorum {
			break
		}
	}
	if t.Version == 4 {
		signed.Signatures, e = forgepool.CanonicalMultiSignatures(t, req.WorkHash, signed.Signatures)
		if e != nil {
			return e
		}
	}
	if e = signed.validate(job, rt.authorized, rt.templateQuorum, forgeNow(rt.clock)); e != nil {
		return e
	}
	if t.Version == 4 {
		if e = job.Digibyte.BindCertificate(t, signed.Signatures); e != nil {
			return e
		}
	}
	job.Signed = signed
	return nil
}

func (s *ProxyServer) fetchMultiTemplate() bool {
	rt := s.forge
	if rt.account.workPaused() {
		return false
	}
	rt.refreshMu.Lock()
	defer rt.refreshMu.Unlock()
	issuer := s.rpc()
	ogg, e := issuer.CreateOGGAuxBlock()
	if e != nil {
		s.markSick()
		return false
	}
	height, e := parseHexUint64(ogg.Height)
	if e != nil || height == 0 || height > 1<<53-1 {
		s.markSick()
		return false
	}
	difficulty, e := parseHexUint64(ogg.Difficulty)
	if e != nil || difficulty == 0 || difficulty > uint64(1<<63-1) {
		s.markSick()
		return false
	}
	stamp, _, e := forgeWorkTime(ogg)
	if e != nil {
		s.markSick()
		return false
	}
	s.markOk()
	views := rt.multiViews()
	var actual, required uint8
	for _, v := range views {
		h := rt.multiHealth[v.bit]
		if v.err == nil {
			h.success()
			actual |= v.bit
		} else {
			h.failure()
		}
		if h.required() {
			required |= v.bit
		}
	}
	if !forgepool.RevenueAnchorPresent(actual) {
		// This malformed request can advance independent health observations only;
		// the signer's key is unreachable for an empty participation mask.
		for _, signer := range rt.signers {
			_, _ = signer.signMulti(forgepool.MultiSignRequest{})
		}
		rt.clearExternalJobs()
		s.disconnectForgeMinersForPause()
		log.Printf("FORGE V3: no eligible DGB or DOGE anchor; OGG work paused")
		return false
	}
	seq := atomic.AddUint64(&rt.jobSeq, 1)
	var parent forgepool.ParentTemplate
	script := []byte{0x6a}
	aux := map[uint32]*forgepool.DogeCandidate{}
	symbols := map[string]uint32{}
	for _, v := range views {
		if v.err != nil {
			continue
		}
		if v.bit == 1 {
			parent = v.tpl
			script = v.script
			continue
		}
		c, err := forgepool.BuildAuxCandidate(v.tpl, v.script, uint32(seq), v.id)
		if err != nil {
			return false
		}
		aux[v.id] = c
		if v.bit != 2 {
			symbols[v.symbol] = v.id
		}
	}
	if actual&1 == 0 {
		parent, e = forgepool.SyntheticParentTemplate(1, uint32(stamp))
		if e != nil {
			return false
		}
	}
	builder := forgepool.BuildMultiParent
	if ogg.ProofVersion != "" {
		version, versionErr := hexutil.DecodeUint64(ogg.ProofVersion)
		if versionErr != nil || (version != 3 && version != 4) {
			return false
		}
		if version == 4 {
			builder = forgepool.BuildMultiParentV4
		}
	}
	native, e := builder(parent, ogg.WorkHash, aux, script, uint32(seq), 4, 4, actual&1 != 0)
	if e != nil {
		return false
	}
	job := &forgeJob{ProtocolV3: true, AuxSymbols: symbols, ID: fmt.Sprintf("%08x", seq), Created: forgeNow(rt.clock), Height: height, Difficulty: int64(difficulty), OGG: ogg, Digibyte: native, ExternalMask: actual, OGGRPC: issuer}
	if e = rt.signJob(job); e != nil {
		log.Printf("FORGE V3: independent authorization pending mask=%#x: %v", actual, e)
		return false
	}
	if actual != required {
		log.Printf("FORGE V3: health convergence pending required=%#x available=%#x; tentative authorization discarded", required, actual)
		return false
	}
	rt.mu.Lock()
	if !job.Signed.fresh(forgeNow(rt.clock)) || rt.account.workPaused() {
		rt.mu.Unlock()
		return false
	}
	old := rt.jobs[rt.currentID]
	if old == nil || !old.Signed.fresh(forgeNow(rt.clock)) || rt.jobNeedsClean(old, job) {
		job.Clean = true
		rt.jobs = map[string]*forgeJob{}
		rt.order = nil
		rt.duplicates = map[string]struct{}{}
	}
	rt.jobs[job.ID] = job
	rt.order = append(rt.order, job.ID)
	rt.currentID = job.ID
	for len(rt.order) > rt.jobBacklog {
		drop := rt.order[0]
		rt.order = rt.order[1:]
		delete(rt.jobs, drop)
		for k := range rt.duplicates {
			if strings.HasPrefix(k, drop+":") {
				delete(rt.duplicates, k)
			}
		}
	}
	rt.mu.Unlock()
	log.Printf("FORGE: new V%d Scrypt job %s OGG height=%d mask=%#x clean=%v", job.Signed.Multi.Version, job.ID, height, actual, job.Clean)
	if s.config.Proxy.Stratum.Enabled {
		go s.broadcastForgeJobs()
	}
	return true
}

func forgeEncodeJobProof(job *forgeJob, sol *forgeSolution) ([]byte, error) {
	if job.ProtocolV3 {
		if job.Signed == nil || job.Signed.Multi == nil {
			return nil, errors.New("missing multi-chain authorization")
		}
		p, e := job.Digibyte.BuildMultiProof(sol.ExtraNonce1, sol.ExtraNonce2, sol.NTime, sol.Nonce, *job.Signed.Multi, job.Signed.Signatures)
		if e != nil {
			return nil, e
		}
		return forgepool.EncodeMultiProof(p)
	}
	p, e := job.Digibyte.BuildOGGProof(sol.ExtraNonce1, sol.ExtraNonce2, sol.NTime, sol.Nonce, nil, job.Signed.Template, job.Signed.Signatures)
	if e != nil {
		return nil, e
	}
	return forgepool.EncodeOGGProof(p)
}

func forgeMultiCompatible(a, b *forgeJob) bool {
	if !a.ProtocolV3 || !b.ProtocolV3 || a.OGG == nil || b.OGG == nil || a.Digibyte == nil || b.Digibyte == nil || a.Signed == nil || b.Signed == nil || a.Signed.Multi == nil || b.Signed.Multi == nil || !forgepool.RevenueAnchorPresent(a.ExternalMask) || a.ExternalMask != b.ExternalMask || a.OGGRPC != b.OGGRPC || !forgeRetentionHash(a.OGG.PreviousBlockHash) || !forgeRetentionHash(b.OGG.PreviousBlockHash) {
		return false
	}
	if a.Signed.Multi.Version != b.Signed.Multi.Version {
		return false
	}
	for _, job := range []*forgeJob{a, b} {
		if job.Signed.Multi.Mask != job.ExternalMask || forgepool.MultiTemplateValid(*job.Signed.Multi) != nil {
			return false
		}
	}
	if a.ExternalMask&1 != 0 && (a.Digibyte.Template.Previous != b.Digibyte.Template.Previous || a.Digibyte.Template.Height != b.Digibyte.Template.Height || a.Digibyte.Template.Bits != b.Digibyte.Template.Bits) {
		return false
	}
	candidates := func(j *forgeJob) map[uint32]*forgepool.DogeCandidate {
		out := map[uint32]*forgepool.DogeCandidate{}
		for id, c := range j.Digibyte.AdditionalAux {
			out[id] = c
		}
		if j.Digibyte.DOGE != nil {
			out[98] = j.Digibyte.DOGE
		}
		return out
	}
	aa, bb := candidates(a), candidates(b)
	if len(aa) != len(bb) {
		return false
	}
	for id, c := range aa {
		d := bb[id]
		if c == nil || d == nil || c.Template.Previous != d.Template.Previous || c.Template.Height != d.Template.Height || c.Bits != d.Bits {
			return false
		}
	}
	return true
}
