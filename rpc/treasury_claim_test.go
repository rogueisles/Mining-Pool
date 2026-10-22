package rpc

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestFullTreasuryClaimRequiresExactNativeBoundary(t *testing.T) {
	for _, tc := range []struct {
		name, original, increment string
		ok                        bool
	}{
		{"full-claim", `null`, `"bad-cb-amount"`, true},
		{"underclaim", `null`, `null`, false},
		{"overclaim", `"bad-cb-amount"`, `"bad-cb-amount"`, false},
		{"unrelated-rejection", `null`, `"bad-txns"`, false},
		{"inconclusive", `null`, `"inconclusive-not-best-prevblk"`, false},
		{"duplicate", `null`, `"duplicate"`, false},
		{"reason-prefix", `null`, `"bad-cb-amount-extra"`, false},
		{"reason-suffix", `null`, `"prefix-bad-cb-amount"`, false},
		{"object", `null`, `{"reason":"bad-cb-amount"}`, false},
		{"false", `null`, `false`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				result := tc.original
				if atomic.AddInt32(&calls, 1) == 2 {
					result = tc.increment
				}
				w.Write([]byte(`{"id":0,"error":null,"result":` + result + `}`))
			}))
			defer server.Close()
			original, incremented := make([]byte, 81), make([]byte, 81)
			incremented[36], incremented[80] = 1, 1
			client := NewRPCClient("native", server.URL, "1s", "", "")
			err := client.ValidateFullTreasuryClaim(context.Background(), original, incremented)
			if (err == nil) != tc.ok {
				t.Fatalf("unexpected claim validation: %v", err)
			}
			if tc.original != `null` && atomic.LoadInt32(&calls) != 1 {
				t.Fatal("increment checked after original rejection")
			}
		})
	}
}

func TestNativeProposalEnvelopeRejectsAmbiguity(t *testing.T) {
	for _, body := range []string{
		`{"id":0,"error":null,"result":null,"result":"bad-cb-amount"}`,
		`{"id":0,"error":null,"result":"bad-cb-amount","result":null}`,
		`{"id":1,"id":0,"error":null,"result":null}`,
		`{"id":0,"error":{"message":"bad-cb-amount"},"error":null,"result":null}`,
		`{"id":0,"result":null}`,
		`{"id":0,"error":null}`,
		`{"id":0,"error":{"message":"bad-cb-amount"},"result":"bad-cb-amount"}`,
		`{"id":0,"error":null,"result":null}{}`,
		`null`,
		strings.Repeat(" ", 8193) + `{"id":0,"error":null,"result":null}`,
	} {
		t.Run(body[:minLength(len(body), 80)], func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(body)) }))
			defer server.Close()
			client := NewRPCClient("native", server.URL, "1s", "", "")
			if _, err := client.nativeProposalResult(context.Background(), make([]byte, 80)); err == nil {
				t.Fatal("ambiguous native response accepted")
			}
		})
	}
}

func minLength(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func TestFullTreasuryClaimRejectsChangedContextAndCancellation(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		json.NewEncoder(w).Encode(map[string]interface{}{"id": 0, "error": nil, "result": nil})
	}))
	defer server.Close()
	client := NewRPCClient("native", server.URL, "1s", "", "")
	original := make([]byte, 81)
	for _, offset := range []int{0, 4, 35, 68, 72, 76} {
		probe := append([]byte(nil), original...)
		probe[offset] = 1
		if err := client.ValidateFullTreasuryClaim(context.Background(), original, probe); err == nil {
			t.Fatal("changed context accepted")
		}
	}
	if err := client.ValidateFullTreasuryClaim(context.Background(), original, bytes.Repeat([]byte{0}, 81)); err == nil {
		t.Fatal("unchanged probe accepted")
	}
	if atomic.LoadInt32(&calls) != 0 {
		t.Fatal("invalid pair reached native RPC")
	}
	probe := append([]byte(nil), original...)
	probe[36] = 1
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := client.ValidateFullTreasuryClaim(ctx, original, probe); err == nil {
		t.Fatal("cancelled request accepted")
	}
}
