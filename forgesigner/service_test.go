package forgesigner

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/HawyUkr/open-progpow-pool/forgepool"
	"github.com/HawyUkr/open-progpow-pool/rpc"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

type testSource struct {
	dgb, doge *rpc.BlockTemplateReply
	err       error
	reads     int
}

func (s *testSource) Templates(context.Context) (*rpc.BlockTemplateReply, *rpc.BlockTemplateReply, error) {
	s.reads++
	return s.dgb, s.doge, s.err
}

type devBackend struct {
	key   *ecdsa.PrivateKey
	calls int
	hook  func()
	fail  bool
}

func (b *devBackend) Address() common.Address { return crypto.PubkeyToAddress(b.key.PublicKey) }
func (b *devBackend) SignDigest(ctx context.Context, digest common.Hash) ([]byte, error) {
	b.calls++
	if b.hook != nil {
		b.hook()
	}
	if b.fail {
		return nil, errors.New("key service offline")
	}
	return crypto.Sign(digest.Bytes(), b.key)
}

func fixture(t *testing.T) (*Service, *testSource, *devBackend, forgepool.TemplateSignRequest, *int64) {
	t.Helper()
	now := int64(1700000000)
	source := &testSource{
		dgb:  &rpc.BlockTemplateReply{PowAlgo: "scrypt", PowAlgoID: func() *int { n := 1; return &n }(), Version: 2, PreviousBlockHash: strings.Repeat("12", 32), Bits: "1d00ffff", Height: 200, CurTime: now, CoinbaseValue: 5000000000},
		doge: &rpc.BlockTemplateReply{PowAlgo: "scrypt", PowAlgoID: func() *int { n := 1; return &n }(), Version: 4, PreviousBlockHash: strings.Repeat("34", 32), Bits: "1e0fffff", Height: 300, CurTime: now, MinTime: now - 10, CoinbaseValue: 1000000000000},
	}
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	backend := &devBackend{key: key}
	service, err := New(source, backend, Policy{DogePayoutScript: []byte{0x52}, MaxValidity: 60, MaxAge: 30}, func() time.Time { return time.Unix(now, 0) })
	if err != nil {
		t.Fatal(err)
	}
	dp, err := source.doge.ToParentTemplate()
	if err != nil {
		t.Fatal(err)
	}
	doge, err := forgepool.BuildDogeCandidate(dp, []byte{0x52}, 77)
	if err != nil {
		t.Fatal(err)
	}
	proposal := forgepool.TemplateSignRequest{Domain: "OGG-FORGE-TEMPLATE-V1", Template: forgepool.ExternalTemplate{Version: 1, DGBPrevHash: common.HexToHash(source.dgb.PreviousBlockHash), DGBBits: 0x1d00ffff, DogeAuxHash: common.HexToHash(doge.DisplayHash), ValidFrom: uint64(now), Expires: uint64(now) + 60}, Doge: &forgepool.DogeSigningEvidence{JobNonce: 77, Time: uint32(now)}}
	proposal.Digest = forgepool.TemplateDigest(proposal.Template).Hex()
	return service, source, backend, proposal, &now
}

func TestIndependentSignerAcceptsAndVerifiesQuorum(t *testing.T) {
	s, src, b, r, now := fixture(t)
	out, err := s.Sign(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	sig, err := hex.DecodeString(strings.TrimPrefix(out.Signature, "0x"))
	if err != nil {
		t.Fatal(err)
	}
	if src.reads != 2 || b.calls != 1 {
		t.Fatal("missing independent pre/post checks")
	}
	if err := forgepool.ValidateTemplateSignatures(r.Template, [][]byte{sig}, []common.Address{b.Address()}, 1, time.Unix(*now, 0)); err != nil {
		t.Fatal(err)
	}
	other, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		sigs    [][]byte
		allowed []common.Address
		quorum  uint64
	}{
		{"insufficient", [][]byte{sig}, []common.Address{b.Address(), crypto.PubkeyToAddress(other.PublicKey)}, 2},
		{"duplicate", [][]byte{sig, sig}, []common.Address{b.Address()}, 1},
		{"unauthorized", [][]byte{sig}, []common.Address{crypto.PubkeyToAddress(other.PublicKey)}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if forgepool.ValidateTemplateSignatures(r.Template, tc.sigs, tc.allowed, tc.quorum, time.Unix(*now, 0)) == nil {
				t.Fatal("bad quorum accepted")
			}
		})
	}
}

func TestIndependentSignerRejectsBeforeKeyUse(t *testing.T) {
	for _, name := range []string{"domain", "digest", "version", "dgb-tip", "dgb-bits", "doge-hash", "missing-evidence", "nonce", "doge-time", "future", "expired", "age", "long-window", "reversed-window", "mintime", "trusted-time", "trusted-dgb-reorg", "trusted-doge-reorg", "trusted-doge-bits", "trusted-reward", "trusted-version", "trusted-flags", "trusted-transactions", "payout", "rpc-failure", "nil-template", "invalid-bits", "cancelled"} {
		t.Run(name, func(t *testing.T) {
			s, src, b, r, now := fixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch name {
			case "domain":
				r.Domain = "other"
			case "version":
				r.Template.Version = 2
			case "dgb-tip":
				r.Template.DGBPrevHash[0] ^= 1
			case "dgb-bits":
				r.Template.DGBBits++
			case "doge-hash":
				r.Template.DogeAuxHash[0] ^= 1
			case "missing-evidence":
				r.Doge = nil
			case "nonce":
				r.Doge.JobNonce++
			case "doge-time":
				r.Doge.Time++
			case "future":
				r.Template.ValidFrom++
			case "expired":
				r.Template.Expires = uint64(*now)
			case "age":
				r.Template.ValidFrom -= 31
				r.Template.Expires = uint64(*now) + 10
			case "long-window":
				r.Template.Expires++
			case "reversed-window":
				r.Template.Expires = r.Template.ValidFrom - 1
			case "mintime":
				src.doge.MinTime = *now + 1
			case "trusted-time":
				src.doge.CurTime++
			case "trusted-dgb-reorg":
				src.dgb.PreviousBlockHash = strings.Repeat("ab", 32)
			case "trusted-doge-reorg":
				src.doge.PreviousBlockHash = strings.Repeat("ab", 32)
			case "trusted-doge-bits":
				src.doge.Bits = "1d00ffff"
			case "trusted-reward":
				src.doge.CoinbaseValue++
			case "trusted-version":
				src.doge.Version++
			case "trusted-flags":
				src.doge.CoinbaseAux = map[string]string{"flags": "ff"}
			case "trusted-transactions":
				src.doge.Transactions = []rpc.GBTTransaction{{Data: "010203", TxID: strings.Repeat("ab", 32)}}
			case "payout":
				s.policy.DogePayoutScript = []byte{0x53}
			case "rpc-failure":
				src.err = errors.New("offline")
			case "nil-template":
				src.doge = nil
			case "invalid-bits":
				src.doge.Bits = "00000000"
			case "cancelled":
				cancel()
			}
			r.Digest = forgepool.TemplateDigest(r.Template).Hex()
			if name == "digest" {
				r.Digest = "0x00"
			}
			if out, err := s.Sign(ctx, r); err == nil || out != nil || b.calls != 0 {
				t.Fatalf("invalid proposal reached key: %v, calls=%d", err, b.calls)
			}
		})
	}
}

func TestIndependentSignerRechecksAfterBackendLatency(t *testing.T) {
	for _, name := range []string{"expiry", "dgb-reorg", "doge-reorg", "rpc-offline", "key-offline", "cancelled"} {
		t.Run(name, func(t *testing.T) {
			s, src, b, r, now := fixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			b.hook = func() {
				switch name {
				case "expiry":
					*now += 60
				case "dgb-reorg":
					src.dgb.PreviousBlockHash = strings.Repeat("ab", 32)
				case "doge-reorg":
					src.doge.PreviousBlockHash = strings.Repeat("ab", 32)
				case "rpc-offline":
					src.err = errors.New("offline")
				case "key-offline":
					b.fail = true
				case "cancelled":
					cancel()
				}
			}
			if out, err := s.Sign(ctx, r); err == nil || out != nil {
				t.Fatal("released stale signature")
			}
		})
	}
}

func TestIndependentSignerHTTPBoundsAndTrustedRPC(t *testing.T) {
	s, src, b, r, _ := fixture(t)
	var dgbCalls, dogeCalls int
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var call struct {
			Method string        `json:"method"`
			Params []interface{} `json:"params"`
		}
		if json.NewDecoder(req.Body).Decode(&call) != nil || call.Method != "getblocktemplate" {
			t.Error("wrong RPC")
			http.Error(w, "bad", 400)
			return
		}
		var result *rpc.BlockTemplateReply
		if req.URL.Path == "/dgb" {
			dgbCalls++
			result = src.dgb
			if len(call.Params) != 2 || call.Params[1] != "scrypt" {
				t.Error("missing DGB rules")
			}
		} else {
			dogeCalls++
			result = src.doge
		}
		json.NewEncoder(w).Encode(map[string]interface{}{"id": 0, "result": result})
	}))
	defer daemon.Close()
	s.source = RPCSource{Digibyte: rpc.NewRPCClient("trusted-dgb", daemon.URL+"/dgb", "1s", "", ""), Dogecoin: rpc.NewRPCClient("trusted-doge", daemon.URL+"/doge", "1s", "", "")}
	body, _ := json.Marshal(r)
	for _, tc := range []struct {
		name, method, body string
		status             int
	}{
		{"valid", "POST", string(body), 200},
		{"missing-evidence", "POST", `{"domain":"OGG-FORGE-TEMPLATE-V1"}`, 422},
		{"unknown-endpoint", "POST", strings.TrimSuffix(string(body), "}") + `,"rpcURL":"http://attacker"}`, 400},
		{"extra-object", "POST", string(body) + "{}", 400},
		{"oversize", "POST", strings.Repeat(" ", forgepool.MaxSignRequestBytes+1) + string(body), 400},
		{"method", "GET", string(body), 405},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			s.ServeHTTP(rec, httptest.NewRequest(tc.method, "/", bytes.NewBufferString(tc.body)))
			if rec.Code != tc.status {
				t.Fatalf("%d: %s", rec.Code, rec.Body.String())
			}
		})
	}
	if dgbCalls != 2 || dogeCalls != 2 || b.calls != 1 {
		t.Fatalf("unexpected trust/key calls %d/%d/%d", dgbCalls, dogeCalls, b.calls)
	}
}
