package storage

import (
	"math"
	"math/big"
	"strings"
	"testing"
	"time"

	"gopkg.in/redis.v3"
)

func TestForgeHashrateUnitsPreservePayoutWeights(t *testing.T) {
	reset()
	s := forgeReceipt("aa")
	s.Difficulty = 600
	s.TimestampMS = time.Now().UnixNano() / int64(time.Millisecond)
	if _, err := r.WriteForgeShare(s); err != nil {
		t.Fatal(err)
	}
	// Mix a legacy work sample in the same dashboard window.
	now := float64(s.TimestampMS / 1000)
	r.client.ZAdd(r.formatKey("hashrate"), redis.Z{Score: now, Member: "600:legacy:rig:1"})
	r.client.ZAdd(r.formatKey("hashrate", "legacy"), redis.Z{Score: now, Member: "600:rig:1"})
	stats, err := r.CollectStats(time.Hour, 10, 10)
	if err != nil {
		t.Fatal(err)
	}
	miners := stats["miners"].(map[string]Miner)
	if miners[s.Login].HR != 65536 || miners["legacy"].HR != 1 || stats["hashrate"].(int64) != 65537 {
		t.Fatalf("wrong units: %+v", stats)
	}
	workers, err := r.CollectWorkersStats(time.Hour, time.Hour, s.Login)
	if err != nil {
		t.Fatal(err)
	}
	w := workers["workers"].(map[string]Worker)[s.Worker]
	if w.HR != 65536 || w.TotalHR != 65536 || w.HashrateOverflow {
		t.Fatalf("worker units: %+v", w)
	}
	legacy, err := r.CollectWorkersStats(time.Hour, time.Hour, "legacy")
	if err != nil || legacy["hashrate"].(int64) != 1 {
		t.Fatal("legacy units changed", err, legacy)
	}
	if got := r.client.HGet(r.formatKey("shares", "roundCurrent"), s.Login).Val(); got != "600" {
		t.Fatal("payout weights rewritten", got)
	}
	if duplicate, err := r.WriteForgeShare(s); err != nil || !duplicate {
		t.Fatal("dedup changed", duplicate, err)
	}
}

func TestForgeHashrateBoundsAndMalformedRows(t *testing.T) {
	id := strings.Repeat("a", 64)
	work := hashrateWork([]string{"9007199254740991", "rig", "1", id}, 3)
	rate, overflow := workRate(work, 600)
	want := new(big.Int).Lsh(big.NewInt(9007199254740991), 16)
	want.Quo(want, big.NewInt(600))
	if overflow || rate != want.Int64() {
		t.Fatal("overflow before window division", rate, overflow)
	}
	if rate, overflow := workRate(work, 1); !overflow || rate != math.MaxInt64 {
		t.Fatal("missing explicit overflow", rate, overflow)
	}
	if rate, overflow := addHashrates(math.MaxInt64, 1); !overflow || rate != math.MaxInt64 {
		t.Fatal("total wrapped")
	}
	for _, p := range [][]string{{}, {"1"}, {"-1", "rig", "1"}, {"x", "rig", "1"}, {"1", "rig", "1", "bad"}} {
		if hashrateWork(p, 3) != nil {
			t.Fatal("malformed row accepted", p)
		}
	}
}
