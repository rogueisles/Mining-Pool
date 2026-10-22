package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func validConfig() config {
	e := endpoint{URL: "http://127.0.0.1:32556", Username: "pilot", Password: "test-only-secret", Genesis: strings.Repeat("ab", 32)}
	dgb := e
	dgb.Genesis = "7497ea1b465eb39f1c8f507bc877078fe016d6fcb6dfad3a64c98dcc6e1e8496"
	return config{KeyFile: "/private/signer.key", Listen: "127.0.0.1:31550", SignerAddress: "0x1111111111111111111111111111111111111111", DogeScript: "76a914" + strings.Repeat("22", 20) + "88ac", Digibyte: dgb, Dogecoin: e}
}
func TestConfigRejectsUnpinnedOrPublicServices(t *testing.T) {
	if e := validConfig().validate(); e != nil {
		t.Fatal(e)
	}
	for _, address := range []string{"http://192.0.2.1:32556", "http://localhost:32556", "https://127.0.0.1:32556", "http://user:password@127.0.0.1:32556", "http://127.0.0.1:32556/?token=x", "http://127.0.0.1:32556/wallet/x", "http://127.0.0.1", "http://127.0.0.1:0"} {
		c := validConfig()
		c.Digibyte.URL = address
		if c.validate() == nil {
			t.Fatalf("accepted unsafe endpoint %s", address)
		}
	}
	for _, listen := range []string{"0.0.0.0:31550", ":31550", "localhost:31550", "127.0.0.1:0"} {
		c := validConfig()
		c.Listen = listen
		if c.validate() == nil {
			t.Fatal("accepted non-loopback/invalid listener")
		}
	}
	c := validConfig()
	c.DogeScript = "51"
	if c.validate() == nil {
		t.Fatal("accepted non-pilot payout")
	}
	c = validConfig()
	c.Dogecoin.Genesis = strings.Repeat("0", 64)
	if c.validate() == nil {
		t.Fatal("accepted zero genesis")
	}
	c = validConfig()
	c.Dogecoin.Password = ""
	if c.validate() == nil {
		t.Fatal("accepted missing authentication")
	}
}

func TestMainnetHealthFailsClosed(t *testing.T) {
	cases := []struct {
		name, chain, genesis string
		ibd                  bool
		peers                int
		age                  int64
		headers              int64
		want                 bool
	}{
		{"healthy", "main", strings.Repeat("ab", 32), false, 8, 30, 100, true},
		{"regtest", "regtest", strings.Repeat("ab", 32), false, 8, 30, 100, false},
		{"syncing", "main", strings.Repeat("ab", 32), true, 8, 30, 100, false},
		{"wrong-genesis", "main", strings.Repeat("cd", 32), false, 8, 30, 100, false},
		{"no-peers", "main", strings.Repeat("ab", 32), false, 0, 30, 100, false},
		{"stale-tip", "main", strings.Repeat("ab", 32), false, 8, 3601, 100, false},
		{"future-tip", "main", strings.Repeat("ab", 32), false, 8, -180, 100, false},
		{"behind-headers", "main", strings.Repeat("ab", 32), false, 8, 30, 103, false},
		{"dgb-accepted-future-tip", "main", validConfig().Digibyte.Genesis, false, 8, -283, 100, true},
		{"dgb-too-far-future", "main", validConfig().Digibyte.Genesis, false, 8, -7300, 100, false},
		{"dgb-stale-tip", "main", validConfig().Digibyte.Genesis, false, 8, 3700, 100, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				user, password, ok := r.BasicAuth()
				if !ok || user != "pilot" || password != "test-only-secret" {
					t.Error("missing/wrong RPC credentials")
					w.WriteHeader(401)
					return
				}
				var req struct{ Method string }
				json.NewDecoder(r.Body).Decode(&req)
				var result interface{}
				switch req.Method {
				case "getblockchaininfo":
					result = map[string]interface{}{"chain": tc.chain, "blocks": 100, "headers": tc.headers, "initialblockdownload": tc.ibd, "bestblockhash": strings.Repeat("ee", 32)}
				case "getblockhash":
					result = tc.genesis
				case "getnetworkinfo":
					result = map[string]interface{}{"connections": tc.peers, "networkactive": true}
				case "getblockheader":
					result = map[string]interface{}{"time": time.Now().Unix() - tc.age}
				default:
					t.Error("unexpected/non-read RPC method")
					w.WriteHeader(400)
					return
				}
				json.NewEncoder(w).Encode(map[string]interface{}{"id": 1, "result": result, "error": nil})
			}))
			defer server.Close()
			e := validConfig().Digibyte
			e.Genesis = strings.Repeat("ab", 32)
			if strings.HasPrefix(tc.name, "dgb-") {
				e.Genesis = validConfig().Digibyte.Genesis
			}
			e.URL = server.URL
			err := e.healthy(context.Background())
			if (err == nil) != tc.want {
				t.Fatalf("healthy=%v error=%v", tc.want, err)
			}
		})
	}
}
func TestRPCDoesNotLeakErrorBodyOrFollowRedirects(t *testing.T) {
	for _, status := range []int{401, 500, 302} {
		follow := false
		target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { follow = true }))
		defer target.Close()
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Location", target.URL)
			w.WriteHeader(status)
			w.Write([]byte("test-only-secret"))
		}))
		e := validConfig().Dogecoin
		e.URL = server.URL
		err := e.healthy(context.Background())
		server.Close()
		if err == nil || strings.Contains(err.Error(), "test-only-secret") || follow {
			t.Fatalf("unsafe RPC failure/redirect behavior: %v", err)
		}
	}
}
