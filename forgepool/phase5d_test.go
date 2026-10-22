package forgepool

import (
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

func phase5DJob(t *testing.T) *DigibyteJob {
	t.Helper()
	doge, err := BuildDogeCandidate(testTemplate(200, false, false), []byte{0x51}, 7)
	if err != nil {
		t.Fatal(err)
	}
	job, err := BuildDigibyteJob(testTemplate(300, false, false), "0x"+strings.Repeat("11", 32), doge, []byte{0x51}, 9, 4, 4)
	if err != nil {
		t.Fatal(err)
	}
	return job
}

func TestDogeAuxPowExactSerializationVector(t *testing.T) {
	job := phase5DJob(t)
	p, err := job.BuildDogeAuxPow([]byte{1, 2, 3, 4}, []byte{5, 6, 7, 8}, job.Template.CurTime+1, 0x11223344)
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.Serialize()
	if err != nil {
		t.Fatal(err)
	}
	// This fixed vector follows Dogecoin Core CAuxPow serialization order.
	want := "02000000010000000000000000000000000000000000000000000000000000000000000000ffffffff38022c0151fabe6d6d7eeb900483249bd4625adc166a0280cdee47d765453d0e58682623c87d43003302000000090000000102030405060708ffffffff0100f2052a01000000015100000000fd7554ba8cb8e6d43854cb9bd863858ba5079bb65a593e7d1a1050f7e92a409d02856a4921cd32690244af7568e7bd1391a94119e17c7f33234f4bf11271b223e528f3ad33241b83d976331b756bbe95c5881d4df122e05df0432e4d918436fda60000000001111111111111111111111111111111111111111111111111111111111111111101000000000000200102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f205ad839130cf00e5c435488faf228b470016030b144c2b7c8fdfe8b3b7376439b01f15365ffff001d44332211"
	if hex.EncodeToString(got) != want {
		t.Fatalf("AuxPoW vector\nhave %s\nwant %s", hex.EncodeToString(got), want)
	}
}

func TestDogeAuxPowBindingRejectsInvalidProofs(t *testing.T) {
	job := phase5DJob(t)
	p, err := job.BuildDogeAuxPow([]byte{1, 2, 3, 4}, []byte{5, 6, 7, 8}, job.Template.CurTime+1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := job.ValidateDogeAuxPow(p); err != nil {
		t.Fatal(err)
	}
	badParent := *p
	badParent.ParentHeader = append([]byte(nil), p.ParentHeader...)
	badParent.ParentHeader[0] ^= 1
	if job.ValidateDogeAuxPow(&badParent) == nil {
		t.Fatal("invalid parent proof accepted")
	}
	badIndex := *p
	badIndex.ChainMerkleIndex ^= 1
	if job.ValidateDogeAuxPow(&badIndex) == nil {
		t.Fatal("wrong DOGE chain index accepted")
	}
	badCommit := *p
	badCommit.ParentCoinbase = append([]byte(nil), p.ParentCoinbase...)
	i := strings.Index(hex.EncodeToString(badCommit.ParentCoinbase), hex.EncodeToString(MergedMiningMagic))
	if i < 0 {
		t.Fatal("test commitment missing")
	}
	badCommit.ParentCoinbase[i/2] ^= 1
	if job.ValidateDogeAuxPow(&badCommit) == nil {
		t.Fatal("wrong coinbase commitment accepted")
	}
}

func TestOGGProofRoundTripAndSignerQuorum(t *testing.T) {
	job := phase5DJob(t)
	now := time.Unix(1700000000, 0)
	tmpl := ExternalTemplate{Version: OGGTemplateVersion, DGBPrevHash: common.HexToHash("0x01"), DGBBits: job.Template.Bits, DogeAuxHash: common.HexToHash("0x02"), ValidFrom: uint64(now.Unix() - 1), Expires: uint64(now.Unix() + 60)}
	k1, _ := crypto.GenerateKey()
	k2, _ := crypto.GenerateKey()
	bad, _ := crypto.GenerateKey()
	s1, _ := crypto.Sign(TemplateDigest(tmpl).Bytes(), k1)
	s2, _ := crypto.Sign(TemplateDigest(tmpl).Bytes(), k2)
	sBad, _ := crypto.Sign(TemplateDigest(tmpl).Bytes(), bad)
	authorized := []common.Address{crypto.PubkeyToAddress(k1.PublicKey), crypto.PubkeyToAddress(k2.PublicKey)}
	if err := ValidateTemplateSignatures(tmpl, [][]byte{s1, s2}, authorized, 2, now); err != nil {
		t.Fatal(err)
	}
	if ValidateTemplateSignatures(tmpl, [][]byte{s1}, authorized, 2, now) == nil {
		t.Fatal("insufficient quorum accepted")
	}
	if ValidateTemplateSignatures(tmpl, [][]byte{s1, s1}, authorized, 2, now) == nil {
		t.Fatal("duplicate signer accepted")
	}
	if ValidateTemplateSignatures(tmpl, [][]byte{s1, sBad}, authorized, 2, now) == nil {
		t.Fatal("unauthorized signer accepted")
	}
	if ValidateTemplateSignatures(tmpl, [][]byte{s1, s2}, authorized, 2, time.Unix(int64(tmpl.Expires+1), 0)) == nil {
		t.Fatal("expired template accepted")
	}
	p, err := job.BuildOGGProof([]byte{1, 2, 3, 4}, []byte{5, 6, 7, 8}, job.Template.CurTime+1, 9, nil, tmpl, [][]byte{s1, s2})
	if err != nil {
		t.Fatal(err)
	}
	blob, err := EncodeOGGProof(p)
	if err != nil {
		t.Fatal(err)
	}
	round, err := DecodeOGGProof(blob)
	if err != nil {
		t.Fatal(err)
	}
	if round.Template != tmpl || len(round.Signatures) != 2 || len(round.ParentHeader) != 80 {
		t.Fatal("OGGF proof roundtrip mismatch")
	}
}
