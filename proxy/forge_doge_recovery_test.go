package proxy

import (
	"bytes"
	"encoding/json"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/HawyUkr/open-progpow-pool/forgepool"
	"github.com/HawyUkr/open-progpow-pool/rpc"
)

// An actual official DOGE node accepts the AuxPoW; the forwarding RPC drops
// only its reply. Recovery must discover the canonical body after restart.
func TestOfficialDogecoinLostReplyRecovery(t *testing.T) {
	officialDogecoinRecovery(t, false)
}

func TestOfficialDogecoinPhase6DurableAccounting(t *testing.T) {
	if os.Getenv("FORGE_PHASE6_REDIS_SERVER") == "" {
		t.Skip("requires isolated Phase 6 harness and disposable Redis binary")
	}
	officialDogecoinRecovery(t, true)
}

func officialDogecoinRecovery(t *testing.T, durable bool) {
	t.Helper()
	url := os.Getenv("FORGE_DOGE_TEST_RPC")
	if url == "" {
		t.Skip("requires isolated official DOGE regtest script")
	}
	forward := func(blob []byte) ([]byte, int, error) {
		req, err := http.NewRequest("POST", url, bytes.NewReader(blob))
		if err != nil {
			return nil, 0, err
		}
		req.SetBasicAuth("forge-regtest", "disposable-test-only")
		resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
		if err != nil {
			return nil, 0, err
		}
		defer resp.Body.Close()
		body, err := ioutil.ReadAll(resp.Body)
		return body, resp.StatusCode, err
	}
	info, status, err := forward([]byte(`{"id":0,"method":"getblockchaininfo","params":[]}`))
	var chain struct{ Result struct{ Chain string } }
	if err != nil || status != 200 || json.Unmarshal(info, &chain) != nil || chain.Result.Chain != "regtest" {
		t.Fatal("requires disposable regtest")
	}
	var submits, accepted int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		blob, err := ioutil.ReadAll(req.Body)
		if err != nil {
			w.WriteHeader(500)
			return
		}
		var call struct{ Method string }
		if json.Unmarshal(blob, &call) != nil {
			w.WriteHeader(400)
			return
		}
		body, status, err := forward(blob)
		if err != nil {
			w.WriteHeader(502)
			return
		}
		if call.Method == "submitblock" {
			atomic.AddInt32(&submits, 1)
			var result struct{ Result, Error json.RawMessage }
			if status == 200 && json.Unmarshal(body, &result) == nil && string(result.Result) == "null" && string(result.Error) == "null" {
				atomic.AddInt32(&accepted, 1)
			}
			w.WriteHeader(503) // The caller cannot know the node's outcome.
			return
		}
		w.WriteHeader(status)
		w.Write(body)
	}))
	defer server.Close()
	f := newIntentFixture(t)
	f.router.doge = rpc.NewRPCClient("official-doge-recovery", server.URL, "5s", "", "")
	gbt, err := f.router.doge.GetDogecoinBlockTemplate()
	if err != nil {
		t.Fatal(err)
	}
	tpl, err := gbt.ToParentTemplate()
	if err != nil {
		t.Fatal(err)
	}
	doge, err := forgepool.BuildDogeCandidate(tpl, []byte{0x51}, 99)
	if err != nil {
		t.Fatal(err)
	}
	parent := f.job.Digibyte.Template
	parent.CurTime = tpl.CurTime
	f.job.Digibyte, err = forgepool.BuildDigibyteJob(parent, f.job.OGG.WorkHash, doge, []byte{0x51}, 99, 4, 4)
	if err != nil {
		t.Fatal(err)
	}
	sol := &forgeSolution{ExtraNonce1: f.cs.extraNonce1, ExtraNonce2: []byte{5, 6, 7, 8}, NTime: parent.CurTime}
	found := false
	for sol.Nonce = 0; sol.Nonce < 100000; sol.Nonce++ {
		sol.Header, err = f.job.Digibyte.Header(sol.ExtraNonce1, sol.ExtraNonce2, sol.NTime, sol.Nonce)
		if err != nil {
			t.Fatal(err)
		}
		sol.Hash, err = forgepool.ScryptHash(sol.Header)
		if err != nil {
			t.Fatal(err)
		}
		if ok, err := forgepool.MeetsCompactTarget(sol.Hash, tpl.Bits); err != nil {
			t.Fatal(err)
		} else if ok {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("regtest mining bound exhausted")
	}
	f.receipt.ID = forgePayloadDigest(sol.Header)
	if durable {
		phase6DurableDogeRecovery(t, f, sol, &submits, &accepted)
		return
	}
	if _, _, _, err := f.s.processForgeIntent(f.router, f.job, sol, forgeHits{Dogecoin: true}, f.receipt); err == nil {
		t.Fatal("lost reply treated as acknowledgement")
	}
	if atomic.LoadInt32(&accepted) != 1 || f.writer.calls != 0 {
		t.Fatal("missing actual acceptance or premature credit")
	}
	q := f.restart(t)
	if err := q.recoverSubmissions(f.router); err != nil {
		t.Fatal(err)
	}
	if err := q.flush(); err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(&submits) != 1 || len(f.writer.receipts) != 1 || !f.writer.receipts[0].Dogecoin.Accepted || f.writer.receipts[0].ProofHash != "" {
		t.Fatal("real daemon recovery failed")
	}
	t.Logf("recovered DOGE height %d after accepted block reply was lost; exactly one submission", gbt.Height)
}
