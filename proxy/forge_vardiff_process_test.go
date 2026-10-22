package proxy

import (
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/HawyUkr/open-progpow-pool/forgepool"
	"github.com/HawyUkr/open-progpow-pool/policy"
	"github.com/HawyUkr/open-progpow-pool/rpc"
	"github.com/HawyUkr/open-progpow-pool/storage"
)

// Real compiled pool + private Redis + TCP miners. Native RPCs/signature issuer
// are explicit fixtures; this test does not claim an external network reward.
func TestForgeVarDiffCompiledPool(t *testing.T) {
	binaryPath := os.Getenv("FORGE_VARDIFF_POOL_BINARY")
	if binaryPath == "" {
		t.Skip("requires compiled variable-difficulty pool")
	}
	r := newPhase6Redis(t)
	now := time.Now().Unix()
	gbt := rpc.BlockTemplateReply{PowAlgo: "scrypt", PowAlgoID: func() *int { n := 1; return &n }(), Version: 2, Height: 2000, CurTime: now, PreviousBlockHash: strings.Repeat("22", 32), Bits: "1b00ffff", CoinbaseValue: 5000000000}
	target, _ := forgepool.TargetForDifficulty(big.NewInt(28))
	work := rpc.OGGAuxBlock{WorkHash: "0x" + strings.Repeat("11", 32), Height: "0xbb8", Difficulty: "0x1c", Target: fmt.Sprintf("0x%064x", target), Timestamp: fmt.Sprintf("0x%x", now), TemplateMaxValidity: "0x12c", PreviousBlockHash: "0x" + strings.Repeat("33", 32)}
	var submissions int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		var call struct{ Method string }
		json.NewDecoder(request.Body).Decode(&call)
		var result interface{}
		switch call.Method {
		case "eth_createAuxBlock":
			result = work
		case "eth_getBlockByNumber":
			result = map[string]string{"number": "0xbb7", "hash": work.PreviousBlockHash}
		default:
			atomic.AddInt32(&submissions, 1)
			w.WriteHeader(500)
			return
		}
		json.NewEncoder(w).Encode(map[string]interface{}{"id": 0, "result": result, "error": nil})
	}))
	defer upstream.Close()
	dir := t.TempDir()
	address := processTestAddress(t)
	cfg := Config{Name: "vardiff-process", Coin: "forge-test", Threads: 2, Redis: storage.Config{Endpoint: "127.0.0.1:" + r.port, Password: r.password}, Upstream: []Upstream{{Name: "fixture", Url: upstream.URL, Timeout: "2s"}}, UpstreamCheckInterval: "1h"}
	cfg.Proxy = Proxy{Enabled: true, Listen: processTestAddress(t), Difficulty: 50000, HashrateExpiration: "1h", BlockRefreshInterval: "1s", StateUpdateInterval: "1h", LimitBodySize: 4096, LimitHeadersSize: 4096, Stratum: Stratum{Enabled: true, Listen: address, Timeout: "10m", MaxConn: 16}}
	cfg.Proxy.Policy = policy.Config{ResetInterval: "1h", RefreshInterval: "1h", Limits: policy.Limits{Grace: "1h"}, Banning: policy.Banning{CheckThreshold: 100, InvalidPercent: 30, MalformedLimit: 100}}
	cfg.Proxy.Forge = ForgeConfig{Enabled: true, AccountingJournalDir: filepath.Join(dir, "journal"), TemplateValiditySec: 300, TemplateQuorum: 1, VariableDifficulty: ForgeVarDiffConfig{true, 50000, 2000000, 2, 30}}
	rt := &forgeRuntime{}
	multiProcessConfiguration(t, &cfg, rt, gbt)
	blob, _ := json.Marshal(cfg)
	path := filepath.Join(dir, "pool.json")
	os.WriteFile(path, blob, 0600)
	logPath := filepath.Join(dir, "pool.log")
	logFile, e := os.Create(logPath)
	if e != nil {
		t.Fatal(e)
	}
	child := exec.Command(binaryPath, path)
	child.Stdout = logFile
	child.Stderr = logFile
	if e = child.Start(); e != nil {
		t.Fatal(e)
	}
	defer func() {
		child.Process.Kill()
		child.Wait()
		logFile.Close()
		if t.Failed() {
			data, _ := os.ReadFile(logPath)
			t.Log(string(data))
		}
	}()
	for _, test := range []struct {
		password string
		want     int64
	}{{"x", 50000}, {"x,d=844800", 844800}} {
		miner := connectProcessMiner(t, address)
		miner.call("mining.subscribe", []string{"vardiff-process-miner"}, 0)
		miner.call("mining.authorize", []string{"0x" + strings.Repeat("ab", 20) + ".mrr", test.password}, 0)
		deadline := time.Now().Add(10 * time.Second)
		for len(miner.notify) != 9 {
			miner.call("mining.extranonce.subscribe", []interface{}{}, 0)
			if time.Now().After(deadline) {
				t.Fatal("no signed V3 work")
			}
			time.Sleep(20 * time.Millisecond)
		}
		if miner.difficulty != test.want {
			t.Fatal("wrong per-worker difficulty", miner.difficulty, test.want)
		}
		miner.conn.Close()
	}
	if atomic.LoadInt32(&submissions) != 0 {
		t.Fatal("probe submitted chain work")
	}
}
