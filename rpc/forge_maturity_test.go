package rpc

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestForgeMaturityExclusionRequiresStableDifferentCanonicalProof(t *testing.T) {
	for _, tc := range []struct {
		name, extra, head string
		change, missing   bool
		excluded          bool
	}{
		{name: "mature different proof", extra: "0x0103", head: "0x4a", excluded: true},
		{name: "too young", extra: "0x0103", head: "0x49"},
		{name: "our canonical proof", extra: "0x0102", head: "0x4a"},
		{name: "reorg during check", extra: "0x0103", head: "0x4a", change: true},
		{name: "missing block", head: "0x4a", missing: true},
		{name: "invalid head", extra: "0x0103", head: "garbage"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reads := 0
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var q struct{ Method string }
				json.NewDecoder(r.Body).Decode(&q)
				if q.Method == "eth_blockNumber" {
					fmt.Fprintf(w, `{"id":0,"result":%q}`, tc.head)
					return
				}
				if q.Method != "eth_getBlockByNumber" {
					t.Error("unexpected method")
					w.WriteHeader(500)
					return
				}
				reads++
				if tc.missing {
					fmt.Fprint(w, `{"id":0,"result":null}`)
					return
				}
				hash := "ab"
				if tc.change && reads > 1 {
					hash = "cd"
				}
				fmt.Fprintf(w, `{"id":0,"result":{"number":"0x2a","hash":"0x%s","extraData":%q}}`, strings.Repeat(hash, 32), tc.extra)
			}))
			defer s.Close()
			c := NewRPCClient("maturity", s.URL, "1s", "", "")
			got, _ := c.ForgeProofExcludedAtMaturity(42, "0102", 32)
			if got != tc.excluded {
				t.Fatalf("exclusion %v want %v", got, tc.excluded)
			}
			if got, err := c.ForgeProofExcludedAtMaturity(42, "0102", 31); got || err == nil {
				t.Fatal("shallower than pool minimum accepted")
			}
		})
	}
}
