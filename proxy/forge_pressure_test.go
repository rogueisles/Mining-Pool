package proxy

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/HawyUkr/open-progpow-pool/storage"
)

func pressureReceipt(n int) storage.ForgeShare {
	return storage.ForgeShare{ID: fmt.Sprintf("%064x", n+1), Login: "miner", Worker: "rig", Height: 3000, Difficulty: 1, RoundDifficulty: 1, TimestampMS: time.Now().UnixNano() / int64(time.Millisecond), WindowSeconds: 3600, WorkHash: "0x" + strings.Repeat("11", 32)}
}

func TestForgeAccountingConcurrentRecordLimitAndResume(t *testing.T) {
	w := &mockForgeWriter{err: errors.New("Redis unavailable")}
	q, err := newForgeAccountingQueue(t.TempDir(), w, ForgeAccountingLimits{MaxPendingRecords: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer q.close()
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(n int) { defer wg.Done(); q.enqueue(pressureReceipt(n)) }(i)
	}
	wg.Wait()
	s := q.status(time.Now())
	if s.PendingRecords != 4 || s.RejectedOrdinary != 16 || !s.WorkPaused || s.UndurableRecords != 0 {
		t.Fatalf("unbounded admission %+v", s)
	}
	files, _ := filepath.Glob(filepath.Join(q.dir, "*.json"))
	if len(files) != 4 {
		t.Fatal("wrong disk bound", files)
	}
	if len(q.byID) != 4 {
		t.Fatal("journal identity index mismatch")
	}
	w.err = nil
	if _, err := q.flushFor(q.pending[0].receipt.ID); err != nil {
		t.Fatal(err)
	}
	if !q.workPaused() {
		t.Fatal("resumed above half capacity")
	}
	if _, err := q.flushFor(q.pending[0].receipt.ID); err != nil {
		t.Fatal(err)
	}
	if q.workPaused() {
		t.Fatal("did not resume at half capacity")
	}
	if err := q.flush(); err != nil {
		t.Fatal(err)
	}
	s = q.status(time.Now())
	if s.PendingBytes != 0 || s.PendingRecords != 0 || s.Health != "healthy" || len(q.byID) != 0 || s.LastAccountingSuccessMS == 0 {
		t.Fatal("drain did not clear pressure", s)
	}
}

func TestForgeAccountingByteLimitAndAgePreserveRecords(t *testing.T) {
	w := &mockForgeWriter{err: errors.New("offline")}
	q, err := newForgeAccountingQueue(t.TempDir(), w)
	if err != nil {
		t.Fatal(err)
	}
	defer q.close()
	r := pressureReceipt(0)
	blob, _ := json.Marshal(forgeJournalRecord{Version: 2, Receipt: r, Credit: &r, Credited: true})
	q.limits.MaxPendingBytes = 2 * int64(len(blob))
	for n := 0; n < 2; n++ {
		row := r
		row.ID = fmt.Sprintf("%064x", n+1)
		q.enqueue(row)
	}
	third := r
	third.ID = fmt.Sprintf("%064x", 3)
	// Resolved admission records can be smaller than a frozen credit record.
	// Set the boundary from actual retained bytes and the next admission's
	// conservative reservation rather than relying on an internal rewrite.
	q.limits.MaxPendingBytes = q.pendingBytes + int64(len(blob)) - 1
	if _, err := q.enqueue(third); !errors.Is(err, errForgeBackpressure) {
		t.Fatal("bytes cap bypassed", err)
	}
	var diskBytes int64
	files, _ := filepath.Glob(filepath.Join(q.dir, "*.json"))
	for _, file := range files {
		info, err := os.Stat(file)
		if err != nil {
			t.Fatal(err)
		}
		diskBytes += info.Size()
	}
	if q.pendingBytes != diskBytes || diskBytes > q.limits.MaxPendingBytes || len(q.pending) != 2 {
		t.Fatal("byte accounting differs")
	}
	s := q.status(time.Now().Add(6 * time.Minute))
	if s.OldestAgeSeconds < 300 || !s.WorkPaused || len(q.pending) != 2 {
		t.Fatal("aged records lost or admitted", s)
	}
	w.err = nil
	if err := q.flush(); err != nil {
		t.Fatal(err)
	}
	old := pressureReceipt(4)
	old.TimestampMS -= 600000
	w.err = errors.New("offline")
	q.enqueue(old)
	if _, err := q.enqueue(pressureReceipt(5)); !errors.Is(err, errForgeBackpressure) {
		t.Fatal("age limit bypassed", err)
	}
	if len(q.pending) != 1 {
		t.Fatal("expired accounting receipt discarded")
	}
}

func TestForgeAccountingRestartRetainsPressureAndIgnoresStatus(t *testing.T) {
	w := &mockForgeWriter{err: errors.New("offline")}
	limits := ForgeAccountingLimits{MaxPendingRecords: 1}
	q, err := newForgeAccountingQueue(t.TempDir(), w, limits)
	if err != nil {
		t.Fatal(err)
	}
	q.enqueue(pressureReceipt(0))
	q.writeStatus(time.Now())
	before := q.status(time.Now())
	q.close()
	restored, err := newForgeAccountingQueue(q.dir, w, limits)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.close()
	after := restored.status(time.Now())
	if !after.WorkPaused || after.PendingBytes != before.PendingBytes || after.PendingRecords != 1 {
		t.Fatal("restart reset backlog pressure", before, after)
	}
	if _, err := restored.enqueue(pressureReceipt(1)); !errors.Is(err, errForgeBackpressure) {
		t.Fatal("restart limit bypass")
	}
}

func TestForgePressureStillRoutesAllChainsAndDoesNotIssueWork(t *testing.T) {
	f := newIntentFixture(t)
	q := f.s.forge.account
	q.limits.MaxPendingRecords = 1
	f.writer.err = errors.New("offline")
	q.enqueue(pressureReceipt(0))
	if f.s.forge.currentJob() != nil {
		t.Fatal("paused queue advertised work")
	}
	if err := f.submit(t); err == nil {
		t.Fatal("missing accounting error")
	}
	for _, d := range f.daemons {
		if d.submits != 1 {
			t.Fatal("capacity blocked chain", d.chain)
		}
	}
	s := q.status(time.Now())
	if s.PendingRecords != 2 || s.PriorityOverflow != 1 || s.Health != "critical" {
		t.Fatal("priority overflow hidden", s)
	}
	f.writer.err = nil
	if err := q.flush(); err != nil {
		t.Fatal(err)
	}
	if f.s.forge.currentJob() == nil {
		t.Fatal("work did not resume")
	}
}

func TestForgePressureOrdinaryRejectDoesNotClaimDuplicateOrBanMiner(t *testing.T) {
	f := newIntentFixture(t)
	// Preserve the valid share target but make all three network targets harder.
	f.job.Digibyte.Template.Bits = 0x01010000
	f.job.Digibyte.DOGE.Template.Bits = 0x01010000
	f.job.Digibyte.DOGE.Bits = 0x01010000
	f.job.OGG.Target = "0x01"
	q := f.s.forge.account
	q.limits.MaxPendingRecords = 1
	f.writer.err = errors.New("offline")
	q.enqueue(pressureReceipt(0))
	extra, ntime, nonce := submitArgs(f.job, f.nonce)
	// Find a valid share under the modified header. Ordinary shares need not
	// win a chain target. No policy object is installed: using it would panic.
	var out bytes.Buffer
	f.cs.enc = json.NewEncoder(&out)
	params, _ := json.Marshal([]string{f.cs.login, f.job.ID, extra, ntime, nonce})
	err := f.cs.handleForgeTCPMessage(f.s, &StratumReq{JSONRpcReq: JSONRpcReq{Id: json.RawMessage("1"), Method: "mining.submit", Params: params}})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.s.forge.duplicates) != 0 || q.status(time.Now()).RejectedOrdinary != 1 || !strings.Contains(out.String(), "temporarily unavailable") {
		t.Fatal("pressure claimed/banned miner", out.String())
	}
	for _, d := range f.daemons {
		if d.submits != 0 {
			t.Fatal("ordinary share routed")
		}
	}
	out.Reset()
	authorize, _ := json.Marshal([]string{f.cs.login})
	if err := f.cs.handleForgeTCPMessage(f.s, &StratumReq{JSONRpcReq: JSONRpcReq{Id: json.RawMessage("2"), Method: "mining.authorize", Params: authorize}}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "capacity unavailable") {
		t.Fatal("new miner admitted while paused")
	}
	out.Reset()
	expires := f.job.Signed.Template.Expires
	f.job.Signed.Template.Expires = uint64(time.Now().Unix() - 1)
	if err := f.cs.handleForgeTCPMessage(f.s, &StratumReq{JSONRpcReq: JSONRpcReq{Id: json.RawMessage("3"), Method: "mining.submit", Params: params}}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "temporarily unavailable") {
		t.Fatal("pool-induced stale work penalized miner")
	}
	f.job.Signed.Template.Expires = expires
	f.writer.err = nil
	if err := q.flush(); err != nil {
		t.Fatal(err)
	}
	if err := f.submit(t); err != nil {
		t.Fatal("uncredited share could not retry after recovery", err)
	}
	if len(f.s.forge.duplicates) != 1 {
		t.Fatal("retry not credited/claimed")
	}
}

func TestForgeOperatorStatusFreshnessPrivacyAndReadOnly(t *testing.T) {
	f := newIntentFixture(t)
	f.daemons["OGG"].reply = "lost"
	if err := f.submit(t); err == nil {
		t.Fatal("expected pending outcome")
	}
	q := f.s.forge.account
	now := time.Now()
	if err := q.writeStatus(now); err != nil {
		t.Fatal(err)
	}
	s, err := ReadForgeAccountingStatus(q.dir, now, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if s.PendingIntents != 1 || s.UnresolvedChains["OGG"] != 1 || s.HeadState != "submission_unresolved" || len(s.Outcomes) != 1 {
		t.Fatal("missing outcome", s)
	}
	blob, _ := os.ReadFile(filepath.Join(q.dir, "STATUS"))
	for _, secret := range []string{f.cs.login, f.router.dgb.Url, "secret-password", f.daemons["OGG"].proof} {
		if strings.Contains(string(blob), secret) {
			t.Fatal("operator status leaks private data")
		}
	}
	if _, err := ReadForgeAccountingStatus(q.dir, now.Add(2*time.Second), time.Second); err == nil {
		t.Fatal("stale status looks healthy")
	}
	if _, err := ReadForgeAccountingStatus(q.dir, now.Add(-time.Second), time.Second); err == nil {
		t.Fatal("future status accepted")
	}
	if len(q.pending) != 1 || f.writer.calls != 0 {
		t.Fatal("inspection changed accounting")
	}
	info, _ := os.Stat(filepath.Join(q.dir, "STATUS"))
	if info.Mode().Perm() != 0600 {
		t.Fatal("status permissions")
	}
	if err := os.WriteFile(filepath.Join(q.dir, "STATUS"), append(blob, []byte("{}")...), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadForgeAccountingStatus(q.dir, now, time.Second); err == nil {
		t.Fatal("trailing status data accepted")
	}
}

func TestForgeAccountingLimitDefaultsAndInvalidSettings(t *testing.T) {
	l, err := (ForgeAccountingLimits{}).defaults()
	if err != nil || l.MaxPendingRecords != 10000 || l.MaxPendingBytes != 64*1024*1024 || l.MaxPendingAgeSec != 300 || l.RecoveryBatchSize != 8 {
		t.Fatal(l, err)
	}
	for _, bad := range []ForgeAccountingLimits{{MaxPendingRecords: -1}, {MaxPendingBytes: 1}, {MaxPendingAgeSec: -1}, {RecoveryBatchSize: 129}} {
		if _, err := bad.defaults(); err == nil {
			t.Fatal("invalid limits accepted", bad)
		}
	}
}

func TestForgeRecoveryBatchesDoNotStarveLaterIntents(t *testing.T) {
	f := newIntentFixture(t)
	q := f.s.forge.account
	q.limits.RecoveryBatchSize = 1
	f.daemons["DGB"].reply = "lost"
	for i := 0; i < 3; i++ {
		sol := *f.sol
		sol.Nonce += uint32(i)
		sol.Header, _ = f.job.Digibyte.Header(sol.ExtraNonce1, sol.ExtraNonce2, sol.NTime, sol.Nonce)
		r := f.receipt
		r.ID = forgePayloadDigest(sol.Header)
		in := f.router.prepareIntent(f.job, &sol, forgeHits{Digibyte: true})
		item, err := q.beginIntent(r, in)
		if err != nil {
			t.Fatal(err)
		}
		q.mu.Lock()
		item.active = false
		q.mu.Unlock() // Simulate restart ownership.
	}
	for n := 1; n <= 3; n++ {
		if err := q.recoverSubmissions(f.router); err != nil {
			t.Fatal(err)
		}
		if f.daemons["DGB"].submits != n {
			t.Fatal("unbounded recovery pass")
		}
	}
	for _, item := range q.pending {
		if !item.intent.Blocks[0].Sent {
			t.Fatal("later intent starved")
		}
	}
	if q.status(time.Now()).LastRecoveryCompletedMS == 0 {
		t.Fatal("missing recovery heartbeat")
	}
}

func BenchmarkForgeJournalDurableReceipt(b *testing.B) {
	q, err := newForgeAccountingQueue(b.TempDir(), &discardForgeWriter{})
	if err != nil {
		b.Fatal(err)
	}
	defer q.close()
	b.ReportAllocs()
	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		if _, err := q.enqueue(pressureReceipt(n)); err != nil {
			b.Fatal(err)
		}
	}
}

type discardForgeWriter struct{}

func (*discardForgeWriter) WriteForgeShare(storage.ForgeShare) (bool, error) { return false, nil }
