package forgepool

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/ethereum/go-ethereum/common"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rlp"
	"math/big"
	"os"
	"testing"
)

func TestOptionalSOQHeaderBindsHeightTimeAndWork(t *testing.T) {
	for _, height := range []uint64{1125599, 1125600, 1125601} {
		h := &optionalFixtureHeader{Number: new(big.Int).SetUint64(height), Difficulty: big.NewInt(1), Time: 1700000000}
		b, _ := rlp.EncodeToBytes(h)
		work := crypto.Keccak256Hash(b)
		if err := ValidateOptionalSOQHeader(b, work, h.Time, 1125600); (err == nil) != (height >= 1125600) {
			t.Fatal(height, err)
		}
		if ValidateOptionalSOQHeader(b, work, h.Time+1, 1125600) == nil {
			t.Fatal("timestamp substitution admitted")
		}
		if ValidateOptionalSOQHeader(b, common.HexToHash("0x1234"), h.Time, 1125600) == nil {
			t.Fatal("work substitution admitted")
		}
		if ValidateOptionalSOQHeader(b, work, h.Time, 0) == nil {
			t.Fatal("unconfigured activation admitted")
		}
	}
}

// Export synthetic native candidates for independent node-side RLP, certificate,
// activation and payout validation. These headers are not proof-of-work claims.
func TestOptionalSOQFixtureExport(t *testing.T) {
	type row struct {
		Mask             uint8
		Height           uint64
		HeaderRLP, Proof string
		Signers          []common.Address
	}
	var rows []row
	dgbScript, _ := hex.DecodeString("0014706267a5255e3666936bcb9a777667599f387546")
	dogeScript, _ := hex.DecodeString("76a9144b1508ebe06723842994adf33bdba36b4eba44c488ac")
	soqScript := append([]byte{0x51, 0x20}, bytes.Repeat([]byte{0x12}, 32)...)
	for _, height := range []uint64{1125599, 1125600, 1125601} {
		for _, mask := range []uint8{1, 2, 3, 32, 33, 34, 35} {
			h := &optionalFixtureHeader{ParentHash: common.HexToHash("0x1234"), UncleHash: common.HexToHash("0x1dcc4de8dec75d7aab85b567b6ccd41ad312451b948a7413f0a142fd40d49347"), Number: new(big.Int).SetUint64(height), Difficulty: big.NewInt(1), Time: 1700000000, GasLimit: 8000000}
			encoded, _ := rlp.EncodeToBytes(h)
			work := crypto.Keccak256Hash(encoded)
			p := ParentTemplate{Version: 4, Height: 300001, Bits: 0x207fffff, CurTime: uint32(h.Time), CoinbaseValue: 100000000}
			for i := range p.Previous {
				p.Previous[i] = 0x11
			}
			aux := map[uint32]*DogeCandidate{}
			for id, script := range map[uint32][]byte{98: dogeScript, SOQAuxChainID: soqScript} {
				if mask&MultiChainBit(id) == 0 {
					continue
				}
				c, e := BuildAuxCandidate(p, script, 17, id)
				if e != nil {
					t.Fatal(e)
				}
				aux[id] = c
			}
			parent := p
			script := dgbScript
			if mask&1 == 0 {
				parent, _ = SyntheticParentTemplate(1, uint32(h.Time))
				script = []byte{0x6a}
			}
			job, e := BuildMultiParentV4(parent, work.Hex(), aux, script, 17, 4, 4, mask&1 != 0)
			if e != nil {
				t.Fatal(e)
			}
			tmpl, e := job.MultiTemplate(h.Time, h.Time+300)
			if e != nil || tmpl.Mask != mask {
				t.Fatal(mask, e)
			}
			digest, e := MultiDigest(tmpl, work)
			if e != nil {
				t.Fatal(e)
			}
			var sigs [][]byte
			var signers []common.Address
			for _, n := range []int{1, 2} {
				key, _ := crypto.HexToECDSA(fmt.Sprintf("%064x", n))
				sig, e := crypto.Sign(digest[:], key)
				if e != nil {
					t.Fatal(e)
				}
				sigs = append(sigs, sig)
				signers = append(signers, crypto.PubkeyToAddress(key.PublicKey))
			}
			sigs, e = CanonicalMultiSignatures(tmpl, work, sigs)
			if e != nil {
				t.Fatal(e)
			}
			if e = job.BindCertificate(tmpl, sigs); e != nil {
				t.Fatal(e)
			}
			proof, e := job.BuildMultiProof(make([]byte, 4), make([]byte, 4), parent.CurTime, 0, tmpl, sigs)
			if e != nil {
				t.Fatal(e)
			}
			b, e := EncodeMultiProof(proof)
			if e != nil {
				t.Fatal(e)
			}
			rows = append(rows, row{mask, height, hex.EncodeToString(encoded), hex.EncodeToString(b), signers})
		}
	}
	if p := os.Getenv("RGN_SOQ_FIXTURES"); p != "" {
		b, _ := json.MarshalIndent(rows, "", "  ")
		if e := os.WriteFile(p, append(b, '\n'), 0644); e != nil {
			t.Fatal(e)
		}
	}
}

// Minimal RLP-compatible RGN header; avoids importing an unrelated geth database.
type optionalFixtureHeader struct {
	ParentHash, UncleHash     common.Hash
	Coinbase                  common.Address
	Root, TxHash, ReceiptHash common.Hash
	Bloom                     [256]byte
	Difficulty, Number        *big.Int
	GasLimit, GasUsed, Time   uint64
	Extra                     []byte
	MixDigest                 common.Hash
	Nonce                     [8]byte
}
