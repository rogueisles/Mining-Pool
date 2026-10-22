package storage

import (
	"fmt"
	"strings"
	"sync"
	"testing"
)

func TestForgeDedupCompactionRetainsExactReplayProtection(t *testing.T) {
	reset()
	var receipts []ForgeShare
	for n := 0; n < 80; n++ {
		s := forgeReceipt("aa")
		s.ID = fmt.Sprintf("ab%062x", n)
		if n == 79 {
			s.ProofHash = "0x" + strings.Repeat("55", 32)
		}
		if _, err := r.WriteForgeShare(s); err != nil {
			t.Fatal(err)
		}
		receipts = append(receipts, s)
	}
	p, err := r.PlanForgeDedup(3000, "ab")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.LiveIDs) != 80 || len(p.Packed) != 80*64 {
		t.Fatal("incomplete preview")
	}
	if n, err := r.ApplyForgeDedup(*p); err != nil || n != 80 {
		t.Fatal(n, err)
	}
	if n, err := r.ApplyForgeDedup(*p); err != nil || n != 80 {
		t.Fatal("lost reply retry", n, err)
	}
	for _, s := range receipts {
		if exists, err := r.WriteForgeShare(s); err != nil || !exists {
			t.Fatal("archived replay credited", exists, err)
		}
	}
	blocks, err := r.GetCandidates(3000)
	if err != nil || len(blocks) != 1 || blocks[0].TotalShares != 800 {
		t.Fatal("native round replayed", blocks, err)
	}
	if r.client.HLen(r.formatKey("forge", "credited", int64(3000))).Val() != 0 {
		t.Fatal("reviewed live records remain")
	}
	v, err := r.client.Eval(`return redis.call('TTL',KEYS[1])`, []string{r.forgeDedupArchiveKey(3000, "ab")}, []string{}).Result()
	if err != nil || v != int64(-1) {
		t.Fatal("archive has expiry", v, err)
	}
	newShare := forgeReceipt("aa")
	newShare.ID = fmt.Sprintf("ab%062x", 81)
	if exists, err := r.WriteForgeShare(newShare); err != nil || exists {
		t.Fatal("new ID incorrectly suppressed", exists, err)
	}
	if exists, err := r.WriteForgeShare(newShare); err != nil || !exists {
		t.Fatal("live delta not protected", exists, err)
	}
	second, err := r.PlanForgeDedup(3000, "ab")
	if err != nil {
		t.Fatal(err)
	}
	if len(second.LiveIDs) != 1 || len(second.Packed) != 81*64 {
		t.Fatal("base/delta merge wrong")
	}
	if _, err := r.ApplyForgeDedup(*second); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ApplyForgeDedup(*p); err == nil {
		t.Fatal("stale plan overwrote newer archive")
	}
}

func TestForgeDedupCompactionConcurrentCreditAndConflicts(t *testing.T) {
	reset()
	first := forgeReceipt("aa")
	first.ID = fmt.Sprintf("cd%062x", 1)
	r.WriteForgeShare(first)
	p, err := r.PlanForgeDedup(3000, "cd")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 40)
	for n := 2; n < 22; n++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			s := first
			s.ID = fmt.Sprintf("cd%062x", n)
			_, err := r.WriteForgeShare(s)
			errs <- err
		}(n)
	}
	wg.Add(1)
	go func() { defer wg.Done(); _, err := r.ApplyForgeDedup(*p); errs <- err }()
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := r.client.HGet(r.formatKey("shares", "roundCurrent"), "miner").Val(); got != "210" {
		t.Fatal("concurrent credit lost", got)
	}
	for n := 1; n < 22; n++ {
		s := first
		s.ID = fmt.Sprintf("cd%062x", n)
		if exists, err := r.WriteForgeShare(s); err != nil || !exists {
			t.Fatal("concurrent ID lost", n, err)
		}
	}
	bad := *p
	bad.Packed = strings.Repeat("00", 32)
	if _, err := r.ApplyForgeDedup(bad); err == nil {
		t.Fatal("tampered plan applied")
	}
	bad = *p
	bad.Namespace = "other"
	bad.Digest = bad.digest()
	if _, err := r.ApplyForgeDedup(bad); err == nil {
		t.Fatal("wrong namespace applied")
	}
	if _, err := r.PlanForgeDedup(3000, "xx"); err == nil {
		t.Fatal("invalid bucket allowed")
	}
}

func TestForgeDedupCorruptArchiveAndBoundFailClosed(t *testing.T) {
	reset()
	s := forgeReceipt("aa")
	r.client.Set(r.forgeDedupArchiveKey(s.Height, "aa"), "broken", 0)
	if _, err := r.WriteForgeShare(s); err == nil {
		t.Fatal("corrupt archive credited")
	}
	if r.client.HGet(r.formatKey("shares", "roundCurrent"), s.Login).Val() != "" {
		t.Fatal("partial credit")
	}
	if _, err := r.PlanForgeDedup(s.Height, "aa"); err == nil {
		t.Fatal("corrupt archive exported")
	}
	r.client.Del(r.forgeDedupArchiveKey(s.Height, "aa"))
	for n := 0; n <= MaxForgeDedupBucketIDs; n++ {
		r.client.HSet(r.formatKey("forge", "credited", s.Height), fmt.Sprintf("aa%062x", n), "1")
	}
	if _, err := r.PlanForgeDedup(s.Height, "aa"); err == nil {
		t.Fatal("oversized bucket compacted")
	}
	if got := r.client.HLen(r.formatKey("forge", "credited", s.Height)).Val(); got != MaxForgeDedupBucketIDs+1 {
		t.Fatal("failure dropped IDs", got)
	}
}
