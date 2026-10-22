package proxy

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/HawyUkr/open-progpow-pool/forgepool"
	"github.com/HawyUkr/open-progpow-pool/rpc"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// Only the test service owns this generated DEV key. The pool receives public
// signer configuration and signatures, exactly as it does with a remote service.
func templateTestSigner(t *testing.T, rt *forgeRuntime, beforeSign func(forgeSignRequest)) (*httptest.Server, *int32) {
	t.Helper()
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	address := crypto.PubkeyToAddress(key.PublicKey)
	var count int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&count, 1)
		var req forgeSignRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		var workHash common.Hash

		v2 := req.Template.Version&^forgepool.ExternalMask ==
			forgepool.OGGTemplateVersionV2Base

		if v2 {
			if req.WorkHash == nil || *req.WorkHash == (common.Hash{}) {
				t.Error("V2 test signer missing exact WorkHash")
				return
			}
			workHash = *req.WorkHash
		} else if req.WorkHash != nil {
			t.Error("V1 test signer unexpectedly received WorkHash")
			return
		}

		digest, err := forgepool.TemplateSigningDigest(
			req.Template,
			workHash,
		)
		if err != nil {
			t.Error(err)
			return
		}

		if req.Domain != forgepool.TemplateDomain(req.Template) ||
			req.Digest != digest.Hex() {
			t.Error("bad signer digest/domain")
			return
		}

		if beforeSign != nil {
			beforeSign(req)
		}

		sig, err := crypto.Sign(digest.Bytes(), key)
		if err != nil {
			t.Error(err)
			return
		}
		json.NewEncoder(w).Encode(forgeSignResponse{Signer: address.Hex(), Signature: fmt.Sprintf("0x%x", sig)})
	}))
	t.Cleanup(srv.Close)
	client, err := newForgeSigner(ForgeSigner{Name: "DEV-test", URL: srv.URL, Address: address.Hex(), Timeout: "2s"})
	if err != nil {
		t.Fatal(err)
	}
	rt.signers = []*forgeSignerClient{client}
	rt.authorized = []common.Address{address}
	rt.templateQuorum = 1
	rt.templateValidity = time.Minute
	return srv, &count
}

func TestForgeSignedTemplateUsesIssuedTimeNotSubmissionTime(t *testing.T) {
	s, _, job, _, _, _ := testForgeServer(t)
	stamp := int64(1700000000)
	job.OGG.Timestamp = fmt.Sprintf("0x%x", stamp)
	job.OGG.TemplateMaxValidity = "0x1e" // Node bound overrides longer pool preference.
	s.forge.clock = func() time.Time { return time.Unix(stamp+20, 0) }
	_, count := templateTestSigner(t, s.forge, nil)
	if err := s.forge.signJob(job); err != nil {
		t.Fatal(err)
	}
	if job.Signed.Template.ValidFrom != uint64(stamp) || job.Signed.Template.Expires != uint64(stamp+30) {
		t.Fatalf("wrong time window %+v", job.Signed.Template)
	}
	if err := job.Signed.validate(job, s.forge.authorized, 1, time.Unix(stamp+25, 0)); err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(count) != 1 {
		t.Fatal("unexpected signing count")
	}
	if err := job.Signed.validate(job, s.forge.authorized, 1, time.Unix(stamp+30, 0)); err == nil {
		t.Fatal("expiry boundary accepted")
	}
	job.OGG.WorkHash = "0x" + strings.Repeat("55", 32)
	if err := job.Signed.validate(job, s.forge.authorized, 1, time.Unix(stamp+25, 0)); err == nil {
		t.Fatal("work substitution accepted")
	}
}

func TestForgeMissingMalformedOldOrFutureWorkTimeFailsClosed(t *testing.T) {
	for _, tc := range []struct{ name, stamp, limit string }{
		{"old-node", "", ""}, {"missing-limit", "0x64", ""}, {"bare-time", "64", "0x12c"},
		{"zero-time", "0x0", "0x12c"}, {"zero-limit", "0x64", "0x0"},
		{"expired", "0x1", "0x12c"}, {"future", "0x1000", "0x12c"},
		{"overflow", "0xffffffffffffffff", "0x12c"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, job, _, _, _ := testForgeServer(t)
			job.Signed = nil
			job.OGG.Timestamp = tc.stamp
			job.OGG.TemplateMaxValidity = tc.limit
			s.forge.clock = func() time.Time { return time.Unix(1000, 0) }
			_, count := templateTestSigner(t, s.forge, nil)
			if err := s.forge.signJob(job); err == nil || job.Signed != nil {
				t.Fatal("invalid work signed")
			}
			if atomic.LoadInt32(count) != 0 {
				t.Fatal("invalid work reached external signer")
			}
		})
	}
}

func TestForgeSignerLatencyCannotPublishExpiredTemplate(t *testing.T) {
	s, _, job, _, _, _ := testForgeServer(t)
	job.Signed = nil
	job.OGG.Timestamp = "0x64"
	var clock int64 = 110
	s.forge.clock = func() time.Time { return time.Unix(atomic.LoadInt64(&clock), 0) }
	templateTestSigner(t, s.forge, func(forgeSignRequest) { atomic.StoreInt64(&clock, 161) })
	if err := s.forge.signJob(job); err == nil || job.Signed != nil {
		t.Fatal("expired signer response became a job")
	}
}

func TestForgeExpiredBacklogRejectedBeforeCredit(t *testing.T) {
	s, cs, job, writer, submitter, nonce := testForgeServer(t)
	now := time.Unix(int64(job.Signed.Template.Expires), 0)
	s.forge.clock = func() time.Time { return now }
	if s.forge.currentJob() != nil {
		t.Fatal("expired current job exposed to Stratum")
	}
	extra, ntime, n := submitArgs(job, nonce)
	if _, _, _, err := s.processForgeShare(cs, cs.login, cs.worker, job.ID, extra, ntime, n, now); !errors.Is(err, errForgeStale) {
		t.Fatal("expired backlog accepted", err)
	}
	if writer.calls != 0 || submitter.ogg != 0 || submitter.dgb != 0 || submitter.doge != 0 {
		t.Fatal("expired share routed or credited")
	}
}

func TestForgeExpiryAfterOtherChainAttemptsStillCreditsOneShare(t *testing.T) {
	s, cs, job, writer, _, nonce := testForgeServer(t)
	templateTestSigner(t, s.forge, nil)
	if err := s.forge.signJob(job); err != nil {
		t.Fatal(err)
	}
	nodeCalls := int32(0)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { atomic.AddInt32(&nodeCalls, 1) }))
	defer srv.Close()
	job.OGGRPC = rpc.NewRPCClient("issuer", srv.URL, "2s", "", "")
	ogg := &rpcForgeSubmitter{authorized: s.forge.authorized, quorum: 1, clock: func() time.Time { return time.Unix(int64(job.Signed.Template.Expires), 0) }}
	router := &templateExpiringRouter{ogg: ogg}
	s.forge.submitter = router
	extra, ntime, n := submitArgs(job, nonce)
	valid, _, _, err := s.processForgeShare(cs, cs.login, cs.worker, job.ID, extra, ntime, n, time.Now())
	if err != nil || !valid || router.dgb != 1 || router.doge != 1 || writer.calls != 1 {
		t.Fatalf("routes %+v err=%v", router, err)
	}
	if atomic.LoadInt32(&nodeCalls) != 0 || writer.receipts[0].ProofHash != "" || writer.receipts[0].OGGError == "" {
		t.Fatal("expired native template submitted or recorded")
	}
}

type templateExpiringRouter struct {
	dgb, doge int
	ogg       *rpcForgeSubmitter
}

func (r *templateExpiringRouter) SubmitDigibyte(*forgeJob, *forgeSolution) error { r.dgb++; return nil }
func (r *templateExpiringRouter) SubmitDogecoin(*forgeJob, *forgeSolution) error {
	r.doge++
	return nil
}
func (r *templateExpiringRouter) SubmitOGG(j *forgeJob, s *forgeSolution) (string, error) {
	return r.ogg.SubmitOGG(j, s)
}

func TestForgeSignerCannotImpersonateAnotherAuthorizedSigner(t *testing.T) {
	s, _, job, _, _, _ := testForgeServer(t)
	srv, _ := templateTestSigner(t, s.forge, nil)
	other, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	otherAddress := crypto.PubkeyToAddress(other.PublicKey)
	client := s.forge.signers[0]
	address := client.address
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req forgeSignRequest
		json.NewDecoder(r.Body).Decode(&req)
		sig, _ := crypto.Sign(forgepool.TemplateDigest(req.Template).Bytes(), other)
		json.NewEncoder(w).Encode(forgeSignResponse{Signer: address.Hex(), Signature: fmt.Sprintf("0x%x", sig)})
	}))
	defer bad.Close()
	srv.Close()
	client.url = bad.URL
	s.forge.authorized = append(s.forge.authorized, otherAddress)
	job.Signed = nil
	if err := s.forge.signJob(job); err == nil || job.Signed != nil {
		t.Fatal("signer impersonation accepted")
	}
}

func TestForgeRefreshPublishesOnlySignedCompatibleWork(t *testing.T) {
	for _, compatible := range []bool{true, false} {
		t.Run(fmt.Sprint(compatible), func(t *testing.T) {
			s, _, fixture, _, _, _ := testForgeServer(t)
			work := *fixture.OGG
			work.Difficulty = "0x1"
			if !compatible {
				work.Timestamp = ""
			}
			nodeServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				json.NewEncoder(w).Encode(map[string]interface{}{"jsonrpc": "2.0", "id": 0, "result": work})
			}))
			defer nodeServer.Close()
			gbtServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				json.NewEncoder(w).Encode(map[string]interface{}{"jsonrpc": "2.0", "id": 0, "result": rpc.BlockTemplateReply{PowAlgo: "scrypt", PowAlgoID: func() *int { n:=1; return &n }(),Version: 1, Height: 2000, CurTime: int64(fixture.Digibyte.Template.CurTime), PreviousBlockHash: strings.Repeat("22", 32), Bits: "1d00ffff", CoinbaseValue: 5000000000}})
			}))
			defer gbtServer.Close()
			rt := s.forge
			rt.jobs = make(map[string]*forgeJob)
			rt.order = nil
			rt.currentID = ""
			s.upstreams = []*rpc.RPCClient{rpc.NewRPCClient("issuer", nodeServer.URL, "2s", "", "")}
			rt.dgbRPC = rpc.NewRPCClient("dgb", gbtServer.URL, "2s", "", "")
			rt.dogeRPC = rpc.NewRPCClient("doge", gbtServer.URL, "2s", "", "")
			rt.dgbPayout = []byte{0x51}
			rt.dogePayout = []byte{0x52}
			_, count := templateTestSigner(t, rt, func(forgeSignRequest) {
				if rt.currentJob() != nil {
					t.Error("job published before signing")
				}
			})
			s.fetchForgeBlockTemplate()
			job := rt.currentJob()
			if !compatible {
				if job != nil || atomic.LoadInt32(count) != 0 {
					t.Fatal("old RPC metadata did not fail closed")
				}
				return
			}
			if job == nil || atomic.LoadInt32(count) != 1 || job.OGGRPC != s.upstreams[0] {
				t.Fatal("signed job not published with issuer")
			}
			if err := job.Signed.validate(job, rt.authorized, rt.templateQuorum, time.Now()); err != nil {
				t.Fatal(err)
			}
		})
	}
}
