package proxy

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/HawyUkr/open-progpow-pool/rpc"
	"github.com/HawyUkr/open-progpow-pool/storage"
)

type intentTestDaemon struct {
	mu               sync.Mutex
	chain, reply     string
	canonical        bool
	noncanonical     bool
	wrongProof       bool
	submits, lookups int
	payloads         []string
	workHashes       []string
	before           func()
	blockHash, proof string
}

func (d *intentTestDaemon) serve(w http.ResponseWriter, r *http.Request) {
	d.mu.Lock()
	defer d.mu.Unlock()
	var req struct {
		Method string
		Params []json.RawMessage
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil {
		w.WriteHeader(400)
		return
	}
	var result interface{}
	switch req.Method {
	case "submitblock", "eth_submitAuxBlock":
		d.submits++
		var payload string
		index := 0
		if req.Method == "eth_submitAuxBlock" {
			index = 1
			var work string
			json.Unmarshal(req.Params[0], &work)
			d.workHashes = append(d.workHashes, work)
		}
		json.Unmarshal(req.Params[index], &payload)
		d.payloads = append(d.payloads, payload)
		if d.before != nil {
			d.before()
		}
		if d.reply == "lost" {
			w.WriteHeader(503)
			return
		}
		if d.chain == "OGG" {
			result = d.reply != "reject"
		} else if d.reply == "reject" {
			result = "bad-block"
		} else if d.reply == "duplicate" {
			result = "duplicate"
		}
	case "getblock":
		d.lookups++
		if d.noncanonical {
			result = map[string]interface{}{"hash": strings.TrimPrefix(d.blockHash, "0x"), "confirmations": -1, "tx": []string{"coinbase"}}
		}
		if d.canonical {
			result = map[string]interface{}{"hash": strings.TrimPrefix(d.blockHash, "0x"), "confirmations": 1, "tx": []string{"coinbase"}}
		}
	case "eth_getBlockByNumber":
		d.lookups++
		if d.canonical {
			proof := d.proof
			if d.wrongProof {
				proof = "00"
			}
			result = map[string]interface{}{"number": "0xbb8", "hash": "0x" + strings.Repeat("aa", 32), "extraData": "0x" + proof}
		}
	default:
		w.WriteHeader(400)
		return
	}
	json.NewEncoder(w).Encode(map[string]interface{}{"id": 0, "result": result, "error": nil})
}

type intentFixture struct {
	s       *ProxyServer
	cs      *Session
	job     *forgeJob
	sol     *forgeSolution
	router  *rpcForgeSubmitter
	writer  *mockForgeWriter
	receipt storage.ForgeShare
	daemons map[string]*intentTestDaemon
	nonce   uint32
}

func newIntentFixture(t *testing.T) *intentFixture {
	t.Helper()
	s, cs, job, writer, _, nonce := testForgeServer(t)
	templateTestSigner(t, s.forge, nil)
	if err := s.forge.signJob(job); err != nil {
		t.Fatal(err)
	}
	f := &intentFixture{s: s, cs: cs, job: job, writer: writer, nonce: nonce, daemons: make(map[string]*intentTestDaemon)}
	clients := make(map[string]*rpc.RPCClient)
	for _, chain := range []string{"DGB", "DOGE", "OGG"} {
		d := &intentTestDaemon{chain: chain}
		server := httptest.NewServer(http.HandlerFunc(d.serve))
		t.Cleanup(server.Close)
		clients[chain] = rpc.NewRPCClient(chain, server.URL, "1s", "secret-user", "secret-password")
		f.daemons[chain] = d
	}
	job.OGGRPC = clients["OGG"]
	f.router = &rpcForgeSubmitter{dgb: clients["DGB"], doge: clients["DOGE"], issuers: []*rpc.RPCClient{clients["OGG"]}, authorized: s.forge.authorized, quorum: 1}
	s.forge.submitter = f.router
	extra, ntime, n := submitArgs(job, nonce)
	_, sol, _, err := s.validateForgeShare(cs, job.ID, extra, ntime, n, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	f.sol = sol
	f.receipt = storage.ForgeShare{ID: forgePayloadDigest(sol.Header), Login: cs.login, Worker: cs.worker, Height: int64(job.Height), Difficulty: 1, RoundDifficulty: 1, TimestampMS: time.Now().UnixNano() / int64(time.Millisecond), WindowSeconds: 3600, WorkHash: job.OGG.WorkHash}
	in := f.router.prepareIntent(job, sol, forgeHits{Digibyte: true, Dogecoin: true, OGG: true})
	if err := in.validate(f.receipt); err != nil {
		t.Fatal(err)
	}
	for _, b := range in.Blocks {
		f.daemons[b.Chain].blockHash = b.BlockHash
		f.daemons[b.Chain].proof = b.Payload
	}
	return f
}

func (f *intentFixture) submit(t *testing.T) error {
	t.Helper()
	extra, ntime, n := submitArgs(f.job, f.nonce)
	_, _, _, err := f.s.processForgeShare(f.cs, f.cs.login, f.cs.worker, f.job.ID, extra, ntime, n, time.Now())
	return err
}

func (f *intentFixture) restart(t *testing.T) *forgeAccountingQueue {
	t.Helper()
	q := f.s.forge.account
	if err := q.close(); err != nil {
		t.Fatal(err)
	}
	newQ, err := newForgeAccountingQueue(q.dir, f.writer)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { newQ.close() })
	f.s.forge.account = newQ
	return newQ
}

func TestForgeIntentLostRepliesRestartPreservesRoundAndPayload(t *testing.T) {
	f := newIntentFixture(t)
	for _, d := range f.daemons {
		d.reply = "lost"
	}
	if err := f.submit(t); err == nil {
		t.Fatal("uncertain replies credited")
	}
	if f.writer.calls != 0 {
		t.Fatal("unresolved native round reached Redis")
	}
	second := f.receipt
	second.ID = strings.Repeat("ab", 32)
	if _, err := f.s.forge.account.enqueue(second); err == nil {
		t.Fatal("later share bypassed unresolved round")
	}
	for _, d := range f.daemons {
		d.mu.Lock()
		d.canonical = true
		d.mu.Unlock()
	}
	q := f.restart(t)
	if err := q.recoverSubmissions(f.router); err != nil {
		t.Fatal(err)
	}
	if err := q.flush(); err != nil {
		t.Fatal(err)
	}
	if len(f.writer.receipts) != 2 || f.writer.receipts[1] != second {
		t.Fatal("share ordering changed")
	}
	proof, _ := hex.DecodeString(f.daemons["OGG"].proof)
	first := f.writer.receipts[0]
	if first.ProofHash != storage.ForgeProofHash(proof) || !first.Digibyte.Accepted || !first.Dogecoin.Accepted {
		t.Fatalf("lost acknowledgement %+v", first)
	}
	for _, d := range f.daemons {
		if d.submits != 1 || d.lookups != 1 {
			t.Fatalf("acknowledged block re-submitted %+v", d)
		}
	}
	if f.daemons["OGG"].workHashes[0] != f.job.OGG.WorkHash {
		t.Fatal("wrong issued work")
	}
}

func TestForgeIntentIsDurableBeforeRPCAndCrashAfterAcceptance(t *testing.T) {
	f := newIntentFixture(t)
	var crashBlob []byte
	var crashPath string
	f.daemons["DGB"].before = func() {
		files, _ := filepath.Glob(filepath.Join(f.s.forge.account.dir, "*.json"))
		if len(files) != 1 {
			t.Error("missing pre-submission journal")
			return
		}
		crashPath = files[0]
		crashBlob, _ = os.ReadFile(crashPath)
		var record forgeJournalRecord
		if json.Unmarshal(crashBlob, &record) != nil || record.Intent == nil || !record.Intent.Blocks[0].Sent {
			t.Error("send marker missing")
		}
		if strings.Contains(string(crashBlob), "secret-") || strings.Contains(string(crashBlob), f.router.dgb.Url) {
			t.Error("credentials/URL persisted")
		}
	}
	f.writer.err = errors.New("Redis offline")
	if err := f.submit(t); err == nil {
		t.Fatal("missing Redis failure")
	}
	// Recreate exactly the disk state at daemon acceptance, before its reply is
	// journaled. A restarted process has no in-memory solution or acknowledgement.
	if err := os.WriteFile(crashPath, crashBlob, 0600); err != nil {
		t.Fatal(err)
	}
	f.daemons["DGB"].mu.Lock()
	f.daemons["DGB"].canonical = true
	f.daemons["DGB"].before = nil
	f.daemons["DGB"].mu.Unlock()
	f.writer.err = nil
	f.writer.receipts = nil
	f.writer.calls = 0
	q := f.restart(t)
	if err := q.recoverSubmissions(f.router); err != nil {
		t.Fatal(err)
	}
	if err := q.flush(); err != nil {
		t.Fatal(err)
	}
	if f.daemons["DGB"].submits != 1 || f.daemons["DOGE"].submits != 2 || f.daemons["OGG"].submits != 2 || len(f.writer.receipts) != 1 {
		t.Fatal("crash recovery skipped unsent intents")
	}
	if !reflect.DeepEqual(f.daemons["OGG"].payloads[0], f.daemons["OGG"].payloads[1]) {
		t.Fatal("proof changed during restart")
	}
}

func TestForgeIntentUnknownThenRejectedOrExpiredRemainsPending(t *testing.T) {
	f := newIntentFixture(t)
	f.daemons["OGG"].reply = "lost"
	if err := f.submit(t); err == nil {
		t.Fatal("missing uncertain result")
	}
	d := f.daemons["OGG"]
	d.mu.Lock()
	d.reply = "reject"
	d.canonical = true
	d.wrongProof = true
	d.mu.Unlock()
	q := f.restart(t)
	if err := q.recoverSubmissions(f.router); err != nil {
		t.Fatal(err)
	}
	if err := q.flush(); err == nil || f.writer.calls != 0 {
		t.Fatal("retry false or wrong proof treated as definitive rejection")
	}
	f.router.clock = func() time.Time { return time.Unix(int64(f.job.Signed.Template.Expires), 0) }
	if err := q.recoverSubmissions(f.router); err != nil {
		t.Fatal(err)
	}
	if d.submits != 2 || f.writer.calls != 0 {
		t.Fatal("expired proof submitted/credited")
	}
	d.mu.Lock()
	d.wrongProof = false
	d.mu.Unlock()
	if err := q.recoverSubmissions(f.router); err != nil {
		t.Fatal(err)
	}
	if err := q.flush(); err != nil {
		t.Fatal(err)
	}
	if f.writer.receipts[0].ProofHash == "" {
		t.Fatal("historical accepted proof lost after expiry")
	}
}

func TestForgeIntentFirstRejectDoesNotPreventOtherChainsOrShare(t *testing.T) {
	f := newIntentFixture(t)
	f.daemons["DGB"].reply = "reject"
	if err := f.submit(t); err != nil {
		t.Fatal(err)
	}
	if len(f.writer.receipts) != 1 {
		t.Fatal("share missing")
	}
	r := f.writer.receipts[0]
	if r.Digibyte.Accepted || r.Digibyte.Error == "" || !r.Dogecoin.Accepted || r.ProofHash == "" {
		t.Fatalf("wrong outcomes %+v", r)
	}
}

func TestForgeIntentTemplateExpiryDuringOtherChainSubmission(t *testing.T) {
	f := newIntentFixture(t)
	var expired int32
	f.router.clock = func() time.Time {
		if atomic.LoadInt32(&expired) != 0 {
			return time.Unix(int64(f.job.Signed.Template.Expires), 0)
		}
		return time.Now()
	}
	f.daemons["DGB"].before = func() { atomic.StoreInt32(&expired, 1) }
	if err := f.submit(t); err != nil {
		t.Fatal(err)
	}
	if f.daemons["DGB"].submits != 1 || f.daemons["DOGE"].submits != 1 || f.daemons["OGG"].submits != 0 {
		t.Fatal("expired OGG sent or external chain skipped")
	}
	if f.writer.calls != 1 || f.writer.receipts[0].ProofHash != "" || f.writer.receipts[0].OGGError == "" {
		t.Fatal("expired proof credited as native block")
	}
}

func TestForgeIntentSingleChainTargetsAndAllTargets(t *testing.T) {
	for _, hits := range []forgeHits{{Digibyte: true}, {Dogecoin: true}, {OGG: true}, {Digibyte: true, Dogecoin: true, OGG: true}} {
		t.Run(fmt.Sprint(hits), func(t *testing.T) {
			f := newIntentFixture(t)
			valid, _, _, err := f.s.processForgeIntent(f.router, f.job, f.sol, hits, f.receipt)
			if err != nil || !valid || f.writer.calls != 1 {
				t.Fatal(valid, err, f.writer.calls)
			}
			for chain, want := range map[string]bool{"DGB": hits.Digibyte, "DOGE": hits.Dogecoin, "OGG": hits.OGG} {
				if (f.daemons[chain].submits == 1) != want {
					t.Fatal("incorrect routing", chain)
				}
			}
		})
	}
}

func TestForgeIntentDiskFailureDefersAllSubmissions(t *testing.T) {
	f := newIntentFixture(t)
	dir := f.s.forge.account.dir
	if err := os.Rename(dir, dir+"-saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir, []byte("disk unavailable"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := f.submit(t); err == nil {
		t.Fatal("durability loss not reported")
	}
	for _, d := range f.daemons {
		if d.submits != 0 {
			t.Fatal("submission preceded durable send marker")
		}
	}
	if f.writer.calls != 0 {
		t.Fatal("nondurable receipt sent to Redis")
	}
	status := f.s.forge.account.status(time.Now())
	if status.UndurableRecords != 1 || status.PersistenceFailures == 0 || status.Health != "critical" || !status.WorkPaused {
		t.Fatal("disk failure hidden from operator", status)
	}
	os.Remove(dir)
	if err := os.Rename(dir+"-saved", dir); err != nil {
		t.Fatal(err)
	}
	if err := f.s.forge.account.recoverSubmissions(f.router); err != nil {
		t.Fatal(err)
	}
	if err := f.s.forge.account.flush(); err != nil {
		t.Fatal(err)
	}
	if f.writer.calls != 1 {
		t.Fatal("in-memory recovery failed")
	}
}

func TestForgeIntentFailedSendMarkerRestartRetainsUnsentIntent(t *testing.T) {
	f := newIntentFixture(t)
	q := f.s.forge.account
	in := f.router.prepareIntent(f.job, f.sol, forgeHits{Digibyte: true, Dogecoin: true, OGG: true})
	item, err := q.beginIntent(f.receipt, in)
	if err != nil {
		t.Fatal(err)
	}
	// Preserve the durable unsent record, then make every subsequent write fail.
	dir := q.dir
	if err := os.Rename(dir, dir+"-saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir, []byte("disk unavailable"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := q.runIntent(item, f.router); err == nil {
		t.Fatal("send marker failure hidden")
	}
	for _, d := range f.daemons {
		if d.submits != 0 {
			t.Fatal("daemon received a submission without a durable marker")
		}
	}
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(dir+"-saved", dir); err != nil {
		t.Fatal(err)
	}
	q = f.restart(t)
	if err := q.recoverSubmissions(f.router); err != nil {
		t.Fatal(err)
	}
	if err := q.flush(); err != nil {
		t.Fatal(err)
	}
	for _, d := range f.daemons {
		if d.submits != 1 {
			t.Fatal("unsent submission not recovered exactly once")
		}
	}
	if len(f.writer.receipts) != 1 || f.writer.receipts[0].ProofHash == "" {
		t.Fatal("recovered native candidate not credited")
	}
}

func TestForgeIntentEndpointChangeCannotRedirectRecovery(t *testing.T) {
	f := newIntentFixture(t)
	f.daemons["OGG"].reply = "lost"
	if err := f.submit(t); err == nil {
		t.Fatal("expected unknown outcome")
	}
	q := f.restart(t)
	original := f.router.issuers
	f.router.issuers = []*rpc.RPCClient{f.router.dgb}
	if err := q.recoverSubmissions(f.router); err != nil {
		t.Fatal(err)
	}
	if f.daemons["DGB"].submits != 1 || f.daemons["OGG"].submits != 1 || f.writer.calls != 0 {
		t.Fatal("issuer changed")
	}
	f.router.issuers = original
	f.daemons["OGG"].mu.Lock()
	f.daemons["OGG"].reply = ""
	f.daemons["OGG"].mu.Unlock()
	if err := q.recoverSubmissions(f.router); err != nil {
		t.Fatal(err)
	}
	if err := q.flush(); err != nil {
		t.Fatal(err)
	}
	if f.daemons["OGG"].payloads[0] != f.daemons["OGG"].payloads[1] {
		t.Fatal("retry proof changed")
	}
}

type blockedForgeWriter struct {
	started, release chan struct{}
	once             sync.Once
}

func (w *blockedForgeWriter) WriteForgeShare(storage.ForgeShare) (bool, error) {
	w.once.Do(func() { close(w.started) })
	<-w.release
	return false, nil
}

func TestForgeSlowRedisCannotHoldSubmissionLock(t *testing.T) {
	f := newIntentFixture(t)
	w := &blockedForgeWriter{started: make(chan struct{}), release: make(chan struct{})}
	f.s.forge.account.writer = w
	old := f.receipt
	old.ID = strings.Repeat("bb", 32)
	oldDone := make(chan error, 1)
	go func() { _, err := f.s.forge.account.enqueue(old); oldDone <- err }()
	<-w.started
	if err := f.s.forge.account.writeStatus(time.Now()); err != nil {
		t.Fatal("Redis stall blocked status", err)
	}
	atOGG := make(chan struct{})
	f.daemons["OGG"].before = func() { close(atOGG) }
	done := make(chan error, 1)
	go func() { done <- f.submit(t) }()
	select {
	case <-atOGG:
	case <-time.After(3 * time.Second):
		close(w.release)
		t.Fatal("Redis blocked routing")
	}
	close(w.release)
	if err := <-oldDone; err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestForgeIntentRestartBeforeAnyRPCAndActiveRecoveryExclusion(t *testing.T) {
	f := newIntentFixture(t)
	in := f.router.prepareIntent(f.job, f.sol, forgeHits{Digibyte: true, Dogecoin: true, OGG: true})
	if _, err := f.s.forge.account.beginIntent(f.receipt, in); err != nil {
		t.Fatal(err)
	}
	// An active foreground intent is never claimed by the background worker.
	if err := f.s.forge.account.recoverSubmissions(f.router); err != nil {
		t.Fatal(err)
	}
	for _, d := range f.daemons {
		if d.submits != 0 {
			t.Fatal("concurrent recovery raced foreground")
		}
	}
	q := f.restart(t)
	if err := q.recoverSubmissions(f.router); err != nil {
		t.Fatal(err)
	}
	if err := q.flush(); err != nil {
		t.Fatal(err)
	}
	for _, d := range f.daemons {
		if d.submits != 1 || d.lookups != 0 {
			t.Fatal("unsent restart failed")
		}
	}
}

func TestForgeIntentRejectsCorruptPayloadAndReadsLegacyJournal(t *testing.T) {
	f := newIntentFixture(t)
	q := f.s.forge.account
	in := f.router.prepareIntent(f.job, f.sol, forgeHits{Digibyte: true, Dogecoin: true, OGG: true})
	item, err := q.beginIntent(f.receipt, in)
	if err != nil {
		t.Fatal(err)
	}
	q.close()
	in.Blocks[0].Payload += "00"
	blob, _ := json.Marshal(forgeJournalRecord{Version: 1, Receipt: f.receipt, Intent: in})
	if err := os.WriteFile(item.path, blob, 0600); err != nil {
		t.Fatal(err)
	}
	if other, err := newForgeAccountingQueue(q.dir, f.writer); err == nil {
		other.close()
		t.Fatal("corrupt payload loaded")
	}
	blob, _ = json.Marshal(forgeJournalRecord{Version: 99, Receipt: f.receipt})
	os.WriteFile(item.path, blob, 0600)
	if other, err := newForgeAccountingQueue(q.dir, f.writer); err == nil {
		other.close()
		t.Fatal("unknown version loaded")
	}
	// 5D.1-5D.5 plain receipt files remain readable and retain their identity.
	blob, _ = json.Marshal(f.receipt)
	os.WriteFile(item.path, blob, 0600)
	legacy, err := newForgeAccountingQueue(q.dir, f.writer)
	if err != nil {
		t.Fatal(err)
	}
	defer legacy.close()
	if err := legacy.flush(); err != nil {
		t.Fatal(err)
	}
	if f.writer.calls != 1 || f.writer.receipts[0] != f.receipt {
		t.Fatal("legacy receipt changed")
	}
}

func TestForgeIntentLostRedisReplyRestartIntegration(t *testing.T) {
	endpoint := os.Getenv("FORGE_TEST_REDIS_ADDR")
	if endpoint == "" {
		t.Skip("requires isolated FORGE_TEST_REDIS_ADDR")
	}
	f := newIntentFixture(t)
	backend := storage.NewRedisClient(&storage.Config{Endpoint: endpoint}, "forge-intent-replay-"+time.Now().Format("150405.000000000"))
	defer backend.Client().Close()
	f.s.forge.account.writer = &lostForgeReply{backend: backend, lose: true}
	if err := f.submit(t); err == nil {
		t.Fatal("lost Redis reply not reported")
	}
	q := f.restart(t)
	q.writer = backend
	if err := q.recoverSubmissions(f.router); err != nil {
		t.Fatal(err)
	}
	if err := q.flush(); err != nil {
		t.Fatal(err)
	}
	blocks, err := backend.GetCandidates(int64(f.job.Height))
	if err != nil || len(blocks) != 1 || blocks[0].TotalShares != 1 {
		t.Fatalf("native round duplicated: %+v %v", blocks, err)
	}
	shares, err := backend.GetRoundShares(int64(f.job.Height), blocks[0].Nonce)
	if err != nil || shares[f.cs.login] != 1 {
		t.Fatal("miner share duplicated", shares, err)
	}
	for _, d := range f.daemons {
		if d.submits != 1 || d.lookups != 0 {
			t.Fatal("final receipt resubmitted after Redis lost reply")
		}
	}
}
