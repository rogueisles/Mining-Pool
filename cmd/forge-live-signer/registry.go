package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"github.com/HawyUkr/open-progpow-pool/forgepool"
	"github.com/HawyUkr/open-progpow-pool/rpc"
	"github.com/HawyUkr/open-progpow-pool/storage"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rlp"
	"strings"
	"time"
)

type treasuryDestination struct {
	Script  string `json:"script"`
	Address string `json:"address,omitempty"`
}

func validateFlexibleConfig(c config) error {
	if c.RegistryBlock == 0 || c.RegistryChainID == 0 || c.OptionalSOQBlock != 0 || len(c.Multi) < 1 || len(c.Multi) > forgepool.RegistryMaxMembers || len(c.TreasuryDestinations) > 32 {
		return errors.New("invalid bounded registry signer configuration")
	}
	if err := c.RegistryRPC.validate(); err != nil {
		return err
	}
	seen := map[uint32]bool{}
	symbols := map[string]bool{}
	for _, ep := range c.Multi {
		for symbol, id := range multiIDs {
			if (ep.Symbol == symbol) != (ep.ChainID == id) {
				return errors.New("registry conventional symbol and chain identity differ")
			}
		}
		if seen[ep.ChainID] || symbols[ep.Symbol] || ep.ChainID > 65535 || ep.ChainID == forgepool.OGGAuxChainID {
			return errors.New("duplicate or unsupported registry endpoint")
		}
		if ep.Symbol == "DGB" {
			if ep.ChainID != 0 {
				return errors.New("DGB parent identity mismatch")
			}
		} else if ep.Symbol == "DOGE" {
			if ep.ChainID != 98 {
				return errors.New("DOGE identity mismatch")
			}
		} else if ep.Symbol == "SOQ" {
			if ep.ChainID != forgepool.SOQAuxChainID {
				return errors.New("SOQ identity mismatch")
			}
		} else if ep.Symbol != "PEP" && ep.Symbol != "DINGO" && ep.Symbol != "BELLS" && !storage.ValidRegisteredSymbol(ep.Symbol) {
			return errors.New("invalid registered endpoint symbol")
		}
		if err := ep.endpoint.validate(); err != nil {
			return err
		}
		script, err := hex.DecodeString(ep.PayoutScript)
		if err != nil || len(script) < 4 || len(script) > 100 || script[0] == 0x6a {
			return errors.New("invalid registered treasury script")
		}
		seen[ep.ChainID] = true
		symbols[ep.Symbol] = true
	}
	for _, d := range c.TreasuryDestinations {
		script, err := hex.DecodeString(d.Script)
		if err != nil || len(script) < 4 || len(script) > 100 || script[0] == 0x6a {
			return errors.New("invalid alternate treasury script")
		}
	}
	return nil
}
func (s *multiService) validateAndSignFlexible(ctx context.Context, req forgepool.MultiSignRequest) ([]byte, error) {
	if s.cfg.ExternalChainPolicy != forgepool.ExternalChainPolicyRegistry || req.Registry == nil || req.WorkHash == (common.Hash{}) || req.Template.Version != 5 || forgepool.MultiTemplateValid(req.Template) != nil {
		return nil, errors.New("registry proposal requires explicit V5 signer policy")
	}
	var fields []rlp.RawValue
	if len(req.RGNHeader) > 4096 || rlp.DecodeBytes(req.RGNHeader, &fields) != nil || len(fields) != 15 {
		return nil, errors.New("invalid registry work header")
	}
	var parent common.Hash
	var height uint64
	if rlp.DecodeBytes(fields[0], &parent) != nil || rlp.DecodeBytes(fields[8], &height) != nil {
		return nil, errors.New("invalid registry header ancestry")
	}
	if err := forgepool.ValidateFlexibleWorkHeader(req.RGNHeader, req.WorkHash, parent, req.Template.ValidFrom, height, s.cfg.RegistryBlock); err != nil {
		return nil, err
	}
	rgn, err := rpc.NewValidationRPCClient(ctx, "RGN-registry", s.cfg.RegistryRPC.URL, "4s", s.cfg.RegistryRPC.Username, s.cfg.RegistryRPC.Password)
	if err != nil {
		return nil, err
	}
	state, err := rgn.ForgeRegistry(parent)
	if err != nil {
		return nil, err
	}
	snapshot, policy, parentHeight, activation, err := state.Decode()
	_ = snapshot
	if err != nil || activation != s.cfg.RegistryBlock || parentHeight+1 != height || strings.TrimPrefix(state.Genesis.Hex(), "0x") != s.cfg.RegistryRPC.Genesis || policy.ChainID.Uint64() != s.cfg.RegistryChainID || policy.ChainID.BitLen() > 64 {
		return nil, errors.New("trusted RGN registry context mismatch")
	}
	own := crypto.PubkeyToAddress(s.key.PublicKey)
	authorized := false
	for _, a := range policy.WorkSigners {
		authorized = authorized || a == own
	}
	if !authorized {
		return nil, errors.New("signer is not authorized by trusted RGN node")
	}
	view, err := forgepool.ValidateRegistryEnvelope(state.Root, height, req.Registry, policy)
	if err != nil {
		return nil, err
	}
	// Public alternate destinations permit future rotations; no administrator
	// private key or treasury spending key is held in the mining service.
	scripts := map[common.Hash]treasuryDestination{}
	for _, ep := range s.cfg.Multi {
		script, _ := hex.DecodeString(ep.PayoutScript)
		scripts[common.Hash(sha256.Sum256(script))] = treasuryDestination{ep.PayoutScript, ep.PayoutAddress}
	}
	for _, d := range s.cfg.TreasuryDestinations {
		script, _ := hex.DecodeString(d.Script)
		scripts[common.Hash(sha256.Sum256(script))] = d
	}
	cfg := s.cfg
	cfg.Multi = nil
	selected := map[uint32]forgepool.RegistryCoin{}
	var mask uint8
	for _, coin := range view.Coins {
		hash, e := forgepool.RegistryTreasury(coin, height)
		if e != nil {
			return nil, e
		}
		destination, ok := scripts[hash]
		if !ok {
			return nil, errors.New("approved treasury public script is not configured")
		}
		found := false
		for _, ep := range s.cfg.Multi {
			if ep.ChainID == coin.ChainID && ep.Genesis == strings.TrimPrefix(coin.Genesis.Hex(), "0x") {
				ep.PayoutScript = destination.Script
				ep.PayoutAddress = destination.Address
				cfg.Multi = append(cfg.Multi, ep)
				found = true
				break
			}
		}
		if !found {
			return nil, errors.New("approved native endpoint is not configured")
		}
		selected[coin.ChainID] = coin
		switch coin.Slot {
		case 1:
			mask |= 1
		case 2:
			mask |= 2
		case 3:
			mask |= forgepool.ExternalSOQ
		}
	}
	if mask != req.Template.Mask || len(selected) != len(req.Template.Aux)+int(mask&1) || len(req.Aux) != len(req.Template.Aux) {
		return nil, errors.New("registry membership and native template differ")
	}
	validator := &multiService{cfg: cfg}
	views := validator.views(ctx, true)
	now := uint64(time.Now().Unix())
	t := req.Template
	if t.ValidFrom > now || now-t.ValidFrom > 300 || t.Expires <= now {
		return nil, errors.New("registry work expired")
	}
	evidence := map[uint32]forgepool.ParentTemplate{}
	for _, entry := range req.Aux {
		if _, ok := evidence[entry.ChainID]; ok || len(entry.Candidate.Transactions) > 20000 {
			return nil, errors.New("invalid bounded native evidence")
		}
		evidence[entry.ChainID] = entry.Candidate
	}
	candidates := map[uint32]*forgepool.DogeCandidate{}
	for _, ref := range t.Aux {
		var v multiView
		found := false
		for _, entry := range views {
			if entry.ep.ChainID == ref.ChainID {
				v = entry
				found = true
				break
			}
		}
		if !found || v.err != nil || v.reply == nil {
			return nil, errors.New("registered native backend unavailable")
		}
		native, ok := evidence[ref.ChainID]
		if !ok || native.Previous != v.tpl.Previous || native.Bits != v.tpl.Bits || native.Height != v.tpl.Height || native.Version&0xffff != v.tpl.Version&0xffff || int64(native.CurTime) < v.reply.MinTime || uint64(native.CurTime) > now+120 || now > uint64(native.CurTime)+300 {
			return nil, errors.New("registered candidate differs from independent native tip")
		}
		script, _ := hex.DecodeString(v.ep.PayoutScript)
		coin := selected[ref.ChainID]
		if coin.Profile == forgepool.RegistryProfileSOQ && !forgepool.ValidSOQScript(script) {
			return nil, errors.New("SOQ witness-v1 destination required")
		}
		candidate, e := forgepool.BuildAuxCandidate(native, script, req.AuxNonce, ref.ChainID)
		if e != nil {
			return nil, e
		}
		if common.HexToHash(candidate.DisplayHash) != ref.Hash {
			return nil, errors.New("registered candidate treasury or body differs")
		}
		proposal, e := candidate.AuxValidationBlock()
		if e != nil {
			return nil, e
		}
		incremented, e := candidate.TreasuryClaimProbe(ref.ChainID, script)
		if e != nil {
			return nil, e
		}
		if e = v.client.ValidateFullTreasuryClaim(ctx, proposal, incremented); e != nil {
			return nil, e
		}
		candidates[ref.ChainID] = candidate
	}
	var native forgepool.ParentTemplate
	script := []byte{0x6a}
	if mask&1 != 0 {
		v, ok := views["DGB"]
		if !ok || v.err != nil || req.DGB == nil {
			return nil, errors.New("DGB registry parent unavailable")
		}
		native = *req.DGB
		if native.Previous != v.tpl.Previous || native.Bits != v.tpl.Bits || native.Height != v.tpl.Height || native.Version != v.tpl.Version || int64(native.CurTime) < v.reply.MinTime || uint64(native.CurTime) > now+120 || now > uint64(native.CurTime)+300 {
			return nil, errors.New("DGB registry parent differs from trusted tip")
		}
		script, _ = hex.DecodeString(v.ep.PayoutScript)
	} else {
		if req.DGB != nil {
			return nil, errors.New("inactive registry DGB evidence")
		}
		native, err = forgepool.SyntheticParentTemplate(1, uint32(t.ValidFrom))
		if err != nil {
			return nil, err
		}
	}
	job, err := forgepool.BuildMultiParentV5(native, req.WorkHash.Hex(), candidates, script, req.AuxNonce, 4, 4, mask&1 != 0)
	if err != nil {
		return nil, err
	}
	if err = job.BindRegistry(req.Registry); err != nil {
		return nil, err
	}
	rebuilt, err := job.MultiTemplate(t.ValidFrom, t.Expires)
	if err != nil || !forgepool.MultiTemplatesEqual(rebuilt, t) {
		return nil, errors.New("registry parent body differs from validated work")
	}
	if mask&1 != 0 {
		block, e := job.FullBlock(make([]byte, 4), make([]byte, 4), native.CurTime, 0)
		if e != nil {
			return nil, e
		}
		probe, e := job.TreasuryClaimProbe(make([]byte, 4), make([]byte, 4), native.CurTime, 0)
		if e != nil {
			return nil, e
		}
		if e = views["DGB"].client.ValidateFullTreasuryClaim(ctx, block, probe); e != nil {
			return nil, e
		}
	}
	work, err := forgepool.MultiRequestWork(req)
	if err != nil {
		return nil, err
	}
	digest, err := forgepool.MultiDigest(t, work)
	if err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	signature, err := crypto.Sign(digest.Bytes(), s.key)
	if err != nil {
		return nil, err
	}
	for _, v := range views {
		var tip string
		if err = v.ep.call(ctx, "getbestblockhash", []interface{}{}, &tip); err != nil {
			return nil, err
		}
		if tip != forgepool.DisplayHash(v.tpl.Previous) {
			return nil, errors.New("registered native tip changed during signing")
		}
	}
	// Recheck the exact RGN parent after native validation, so approval context
	// cannot be silently substituted while this signing request is in flight.
	final, err := rgn.ForgeRegistry(parent)
	if err != nil || final.Root != state.Root || final.Checkpoint != state.Checkpoint || !bytes.Equal(final.Genesis[:], state.Genesis[:]) {
		return nil, errors.New("RGN registry ancestry changed during signing")
	}
	return signature, nil
}
