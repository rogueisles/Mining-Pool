package proxy

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/HawyUkr/open-progpow-pool/forgepool"
	"github.com/HawyUkr/open-progpow-pool/rpc"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// Paired with the node's TestExportForgePoolWork and TestPoolProofInterop.
// A JSON boundary uses each repository's real types/encoding independently.
// No node source is copied into the pool and no consensus checks are bypassed.
func TestExportForgeNodeFixture(t *testing.T) {
	input, output := os.Getenv("FORGE_ISSUED_FIXTURE"), os.Getenv("FORGE_POOL_FIXTURE")
	if input == "" || output == "" {
		t.Skip("run scripts/test-forge-node.sh")
	}
	var issued struct {
		HeaderRLP, WorkHash            string
		Timestamp, TemplateMaxValidity uint64
	}
	data, err := os.ReadFile(input)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &issued); err != nil {
		t.Fatal(err)
	}
	headerRLP, err := hex.DecodeString(issued.HeaderRLP)
	if err != nil {
		t.Fatal(err)
	}
	if crypto.Keccak256Hash(headerRLP).Hex() != issued.WorkHash {
		t.Fatal("issued header and work hash disagree")
	}
	var prevDGB, prevDOGE forgepool.Hash
	for i := range prevDGB {
		prevDGB[i] = byte(0x80 + i)
		prevDOGE[i] = byte(i + 1)
	}
	doge, err := forgepool.BuildDogeCandidate(forgepool.ParentTemplate{Version: 1, Previous: prevDOGE, Height: 1000, CurTime: uint32(issued.Timestamp + 20), Bits: 0x1d00ffff, CoinbaseValue: 5000000000}, []byte{0x52}, 7)
	if err != nil {
		t.Fatal(err)
	}
	dgb, err := forgepool.BuildDigibyteJob(forgepool.ParentTemplate{Version: 2, Previous: prevDGB, Height: 2000, CurTime: uint32(issued.Timestamp + 20), Bits: 0x1d00ffff, CoinbaseValue: 5000000000}, issued.WorkHash, doge, []byte{0x51}, 9, 4, 4)
	if err != nil {
		t.Fatal(err)
	}
	job := &forgeJob{OGG: &rpc.OGGAuxBlock{WorkHash: issued.WorkHash, Timestamp: fmt.Sprintf("0x%x", issued.Timestamp), TemplateMaxValidity: fmt.Sprintf("0x%x", issued.TemplateMaxValidity)}, Digibyte: dgb}
	// Publicly known DEV fixture key (scalar 1). Never configured in pool runtime.
	key, err := crypto.HexToECDSA(strings.Repeat("0", 63) + "1")
	if err != nil {
		t.Fatal(err)
	}
	address := crypto.PubkeyToAddress(key.PublicKey)
	signerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req forgeSignRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		if req.Template.ValidFrom != issued.Timestamp || req.Template.DogeAuxHash != common.HexToHash("0x"+doge.DisplayHash) {
			t.Error("fixture binding changed")
			return
		}
		sig, err := crypto.Sign(forgepool.TemplateDigest(req.Template).Bytes(), key)
		if err != nil {
			t.Error(err)
			return
		}
		json.NewEncoder(w).Encode(forgeSignResponse{Signer: address.Hex(), Signature: fmt.Sprintf("0x%x", sig)})
	}))
	defer signerServer.Close()
	signer, err := newForgeSigner(ForgeSigner{Name: "DEV-fixture", URL: signerServer.URL, Address: address.Hex(), Timeout: "2s"})
	if err != nil {
		t.Fatal(err)
	}
	rt := &forgeRuntime{signers: []*forgeSignerClient{signer}, authorized: []common.Address{address}, templateQuorum: 1, templateValidity: time.Minute, clock: func() time.Time { return time.Unix(int64(issued.Timestamp)+20, 0) }}
	if err := rt.signJob(job); err != nil {
		t.Fatal(err)
	}
	proof, err := dgb.BuildOGGProof([]byte{1, 2, 3, 4}, []byte{5, 6, 7, 8}, dgb.Template.CurTime, 0, nil, job.Signed.Template, job.Signed.Signatures)
	if err != nil {
		t.Fatal(err)
	}
	blob, err := forgepool.EncodeOGGProof(proof)
	if err != nil {
		t.Fatal(err)
	}
	scrypt, err := forgepool.ScryptHash(proof.ParentHeader)
	if err != nil {
		t.Fatal(err)
	}
	fixture := map[string]interface{}{"headerRLP": issued.HeaderRLP, "workHash": issued.WorkHash, "proof": hex.EncodeToString(blob),
		"templateDigest": forgepool.TemplateDigest(proof.Template).Hex(), "scryptHash": hex.EncodeToString(scrypt[:]), "signer": address.Hex()}
	// Reproduce the old bug: at submission +20s, old code used ValidFrom +15s.
	// Sign again only to build an explicitly invalid regression fixture.
	proof.Template.ValidFrom = issued.Timestamp + 15
	proof.Template.Expires = issued.Timestamp + 75
	proof.Signatures[0], err = crypto.Sign(forgepool.TemplateDigest(proof.Template).Bytes(), key)
	if err != nil {
		t.Fatal(err)
	}
	late, err := forgepool.EncodeOGGProof(proof)
	if err != nil {
		t.Fatal(err)
	}
	fixture["lateProof"] = hex.EncodeToString(late)
	data, err = json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(output, data, 0600); err != nil {
		t.Fatal(err)
	}
}
