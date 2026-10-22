package rpc

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMultiFamilyPostGenesisIdentity(t *testing.T) {
	for _, doge := range []bool{true, false} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var req struct {
				ID     interface{}
				Params []int
			}
			json.NewDecoder(r.Body).Decode(&req)
			hash := "594a42d8fe16382085dc982135df72cf8fcea12d34e6efd566e2f9e442e2136f"
			if doge {
				hash = "35eb87ae90d44b98898fec8c39577b76cb1eb08e1261cfc10706c8ce9a1d01cf"
			}
			json.NewEncoder(w).Encode(map[string]interface{}{"id": req.ID, "result": hash, "error": nil})
		}))
		c := NewRPCClient("family", server.URL, "1s", "test", "test-only")
		if (c.CheckDogecoinIdentity() == nil) != doge {
			t.Fatal("Dogecoin post-genesis identity confused")
		}
		if (c.CheckDingocoinIdentity() == nil) == doge {
			t.Fatal("Dingocoin post-genesis identity confused")
		}
		server.Close()
	}
}
