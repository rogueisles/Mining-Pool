package proxy

import (
	"encoding/json"
	"github.com/HawyUkr/open-progpow-pool/forgepool"
	"github.com/HawyUkr/open-progpow-pool/rpc"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func multiProcessConfiguration(t *testing.T, cfg *Config, rt *forgeRuntime, gbt rpc.BlockTemplateReply, observers ...func(forgepool.MultiSignRequest, []byte)) {
	t.Helper()
	multiTemplateTestSigner(t, rt, observers...)
	cfg.Proxy.Forge.MultiChain = true
	cfg.Proxy.Forge.ExtraNonce1Size = 4
	cfg.Proxy.Forge.ExtraNonce2Size = 4
	cfg.Proxy.Forge.TemplateSigners = []ForgeSigner{{Name: "DEV-V3", URL: rt.signers[0].url, Address: rt.authorized[0].Hex(), Timeout: "2s"}}
	ids := map[string]uint32{"DOGE": 98, "PEP": 63, "DINGO": 50, "BELLS": 16}
	genesis := map[string]string{"DGB": "7497ea1b465eb39f1c8f507bc877078fe016d6fcb6dfad3a64c98dcc6e1e8496", "DOGE": "1a91e3dace36e2be3bf030a65679fe821aa1d6ef92e7c9902eb318182c355691", "PEP": "37981c0c48b8d48965376c8a42ece9a0838daadb93ff975cb091f57f8c2a5faa", "DINGO": "1a91e3dace36e2be3bf030a65679fe821aa1d6ef92e7c9902eb318182c355691", "BELLS": "e5be24df57c43a82d15c2f06bda961296948f8f8eb48501bed1efb929afe0698"}
	symbols := []string{"DGB", "DOGE", "PEP", "DINGO", "BELLS"}
	if cfg.Proxy.Forge.ExternalChainPolicy == "dgb-doge" || os.Getenv("FORGE_TEST_EXTERNAL_CHAIN_POLICY") == "dgb-doge" {
		cfg.Proxy.Forge.ExternalChainPolicy = "dgb-doge"
		symbols = []string{"DGB", "DOGE"}
	}
	for _, symbol := range symbols {
		name := symbol
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var req struct {
				ID     interface{}
				Method string
				Params []json.RawMessage
			}
			if json.NewDecoder(r.Body).Decode(&req) != nil {
				w.WriteHeader(400)
				return
			}
			var result interface{}
			switch req.Method {
			case "getblockchaininfo":
				result = map[string]interface{}{"chain": "main", "blocks": 300000, "headers": 300000, "initialblockdownload": false, "bestblockhash": strings.Repeat("22", 32)}
			case "getblockhash":
				var h int
				json.Unmarshal(req.Params[0], &h)
				if h == 104679 && name == "DOGE" {
					result = "35eb87ae90d44b98898fec8c39577b76cb1eb08e1261cfc10706c8ce9a1d01cf"
				} else if h == 1 && name == "DINGO" {
					result = "594a42d8fe16382085dc982135df72cf8fcea12d34e6efd566e2f9e442e2136f"
				} else {
					result = genesis[name]
				}
			case "getnetworkinfo":
				result = map[string]interface{}{"connections": 8}
			case "getblockheader":
				result = map[string]interface{}{"time": time.Now().Unix()}
			case "getblocktemplate":
				var params map[string]interface{}
				json.Unmarshal(req.Params[0], &params)
				if params["mode"] != "proposal" {
					result = gbt
				}
			default:
				t.Error("unexpected native submission in share-only process test: " + req.Method)
				w.WriteHeader(500)
				return
			}
			json.NewEncoder(w).Encode(map[string]interface{}{"id": req.ID, "result": result, "error": nil})
		}))
		t.Cleanup(server.Close)
		ep := ForgeChainEndpoint{Url: server.URL, Username: "fixture", Password: "test-only", PayoutScript: "51", Timeout: "2s"}
		if name == "DGB" {
			cfg.Proxy.Forge.Digibyte = ep
		} else if name == "DOGE" {
			cfg.Proxy.Forge.Dogecoin = ep
		} else {
			cfg.Proxy.Forge.AdditionalAux = append(cfg.Proxy.Forge.AdditionalAux, ForgeAuxEndpoint{Symbol: name, ChainID: ids[name], Genesis: genesis[name], Endpoint: ep})
		}
	}
}
