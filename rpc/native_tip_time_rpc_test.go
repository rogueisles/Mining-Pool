package rpc

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestDigiByteTipEligibilityReachesNativeProposal(t *testing.T) {
	const genesis = "7497ea1b465eb39f1c8f507bc877078fe016d6fcb6dfad3a64c98dcc6e1e8496"
	for _, tc := range []struct {
		name   string
		offset int64
		want   bool
	}{{"accepted future tip", 283, true}, {"beyond future policy", 7300, false}, {"stale tip", -3700, false}} {
		t.Run(tc.name, func(t *testing.T) {
			var proposals int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				user, password, ok := r.BasicAuth()
				if !ok || user != "fixture" || password != "disposable" {
					t.Error("native fixture authentication missing")
					w.WriteHeader(401)
					return
				}
				var request struct {
					ID     json.RawMessage `json:"id"`
					Method string          `json:"method"`
					Params []interface{}   `json:"params"`
				}
				if json.NewDecoder(r.Body).Decode(&request) != nil {
					t.Error("native fixture request invalid")
					return
				}
				var result interface{}
				switch request.Method {
				case "getblockchaininfo":
					result = map[string]interface{}{"chain": "main", "blocks": 100, "headers": 100, "initialblockdownload": false, "bestblockhash": strings.Repeat("11", 32)}
				case "getblockhash":
					result = genesis
				case "getnetworkinfo":
					result = map[string]interface{}{"connections": 8}
				case "getblockheader":
					result = map[string]interface{}{"time": time.Now().Unix() + tc.offset}
				case "getblocktemplate":
					if len(request.Params) > 0 {
						if options, ok := request.Params[0].(map[string]interface{}); ok && options["mode"] == "proposal" {
							atomic.AddInt32(&proposals, 1)
							result = nil
							break
						}
					}
					result = map[string]interface{}{"height": 101, "version": 536870912, "previousblockhash": strings.Repeat("11", 32), "bits": "1d00ffff", "curtime": time.Now().Unix(), "coinbasevalue": 100000000, "pow_algo": "scrypt", "pow_algo_id": 1, "transactions": []interface{}{}}
				default:
					t.Error("unexpected native fixture method")
					w.WriteHeader(403)
					return
				}
				json.NewEncoder(w).Encode(map[string]interface{}{"id": request.ID, "result": result, "error": nil})
			}))
			defer server.Close()
			ctx := context.Background()
			client, err := NewValidationRPCClient(ctx, "DGB", server.URL, "2s", "fixture", "disposable")
			if err != nil {
				t.Fatal(err)
			}
			err = client.CheckAuxiliaryMainnet(genesis)
			if (err == nil) != tc.want {
				t.Fatalf("native health eligibility = %v, want %v", err, tc.want)
			}
			if tc.want {
				template, err := client.GetDigibyteBlockTemplate()
				if err != nil {
					t.Fatal(err)
				}
				parent, err := template.ToParentTemplate()
				if err != nil {
					t.Fatal(err)
				}
				script, _ := hex.DecodeString("0014706267a5255e3666936bcb9a777667599f387546")
				if err := client.CheckTreasuryProposal(ctx, parent, script, 0); err != nil {
					t.Fatal(err)
				}
				if atomic.LoadInt32(&proposals) != 1 {
					t.Fatal("accepted native tip never reached proposal validation")
				}
			} else if atomic.LoadInt32(&proposals) != 0 {
				t.Fatal("unhealthy tip reached native proposal validation")
			}
		})
	}
}
