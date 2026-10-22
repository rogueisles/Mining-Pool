package proxy

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/HawyUkr/open-progpow-pool/storage"
)

func TestForgeJournalRestartsWithSameReceiptAndOrder(t *testing.T) {
	s, cs, job, writer, submitter, nonce := testForgeServer(t)
	writer.err = errors.New("Redis unavailable")
	extra, ntime, n := submitArgs(job, nonce)
	if _, _, _, err := s.processForgeShare(cs, cs.login, cs.worker, job.ID, extra, ntime, n, time.Now()); err == nil {
		t.Fatal("missing error")
	}
	q := s.forge.account
	first := writer.receipts[0]
	if first.ProofHash == "" {
		t.Fatal("missing accepted proof receipt")
	}
	second := first
	second.ID = strings.Repeat("ab", 32)
	second.ProofHash = ""
	if _, err := q.enqueue(second); err == nil {
		t.Fatal("missing error")
	}
	files, _ := filepath.Glob(filepath.Join(q.dir, "*.json"))
	if len(files) != 2 {
		t.Fatalf("durable records %v", files)
	}
	if err := q.close(); err != nil {
		t.Fatal(err)
	}
	newWriter := &mockForgeWriter{}
	restarted, err := newForgeAccountingQueue(q.dir, newWriter)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.close()
	if err := restarted.flush(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(newWriter.receipts, []storage.ForgeShare{first, second}) {
		t.Fatal("restart lost receipt/order")
	}
	files, _ = filepath.Glob(filepath.Join(q.dir, "*.json"))
	if len(files) != 0 {
		t.Fatal("acknowledged journal records remain")
	}
	if submitter.dgb != 1 || submitter.doge != 1 || submitter.ogg != 1 {
		t.Fatal("restart re-submitted chains")
	}
}

func TestForgeJournalRejectsSecondOwnerAndCorruption(t *testing.T) {
	dir := t.TempDir()
	q, err := newForgeAccountingQueue(dir, &mockForgeWriter{})
	if err != nil {
		t.Fatal(err)
	}
	if other, err := newForgeAccountingQueue(dir, &mockForgeWriter{}); err == nil {
		other.close()
		t.Fatal("two journal owners")
	}
	q.close()
	if err := os.WriteFile(filepath.Join(dir, "00000000000000000001-bad.json"), []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if other, err := newForgeAccountingQueue(dir, &mockForgeWriter{}); err == nil {
		other.close()
		t.Fatal("corrupt journal silently ignored")
	}
}

func TestForgeRejectedOGGNeverProducesNativeReceipt(t *testing.T) {
	s, cs, job, writer, submitter, nonce := testForgeServer(t)
	submitter.oggErr = errors.New("OGG rejected")
	extra, ntime, n := submitArgs(job, nonce)
	if valid, _, _, err := s.processForgeShare(cs, cs.login, cs.worker, job.ID, extra, ntime, n, time.Now()); err != nil || !valid {
		t.Fatal(err)
	}
	r := writer.receipts[0]
	if r.ProofHash != "" || r.OGGError == "" || !r.Digibyte.Accepted || !r.Dogecoin.Accepted {
		t.Fatalf("bad routing receipt %+v", r)
	}
}

// Simulate Redis committing a write but the TCP reply getting lost. The queue
// must leave the receipt on disk, and restart must safely replay it once.
type lostForgeReply struct {
	backend *storage.RedisClient
	lose    bool
}

func (w *lostForgeReply) WriteForgeShare(s storage.ForgeShare) (bool, error) {
	exists, err := w.backend.WriteForgeShare(s)
	if err == nil && w.lose {
		w.lose = false
		return false, errors.New("lost Redis reply")
	}
	return exists, err
}

func TestForgeRedisLostReplyRestartIntegration(t *testing.T) {
	endpoint := os.Getenv("FORGE_TEST_REDIS_ADDR")
	if endpoint == "" {
		t.Skip("requires isolated FORGE_TEST_REDIS_ADDR")
	}
	s, cs, job, _, submitter, nonce := testForgeServer(t)
	backend := storage.NewRedisClient(&storage.Config{Endpoint: endpoint}, "forge-replay-"+time.Now().Format("150405.000000000"))
	defer backend.Client().Close()
	s.forge.account.writer = &lostForgeReply{backend: backend, lose: true}
	extra, ntime, n := submitArgs(job, nonce)
	if _, _, _, err := s.processForgeShare(cs, cs.login, cs.worker, job.ID, extra, ntime, n, time.Now()); err == nil {
		t.Fatal("expected lost reply")
	}
	dir := s.forge.account.dir
	s.forge.account.close()
	q, err := newForgeAccountingQueue(dir, backend)
	if err != nil {
		t.Fatal(err)
	}
	defer q.close()
	if err := q.flush(); err != nil {
		t.Fatal(err)
	}
	blocks, err := backend.GetCandidates(int64(job.Height))
	if err != nil || len(blocks) != 1 || blocks[0].TotalShares != 1 {
		t.Fatalf("duplicate native round %+v %v", blocks, err)
	}
	shares, err := backend.GetRoundShares(int64(job.Height), blocks[0].Nonce)
	if err != nil || shares[cs.login] != 1 {
		t.Fatalf("duplicate miner shares %v %v", shares, err)
	}
	if submitter.dgb != 1 || submitter.doge != 1 || submitter.ogg != 1 {
		t.Fatal("retry submitted network blocks")
	}
}
