package storage

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSOQWinHasSeparateLedgerAndDoesNotCreateRGNRound(t *testing.T) {
	reset()
	s := forgeReceipt("ab")
	s.Soqucoin = ForgeSubmission{Attempted: true, Accepted: true, BlockHash: "0x" + strings.Repeat("45", 32)}
	if dup, e := r.WriteForgeShare(s); e != nil || dup {
		t.Fatal(e)
	}
	if dup, e := r.WriteForgeShare(s); e != nil || !dup {
		t.Fatal("duplicate share credit", e)
	}
	key := r.formatKey("forge", "submissions", "soq", s.Height)
	blob, e := r.client.HGet(key, s.ID).Result()
	if e != nil {
		t.Fatal(e)
	}
	var out ForgeSubmission
	if json.Unmarshal([]byte(blob), &out) != nil || out != s.Soqucoin {
		t.Fatal("SOQ outcome missing")
	}
	blocks, e := r.GetCandidates(s.Height)
	if e != nil || len(blocks) != 0 {
		t.Fatal("external win created native RGN candidate", e)
	}
	if n := r.client.HGet(r.formatKey("shares", "roundCurrent"), s.Login).Val(); n != "10" {
		t.Fatal("share credit changed", n)
	}
	s.Soqucoin.Accepted = false
	s.Soqucoin.Observation = "noncanonical"
	s.Soqucoin.Error = "native chain reorganization"
	if e = r.WriteForgeExternalOutcomes(s); e != nil {
		t.Fatal(e)
	}
	blob, e = r.client.HGet(key, s.ID).Result()
	if e != nil {
		t.Fatal(e)
	}
	if json.Unmarshal([]byte(blob), &out) != nil || out != s.Soqucoin {
		t.Fatal("SOQ reconciliation missing")
	}
	if n := r.client.HGet(r.formatKey("shares", "roundCurrent"), s.Login).Val(); n != "10" {
		t.Fatal("observation double-credited miner", n)
	}
}
