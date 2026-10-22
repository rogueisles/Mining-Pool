package rpc

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestForgeBodyObservationDoesNotInventAcceptance(t *testing.T) {
	hash := strings.Repeat("ab", 32)
	for _, tc := range []struct{ name, fields, want string }{
		{"canonical", `"confirmations":1,"tx":["cb"]`, "canonical"},
		{"noncanonical", `"confirmations":-1,"tx":["cb"]`, "noncanonical"},
		{"headers-only", `"confirmations":-1,"tx":[]`, "unknown"},
		{"missing-confirmations", `"tx":["cb"]`, "unknown"},
		{"null-confirmations", `"confirmations":null,"tx":["cb"]`, "unknown"},
		{"zero-confirmations", `"confirmations":0,"tx":["cb"]`, "unknown"},
		{"wrong-negative", `"confirmations":-2,"tx":["cb"]`, "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprintf(w, `{"id":0,"result":{"hash":"%s",%s}}`, hash, tc.fields)
			}))
			defer srv.Close()
			client := NewRPCClient("observation", srv.URL, "1s", "", "")
			got, err := client.ForgeBlockObservation("0x" + hash)
			if err != nil || got != tc.want {
				t.Fatalf("%s %v", got, err)
			}
			accepted, err := client.HasForgeBlock(hash)
			if err != nil || accepted != (tc.want == "canonical") {
				t.Fatal("observed branch treated as accepted")
			}
			wrong, err := client.ForgeBlockObservation(strings.Repeat("cd", 32))
			if err != nil || wrong != "unknown" {
				t.Fatal("wrong block identity matched")
			}
		})
	}
}
