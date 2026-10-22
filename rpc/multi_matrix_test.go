package rpc

import (
	"encoding/hex"
	"encoding/json"
	"github.com/HawyUkr/open-progpow-pool/forgepool"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"os"
	"strings"
	"testing"
)

func TestMultiV3ExportAllSubsets(t *testing.T) {
	path := os.Getenv("FORGE_MULTI_MATRIX_ISSUED")
	if path == "" {
		t.Skip("private matrix fixture required")
	}
	var v struct {
		HeaderRLP, WorkHash string
		Timestamp           uint64
		ChainID             int
		Private             bool `json:"privateCpuTest"`
	}
	raw, e := os.ReadFile(path)
	if e != nil || json.Unmarshal(raw, &v) != nil || !v.Private || v.ChainID != 7008902 {
		t.Fatal("invalid private matrix fixture")
	}
	scripts := map[uint32]string{0: "0014706267a5255e3666936bcb9a777667599f387546", 98: "76a9144b1508ebe06723842994adf33bdba36b4eba44c488ac", 63: "76a914bb0ff0797074c03e9969359b605997b53c9eaa8488ac", 50: "76a914e306e2a1a240f634da707d31079f18431317d7af88ac", 16: "0014088a288ee425094ccd67779afd0c2016d010368c"}
	key, e := crypto.HexToECDSA(strings.Repeat("0", 63) + "1")
	if e != nil {
		t.Fatal(e)
	}
	var rows []map[string]interface{}
	for mask := uint8(1); mask <= 31; mask++ {
		parent := forgepool.ParentTemplate{Version: 4, Previous: forgepool.Hash{1}, Height: 111, Bits: 0x207fffff, CurTime: uint32(v.Timestamp), CoinbaseValue: 100000000}
		script, _ := hex.DecodeString(scripts[0])
		if mask&1 == 0 {
			parent, e = forgepool.SyntheticParentTemplate(1, uint32(v.Timestamp))
			if e != nil {
				t.Fatal(e)
			}
			script = []byte{0x6a}
		}
		aux := map[uint32]*forgepool.DogeCandidate{}
		for _, id := range []uint32{16, 50, 63, 98} {
			if mask&forgepool.MultiChainBit(id) == 0 {
				continue
			}
			tpl := forgepool.ParentTemplate{Version: 4, Previous: forgepool.Hash{byte(id)}, Height: 111, Bits: 0x207fffff, CurTime: uint32(v.Timestamp), CoinbaseValue: 100000000}
			payout, _ := hex.DecodeString(scripts[id])
			aux[id], e = forgepool.BuildAuxCandidate(tpl, payout, 19, id)
			if e != nil {
				t.Fatal(e)
			}
		}
		job, e := forgepool.BuildMultiParent(parent, v.WorkHash, aux, script, 19, 4, 4, mask&1 != 0)
		if e != nil {
			t.Fatal(mask, e)
		}
		tmpl, e := job.MultiTemplate(v.Timestamp, v.Timestamp+300)
		if e != nil || tmpl.Mask != mask {
			t.Fatal(mask, e)
		}
		digest, e := forgepool.MultiDigest(tmpl, common.HexToHash(v.WorkHash))
		if e != nil {
			t.Fatal(e)
		}
		sig, e := crypto.Sign(digest.Bytes(), key)
		if e != nil {
			t.Fatal(e)
		}
		p, e := job.BuildMultiProof([]byte{1, 2, 3, 4}, []byte{5, 6, 7, 8}, parent.CurTime, 0, tmpl, [][]byte{sig})
		if e != nil {
			t.Fatal(e)
		}
		blob, e := forgepool.EncodeMultiProof(p)
		if e != nil {
			t.Fatal(e)
		}
		rows = append(rows, map[string]interface{}{"mask": mask, "proof": hex.EncodeToString(blob), "headerRLP": v.HeaderRLP})
	}
	if _, e = forgepool.BuildMultiParent(forgepool.ParentTemplate{}, v.WorkHash, nil, []byte{0x6a}, 19, 4, 4, false); e == nil {
		t.Fatal("OGG-only construction accepted")
	}
	out, _ := json.Marshal(rows)
	if e = os.WriteFile(os.Getenv("FORGE_MULTI_MATRIX_PROOFS"), out, 0600); e != nil {
		t.Fatal(e)
	}
	t.Log("exported all 31 nonempty chain subsets using explicit private-CPU fixture; no native wins claimed")
}
