package proxy

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/HawyUkr/open-progpow-pool/forgepool"
	"github.com/HawyUkr/open-progpow-pool/forgesigner"
	"github.com/HawyUkr/open-progpow-pool/policy"
	"github.com/HawyUkr/open-progpow-pool/rpc"
	"github.com/HawyUkr/open-progpow-pool/storage"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// This test is launched only by the owning six-node harness. It never lowers
// OGG's target, supplies fabricated daemon replies, or claims an OGG block.
func TestForgeIntegratedRealChains(t *testing.T) {
	report := os.Getenv("FORGE_INTEGRATED_REPORT")
	if report == "" {
		t.Skip("run scripts/test-forge-integrated.py")
	}
	if !filepath.IsAbs(report) {
		t.Fatal("absolute owned report required")
	}
	endpoint := func(name string) string {
		t.Helper()
		u, err := url.Parse(os.Getenv(name))
		if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.Port() == "" || u.User != nil {
			t.Fatal("owned loopback endpoint required", name)
		}
		return u.String()
	}
	oggURL := endpoint("FORGE_INTEGRATED_OGG_RPC")
	dgbURL, dogeURL := endpoint("FORGE_DGB_TEST_RPC"), endpoint("FORGE_DOGE_TEST_RPC")
	dgbSignerURL, dogeSignerURL := endpoint("FORGE_DGB_SIGNER_RPC"), endpoint("FORGE_DOGE_SIGNER_RPC")
	if dgbURL == dgbSignerURL || dogeURL == dogeSignerURL {
		t.Fatal("distinct signer full nodes required")
	}
	client := func(name, address string) *rpc.RPCClient {
		return rpc.NewRPCClient(name, address, "3s", "forge-regtest", "disposable-test-only")
	}

	// Observe actual OGG work without synthesizing or changing any RPC result.
	var mu sync.Mutex
	var issued rpc.OGGAuxBlock
	targetURL, _ := url.Parse(oggURL)
	forward := httputil.NewSingleHostReverseProxy(targetURL)
	forward.ModifyResponse = func(res *http.Response) error {
		body, err := ioutil.ReadAll(res.Body)
		res.Body.Close()
		if err != nil {
			return err
		}
		res.Body = ioutil.NopCloser(bytes.NewReader(body))
		var out struct{ Result rpc.OGGAuxBlock }
		if json.Unmarshal(body, &out) == nil && out.Result.WorkHash != "" {
			mu.Lock()
			issued = out.Result
			mu.Unlock()
		}
		return nil
	}
	oggProxy := httptest.NewServer(forward)
	defer oggProxy.Close()

	// Public scalar 1 exists only in the test driver's external HTTP signer.
	// Pool configuration contains its public address, never a private key.
	key, err := crypto.HexToECDSA(strings.Repeat("0", 63) + "1")
	if err != nil {
		t.Fatal(err)
	}
	backend := independentDevKey{key}
	service, err := forgesigner.New(forgesigner.RPCSource{
		Digibyte: client("trusted-dgb", dgbSignerURL), Dogecoin: client("trusted-doge", dogeSignerURL),
	}, backend, forgesigner.Policy{DogePayoutScript: []byte{0x52}, MaxValidity: 300, MaxAge: 300}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var proposal forgepool.TemplateSignRequest
	var signature []byte
	signer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p forgepool.TemplateSignRequest
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, forgepool.MaxSignRequestBytes)).Decode(&p) != nil {
			http.Error(w, "bad proposal", 400)
			return
		}
		out, err := service.Sign(r.Context(), p)
		if err != nil {
			t.Log("independent signer rejected:", err)
			http.Error(w, "proposal rejected", 422)
			return
		}
		sig, err := hex.DecodeString(strings.TrimPrefix(out.Signature, "0x"))
		if err != nil {
			t.Error(err)
			return
		}
		mu.Lock()
		proposal, signature = p, sig
		mu.Unlock()
		json.NewEncoder(w).Encode(out)
	}))
	defer signer.Close()
	r := newPhase6Redis(t)
	dir := t.TempDir()
	stratum := processTestAddress(t)
	cfg := Config{Name: "phase6d1", Coin: "forge-phase6", Threads: 4,
		Redis:    storage.Config{Endpoint: "127.0.0.1:" + r.port, Password: r.password},
		Upstream: []Upstream{{Name: "real-ogg", Url: oggProxy.URL, Timeout: "3s"}}, UpstreamCheckInterval: "1h"}
	// Keep one signed job during this bounded gate. Continuous mining must use
	// regular refresh and handle tip changes; that is a separate endurance gate.
	cfg.Proxy = Proxy{Enabled: true, Listen: processTestAddress(t), Difficulty: 1, HashrateExpiration: "1h", BlockRefreshInterval: "1h", StateUpdateInterval: "1h", LimitBodySize: 4096, LimitHeadersSize: 4096, Stratum: Stratum{Enabled: true, Listen: stratum, Timeout: "10m", MaxConn: 16}}
	cfg.Proxy.Policy = policy.Config{ResetInterval: "1h", RefreshInterval: "1h", Limits: policy.Limits{Grace: "1h"}, Banning: policy.Banning{CheckThreshold: 100, InvalidPercent: 30, MalformedLimit: 100}}
	chain := func(name, address, script string) ForgeChainEndpoint {
		return ForgeChainEndpoint{Name: name, Url: address, Timeout: "3s", Username: "forge-regtest", Password: "disposable-test-only", PayoutScript: script}
	}
	cfg.Proxy.Forge = ForgeConfig{Enabled: true, AccountingJournalDir: filepath.Join(dir, "journal"), TemplateValiditySec: 300, TemplateQuorum: 1,
		TemplateSigners: []ForgeSigner{{Name: "DEV-independent", URL: signer.URL, Address: backend.Address().Hex(), Timeout: "5s"}},
		Digibyte:        chain("real-dgb", dgbURL, "51"), Dogecoin: chain("real-doge", dogeURL, "52")}
	blob, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, "pool.json")
	if err := os.WriteFile(configPath, blob, 0600); err != nil {
		t.Fatal(err)
	}
	binaryPath := os.Getenv("FORGE_PHASE6_POOL_BINARY")
	if !filepath.IsAbs(binaryPath) {
		t.Fatal("absolute compiled pool required")
	}
	logPath := filepath.Join(report, "pool.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binaryPath, configPath)
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		logFile.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cmd.Process.Kill()
		cmd.Wait()
		logFile.Close()
		b, _ := os.ReadFile(logPath)
		if bytes.Contains(b, []byte("DATA RACE")) {
			t.Error("pool process race", string(b))
		}
		if t.Failed() {
			t.Log(string(b))
		}
	})
	miner := connectProcessMiner(t, stratum)
	login := "0x" + strings.Repeat("ab", 20)
	user := login + ".rig"
	miner.handshake(user)
	header, jobID, ntime := processNotifyHeader(t, miner)
	mu.Lock()
	work, request, sig := issued, proposal, append([]byte{}, signature...)
	mu.Unlock()
	if work.Height != "0x1" || work.Target != "0x00000000ffff0000000000000000000000000000000000000000000000000000" || request.Doge == nil || request.Doge.Candidate == nil || len(sig) != 65 {
		t.Fatal("missing real work or independently signed proposal")
	}
	if err := forgepool.ValidateTemplateSignatures(request.Template, [][]byte{sig}, []common.Address{backend.Address()}, 1, time.Now()); err != nil {
		t.Fatal(err)
	}
	nonce := mineProcessShares(t, header, 1)[0] // Explicitly excludes OGG target hits.
	binary.LittleEndian.PutUint32(header[76:], nonce)
	hash, err := forgepool.ScryptHash(header)
	if err != nil {
		t.Fatal(err)
	}
	for _, bits := range []uint32{binary.LittleEndian.Uint32(header[72:76]), request.Doge.Candidate.Bits} {
		target, err := forgepool.CompactTarget(bits)
		if err != nil || forgepool.HashValue(hash).Cmp(target) > 0 {
			t.Fatal("mined share did not meet an external target")
		}
	}

	// Assemble the exact proof from the real Stratum coinbase and independently
	// signed DOGE candidate. Submit directly to assert OGG's target boundary;
	// ordinary shares must never reach native block routing inside the pool.
	str := func(i int) string {
		var s string
		if json.Unmarshal(miner.notify[i], &s) != nil {
			t.Fatal("notify string")
		}
		return s
	}
	coinbase, err := hex.DecodeString(str(2) + miner.extra1 + strings.Repeat("00", miner.extra2size) + str(3))
	if err != nil {
		t.Fatal(err)
	}
	dogeCandidate, err := forgepool.BuildDogeCandidate(*request.Doge.Candidate, []byte{0x52}, request.Doge.JobNonce)
	if err != nil {
		t.Fatal(err)
	}
	proof := &forgepool.OGGAuxPowProof{ParentHeader: header, ParentCoinbase: coinbase,
		DogeHeader: dogeCandidate.Header, DogeCoinbase: dogeCandidate.Coinbase,
		Template: request.Template, Signatures: [][]byte{sig}}
	var branches []string
	if err := json.Unmarshal(miner.notify[4], &branches); err != nil {
		t.Fatal(err)
	}
	for _, s := range branches {
		b, err := hex.DecodeString(s)
		if err != nil || len(b) != 32 {
			t.Fatal("invalid branch")
		}
		proof.ParentMerkleBranch = append(proof.ParentMerkleBranch, common.BytesToHash(b))
	}
	for _, b := range dogeCandidate.MerkleBranch {
		proof.DogeMerkleBranch = append(proof.DogeMerkleBranch, common.BytesToHash(b[:]))
	}
	encoded, err := forgepool.EncodeOGGProof(proof)
	if err != nil {
		t.Fatal(err)
	}
	ogg := rpc.NewRPCClient("real-ogg", oggURL, "3s", "", "")
	if ok, rejected, err := ogg.SubmitForgeOGG(work.WorkHash, hex.EncodeToString(encoded)); ok || !rejected || err == nil || err.Error() != "OGG rejected proof" {
		t.Fatal("non-winning OGG proof was not conclusively rejected", ok, rejected, err)
	}
	params := []string{user, jobID, "00000000", ntime, fmt.Sprintf("%08x", nonce)}
	if result := miner.call("mining.submit", params, 0); string(result) != "true" {
		t.Fatal("share rejected")
	}
	miner.call("mining.submit", params, 22)
	if n, err := r.backend.Client().HGet("forge-phase6:shares:roundCurrent", login).Int64(); err != nil || n != 1 {
		t.Fatal("share credits", n, err)
	}
	if n, err := r.backend.Client().ZCard("forge-phase6:hashrate").Result(); err != nil || n != 1 {
		t.Fatal("hashrate entries", n, err)
	}
	outcomes := make(map[string]storage.ForgeSubmission)
	var receiptID string
	for _, name := range []string{"dgb", "doge"} {
		rows, err := r.backend.Client().HGetAll("forge-phase6:forge:submissions:" + name + ":1").Result()
		if err != nil || len(rows) != 2 {
			t.Fatal("external outcome count", name, rows, err)
		}
		for i := 0; i < len(rows); i += 2 {
			id, raw := rows[i], rows[i+1]
			if receiptID != "" && receiptID != id {
				t.Fatal("external wins used different share receipts")
			}
			receiptID = id
			var out storage.ForgeSubmission
			if json.Unmarshal([]byte(raw), &out) != nil || !out.Attempted || !out.Accepted || out.BlockHash == "" {
				t.Fatal("external win not acknowledged", name, raw)
			}
			outcomes[name] = out
		}
	}
	if blocks, err := r.backend.GetCandidates(1); err != nil || len(blocks) != 0 {
		t.Fatal("external wins created native reward round", err)
	}
	files, err := filepath.Glob(filepath.Join(dir, "journal", "*.json"))
	if err != nil || len(files) != 0 {
		t.Fatal("pending receipts", files, err)
	}
	evidence := map[string]interface{}{"shareCredits": 1, "hashrateEntries": 1, "receiptID": receiptID,
		"external": outcomes, "nativeCandidates": 0, "issuedWork": work, "scryptHash": hex.EncodeToString(hash[:]),
		"proofHex": hex.EncodeToString(encoded), "proofSubmissionAccepted": false}
	b, err := json.MarshalIndent(evidence, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(report, "accounting.json"), append(b, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	t.Log("PASS: real six-node stack, full pool executable, TCP Scrypt, independent signer, DGB+DOGE accepted, one receipt/credit, zero native reward rounds")
}
