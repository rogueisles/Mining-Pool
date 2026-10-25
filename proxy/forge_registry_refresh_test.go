package proxy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/HawyUkr/open-progpow-pool/forgepool"
	"github.com/HawyUkr/open-progpow-pool/rpc"
	"github.com/HawyUkr/open-progpow-pool/storage"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rlp"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRegistryV5RefreshRecoveryAndOptionalFallback(t *testing.T) {
	s, _, _, _, _, _ := testForgeServer(t)
	rt := s.forge
	now := uint64(time.Now().Unix() - 1)
	var height uint64 = 1125600
	var active uint32 = 99
	var rejectSOQ uint32
	var hugeTST uint32
	var badTST uint32
	var slowTST uint32
	var rgnSubmittedAt int64
	var soqSubmittedAt int64
	var soqCalls uint32
	soqAddress := "sq1ptk79z875zygp72awur73j9zk332r0vqkjwcsp3mcdg73nqyuwktq87gczj"
	soqHex := "51205dbc511fd411101f2baee0fd1914568c5437b01693b100c7786a3d19809c7596"
	soqScript, _ := hex.DecodeString(soqHex)
	for _, sym := range []string{"DGB", "DOGE", "SOQ", "TST"} {
		symbol := sym
		bit := map[string]uint32{"DGB": 1, "DOGE": 2, "SOQ": 32, "TST": 64}[symbol]
		genesis := map[string]string{"DGB": "7497ea1b465eb39f1c8f507bc877078fe016d6fcb6dfad3a64c98dcc6e1e8496", "DOGE": "1a91e3dace36e2be3bf030a65679fe821aa1d6ef92e7c9902eb318182c355691", "SOQ": forgepool.SOQMainnetGenesis, "TST": strings.TrimPrefix(common.HexToHash("0x5544").Hex(), "0x")}[symbol]
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if symbol == "TST" && atomic.LoadUint32(&slowTST) != 0 {
				select {
				case <-r.Context().Done():
					return
				case <-time.After(3 * time.Second):
				}
			}
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
			case "submitblock":
				if symbol == "SOQ" {
					atomic.StoreInt64(&soqSubmittedAt, time.Now().UnixNano())
					time.Sleep(500 * time.Millisecond)
				}
				result = nil
			case "getblocktemplate":
				var p map[string]interface{}
				json.Unmarshal(req.Params[0], &p)
				if p["mode"] != "proposal" {
					result = map[string]interface{}{"version": 4, "height": 300001, "curtime": now, "mintime": now - 1, "previousblockhash": strings.Repeat("22", 32), "bits": "1d00ffff", "coinbasevalue": 5000000000, "transactions": []interface{}{}, "pow_algo": "scrypt", "pow_algo_id": 1}
					if symbol == "TST" {
						row := result.(map[string]interface{})
						if atomic.LoadUint32(&hugeTST) != 0 {
							row["default_witness_commitment"] = strings.Repeat("00", 17000)
						}
						if atomic.LoadUint32(&badTST) != 0 {
							row["coinbasevalue"] = -1
						}
					}
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
			rt.additionalAux = append(rt.additionalAux, forgeAuxRuntime{symbol: symbol, chainID: 21329, genesis: genesis, client: c, script: soqScript, address: soqAddress})
		case "TST":
			rt.additionalAux = append(rt.additionalAux, forgeAuxRuntime{symbol: symbol, chainID: 99, genesis: genesis, client: c, script: []byte{0x51}})
		}
	}
	key, _ := crypto.GenerateKey()
	address := crypto.PubkeyToAddress(key.PublicKey)
	admin, _ := crypto.GenerateKey()
	parentHash := common.HexToHash("0xabc")
	registry := forgepool.RegistrySnapshot{Admin: forgepool.RegistryAdmin{Keys: []common.Address{crypto.PubkeyToAddress(admin.PublicKey)}, Quorum: 1, Epoch: 1}}
	for i, symbol := range []string{"DGB", "DOGE", "SOQ", "TST"} {
		id := map[string]uint32{"DGB": 0, "DOGE": 98, "SOQ": 21329, "TST": 99}[symbol]
		profile := forgepool.RegistryProfileBitcoin80
		script := []byte{0x51}
		genesis := common.HexToHash("0x5544")
		switch symbol {
		case "DGB":
			genesis = common.HexToHash("7497ea1b465eb39f1c8f507bc877078fe016d6fcb6dfad3a64c98dcc6e1e8496")
		case "DOGE":
			genesis = common.HexToHash("1a91e3dace36e2be3bf030a65679fe821aa1d6ef92e7c9902eb318182c355691")
		case "SOQ":
			genesis = common.HexToHash(forgepool.SOQMainnetGenesis)
			profile = forgepool.RegistryProfileSOQ
			script = soqScript
		}
		registry.Coins = append(registry.Coins, forgepool.RegistryCoin{Slot: uint8(i + 1), ChainID: id, Genesis: genesis, Profile: profile, Core: i < 3, EnabledFrom: 1125600, Treasury: common.Hash(sha256.Sum256(script))})
	}
	root, _ := forgepool.RegistrySnapshotRoot(registry)
	snapshot, _ := rlp.EncodeToBytes(registry)
	issuer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ID     interface{}
			Method string
		}
		json.NewDecoder(r.Body).Decode(&request)
		if request.Method == "eth_submitAuxBlock" {
			atomic.StoreInt64(&rgnSubmittedAt, time.Now().UnixNano())
			json.NewEncoder(w).Encode(map[string]interface{}{"id": request.ID, "error": nil, "result": true})
			return
		}
		h := atomic.LoadUint64(&height)
		if request.Method == "eth_forgeRegistry" {
			json.NewEncoder(w).Encode(map[string]interface{}{"id": request.ID, "error": nil, "result": rpc.ForgeRegistryReply{ParentHash: parentHash, ParentHeight: hexutil.EncodeUint64(h - 1), ActivationBlock: hexutil.EncodeUint64(1125600), Root: root, Checkpoint: "0x0", SnapshotRLP: hexutil.Encode(snapshot), Genesis: common.HexToHash("0x9876"), ChainID: "0x111c8", WorkSigners: []common.Address{address}, MinDelay: "0x2", MinOperationInterval: "0x2"}})
			return
		}
		fields := []interface{}{parentHash, common.Hash{}, common.Address{}, common.Hash{}, common.Hash{}, common.Hash{}, [256]byte{}, big.NewInt(1), new(big.Int).SetUint64(h), uint64(8000000), uint64(0), now, []byte{}, common.Hash{}, [8]byte{}}
		b, _ := rlp.EncodeToBytes(fields)
		work := rpc.OGGAuxBlock{HeaderRLP: hexutil.Encode(b), WorkHash: crypto.Keccak256Hash(b).Hex(), Height: hexutil.EncodeUint64(h), Timestamp: hexutil.EncodeUint64(now), Difficulty: "0x1", Target: "0x" + strings.Repeat("ff", 32), TemplateMaxValidity: "0x12c", ProofVersion: "0x5", PreviousBlockHash: parentHash.Hex()}
		json.NewEncoder(w).Encode(map[string]interface{}{"id": request.ID, "result": work, "error": nil})
	}))
	t.Cleanup(issuer.Close)
	s.upstreams = []*rpc.RPCClient{rpc.NewRPCClient("RGN", issuer.URL, "2s", "", "")}
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
		bound, e := forgepool.MultiRequestWork(req)
		if e != nil {
			http.Error(w, "invalid", 422)
			return
		}
		digest, e := forgepool.MultiDigest(req.Template, bound)
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
	rt.registryBlock = 1125600
	rt.treasuryScripts = map[common.Hash][]byte{common.Hash(sha256.Sum256([]byte{0x51})): []byte{0x51}, common.Hash(sha256.Sum256(soqScript)): soqScript}
	rt.treasuryAddresses = map[common.Hash]string{common.Hash(sha256.Sum256(soqScript)): soqAddress}
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
	expect(35)
	initial := rt.jobs[rt.currentID]
	if initial.AuxSymbols["TST"] != 99 || len(initial.Registry.Members) != 4 {
		t.Fatal("registered extra was not included")
	}
	if initial.Signed.Multi.Version != 5 {
		t.Fatal("V5 job missing")
	}
	proof, e := initial.Digibyte.BuildMultiProof(make([]byte, 4), make([]byte, 4), initial.Digibyte.Template.CurTime, 0, *initial.Signed.Multi, initial.Signed.Signatures)
	if e != nil {
		t.Fatal(e)
	}
	blob, e := forgepool.EncodeFlexibleProof(&forgepool.FlexibleProof{Multi: *proof, Registry: *initial.Registry})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = forgepool.DecodeOGGProof(blob); e != nil {
		t.Fatal(e)
	}
	router := &rpcForgeSubmitter{issuers: s.upstreams, dgb: rt.dgbRPC, doge: rt.dogeRPC, auxClients: map[string]*rpc.RPCClient{}, authorized: rt.authorized, quorum: rt.templateQuorum}
	for _, aux := range rt.additionalAux {
		router.auxClients[aux.symbol] = aux.client
	}
	solution := &forgeSolution{ExtraNonce1: make([]byte, 4), ExtraNonce2: make([]byte, 4), NTime: initial.Digibyte.Template.CurTime}
	solution.Header, _ = initial.Digibyte.Header(solution.ExtraNonce1, solution.ExtraNonce2, solution.NTime, 0)
	intent := router.prepareIntent(initial, solution, forgeHits{OGG: true, Digibyte: true, Dogecoin: true, Soqucoin: true, Registered: [5]forgeRegisteredHit{{Symbol: "TST", ChainID: 99, Hit: true}}})
	if len(intent.Blocks) < 5 || intent.Blocks[0].Chain != "OGG" || intent.Blocks[0].State != "pending" {
		t.Fatal("V5 main-chain submission is blocked behind optional native work")
	}
	receipt := storage.ForgeShare{ID: forgePayloadDigest(solution.Header), Login: "0x" + strings.Repeat("ab", 20), Worker: "priority-test", Height: int64(initial.Height), Difficulty: 1, RoundDifficulty: 1, TimestampMS: time.Now().UnixNano() / int64(time.Millisecond), WindowSeconds: 3600, WorkHash: initial.OGG.WorkHash}
	if valid, _, _, err := s.processForgeIntent(router, initial, solution, forgeHits{OGG: true, Digibyte: true, Dogecoin: true, Soqucoin: true, Registered: [5]forgeRegisteredHit{{Symbol: "TST", ChainID: 99, Hit: true}}}, receipt); err != nil || !valid {
		t.Fatal("V5 priority submission failed", err)
	}
	if rgn := atomic.LoadInt64(&rgnSubmittedAt); rgn == 0 || rgn >= atomic.LoadInt64(&soqSubmittedAt) {
		t.Fatal("slow SOQ submission delayed RGN acceptance")
	}
	rt.registryOperationFile = filepath.Join(t.TempDir(), "unapproved-operation.json")
	if err := os.WriteFile(rt.registryOperationFile, []byte("{corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	expect(35)
	if rt.jobs[rt.currentID].AuxSymbols["TST"] != 99 {
		t.Fatal("bad administrator file interrupted approved optional work")
	}
	rt.registryOperationFile = ""
	atomic.StoreUint32(&badTST, 1)
	expect(35)
	if _, ok := rt.jobs[rt.currentID].AuxSymbols["TST"]; ok {
		t.Fatal("invalid native candidate included")
	}
	atomic.StoreUint32(&badTST, 0)
	atomic.StoreUint32(&hugeTST, 1)
	expect(3)
	if _, ok := rt.jobs[rt.currentID].AuxSymbols["TST"]; ok {
		t.Fatal("oversized optional native proof published")
	}
	atomic.StoreUint32(&hugeTST, 0)
	atomic.StoreUint32(&slowTST, 1)
	started := time.Now()
	expect(35)
	if time.Since(started) > 2800*time.Millisecond {
		t.Fatal("slow optional source stalled working core")
	}
	if _, ok := rt.jobs[rt.currentID].AuxSymbols["TST"]; ok {
		t.Fatal("timed out optional source included")
	}
	atomic.StoreUint32(&slowTST, 0)
	atomic.StoreUint32(&active, 3)
	expect(3)
	atomic.StoreUint32(&active, 35)
	atomic.StoreUint32(&rejectSOQ, 1)
	expect(3)
	atomic.StoreUint32(&rejectSOQ, 0)
	atomic.StoreUint32(&active, 32)
	expect(32)
	atomic.StoreUint32(&active, 0)
	if s.fetchMultiTemplate() || rt.currentID != "" {
		t.Fatal("all-native outage left work active")
	}
	atomic.StoreUint32(&active, 32)
	expect(32)
}
