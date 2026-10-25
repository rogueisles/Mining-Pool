package proxy

// This opt-in integration test is never compiled into a release executable.
// It requires an owned private-CPU RGN network and real native regtest nodes.
// The bridge changes only the chain-name health label and the out-of-range
// DOGE historical checkpoint guard. Native genesis, peers, templates, full
// proposals, Scrypt work, block submissions and canonical receipts stay real.
import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/HawyUkr/open-progpow-pool/forgepool"
	"github.com/HawyUkr/open-progpow-pool/policy"
	"github.com/HawyUkr/open-progpow-pool/rpc"
	"github.com/HawyUkr/open-progpow-pool/storage"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type realRegistryNative struct {
	URL, Username, Password, Script, NextScript, Address, NextAddress, Genesis string
	ChainID                                                                    uint32
}
type realRegistryDriver struct {
	RGN        string
	Native     map[string]realRegistryNative
	SignerKeys []string
}

func realRegistryCall(t *testing.T, endpoint, username, password, method string, params interface{}, out interface{}) {
	t.Helper()
	raw, _ := json.Marshal(map[string]interface{}{"id": 1, "jsonrpc": "2.0", "method": method, "params": params})
	request, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.SetBasicAuth(username, password)
	client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{Proxy: nil}}
	reply, err := client.Do(request)
	if err != nil {
		t.Fatal("private lab RPC unavailable", method, err)
	}
	defer reply.Body.Close()
	var envelope struct {
		Result json.RawMessage
		Error  json.RawMessage
	}
	if json.NewDecoder(io.LimitReader(reply.Body, 32*1024*1024)).Decode(&envelope) != nil || (len(envelope.Error) > 0 && string(envelope.Error) != "null") {
		t.Fatal("private lab RPC rejected request", method, string(envelope.Error))
	}
	if out != nil && json.Unmarshal(envelope.Result, out) != nil {
		t.Fatal("private lab RPC malformed result", method)
	}
}
func realRegistryLoopback(t *testing.T, endpoint string) {
	t.Helper()
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "http" || !net.ParseIP(u.Hostname()).IsLoopback() || u.Port() == "" {
		t.Fatal("integration requires explicit loopback endpoints")
	}
}
func realRegistryBridge(t *testing.T, symbol string, native realRegistryNative, blocked *int32) *httptest.Server {
	t.Helper()
	realRegistryLoopback(t, native.URL)
	var info struct{ Chain string }
	realRegistryCall(t, native.URL, native.Username, native.Password, "getblockchaininfo", []interface{}{}, &info)
	if info.Chain != "regtest" {
		t.Fatal("integration refused non-regtest native node", symbol)
	}
	var genesis string
	realRegistryCall(t, native.URL, native.Username, native.Password, "getblockhash", []interface{}{0}, &genesis)
	if genesis != native.Genesis {
		t.Fatal("private native genesis does not match driver", symbol)
	}
	client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{Proxy: nil}}
	bridge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.LoadInt32(blocked) != 0 {
			http.Error(w, "test-only native outage", http.StatusServiceUnavailable)
			return
		}
		data, err := io.ReadAll(io.LimitReader(r.Body, 16*1024*1024+1))
		if err != nil || len(data) > 16*1024*1024 {
			http.Error(w, "bounded request", 400)
			return
		}
		var call struct {
			ID     json.RawMessage
			Method string
			Params []json.RawMessage
		}
		if json.Unmarshal(data, &call) != nil {
			http.Error(w, "invalid request", 400)
			return
		}
		if symbol == "DOGE" && call.Method == "getblockhash" && len(call.Params) == 1 && string(call.Params[0]) == "104679" {
			json.NewEncoder(w).Encode(map[string]interface{}{"id": json.RawMessage(call.ID), "error": nil, "result": "35eb87ae90d44b98898fec8c39577b76cb1eb08e1261cfc10706c8ce9a1d01cf"})
			return
		}
		request, e := http.NewRequest(http.MethodPost, native.URL, bytes.NewReader(data))
		if e != nil {
			http.Error(w, "bad endpoint", 500)
			return
		}
		request.Header.Set("Content-Type", "application/json")
		request.SetBasicAuth(native.Username, native.Password)
		response, e := client.Do(request)
		if e != nil {
			http.Error(w, "native unavailable", 503)
			return
		}
		defer response.Body.Close()
		body, e := io.ReadAll(io.LimitReader(response.Body, 32*1024*1024+1))
		if e != nil || len(body) > 32*1024*1024 {
			http.Error(w, "bounded reply", 502)
			return
		}
		if call.Method == "getblockchaininfo" && response.StatusCode == 200 {
			var envelope map[string]json.RawMessage
			var result map[string]interface{}
			if json.Unmarshal(body, &envelope) != nil || json.Unmarshal(envelope["result"], &result) != nil || result["chain"] != "regtest" {
				http.Error(w, "native left regtest", 502)
				return
			}
			result["chain"] = "main"
			envelope["result"], _ = json.Marshal(result)
			body, _ = json.Marshal(envelope)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(response.StatusCode)
		w.Write(body)
	}))
	t.Cleanup(bridge.Close)
	return bridge
}
func mineRealRegistryShare(t *testing.T, header []byte, target *big.Int) uint32 {
	t.Helper()
	target = new(big.Int).Set(target)
	stratum, _ := forgepool.StratumTargetForDifficulty(big.NewInt(1))
	if target.Cmp(stratum) > 0 {
		target = stratum
	}
	stop := make(chan struct{})
	found := make(chan uint32, 1)
	var workers sync.WaitGroup
	for worker := uint32(0); worker < 4; worker++ {
		workers.Add(1)
		go func(start uint32) {
			defer workers.Done()
			local := append([]byte(nil), header...)
			for nonce := start; ; nonce += 4 {
				select {
				case <-stop:
					return
				default:
				}
				binary.LittleEndian.PutUint32(local[76:], nonce)
				hash, err := forgepool.ScryptHash(local)
				if err == nil && forgepool.HashValue(hash).Cmp(target) <= 0 {
					select {
					case found <- nonce:
					case <-stop:
					}
					return
				}
			}
		}(worker)
	}
	timer := time.NewTimer(240 * time.Second)
	defer timer.Stop()
	defer func() { close(stop); workers.Wait() }()
	select {
	case nonce := <-found:
		return nonce
	case <-timer.C:
		t.Fatal("real Scrypt mining budget exhausted")
	}
	return 0
}
func TestRegistryV5RealPoolSignerStratumAndRecovery(t *testing.T) {
	path := os.Getenv("RGN_REAL_LAB")
	if path == "" {
		t.Skip("requires owned private RGN/native regtest lab")
	}
	if !filepath.IsAbs(path) {
		t.Fatal("absolute private lab driver required")
	}
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) > 65536 {
		t.Fatal("private driver unavailable or oversized")
	}
	var driver realRegistryDriver
	if json.Unmarshal(raw, &driver) != nil {
		t.Fatal("invalid private driver")
	}
	realRegistryLoopback(t, driver.RGN)
	issuer := rpc.NewRPCClient("private-RGN", driver.RGN, "5s", "", "")
	work, err := issuer.CreateOGGAuxBlock()
	if err != nil {
		t.Fatal(err)
	}
	state, err := issuer.ForgeRegistry(common.HexToHash(work.PreviousBlockHash))
	if err != nil {
		t.Fatal(err)
	}
	snapshot, ctx, _, activation, err := state.Decode()
	if err != nil || ctx.ChainID.Uint64() != 7008902 || ctx.ChainID.BitLen() > 64 || activation != 1 || work.ProofVersion != "0x5" {
		t.Fatal("refusing any network except owned private-CPU V5 lab", err)
	}
	poolBinary := os.Getenv("FORGE_PHASE6_POOL_BINARY")
	signerBinary := os.Getenv("FORGE_REAL_SIGNER_BINARY")
	if !filepath.IsAbs(poolBinary) || !filepath.IsAbs(signerBinary) || len(driver.SignerKeys) < 2 {
		t.Fatal("owned pool/signer binaries and test keys required")
	}
	redis := newPhase6Redis(t)
	dir := t.TempDir()
	stratum := processTestAddress(t)
	bridges := map[string]*httptest.Server{}
	bridgeBlocks := map[string]*int32{}
	destinations := []ForgeTreasuryDestination{}
	endpoints := map[string]ForgeChainEndpoint{}
	addresses := map[string]string{}
	for symbol, native := range driver.Native {
		if symbol != "DGB" && symbol != "DOGE" && symbol != "SOQ" && symbol != "PEP" {
			continue
		}
		blocked := new(int32)
		bridgeBlocks[symbol] = blocked
		bridge := realRegistryBridge(t, symbol, native, blocked)
		bridges[symbol] = bridge
		script := native.Script
		address := native.Address
		for _, coin := range snapshot.Coins {
			if coin.ChainID == native.ChainID {
				hash, e := forgepool.RegistryTreasury(coin, func() uint64 { h, _ := strconv.ParseUint(strings.TrimPrefix(work.Height, "0x"), 16, 64); return h }())
				if e != nil {
					t.Fatal(e)
				}
				for _, candidate := range []struct{ script, address string }{{native.Script, native.Address}, {native.NextScript, native.NextAddress}} {
					b, _ := hex.DecodeString(candidate.script)
					digest := sha256Sum(b)
					if hash == digest {
						script = candidate.script
						if candidate.address != "" {
							address = candidate.address
						}
					}
				}
			}
		}
		endpoints[symbol] = ForgeChainEndpoint{Name: symbol, Url: bridge.URL, Username: native.Username, Password: native.Password, PayoutScript: script}
		addresses[symbol] = address
		destinations = append(destinations, ForgeTreasuryDestination{Script: script, Address: address})
	}
	signers := []ForgeSigner{}
	var signerProcesses []*exec.Cmd
	var signerConfigs []string
	for i, keyPath := range driver.SignerKeys[:2] {
		if !filepath.IsAbs(keyPath) {
			t.Fatal("absolute test signing key required")
		}
		key, e := crypto.LoadECDSA(keyPath)
		if e != nil {
			t.Fatal(e)
		}
		address := crypto.PubkeyToAddress(key.PublicKey)
		allowed := false
		for _, a := range ctx.WorkSigners {
			allowed = allowed || a == address
		}
		if !allowed {
			t.Fatal("test signer does not match private chain")
		}
		listen := processTestAddress(t)
		multi := []map[string]interface{}{}
		for symbol, ep := range endpoints {
			native := driver.Native[symbol]
			multi = append(multi, map[string]interface{}{"symbol": symbol, "chainId": native.ChainID, "payoutAddress": addresses[symbol], "payoutScript": ep.PayoutScript, "url": ep.Url, "username": native.Username, "password": native.Password, "genesis": native.Genesis})
		}
		config := map[string]interface{}{"externalChainPolicy": "registry-v5", "registryBlock": 1, "registryChainId": 7008902, "registryRpc": map[string]interface{}{"url": driver.RGN, "username": "private-test", "password": "private-test", "genesis": strings.TrimPrefix(ctx.Genesis.Hex(), "0x")}, "multi": multi, "keyFile": keyPath, "listen": listen, "signerAddress": address.Hex()}
		encoded, _ := json.Marshal(config)
		configPath := filepath.Join(dir, fmt.Sprintf("signer-%d.json", i))
		os.WriteFile(configPath, encoded, 0600)
		logFile, e := os.Create(filepath.Join(dir, fmt.Sprintf("signer-%d.log", i)))
		if e != nil {
			t.Fatal(e)
		}
		cmd := exec.Command(signerBinary, configPath)
		cmd.Stdout = logFile
		cmd.Stderr = logFile
		if e = cmd.Start(); e != nil {
			t.Fatal(e)
		}
		t.Cleanup(func() {
			cmd.Process.Kill()
			cmd.Wait()
			logFile.Close()
			if t.Failed() {
				b, _ := os.ReadFile(logFile.Name())
				t.Log(string(b))
			}
		})
		signerProcesses = append(signerProcesses, cmd)
		signerConfigs = append(signerConfigs, configPath)
		signers = append(signers, ForgeSigner{Name: fmt.Sprintf("private-%d", i), URL: "http://" + listen, Address: address.Hex(), Timeout: "6s"})
	}
	cfg := Config{Name: "private-registry-runtime", Coin: "forge-phase6", Threads: 4, Redis: storage.Config{Endpoint: "127.0.0.1:" + redis.port, Password: redis.password}, Upstream: []Upstream{{Name: "private-RGN", Url: driver.RGN, Timeout: "5s"}}, UpstreamCheckInterval: "1h"}
	cfg.Proxy = Proxy{Enabled: true, Listen: processTestAddress(t), Difficulty: 1, HashrateExpiration: "1h", BlockRefreshInterval: "2s", StateUpdateInterval: "1h", LimitBodySize: 4096, LimitHeadersSize: 4096, Stratum: Stratum{Enabled: true, Listen: stratum, Timeout: "10m", MaxConn: 16}}
	cfg.Proxy.Policy = policy.Config{ResetInterval: "1h", RefreshInterval: "1h", Limits: policy.Limits{Grace: "1h"}, Banning: policy.Banning{CheckThreshold: 100, InvalidPercent: 30, MalformedLimit: 100}}
	cfg.Proxy.Forge = ForgeConfig{Enabled: true, MultiChain: true, RegistryBlock: 1, ExternalChainPolicy: "registry-v5", Digibyte: endpoints["DGB"], Dogecoin: endpoints["DOGE"], TreasuryDestinations: destinations, AccountingJournalDir: filepath.Join(dir, "journal"), JobBacklog: 64, TemplateValiditySec: 300, TemplateQuorum: 2, TemplateSigners: signers, DurableShareAcknowledgements: true}
	for _, symbol := range []string{"SOQ", "PEP"} {
		ep, ok := endpoints[symbol]
		if ok {
			native := driver.Native[symbol]
			cfg.Proxy.Forge.AdditionalAux = append(cfg.Proxy.Forge.AdditionalAux, ForgeAuxEndpoint{Symbol: symbol, ChainID: native.ChainID, Genesis: native.Genesis, PayoutAddress: addresses[symbol], Endpoint: ep})
		}
	}
	encoded, _ := json.Marshal(cfg)
	configPath := filepath.Join(dir, "pool.json")
	os.WriteFile(configPath, encoded, 0600)
	var cmd *exec.Cmd
	var logfile *os.File
	generation := 0
	stop := func() {
		if cmd != nil {
			cmd.Process.Kill()
			cmd.Wait()
			cmd = nil
		}
		if logfile != nil {
			logfile.Close()
			logfile = nil
		}
	}
	start := func() {
		generation++
		logfile, err = os.Create(filepath.Join(dir, fmt.Sprintf("pool-%d.log", generation)))
		if err != nil {
			t.Fatal(err)
		}
		cmd = exec.Command(poolBinary, configPath)
		cmd.Stdout = logfile
		cmd.Stderr = logfile
		if err = cmd.Start(); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		stop()
		if t.Failed() {
			files, _ := filepath.Glob(filepath.Join(dir, "pool-*.log"))
			for _, file := range files {
				b, _ := os.ReadFile(file)
				t.Log(string(b))
			}
		}
	})
	start()
	if os.Getenv("RGN_REAL_LAB_READINESS_ONLY") == "1" {
		miner := connectProcessMiner(t, stratum)
		miner.handshake("0x" + strings.Repeat("ab", 20) + ".ready")
		miner.conn.Close()
		t.Log("production pool and signer produced a real V5 Stratum job; mining not requested")
		return
	}
	user := "0x" + strings.Repeat("ab", 20) + ".real-cpu"
	var mined []string
	for round := 0; round < 2; round++ {
		miner := connectProcessMiner(t, stratum)
		miner.handshake(user)
		header, job, ntime := processNotifyHeader(t, miner)
		current, e := issuer.CreateOGGAuxBlock()
		if e != nil {
			t.Fatal(e)
		}
		target, ok := new(big.Int).SetString(strings.TrimPrefix(current.Target, "0x"), 16)
		if !ok {
			t.Fatal("private target invalid")
		}
		nonce := mineRealRegistryShare(t, header, target)
		before := current.Height
		result := miner.call("mining.submit", []string{user, job, "00000000", ntime, fmt.Sprintf("%08x", nonce)}, 0)
		if string(result) != "true" {
			t.Fatal("real miner share rejected", string(result))
		}
		miner.conn.Close()
		deadline := time.Now().Add(15 * time.Second)
		accepted := false
		for time.Now().Before(deadline) {
			next, e := issuer.CreateOGGAuxBlock()
			if e == nil && next.Height != before {
				accepted = true
				mined = append(mined, next.PreviousBlockHash)
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if !accepted {
			t.Fatal("real Stratum work failed to produce a canonical RGN block")
		}
		t.Logf("production pool+signer Stratum mined canonical private RGN block %d", round+1)
		if round == 0 {
			stop()
			start()
		}
	}
	count := int64(0)
	creditDeadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(creditDeadline) {
		count = redis.backend.Client().ZCard("forge-phase6:hashrate").Val()
		if count == 2 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if count != 2 {
		t.Fatal("miner share credit lost or duplicated across pool restart", count)
	}
	nativeCanonical := map[string][]string{}
	for symbol, native := range driver.Native {
		if _, ok := endpoints[symbol]; !ok {
			continue
		}
		keys, e := redis.backend.Client().Keys("forge-phase6:forge:submissions:" + strings.ToLower(symbol) + ":*").Result()
		if e != nil {
			t.Fatal(e)
		}
		seen := map[string]bool{}
		for _, key := range keys {
			rows, e := redis.backend.Client().HGetAllMap(key).Result()
			if e != nil {
				t.Fatal(e)
			}
			for _, row := range rows {
				var result storage.ForgeSubmission
				if json.Unmarshal([]byte(row), &result) != nil || !result.Accepted || seen[result.BlockHash] {
					continue
				}
				var header struct{ Confirmations int64 }
				realRegistryCall(t, native.URL, native.Username, native.Password, "getblockheader", []interface{}{strings.TrimPrefix(result.BlockHash, "0x"), true}, &header)
				if header.Confirmations < 1 {
					t.Fatal("production pool native acceptance is not canonical", symbol)
				}
				seen[result.BlockHash] = true
				nativeCanonical[symbol] = append(nativeCanonical[symbol], result.BlockHash)
			}
		}
	}
	if len(nativeCanonical["DOGE"]) == 0 || len(nativeCanonical["SOQ"]) == 0 {
		t.Fatal("real production pool did not record canonical DOGE and SOQ test blocks", nativeCanonical)
	}
	// Fault only the owned test relays. Real native nodes and protected services
	// continue running throughout these recovery checks.
	waitCore := func(mask string, offset int64) {
		t.Helper()
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			b, _ := os.ReadFile(logfile.Name())
			if int64(len(b)) > offset && strings.Contains(string(b[offset:]), "core="+mask+" ") {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatal("production runtime failed to publish expected core after test relay recovery", mask)
	}
	logOffset := func() int64 {
		info, e := os.Stat(logfile.Name())
		if e != nil {
			t.Fatal(e)
		}
		return info.Size()
	}
	if blocked := bridgeBlocks["SOQ"]; blocked != nil {
		offset := logOffset()
		atomic.StoreInt32(blocked, 1)
		waitCore("0x3", offset)
		offset = logOffset()
		atomic.StoreInt32(blocked, 0)
		waitCore("0x23", offset)
		t.Log("production pool excluded unavailable SOQ and recovered its native work")
	}
	monitor := connectProcessMiner(t, stratum)
	monitor.handshake(user)
	for _, blocked := range bridgeBlocks {
		atomic.StoreInt32(blocked, 1)
	}
	monitor.conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	disconnected := false
	for {
		var event json.RawMessage
		e := monitor.dec.Decode(&event)
		if e != nil {
			if e == io.EOF {
				disconnected = true
			}
			break
		}
	}
	monitor.conn.Close()
	if !disconnected {
		t.Fatal("all-native outage did not disconnect stale work")
	}
	offset := logOffset()
	for _, blocked := range bridgeBlocks {
		atomic.StoreInt32(blocked, 0)
	}
	waitCore("0x23", offset)
	restored := connectProcessMiner(t, stratum)
	restored.handshake(user)
	restored.conn.Close()
	t.Log("all-native outage paused work; native recovery restored real V5 Stratum jobs")
	// A deliberately short certificate makes the real quorum-expiry recovery
	// test bounded without changing any node's consensus rules or clock.
	stop()
	cfg.Proxy.Forge.TemplateValiditySec = 10
	encoded, _ = json.Marshal(cfg)
	os.WriteFile(configPath, encoded, 0600)
	start()
	short := connectProcessMiner(t, stratum)
	short.handshake(user)
	_, oldJob, oldTime := processNotifyHeader(t, short)
	signerProcesses[0].Process.Kill()
	signerProcesses[0].Wait()
	time.Sleep(12 * time.Second)
	short.call("mining.submit", []string{user, oldJob, "00000000", oldTime, "00000000"}, 21)
	short.conn.Close()
	restarted := exec.Command(signerBinary, signerConfigs[0])
	signerLog, e := os.Create(filepath.Join(dir, "signer-restarted.log"))
	if e != nil {
		t.Fatal(e)
	}
	restarted.Stdout = signerLog
	restarted.Stderr = signerLog
	if e = restarted.Start(); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { restarted.Process.Kill(); restarted.Wait(); signerLog.Close() })
	offset = logOffset()
	waitCore("0x23", offset)
	finalMiner := connectProcessMiner(t, stratum)
	finalMiner.handshake(user)
	finalMiner.conn.Close()
	t.Log("lost mining quorum expired old work; restarting the owned signer restored V5 jobs")
	report := map[string]interface{}{"passed": true, "scope": "private CPU RGN and real native regtests", "chainId": 7008902, "canonicalRGNBlocks": mined, "canonicalNativeBlocks": nativeCanonical, "poolProcessGenerations": generation, "realRecoveryChecks": []string{"SOQ relay outage and recovery", "all-native relay outage pauses work and restores jobs", "mining signer quorum loss, certificate expiry and recovery"}, "creditedShares": count, "nativeGenesisPeersTemplatesProposalsAndSubmission": "real", "identityBridgeOverrides": []string{"getblockchaininfo.chain label", "DOGE historical checkpoint104679"}, "mainnetChanged": false}
	if out := os.Getenv("RGN_REAL_LAB_REPORT"); out != "" {
		blob, _ := json.MarshalIndent(report, "", "  ")
		if err = os.WriteFile(out, append(blob, '\n'), 0644); err != nil {
			t.Fatal(err)
		}
	}
}
func sha256Sum(b []byte) common.Hash { return common.Hash(sha256.Sum256(b)) }
