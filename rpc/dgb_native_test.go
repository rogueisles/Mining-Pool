package rpc

import (
	"encoding/hex"
	"github.com/HawyUkr/open-progpow-pool/forgepool"
	"os"
	"strings"
	"testing"
)

// Submit only to a disposable DGB regression chain; reject all other networks.
func TestDGBNativeMergedParentAcceptance(t *testing.T) {
	url := os.Getenv("D13_DGB_REGTEST_RPC")
	if url == "" {
		t.Skip("requires disposable DigiByte regtest daemon")
	}
	r := NewRPCClient("D13-native-regtest", url, "5s", "test", "test-only")
	resp, err := r.doPost(r.Url, "getblockchaininfo", []interface{}{})
	if err != nil {
		t.Fatal(err)
	}
	var info struct {
		Chain  string
		Blocks int64
	}
	if unmarshalRPCResult(resp, &info) != nil || info.Chain != "regtest" {
		t.Fatal("refusing to submit outside regtest")
	}
	tpl, err := r.GetDigibyteBlockTemplate()
	if err != nil {
		t.Fatal(err)
	}
	parent, err := tpl.ToParentTemplate()
	if err != nil {
		t.Fatal(err)
	}
	script, _ := hex.DecodeString("76a914f5c5536dd8df8e22ccfa6c366e54e4a28a6485fe88ac")
	// A real two-leaf auxiliary commitment is carried by the submitted DGB block.
	dogeTpl := forgepool.ParentTemplate{Version: 1, Previous: parent.Previous, Height: 200, CurTime: parent.CurTime, Bits: 0x207fffff, CoinbaseValue: 1000000000000}
	var dogeRPC *RPCClient
	if dogeURL := os.Getenv("D13_DOGE_REGTEST_RPC"); dogeURL != "" {
		dogeRPC = NewRPCClient("D13-DOGE-regtest", dogeURL, "5s", "test", "test-only")
		res, e := dogeRPC.doPost(dogeURL, "getblockchaininfo", []interface{}{})
		if e != nil {
			t.Fatal(e)
		}
		var chain struct{ Chain string }
		if unmarshalRPCResult(res, &chain) != nil || chain.Chain != "regtest" {
			t.Fatal("refusing DOGE mainnet submission")
		}
		dt, e := dogeRPC.GetDogecoinBlockTemplate()
		if e != nil {
			t.Fatal(e)
		}
		dogeTpl, e = dt.ToParentTemplate()
		if e != nil {
			t.Fatal(e)
		}
	}
	dogeScript, _ := hex.DecodeString("76a914407041d76ae66ed29e46e7c887d2bce9cc61484388ac")
	doge, err := forgepool.BuildDogeCandidate(dogeTpl, dogeScript, 17)
	if err != nil {
		t.Fatal(err)
	}
	job, err := forgepool.BuildDigibyteJob(parent, "0x"+strings.Repeat("42", 32), doge, script, 11, 4, 4)
	if err != nil {
		t.Fatal(err)
	}
	target, err := forgepool.CompactTarget(parent.Bits)
	if err != nil {
		t.Fatal(err)
	}
	var nonce uint32
	for ; nonce < 1000000; nonce++ {
		header, err := job.Header(make([]byte, 4), make([]byte, 4), parent.CurTime, nonce)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := forgepool.ScryptHash(header)
		if err != nil {
			t.Fatal(err)
		}
		dogeHit, e := forgepool.MeetsCompactTarget(raw, doge.Bits)
		if e != nil {
			t.Fatal(e)
		}
		if forgepool.HashValue(raw).Cmp(target) <= 0 && dogeHit {
			break
		}
	}
	if nonce == 1000000 {
		t.Fatal("regtest mining budget exhausted")
	}
	block, err := job.FullBlock(make([]byte, 4), make([]byte, 4), parent.CurTime, nonce)
	if err != nil {
		t.Fatal(err)
	}
	// The native daemon validates full body, witness commitment and coinbase.
	accepted, err := r.SubmitRawBlock(hex.EncodeToString(block))
	if err != nil || !accepted {
		t.Fatalf("accept=%v err=%v", accepted, err)
	}
	resp, err = r.doPost(r.Url, "getblockchaininfo", []interface{}{})
	if err != nil {
		t.Fatal(err)
	}
	var after struct {
		Blocks        int64
		BestBlockHash string `json:"bestblockhash"`
	}
	if unmarshalRPCResult(resp, &after) != nil || after.Blocks != info.Blocks+1 {
		t.Fatal("DGB block did not become canonical")
	}
	resp, err = r.doPost(r.Url, "getblock", []interface{}{after.BestBlockHash, 2})
	if err != nil {
		t.Fatal(err)
	}
	var mined struct {
		Confirmations int
		Tx            []struct {
			Vout []struct {
				Value  float64
				Script struct{ Hex string } `json:"scriptPubKey"`
			}
		}
	}
	if unmarshalRPCResult(resp, &mined) != nil || mined.Confirmations < 1 || len(mined.Tx) == 0 {
		t.Fatal("missing canonical coinbase")
	}
	found := false
	for _, v := range mined.Tx[0].Vout {
		if v.Script.Hex == hex.EncodeToString(script) && v.Value > 0 {
			found = true
		}
	}
	if !found {
		t.Fatal("native DGB coinbase did not pay configured treasury script")
	}
	if dogeRPC != nil {
		dogeBlock, e := job.DogeAuxPowBlock(make([]byte, 4), make([]byte, 4), parent.CurTime, nonce)
		if e != nil {
			t.Fatal(e)
		}
		ok, e := dogeRPC.SubmitRawBlock(hex.EncodeToString(dogeBlock))
		if e != nil || !ok {
			t.Fatalf("DOGE accept=%v error=%v", ok, e)
		}
		res, e := dogeRPC.doPost(dogeRPC.Url, "getblockchaininfo", []interface{}{})
		if e != nil {
			t.Fatal(e)
		}
		var dh struct {
			Blocks        int64
			BestBlockHash string `json:"bestblockhash"`
		}
		if unmarshalRPCResult(res, &dh) != nil || dh.Blocks != doge.Template.Height || dh.BestBlockHash != doge.DisplayHash {
			t.Fatal("DOGE AuxPoW did not become canonical")
		}
		t.Logf("same native DGB parent work also accepted by Dogecoin at height %d hash %s; regtest only", dh.Blocks, dh.BestBlockHash)
	}
	t.Logf("native DigiByte accepted DGB+DOGE+OGG commitment block at height %d, hash %s; regtest only", after.Blocks, after.BestBlockHash)
}
