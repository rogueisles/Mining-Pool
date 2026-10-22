package proxy

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sync"
	"testing"
	"time"

	"github.com/HawyUkr/open-progpow-pool/forgepool"
	"github.com/HawyUkr/open-progpow-pool/policy"
)

func enableTestVarDiff(s *ProxyServer) {
	s.config.Proxy.Difficulty = 50000
	s.config.Proxy.Forge.VariableDifficulty = ForgeVarDiffConfig{true, 50000, 2000000, 2, 30}
}

func realisticVarDiffJob(t *testing.T, job *forgeJob) {
	t.Helper()
	target, _ := forgepool.TargetForDifficulty(big.NewInt(28))
	job.OGG.Target = fmt.Sprintf("0x%064x", target)
	job.Digibyte.Template.Bits = 0x1b00ffff
	job.Digibyte.DOGE.Bits = 0x1c00ffff
}

func TestForgeVarDiffCoverageOfEveryChain(t *testing.T) {
	s, cs, job, _, _, _ := testForgeServer(t)
	enableTestVarDiff(s)
	realisticVarDiffJob(t, job)
	cs.requestForgeDifficulty(s, 2000000, time.Now())
	a, e := cs.forgeAssignment(s, job, time.Now())
	if e != nil {
		t.Fatal(e)
	}
	if a.difficulty != 28*65536 {
		t.Fatal("OGG target not preserved", a.difficulty)
	}
	ogg, _ := parseHexTarget(job.OGG.Target)
	if a.target.Cmp(ogg) < 0 {
		t.Fatal("valid OGG block could be discarded")
	}
	// Easier external target must win over a harder OGG target as well.
	job2 := *job
	job2.ID = "external-easier"
	job2.Digibyte.DOGE.Bits = 0x1d00ffff
	a, e = cs.forgeAssignment(s, &job2, time.Now())
	if e != nil || a.difficulty != 65536 {
		t.Fatal(a, e)
	}
	ext, _ := forgepool.CompactTarget(job2.Digibyte.DOGE.Bits)
	if a.target.Cmp(ext) < 0 {
		t.Fatal("valid DOGE block could be discarded")
	}
}

func TestForgeVarDiffAutoUpDownOldJobsAndBoundedMemory(t *testing.T) {
	s, cs, job, _, _, _ := testForgeServer(t)
	enableTestVarDiff(s)
	realisticVarDiffJob(t, job)
	start := time.Unix(100, 0)
	a, e := cs.forgeAssignment(s, job, start)
	if e != nil || a.difficulty != 50000 {
		t.Fatal(a, e)
	}
	for i := 0; i < 400; i++ {
		cs.recordForgeAccepted(s, job.ID)
	}
	next := *job
	next.ID = "next"
	b, e := cs.forgeAssignment(s, &next, start.Add(30*time.Second))
	if e != nil || b.difficulty != 200000 {
		t.Fatal(b, e)
	}
	old, _ := cs.issuedForgeAssignment(job.ID)
	if old.difficulty != 50000 {
		t.Fatal("retarget changed old job")
	}
	next.ID = "slow"
	b, e = cs.forgeAssignment(s, &next, start.Add(60*time.Second))
	if e != nil || b.difficulty != 50000 {
		t.Fatal(b, e)
	}
	for i := 0; i < 100; i++ {
		next.ID = fmt.Sprint(i)
		if _, e = cs.forgeAssignment(s, &next, start.Add(61*time.Second)); e != nil {
			t.Fatal(e)
		}
	}
	if len(cs.forgeDiff.assignments) > s.forge.jobBacklog+2 {
		t.Fatal("unbounded job history")
	}
}

func TestForgeVarDiffPreservesPerJobAccountingAndSubmissions(t *testing.T) {
	s, cs, job, writer, submitter, nonce := testForgeServer(t)
	enableTestVarDiff(s)
	// Cheap regtest target lets this test route actual Scrypt solutions to every
	// submitter. Assignment/coverage arithmetic is checked independently above.
	cs.forgeDiff.assignments = map[string]forgeShareAssignment{job.ID: {844800, maxUint256()}}
	extra, ntime, n := submitArgs(job, nonce)
	valid, duplicate, hits, e := s.processForgeShare(cs, cs.login, cs.worker, job.ID, extra, ntime, n, time.Now())
	if e != nil || !valid || duplicate || !hits.OGG || !hits.Dogecoin || !hits.Digibyte {
		t.Fatal(valid, duplicate, hits, e)
	}
	if len(writer.receipts) != 1 || writer.receipts[0].Difficulty != 844800 {
		t.Fatal("credited default rather than issued work", writer.receipts)
	}
	if submitter.ogg != 1 || submitter.doge != 1 || submitter.dgb != 1 {
		t.Fatal("lost chain submission")
	}
	cs.forgeDiff.assignments[job.ID] = forgeShareAssignment{50000, big.NewInt(1)}
	_, _, _, e = s.validateForgeShare(cs, job.ID, extra, ntime, n, time.Now())
	if !errors.Is(e, errForgeLowDifficulty) {
		t.Fatal("did not enforce assigned target", e)
	}
	delete(cs.forgeDiff.assignments, job.ID)
	_, _, _, e = s.validateForgeShare(cs, job.ID, extra, ntime, n, time.Now())
	if !errors.Is(e, errForgeStale) {
		t.Fatal("accepted unissued job", e)
	}
}

func TestForgeVarDiffRequestsAndFixedPreference(t *testing.T) {
	s, cs, job, _, _, _ := testForgeServer(t)
	enableTestVarDiff(s)
	realisticVarDiffJob(t, job)
	for _, bad := range []string{"d=0", "d=-1", "d=1e6", "d=4,d=5", "d=9007199254740992"} {
		if _, _, e := forgePasswordDifficulty(bad); e == nil {
			t.Fatal("accepted", bad)
		}
	}
	n, found, e := forgePasswordDifficulty("x,d=844800")
	if e != nil || !found || n != 844800 {
		t.Fatal(n, found, e)
	}
	for _, raw := range []string{"[0]", "[-1]", "[1.5]", "[1,2]", "[]", "null", "[\"NaN\"]"} {
		if _, e := decodeForgeSuggestedDifficulty(json.RawMessage(raw)); e == nil {
			t.Fatal("accepted", raw)
		}
	}
	cs.requestForgeDifficulty(s, n, time.Unix(100, 0))
	a, e := cs.forgeAssignment(s, job, time.Unix(10000, 0))
	if e != nil || a.difficulty != 844800 {
		t.Fatal(a, e)
	}
	// A requested value is a preference, never permission to hide OGG winners.
	target, _ := forgepool.TargetForDifficulty(big.NewInt(1))
	job2 := *job
	job2.ID = "easy-ogg"
	job2.OGG.Target = fmt.Sprintf("0x%064x", target)
	a, e = cs.forgeAssignment(s, &job2, time.Unix(10001, 0))
	if e != nil || a.difficulty != 65536 {
		t.Fatal(a, e)
	}
}

func TestForgeVarDiffProtocolAndAtomicBroadcast(t *testing.T) {
	s, cs, job, _, _, _ := testForgeServer(t)
	enableTestVarDiff(s)
	realisticVarDiffJob(t, job)
	s.policy = &policy.PolicyServer{}
	s.sessions = map[*Session]struct{}{cs: {}}
	var output bytes.Buffer
	cs.enc = json.NewEncoder(&output)
	args, _ := json.Marshal([]string{cs.login + ".rental", "d=844800"})
	if e := cs.handleForgeTCPMessage(s, &StratumReq{JSONRpcReq: JSONRpcReq{Id: json.RawMessage("1"), Method: "mining.authorize", Params: args}}); e != nil {
		t.Fatal(e)
	}
	dec := json.NewDecoder(&output)
	var response map[string]interface{}
	var diff, notify StratumPushMessage
	if dec.Decode(&response) != nil || dec.Decode(&diff) != nil || dec.Decode(&notify) != nil || response["result"] != true {
		t.Fatal("handshake failed")
	}
	if diff.Method != "mining.set_difficulty" || diff.Params.([]interface{})[0].(float64) != 844800 || notify.Method != "mining.notify" {
		t.Fatal(diff, notify)
	}
	output.Reset()
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 30; j++ {
				if e := cs.pushForgeWork(s, job); e != nil {
					t.Error(e)
				}
			}
		}()
	}
	wg.Wait()
	dec = json.NewDecoder(&output)
	for i := 0; i < 90; i++ {
		if dec.Decode(&diff) != nil || dec.Decode(&notify) != nil || diff.Method != "mining.set_difficulty" || notify.Method != "mining.notify" {
			t.Fatal("interleaved difficulty/job pair")
		}
	}
}

func TestForgeVarDiffRejectsUncoverableOrInvalidConfig(t *testing.T) {
	s, _, job, _, _, _ := testForgeServer(t)
	enableTestVarDiff(s)
	if _, e := forgeSafeShareDifficulty(job); e == nil {
		t.Fatal("accepted uncoverable CPU target")
	}
	good := s.config.Proxy.Forge.VariableDifficulty
	for _, bad := range []ForgeVarDiffConfig{{true, 0, 2000000, 2, 30}, {true, 50000, 1, 2, 30}, {true, 50000, 2000000, 0, 30}, {true, 50000, 2000000, 2, 1}} {
		if bad.validate(50000) == nil {
			t.Fatal("invalid bounds accepted")
		}
	}
	if good.validate(50000) != nil {
		t.Fatal("valid bounds refused")
	}
}
