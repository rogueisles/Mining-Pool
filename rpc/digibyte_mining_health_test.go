package rpc

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestFastDigibyteHealthFailsClosed(t *testing.T) {
	for _, name := range []string{"healthy", "missing headers", "missing validation", "unvalidated", "lagging", "headers behind", "empty states", "wrong genesis", "no peers", "network disabled", "old tip", "future tip", "wrong algo", "IBD refused", "changed parent", "wrong height", "unsupported RPC", "active state last"} {
		t.Run(name, func(t *testing.T) {
			var summaries, templates int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				user, password, ok := r.BasicAuth()
				if !ok || user != "fixture" || password != "disposable" {
					t.Error("authentication missing")
					w.WriteHeader(401)
					return
				}
				var q struct {
					ID     interface{}
					Method string
				}
				json.NewDecoder(r.Body).Decode(&q)
				var result interface{}
				switch q.Method {
				case "getchainstates":
					if name == "unsupported RPC" {
						json.NewEncoder(w).Encode(map[string]interface{}{"id": q.ID, "error": map[string]interface{}{"code": -32601, "message": "unsupported"}})
						return
					}
					a := map[string]interface{}{"blocks": 100, "bestblockhash": strings.Repeat("11", 32), "validated": true}
					states := []interface{}{a}
					headers := 101
					if name == "missing validation" {
						delete(a, "validated")
					}
					if name == "unvalidated" {
						a["validated"] = false
					}
					if name == "lagging" {
						headers = 103
					}
					if name == "headers behind" {
						headers = 99
					}
					if name == "empty states" {
						states = nil
					}
					if name == "active state last" {
						states = append(states, map[string]interface{}{"blocks": 101, "bestblockhash": strings.Repeat("22", 32), "validated": false})
					}
					v := map[string]interface{}{"headers": headers, "chainstates": states}
					if name == "missing headers" {
						delete(v, "headers")
					}
					result = v
				case "getblockchaininfo":
					atomic.AddInt32(&summaries, 1)
					t.Error("fast health called expensive summary")
					w.WriteHeader(500)
					return
				case "getblockhash":
					result = digibyteMainnetGenesis
					if name == "wrong genesis" {
						result = strings.Repeat("ff", 32)
					}
				case "getnetworkinfo":
					v := map[string]interface{}{"connections": 8, "networkactive": true}
					if name == "no peers" {
						v["connections"] = 0
					}
					if name == "network disabled" {
						v["networkactive"] = false
					}
					result = v
				case "getblockheader":
					stamp := time.Now().Unix()
					if name == "old tip" {
						stamp -= 3700
					}
					if name == "future tip" {
						stamp += 7300
					}
					result = map[string]interface{}{"time": stamp}
				case "getblocktemplate":
					atomic.AddInt32(&templates, 1)
					if name == "IBD refused" {
						json.NewEncoder(w).Encode(map[string]interface{}{"id": q.ID, "error": map[string]interface{}{"code": -10, "message": "initial sync"}})
						return
					}
					v := map[string]interface{}{"height": 101, "version": 4, "previousblockhash": strings.Repeat("11", 32), "bits": "1d00ffff", "curtime": time.Now().Unix(), "coinbasevalue": 100000000, "pow_algo": "scrypt", "pow_algo_id": 1, "transactions": []interface{}{}}
					if name == "wrong algo" {
						v["pow_algo"] = "sha256d"
					}
					if name == "changed parent" {
						v["previousblockhash"] = strings.Repeat("22", 32)
					}
					if name == "wrong height" {
						v["height"] = 102
					}
					result = v
				default:
					t.Error("unexpected method", q.Method)
					w.WriteHeader(500)
					return
				}
				json.NewEncoder(w).Encode(map[string]interface{}{"id": q.ID, "result": result, "error": nil})
			}))
			defer server.Close()
			c, err := NewValidationRPCClient(context.Background(), "DGB", server.URL, "1s", "fixture", "disposable")
			if err != nil {
				t.Fatal(err)
			}
			got, err := c.GetHealthyDigibyteMiningTemplate()
			if name == "healthy" {
				if err != nil || got == nil || atomic.LoadInt32(&templates) != 1 {
					t.Fatal("healthy native work refused", err)
				}
			} else if err == nil || got != nil {
				t.Fatal("unsafe native work admitted", name)
			}
			if atomic.LoadInt32(&summaries) != 0 {
				t.Fatal("summary was used")
			}
		})
	}
}
