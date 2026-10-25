package proxy

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HawyUkr/open-progpow-pool/storage"
)

type resolvedReplayWriter struct {
	dir               string
	seen              map[string]bool
	credits, attempts int
	interrupt         bool
	interruptAt       int
}

func (w *resolvedReplayWriter) WriteForgeShare(s storage.ForgeShare) (bool, error) {
	w.attempts++
	if w.seen[s.ID] {
		return true, nil
	}
	w.seen[s.ID] = true
	w.credits++
	if w.interrupt || (w.interruptAt > 0 && w.credits == w.interruptAt) {
		w.interrupt = false
		if err := os.Rename(w.dir, w.dir+"-interrupted"); err != nil {
			return false, err
		}
	}
	return false, nil
}

func TestResolvedCleanupBatchFailureReplaysExactlyOnce(t *testing.T) {
	f := newIntentFixture(t)
	dir := filepath.Join(t.TempDir(), "receipts")
	w := &resolvedReplayWriter{dir: dir, seen: map[string]bool{}, interruptAt: 64}
	q, err := newForgeAccountingQueue(dir, w)
	if err != nil {
		t.Fatal(err)
	}
	for n := 0; n < 128; n++ {
		s := f.receipt
		s.ID = fmt.Sprintf("%064x", n+1)
		s.TimestampMS += int64(n)
		if _, err = q.enqueueDurable(s); err != nil {
			t.Fatal(err)
		}
	}
	if err = q.flush(); err == nil {
		t.Fatal("batch cleanup failure ignored")
	}
	if err = q.close(); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(dir+"-interrupted", dir); err != nil {
		t.Fatal(err)
	}
	q, err = newForgeAccountingQueue(dir, w)
	if err != nil {
		t.Fatal(err)
	}
	defer q.close()
	if err = q.flush(); err != nil {
		t.Fatal(err)
	}
	if w.credits != 128 || len(w.seen) != 128 || w.attempts != 129 || len(q.pending) != 0 {
		t.Fatal("batched cleanup lost or double credited a share", w.credits, w.attempts, len(q.pending))
	}
}

func TestResolvedDurableReceiptReplaysOnceAfterRemovalFailure(t *testing.T) {
	f := newIntentFixture(t)
	dir := filepath.Join(t.TempDir(), "receipts")
	w := &resolvedReplayWriter{dir: dir, seen: map[string]bool{}, interrupt: true}
	q, err := newForgeAccountingQueue(dir, w)
	if err != nil {
		t.Fatal(err)
	}
	s := f.receipt
	s.ID = strings.Repeat("de", 32)
	if _, err = q.enqueueDurable(s); err != nil {
		t.Fatal(err)
	}
	if err = q.flush(); err == nil {
		t.Fatal("removal durability failure was ignored")
	}
	if err = q.close(); err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob(dir + "-interrupted/*.json")
	if err != nil || len(files) != 1 {
		t.Fatal("admission journal lost", err)
	}
	b, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	var record forgeJournalRecord
	if err = json.Unmarshal(b, &record); err != nil {
		t.Fatal(err)
	}
	if record.Credited || record.Credit != nil {
		t.Fatal("resolved credit unnecessarily rewrote admission journal")
	}
	if err = os.Rename(dir+"-interrupted", dir); err != nil {
		t.Fatal(err)
	}
	q, err = newForgeAccountingQueue(dir, w)
	if err != nil {
		t.Fatal(err)
	}
	defer q.close()
	if err = q.flush(); err != nil {
		t.Fatal(err)
	}
	if w.credits != 1 || w.attempts != 2 || len(q.pending) != 0 {
		t.Fatal("crash replay duplicated credit or lost receipt")
	}
}
