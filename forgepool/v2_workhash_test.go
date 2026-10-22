package forgepool

import (
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

func TestV2SigningDigestBindsExactOGGWorkHash(t *testing.T) {
	tmpl := ExternalTemplate{
		Version:     OGGTemplateVersionV2Base | ExternalDGB,
		DGBPrevHash: common.HexToHash("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"),
		DGBBits:     0x1d00ffff,
		ValidFrom:   1700000000,
		Expires:     1700000060,
	}

	workA := common.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111")
	workB := common.HexToHash("0x2222222222222222222222222222222222222222222222222222222222222222")

	digestA, err := TemplateSigningDigest(tmpl, workA)
	if err != nil {
		t.Fatal(err)
	}
	digestB, err := TemplateSigningDigest(tmpl, workB)
	if err != nil {
		t.Fatal(err)
	}

	if digestA == digestB {
		t.Fatal("different OGG WorkHash values produced identical V2 signing digest")
	}

	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}

	sig, err := crypto.Sign(digestA.Bytes(), key)
	if err != nil {
		t.Fatal(err)
	}

	addr := crypto.PubkeyToAddress(key.PublicKey)
	at := time.Unix(1700000001, 0)

	if err := ValidateTemplateSignaturesForWork(
		tmpl,
		workA,
		[][]byte{sig},
		[]common.Address{addr},
		1,
		at,
	); err != nil {
		t.Fatalf("correct WorkHash rejected: %v", err)
	}

	if err := ValidateTemplateSignaturesForWork(
		tmpl,
		workB,
		[][]byte{sig},
		[]common.Address{addr},
		1,
		at,
	); err == nil {
		t.Fatal("signature was replayable onto different OGG WorkHash")
	}
}

func TestV2SigningDigestRejectsZeroWorkHash(t *testing.T) {
	tmpl := ExternalTemplate{
		Version:     OGGTemplateVersionV2Base | ExternalDGB,
		DGBPrevHash: common.HexToHash("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"),
		DGBBits:     0x1d00ffff,
		ValidFrom:   1700000000,
		Expires:     1700000060,
	}

	if _, err := TemplateSigningDigest(tmpl, common.Hash{}); err == nil {
		t.Fatal("zero OGG WorkHash accepted for V2")
	}
}

func TestV1DigestRemainsLegacyCompatible(t *testing.T) {
	tmpl := ExternalTemplate{
		Version:     OGGTemplateVersion,
		DGBPrevHash: common.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111"),
		DGBBits:     0x1d00ffff,
		DogeAuxHash: common.HexToHash("0x2222222222222222222222222222222222222222222222222222222222222222"),
		ValidFrom:   1700000000,
		Expires:     1700000060,
	}

	got, err := TemplateSigningDigest(tmpl, common.Hash{})
	if err != nil {
		t.Fatal(err)
	}
	if got != TemplateDigest(tmpl) {
		t.Fatal("V1 signing digest changed")
	}
}
