package proxy

import (
	"context"
	"crypto/ecdsa"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/HawyUkr/open-progpow-pool/forgepool"
	"github.com/HawyUkr/open-progpow-pool/forgesigner"
	"github.com/HawyUkr/open-progpow-pool/rpc"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// DEV-only backend belongs to the external test HTTP service, not forgeRuntime.
type independentDevKey struct{ key *ecdsa.PrivateKey }

func (k independentDevKey) Address() common.Address { return crypto.PubkeyToAddress(k.key.PublicKey) }
func (k independentDevKey) SignDigest(_ context.Context, h common.Hash) ([]byte, error) {
	return crypto.Sign(h.Bytes(), k.key)
}

type independentTestTemplates struct{ dgb, doge *rpc.BlockTemplateReply }

// This fixture stands in for the proposal RPC; the official daemon gate in
// forgesigner tests performs actual transaction/contextual validation.
func (s independentTestTemplates) ValidateDogeProposal(context.Context, []byte) error { return nil }

func (s independentTestTemplates) Templates(context.Context) (*rpc.BlockTemplateReply, *rpc.BlockTemplateReply, error) {
	return s.dgb, s.doge, nil
}
func signerGBT(t forgepool.ParentTemplate) *rpc.BlockTemplateReply {
	return &rpc.BlockTemplateReply{PowAlgo: "scrypt", PowAlgoID: func() *int { n := 1; return &n }(), Version: t.Version, PreviousBlockHash: forgepool.DisplayHash(t.Previous), Bits: fmt.Sprintf("%08x", t.Bits), Height: t.Height, CurTime: int64(t.CurTime), MinTime: int64(t.CurTime) - 1, CoinbaseValue: t.CoinbaseValue}
}

func TestForgeIndependentSignerQuorumHTTPAndProof(t *testing.T) {
	for _, tampered := range []bool{false, true} {
		t.Run(fmt.Sprint(tampered), func(t *testing.T) {
			s, _, job, _, _, nonce := testForgeServer(t)
			rt := s.forge
			rt.templateQuorum = 2
			rt.templateValidity = time.Minute
			// These copies represent each signer's independently received chain data.
			source := independentTestTemplates{dgb: signerGBT(job.Digibyte.Template), doge: signerGBT(job.Digibyte.DOGE.Template)}
			for i := 0; i < 2; i++ {
				key, err := crypto.GenerateKey()
				if err != nil {
					t.Fatal(err)
				}
				backend := independentDevKey{key}
				service, err := forgesigner.New(source, backend, forgesigner.Policy{DogePayoutScript: []byte{0x51}, MaxValidity: 60, MaxAge: 30}, nil)
				if err != nil {
					t.Fatal(err)
				}
				server := httptest.NewServer(service)
				defer server.Close()
				client, err := newForgeSigner(ForgeSigner{Name: fmt.Sprint(i), URL: server.URL, Address: backend.Address().Hex(), Timeout: "1s"})
				if err != nil {
					t.Fatal(err)
				}
				rt.signers = append(rt.signers, client)
				rt.authorized = append(rt.authorized, backend.Address())
			}
			if tampered {
				job.Digibyte.DOGE.JobNonce++
			}
			err := rt.signJob(job)
			if tampered {
				if err == nil || job.Signed != nil {
					t.Fatal("pool published a candidate rejected by independent signers")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(job.Signed.Signatures) != 2 {
				t.Fatal("missing quorum")
			}
			proof, err := job.Digibyte.BuildOGGProof([]byte{1, 2, 3, 4}, []byte{5, 6, 7, 8}, job.Digibyte.Template.CurTime, nonce, nil, job.Signed.Template, job.Signed.Signatures)
			if err != nil {
				t.Fatal(err)
			}
			blob, err := forgepool.EncodeOGGProof(proof)
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := forgepool.DecodeOGGProof(blob)
			if err != nil {
				t.Fatal(err)
			}
			if err := forgepool.ValidateTemplateSignatures(decoded.Template, decoded.Signatures, rt.authorized, 2, time.Now()); err != nil {
				t.Fatal(err)
			}
		})
	}
}
