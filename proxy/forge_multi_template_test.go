package proxy

import (
	"encoding/hex"
	"encoding/json"
	"github.com/HawyUkr/open-progpow-pool/forgepool"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func multiTemplateTestSigner(t *testing.T, rt *forgeRuntime, observers ...func(forgepool.MultiSignRequest, []byte)) {
	t.Helper()
	key, e := crypto.GenerateKey()
	if e != nil {
		t.Fatal(e)
	}
	address := crypto.PubkeyToAddress(key.PublicKey)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req forgepool.MultiSignRequest
		if json.NewDecoder(r.Body).Decode(&req) != nil {
			w.WriteHeader(400)
			return
		}
		digest, e := forgepool.MultiDigest(req.Template, req.WorkHash)
		if e != nil {
			w.WriteHeader(422)
			return
		}
		sig, e := crypto.Sign(digest.Bytes(), key)
		if e != nil {
			t.Error(e)
			return
		}
		for _, observe := range observers {
			observe(req, sig)
		}
		json.NewEncoder(w).Encode(forgepool.TemplateSignResponse{Signer: address.Hex(), Signature: hex.EncodeToString(sig)})
	}))
	t.Cleanup(server.Close)
	client, e := newForgeSigner(ForgeSigner{Name: "DEV-V3", URL: server.URL, Address: address.Hex(), Timeout: "2s"})
	if e != nil {
		t.Fatal(e)
	}
	rt.multi = true
	rt.signers = []*forgeSignerClient{client}
	rt.authorized = []common.Address{address}
	rt.templateQuorum = 1
	rt.templateValidity = time.Minute
}

func TestMultiV3SignedJobAndImmutableProof(t *testing.T) {
	s, _, job, _, _, _ := testForgeServer(t)
	multiTemplateTestSigner(t, s.forge)
	job.ProtocolV3 = true
	job.ExternalMask = 3
	if e := s.forge.signJob(job); e != nil {
		t.Fatal(e)
	}
	if job.Signed.Multi == nil || job.Signed.Multi.Mask != 3 {
		t.Fatal("missing V3 authorization")
	}
	sol := &forgeSolution{ExtraNonce1: make([]byte, 4), ExtraNonce2: make([]byte, 4), NTime: job.Digibyte.Template.CurTime, Nonce: 7}
	blob, e := forgeEncodeJobProof(job, sol)
	if e != nil {
		t.Fatal(e)
	}
	proof, e := forgepool.DecodeOGGProof(blob)
	if e != nil || proof.Multi == nil {
		t.Fatal("not a V3 envelope", e)
	}
	original := job.Digibyte.Coinbase.Coinb2[0]
	job.Digibyte.Coinbase.Coinb2[0] ^= 1
	if job.Signed.validate(job, s.forge.authorized, 1, time.Now()) == nil {
		t.Fatal("mutated parent coinbase remained authorized")
	}
	job.Digibyte.Coinbase.Coinb2[0] = original
	job.ExternalMask = 1
	if job.Signed.validate(job, s.forge.authorized, 1, time.Now()) == nil {
		t.Fatal("changed chain mask remained authorized")
	}
}

func TestMultiV3TwoOfThreeSignerAvailability(t *testing.T) {
	unavailable := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer unavailable.Close()
	s, _, job, _, _, _ := testForgeServer(t)
	rt := s.forge
	rt.multi, rt.allowedExternalMask, rt.templateQuorum = true, 3, 2
	rt.templateValidity = time.Minute
	rt.signers, rt.authorized = nil, nil
	for i := 0; i < 3; i++ {
		one := &forgeRuntime{}
		multiTemplateTestSigner(t, one)
		rt.signers = append(rt.signers, one.signers[0])
		rt.authorized = append(rt.authorized, one.authorized[0])
	}
	all := append([]*forgeSignerClient(nil), rt.signers...)
	job.ProtocolV3, job.ExternalMask = true, 3
	for _, failures := range [][]int{nil, {0}, {1}, {2}, {0, 1}} {
		rt.signers = append([]*forgeSignerClient(nil), all...)
		for _, index := range failures {
			copy := *all[index]
			copy.url = unavailable.URL
			rt.signers[index] = &copy
		}
		job.Signed = nil
		err := rt.signMultiJob(job)
		if len(failures) > 1 {
			if err == nil || job.Signed != nil {
				t.Fatal("job authorized without two available signers")
			}
			continue
		}
		if err != nil || job.Signed == nil || len(job.Signed.Signatures) != 2 {
			t.Fatal("one signer outage prevented quorum", failures, err)
		}
		if err := job.Signed.validate(job, rt.authorized, 2, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
}
