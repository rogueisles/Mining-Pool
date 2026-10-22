package proxy

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/HawyUkr/open-progpow-pool/storage"
	"os"
	"testing"
	"time"
)

func TestForgeNoncanonicalObservationRetainsEvidenceAndCanBecomeCanonical(t *testing.T) {
	f := newIntentFixture(t)
	q := f.s.forge.account
	w := &creditTestWriter{}
	q.writer = w
	d := f.daemons["DGB"]
	d.reply = "lost"
	if _, _, _, err := f.s.processForgeIntent(f.router, f.job, f.sol, forgeHits{Digibyte: true}, f.receipt); err != nil {
		t.Fatal(err)
	}
	payload := q.pending[0].intent.Blocks[0].Payload
	d.mu.Lock()
	d.noncanonical = true
	d.mu.Unlock()
	if err := q.recoverSubmissions(f.router); err != nil {
		t.Fatal(err)
	}
	if err := q.flush(); err != nil {
		t.Fatal(err)
	}
	s := q.status(time.Now())
	if s.UncreditedRecords != 0 || s.PendingIntents != 0 || s.ObservedIntents != 1 || s.NoncanonicalChains["DGB"] != 1 || s.UnresolvedChains["DGB"] != 0 || s.WorkPaused {
		t.Fatalf("wrong observation status %+v", s)
	}
	if len(w.receipts) != 1 || len(w.updates) != 1 || w.updates[0].Digibyte.Accepted || w.updates[0].Digibyte.Observation != "noncanonical" {
		t.Fatal("observation credited or treated as acceptance")
	}
	path := q.pending[0].path
	blob, _ := os.ReadFile(path)
	var record forgeJournalRecord
	if json.Unmarshal(blob, &record) != nil || record.Version != 3 || record.Intent.Blocks[0].Payload != payload {
		t.Fatal("evidence lost")
	}
	dir := q.dir
	q.close()
	var err error
	q, err = newForgeAccountingQueue(dir, w)
	if err != nil {
		t.Fatal(err)
	}
	defer q.close()
	if err = q.recoverSubmissions(f.router); err != nil {
		t.Fatal(err)
	}
	if err = q.flush(); err != nil {
		t.Fatal(err)
	}
	if len(w.receipts) != 1 || len(w.updates) != 1 {
		t.Fatal("restart rewrote unchanged observation or duplicated credit")
	}
	d.mu.Lock()
	d.canonical = true
	d.mu.Unlock()
	if err = q.recoverSubmissions(f.router); err != nil {
		t.Fatal(err)
	}
	if err = q.flush(); err != nil {
		t.Fatal(err)
	}
	if len(q.pending) != 0 || len(w.receipts) != 1 || len(w.updates) != 2 || !w.updates[1].Digibyte.Accepted || w.updates[1].Digibyte.Observation != "" {
		t.Fatal("late canonical evidence not reconciled separately")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.submits != 1 {
		t.Fatal("observed branch was resubmitted")
	}
}

func TestForgeNativeUnknownStillBlocksDespiteExternalObservation(t *testing.T) {
	f := newIntentFixture(t)
	q := f.s.forge.account
	for _, d := range f.daemons {
		d.reply = "lost"
	}
	if err := f.submit(t); err == nil {
		t.Fatal("uncertain native block credited")
	}
	d := f.daemons["DGB"]
	d.mu.Lock()
	d.noncanonical = true
	d.mu.Unlock()
	if err := q.recoverSubmissions(f.router); err != nil {
		t.Fatal(err)
	}
	if err := q.flush(); err == nil || f.writer.calls != 0 {
		t.Fatal("external observation bypassed native barrier")
	}
}

func TestForgeObservationRetainsCapacityLimit(t *testing.T) {
	f := newIntentFixture(t)
	q := f.s.forge.account
	q.writer = &creditTestWriter{}
	f.daemons["DGB"].reply = "lost"
	if _, _, _, err := f.s.processForgeIntent(f.router, f.job, f.sol, forgeHits{Digibyte: true}, f.receipt); err != nil {
		t.Fatal(err)
	}
	f.daemons["DGB"].mu.Lock()
	f.daemons["DGB"].noncanonical = true
	f.daemons["DGB"].mu.Unlock()
	q.recoverSubmissions(f.router)
	q.flush()
	q.limits.MaxPendingRecords = 1
	if !q.workPaused() {
		t.Fatal("retained evidence bypassed memory/disk capacity bound")
	}
}

type lostObservationReply struct {
	backend *storage.RedisClient
	lose    bool
}

func (w *lostObservationReply) WriteForgeShare(s storage.ForgeShare) (bool, error) {
	return w.backend.WriteForgeShare(s)
}
func (w *lostObservationReply) WriteForgeExternalOutcomes(s storage.ForgeShare) error {
	if err := w.backend.WriteForgeExternalOutcomes(s); err != nil {
		return err
	}
	if w.lose {
		w.lose = false
		return errors.New("observation commit reply lost")
	}
	return nil
}
func TestForgeNoncanonicalRealRedisLostReplyAndAOFReplay(t *testing.T) {
	if os.Getenv("FORGE_PHASE6_REDIS_SERVER") == "" {
		t.Skip("requires disposable Redis binary")
	}
	redis := newPhase6Redis(t)
	f := newIntentFixture(t)
	q := f.s.forge.account
	q.writer = redis.backend
	d := f.daemons["DGB"]
	d.reply = "lost"
	if _, _, _, err := f.s.processForgeIntent(f.router, f.job, f.sol, forgeHits{Digibyte: true}, f.receipt); err != nil {
		t.Fatal(err)
	}
	d.mu.Lock()
	d.noncanonical = true
	d.mu.Unlock()
	if err := q.recoverSubmissions(f.router); err != nil {
		t.Fatal(err)
	}
	q.writer = &lostObservationReply{backend: redis.backend, lose: true}
	if err := q.flush(); err == nil {
		t.Fatal("lost observation reply acknowledged")
	}
	dir := q.dir
	q.close()
	redis.stop()
	redis.start()
	var err error
	q, err = newForgeAccountingQueue(dir, redis.backend)
	if err != nil {
		t.Fatal(err)
	}
	defer q.close()
	if err = q.flush(); err != nil {
		t.Fatal(err)
	}
	c := redis.backend.Client()
	key := fmt.Sprintf("forge-phase6:forge:submissions:dgb:%d", f.receipt.Height)
	blob, err := c.HGet(key, f.receipt.ID).Result()
	var observed storage.ForgeSubmission
	if err != nil || json.Unmarshal([]byte(blob), &observed) != nil || observed.Accepted || observed.Observation != "noncanonical" {
		t.Fatal("observation lost or invented acceptance", err)
	}
	if n := c.ZCard("forge-phase6:hashrate").Val(); n != 1 {
		t.Fatal("duplicate credit", n)
	}
	if n := c.ZCard("forge-phase6:blocks:candidates").Val(); n != 0 {
		t.Fatal("native candidate invented", n)
	}
	d.mu.Lock()
	d.canonical = true
	d.mu.Unlock()
	if err = q.recoverSubmissions(f.router); err != nil {
		t.Fatal(err)
	}
	if err = q.flush(); err != nil {
		t.Fatal(err)
	}
	if len(q.pending) != 0 || c.ZCard("forge-phase6:hashrate").Val() != 1 {
		t.Fatal("late canonical recovery changed credit")
	}
	assertPhase6ExternalAccounting(t, redis.backend, f.receiptWithAcceptedDGB(), "dgb")
}
func (f *intentFixture) receiptWithAcceptedDGB() storage.ForgeShare {
	s := f.receipt
	s.Digibyte = storage.ForgeSubmission{Attempted: true, Accepted: true, BlockHash: f.daemons["DGB"].blockHash}
	return s
}

func TestForgeNoncanonicalStateCannotApplyToOGGOrUnsentProof(t *testing.T) {
	f := newIntentFixture(t)
	for _, chain := range []string{"DGB", "OGG"} {
		in := f.router.prepareIntent(f.job, f.sol, forgeHits{Digibyte: chain == "DGB", OGG: chain == "OGG"})
		in.Blocks[0].State = "noncanonical"
		in.Blocks[0].Error = "observed"
		if chain == "OGG" {
			in.Blocks[0].Sent = true
		}
		if err := in.validate(f.receipt); err == nil {
			t.Fatal("invalid observation accepted", chain)
		}
	}
}
