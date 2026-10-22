package proxy

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/HawyUkr/open-progpow-pool/forgepool"
	"github.com/HawyUkr/open-progpow-pool/policy"
	"github.com/HawyUkr/open-progpow-pool/rpc"
	"github.com/HawyUkr/open-progpow-pool/storage"
)

func processTestAddress(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	return addr
}

// RESP framing for a test-only transparent fault proxy. Bounds apply to each
// frame; the pool only sends normal RESP2 commands through this private socket.
func processReadRESP(r *bufio.Reader, depth int) ([]byte, error) {
	if depth > 8 {
		return nil, fmt.Errorf("RESP nesting")
	}
	line, err := r.ReadBytes('\n')
	if err != nil {
		return nil, err
	}
	if len(line) < 3 || len(line) > 16*1024*1024 {
		return nil, fmt.Errorf("RESP line size")
	}
	switch line[0] {
	case '+', '-', ':':
		return line, nil
	case '$', '*':
		n, err := strconv.Atoi(strings.TrimSpace(string(line[1:])))
		if err != nil || n < -1 || n > 16*1024*1024 {
			return nil, fmt.Errorf("RESP size")
		}
		if n == -1 {
			return line, nil
		}
		if line[0] == '$' {
			body := make([]byte, n+2)
			_, err = io.ReadFull(r, body)
			return append(line, body...), err
		}
		for i := 0; i < n; i++ {
			body, err := processReadRESP(r, depth+1)
			if err != nil {
				return nil, err
			}
			line = append(line, body...)
			if len(line) > 16*1024*1024 {
				return nil, fmt.Errorf("RESP frame too large")
			}
		}
		return line, nil
	}
	return nil, fmt.Errorf("unknown RESP type")
}

type processRedisFault struct {
	mode        int32 // 1: withhold before commit; 2: withhold committed reply.
	reached     chan int32
	release     chan struct{}
	listener    net.Listener
	stop        chan struct{}
	mu          sync.Mutex
	connections []net.Conn
	wg          sync.WaitGroup
}

func newProcessRedisFault(t *testing.T, backend string) *processRedisFault {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &processRedisFault{listener: l, stop: make(chan struct{}), reached: make(chan int32, 1), release: make(chan struct{})}
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			p.mu.Lock()
			p.connections = append(p.connections, c)
			p.mu.Unlock()
			p.wg.Add(1)
			go func() {
				defer p.wg.Done()
				defer c.Close()
				remote, err := net.DialTimeout("tcp", backend, time.Second)
				if err != nil {
					return
				}
				defer remote.Close()
				p.mu.Lock()
				p.connections = append(p.connections, remote)
				p.mu.Unlock()
				in, out := bufio.NewReader(c), bufio.NewReader(remote)
				for {
					req, err := processReadRESP(in, 0)
					if err != nil {
						return
					}
					m := int32(0)
					if bytes.Contains(req, []byte("FORGE wrong key type")) {
						v := atomic.LoadInt32(&p.mode)
						if v != 0 && atomic.CompareAndSwapInt32(&p.mode, v, 0) {
							m = v
						}
					}
					if m == 1 {
						p.reached <- m
						select {
						case <-p.release:
						case <-p.stop:
						}
						return
					}
					if _, err = remote.Write(req); err != nil {
						return
					}
					reply, err := processReadRESP(out, 0)
					if err != nil {
						return
					}
					if m == 2 {
						if string(reply) != ":0\r\n" {
							p.reached <- -1
						} else {
							p.reached <- m
						}
						select {
						case <-p.release:
						case <-p.stop:
						}
						return
					}
					if _, err = c.Write(reply); err != nil {
						return
					}
				}
			}()
		}
	}()
	t.Cleanup(func() {
		close(p.stop)
		l.Close()
		p.mu.Lock()
		for _, c := range p.connections {
			c.Close()
		}
		p.mu.Unlock()
		p.wg.Wait()
	})
	return p
}

type processMiner struct {
	t          *testing.T
	conn       net.Conn
	enc        *json.Encoder
	dec        *json.Decoder
	id         int
	notify     []json.RawMessage
	extra1     string
	extra2size int
	difficulty int64
}

func connectProcessMiner(t *testing.T, address string) *processMiner {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		c, err := net.DialTimeout("tcp", address, 200*time.Millisecond)
		if err == nil {
			m := &processMiner{t: t, conn: c, enc: json.NewEncoder(c), dec: json.NewDecoder(c)}
			t.Cleanup(func() { c.Close() })
			return m
		}
		if time.Now().After(deadline) {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (m *processMiner) send(method string, params interface{}) int {
	m.t.Helper()
	m.id++
	m.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if err := m.enc.Encode(map[string]interface{}{"id": m.id, "method": method, "params": params}); err != nil {
		m.t.Fatal(err)
	}
	return m.id
}

func (m *processMiner) response(id int) (json.RawMessage, *ErrorReply) {
	m.t.Helper()
	m.conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	for {
		var v struct {
			ID     *int `json:"id"`
			Method string
			Params []json.RawMessage
			Result json.RawMessage
			Error  json.RawMessage
		}
		if err := m.dec.Decode(&v); err != nil {
			m.t.Fatal(err)
		}
		if v.Method == "mining.notify" {
			m.notify = v.Params
		}
		if v.Method == "mining.set_difficulty" && len(v.Params) == 1 {
			json.Unmarshal(v.Params[0], &m.difficulty)
		}
		if v.ID != nil && *v.ID == id {
			if string(v.Error) == "null" {
				return v.Result, nil
			}
			var tuple []json.RawMessage
			var reply ErrorReply
			if json.Unmarshal(v.Error, &tuple) != nil || len(tuple) != 3 || json.Unmarshal(tuple[0], &reply.Code) != nil || json.Unmarshal(tuple[1], &reply.Message) != nil || string(tuple[2]) != "null" {
				m.t.Fatalf("nonstandard Stratum error: %s", v.Error)
			}
			return v.Result, &reply
		}
	}
}

func (m *processMiner) call(method string, params interface{}, code int) json.RawMessage {
	m.t.Helper()
	r, e := m.response(m.send(method, params))
	if code == 0 {
		if e != nil {
			m.t.Fatalf("%s: %+v", method, e)
		}
	} else if e == nil || e.Code != code {
		m.t.Fatalf("%s error=%+v want=%d", method, e, code)
	}
	return r
}

func (m *processMiner) handshake(user string) {
	m.t.Helper()
	m.call("mining.configure", []interface{}{[]string{}, map[string]interface{}{}}, 0)
	s := m.call("mining.subscribe", []string{"phase6c-tcp-miner"}, 0)
	var row []json.RawMessage
	if json.Unmarshal(s, &row) != nil || len(row) != 3 || json.Unmarshal(row[1], &m.extra1) != nil || json.Unmarshal(row[2], &m.extra2size) != nil {
		m.t.Fatal("bad subscribe")
	}
	m.call("mining.authorize", []string{user, "test-only"}, 0)
	// A roundtrip after authorization drains preceding difficulty/job pushes.
	deadline := time.Now().Add(10 * time.Second)
	for len(m.notify) != 9 {
		m.call("mining.extranonce.subscribe", []interface{}{}, 0)
		if time.Now().After(deadline) {
			m.t.Fatal("no job")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if m.difficulty != 1 || m.extra2size != 4 {
		m.t.Fatalf("unexpected difficulty/extranonce: %d/%d", m.difficulty, m.extra2size)
	}
}

func processNotifyHeader(t *testing.T, m *processMiner) ([]byte, string, string) {
	t.Helper()
	if len(m.notify) != 9 {
		t.Fatal("bad notify")
	}
	str := func(i int) string {
		var s string
		if json.Unmarshal(m.notify[i], &s) != nil {
			t.Fatal("notify string")
		}
		return s
	}
	decode := func(s string) []byte {
		b, err := hex.DecodeString(s)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	number := func(s string) uint32 {
		n, err := strconv.ParseUint(s, 16, 32)
		if err != nil {
			t.Fatal(err)
		}
		return uint32(n)
	}
	cb := decode(str(2) + m.extra1 + strings.Repeat("00", m.extra2size) + str(3))
	a := sha256.Sum256(cb)
	root := sha256.Sum256(a[:])
	var branches []string
	if json.Unmarshal(m.notify[4], &branches) != nil {
		t.Fatal("bad branches")
	}
	for _, b := range branches {
		a = sha256.Sum256(append(root[:], decode(b)...))
		root = sha256.Sum256(a[:])
	}
	header := make([]byte, 80)
	binary.LittleEndian.PutUint32(header, number(str(5)))
	prev := decode(str(1))
	if len(prev) != 32 {
		t.Fatal("prevhash length")
	}
	for i := 0; i < 32; i += 4 {
		header[4+i] = prev[i+3]
		header[5+i] = prev[i+2]
		header[6+i] = prev[i+1]
		header[7+i] = prev[i]
	}
	copy(header[36:68], root[:])
	binary.LittleEndian.PutUint32(header[68:72], number(str(7)))
	binary.LittleEndian.PutUint32(header[72:76], number(str(6)))
	return header, str(0), str(7)
}

func mineProcessShares(t *testing.T, header []byte, count int) []uint32 {
	t.Helper()
	target, _ := new(big.Int).SetString("0000ffff00000000000000000000000000000000000000000000000000000000", 16)
	network, _ := new(big.Int).SetString("00000000ffff0000000000000000000000000000000000000000000000000000", 16)
	stop := make(chan struct{})
	found := make(chan uint32, 4)
	var wg sync.WaitGroup
	for worker := uint32(0); worker < 4; worker++ {
		wg.Add(1)
		go func(first uint32) {
			defer wg.Done()
			h := append([]byte{}, header...)
			for n := first; ; n += 4 {
				select {
				case <-stop:
					return
				default:
				}
				binary.LittleEndian.PutUint32(h[76:], n)
				hash, _ := forgepool.ScryptHash(h)
				v := forgepool.HashValue(hash)
				if v.Cmp(target) <= 0 && v.Cmp(network) > 0 {
					select {
					case found <- n:
					case <-stop:
						return
					}
				}
			}
		}(worker)
	}
	defer func() { close(stop); wg.Wait() }()
	timer := time.NewTimer(240 * time.Second)
	defer timer.Stop()
	tick := time.NewTicker(20 * time.Second)
	defer tick.Stop()
	var result []uint32
	for len(result) < count {
		select {
		case n := <-found:
			result = append(result, n)
			t.Logf("mined real Scrypt Stratum share %d/%d", len(result), count)
		case <-tick.C:
			t.Log("CPU mining unchanged Stratum difficulty 1")
		case <-timer.C:
			t.Fatal("CPU mining budget exhausted")
		}
	}
	return result
}

func TestForgePoolProcessStratumCrashRecovery(t *testing.T) {
	testForgePoolProcess(t, false)
}

func TestForgePoolTwoChainProcessStartup(t *testing.T) {
	testForgePoolProcess(t, true)
}

func TestForgePoolV4TwoChainProcessStartup(t *testing.T) {
	testForgePoolProcess(t, true, "0x4")
}

func TestForgePoolV4ProcessStratumCrashRecovery(t *testing.T) {
	testForgePoolProcess(t, false, "0x4")
}

func testForgePoolProcess(t *testing.T, startupOnly bool, proofVersion ...string) {
	t.Helper()
	binaryPath := os.Getenv("FORGE_PHASE6_POOL_BINARY")
	if binaryPath == "" || os.Getenv("FORGE_PHASE6_REDIS_SERVER") == "" {
		t.Skip("requires isolated compiled-pool harness")
	}
	if !filepath.IsAbs(binaryPath) {
		t.Fatal("absolute pool binary required")
	}
	r := newPhase6Redis(t)
	fault := newProcessRedisFault(t, "127.0.0.1:"+r.port)
	now := time.Now().Unix()
	var submissions int32
	gbt := rpc.BlockTemplateReply{PowAlgo: "scrypt", PowAlgoID: func() *int { n := 1; return &n }(), Version: 2, Height: 2000, CurTime: now, PreviousBlockHash: strings.Repeat("22", 32), Bits: "1d00ffff", CoinbaseValue: 5000000000}
	work := rpc.OGGAuxBlock{WorkHash: "0x" + strings.Repeat("11", 32), Height: "0xbb8", Difficulty: "0x1", Target: "0x00000000ffff0000000000000000000000000000000000000000000000000000", Timestamp: fmt.Sprintf("0x%x", now), TemplateMaxValidity: "0x12c", PreviousBlockHash: "0x" + strings.Repeat("33", 32)}
	v4 := len(proofVersion) == 1 && proofVersion[0] == "0x4"
	if len(proofVersion) != 0 {
		work.ProofVersion = proofVersion[0]
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var call struct{ Method string }
		if json.NewDecoder(req.Body).Decode(&call) != nil {
			w.WriteHeader(400)
			return
		}
		var result interface{}
		switch call.Method {
		case "eth_createAuxBlock":
			result = work
		case "getblocktemplate":
			result = gbt
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
	rt := &forgeRuntime{}
	signer, _ := templateTestSigner(t, rt, nil)
	dir := t.TempDir()
	journal := filepath.Join(dir, "journal")
	stratumAddr := processTestAddress(t)
	cfg := Config{Name: "phase6c", Coin: "forge-phase6", Threads: 4, Redis: storage.Config{Endpoint: fault.listener.Addr().String(), Password: r.password}, Upstream: []Upstream{{Name: "fixture-ogg", Url: upstream.URL, Timeout: "2s"}}, UpstreamCheckInterval: "1h"}
	cfg.Proxy = Proxy{Enabled: true, Listen: processTestAddress(t), Difficulty: 1, HashrateExpiration: "1h", BlockRefreshInterval: "1h", StateUpdateInterval: "1h", LimitBodySize: 4096, LimitHeadersSize: 4096, Stratum: Stratum{Enabled: true, Listen: stratumAddr, Timeout: "10m", MaxConn: 16}}
	cfg.Proxy.Policy = policy.Config{ResetInterval: "1h", RefreshInterval: "1h", Limits: policy.Limits{Grace: "1h"}, Banning: policy.Banning{CheckThreshold: 100, InvalidPercent: 30, MalformedLimit: 100}}
	cfg.Proxy.Forge = ForgeConfig{Enabled: true, AccountingJournalDir: journal, TemplateValiditySec: 300, TemplateQuorum: 1, TemplateSigners: []ForgeSigner{{Name: "DEV-external", URL: signer.URL, Address: rt.authorized[0].Hex(), Timeout: "2s"}}, Digibyte: ForgeChainEndpoint{Url: upstream.URL, PayoutScript: "51"}, Dogecoin: ForgeChainEndpoint{Url: upstream.URL, PayoutScript: "52"}}
	cfg.Proxy.Forge.RetainExternalWork = os.Getenv("FORGE_TEST_RETAIN_EXTERNAL_WORK") == "1"
	if startupOnly || v4 {
		cfg.Proxy.Forge.ExternalChainPolicy = forgepool.ExternalChainPolicyDGBDOGE
	}
	var signedCertificate atomic.Value
	if startupOnly || v4 || os.Getenv("FORGE_TEST_MULTI_PROTOCOL") == "1" {
		multiProcessConfiguration(t, &cfg, rt, gbt, func(req forgepool.MultiSignRequest, signature []byte) {
			if req.Template.Version == 4 {
				digest, err := forgepool.MultiCertificateDigest(req.Template, req.WorkHash, [][]byte{signature})
				if err != nil {
					t.Error(err)
					return
				}
				signedCertificate.Store(digest.Bytes())
			}
		})
	}
	blob, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "pool.json")
	if err = os.WriteFile(path, blob, 0600); err != nil {
		t.Fatal(err)
	}
	var cmd *exec.Cmd
	var logFile *os.File
	generation := 0
	stop := func() {
		if cmd != nil {
			cmd.Process.Kill()
			cmd.Wait()
			cmd = nil
		}
		if logFile != nil {
			logFile.Close()
			logFile = nil
		}
	}
	t.Cleanup(stop)
	start := func() {
		generation++
		logPath := filepath.Join(dir, fmt.Sprintf("pool-%d.log", generation))
		var err error
		logFile, err = os.Create(logPath)
		if err != nil {
			t.Fatal(err)
		}
		cmd = exec.Command(binaryPath, path)
		cmd.Stdout = logFile
		cmd.Stderr = logFile
		if err = cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Logf("started full pool executable generation %d PID %d", generation, cmd.Process.Pid)
	}
	t.Cleanup(func() {
		stop()
		files, _ := filepath.Glob(filepath.Join(dir, "pool-*.log"))
		for _, file := range files {
			b, _ := os.ReadFile(file)
			if bytes.Contains(b, []byte("DATA RACE")) {
				t.Error("pool process race detected", string(b))
			}
			version := 3
			if v4 {
				version = 4
			}
			if (startupOnly || v4 || os.Getenv("FORGE_TEST_MULTI_PROTOCOL") == "1") && !bytes.Contains(b, []byte(fmt.Sprintf("new V%d Scrypt job", version))) {
				t.Error("compiled pool did not publish the requested proof version", string(b))
			}
			if t.Failed() {
				t.Log(string(b))
			}
		}
	})
	start()
	miner := connectProcessMiner(t, stratumAddr)
	user := "0x" + strings.Repeat("ab", 20) + ".rig"
	miner.call("mining.submit", []string{user, "none", "00000000", "00000000", "00000000"}, 25)
	miner.handshake(user)
	header, job, ntime := processNotifyHeader(t, miner)
	if v4 {
		var first, second string
		if json.Unmarshal(miner.notify[2], &first) != nil || json.Unmarshal(miner.notify[3], &second) != nil {
			t.Fatal("invalid V4 coinbase fragments")
		}
		coinb1, _ := hex.DecodeString(first)
		coinb2, _ := hex.DecodeString(second)
		certificateTag := []byte("RGNCERT4")
		coinbase := append(coinb1, coinb2...)
		if bytes.Count(coinbase, certificateTag) != 1 {
			t.Fatal("V4 Stratum job did not carry exactly one certificate")
		}
		index := bytes.Index(coinbase, certificateTag)
		want := signedCertificate.Load()
		if index < 2 || index+40 > len(coinbase) || !bytes.Equal(coinbase[index-2:index], []byte{0x6a, 40}) || want == nil || !bytes.Equal(coinbase[index+8:index+40], want.([]byte)) || bytes.Equal(coinbase[index+8:index+40], make([]byte, 32)) {
			t.Fatal("published Stratum parent differs from the signed V4 certificate")
		}
	}
	if startupOnly {
		if len(header) != 80 || job == "" || ntime == "" {
			t.Fatal("invalid authorized Stratum work")
		}
		miner.conn.Close()
		t.Logf("PASS: compiled pool starts with exactly DGB/DOGE endpoints and delivers authorized Stratum work (V4=%v); no share mining or crash drill performed", v4)
		return
	}
	params := func(n uint32) []string { return []string{user, job, "00000000", ntime, fmt.Sprintf("%08x", n)} }
	miner.call("mining.submit", []string{user, "missing", "00000000", ntime, "00000000"}, 21)
	miner.call("mining.submit", []string{user, job, "xx", ntime, "00000000"}, 20)
	// Find an independently known low-difficulty header before submitting it.
	low := uint32(0)
	threshold, _ := forgepool.StratumTargetForDifficulty(big.NewInt(1))
	for {
		binary.LittleEndian.PutUint32(header[76:], low)
		hash, _ := forgepool.ScryptHash(header)
		if forgepool.HashValue(hash).Cmp(threshold) > 0 {
			break
		}
		low++
	}
	miner.call("mining.submit", params(low), 23)
	shares := mineProcessShares(t, header, 3)
	if result := miner.call("mining.submit", params(shares[0]), 0); string(result) != "true" {
		t.Fatal("valid share not accepted")
	}
	miner.call("mining.submit", params(shares[0]), 22)
	check := func(want int64) {
		t.Helper()
		n, err := r.backend.Client().HGet("forge-phase6:shares:roundCurrent", strings.Split(user, ".")[0]).Int64()
		if err != nil || n != want {
			t.Fatalf("credits %d want %d: %v", n, want, err)
		}
		if n, err := r.backend.Client().ZCard("forge-phase6:hashrate").Result(); err != nil || n != want {
			t.Fatal("hashrate count", n, err)
		}
		stats, err := r.backend.CollectWorkersStats(time.Hour, time.Hour, strings.Split(user, ".")[0])
		if err != nil || stats["hashrate"] != want*65536/600 {
			t.Fatal("Scrypt dashboard units", stats, err)
		}
	}
	check(1)
	for i, mode := range []int32{2, 1} {
		atomic.StoreInt32(&fault.mode, mode)
		miner.send("mining.submit", params(shares[i+1]))
		select {
		case got := <-fault.reached:
			if got != mode {
				t.Fatalf("fault mode %d", got)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("fault not reached")
		}
		before := int64(i + 1)
		if mode == 2 {
			before++
		}
		check(before)
		files, _ := filepath.Glob(filepath.Join(journal, "*.json"))
		if len(files) != 1 {
			t.Fatal("crash receipt not persisted", files)
		}
		stop()
		miner.conn.Close()
		fault.release <- struct{}{}
		t.Logf("SIGKILL generation %d at Redis fault mode %d, durable receipt retained", generation, mode)
		start()
		deadline := time.Now().Add(12 * time.Second)
		for {
			files, _ = filepath.Glob(filepath.Join(journal, "*.json"))
			if len(files) == 0 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("restart journal did not drain")
			}
			time.Sleep(25 * time.Millisecond)
		}
		check(int64(i + 2))
		miner = connectProcessMiner(t, stratumAddr)
		miner.handshake(user)
		nextHeader, nextJob, nextTime := processNotifyHeader(t, miner)
		if !bytes.Equal(nextHeader[:76], header[:76]) {
			t.Fatal("stable replay fixture changed")
		}
		job, ntime = nextJob, nextTime
		miner.call("mining.submit", params(shares[i+1]), 22)
		check(int64(i + 2))
	}
	if atomic.LoadInt32(&submissions) != 0 {
		t.Fatal("ordinary shares reached chain submission")
	}
	if candidates, err := r.backend.GetCandidates(3000); err != nil || len(candidates) != 0 {
		t.Fatal("ordinary shares created native round", err)
	}
	t.Log("PASS: full executable TCP Stratum, 3 mined shares, duplicate/stale/malformed/low-difficulty rejection; two SIGKILL/restarts before and after Redis commit; exactly 3 credits")
}
