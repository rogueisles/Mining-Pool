package forgepool

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rlp"
)

type certificateFixture struct {
	Mask                                          uint8
	HeaderRLP, WorkHash, Proof, CertificateDigest string
	Signers                                       []common.Address
	PrivateCPU                                    bool
}

// This independent wire definition avoids importing a second node's runtime
// into the pool. Node interoperability checks decode it as their native Header.
type certificateTestHeader struct {
	ParentHash  common.Hash
	UncleHash   common.Hash
	Coinbase    common.Address
	Root        common.Hash
	TxHash      common.Hash
	ReceiptHash common.Hash
	Bloom       [256]byte
	Difficulty  *big.Int
	Number      *big.Int
	GasLimit    uint64
	GasUsed     uint64
	Time        *big.Int
	Extra       []byte
	MixDigest   common.Hash
	Nonce       [8]byte
}

func (h *certificateTestHeader) Hash() common.Hash {
	data, err := rlp.EncodeToBytes(h)
	if err != nil {
		panic(err)
	}
	return crypto.Keccak256Hash(data)
}

func certificateSignatures(t *testing.T, template MultiTemplate, work common.Hash, indices ...int) ([][]byte, []common.Address) {
	t.Helper()
	digest, err := MultiDigest(template, work)
	if err != nil {
		t.Fatal(err)
	}
	var signatures [][]byte
	var allowed []common.Address
	for i := 1; i <= 3; i++ {
		key, err := crypto.HexToECDSA(fmt.Sprintf("%064x", i))
		if err != nil {
			t.Fatal(err)
		}
		allowed = append(allowed, crypto.PubkeyToAddress(key.PublicKey))
		for _, index := range indices {
			if index == i {
				signature, err := crypto.Sign(digest[:], key)
				if err != nil {
					t.Fatal(err)
				}
				signatures = append(signatures, signature)
			}
		}
	}
	signatures, err = CanonicalMultiSignatures(template, work, signatures)
	if err != nil {
		t.Fatal(err)
	}
	return signatures, allowed
}

func TestV4CertificateBindsExactAuthorizationToParent(t *testing.T) {
	var fixtures []certificateFixture
	for _, mask := range []uint8{1, 2, 3} {
		t.Run(fmt.Sprint(mask), func(t *testing.T) {
			issued := &certificateTestHeader{Number: big.NewInt(1100100), Difficulty: big.NewInt(1), Time: big.NewInt(1700000000), Coinbase: common.HexToAddress("0xbeef"), UncleHash: crypto.Keccak256Hash([]byte{0xc0}), GasLimit: 8000000}
			work := issued.Hash()
			parent := ParentTemplate{Version: 2, Height: 1000, Previous: Hash{0x11}, CurTime: 1700000000, Bits: 0x1d00ffff, CoinbaseValue: 50000000}
			dgb, _ := hex.DecodeString("0014706267a5255e3666936bcb9a777667599f387546")
			doge, _ := hex.DecodeString("76a9144b1508ebe06723842994adf33bdba36b4eba44c488ac")
			aux := map[uint32]*DogeCandidate{}
			if mask&2 != 0 {
				child := parent
				child.Previous = Hash{0x22}
				candidate, err := BuildAuxCandidate(child, doge, 7, 98)
				if err != nil {
					t.Fatal(err)
				}
				aux[98] = candidate
			}
			script := dgb
			if mask&1 == 0 {
				var err error
				parent, err = SyntheticParentTemplate(1, 1700000000)
				if err != nil {
					t.Fatal(err)
				}
				script = []byte{0x6a}
			}
			job, err := BuildMultiParentV4(parent, work.Hex(), aux, script, 7, 4, 4, mask&1 != 0)
			if err != nil {
				t.Fatal(err)
			}
			template, err := job.MultiTemplate(1700000000, 1700000300)
			if err != nil || template.Version != 4 {
				t.Fatal("invalid V4 template", err)
			}
			signatures, allowed := certificateSignatures(t, template, work, 1, 2)
			if err := ValidateMultiSignatures(template, work, signatures, allowed, 2, 1700000000); err != nil {
				t.Fatal(err)
			}
			if _, err := job.BuildMultiProof(make([]byte, 4), make([]byte, 4), parent.CurTime, 0, template, signatures); err == nil {
				t.Fatal("uncommitted certificate emitted")
			}
			if err := job.BindCertificate(template, signatures); err != nil {
				t.Fatal(err)
			}
			rebuilt, err := job.MultiTemplate(1700000000, 1700000300)
			if err != nil || !MultiTemplatesEqual(template, rebuilt) {
				t.Fatal("certificate fill changed signed template", err)
			}
			proof, err := job.BuildMultiProof(make([]byte, 4), make([]byte, 4), parent.CurTime, 0, template, signatures)
			if err != nil {
				t.Fatal(err)
			}
			blob, err := EncodeMultiProof(proof)
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := DecodeOGGProof(blob)
			if err != nil || decoded.Multi == nil || decoded.Multi.Template.Version != 4 {
				t.Fatal("V4 envelope mismatch", err)
			}
			digest, err := MultiCertificateDigest(template, work, signatures)
			if err != nil {
				t.Fatal(err)
			}
			headerRLP, err := rlp.EncodeToBytes(issued)
			if err != nil {
				t.Fatal(err)
			}
			fixtures = append(fixtures, certificateFixture{Mask: mask, HeaderRLP: hex.EncodeToString(headerRLP), WorkHash: work.Hex(), Proof: hex.EncodeToString(blob), CertificateDigest: digest.Hex(), Signers: allowed})
			originalHeader := append([]byte(nil), proof.ParentHeader...)
			for name, changed := range map[string]MultiTemplate{"expiry": func() MultiTemplate { x := template; x.Expires--; return x }(), "minimum time": func() MultiTemplate {
				x := template
				if mask&1 != 0 {
					x.ParentMinTime--
				}
				return x
			}()} {
				if name == "minimum time" && mask&1 == 0 {
					continue
				}
				changedSignatures, _ := certificateSignatures(t, changed, work, 1, 2)
				changedDigest, err := MultiCertificateDigest(changed, work, changedSignatures)
				if err != nil || changedDigest == digest {
					t.Fatalf("%s did not change certificate", name)
				}
			}
			other, _ := certificateSignatures(t, template, work, 2, 3)
			if err := job.CheckCertificate(template, other); err == nil {
				t.Fatal("different signer subset reused parent work")
			}
			if err := job.BindCertificate(template, other); err != nil {
				t.Fatal(err)
			}
			changedHeader, err := job.Header(make([]byte, 4), make([]byte, 4), parent.CurTime, 0)
			if err != nil || bytes.Equal(changedHeader, originalHeader) {
				t.Fatal("new certificate reused identical Scrypt input")
			}
			if err := job.BindCertificate(template, signatures); err != nil {
				t.Fatal(err)
			}
			for name, mutated := range map[string][][]byte{"duplicate": {signatures[0], signatures[0]}, "reordered": {signatures[1], signatures[0]}, "surplus": {signatures[0], signatures[1], signatures[0]}, "invalid": {signatures[0], make([]byte, 65)}} {
				if err := ValidateMultiSignatures(template, work, mutated, allowed, 2, 1700000000); err == nil {
					t.Fatal("accepted " + name + " signatures")
				}
			}
			job.Coinbase.Outputs = append(job.Coinbase.Outputs, TxOutput{Value: 0, Script: certificateScript(common.Hash{})})
			if _, err := job.MultiTemplate(1700000000, 1700000300); err == nil {
				t.Fatal("ambiguous certificate output accepted")
			}
		})
	}
	if path := os.Getenv("FORGE_V4_FIXTURE_OUTPUT"); path != "" {
		data, err := json.MarshalIndent(fixtures, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append(data, '\n'), 0644); err != nil {
			t.Fatal(err)
		}
	}
}
