package forgepool_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/HawyUkr/open-progpow-pool/forgepool"
	"github.com/HawyUkr/open-progpow-pool/rpc"
)

func mineDGB(t *testing.T, block []byte, bits uint32, valid bool) {
	t.Helper()
	for nonce := uint32(0); nonce < 100000; nonce++ {
		binary.LittleEndian.PutUint32(block[76:80], nonce)
		hash, err := forgepool.ScryptHash(block[:80])
		if err != nil {
			t.Fatal(err)
		}
		ok, err := forgepool.MeetsCompactTarget(hash, bits)
		if err != nil {
			t.Fatal(err)
		}
		if ok == valid {
			return
		}
	}
	t.Fatal("regtest mining bound exhausted")
}

func TestOfficialDigibyteAcceptance(t *testing.T) {
	url, stage := os.Getenv("FORGE_DGB_TEST_RPC"), os.Getenv("FORGE_DGB_TEST_STAGE")
	if url == "" {
		t.Skip("requires isolated Digibyte script")
	}
	var info struct {
		Chain  string
		Blocks int64
	}
	if err := json.Unmarshal(dogeRPC(t, url, "getblockchaininfo"), &info); err != nil || info.Chain != "regtest" {
		t.Fatal("disposable regtest required", err)
	}
	client := rpc.NewRPCClient("isolated-dgb", url, "5s", "forge-regtest", "disposable-test-only")
	wantMWEB := strings.HasPrefix(stage, "mweb-")
	wantTx := stage == "witness" || stage == "mweb-pegin"
	if stage != "empty" && stage != "witness" && stage != "mweb-empty" && stage != "mweb-pegin" {
		t.Fatal("explicit stage required")
	}
	var tpl forgepool.ParentTemplate
	deadline := time.Now().Add(10 * time.Second)
	for {
		gbt, err := client.GetDigibyteBlockTemplate()
		if err != nil {
			t.Fatal(err)
		}
		tpl, err = gbt.ToParentTemplate()
		if err != nil {
			t.Fatal(err)
		}
		if (len(tpl.MWEB) > 0) == wantMWEB && (!wantTx || len(tpl.Transactions) > 0) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("stage %s template missing data: MWEB=%d transactions=%d", stage, len(tpl.MWEB), len(tpl.Transactions))
		}
		time.Sleep(100 * time.Millisecond)
	}
	if len(tpl.WitnessCommitment) == 0 {
		t.Fatal("missing SegWit commitment")
	}
	if (stage == "empty" && len(tpl.Transactions) != 0) || (stage == "mweb-empty" && len(tpl.Transactions) != 1) || (stage == "mweb-pegin" && len(tpl.Transactions) < 2) {
		t.Fatal("unexpected stage transaction inventory")
	}
	if stage == "witness" {
		witness := false
		for _, tx := range tpl.Transactions {
			if len(tx.Data) > 6 && tx.Data[4] == 0 && tx.Data[5] == 1 {
				witness = true
			}
		}
		if !witness {
			t.Fatal("fixture did not spend a witness output")
		}
	}
	doge := &forgepool.DogeCandidate{Hash: forgepool.Hash{0x62}, Bits: tpl.Bits}
	job, err := forgepool.BuildDigibyteJob(tpl, "0x"+strings.Repeat("11", 32), doge, []byte{0x51}, 42, 4, 4)
	if err != nil {
		t.Fatal(err)
	}
	extra1, extra2 := []byte{1, 2, 3, 4}, []byte{5, 6, 7, 8}
	block, err := job.FullBlock(extra1, extra2, tpl.CurTime, 0)
	if err != nil {
		t.Fatal(err)
	}
	mineDGB(t, block, tpl.Bits, true)
	for n, name := range []string{"bad-merkle", "bad-pow", "bad-witness", "truncated-mweb"} {
		if name == "truncated-mweb" && !wantMWEB {
			continue
		}
		t.Run(name, func(t *testing.T) {
			bad := append([]byte{}, block...)
			// Keep negative-test header identities separate from the valid
			// candidate, including daemon headers-only/inconclusive caches.
			binary.LittleEndian.PutUint32(bad[68:72], tpl.CurTime+uint32(n)+1)
			mineDGB(t, bad, tpl.Bits, true)
			switch name {
			case "bad-merkle":
				bad[36] ^= 1
				mineDGB(t, bad, tpl.Bits, true)
			case "bad-pow":
				mineDGB(t, bad, tpl.Bits, false)
			case "bad-witness":
				coinbase, err := job.Coinbase.FullCoinbase(extra1, extra2)
				if err != nil {
					t.Fatal(err)
				}
				// Reserved witness value is the final 32 bytes before locktime;
				// changing it preserves the base txid/header but breaks commitment.
				bad[81+len(coinbase)-5] ^= 1
			case "truncated-mweb":
				bad = bad[:len(bad)-1]
			}
			accepted, _, err := client.SubmitForgeBlock(hex.EncodeToString(bad))
			if err == nil || accepted {
				t.Fatal("daemon accepted malformed " + name)
			}
		})
	}
	// BIP23 checks context/body without inserting the candidate.
	result := dogeRPC(t, url, "getblocktemplate", map[string]interface{}{"mode": "proposal", "data": hex.EncodeToString(block), "rules": []string{"segwit", "mweb"}})
	if string(result) != "null" {
		t.Fatalf("valid %s proposal rejected: %s", stage, result)
	}
	peer := os.Getenv("FORGE_DGB_SIGNER_RPC")
	if peer == "" {
		t.Fatal("independent Digibyte node required")
	}
	var peerInfo struct{ Chain string }
	if err := json.Unmarshal(dogeRPC(t, peer, "getblockchaininfo"), &peerInfo); err != nil || peerInfo.Chain != "regtest" {
		t.Fatal("independent regtest required", err)
	}
	wantTip := string(dogeRPC(t, url, "getbestblockhash"))
	deadline = time.Now().Add(10 * time.Second)
	for string(dogeRPC(t, peer, "getbestblockhash")) != wantTip {
		if time.Now().After(deadline) {
			t.Fatal("independent node did not synchronize")
		}
		time.Sleep(20 * time.Millisecond)
	}
	result = dogeRPC(t, peer, "getblocktemplate", map[string]interface{}{"mode": "proposal", "data": hex.EncodeToString(block), "rules": []string{"segwit", "mweb"}})
	if string(result) != "null" {
		t.Fatalf("independent %s proposal rejected: %s", stage, result)
	}
	if accepted, _, err := client.SubmitForgeBlock(hex.EncodeToString(block)); err != nil || !accepted {
		t.Fatalf("valid %s block rejected: %v", stage, err)
	}
	a := sha256.Sum256(block[:80])
	hash := sha256.Sum256(a[:])
	expected := forgepool.DisplayHash(hash)
	var best, raw string
	json.Unmarshal(dogeRPC(t, url, "getbestblockhash"), &best)
	if best != expected {
		t.Fatalf("accepted hash mismatch %s != %s", best, expected)
	}
	if found, err := client.HasForgeBlock(expected); err != nil || !found {
		t.Fatal("accepted body not recoverable", err)
	}
	json.Unmarshal(dogeRPC(t, url, "getblock", best, 0), &raw)
	decoded, err := hex.DecodeString(raw)
	if err != nil || !bytes.Equal(decoded, block) {
		t.Fatalf("daemon wire differs: submitted=%d stored=%d", len(block), len(decoded))
	}
	if accepted, rejected, err := client.SubmitForgeBlock(hex.EncodeToString(block)); accepted || rejected || err == nil {
		t.Fatal("duplicate certainty incorrect")
	}
	t.Logf("accepted %s height=%d hash=%s transactions=%d MWEB-bytes=%d exact-wire-bytes=%d", stage, tpl.Height, best, len(tpl.Transactions), len(tpl.MWEB), len(block))
}
