package proxy

import (
	"encoding/json"
	"fmt"
	"github.com/HawyUkr/open-progpow-pool/rpc"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestExpiredNoncanonicalRGNIntentUnblocksOrderedSharesAtMaturity(t *testing.T) {
	for _, depth := range []int64{31, 32} {
		t.Run(fmt.Sprint(depth), func(t *testing.T) {
			f := newIntentFixture(t)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var q struct{ Method string }
				json.NewDecoder(r.Body).Decode(&q)
				switch q.Method {
				case "eth_getBlockByNumber":
					fmt.Fprintf(w, `{"id":0,"result":{"number":"0x%x","hash":"0x%s","extraData":"0x00"}}`, f.receipt.Height, strings.Repeat("ab", 32))
				case "eth_blockNumber":
					fmt.Fprintf(w, `{"id":0,"result":"0x%x"}`, f.receipt.Height+depth)
				default:
					t.Error("recovery attempted a submission or unexpected read")
					w.WriteHeader(500)
				}
			}))
			defer server.Close()
			client := rpc.NewRPCClient("maturity", server.URL, "1s", "", "")
			f.job.OGGRPC = client
			f.router.issuers = []*rpc.RPCClient{client}
			f.router.maturityDepth = 32
			in := f.router.prepareIntent(f.job, f.sol, forgeHits{OGG: true})
			in.Blocks[0].Sent = true
			in.Blocks[0].Error = "uncertain delivery"
			f.router.clock = func() time.Time { return time.Unix(in.Blocks[0].Expires+1, 0) }
			item, err := f.s.forge.account.beginIntent(f.receipt, in)
			if err != nil {
				t.Fatal(err)
			}
			if err = f.s.forge.account.runIntent(item, f.router); err != nil {
				t.Fatal(err)
			}
			err = f.s.forge.account.flush()
			if depth < 32 {
				if err == nil || len(f.writer.receipts) != 0 || item.intent == nil {
					t.Fatal("young uncertain outcome was discarded or credited")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(f.writer.receipts) != 1 || f.writer.receipts[0].ProofHash != "" || f.writer.receipts[0].OGGError != "not canonical at pool maturity" {
				t.Fatal("noncanonical proof was paid, lost, or not resolved")
			}
			if len(f.s.forge.account.pending) != 0 {
				t.Fatal("resolved intent still blocks accounting")
			}
			later := f.receipt
			later.ID = strings.Repeat("cd", 32)
			later.TimestampMS++
			if _, err = f.s.forge.account.enqueue(later); err != nil {
				t.Fatal(err)
			}
			if len(f.writer.receipts) != 2 {
				t.Fatal("following shares did not resume")
			}
		})
	}
}
