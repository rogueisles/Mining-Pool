package proxy

import (
	"archive/zip"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/HawyUkr/open-progpow-pool/storage"
)

type creditTestWriter struct {
	receipts []storage.ForgeShare
	updates  []storage.ForgeShare
	seen     map[string]bool
	lose     bool
}

func (w *creditTestWriter) WriteForgeShare(s storage.ForgeShare) (bool, error) {
	if w.seen == nil {
		w.seen = make(map[string]bool)
	}
	if w.seen[s.ID] {
		return true, nil
	}
	w.seen[s.ID] = true
	w.receipts = append(w.receipts, s)
	if w.lose {
		w.lose = false
		return false, errors.New("committed Redis reply lost")
	}
	return false, nil
}
func (w *creditTestWriter) WriteForgeExternalOutcomes(s storage.ForgeShare) error {
	if !w.seen[s.ID] {
		return errors.New("uncredited")
	}
	w.updates = append(w.updates, s)
	return nil
}

func copyVPSReceipts(t *testing.T, dir string) []string {
	t.Helper()
	archive, err := zip.OpenReader("testdata/forge-vps-51-receipts.zip")
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	ids := []string{}
	for _, file := range archive.File {
		reader, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		blob, err := ioutil.ReadAll(reader)
		reader.Close()
		if err != nil {
			t.Fatal(err)
		}
		// This isolated DGB profile uses a fresh journal. Adapt only the historic
		// test archive's first-parent labels; production never imports D12 records.
		blob = []byte(strings.ReplaceAll(strings.ReplaceAll(string(blob), "\"LTC\"", "\"DGB\""), "\"Litecoin\"", "\"Digibyte\""))
		blob = []byte(strings.ReplaceAll(string(blob), "\"litecoin\"", "\"digibyte\""))
		var record forgeJournalRecord
		if err = json.Unmarshal(blob, &record); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, record.Receipt.ID)
		if err = os.WriteFile(filepath.Join(dir, filepath.Base(file.Name)), blob, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if len(ids) != 51 {
		t.Fatal("wrong VPS fixture size")
	}
	return ids
}
func assertVPSRetained(t *testing.T, q *forgeAccountingQueue) {
	t.Helper()
	s := q.status(time.Now().Add(24 * time.Hour))
	if s.UncreditedRecords != 0 || s.PendingRecords != 18 || s.PendingIntents != 18 || s.WorkPaused || s.UnresolvedChains["DGB"] != 18 || s.UnresolvedChains["DOGE"] != 4 || s.UnresolvedChains["OGG"] != 0 || s.Health != "degraded" {
		t.Fatalf("wrong replay status %+v", s)
	}
	for _, item := range q.pending {
		if !item.credited || item.credit.ProofHash != "" || item.intent == nil {
			t.Fatal("lost evidence or fabricated native block")
		}
	}
}
func TestForgeVPS51ReceiptsCreditWithoutInventingExternalAcceptance(t *testing.T) {
	dir := t.TempDir()
	ids := copyVPSReceipts(t, dir)
	w := &creditTestWriter{}
	q, err := newForgeAccountingQueue(dir, w)
	if err != nil {
		t.Fatal(err)
	}
	if err = q.flush(); err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, s := range w.receipts {
		got = append(got, s.ID)
		if s.ProofHash != "" {
			t.Fatal("native block invented")
		}
	}
	if !reflect.DeepEqual(got, ids) {
		t.Fatal("lost, duplicated or reordered credit")
	}
	assertVPSRetained(t, q)
	q.close()
	q, err = newForgeAccountingQueue(dir, w)
	if err != nil {
		t.Fatal(err)
	}
	defer q.close()
	if err = q.flush(); err != nil {
		t.Fatal(err)
	}
	if len(w.receipts) != 51 || len(w.updates) != 0 {
		t.Fatal("restart replay changed credits/outcomes")
	}
	assertVPSRetained(t, q)
	// Evidence remains subject to the count/byte bound, despite the age exemption.
	q.limits.MaxPendingRecords = 18
	if !q.workPaused() {
		t.Fatal("external backlog bypassed capacity bound")
	}
}
func TestForgeExternalCreditLostReplyRestartAndLaterResolution(t *testing.T) {
	f := newIntentFixture(t)
	q := f.s.forge.account
	w := &creditTestWriter{lose: true}
	q.writer = w
	f.daemons["DGB"].reply = "lost"
	_, _, _, err := f.s.processForgeIntent(f.router, f.job, f.sol, forgeHits{Digibyte: true}, f.receipt)
	if err == nil || len(w.receipts) != 1 || q.pending[0].credited {
		t.Fatal("lost reply not retained")
	}
	dir := q.dir
	q.close()
	q, err = newForgeAccountingQueue(dir, w)
	if err != nil {
		t.Fatal(err)
	}
	defer q.close()
	f.s.forge.account = q
	if err = q.flush(); err != nil {
		t.Fatal(err)
	}
	if len(w.receipts) != 1 || !q.pending[0].credited {
		t.Fatal("credit replay not exactly once")
	}
	// A busy recovery pass on this external record must not obstruct later shares.
	q.pending[0].active = true
	later := pressureReceipt(90)
	if _, err = q.enqueue(later); err != nil {
		t.Fatal(err)
	}
	q.pending[0].active = false
	if len(w.receipts) != 2 {
		t.Fatal("external recovery blocked later credit")
	}
	f.daemons["DGB"].mu.Lock()
	f.daemons["DGB"].canonical = true
	f.daemons["DGB"].mu.Unlock()
	if err = q.recoverSubmissions(f.router); err != nil {
		t.Fatal(err)
	}
	if err = q.flush(); err != nil {
		t.Fatal(err)
	}
	if len(w.receipts) != 2 || len(w.updates) != 1 || !w.updates[0].Digibyte.Accepted || len(q.pending) != 0 {
		t.Fatal("external resolution changed credit or lost outcome")
	}
}
func TestForgeAcceptedOGGRoundDoesNotWaitForExternalUncertainty(t *testing.T) {
	f := newIntentFixture(t)
	q := f.s.forge.account
	w := &creditTestWriter{}
	q.writer = w
	f.daemons["DGB"].reply = "lost"
	f.daemons["DOGE"].reply = "lost"
	if err := f.submit(t); err != nil {
		t.Fatal(err)
	}
	proof, _ := hex.DecodeString(f.daemons["OGG"].proof)
	if len(w.receipts) != 1 || w.receipts[0].ProofHash != storage.ForgeProofHash(proof) {
		t.Fatal("native round was not credited")
	}
	if _, err := q.enqueue(pressureReceipt(91)); err != nil {
		t.Fatal(err)
	}
	dir := q.dir
	q.close()
	var err error
	q, err = newForgeAccountingQueue(dir, w)
	if err != nil {
		t.Fatal(err)
	}
	defer q.close()
	for _, chain := range []string{"DGB", "DOGE"} {
		d := f.daemons[chain]
		d.mu.Lock()
		d.canonical = true
		d.mu.Unlock()
	}
	if err = q.recoverSubmissions(f.router); err != nil {
		t.Fatal(err)
	}
	if err = q.flush(); err != nil {
		t.Fatal(err)
	}
	if len(w.receipts) != 2 || len(w.updates) != 1 || len(q.pending) != 0 {
		t.Fatal("native round replayed during external update")
	}
}
func TestForgeFrozenCreditCorruptionRejected(t *testing.T) {
	for _, kind := range []string{"old-version", "missing-credit", "changed-native"} {
		t.Run(kind, func(t *testing.T) {
			s := pressureReceipt(0)
			credit := s
			row := forgeJournalRecord{Version: 2, Receipt: s, Credit: &credit, Credited: true}
			switch kind {
			case "old-version":
				row.Version = 1
			case "missing-credit":
				row.Credit = nil
			case "changed-native":
				credit.ProofHash = "0x" + strings.Repeat("ab", 32)
			}
			dir := t.TempDir()
			blob, _ := json.Marshal(row)
			os.WriteFile(filepath.Join(dir, fmt.Sprintf("%020d-%s.json", 1, s.ID)), blob, 0600)
			q, err := newForgeAccountingQueue(dir, &creditTestWriter{})
			if err == nil {
				q.close()
				t.Fatal("corrupt frozen credit accepted")
			}
		})
	}
}
func TestForgeVPS51RealRedisCrashReplay(t *testing.T) {
	if os.Getenv("FORGE_PHASE6_REDIS_SERVER") == "" {
		t.Skip("requires disposable Redis binary")
	}
	redis := newPhase6Redis(t)
	dir := t.TempDir()
	ids := copyVPSReceipts(t, dir)
	q, err := newForgeAccountingQueue(dir, redis.backend)
	if err != nil {
		t.Fatal(err)
	}
	if err = q.flush(); err != nil {
		t.Fatal(err)
	}
	assertVPSRetained(t, q)
	q.close()
	redis.stop()
	redis.start()
	q, err = newForgeAccountingQueue(dir, redis.backend)
	if err != nil {
		t.Fatal(err)
	}
	defer q.close()
	if err = q.flush(); err != nil {
		t.Fatal(err)
	}
	assertVPSRetained(t, q)
	c := redis.backend.Client()
	if got := c.ZCard("forge-phase6:hashrate").Val(); got != int64(len(ids)) {
		t.Fatalf("share count %d", got)
	}
	if got := c.HGet("forge-phase6:shares:roundCurrent", q.pending[0].receipt.Login).Val(); got != "51" {
		t.Fatalf("round shares %s", got)
	}
	if got := c.ZCard("forge-phase6:blocks:candidates").Val(); got != 0 {
		t.Fatal("native candidate fabricated")
	}
	if got := c.HLen("forge-phase6:forge:submissions:dgb:1").Val(); got != 51 {
		t.Fatalf("DGB observations %d", got)
	}
	if got := c.HLen("forge-phase6:forge:submissions:doge:1").Val(); got != 51 {
		t.Fatalf("DOGE observations %d", got)
	}
	t.Log("51 unique credits; 18 retained intents; 22 unresolved external outcomes; zero native candidates; Redis AOF crash/restart passed")
}
