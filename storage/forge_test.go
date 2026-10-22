package storage

import (
	"encoding/json"
	"math/big"
	"strings"
	"sync"
	"testing"
)

func forgeReceipt(id string) ForgeShare {
	return ForgeShare{ID: strings.Repeat(id, 32), Login: "miner", Worker: "rig", Height: 3000,
		Difficulty: 10, RoundDifficulty: 100, TimestampMS: 1700000000000, WindowSeconds: 3600,
		WorkHash: "0x" + strings.Repeat("11", 32)}
}

func TestForgeNativeRoundAndSeparateExternalLedger(t *testing.T) {
	reset()
	if _, err := r.Check(); err != nil {
		t.Fatal(err)
	}
	first := forgeReceipt("aa")
	if _, err := r.WriteForgeShare(first); err != nil {
		t.Fatal(err)
	}
	win := forgeReceipt("bb")
	win.ProofHash = "0x" + strings.Repeat("22", 32)
	win.Digibyte = ForgeSubmission{Attempted: true, Accepted: true, BlockHash: "0x" + strings.Repeat("33", 32)}
	win.Dogecoin = ForgeSubmission{Attempted: true, BlockHash: "0x" + strings.Repeat("44", 32), Error: "rejected"}
	if exists, err := r.WriteForgeShare(win); err != nil || exists {
		t.Fatalf("write %v %v", exists, err)
	}
	// Retry even after a lost Redis reply must not rename a fresh round.
	if exists, err := r.WriteForgeShare(win); err != nil || !exists {
		t.Fatalf("duplicate %v %v", exists, err)
	}
	blocks, err := r.GetCandidates(3000)
	if err != nil || len(blocks) != 1 {
		t.Fatalf("candidates %v %v", blocks, err)
	}
	b := blocks[0]
	if b.Nonce != ForgeRoundID(win.ProofHash) || b.PowHash != win.WorkHash || b.MixDigest != win.ProofHash || b.TotalShares != 20 {
		t.Fatalf("wrong native candidate %+v", b)
	}
	shares, err := r.GetRoundShares(3000, b.Nonce)
	if err != nil || shares["miner"] != 20 {
		t.Fatalf("shares %v %v", shares, err)
	}
	if n := r.client.HGet(r.formatKey("miners", "miner"), "blocksFound").Val(); n != "1" {
		t.Fatalf("blocksFound %s", n)
	}
	for _, chain := range []string{"dgb", "doge"} {
		blob, err := r.client.HGet(r.formatKey("forge", "submissions", chain, int64(3000)), win.ID).Result()
		if err != nil {
			t.Fatal(err)
		}
		var outcome ForgeSubmission
		if err := json.Unmarshal([]byte(blob), &outcome); err != nil {
			t.Fatal(err)
		}
		if outcome.Accepted != (chain == "dgb") {
			t.Fatalf("outcome %s: %+v", chain, outcome)
		}
	}
	// Round ID survives the existing immature block row encoding.
	b.Hash = "0x" + strings.Repeat("55", 32)
	b.Reward = big.NewInt(450)
	if err := r.WriteImmatureBlock(b, map[string]int64{"miner": 0}); err != nil {
		t.Fatal(err)
	}
	immature, err := r.GetImmatureBlocks(3000)
	if err != nil || len(immature) != 1 || immature[0].Nonce != b.Nonce || immature[0].Hash != b.Hash {
		t.Fatalf("immature identity %v %v", immature, err)
	}
}

func TestForgeExternalOnlyDoesNotCloseOGGRound(t *testing.T) {
	reset()
	s := forgeReceipt("aa")
	s.Digibyte = ForgeSubmission{Attempted: true, Accepted: true, BlockHash: "0x" + strings.Repeat("33", 32)}
	s.Dogecoin = s.Digibyte
	s.OGGError = "rejected"
	if _, err := r.WriteForgeShare(s); err != nil {
		t.Fatal(err)
	}
	blocks, err := r.GetCandidates(3000)
	if err != nil || len(blocks) != 0 {
		t.Fatal("external win created native candidate", err)
	}
	if n := r.client.HGet(r.formatKey("shares", "roundCurrent"), "miner").Val(); n != "10" {
		t.Fatalf("share credit %s", n)
	}
	if n := r.client.HGet(r.formatKey("miners", "miner"), "blocksFound").Val(); n != "" {
		t.Fatalf("external counted as native %s", n)
	}
}

func TestForgeAccountingPreflightAllowsCleanRetry(t *testing.T) {
	for _, kind := range []string{"wrong-type", "bad-counter", "noncanonical-counter", "overflow"} {
		t.Run(kind, func(t *testing.T) {
			reset()
			s := forgeReceipt("aa")
			s.ProofHash = "0x" + strings.Repeat("22", 32)
			badKey := r.formatKey("blocks", "candidates")
			if kind == "wrong-type" {
				r.client.Set(badKey, "bad", 0)
			} else {
				badKey = r.formatKey("stats")
				value := "bad"
				if kind == "noncanonical-counter" {
					value = "01"
				}
				if kind == "overflow" {
					value = "9007199254740991"
				}
				r.client.HSet(badKey, "roundShares", value)
			}
			if _, err := r.WriteForgeShare(s); err == nil {
				t.Fatal("expected preflight error")
			}
			if r.client.Exists(r.formatKey("forge", "credited", int64(3000))).Val() || r.client.Exists(r.formatKey("shares", "roundCurrent")).Val() {
				t.Fatal("partial accounting after failed preflight")
			}
			r.client.Del(badKey)
			if exists, err := r.WriteForgeShare(s); err != nil || exists {
				t.Fatalf("retry %v %v", exists, err)
			}
			blocks, err := r.GetCandidates(3000)
			if err != nil || len(blocks) != 1 || blocks[0].TotalShares != 10 {
				t.Fatalf("retry candidate %v %v", blocks, err)
			}
		})
	}
}

func TestForgeConcurrentDuplicateCreditsOnce(t *testing.T) {
	reset()
	s := forgeReceipt("aa")
	s.ProofHash = "0x" + strings.Repeat("22", 32)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := r.WriteForgeShare(s); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	blocks, err := r.GetCandidates(3000)
	if err != nil || len(blocks) != 1 || blocks[0].TotalShares != 10 {
		t.Fatalf("concurrent credit %v %v", blocks, err)
	}
}
