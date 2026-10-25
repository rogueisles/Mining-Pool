package proxy

import (
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/HawyUkr/open-progpow-pool/forgepool"
	"github.com/ethereum/go-ethereum/crypto"
)

func TestParallelQuorumDoesNotWaitForFirstTwoSlowSigners(t *testing.T) {
	s, _, job, _, _, _ := testForgeServer(t)
	rt := s.forge
	rt.multi, rt.allowedExternalMask, rt.templateQuorum = true, 3, 2
	rt.templateValidity = time.Minute
	rt.signers, rt.authorized = nil, nil
	allStarted := make(chan struct{})
	var startedCount int32
	canceled := make(chan struct{}, 2)
	release := make(chan struct{})
	defer close(release)
	for i := 0; i < 4; i++ {
		index := i
		key, err := crypto.GenerateKey()
		if err != nil {
			t.Fatal(err)
		}
		address := crypto.PubkeyToAddress(key.PublicKey)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var req forgepool.MultiSignRequest
			if json.NewDecoder(r.Body).Decode(&req) != nil {
				w.WriteHeader(400)
				return
			}
			if atomic.AddInt32(&startedCount, 1) == 4 {
				close(allStarted)
			}
			select {
			case <-allStarted:
			case <-r.Context().Done():
				return
			}
			if index < 2 {
				select {
				case <-r.Context().Done():
					canceled <- struct{}{}
					return
				case <-release:
					w.WriteHeader(503)
					return
				}
			}
			digest, err := forgepool.MultiDigest(req.Template, req.WorkHash)
			if err != nil {
				w.WriteHeader(422)
				return
			}
			sig, err := crypto.Sign(digest[:], key)
			if err != nil {
				w.WriteHeader(500)
				return
			}
			json.NewEncoder(w).Encode(forgepool.TemplateSignResponse{Signer: address.Hex(), Signature: hex.EncodeToString(sig)})
		}))
		t.Cleanup(server.Close)
		client, err := newForgeSigner(ForgeSigner{URL: server.URL, Address: address.Hex(), Timeout: "2s"})
		if err != nil {
			t.Fatal(err)
		}
		rt.signers = append(rt.signers, client)
		rt.authorized = append(rt.authorized, address)
	}
	parent, err := forgepool.BuildMultiParentV4(job.Digibyte.Template, job.OGG.WorkHash,
		map[uint32]*forgepool.DogeCandidate{98: job.Digibyte.DOGE}, []byte{0x51}, 9, 4, 4, true)
	if err != nil {
		t.Fatal(err)
	}
	job.Digibyte, job.ProtocolV3, job.ExternalMask = parent, true, 3
	done := make(chan error, 1)
	go func() { done <- rt.signJob(job) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("available quorum waited for unavailable first signers")
	}
	if len(job.Signed.Signatures) != 2 {
		t.Fatal("quorum changed")
	}
	if err = job.Signed.validate(job, rt.authorized, 2, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err = parent.CheckCertificate(*job.Signed.Multi, job.Signed.Signatures); err != nil {
		t.Fatal(err)
	}
	// Completion did not wait for the slow requests; every request retains
	// its finite client timeout and the collector cancels remaining requests.
	if atomic.LoadInt32(&startedCount) != 4 {
		t.Fatal("fallback signers were not contacted")
	}

}

func TestParallelQuorumRequiresDistinctAuthorizedIdentities(t *testing.T) {
	for _, unauthorized := range []bool{false, true} {
		s, _, job, _, _, _ := testForgeServer(t)
		rt := s.forge
		multiTemplateTestSigner(t, rt)
		client := rt.signers[0]
		rt.signers = append(rt.signers, client)
		rt.templateQuorum = 2
		if unauthorized {
			rt.authorized = nil
		}
		job.ProtocolV3, job.ExternalMask = true, 3
		job.Signed = nil
		if err := rt.signJob(job); err == nil || job.Signed != nil {
			t.Fatal("duplicate or unauthorized identity reached quorum")
		}
	}
}

func TestParallelQuorumIgnoresInvalidFastSignature(t *testing.T) {
	s, _, job, _, _, _ := testForgeServer(t)
	rt := s.forge
	multiTemplateTestSigner(t, rt)
	good := rt.signers[0]
	var called int32
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&called, 1)
		json.NewEncoder(w).Encode(forgepool.TemplateSignResponse{Signer: good.address.Hex(), Signature: hex.EncodeToString(make([]byte, 65))})
	}))
	defer bad.Close()
	copyClient := *good
	copyClient.url = bad.URL
	rt.signers = []*forgeSignerClient{&copyClient, good}
	rt.templateQuorum = 1
	job.ProtocolV3, job.ExternalMask = true, 3
	if err := rt.signJob(job); err != nil {
		t.Fatal(err)
	}
	if err := job.Signed.validate(job, rt.authorized, 1, time.Now()); err != nil {
		t.Fatal(err)
	}
}
