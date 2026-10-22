package rpc

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDogeProposalRPCFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		ok         bool
	}{
		{"null", `{"id":0,"error":null,"result":null}`, 200, true},
		{"missing", `{"id":0,"error":null}`, 200, false},
		{"null-envelope", `null`, 200, false},
		{"empty-string", `{"id":0,"result":""}`, 200, false},
		{"duplicate", `{"id":0,"result":"duplicate"}`, 200, false},
		{"inconclusive", `{"id":0,"result":"inconclusive-not-best-prevblk"}`, 200, false},
		{"false", `{"id":0,"result":false}`, 200, false},
		{"rpc-error", `{"id":0,"result":null,"error":{"message":"bad"}}`, 200, false},
		{"wrong-id", `{"id":1,"result":null}`, 200, false},
		{"http-error", `{"id":0,"result":null}`, 500, false},
		{"trailing", `{"id":0,"result":null}{}`, 200, false},
		{"oversize", strings.Repeat(" ", 8193) + `{"id":0,"result":null}`, 200, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var call struct {
					Method string
					Params []map[string]string
				}
				if json.NewDecoder(r.Body).Decode(&call) != nil || call.Method != "getblocktemplate" || len(call.Params) != 1 || call.Params[0]["mode"] != "proposal" || len(call.Params[0]["data"]) != 160 {
					t.Error("wrong proposal request")
				}
				w.WriteHeader(tc.status)
				w.Write([]byte(tc.body))
			}))
			defer server.Close()
			client := NewRPCClient("test", server.URL, "1s", "", "")
			err := client.ValidateDogecoinProposal(context.Background(), make([]byte, 80))
			if (err == nil) != tc.ok {
				t.Fatalf("unexpected validation result: %v", err)
			}
		})
	}
}

func TestDogeProposalRPCCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := NewRPCClient("unreachable", "http://127.0.0.1:1", "1s", "", "")
	if err := client.ValidateDogecoinProposal(ctx, make([]byte, 80)); err == nil {
		t.Fatal("cancelled validation succeeded")
	}
}
