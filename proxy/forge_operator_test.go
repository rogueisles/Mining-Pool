package proxy

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func operatorConfig(f *intentFixture) *Config {
	c := *f.s.config
	c.Proxy.Forge.AccountingJournalDir = f.s.forge.account.dir
	c.Proxy.Forge.Digibyte = ForgeChainEndpoint{Name: f.router.dgb.Name, Url: f.router.dgb.Url, Timeout: "1s", Username: "secret-user", Password: "secret-password"}
	c.Proxy.Forge.Dogecoin = ForgeChainEndpoint{Name: f.router.doge.Name, Url: f.router.doge.Url, Timeout: "1s", Username: "secret-user", Password: "secret-password"}
	c.Upstream = []Upstream{{Name: f.job.OGGRPC.Name, Url: f.job.OGGRPC.Url, Timeout: "1s", Username: "secret-user", Password: "secret-password"}}
	return &c
}

func TestForgeOperatorPositiveEvidenceAuditAndOrderedReplay(t *testing.T) {
	f := newIntentFixture(t)
	f.daemons["OGG"].reply = "lost"
	if err := f.submit(t); err == nil {
		t.Fatal("expected unresolved")
	}
	cfg := operatorConfig(f)
	if _, err := PrepareForgeReconcile(cfg, f.receipt.ID, "OGG", "", "tester", "lost reply"); err == nil {
		t.Fatal("live journal changed")
	}
	f.s.forge.account.close()
	p, err := PrepareForgeReconcile(cfg, f.receipt.ID, "OGG", "", "tester", "lost reply")
	if err != nil {
		t.Fatal(err)
	}
	if p.Decision != "unresolved" {
		t.Fatal("missing block called accepted")
	}
	if err := ApplyForgeReconcile(cfg, *p, p.Digest); err == nil {
		t.Fatal("forced unknown acceptance")
	}
	f.daemons["OGG"].mu.Lock()
	f.daemons["OGG"].canonical = true
	f.daemons["OGG"].mu.Unlock()
	p, err = PrepareForgeReconcile(cfg, f.receipt.ID, "OGG", "", "tester", "restored node evidence")
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyForgeReconcile(cfg, *p, "wrong"); err == nil {
		t.Fatal("unreviewed decision applied")
	}
	if err := ApplyForgeReconcile(cfg, *p, p.Digest); err != nil {
		t.Fatal(err)
	}
	if f.writer.calls != 0 || f.daemons["OGG"].submits != 1 {
		t.Fatal("operator submitted or credited")
	}
	audit := filepath.Join(cfg.Proxy.Forge.AccountingJournalDir, "audit", "reconcile-"+p.Digest+".json")
	blob, err := os.ReadFile(audit)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(blob), "secret-password") || strings.Contains(string(blob), f.daemons["OGG"].proof) {
		t.Fatal("audit leaked secret/payload")
	}
	var saved ForgeReconcilePlan
	if json.Unmarshal(blob, &saved) != nil || saved.Digest != p.Digest || saved.Operator != "tester" {
		t.Fatal("audit mismatch")
	}
	q, err := newForgeAccountingQueue(cfg.Proxy.Forge.AccountingJournalDir, f.writer)
	if err != nil {
		t.Fatal(err)
	}
	defer q.close()
	if err := q.flush(); err != nil {
		t.Fatal(err)
	}
	if f.writer.calls != 1 || f.writer.receipts[0].ProofHash == "" {
		t.Fatal("native receipt lost")
	}
}

func TestForgeOperatorRejectsChangedEvidenceAndAuditFailure(t *testing.T) {
	f := newIntentFixture(t)
	f.daemons["OGG"].reply = "lost"
	f.submit(t)
	cfg := operatorConfig(f)
	f.s.forge.account.close()
	d := f.daemons["OGG"]
	d.mu.Lock()
	d.canonical = true
	d.mu.Unlock()
	p, err := PrepareForgeReconcile(cfg, f.receipt.ID, "OGG", "", "tester", "review")
	if err != nil {
		t.Fatal(err)
	}
	d.mu.Lock()
	d.wrongProof = true
	d.mu.Unlock()
	if err := ApplyForgeReconcile(cfg, *p, p.Digest); err == nil {
		t.Fatal("changed evidence accepted")
	}
	d.mu.Lock()
	d.wrongProof = false
	d.mu.Unlock()
	if err := os.WriteFile(filepath.Join(cfg.Proxy.Forge.AccountingJournalDir, "audit"), []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := ApplyForgeReconcile(cfg, *p, p.Digest); err == nil {
		t.Fatal("unaudited update allowed")
	}
	q, err := newForgeAccountingQueue(cfg.Proxy.Forge.AccountingJournalDir, f.writer)
	if err != nil {
		t.Fatal(err)
	}
	defer q.close()
	if q.pending[0].intent == nil || q.pending[0].intent.Blocks[2].State != "pending" {
		t.Fatal("audit failure changed journal")
	}
	if err := q.flush(); err == nil {
		t.Fatal("uncertain receipt credited")
	}
}

func TestForgeReviewImmutability(t *testing.T) {
	dir := t.TempDir()
	value := map[string]string{"decision": "review"}
	if err := SaveForgeReview(dir, "plan.json", value); err != nil {
		t.Fatal(err)
	}
	if err := SaveForgeReview(dir, "plan.json", value); err != nil {
		t.Fatal(err)
	}
	if err := SaveForgeReview(dir, "plan.json", map[string]string{"decision": "changed"}); err == nil {
		t.Fatal("audit overwritten")
	}
	if err := SaveForgeReview(dir, "../outside", value); err == nil {
		t.Fatal("path escape")
	}
}

func TestForgeOperatorJournalConflict(t *testing.T) {
	f := newIntentFixture(t)
	f.daemons["OGG"].reply = "lost"
	f.submit(t)
	cfg := operatorConfig(f)
	f.s.forge.account.close()
	d := f.daemons["OGG"]
	d.mu.Lock()
	d.canonical = true
	d.mu.Unlock()
	p, err := PrepareForgeReconcile(cfg, f.receipt.ID, "OGG", "", "tester", "review")
	if err != nil {
		t.Fatal(err)
	}
	q, err := newForgeAccountingQueue(cfg.Proxy.Forge.AccountingJournalDir, f.writer)
	if err != nil {
		t.Fatal(err)
	}
	q.mu.Lock()
	q.pending[0].intent.Blocks[2].Error = errors.New("later recovery attempt").Error()
	q.persist(q.pending[0])
	q.mu.Unlock()
	q.close()
	if err := ApplyForgeReconcile(cfg, *p, p.Digest); err == nil {
		t.Fatal("stale journal review applied")
	}
	p.CreatedUnixMS = time.Now().Add(-2*time.Hour).UnixNano() / int64(time.Millisecond)
	p.Digest = p.digest()
	if err := ApplyForgeReconcile(cfg, *p, p.Digest); err == nil {
		t.Fatal("expired review accepted")
	}
}
