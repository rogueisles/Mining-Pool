package rpc

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/HawyUkr/open-progpow-pool/forgepool"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"math/big"
	"os"
	"strings"
	"testing"
)

func TestMultiV3NativeSingleChains(t *testing.T) {
	path := os.Getenv("FORGE_MULTI_ENDPOINTS")
	if path == "" {
		t.Skip("owned native regtest cohort required")
	}
	var urls map[string]string
	b, e := os.ReadFile(path)
	if e != nil || json.Unmarshal(b, &urls) != nil {
		t.Fatal("invalid endpoint fixture")
	}
	var issued struct {
		HeaderRLP, WorkHash, Target string
		Timestamp                   uint64
		Private                     bool `json:"privateCpuTest"`
	}
	b, e = os.ReadFile(os.Getenv("FORGE_MULTI_ISSUED_FIXTURE"))
	if e != nil || json.Unmarshal(b, &issued) != nil || !issued.Private {
		t.Fatal("private CPU work required")
	}
	target, ok := new(big.Int).SetString(strings.TrimPrefix(issued.Target, "0x"), 16)
	if !ok {
		t.Fatal("invalid private target")
	}
	scripts := map[string]string{"DGB": "0014706267a5255e3666936bcb9a777667599f387546", "DOGE": "76a9144b1508ebe06723842994adf33bdba36b4eba44c488ac", "PEP": "76a914bb0ff0797074c03e9969359b605997b53c9eaa8488ac", "DINGO": "76a914e306e2a1a240f634da707d31079f18431317d7af88ac", "BELLS": "0014088a288ee425094ccd67779afd0c2016d010368c"}
	ids := map[string]uint32{"DOGE": 98, "PEP": 63, "DINGO": 50, "BELLS": 16}
	key, _ := crypto.HexToECDSA(strings.Repeat("0", 63) + "1")
	var rows []map[string]interface{}
	for _, symbol := range []string{"DGB", "DOGE", "PEP", "DINGO", "BELLS"} {
		c := NewRPCClient(symbol, urls[symbol], "5s", "test", "test-only")
		info, e := c.doPost(c.Url, "getblockchaininfo", []interface{}{})
		if e != nil {
			t.Fatal(e)
		}
		var chain struct{ Chain string }
		if unmarshalRPCResult(info, &chain) != nil || chain.Chain != "regtest" {
			t.Fatal("refusing mainnet submission")
		}
		var reply *BlockTemplateReply
		if symbol == "DGB" {
			reply, e = c.GetDigibyteBlockTemplate()
		} else {
			reply, e = c.GetAuxiliaryBlockTemplate(symbol == "BELLS")
		}
		if e != nil {
			t.Fatal(e)
		}
		native, e := reply.ToParentTemplate()
		if e != nil {
			t.Fatal(e)
		}
		script, _ := hex.DecodeString(scripts[symbol])
		parent := native
		parentScript := script
		aux := map[uint32]*forgepool.DogeCandidate{}
		if symbol != "DGB" {
			aux[ids[symbol]], e = forgepool.BuildAuxCandidate(native, script, 27, ids[symbol])
			if e != nil {
				t.Fatal(e)
			}
			parent, e = forgepool.SyntheticParentTemplate(1, uint32(issued.Timestamp))
			if e != nil {
				t.Fatal(e)
			}
			parentScript = []byte{0x6a}
		}
		job, e := forgepool.BuildMultiParent(parent, issued.WorkHash, aux, parentScript, 27, 4, 4, symbol == "DGB")
		if e != nil {
			t.Fatal(e)
		}
		tmpl, e := job.MultiTemplate(issued.Timestamp, issued.Timestamp+300)
		if e != nil {
			t.Fatal(e)
		}
		digest, e := forgepool.MultiDigest(tmpl, common.HexToHash(issued.WorkHash))
		if e != nil {
			t.Fatal(e)
		}
		sig, e := crypto.Sign(digest.Bytes(), key)
		if e != nil {
			t.Fatal(e)
		}
		var nonce uint32
		for ; nonce < 2000000; nonce++ {
			h, e := job.Header(make([]byte, 4), make([]byte, 4), parent.CurTime, nonce)
			if e != nil {
				t.Fatal(e)
			}
			hash, e := forgepool.ScryptHash(h)
			if e != nil {
				t.Fatal(e)
			}
			nativeOK, e := forgepool.MeetsCompactTarget(hash, native.Bits)
			if e != nil {
				t.Fatal(e)
			}
			if nativeOK && forgepool.HashValue(hash).Cmp(target) <= 0 {
				break
			}
		}
		if nonce == 2000000 {
			t.Fatal("private CPU budget exhausted")
		}
		proof, e := job.BuildMultiProof(make([]byte, 4), make([]byte, 4), parent.CurTime, nonce, tmpl, [][]byte{sig})
		if e != nil {
			t.Fatal(e)
		}
		encoded, e := forgepool.EncodeMultiProof(proof)
		if e != nil {
			t.Fatal(e)
		}
		var block []byte
		var hash string
		if symbol == "DGB" {
			block, e = job.FullBlock(make([]byte, 4), make([]byte, 4), parent.CurTime, nonce)
			a := sha256.Sum256(proof.ParentHeader)
			h := sha256.Sum256(a[:])
			hash = forgepool.DisplayHash(forgepool.Hash(h))
		} else {
			block, e = job.AuxBlockFor(ids[symbol], make([]byte, 4), make([]byte, 4), parent.CurTime, nonce)
			hash = aux[ids[symbol]].DisplayHash
		}
		if e != nil {
			t.Fatal(e)
		}
		if _, e = c.doPost(c.Url, "submitblock", []interface{}{hex.EncodeToString(block)}); e != nil {
			t.Fatal(e)
		}
		observation, e := c.ForgeBlockObservation("0x" + hash)
		if e != nil || observation != "canonical" {
			t.Fatal(symbol, "single-chain native block not canonical", observation, e)
		}
		rows = append(rows, map[string]interface{}{"symbol": symbol, "mask": tmpl.Mask, "proof": hex.EncodeToString(encoded), "headerRLP": issued.HeaderRLP, "privateCpuTest": true})
		t.Log(symbol, "single-chain native block canonical; OGG proof exported")
	}
	b, _ = json.Marshal(rows)
	if e = os.WriteFile(os.Getenv("FORGE_MULTI_PROOF_FIXTURE")+".singles", b, 0600); e != nil {
		t.Fatal(e)
	}
}
