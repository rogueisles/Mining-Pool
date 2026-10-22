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

func TestOfficialDigibyteLostReplyRecovery(t *testing.T) {
	url := os.Getenv("FORGE_DGB_TEST_RPC")
	if url == "" || os.Getenv("FORGE_PHASE6_REDIS_SERVER") == "" {
		t.Skip("requires isolated Digibyte/Redis script")
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
	var chain struct {
		Result struct {
			Chain  string
			Blocks int64
		}
	}
	if err != nil || status != 200 || json.Unmarshal(info, &chain) != nil || chain.Result.Chain != "regtest" || chain.Result.Blocks < 432 {
		t.Fatal("isolated post-MWEB regtest required")
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
			w.WriteHeader(503)
			return
		}
		w.WriteHeader(status)
		w.Write(body)
	}))
	defer server.Close()
	f := newIntentFixture(t)
	f.router.dgb = rpc.NewRPCClient("official-dgb-recovery", server.URL, "5s", "", "")
	gbt, err := f.router.dgb.GetDigibyteBlockTemplate()
	if err != nil {
		t.Fatal(err)
	}
	tpl, err := gbt.ToParentTemplate()
	if err != nil {
		t.Fatal(err)
	}
	if len(tpl.MWEB) == 0 {
		t.Fatal("recovery fixture missing MWEB extension")
	}
	f.job.Digibyte, err = forgepool.BuildDigibyteJob(tpl, f.job.OGG.WorkHash, f.job.Digibyte.DOGE, []byte{0x51}, 101, 4, 4)
	if err != nil {
		t.Fatal(err)
	}
	sol := &forgeSolution{ExtraNonce1: f.cs.extraNonce1, ExtraNonce2: []byte{5, 6, 7, 8}, NTime: tpl.CurTime}
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
	phase6DurableExternalRecovery(t, f, sol, &submits, &accepted, "dgb")
}
