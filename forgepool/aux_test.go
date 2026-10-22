package forgepool

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestAuxSlotsNeverCollideForOGGAndDOGE(t *testing.T) {
	for nonce := uint32(0); nonce < 100000; nonce++ {
		oi := ExpectedChainIndex(nonce, OGGAuxChainID, AuxMerkleSize)
		di := ExpectedChainIndex(nonce, DOGEAuxChainID, AuxMerkleSize)
		if oi == di {
			t.Fatalf("slot collision at nonce %d", nonce)
		}
	}
}

func TestAuxCommitmentLayoutAndDeterminism(t *testing.T) {
	var ogg, doge Hash
	ogg[0] = 1
	doge[0] = 2
	nonce := DeterministicAuxNonce(7)
	a, oi, di, err := AuxCommitment(ogg, doge, nonce)
	if err != nil {
		t.Fatal(err)
	}
	b, oi2, di2, err := AuxCommitment(ogg, doge, nonce)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) || oi != oi2 || di != di2 {
		t.Fatal("aux commitment is not deterministic")
	}
	if len(a) != 44 {
		t.Fatalf("commitment length %d want 44", len(a))
	}
	if !bytes.Equal(a[:4], MergedMiningMagic) {
		t.Fatal("missing merged-mining magic")
	}
	if got := binary.LittleEndian.Uint32(a[36:40]); got != 2 {
		t.Fatalf("tree size %d want 2", got)
	}
	if got := binary.LittleEndian.Uint32(a[40:44]); got != nonce {
		t.Fatalf("nonce %d want %d", got, nonce)
	}
}

func TestScriptHashStable(t *testing.T) {
	a := ScriptHash([]byte{0x51})
	b := ScriptHash([]byte{0x51})
	if a != b {
		t.Fatal("script hash not deterministic")
	}
	if a == ScriptHash([]byte{0x52}) {
		t.Fatal("distinct scripts have identical test hash")
	}
}
