package proxy

import (
	"encoding/hex"
	"encoding/json"
	"github.com/HawyUkr/open-progpow-pool/forgepool"
	"github.com/HawyUkr/open-progpow-pool/rpc"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rlp"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestOptionalSOQRefreshActivationOutageAndSignerFallback(t *testing.T) {
	s, _, _, _, _, _ := testForgeServer(t)
	rt := s.forge
	now := uint64(time.Now().Unix() - 1)
	var height uint64 = 1125599
	var active uint32 = 35
	var rejectSOQ uint32
	var soqCalls uint32
	soqAddress := "sq1ptk79z875zygp72awur73j9zk332r0vqkjwcsp3mcdg73nqyuwktq87gczj"
	soqHex := "51205dbc511fd411101f2baee0fd1914568c5437b01693b100c7786a3d19809c7596"
	soqScript, _ := hex.DecodeString(soqHex)
	for _, sym := range []string{"DGB", "DOGE", "SOQ"} {
		symbol := sym
		bit := map[string]uint32{"DGB": 1, "DOGE": 2, "SOQ": 32}[symbol]
		genesis := map[string]string{"DGB": "7497ea1b465eb39f1c8f507bc877078fe016d6fcb6dfad3a64c98dcc6e1e8496", "DOGE": "1a91e3dace36e2be3bf030a65679fe821aa1d6ef92e7c9902eb318182c355691", "SOQ": forgepool.SOQMainnetGenesis}[symbol]
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if symbol == "SOQ" {
				atomic.AddUint32(&soqCalls, 1)
			}
			if atomic.LoadUint32(&active)&bit == 0 {
				http.Error(w, "unavailable", 503)
				return
			}
			var req struct {
				ID     interface{}
				Method string
				Params []json.RawMessage
			}
			if json.NewDecoder(r.Body).Decode(&req) != nil {
				http.Error(w, "bad request", 400)
				return
			}
			var result interface{}
			switch req.Method {
			case "getblockchaininfo":
				result = map[string]interface{}{"chain": "main", "blocks": 300000, "headers": 300000, "initialblockdownload": false, "bestblockhash": strings.Repeat("22", 32)}
			case "getblockhash":
				var h int
				json.Unmarshal(req.Params[0], &h)
				result = genesis
				if symbol == "DOGE" && h == 104679 {
					result = "35eb87ae90d44b98898fec8c39577b76cb1eb08e1261cfc10706c8ce9a1d01cf"
				}
			case "getnetworkinfo":
				result = map[string]interface{}{"connections": 8}
			case "getblockheader":
				result = map[string]interface{}{"time": now}
			case "validateaddress":
				result = map[string]interface{}{"isvalid": true, "isdilithium": true, "address": soqAddress, "scriptPubKey": soqHex}
			case "getblocktemplate":
				var p map[string]interface{}
				json.Unmarshal(req.Params[0], &p)
				if p["mode"] != "proposal" {
					result = map[string]interface{}{"version": 4, "height": 300001, "curtime": now, "mintime": now - 1, "previousblockhash": strings.Repeat("22", 32), "bits": "1d00ffff", "coinbasevalue": 5000000000, "transactions": []interface{}{}, "pow_algo": "scrypt", "pow_algo_id": 1}
				}
			default:
				t.Errorf("unexpected native method %s", req.Method)
				http.Error(w, "unexpected", 400)
				return
			}
			json.NewEncoder(w).Encode(map[string]interface{}{"id": req.ID, "result": result, "error": nil})
		}))
		t.Cleanup(server.Close)
		c := rpc.NewRPCClient(symbol, server.URL, "2s", "test", "test-only")
		switch symbol {
		case "DGB":
			rt.dgbRPC = c
			rt.dgbPayout = []byte{0x51}
		case "DOGE":
			rt.dogeRPC = c
			rt.dogePayout = []byte{0x51}
		case "SOQ":
			rt.additionalAux = []forgeAuxRuntime{{symbol: symbol, chainID: 21329, genesis: genesis, client: c, script: soqScript, address: soqAddress}}
		}
	}
	issuer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := atomic.LoadUint64(&height)
		fields := []interface{}{common.Hash{}, common.Hash{}, common.Address{}, common.Hash{}, common.Hash{}, common.Hash{}, [256]byte{}, big.NewInt(1), new(big.Int).SetUint64(h), uint64(8000000), uint64(0), now, []byte{}, common.Hash{}, [8]byte{}}
		b, _ := rlp.EncodeToBytes(fields)
		work := rpc.OGGAuxBlock{HeaderRLP: hexutil.Encode(b), WorkHash: crypto.Keccak256Hash(b).Hex(), Height: hexutil.EncodeUint64(h), Timestamp: hexutil.EncodeUint64(now), Difficulty: "0x1", Target: "0x" + strings.Repeat("ff", 32), TemplateMaxValidity: "0x12c", ProofVersion: "0x4", OptionalSOQ: h >= 1125600}
		json.NewEncoder(w).Encode(map[string]interface{}{"id": 0, "result": work, "error": nil})
	}))
	t.Cleanup(issuer.Close)
	s.upstreams = []*rpc.RPCClient{rpc.NewRPCClient("RGN", issuer.URL, "2s", "", "")}
	key, _ := crypto.GenerateKey()
	address := crypto.PubkeyToAddress(key.PublicKey)
	signer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req forgepool.MultiSignRequest
		if json.NewDecoder(r.Body).Decode(&req) != nil {
			http.Error(w, "bad request", 400)
			return
		}
		if req.Template.Mask&32 != 0 && atomic.LoadUint32(&rejectSOQ) != 0 {
			http.Error(w, "optional backend unavailable", 422)
			return
		}
		digest, e := forgepool.MultiDigest(req.Template, req.WorkHash)
		if e != nil {
			http.Error(w, "invalid", 422)
			return
		}
		sig, _ := crypto.Sign(digest[:], key)
		json.NewEncoder(w).Encode(forgepool.TemplateSignResponse{Signer: address.Hex(), Signature: hex.EncodeToString(sig)})
	}))
	t.Cleanup(signer.Close)
	client, e := newForgeSigner(ForgeSigner{Name: "DEV", URL: signer.URL, Address: address.Hex(), Timeout: "2s"})
	if e != nil {
		t.Fatal(e)
	}
	rt.multi = true
	rt.allowedExternalMask = 35
	rt.optionalSOQBlock = 1125600
	rt.signers = []*forgeSignerClient{client}
	rt.authorized = []common.Address{address}
	rt.templateQuorum = 1
	rt.templateValidity = time.Minute
	rt.multiHealth = map[uint8]*forgeChainHealth{}
	for _, bit := range []uint8{1, 2, 4, 8, 16, 32} {
		rt.multiHealth[bit] = &forgeChainHealth{}
	}
	expect := func(mask uint8) {
		t.Helper()
		if !s.fetchMultiTemplate() {
			t.Fatalf("refresh failed for expected mask %d", mask)
		}
		if job := rt.jobs[rt.currentID]; job == nil || job.ExternalMask != mask {
			t.Fatal("wrong refreshed participation", mask)
		}
	}
	expect(3)
	if atomic.LoadUint32(&soqCalls) != 0 {
		t.Fatal("SOQ backend contacted before activation")
	}
	atomic.StoreUint64(&height, 1125600)
	expect(35)
	atomic.StoreUint32(&active, 3)
	expect(3)
	atomic.StoreUint32(&active, 35)
	atomic.StoreUint32(&rejectSOQ, 1)
	expect(3)
	atomic.StoreUint32(&rejectSOQ, 0)
	atomic.StoreUint32(&active, 32)
	for i := 0; i < 2; i++ {
		if s.fetchMultiTemplate() {
			t.Fatal("anchor outage hysteresis bypassed")
		}
	}
	expect(32)
	atomic.StoreUint32(&active, 0)
	if s.fetchMultiTemplate() || rt.currentID != "" {
		t.Fatal("all-native outage left work active")
	}
	atomic.StoreUint32(&active, 32)
	expect(32)
}
