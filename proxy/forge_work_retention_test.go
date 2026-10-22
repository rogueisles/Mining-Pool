package proxy

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/HawyUkr/open-progpow-pool/forgepool"
	"github.com/HawyUkr/open-progpow-pool/rpc"
)

func retentionClone(job *forgeJob) *forgeJob {
	next := *job
	work := *job.OGG
	parent := *job.Digibyte
	signed := *job.Signed
	next.OGG, next.Digibyte, next.Signed = &work, &parent, &signed
	if parent.DOGE != nil {
		doge := *parent.DOGE
		next.Digibyte.DOGE = &doge
	}
	return &next
}

func retentionFixture(t *testing.T) (*ProxyServer, *Session, *forgeJob, *forgeJob, *mockForgeWriter, *mockForgeSubmitter, uint32) {
	t.Helper()
	s, cs, old, writer, submitter, nonce := testForgeServer(t)
	old.ExternalMask = forgepool.ExternalMask
	old.OGG.PreviousBlockHash = "0x" + strings.Repeat("11", 32)
	old.Signed.Template.Version = forgepool.OGGTemplateVersionV2Base | old.ExternalMask
	next := retentionClone(old)
	next.ID, next.Height = "deadbeef", old.Height+1
	next.OGG.PreviousBlockHash = "0x" + strings.Repeat("22", 32)
	next.OGG.WorkHash = "0x" + strings.Repeat("33", 32)
	next.Signed.WorkHash = next.OGG.WorkHash
	s.forge.retainExternalWork = true
	s.forge.jobs[next.ID] = next
	s.forge.currentID = next.ID
	return s, cs, old, next, writer, submitter, nonce
}

func TestForgeRetentionCleanPolicy(t *testing.T) {
	s, _, old, next, _, _, _ := retentionFixture(t)
	if s.forge.jobNeedsClean(old, next) {
		t.Fatal("OGG-only change unnecessarily resets external work")
	}
	s.forge.retainExternalWork = false
	if !s.forge.jobNeedsClean(old, next) {
		t.Fatal("default legacy clean policy changed")
	}
	s.forge.retainExternalWork = true
	cases := map[string]func(*forgeJob){
		"dgb parent":         func(j *forgeJob) { j.Digibyte.Template.Previous[0]++ },
		"doge parent":        func(j *forgeJob) { j.Digibyte.DOGE.Template.Previous[0]++ },
		"dgb bits":           func(j *forgeJob) { j.Digibyte.Template.Bits++ },
		"doge bits":          func(j *forgeJob) { j.Digibyte.DOGE.Bits++ },
		"mode":               func(j *forgeJob) { j.ExternalMask = forgepool.ExternalDGB },
		"legacy template":    func(j *forgeJob) { j.Signed.Template.Version = forgepool.OGGTemplateVersion },
		"missing template":   func(j *forgeJob) { j.Signed = nil },
		"invalid OGG parent": func(j *forgeJob) { j.OGG.PreviousBlockHash = "0x00" },
		"different issuer":   func(j *forgeJob) { j.OGGRPC = rpc.NewRPCClient("other", "http://127.0.0.1:1", "1s", "", "") },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			changed := retentionClone(next)
			mutate(changed)
			if forgeExternalWorkCompatible(old, changed) || !s.forge.jobNeedsClean(old, changed) {
				t.Fatal("incompatible change retained previous work")
			}
		})
	}
}

func TestForgeRetentionSupportsEachSignedExternalMode(t *testing.T) {
	for _, mask := range []uint8{forgepool.ExternalDGB, forgepool.ExternalDOGE, forgepool.ExternalMask} {
		t.Run(fmt.Sprint(mask), func(t *testing.T) {
			rt, ogg, dgb, doge := forgeRuntimeModeFixture(t)
			ogg.PreviousBlockHash = "0x" + strings.Repeat("11", 32)
			old, err := rt.buildV2RuntimeJob(mask, ogg, 4000, 1, nil, 1, &dgb, doge)
			if err != nil {
				t.Fatal(err)
			}
			old.Signed = &forgeSignedTemplate{Template: forgepool.ExternalTemplate{Version: forgepool.OGGTemplateVersionV2Base | mask}}
			next := retentionClone(old)
			next.Height++
			next.OGG.PreviousBlockHash = "0x" + strings.Repeat("22", 32)
			rt.retainExternalWork = true
			if !forgeExternalWorkCompatible(old, next) || rt.jobNeedsClean(old, next) {
				t.Fatal("compatible external mode lost retention", mask)
			}
		})
	}
}

func TestForgeRetentionRoutesExternalWinsWithoutOldOGGOrDuplicateCredit(t *testing.T) {
	s, cs, old, _, writer, submitter, nonce := retentionFixture(t)
	extra, ntime, n := submitArgs(old, nonce)
	valid, duplicate, hits, err := s.processForgeShare(cs, cs.login, cs.worker, old.ID, extra, ntime, n, time.Now())
	if err != nil || !valid || duplicate || !hits.Digibyte || !hits.Dogecoin || hits.OGG {
		t.Fatalf("retained solution incorrectly classified: %v %v %+v %v", valid, duplicate, hits, err)
	}
	if submitter.dgb != 1 || submitter.doge != 1 || submitter.ogg != 0 || len(writer.receipts) != 1 || writer.receipts[0].ProofHash != "" {
		t.Fatal("external win lost, stale native block submitted, or native round closed")
	}
	_, duplicate, _, err = s.processForgeShare(cs, cs.login, cs.worker, old.ID, extra, ntime, n, time.Now())
	if !duplicate || !errors.Is(err, errForgeDuplicate) || writer.calls != 1 || submitter.dgb != 1 || submitter.doge != 1 {
		t.Fatal("retained work was submitted or credited twice")
	}
}

func TestForgeRetentionSameParentStillRoutesOGG(t *testing.T) {
	s, cs, old, next, _, submitter, nonce := retentionFixture(t)
	next.Height = old.Height
	next.OGG.PreviousBlockHash = old.OGG.PreviousBlockHash
	extra, ntime, n := submitArgs(old, nonce)
	valid, _, hits, err := s.processForgeShare(cs, cs.login, cs.worker, old.ID, extra, ntime, n, time.Now())
	if err != nil || !valid || !hits.OGG || submitter.ogg != 1 {
		t.Fatalf("valid same-parent OGG work suppressed: %+v %v", hits, err)
	}
}

func TestForgeRetentionDurableExternalSubmissionsSurviveRestart(t *testing.T) {
	f := newIntentFixture(t)
	f.job.ExternalMask = forgepool.ExternalMask
	f.job.OGG.PreviousBlockHash = "0x" + strings.Repeat("11", 32)
	if err := f.s.forge.signJob(f.job); err != nil {
		t.Fatal(err)
	}
	next := retentionClone(f.job)
	next.ID, next.Height = "deadbeef", f.job.Height+1
	next.OGG.PreviousBlockHash = "0x" + strings.Repeat("22", 32)
	next.OGG.WorkHash = "0x" + strings.Repeat("33", 32)
	if err := f.s.forge.signJob(next); err != nil {
		t.Fatal(err)
	}
	rt := f.s.forge
	rt.retainExternalWork = true
	rt.jobs[next.ID], rt.currentID = next, next.ID
	if err := f.submit(t); err != nil {
		t.Fatal(err)
	}
	if f.daemons["DGB"].submits != 1 || f.daemons["DOGE"].submits != 1 || f.daemons["OGG"].submits != 0 ||
		len(f.writer.receipts) != 1 || f.writer.receipts[0].ProofHash != "" ||
		!f.writer.receipts[0].Digibyte.Accepted || !f.writer.receipts[0].Dogecoin.Accepted {
		t.Fatal("retained real RPC submissions did not preserve external outcomes without native credit")
	}
	q := f.restart(t)
	if err := q.recoverSubmissions(f.router); err != nil {
		t.Fatal(err)
	}
	if err := q.flush(); err != nil {
		t.Fatal(err)
	}
	if len(f.writer.receipts) != 1 || f.daemons["DGB"].submits != 1 || f.daemons["DOGE"].submits != 1 || f.daemons["OGG"].submits != 0 {
		t.Fatal("restart resubmitted or credited retained work twice")
	}
}

func TestForgeRetentionRealRedisDoesNotCloseNativeRound(t *testing.T) {
	if os.Getenv("FORGE_PHASE6_REDIS_SERVER") == "" {
		t.Skip("requires owned disposable Redis")
	}
	r := newPhase6Redis(t)
	s, cs, old, _, _, submitter, nonce := retentionFixture(t)
	s.forge.account.writer = r.backend
	extra, ntime, n := submitArgs(old, nonce)
	valid, _, _, err := s.processForgeShare(cs, cs.login, cs.worker, old.ID, extra, ntime, n, time.Now())
	if err != nil || !valid {
		t.Fatalf("retained external share failed: %v", err)
	}
	if r.backend.Client().ZCard("forge-phase6:blocks:candidates").Val() != 0 ||
		r.backend.Client().HGet("forge-phase6:stats", "roundShares").Val() != "1" ||
		r.backend.Client().HGet("forge-phase6:shares:roundCurrent", cs.login).Val() != "1" || submitter.ogg != 0 {
		t.Fatal("retained share closed a native round or failed current share credit")
	}
	for _, chain := range []string{"dgb", "doge"} {
		if r.backend.Client().HLen(fmt.Sprintf("forge-phase6:forge:submissions:%s:%d", chain, old.Height)).Val() != 1 {
			t.Fatal("missing external submission outcome", chain)
		}
	}
	_, duplicate, _, err := s.processForgeShare(cs, cs.login, cs.worker, old.ID, extra, ntime, n, time.Now())
	if !duplicate || !errors.Is(err, errForgeDuplicate) || r.backend.Client().HGet("forge-phase6:stats", "roundShares").Val() != "1" {
		t.Fatal("duplicate retained work changed share credit")
	}
}

func TestForgeRetentionRejectsExpiredOrIncompatibleShares(t *testing.T) {
	for _, name := range []string{"expiry", "current expiry", "missing current", "dgb tip", "doge tip", "dgb bits", "doge bits", "pause"} {
		t.Run(name, func(t *testing.T) {
			s, cs, old, next, writer, submitter, nonce := retentionFixture(t)
			switch name {
			case "expiry":
				old.Signed.Template.Expires = uint64(time.Now().Unix())
			case "current expiry":
				next.Signed.Template.Expires = uint64(time.Now().Unix())
			case "missing current":
				s.forge.currentID = "missing"
			case "dgb tip":
				next.Digibyte.Template.Previous[0]++
			case "doge tip":
				next.Digibyte.DOGE.Template.Previous[0]++
			case "dgb bits":
				next.Digibyte.Template.Bits++
			case "doge bits":
				next.Digibyte.DOGE.Bits++
			case "pause":
				s.forge.clearExternalJobs()
			}
			extra, ntime, n := submitArgs(old, nonce)
			_, _, _, err := s.processForgeShare(cs, cs.login, cs.worker, old.ID, extra, ntime, n, time.Now())
			if !errors.Is(err, errForgeStale) || writer.calls != 0 || submitter.dgb+submitter.doge+submitter.ogg != 0 {
				t.Fatalf("stale work reached submission/accounting: %v", err)
			}
		})
	}
}

// Exercise the actual refresh/sign/publication path rather than inserting only
// hand-made jobs. Signed OGG changes retain work; an external change clears it.
func TestForgeRetentionSignedRefreshBoundsAndExternalReset(t *testing.T) {
	s, rt, _, doge := newForgeTransitionHarness(t)
	_, _, fixture, _, _, _ := testForgeServer(t)
	work := *fixture.OGG
	work.Difficulty = "0x1"
	work.PreviousBlockHash = "0x" + strings.Repeat("11", 32)
	var mu sync.Mutex
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		json.NewEncoder(w).Encode(map[string]interface{}{"jsonrpc": "2.0", "id": 0, "result": work})
	}))
	t.Cleanup(node.Close)
	s.upstreams = []*rpc.RPCClient{rpc.NewRPCClient("issuer", node.URL, "2s", "", "")}
	rt.retainExternalWork = true
	s.fetchForgeBlockTemplate()
	first := rt.currentJob()
	if first == nil || !first.Clean {
		t.Fatal("first job must clear unknown miner state")
	}
	for i := 1; i <= rt.jobBacklog+2; i++ {
		mu.Lock()
		work.PreviousBlockHash = fmt.Sprintf("0x%064x", i+256)
		work.WorkHash = fmt.Sprintf("0x%064x", i+512)
		work.Height = fmt.Sprintf("0x%x", first.Height+uint64(i))
		mu.Unlock()
		if !s.fetchForgeBlockTemplate() {
			t.Fatal("signed refresh failed")
		}
		if rt.currentJob().Clean || len(rt.jobs) > rt.jobBacklog {
			t.Fatal("OGG-only refresh resets work or exceeds bounded cache")
		}
	}
	if rt.job(first.ID) != nil {
		t.Fatal("oldest job was not evicted")
	}
	previous := rt.currentJob()
	doge.mu.Lock()
	doge.reply.PreviousBlockHash = strings.Repeat("44", 32)
	doge.mu.Unlock()
	if !s.fetchForgeBlockTemplate() || !rt.currentJob().Clean || rt.job(previous.ID) != nil {
		t.Fatal("external parent change failed to invalidate old jobs")
	}
}
