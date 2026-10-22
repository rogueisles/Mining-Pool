package forgepool

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

func TestBuildOGGProofV2DGBOnlyOmitsDOGE(t *testing.T) {
	parent, err := SyntheticParentTemplate(100, 1700000000)
	if err != nil {
		t.Fatal(err)
	}

	work := "0x1111111111111111111111111111111111111111111111111111111111111111"

	job, err := BuildParentJobV2(
		parent,
		work,
		nil,
		[]byte{0x51},
		7,
		4,
		4,
		true,
	)
	if err != nil {
		t.Fatal(err)
	}

	tmpl := ExternalTemplate{
		Version:     OGGTemplateVersionV2Base | ExternalDGB,
		DGBPrevHash: common.HexToHash("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"),
		DGBBits:     parent.Bits,
		ValidFrom:   1700000000,
		Expires:     1700000060,
	}

	proof, err := job.BuildOGGProof(
		make([]byte, 4),
		make([]byte, 4),
		parent.CurTime,
		0,
		nil,
		tmpl,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}

	if len(proof.DogeHeader) != 0 ||
		len(proof.DogeCoinbase) != 0 ||
		len(proof.DogeMerkleBranch) != 0 ||
		proof.DogeMerkleIndex != 0 {
		t.Fatal("DGB-only V2 proof unexpectedly contains DOGE proof data")
	}
}

func TestBuildOGGProofRejectsParticipationMismatch(t *testing.T) {
	parent, err := SyntheticParentTemplate(100, 1700000000)
	if err != nil {
		t.Fatal(err)
	}

	job, err := BuildParentJobV2(
		parent,
		"0x1111111111111111111111111111111111111111111111111111111111111111",
		nil,
		[]byte{0x51},
		7,
		4,
		4,
		true,
	)
	if err != nil {
		t.Fatal(err)
	}

	tmpl := ExternalTemplate{
		Version:     OGGTemplateVersionV2Base | ExternalDOGE,
		DogeAuxHash: common.HexToHash("0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"),
		ValidFrom:   1700000000,
		Expires:     1700000060,
	}

	if _, err := job.BuildOGGProof(
		make([]byte, 4),
		make([]byte, 4),
		parent.CurTime,
		0,
		nil,
		tmpl,
		nil,
	); err == nil {
		t.Fatal("mismatched runtime/template participation was accepted")
	}
}
