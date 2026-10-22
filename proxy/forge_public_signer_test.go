package proxy

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/HawyUkr/open-progpow-pool/forgepool"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

func TestPublicSignerTransportPolicy(t *testing.T) {
	address := "0x0000000000000000000000000000000000000001"
	for _, endpoint := range []string{"http://example.com", "ftp://example.com", "https://user:pass@example.com", "https://example.com/?secret=x", "https://example.com/#fragment"} {
		if _, err := newForgeSigner(ForgeSigner{URL: endpoint, Address: address}); err == nil {
			t.Fatal("unsafe public signer URL accepted", endpoint)
		}
	}
	client, err := newForgeSigner(ForgeSigner{URL: "https://signer1.rogueisles.com/v4/sign", Address: address})
	if err != nil {
		t.Fatal(err)
	}
	if client.client.Transport.(*http.Transport).Proxy != nil {
		t.Fatal("signer transport uses environment proxy")
	}
	var followed int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&followed, 1)
	}))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	client, err = newForgeSigner(ForgeSigner{URL: redirect.URL, Address: address})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.signMulti(forgepool.MultiSignRequest{}); err == nil || atomic.LoadInt32(&followed) != 0 {
		t.Fatal("signer redirect followed or accepted")
	}
}

// The services exercise HTTPS and quorum handling; native validity is covered
// separately against the native engines and the actual signer implementation.
func TestV4TwoOfFourHTTPSAllAvailabilitySubsets(t *testing.T) {
	s, _, job, _, _, _ := testForgeServer(t)
	rt := s.forge
	rt.multi, rt.allowedExternalMask, rt.templateQuorum = true, 3, 2
	rt.templateValidity = time.Minute
	rt.signers, rt.authorized = nil, nil
	var unavailable uint32
	for index := uint32(0); index < 4; index++ {
		bit := uint32(1) << index
		key, err := crypto.GenerateKey()
		if err != nil {
			t.Fatal(err)
		}
		address := crypto.PubkeyToAddress(key.PublicKey)
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if atomic.LoadUint32(&unavailable)&bit != 0 {
				http.Error(w, "unavailable", http.StatusServiceUnavailable)
				return
			}
			var request forgepool.MultiSignRequest
			if json.NewDecoder(r.Body).Decode(&request) != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			digest, err := forgepool.MultiDigest(request.Template, request.WorkHash)
			if err != nil {
				w.WriteHeader(http.StatusUnprocessableEntity)
				return
			}
			signature, err := crypto.Sign(digest[:], key)
			if err != nil {
				t.Error(err)
				return
			}
			json.NewEncoder(w).Encode(forgepool.TemplateSignResponse{Signer: address.Hex(), Signature: hex.EncodeToString(signature)})
		}))
		t.Cleanup(server.Close)
		client, err := newForgeSigner(ForgeSigner{URL: server.URL, Address: address.Hex(), Timeout: "1s"})
		if err != nil {
			t.Fatal(err)
		}
		roots := x509.NewCertPool()
		roots.AddCert(server.Certificate())
		client.client.Transport.(*http.Transport).TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
		rt.signers = append(rt.signers, client)
		rt.authorized = append(rt.authorized, address)
	}
	parent, err := forgepool.BuildMultiParentV4(job.Digibyte.Template, job.OGG.WorkHash,
		map[uint32]*forgepool.DogeCandidate{98: job.Digibyte.DOGE}, []byte{0x51}, 9, 4, 4, true)
	if err != nil {
		t.Fatal(err)
	}
	job.Digibyte, job.ProtocolV3, job.ExternalMask = parent, true, 3
	for mask := uint32(0); mask < 16; mask++ {
		atomic.StoreUint32(&unavailable, mask)
		available := 0
		for index := uint32(0); index < 4; index++ {
			if mask&(1<<index) == 0 {
				available++
			}
		}
		job.Signed = nil
		err := rt.signMultiJob(job)
		if available < 2 {
			if err == nil || job.Signed != nil {
				t.Fatal("job authorized without quorum", mask)
			}
			continue
		}
		if err != nil || job.Signed == nil || len(job.Signed.Signatures) != 2 {
			t.Fatal("available pair did not authorize work", mask, err)
		}
		if err := job.Signed.validate(job, rt.authorized, 2, time.Now()); err != nil {
			t.Fatal("quorum failed identity checks", mask, err)
		}
		if err := parent.CheckCertificate(*job.Signed.Multi, job.Signed.Signatures); err != nil {
			t.Fatal("quorum certificate not bound to parent", mask, err)
		}
		if job.Signed.WorkHash != common.HexToHash(job.OGG.WorkHash).Hex() {
			t.Fatal("pool reward work changed")
		}
	}
}
