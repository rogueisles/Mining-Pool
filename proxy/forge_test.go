package proxy

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/HawyUkr/open-progpow-pool/forgepool"
	"github.com/HawyUkr/open-progpow-pool/rpc"
	"github.com/HawyUkr/open-progpow-pool/storage"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

type mockForgeWriter struct {
	calls    int
	exist    bool
	err      error
	events   *[]string
	receipts []storage.ForgeShare
}

func (m *mockForgeWriter) WriteForgeShare(receipt storage.ForgeShare) (bool, error) {
	m.calls++
	m.receipts = append(m.receipts, receipt)
	if m.events != nil {
		*m.events = append(*m.events, "account")
	}
	return m.exist, m.err
}

type mockForgeSubmitter struct {
	dgb, doge, ogg          int
	dgbErr, dogeErr, oggErr error
	events                  *[]string
}

func (m *mockForgeSubmitter) SubmitDigibyte(job *forgeJob, sol *forgeSolution) error {
	m.dgb++
	if m.events != nil {
		*m.events = append(*m.events, "dgb")
	}
	return m.dgbErr
}

func (m *mockForgeSubmitter) SubmitDogecoin(job *forgeJob, sol *forgeSolution) error {
	m.doge++
	if m.events != nil {
		*m.events = append(*m.events, "doge")
	}
	return m.dogeErr
}

func (m *mockForgeSubmitter) SubmitOGG(job *forgeJob, sol *forgeSolution) (string, error) {
	m.ogg++
	if m.events != nil {
		*m.events = append(*m.events, "ogg")
	}
	if m.oggErr != nil {
		return "", m.oggErr
	}
	return storage.ForgeProofHash(sol.Header), nil
}

func maxUint256() *big.Int {
	return new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1))
}

func testForgeServer(t *testing.T) (*ProxyServer, *Session, *forgeJob, *mockForgeWriter, *mockForgeSubmitter, uint32) {
	t.Helper()

	now := uint32(time.Now().Unix() - 5)
	var dogePrev forgepool.Hash
	var dgbPrev forgepool.Hash
	for i := range dogePrev {
		dogePrev[i] = byte(i + 1)
		dgbPrev[i] = byte(0x80 + i)
	}
	dogeTpl := forgepool.ParentTemplate{
		Version:       1,
		Previous:      dogePrev,
		CoinbaseValue: 5000000000,
		CurTime:       now,
		Bits:          0x207fffff,
		Height:        1000,
	}
	doge, err := forgepool.BuildDogeCandidate(dogeTpl, []byte{0x51}, 7)
	if err != nil {
		t.Fatal(err)
	}
	dgbTpl := forgepool.ParentTemplate{
		Version:       2,
		Previous:      dgbPrev,
		CoinbaseValue: 5000000000,
		CurTime:       now,
		Bits:          0x207fffff,
		Height:        2000,
	}
	dgb, err := forgepool.BuildDigibyteJob(
		dgbTpl,
		"0x"+strings.Repeat("11", 32),
		doge,
		[]byte{0x51},
		9,
		4,
		4,
	)
	if err != nil {
		t.Fatal(err)
	}
	job := &forgeJob{
		ID:         "cafebabe",
		Created:    time.Now(),
		Clean:      true,
		Height:     3000,
		Difficulty: 1,
		OGG:        &rpc.OGGAuxBlock{},
		Digibyte:   dgb,
	}
	// Avoid importing rpc only to construct a literal in every test.
	job.OGG.WorkHash = "0x" + strings.Repeat("11", 32)
	job.OGG.Target = "0x" + strings.Repeat("ff", 32)
	job.OGG.Height = "0xbb8"
	job.OGG.Timestamp = fmt.Sprintf("0x%x", now)
	job.OGG.TemplateMaxValidity = "0x12c"
	prevDisplay := forgepoolReverse(dgb.Template.Previous)
	job.Signed = &forgeSignedTemplate{
		WorkHash:    job.OGG.WorkHash,
		Timestamp:   uint64(now),
		MaxValidity: 300,
		Template: forgepool.ExternalTemplate{
			Version:     forgepool.OGGTemplateVersion,
			DGBPrevHash: common.BytesToHash(prevDisplay[:]),
			DGBBits:     dgb.Template.Bits,
			DogeAuxHash: common.HexToHash("0x" + doge.DisplayHash),
			ValidFrom:   uint64(now),
			Expires:     uint64(now) + 60,
		},
	}

	events := []string{}
	writer := &mockForgeWriter{events: &events}
	queue, err := newForgeAccountingQueue(t.TempDir(), writer)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { queue.close() })
	submitter := &mockForgeSubmitter{events: &events}
	rt := &forgeRuntime{
		jobs:            map[string]*forgeJob{job.ID: job},
		order:           []string{job.ID},
		currentID:       job.ID,
		duplicates:      make(map[string]struct{}),
		extraNonce1Size: 4,
		extraNonce2Size: 4,
		jobBacklog:      8,
		maxNTimeDrift:   2 * time.Hour,
		shareTarget:     maxUint256(),
		account:         queue,
		submitter:       submitter,
	}
	s := &ProxyServer{
		config:             &Config{Proxy: Proxy{Difficulty: 1}},
		forge:              rt,
		hashrateExpiration: time.Hour,
	}
	cs := &Session{
		ip:          "127.0.0.1",
		login:       "0x1111111111111111111111111111111111111111",
		worker:      "rig1",
		subscribed:  true,
		authorized:  true,
		extraNonce1: []byte{1, 2, 3, 4},
	}

	// Find a cheap regtest-style nonce that also meets 0x207fffff so the same
	// submission can exercise DGB+DOGE+OGG target routing.
	extra2 := []byte{5, 6, 7, 8}
	target, err := forgepool.CompactTarget(0x207fffff)
	if err != nil {
		t.Fatal(err)
	}
	var winning uint32
	found := false
	for n := uint32(0); n < 10000; n++ {
		hdr, err := job.Digibyte.Header(cs.extraNonce1, extra2, now, n)
		if err != nil {
			t.Fatal(err)
		}
		hash, err := forgepool.ScryptHash(hdr)
		if err != nil {
			t.Fatal(err)
		}
		if forgepool.HashValue(hash).Cmp(target) <= 0 {
			winning = n
			found = true
			break
		}
	}
	if !found {
		t.Fatal("failed to find easy test Scrypt nonce")
	}
	return s, cs, job, writer, submitter, winning
}

func submitArgs(job *forgeJob, nonce uint32) (string, string, string) {
	return "05060708", fmt.Sprintf("%08x", job.Digibyte.Template.CurTime), fmt.Sprintf("%08x", nonce)
}

func TestForgeUnknownOrStaleJobRejectedBeforeAccounting(t *testing.T) {
	s, cs, job, writer, _, nonce := testForgeServer(t)
	extra2, ntime, nonceHex := submitArgs(job, nonce)
	_, _, _, err := s.processForgeShare(cs, cs.login, cs.worker, "deadbeef", extra2, ntime, nonceHex, time.Now())
	if !errors.Is(err, errForgeStale) {
		t.Fatalf("have %v want stale", err)
	}
	if writer.calls != 0 {
		t.Fatalf("rejected stale share reached accounting: %d", writer.calls)
	}
}

func TestForgeMalformedFieldsRejectedBeforeAccounting(t *testing.T) {
	s, cs, job, writer, _, nonce := testForgeServer(t)
	_, ntime, nonceHex := submitArgs(job, nonce)
	cases := []struct {
		name   string
		extra2 string
		ntime  string
		nonce  string
	}{
		{"short-extranonce2", "00", ntime, nonceHex},
		{"bad-nonce", "05060708", ntime, "zzzzzzzz"},
		{"short-nonce", "05060708", ntime, "01"},
		{"bad-ntime", "05060708", "zzzzzzzz", nonceHex},
		{"old-ntime", "05060708", fmt.Sprintf("%08x", job.Digibyte.Template.CurTime-1), nonceHex},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, _, err := s.processForgeShare(cs, cs.login, cs.worker, job.ID, tc.extra2, tc.ntime, tc.nonce, time.Now())
			if err == nil {
				t.Fatal("expected rejection")
			}
		})
	}
	if writer.calls != 0 {
		t.Fatalf("malformed share reached accounting: %d", writer.calls)
	}
}

func TestForgeLowDifficultyRejectedBeforeAccounting(t *testing.T) {
	s, cs, job, writer, _, nonce := testForgeServer(t)
	s.forge.shareTarget = big.NewInt(1)
	extra2, ntime, nonceHex := submitArgs(job, nonce)
	_, _, _, err := s.processForgeShare(cs, cs.login, cs.worker, job.ID, extra2, ntime, nonceHex, time.Now())
	if !errors.Is(err, errForgeLowDifficulty) {
		t.Fatalf("have %v want low difficulty", err)
	}
	if writer.calls != 0 {
		t.Fatalf("low-difficulty share reached accounting: %d", writer.calls)
	}
}

func TestForgeValidShareCreditedExactlyOnceAndDuplicateRejected(t *testing.T) {
	s, cs, job, writer, submitter, nonce := testForgeServer(t)
	extra2, ntime, nonceHex := submitArgs(job, nonce)
	valid, dup, hits, err := s.processForgeShare(cs, cs.login, cs.worker, job.ID, extra2, ntime, nonceHex, time.Now())
	if err != nil || !valid || dup {
		t.Fatalf("first share valid=%v dup=%v err=%v", valid, dup, err)
	}
	if !hits.Digibyte || !hits.Dogecoin || !hits.OGG {
		t.Fatalf("expected all network hits, have %+v", hits)
	}
	if writer.calls != 1 {
		t.Fatalf("credit calls %d want 1", writer.calls)
	}
	if submitter.dgb != 1 || submitter.doge != 1 || submitter.ogg != 1 {
		t.Fatalf("submission routes DGB=%d DOGE=%d OGG=%d", submitter.dgb, submitter.doge, submitter.ogg)
	}

	valid, dup, _, err = s.processForgeShare(cs, cs.login, cs.worker, job.ID, extra2, ntime, nonceHex, time.Now())
	if valid || !dup || !errors.Is(err, errForgeDuplicate) {
		t.Fatalf("duplicate valid=%v dup=%v err=%v", valid, dup, err)
	}
	if writer.calls != 1 {
		t.Fatalf("duplicate was credited: %d calls", writer.calls)
	}
	if submitter.dgb != 1 || submitter.doge != 1 || submitter.ogg != 1 {
		t.Fatal("duplicate triggered repeated network submission")
	}
}

func TestForgeBlockSubmissionsPrecedeAccountingAndAccountingFailureDoesNotBlockThem(t *testing.T) {
	s, cs, job, writer, submitter, nonce := testForgeServer(t)
	writer.err = errors.New("redis unavailable")
	events := []string{}
	writer.events = &events
	submitter.events = &events

	extra2, ntime, nonceHex := submitArgs(job, nonce)
	valid, dup, hits, err := s.processForgeShare(cs, cs.login, cs.worker, job.ID, extra2, ntime, nonceHex, time.Now())
	if err == nil || valid || dup {
		t.Fatalf("expected accounting failure valid=%v dup=%v hits=%+v err=%v", valid, dup, hits, err)
	}
	want := []string{"dgb", "doge", "ogg", "account"}
	if len(events) != len(want) {
		t.Fatalf("events %v want %v", events, want)
	}
	for i := range want {
		if events[i] != want[i] {
			t.Fatalf("events %v want %v", events, want)
		}
	}
}

func TestForgeOneChainSubmissionFailureDoesNotBlockOthers(t *testing.T) {
	s, cs, job, writer, submitter, nonce := testForgeServer(t)
	submitter.dgbErr = errors.New("DGB unavailable")
	extra2, ntime, nonceHex := submitArgs(job, nonce)
	valid, dup, _, err := s.processForgeShare(cs, cs.login, cs.worker, job.ID, extra2, ntime, nonceHex, time.Now())
	if err != nil || !valid || dup {
		t.Fatalf("valid=%v dup=%v err=%v", valid, dup, err)
	}
	if submitter.dgb != 1 || submitter.doge != 1 || submitter.ogg != 1 {
		t.Fatalf("routes DGB=%d DOGE=%d OGG=%d", submitter.dgb, submitter.doge, submitter.ogg)
	}
	if writer.calls != 1 {
		t.Fatalf("accounting calls %d", writer.calls)
	}
}

func TestForgeRedisDuplicateNotCreditedTwice(t *testing.T) {
	s, cs, job, writer, _, nonce := testForgeServer(t)
	writer.exist = true
	extra2, ntime, nonceHex := submitArgs(job, nonce)
	valid, dup, _, err := s.processForgeShare(cs, cs.login, cs.worker, job.ID, extra2, ntime, nonceHex, time.Now())
	if valid || !dup || !errors.Is(err, errForgeDuplicate) {
		t.Fatalf("valid=%v dup=%v err=%v", valid, dup, err)
	}
	if writer.calls != 1 {
		t.Fatalf("writer calls %d", writer.calls)
	}
}

func TestForgeStratumPrevHashWordOrder(t *testing.T) {
	var raw forgepool.Hash
	for i := 0; i < len(raw); i++ {
		raw[i] = byte(i)
	}
	got := stratumPrevHash(raw)
	want := "03020100070605040b0a09080f0e0d0c13121110171615141b1a19181f1e1d1c"
	if got != want {
		t.Fatalf("prevhash\nhave %s\nwant %s", got, want)
	}
}

func TestForgeNotifyTupleShape(t *testing.T) {
	_, _, job, _, _, _ := testForgeServer(t)
	p := forgeNotifyParams(job)
	if len(p) != 9 {
		t.Fatalf("notify field count %d", len(p))
	}
	if p[0] != job.ID {
		t.Fatalf("job id have %v", p[0])
	}
	if p[8] != true {
		t.Fatalf("clean_jobs have %v", p[8])
	}
}

func TestParseForgeWorkerName(t *testing.T) {
	login, worker, err := parseForgeWorkerName("0x1111111111111111111111111111111111111111.rig-1")
	if err != nil {
		t.Fatal(err)
	}
	if login != "0x1111111111111111111111111111111111111111" || worker != "rig-1" {
		t.Fatalf("login=%s worker=%s", login, worker)
	}
	if _, _, err := parseForgeWorkerName("not-an-address.rig"); err == nil {
		t.Fatal("accepted invalid address")
	}
}

func TestForgePoolShareWithoutNetworkHitOnlyCreditsAccounting(t *testing.T) {
	s, cs, job, writer, submitter, nonce := testForgeServer(t)
	// Tighten all network targets after finding an easy pool-valid nonce. The
	// pool target remains maxUint256, so this must be credited as a share but
	// not routed as a block to any chain.
	job.Digibyte.Template.Bits = 0x1d00ffff
	job.Digibyte.DOGE.Bits = 0x1d00ffff
	job.OGG.Target = "0x1"

	extra2, ntime, nonceHex := submitArgs(job, nonce)
	valid, dup, hits, err := s.processForgeShare(cs, cs.login, cs.worker, job.ID, extra2, ntime, nonceHex, time.Now())
	if err != nil || !valid || dup {
		t.Fatalf("valid=%v dup=%v err=%v", valid, dup, err)
	}
	if hits.Digibyte || hits.Dogecoin || hits.OGG {
		t.Fatalf("unexpected network hit %+v", hits)
	}
	if writer.calls != 1 {
		t.Fatalf("credit calls %d want 1", writer.calls)
	}
	if submitter.dgb != 0 || submitter.doge != 0 || submitter.ogg != 0 {
		t.Fatalf("unexpected routing DGB=%d DOGE=%d OGG=%d", submitter.dgb, submitter.doge, submitter.ogg)
	}
}

func TestForgeIndependentSingleChainTargetHits(t *testing.T) {
	cases := []struct {
		name              string
		dgbBits, dogeBits uint32
		oggTarget         string
		want              forgeHits
	}{
		{"dgb-only", 0x207fffff, 0x1d00ffff, "0x1", forgeHits{Digibyte: true}},
		{"doge-only", 0x1d00ffff, 0x207fffff, "0x1", forgeHits{Dogecoin: true}},
		{"ogg-only", 0x1d00ffff, 0x1d00ffff, "0x" + strings.Repeat("ff", 32), forgeHits{OGG: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, cs, job, _, submitter, _ := testForgeServer(t)
			job.Digibyte.Template.Bits = tc.dgbBits
			job.Digibyte.DOGE.Bits = tc.dogeBits
			job.OGG.Target = tc.oggTarget
			lt, _ := forgepool.CompactTarget(tc.dgbBits)
			dt, _ := forgepool.CompactTarget(tc.dogeBits)
			ot, _ := parseHexTarget(tc.oggTarget)
			var nonce uint32
			found := false
			for n := uint32(0); n < 10000; n++ {
				hdr, _ := job.Digibyte.Header(cs.extraNonce1, []byte{5, 6, 7, 8}, job.Digibyte.Template.CurTime, n)
				h, _ := forgepool.ScryptHash(hdr)
				v := forgepool.HashValue(h)
				have := forgeHits{Digibyte: v.Cmp(lt) <= 0, Dogecoin: v.Cmp(dt) <= 0, OGG: v.Cmp(ot) <= 0}
				if have == tc.want {
					nonce, found = n, true
					break
				}
			}
			if !found {
				t.Fatal("failed to find deterministic single-chain hit")
			}
			extra2, ntime, nonceHex := submitArgs(job, nonce)
			valid, dup, hits, err := s.processForgeShare(cs, cs.login, cs.worker, job.ID, extra2, ntime, nonceHex, time.Now())
			if err != nil || !valid || dup {
				t.Fatalf("valid=%v dup=%v err=%v", valid, dup, err)
			}
			if hits != tc.want {
				t.Fatalf("hits=%+v want=%+v", hits, tc.want)
			}
			if submitter.dgb != boolInt(tc.want.Digibyte) || submitter.doge != boolInt(tc.want.Dogecoin) || submitter.ogg != boolInt(tc.want.OGG) {
				t.Fatalf("routes DGB=%d DOGE=%d OGG=%d", submitter.dgb, submitter.doge, submitter.ogg)
			}
		})
	}
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func TestForgeAccountingFailureReplaysWithoutResubmitting(t *testing.T) {
	s, cs, job, writer, submitter, nonce := testForgeServer(t)
	writer.err = errors.New("redis unavailable")
	extra2, ntime, nonceHex := submitArgs(job, nonce)
	_, _, _, err := s.processForgeShare(cs, cs.login, cs.worker, job.ID, extra2, ntime, nonceHex, time.Now())
	if err == nil {
		t.Fatal("expected accounting error")
	}

	valid, dup, _, err := s.processForgeShare(cs, cs.login, cs.worker, job.ID, extra2, ntime, nonceHex, time.Now())
	if !errors.Is(err, errForgeDuplicate) || valid || !dup {
		t.Fatalf("retry valid=%v dup=%v err=%v", valid, dup, err)
	}
	// Recovery works without the miner and without a surviving job.
	delete(s.forge.jobs, job.ID)
	writer.err = nil
	if err := s.forge.account.flush(); err != nil {
		t.Fatal(err)
	}
	if writer.calls != 2 {
		t.Fatalf("writer calls %d want 2", writer.calls)
	}
	if submitter.dgb != 1 || submitter.doge != 1 || submitter.ogg != 1 {
		t.Fatal("replayed network submission")
	}
	if writer.receipts[0].ProofHash == "" || writer.receipts[0] != writer.receipts[1] {
		t.Fatal("accepted proof identity lost on retry")
	}
}

func TestForgeStratumWireShapes(t *testing.T) {
	var buf bytes.Buffer
	cs := &Session{enc: json.NewEncoder(&buf)}
	id := json.RawMessage("1")

	if err := cs.sendStratumResult(id, true); err != nil {
		t.Fatal(err)
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &resp); err != nil {
		t.Fatal(err)
	}
	if _, ok := resp["jsonrpc"]; ok {
		t.Fatal("FORGE Stratum response unexpectedly contains jsonrpc field")
	}
	if resp["result"] != true {
		t.Fatalf("result=%v", resp["result"])
	}

	buf.Reset()
	cs.enc = json.NewEncoder(&buf)
	if err := cs.sendStratumNotification("mining.set_difficulty", []interface{}{int64(16)}); err != nil {
		t.Fatal(err)
	}
	var push map[string]interface{}
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &push); err != nil {
		t.Fatal(err)
	}
	if push["id"] != nil || push["method"] != "mining.set_difficulty" {
		t.Fatalf("push=%v", push)
	}
	if _, ok := push["result"]; ok {
		t.Fatal("notification unexpectedly contains result field")
	}
}

func TestExternalSignerInterfaceReturnsVerifiableSignature(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	addr := crypto.PubkeyToAddress(key.PublicKey)
	tmpl := forgepool.ExternalTemplate{Version: forgepool.OGGTemplateVersion, DGBPrevHash: common.HexToHash("0x01"), DGBBits: 0x1d00ffff, DogeAuxHash: common.HexToHash("0x02"), ValidFrom: uint64(time.Now().Unix() - 1), Expires: uint64(time.Now().Unix() + 60)}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req forgeSignRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		if req.Digest != forgepool.TemplateDigest(req.Template).Hex() {
			t.Error("signer request digest mismatch")
		}
		sig, _ := crypto.Sign(forgepool.TemplateDigest(req.Template).Bytes(), key)
		_ = json.NewEncoder(w).Encode(forgeSignResponse{Signer: addr.Hex(), Signature: "0x" + fmt.Sprintf("%x", sig)})
	}))
	defer srv.Close()
	c, err := newForgeSigner(ForgeSigner{Name: "test", URL: srv.URL, Address: addr.Hex(), Timeout: "2s"})
	if err != nil {
		t.Fatal(err)
	}
	sig, err := c.sign(tmpl, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := forgepool.ValidateTemplateSignatures(tmpl, [][]byte{sig}, []common.Address{addr}, 1, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestForgeRPCReceiptBindsExactSubmittedEnvelope(t *testing.T) {
	s, cs, job, _, _, nonce := testForgeServer(t)
	extra, ntime, nonceHex := submitArgs(job, nonce)
	_, sol, _, err := s.validateForgeShare(cs, job.ID, extra, ntime, nonceHex, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	addr := crypto.PubkeyToAddress(key.PublicKey)
	signerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req forgeSignRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		sig, err := crypto.Sign(forgepool.TemplateDigest(req.Template).Bytes(), key)
		if err != nil {
			t.Error(err)
			return
		}
		json.NewEncoder(w).Encode(forgeSignResponse{Signer: addr.Hex(), Signature: fmt.Sprintf("0x%x", sig)})
	}))
	defer signerServer.Close()
	signer, err := newForgeSigner(ForgeSigner{Name: "test", URL: signerServer.URL, Address: addr.Hex(), Timeout: "2s"})
	if err != nil {
		t.Fatal(err)
	}
	var submittedProof []byte
	accepted := true
	nodeServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string
			Params []string
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		if req.Method != "eth_submitAuxBlock" || len(req.Params) != 2 || req.Params[0] != job.OGG.WorkHash {
			t.Error("wrong work binding")
			return
		}
		submittedProof, err = hex.DecodeString(strings.TrimPrefix(req.Params[1], "0x"))
		if err != nil {
			t.Error(err)
			return
		}
		if _, err := forgepool.DecodeOGGProof(submittedProof); err != nil {
			t.Error(err)
			return
		}
		json.NewEncoder(w).Encode(map[string]interface{}{"jsonrpc": "2.0", "id": 0, "result": accepted})
	}))
	defer nodeServer.Close()
	s.upstreams = []*rpc.RPCClient{rpc.NewRPCClient("test", nodeServer.URL, "2s", "", "")}
	job.OGGRPC = s.upstreams[0]
	s.forge.signers = []*forgeSignerClient{signer}
	s.forge.authorized = []common.Address{addr}
	s.forge.templateQuorum = 1
	s.forge.templateValidity = time.Minute
	if err := s.forge.signJob(job); err != nil {
		t.Fatal(err)
	}
	// Submission must use the issuer even if the active upstream later changes.
	s.upstreams = nil
	signerServer.Close()
	submitter := &rpcForgeSubmitter{authorized: []common.Address{addr}, quorum: 1}
	hash, err := submitter.SubmitOGG(job, sol)
	if err != nil || hash != storage.ForgeProofHash(submittedProof) {
		t.Fatalf("receipt identity %s %v", hash, err)
	}
	accepted = false
	if hash, err := submitter.SubmitOGG(job, sol); err == nil || hash != "" {
		t.Fatal("rejection produced accepted proof receipt")
	}
}
