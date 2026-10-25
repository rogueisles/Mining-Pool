package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/HawyUkr/open-progpow-pool/forgepool"
	"github.com/HawyUkr/open-progpow-pool/rpc"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"io"
	"io/ioutil"
	"log"
	"os"
	"sort"
	"strings"
	"sync/atomic"
	"time"
)

var registryOperationLastNote int64

func registryOperationNote(reason string) {
	now := time.Now().Unix()
	last := atomic.LoadInt64(&registryOperationLastNote)
	if now-last >= 60 && atomic.CompareAndSwapInt64(&registryOperationLastNote, last, now) {
		log.Printf("FORGE registry administrator operation %s; continuing existing approved mining", reason)
	}
}
func registryPendingOperation(path string, snapshot forgepool.RegistrySnapshot, height uint64, ctx forgepool.RegistryContext) (*forgepool.RegistryOperation, forgepool.RegistrySnapshot) {
	if path == "" {
		return nil, snapshot
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 65536 {
		registryOperationNote("file unavailable or oversized")
		return nil, snapshot
	}
	f, err := os.Open(path)
	if err != nil {
		registryOperationNote("file unavailable")
		return nil, snapshot
	}
	defer f.Close()
	raw, err := ioutil.ReadAll(io.LimitReader(f, 65537))
	if err != nil || len(raw) > 65536 {
		registryOperationNote("file read failed")
		return nil, snapshot
	}
	var op forgepool.RegistryOperation
	if json.Unmarshal(raw, &op) != nil {
		registryOperationNote("JSON invalid")
		return nil, snapshot
	}
	before, err := forgepool.RegistrySnapshotRoot(op.Before)
	current, e := forgepool.RegistrySnapshotRoot(snapshot)
	if err != nil || e != nil || before != current {
		if op.Change.Sequence > snapshot.Admin.Sequence {
			registryOperationNote("ancestry stale")
		}
		return nil, snapshot
	}
	after, err := forgepool.RegistryApplyChange(snapshot, height, op.Change, ctx)
	if err != nil {
		registryOperationNote("change invalid or not yet eligible")
		return nil, snapshot
	}
	root, err := forgepool.RegistrySnapshotRoot(after)
	if err != nil {
		registryOperationNote("result invalid")
		return nil, snapshot
	}
	// The signed change binds the parent state and future activation, while
	// inclusion height remains free until a miner actually finds its block.
	op.ResultRoot = root
	return &op, after
}
func (rt *forgeRuntime) flexibleViews(snapshot forgepool.RegistrySnapshot, height uint64, coreOnly ...bool) []forgeMultiView {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	result := make(chan forgeMultiView, len(snapshot.Coins))
	count := 0
	for _, coin := range snapshot.Coins {
		if len(coreOnly) > 0 && coreOnly[0] && !coin.Core {
			continue
		}
		hash, err := forgepool.RegistryTreasury(coin, height)
		if err != nil {
			continue
		}
		script := rt.treasuryScripts[hash]
		if len(script) == 0 {
			continue
		}
		v := forgeMultiView{id: coin.ChainID, genesis: strings.TrimPrefix(coin.Genesis.Hex(), "0x"), script: script, address: rt.treasuryAddresses[hash], profile: coin.Profile}
		switch coin.Slot {
		case 1:
			v.symbol = "DGB"
			v.bit = 1
			v.client = rt.dgbRPC
		case 2:
			v.symbol = "DOGE"
			v.bit = 2
			v.client = rt.dogeRPC
		default:
			for _, a := range rt.additionalAux {
				if a.chainID == coin.ChainID && a.genesis == v.genesis {
					v.symbol = a.symbol
					v.client = a.client
				}
			}
			if coin.Slot == 3 {
				v.bit = forgepool.ExternalSOQ
			}
		}
		if v.client == nil || v.symbol == "" {
			continue
		}
		count++
		go func(v forgeMultiView) {
			c, e := rpc.NewValidationRPCClient(ctx, v.symbol, v.client.Url, "3s", v.client.Username, v.client.Password)
			v.err = e
			if v.err == nil {
				v.err = c.CheckAuxiliaryMainnet(v.genesis)
			}
			if v.err == nil && v.symbol == "DOGE" {
				v.err = c.CheckDogecoinIdentity()
			}
			if v.err == nil && v.profile == forgepool.RegistryProfileSOQ {
				v.err = c.ValidateSOQPayout(v.address, v.script)
			}
			var reply *rpc.BlockTemplateReply
			if v.err == nil {
				if v.id == 0 {
					reply, v.err = c.GetDigibyteBlockTemplate()
				} else {
					reply, v.err = c.GetAuxiliaryBlockTemplate(v.profile == forgepool.RegistryProfileSOQ || v.symbol == "BELLS")
				}
			}
			if v.err == nil {
				v.tpl, v.err = reply.ToParentTemplate()
			}
			if v.err == nil {
				v.err = c.CheckTreasuryProposal(ctx, v.tpl, v.script, v.id)
			}
			result <- v
		}(v)
	}
	views := make([]forgeMultiView, 0, count)
	for i := 0; i < count; i++ {
		select {
		case v := <-result:
			views = append(views, v)
		case <-ctx.Done():
			i = count
		}
	}
	sort.Slice(views, func(i, j int) bool { return views[i].id < views[j].id })
	return views
}
func registryEnvelope(snapshot forgepool.RegistrySnapshot, checkpoint uint64, op *forgepool.RegistryOperation, selected map[uint32]bool) (*forgepool.RegistryEnvelope, error) {
	root, err := forgepool.RegistrySnapshotRoot(snapshot)
	if err != nil {
		return nil, err
	}
	env := &forgepool.RegistryEnvelope{Root: root, Checkpoint: checkpoint}
	if op != nil {
		env.Operations = []forgepool.RegistryOperation{*op}
	}
	for _, coin := range snapshot.Coins {
		if selected[coin.ChainID] {
			m, e := forgepool.RegistryMembershipFor(snapshot, coin.Slot)
			if e != nil {
				return nil, e
			}
			env.Members = append(env.Members, m)
		}
	}
	sort.Slice(env.Members, func(i, j int) bool { return env.Members[i].Coin.ChainID < env.Members[j].Coin.ChainID })
	return env, nil
}
func (s *ProxyServer) fetchFlexibleTemplate(ogg *rpc.OGGAuxBlock, issuer *rpc.RPCClient, height, difficulty, stamp uint64) bool {
	rt := s.forge
	if rt.registryBlock == 0 || height < rt.registryBlock {
		return false
	}
	reply, err := issuer.ForgeRegistry(common.HexToHash(ogg.PreviousBlockHash))
	if err != nil {
		return false
	}
	snapshot, ctx, parentHeight, activation, err := reply.Decode()
	if err != nil || activation != rt.registryBlock || parentHeight+1 != height {
		return false
	}
	header, err := hexutil.Decode(ogg.HeaderRLP)
	if err != nil || forgepool.ValidateFlexibleWorkHeader(header, common.HexToHash(ogg.WorkHash), reply.ParentHash, stamp, height, activation) != nil {
		return false
	}
	allowed := map[common.Address]bool{}
	for _, a := range ctx.WorkSigners {
		allowed[a] = true
	}
	for _, a := range rt.authorized {
		if !allowed[a] {
			return false
		}
	}
	op, after := registryPendingOperation(rt.registryOperationFile, snapshot, height, ctx)
	coreOnly := false
	fallback := func() bool {
		if !coreOnly {
			coreOnly = true
			return true
		}
		if op != nil {
			op = nil
			after = snapshot
			return true
		}
		return false
	}
rebuildFlexible:
	checkpoint := ctx.ParentCheckpoint
	if op != nil {
		checkpoint = height
	}
	views := rt.flexibleViews(after, height, coreOnly)
	selected := map[uint32]bool{}
	var mask uint8
	for _, v := range views {
		if v.err == nil {
			selected[v.id] = true
			mask |= v.bit
		}
	}
	if coreOnly {
		for _, v := range views {
			if v.bit == 0 || (v.bit == forgepool.ExternalSOQ && mask&3 != 0) {
				delete(selected, v.id)
				mask &^= v.bit
			}
		}
	}
	if !forgepool.RevenueAnchorPresent(mask) {
		rt.clearExternalJobs()
		s.disconnectForgeMinersForPause()
		return false
	}
	// A bounded inclusion set contains core sources first; optional registered
	// sources never displace a functioning core source or block its recovery.
	coreCount := 0
	for id := range selected {
		if id == 0 || id == 98 || id == forgepool.SOQAuxChainID {
			coreCount++
		}
	}
	slots := forgepool.RegistryMaxMembers - coreCount
	for _, v := range views {
		if !selected[v.id] || v.bit != 0 {
			continue
		}
		if slots == 0 {
			delete(selected, v.id)
		} else {
			slots--
		}
	}
	if len(selected) > forgepool.RegistryMaxMembers {
		if fallback() {
			goto rebuildFlexible
		}
		return false
	}
	env, err := registryEnvelope(after, checkpoint, op, selected)
	if err != nil {
		if fallback() {
			goto rebuildFlexible
		}
		return false
	}
	if _, err = forgepool.ValidateRegistryEnvelope(reply.Root, height, env, ctx); err != nil {
		if op != nil {
			op = nil
			after = snapshot
			goto rebuildFlexible
		}
		log.Printf("FORGE V5 registry proposal refused: %v", err)
		return false
	}
	seq := atomic.AddUint64(&rt.jobSeq, 1)
	var parent forgepool.ParentTemplate
	script := []byte{0x6a}
	aux := map[uint32]*forgepool.DogeCandidate{}
	symbols := map[string]uint32{}
	for _, v := range views {
		if !selected[v.id] {
			continue
		}
		if v.id == 0 {
			parent = v.tpl
			script = v.script
			continue
		}
		candidate, e := forgepool.BuildAuxCandidate(v.tpl, v.script, uint32(seq), v.id)
		if e != nil {
			if fallback() {
				goto rebuildFlexible
			}
			return false
		}
		aux[v.id] = candidate
		if v.id != 98 {
			symbols[v.symbol] = v.id
		}
	}
	if mask&1 == 0 {
		parent, err = forgepool.SyntheticParentTemplate(1, uint32(stamp))
		if err != nil {
			return false
		}
	}
	native, err := forgepool.BuildMultiParentV5(parent, ogg.WorkHash, aux, script, uint32(seq), 4, 4, mask&1 != 0)
	if err != nil {
		if fallback() {
			goto rebuildFlexible
		}
		return false
	}
	if err = native.BindRegistry(env); err != nil {
		if fallback() {
			goto rebuildFlexible
		}
		return false
	}
	job := &forgeJob{Registry: env, RegistryContext: &ctx, ProtocolV3: true, AuxSymbols: symbols, ID: fmt.Sprintf("%08x", seq), Created: forgeNow(rt.clock), Height: height, Difficulty: int64(difficulty), OGG: ogg, Digibyte: native, ExternalMask: mask, OGGRPC: issuer}
	if err = rt.signJob(job); err != nil {
		if !coreOnly && len(selected) > 0 {
			coreOnly = true
			goto rebuildFlexible
		}
		if op != nil {
			op = nil
			after = snapshot
			goto rebuildFlexible
		}
		log.Printf("FORGE V5 authorization pending: %v", err)
		return false
	}
	// Prove the complete bounded wire size before miners receive this work.
	probe, e := native.BuildMultiProof(make([]byte, 4), make([]byte, 4), native.Template.CurTime, 0, *job.Signed.Multi, job.Signed.Signatures)
	if e == nil {
		_, e = forgepool.EncodeFlexibleProof(&forgepool.FlexibleProof{Multi: *probe, Registry: *env})
	}
	if e != nil {
		if fallback() {
			goto rebuildFlexible
		}
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
		for key := range rt.duplicates {
			if strings.HasPrefix(key, drop+":") {
				delete(rt.duplicates, key)
			}
		}
	}
	rt.mu.Unlock()
	s.markOk()
	log.Printf("FORGE V5 job %s RGN height=%d core=%#x native_sources=%d", job.ID, height, mask, len(selected))
	if s.config.Proxy.Stratum.Enabled {
		go s.broadcastForgeJobs()
	}
	return true
}
