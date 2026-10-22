package rpc

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestForgeOGGHashHintRequiresExactStoredProof(t *testing.T) {
	hash := "0x" + strings.Repeat("ab", 32)
	for _, tc := range []struct {
		name, result string
		found        bool
	}{
		{"exact", fmt.Sprintf(`{"number":"0x2a","hash":"%s","extraData":"0x0102"}`, hash), true},
		{"wrong-height", fmt.Sprintf(`{"number":"0x2b","hash":"%s","extraData":"0x0102"}`, hash), false},
		{"wrong-hash", fmt.Sprintf(`{"number":"0x2a","hash":"0x%s","extraData":"0x0102"}`, strings.Repeat("cd", 32)), false},
		{"wrong-proof", fmt.Sprintf(`{"number":"0x2a","hash":"%s","extraData":"0x0103"}`, hash), false},
		{"missing", "null", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				var req struct {
					Method string
					Params []json.RawMessage
				}
				if json.NewDecoder(r.Body).Decode(&req) != nil || req.Method != "eth_getBlockByHash" || len(req.Params) != 2 || string(req.Params[0]) != `"`+hash+`"` || string(req.Params[1]) != "false" {
					t.Error("incorrect hash lookup request")
				}
				fmt.Fprintf(w, `{"id":0,"result":%s}`, tc.result)
			}))
			defer srv.Close()
			client := NewRPCClient("hint", srv.URL, "1s", "", "")
			found, err := client.FindForgeProofBlock(42, "0102", hash)
			if err != nil || (found == hash) != tc.found {
				t.Fatalf("binding %q %v", found, err)
			}
			if _, err := client.FindForgeProofBlock(42, "0102", "arbitrary"); err == nil || calls != 1 {
				t.Fatal("invalid hint reached RPC")
			}
		})
	}
}

func TestForgeSubmissionReplyCertainty(t *testing.T) {
	for _, tc := range []struct {
		name, body         string
		status             int
		accepted, rejected bool
	}{
		{"null", `{"id":0,"result":null,"error":null}`, 200, true, false},
		{"missing", `{"id":0}`, 200, false, false},
		{"wrong-id", `{"id":1,"result":null}`, 200, false, false},
		{"empty-string", `{"id":0,"result":""}`, 200, false, false},
		{"false", `{"id":0,"result":false}`, 200, false, false},
		{"reject", `{"id":0,"result":"bad-txnmrklroot"}`, 200, false, true},
		{"duplicate", `{"id":0,"result":"duplicate"}`, 200, false, false},
		{"inconclusive", `{"id":0,"result":"inconclusive"}`, 200, false, false},
		{"http", `{"id":0,"result":null}`, 500, false, false},
		{"error", `{"id":0,"result":null,"error":{"message":"secret"}}`, 200, false, false},
		{"duplicate-key", `{"id":0,"result":"rejected","result":null}`, 200, false, false},
		{"trailing", `{"id":0,"result":null}{}`, 200, false, false},
		{"malformed", `null`, 200, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status); fmt.Fprint(w, tc.body) }))
			defer srv.Close()
			c := NewRPCClient("test", srv.URL, "1s", "", "")
			a, rejected, err := c.SubmitForgeBlock("00")
			if a != tc.accepted || rejected != tc.rejected || (!a && err == nil) {
				t.Fatalf("certainty %v %v %v", a, rejected, err)
			}
			if err != nil && strings.Contains(err.Error(), "secret") {
				t.Fatal("daemon message leaked")
			}
		})
	}
}

func TestForgeOGGReplyAndLookupCertainty(t *testing.T) {
	for _, tc := range []struct {
		result             string
		accepted, rejected bool
	}{{"true", true, false}, {"false", false, true}, {"null", false, false}, {`"true"`, false, false}} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprintf(w, `{"id":0,"result":%s}`, tc.result) }))
		c := NewRPCClient("test", srv.URL, "1s", "", "")
		a, rejected, _ := c.SubmitForgeOGG(strings.Repeat("11", 32), "00")
		if a != tc.accepted || rejected != tc.rejected {
			t.Fatal(tc, a, rejected)
		}
		srv.Close()
	}
	hash := strings.Repeat("11", 32)
	for _, result := range []string{"null", fmt.Sprintf(`{"hash":"%s","confirmations":1,"tx":[]}`, hash), fmt.Sprintf(`{"hash":"%s","confirmations":-1,"tx":["00"]}`, hash), fmt.Sprintf(`{"hash":"%s","confirmations":1,"tx":["00"]}`, strings.Repeat("22", 32))} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprintf(w, `{"id":0,"result":%s}`, result) }))
		c := NewRPCClient("test", srv.URL, "1s", "", "")
		if found, _ := c.HasForgeBlock(hash); found {
			t.Fatal("noncanonical/missing body accepted", result)
		}
		if found, _ := c.HasForgeProof(1, "00"); found {
			t.Fatal("missing proof accepted")
		}
		srv.Close()
	}
}
