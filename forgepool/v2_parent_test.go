package forgepool

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"
)

func testV2WorkHash() string {
	return "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20"
}

func TestAuxCommitmentV2OGGOnly(t *testing.T) {
	ogg, err := ParseRawHashHex(testV2WorkHash())
	if err != nil {
		t.Fatal(err)
	}

	got, oi, di, err := AuxCommitmentV2(ogg, nil, 123)
	if err != nil {
		t.Fatal(err)
	}

	if len(got) != 44 {
		t.Fatalf("commitment length=%d want 44", len(got))
	}
	if !bytes.Equal(got[:4], MergedMiningMagic) {
		t.Fatal("missing merged-mining magic")
	}

	wantRoot := reverseHash(ogg)
	if !bytes.Equal(got[4:36], wantRoot[:]) {
		t.Fatal("OGG-only commitment root mismatch")
	}
	if binary.LittleEndian.Uint32(got[36:40]) != 1 {
		t.Fatal("OGG-only commitment must use tree size 1")
	}
	if binary.LittleEndian.Uint32(got[40:44]) != 123 {
		t.Fatal("OGG-only commitment nonce mismatch")
	}
	if oi != 0 || di != 0 {
		t.Fatalf("unexpected single-leaf indexes %d/%d", oi, di)
	}
}

func TestBuildParentJobV2DigibyteWithoutDOGE(t *testing.T) {
	tpl := ParentTemplate{
		Version:       1,
		CoinbaseValue: 5000000000,
		CurTime:       1700000000,
		Bits:          0x1d00ffff,
		Height:        100,
	}

	job, err := BuildParentJobV2(
		tpl,
		testV2WorkHash(),
		nil,
		[]byte{0x51},
		5,
		4,
		4,
		true,
	)
	if err != nil {
		t.Fatal(err)
	}

	if !job.DigibyteActive {
		t.Fatal("real Digibyte parent marked inactive")
	}
	if job.DOGE != nil {
		t.Fatal("unexpected DOGE candidate")
	}
	if binary.LittleEndian.Uint32(job.Commitment[36:40]) != 1 {
		t.Fatal("DGB-only parent must contain one-leaf OGG commitment")
	}

	header, err := job.Header(
		make([]byte, 4),
		make([]byte, 4),
		tpl.CurTime,
		0,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(header) != 80 {
		t.Fatalf("header length=%d want 80", len(header))
	}
}

func TestSyntheticParentCannotBecomeDigibyteSubmission(t *testing.T) {
	tpl, err := SyntheticParentTemplate(123, 1700000000)
	if err != nil {
		t.Fatal(err)
	}

	job, err := BuildParentJobV2(
		tpl,
		testV2WorkHash(),
		nil,
		[]byte{0x51},
		9,
		4,
		4,
		false,
	)
	if err != nil {
		t.Fatal(err)
	}

	if job.DigibyteActive {
		t.Fatal("synthetic parent marked as Digibyte-active")
	}
	if job.Template.Previous != (Hash{}) {
		t.Fatal("synthetic previous hash is not zero")
	}
	if job.Template.CoinbaseValue != 0 {
		t.Fatal("synthetic coinbase unexpectedly has value")
	}

	header, err := job.Header(
		make([]byte, 4),
		make([]byte, 4),
		tpl.CurTime,
		0,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(header) != 80 {
		t.Fatalf("synthetic header length=%d want 80", len(header))
	}

	if _, err := job.FullBlock(
		make([]byte, 4),
		make([]byte, 4),
		tpl.CurTime,
		0,
	); err == nil || !strings.Contains(err.Error(), "cannot be submitted") {
		t.Fatalf("synthetic parent submission did not fail closed: %v", err)
	}
}

func TestSyntheticParentWithDOGEUsesTwoLeafTree(t *testing.T) {
	dogeTpl := ParentTemplate{
		Version:       1,
		CoinbaseValue: 100000000,
		CurTime:       1700000000,
		Bits:          0x1d00ffff,
		Height:        200,
	}

	doge, err := BuildDogeCandidate(dogeTpl, []byte{0x51}, 77)
	if err != nil {
		t.Fatal(err)
	}

	parentTpl, err := SyntheticParentTemplate(123, 1700000000)
	if err != nil {
		t.Fatal(err)
	}

	job, err := BuildParentJobV2(
		parentTpl,
		testV2WorkHash(),
		doge,
		[]byte{0x51},
		11,
		4,
		4,
		false,
	)
	if err != nil {
		t.Fatal(err)
	}

	if job.DigibyteActive {
		t.Fatal("synthetic DOGE parent marked Digibyte-active")
	}
	if job.DOGE == nil {
		t.Fatal("DOGE candidate missing")
	}
	if binary.LittleEndian.Uint32(job.Commitment[36:40]) != 2 {
		t.Fatal("OGG+DOGE synthetic parent must use two-leaf tree")
	}
}
