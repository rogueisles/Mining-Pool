package rpc

import (
	"encoding/hex"
	"encoding/json"
	"github.com/HawyUkr/open-progpow-pool/forgepool"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"math/big"
	"os"
	"strings"
	"testing"
	"time"
)

func TestMultiNativeSharedParent(t *testing.T) {
	path := os.Getenv("FORGE_MULTI_ENDPOINTS")
	if path == "" {
		t.Skip("owned native regtest cohort required")
	}
	var urls map[string]string
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if json.Unmarshal(data, &urls) != nil {
		t.Fatal("invalid endpoint fixture")
	}
	var issued struct {
		HeaderRLP, WorkHash, Target string
		Timestamp                   uint64
		ChainID                     int  `json:"chainId"`
		Private                     bool `json:"privateCpuTest"`
	}
	data, err = os.ReadFile(os.Getenv("FORGE_MULTI_ISSUED_FIXTURE"))
	if err != nil || json.Unmarshal(data, &issued) != nil || !issued.Private || issued.ChainID != 7008902 {
		t.Fatal("private CPU lab fixture required")
	}
	rawHeader, _ := hex.DecodeString(issued.HeaderRLP)
	if crypto.Keccak256Hash(rawHeader).Hex() != issued.WorkHash {
		t.Fatal("issued OGG header hash mismatch")
	}
	clients := map[string]*RPCClient{}
	templates := map[string]forgepool.ParentTemplate{}
	for symbol, url := range urls {
		c := NewRPCClient(symbol, url, "5s", "test", "test-only")
		clients[symbol] = c
		res, e := c.doPost(url, "getblockchaininfo", []interface{}{})
		if e != nil {
			t.Fatal(e)
		}
		var info struct{ Chain string }
		if unmarshalRPCResult(res, &info) != nil || info.Chain != "regtest" {
			t.Fatal("refusing any native mainnet submission")
		}
		var tpl *BlockTemplateReply
		if symbol == "DGB" {
			tpl, e = c.GetDigibyteBlockTemplate()
		} else {
			tpl, e = c.GetAuxiliaryBlockTemplate(symbol == "BELLS")
		}
		if e != nil {
			t.Fatalf("%s GBT: %v", symbol, e)
		}
		templates[symbol], e = tpl.ToParentTemplate()
		if e != nil {
			t.Fatal(e)
		}
	}
	dgbScript, _ := hex.DecodeString("0014706267a5255e3666936bcb9a777667599f387546")
	dogeScript, _ := hex.DecodeString("76a9144b1508ebe06723842994adf33bdba36b4eba44c488ac")
	doge, err := forgepool.BuildDogeCandidate(templates["DOGE"], dogeScript, 7)
	if err != nil {
		t.Fatal(err)
	}
	job, err := forgepool.BuildDigibyteJob(templates["DGB"], issued.WorkHash, doge, dgbScript, 11, 4, 4)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]uint32{"PEP": 63, "DINGO": 50, "BELLS": 16}
	scripts := map[string]string{"DGB": hex.EncodeToString(dgbScript), "DOGE": hex.EncodeToString(dogeScript), "PEP": "76a914bb0ff0797074c03e9969359b605997b53c9eaa8488ac", "DINGO": "76a914e306e2a1a240f634da707d31079f18431317d7af88ac", "BELLS": "0014088a288ee425094ccd67779afd0c2016d010368c"}
	extra := map[uint32]*forgepool.DogeCandidate{}
	for symbol, id := range ids {
		script, _ := hex.DecodeString(scripts[symbol])
		extra[id], err = forgepool.BuildAuxCandidate(templates[symbol], script, 17, id)
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := job.SetAdditionalAux(extra); err != nil {
		t.Fatal(err)
	}
	labTarget, ok := new(big.Int).SetString(strings.TrimPrefix(issued.Target, "0x"), 16)
	if !ok {
		t.Fatal("lab target missing")
	}
	var nonce uint32
	started := time.Now()
	for ; nonce < 2000000; nonce++ {
		header, e := job.Header(make([]byte, 4), make([]byte, 4), job.Template.CurTime, nonce)
		if e != nil {
			t.Fatal(e)
		}
		raw, e := forgepool.ScryptHash(header)
		if e != nil {
			t.Fatal(e)
		}
		if forgepool.HashValue(raw).Cmp(labTarget) > 0 {
			continue
		}
		all := true
		for _, tpl := range templates {
			yes, e := forgepool.MeetsCompactTarget(raw, tpl.Bits)
			if e != nil {
				t.Fatal(e)
			}
			all = all && yes
		}
		if all {
			break
		}
	}
	if nonce == 2000000 {
		t.Fatal("private CPU lab mining budget exhausted")
	}
	parentPrev := job.Template.Previous
	var display common.Hash
	for i := 0; i < 32; i++ {
		display[i] = parentPrev[31-i]
	}
	tmpl := forgepool.ExternalTemplate{Version: 0x23, DGBPrevHash: display, DGBBits: job.Template.Bits, DogeAuxHash: common.HexToHash(doge.DisplayHash), ValidFrom: issued.Timestamp, Expires: issued.Timestamp + 300}
	digest, err := forgepool.TemplateSigningDigest(tmpl, common.HexToHash(issued.WorkHash))
	if err != nil {
		t.Fatal(err)
	}
	key, err := crypto.HexToECDSA(strings.Repeat("0", 63) + "1")
	if err != nil {
		t.Fatal(err)
	} // Public DEV fixture key only.
	sig, err := crypto.Sign(digest.Bytes(), key)
	if err != nil {
		t.Fatal(err)
	}
	proof, err := job.BuildOGGProof(make([]byte, 4), make([]byte, 4), job.Template.CurTime, nonce, nil, tmpl, [][]byte{sig})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := forgepool.EncodeOGGProof(proof)
	if err != nil {
		t.Fatal(err)
	}
	if encoded[4] != 2 || len(proof.OGGChainBranch) != 5 || len(proof.DOGEChainBranch) != 5 {
		t.Fatal("missing expanded auxiliary proof")
	}
	fixture := map[string]interface{}{"headerRLP": issued.HeaderRLP, "workHash": issued.WorkHash, "proof": hex.EncodeToString(encoded), "privateCpuTest": true}
	blob, _ := json.MarshalIndent(fixture, "", "  ")
	if err := os.WriteFile(os.Getenv("FORGE_MULTI_PROOF_FIXTURE"), blob, 0600); err != nil {
		t.Fatal(err)
	}
	for _, symbol := range []string{"DGB", "DOGE", "PEP", "DINGO", "BELLS"} {
		var block []byte
		if symbol == "DGB" {
			block, err = job.FullBlock(make([]byte, 4), make([]byte, 4), job.Template.CurTime, nonce)
		} else {
			id := forgepool.DOGEAuxChainID
			if symbol != "DOGE" {
				id = ids[symbol]
			}
			block, err = job.AuxBlockFor(id, make([]byte, 4), make([]byte, 4), job.Template.CurTime, nonce)
		}
		if err != nil {
			t.Fatal(err)
		}
		reply, e := clients[symbol].doPost(clients[symbol].Url, "submitblock", []interface{}{hex.EncodeToString(block)})
		if e != nil {
			t.Fatal(symbol, e)
		}
		var reason interface{}
		if reply.Result != nil {
			if e := json.Unmarshal(*reply.Result, &reason); e != nil {
				t.Fatal(e)
			}
		}
		if reason != nil {
			// Bells Core can return inconclusive even after connecting the block.
			// Require the exact hash to be canonical; an RPC acknowledgement is insufficient.
			if symbol != "BELLS" || reason != "inconclusive" {
				t.Fatalf("%s native rejection: %v", symbol, reason)
			}
			observed, e := clients[symbol].ForgeBlockObservation("0x" + extra[ids[symbol]].DisplayHash)
			if e != nil || observed != "canonical" {
				t.Fatalf("Bells inconclusive block not canonical: %s %v", observed, e)
			}
		}
		res, e := clients[symbol].doPost(clients[symbol].Url, "getblockchaininfo", []interface{}{})
		if e != nil {
			t.Fatal(e)
		}
		var info struct{ Blocks int64 }
		if unmarshalRPCResult(res, &info) != nil || info.Blocks != templates[symbol].Height {
			t.Fatalf("%s block did not become canonical", symbol)
		}
		t.Logf("same parent work accepted by native %s regtest at height %d", symbol, info.Blocks)
	}
	t.Logf("five external native regtest blocks share one DGB header; OGG private-CPU proof exported, tree size %d, elapsed %v", job.AuxTree.Size, time.Since(started))
}
