package proxy

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestDurableAcknowledgementDoesNotCreditUncertainBlock(t *testing.T) {
	f := newIntentFixture(t)
	f.s.forge.durableShareAcknowledgements = true
	f.daemons["OGG"].reply = "lost"
	if err := f.submit(t); err != nil {
		t.Fatal("durable valid share was not acknowledged", err)
	}
	if f.writer.calls != 0 {
		t.Fatal("uncertain block or share credited before ordered resolution")
	}
	q := f.s.forge.account
	later := f.receipt
	later.ID = strings.Repeat("cb", 32)
	later.TimestampMS++
	q.flushMu.Lock()
	done := make(chan error, 1)
	go func() {
		_, _, _, err := f.s.processForgeIntent(f.router, f.job, f.sol, forgeHits{}, later)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		q.flushMu.Unlock()
		t.Fatal("miner response waited for accounting flush")
	}
	q.flushMu.Unlock()
	if f.writer.calls != 0 {
		t.Fatal("share acknowledgement moved balances")
	}
	q = f.restart(t)
	d := f.daemons["OGG"]
	d.mu.Lock()
	d.canonical = true
	d.mu.Unlock()
	if err := q.recoverSubmissions(f.router); err != nil {
		t.Fatal(err)
	}
	if err := q.flush(); err != nil {
		t.Fatal(err)
	}
	if len(f.writer.receipts) != 2 || f.writer.receipts[0].ProofHash == "" || f.writer.receipts[1].ID != later.ID {
		t.Fatal("recovery lost or reordered acknowledged shares")
	}
	if err := q.flush(); err != nil || len(f.writer.receipts) != 2 {
		t.Fatal("acknowledged share was credited twice")
	}
}
func TestDurableAcknowledgementStillRejectsDiskFailure(t *testing.T) {
	f := newIntentFixture(t)
	f.s.forge.durableShareAcknowledgements = true
	q := f.s.forge.account
	dir := q.dir
	if err := os.Rename(dir, dir+"-saved"); err != nil {
		t.Fatal(err)
	}
	defer os.Rename(dir+"-saved", dir)
	if err := os.WriteFile(dir, []byte("unavailable"), 0600); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(dir)
	valid, _, _, err := f.s.processForgeIntent(f.router, f.job, f.sol, forgeHits{}, f.receipt)
	if valid || err == nil || f.writer.calls != 0 {
		t.Fatal("non-durable share was acknowledged")
	}
}
