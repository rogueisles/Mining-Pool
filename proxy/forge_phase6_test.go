package proxy

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/HawyUkr/open-progpow-pool/storage"
)

// This harness owns its Redis process, random password, datadir and namespace.
// It cannot take an existing Redis URL or shut down a process it did not start.
type phase6Redis struct {
	t                           *testing.T
	binary, dir, port, password string
	cmd                         *exec.Cmd
	backend                     *storage.RedisClient
	log                         *os.File
}

func newPhase6Redis(t *testing.T) *phase6Redis {
	t.Helper()
	binary := os.Getenv("FORGE_PHASE6_REDIS_SERVER")
	if !filepath.IsAbs(binary) {
		t.Fatal("absolute disposable Redis binary required")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)
	listener.Close()
	secret := make([]byte, 24)
	if _, err := rand.Read(secret); err != nil {
		t.Fatal(err)
	}
	r := &phase6Redis{t: t, binary: binary, dir: t.TempDir(), port: port, password: hex.EncodeToString(secret)}
	t.Cleanup(r.stop)
	r.start()
	return r
}

func (r *phase6Redis) start() {
	r.t.Helper()
	var err error
	r.log, err = os.OpenFile(filepath.Join(r.dir, "redis.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		r.t.Fatal(err)
	}
	r.cmd = exec.Command(r.binary, "--bind", "127.0.0.1", "--port", r.port,
		"--dir", r.dir, "--save", "", "--appendonly", "yes", "--appendfsync", "always",
		"--maxmemory-policy", "noeviction", "--requirepass", r.password)
	r.cmd.Stdout, r.cmd.Stderr = r.log, r.log
	if err := r.cmd.Start(); err != nil {
		r.t.Fatal(err)
	}
	r.backend = storage.NewRedisClient(&storage.Config{Endpoint: "127.0.0.1:" + r.port, Password: r.password}, "forge-phase6")
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := r.backend.Check(); err == nil {
			info, err := r.backend.Client().Info("server").Result()
			if err == nil && strings.Contains(info, fmt.Sprintf("process_id:%d\r\n", r.cmd.Process.Pid)) {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	r.t.Fatal("owned disposable Redis did not become ready")
}

func (r *phase6Redis) stop() {
	if r.backend != nil {
		r.backend.Client().Close()
		r.backend = nil
	}
	if r.cmd != nil && r.cmd.Process != nil {
		r.cmd.Process.Kill() // Deliberate process crash; AOF must recover committed data.
		r.cmd.Wait()
		r.cmd = nil
	}
	if r.log != nil {
		r.log.Close()
		r.log = nil
	}
}

// Real DOGE acceptance, dropped DOGE reply, Redis down, dropped committed Redis
// reply, exact compaction, SIGKILL/restart Redis, then journal replay. DGB/OGG
// are not submitted: the parent job is synthetic and this is a DOGE-only win.
func phase6DurableDogeRecovery(t *testing.T, f *intentFixture, sol *forgeSolution, submits, accepted *int32) {
	phase6DurableExternalRecovery(t, f, sol, submits, accepted, "doge")
}

func phase6Outcome(s storage.ForgeShare, chain string) storage.ForgeSubmission {
	if chain == "dgb" {
		return s.Digibyte
	}
	return s.Dogecoin
}

func phase6DurableExternalRecovery(t *testing.T, f *intentFixture, sol *forgeSolution, submits, accepted *int32, chain string) {
	t.Helper()
	if chain != "dgb" && chain != "doge" {
		t.Fatal("unknown external test chain")
	}
	r := newPhase6Redis(t)
	q := f.s.forge.account
	q.writer = r.backend
	r.stop()
	if _, _, _, err := f.s.processForgeIntent(f.router, f.job, sol, forgeHits{Digibyte: chain == "dgb", Dogecoin: chain == "doge"}, f.receipt); err == nil {
		t.Fatal("lost external reply treated as acknowledgement")
	}
	if atomic.LoadInt32(accepted) != 1 || atomic.LoadInt32(submits) != 1 {
		t.Fatal("real external acceptance missing")
	}
	if len(q.pending) != 1 || q.pending[0].intent == nil {
		t.Fatal("uncertain intent not retained")
	}
	t.Logf("%s accepted while Redis was stopped; its discarded reply left a durable unresolved intent", chain)
	dir := q.dir
	if err := q.close(); err != nil {
		t.Fatal(err)
	}
	var err error
	q, err = newForgeAccountingQueue(dir, &mockForgeWriter{})
	if err != nil {
		t.Fatal(err)
	}
	f.s.forge.account = q
	t.Cleanup(func() { q.close() })
	// Recover block evidence with Redis still down, then prove flushing fails.
	if err := q.recoverSubmissions(f.router); err != nil {
		t.Fatal(err)
	}
	if len(q.pending) != 1 || q.pending[0].intent != nil || !phase6Outcome(q.pending[0].receipt, chain).Accepted {
		t.Fatal("recovered outcome lost")
	}
	closedBackend := storage.NewRedisClient(&storage.Config{Endpoint: "127.0.0.1:" + r.port, Password: r.password}, "forge-phase6")
	q.writer = closedBackend
	if err := q.flush(); err == nil {
		t.Fatal("stopped Redis appeared available")
	}
	closedBackend.Client().Close()
	r.start()
	q.writer = &lostForgeReply{backend: r.backend, lose: true}
	if err := q.flush(); err == nil {
		t.Fatal("lost Redis commit reply acknowledged")
	}
	if len(q.pending) != 1 {
		t.Fatal("unacknowledged receipt removed")
	}
	saved := q.pending[0].receipt
	if saved.ProofHash != "" || (chain == "doge" && saved.Digibyte.Attempted) || (chain == "dgb" && saved.Dogecoin.Attempted) {
		t.Fatal("external win polluted other outcomes")
	}
	assertPhase6ExternalAccounting(t, r.backend, saved, chain)
	plan, err := r.backend.PlanForgeDedup(saved.Height, saved.ID[:2])
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.LiveIDs) != 1 {
		t.Fatal("wrong compaction inventory")
	}
	if err := SaveForgeReview(filepath.Join(dir, "audit"), plan.Filename(), plan); err != nil {
		t.Fatal(err)
	}
	if _, err := r.backend.ApplyForgeDedup(*plan); err != nil {
		t.Fatal(err)
	}
	if err := q.close(); err != nil {
		t.Fatal(err)
	}
	r.stop()
	r.start()
	assertPhase6ExternalAccounting(t, r.backend, saved, chain)
	packedKey := fmt.Sprintf("forge-phase6:forge:credited-compact:%d:%s", saved.Height, saved.ID[:2])
	if packed, err := r.backend.Client().Get(packedKey).Result(); err != nil || packed != saved.ID {
		t.Fatalf("AOF lost packed membership: %v", err)
	}
	q, err = newForgeAccountingQueue(dir, r.backend)
	if err != nil {
		t.Fatal(err)
	}
	f.s.forge.account = q
	if err := q.flush(); err != nil {
		t.Fatal(err)
	}
	if len(q.pending) != 0 {
		t.Fatal("recovered receipt failed to drain")
	}
	if exists, err := r.backend.WriteForgeShare(saved); err != nil || !exists {
		t.Fatalf("late replay paid again: %v %v", exists, err)
	}
	assertPhase6ExternalAccounting(t, r.backend, saved, chain)
	if atomic.LoadInt32(submits) != 1 || atomic.LoadInt32(accepted) != 1 {
		t.Fatal("restart resubmitted external block")
	}
	if files, err := filepath.Glob(filepath.Join(dir, "*.json")); err != nil || len(files) != 0 {
		t.Fatal("acknowledged journal not drained")
	}
	if err := q.writeStatus(time.Now()); err != nil {
		t.Fatal(err)
	}
	status, err := ReadForgeAccountingStatus(dir, time.Now(), 15*time.Second)
	if err != nil || status.Health != "healthy" {
		t.Fatalf("post-recovery status: %+v %v", status, err)
	}
	t.Logf("PASS: %s %s; 1 submission, 1 share, 1 external record, 0 native rounds; survived Redis SIGKILL/AOF restore and compacted late replay", chain, phase6Outcome(saved, chain).BlockHash)
}

func assertPhase6ExternalAccounting(t *testing.T, b *storage.RedisClient, s storage.ForgeShare, chain string) {
	t.Helper()
	if shares, err := b.Client().HGet("forge-phase6:shares:roundCurrent", s.Login).Result(); err != nil || shares != strconv.FormatInt(s.Difficulty, 10) {
		t.Fatalf("share count %q %v", shares, err)
	}
	if n, err := b.Client().ZCard("forge-phase6:hashrate").Result(); err != nil || n != 1 {
		t.Fatalf("hashrate entries %d %v", n, err)
	}
	key := fmt.Sprintf("forge-phase6:forge:submissions:%s:%d", chain, s.Height)
	if n, err := b.Client().HLen(key).Result(); err != nil || n != 1 {
		t.Fatalf("external records %d %v", n, err)
	}
	blob, err := b.Client().HGet(key, s.ID).Result()
	var outcome storage.ForgeSubmission
	if err != nil || json.Unmarshal([]byte(blob), &outcome) != nil || !outcome.Accepted || !strings.EqualFold(outcome.BlockHash, phase6Outcome(s, chain).BlockHash) {
		t.Fatal("external candidate/outcome mismatch", err)
	}
	if blocks, err := b.GetCandidates(s.Height); err != nil || len(blocks) != 0 {
		t.Fatalf("external win created native round: %v", err)
	}
	other := "dgb"
	if chain == "dgb" {
		other = "doge"
	}
	if n, err := b.Client().Exists("forge-phase6:forge:submissions:" + other + ":" + strconv.FormatInt(s.Height, 10)).Result(); err != nil || n {
		t.Fatal("other chain record created", err)
	}
}
