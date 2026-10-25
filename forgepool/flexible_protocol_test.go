package forgepool

import (
	"bytes"
	"crypto/sha256"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"testing"
)

func TestFlexibleCertificateEnvelopeBindingAndBudget(t *testing.T) {
	snapshot, ctx, _ := registryTestFixture()
	script := []byte{0x76, 0xa9, 0x14, 1, 2, 3, 4, 5, 6, 7, 8, 9, 0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 0x88, 0xac}
	for i := range snapshot.Coins {
		snapshot.Coins[i].Treasury = common.Hash(sha256.Sum256(script))
	}
	snapshot.Coins = append(snapshot.Coins, RegistryCoin{Slot: 4, ChainID: 99, Genesis: registryTestHash(109), Profile: RegistryProfileBitcoin80, Treasury: common.Hash(sha256.Sum256(script))})
	for _, mask := range []uint8{1, 2, 3, 32, 33, 34, 35} {
		parent := ParentTemplate{Version: 4, Height: 100, Previous: Hash{0x11}, CurTime: 1700000000, Bits: 0x207fffff, CoinbaseValue: 5000000}
		aux := map[uint32]*DogeCandidate{}
		slots := []uint8{4}
		if mask&1 != 0 {
			slots = append(slots, 1)
		}
		for _, entry := range []struct {
			id   uint32
			bit  uint8
			slot uint8
		}{{98, 2, 2}, {21329, 32, 3}, {99, 0, 4}} {
			if entry.bit != 0 && mask&entry.bit == 0 {
				continue
			}
			candidate, err := BuildAuxCandidate(parent, script, 7, entry.id)
			if err != nil {
				t.Fatal(err)
			}
			aux[entry.id] = candidate
			if entry.slot != 4 {
				slots = append(slots, entry.slot)
			}
		}
		payout := script
		if mask&1 == 0 {
			parent, _ = SyntheticParentTemplate(1, 1700000000)
			payout = []byte{0x6a}
		}
		env := registryTestEnvelope(t, snapshot, slots...)
		if _, err := ValidateRegistryEnvelope(env.Root, 100, env, ctx); err != nil {
			t.Fatal(err)
		}
		work := crypto.Keccak256Hash([]byte{mask})
		job, err := BuildMultiParentV5(parent, work.Hex(), aux, payout, 7, 4, 4, mask&1 != 0)
		if err != nil {
			t.Fatal(err)
		}
		if err = job.BindRegistry(env); err != nil {
			t.Fatal(err)
		}
		template, err := job.MultiTemplate(1700000000, 1700000300)
		if err != nil || template.Mask != mask {
			t.Fatal("incorrect bounded core mask", err)
		}
		bound, err := FlexibleSigningWork(work, env)
		if err != nil {
			t.Fatal(err)
		}
		digest, err := MultiDigest(template, bound)
		if err != nil {
			t.Fatal(err)
		}
		keys := registryTestKeys(21)
		var sigs [][]byte
		for _, key := range keys[:2] {
			sig, e := crypto.Sign(digest[:], key)
			if e != nil {
				t.Fatal(e)
			}
			sigs = append(sigs, sig)
		}
		sigs, err = CanonicalMultiSignatures(template, bound, sigs)
		if err != nil {
			t.Fatal(err)
		}
		if err = ValidateMultiSignatures(template, bound, sigs, ctx.WorkSigners, 2, 1700000000); err != nil {
			t.Fatal(err)
		}
		if err = job.BindCertificate(template, sigs); err != nil {
			t.Fatal(err)
		}
		proof, err := job.BuildMultiProof(make([]byte, 4), make([]byte, 4), parent.CurTime, 0, template, sigs)
		if err != nil {
			t.Fatal(err)
		}
		blob, err := EncodeFlexibleProof(&FlexibleProof{Multi: *proof, Registry: *env})
		if err != nil {
			t.Fatal(err)
		}
		if len(blob) > 8*1024 {
			t.Fatalf("ordinary V5 proof unexpectedly heavy: %d", len(blob))
		}
		t.Logf("core mask %d, %d native sources, %d proof bytes", mask, len(slots), len(blob))
		decoded, err := DecodeOGGProof(blob)
		if err != nil || decoded.Flexible == nil || decoded.Multi.Template.Version != 5 {
			t.Fatal("V5 wire mismatch", err)
		}
		if _, err = EncodeMultiProof(proof); err == nil {
			t.Fatal("legacy envelope accepted V5")
		}
		changed := *env
		changed.Checkpoint++
		wrong, err := FlexibleSigningWork(work, &changed)
		if err != nil {
			t.Fatal(err)
		}
		if ValidateMultiSignatures(template, wrong, sigs, ctx.WorkSigners, 2, 1700000000) == nil {
			t.Fatal("registry replay not bound to work")
		}
		if !bytes.Contains(job.Coinbase.Coinb2, []byte("RGNCERT5")) {
			t.Fatal("V5 mined commitment tag missing")
		}
	}
	if _, err := DecodeFlexibleProof(make([]byte, MaxOGGProofSize+1)); err == nil {
		t.Fatal("oversized proof accepted")
	}
}
