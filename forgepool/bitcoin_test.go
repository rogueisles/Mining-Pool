package forgepool

import (
	"bytes"
	"encoding/hex"
	"testing"
)

func TestBIP34HeightEncoding(t *testing.T) {
	cases := []struct {
		height int64
		hex    string
	}{
		{0, "00"},
		{1, "0101"},
		{127, "017f"},
		{128, "028000"},
		{255, "02ff00"},
		{256, "020001"},
		{500000, "0320a107"},
	}
	for _, tc := range cases {
		got, err := BIP34Height(tc.height)
		if err != nil {
			t.Fatalf("height %d: %v", tc.height, err)
		}
		if hex.EncodeToString(got) != tc.hex {
			t.Fatalf("height %d: got %x want %s", tc.height, got, tc.hex)
		}
	}
}

func TestSortedCoinbaseAuxDeterministic(t *testing.T) {
	aux := map[string][]byte{
		"zz": {0x03},
		"aa": {0x01},
		"mm": {0x02},
	}
	if got := SortedCoinbaseAux(aux); !bytes.Equal(got, []byte{1, 2, 3}) {
		t.Fatalf("sorted aux %x", got)
	}
}

func TestCoinbasePlanSplitAndWitnessSerialization(t *testing.T) {
	witnessCommitment := append([]byte{0x6a, 0x24, 0xaa, 0x21, 0xa9, 0xed}, bytes.Repeat([]byte{0x11}, 32)...)
	plan, err := BuildCoinbasePlan(500000, map[string][]byte{"flags": {0xfa, 0xbe}}, []byte{0xde, 0xad}, []byte{0x51}, 5000000000, witnessCommitment, 4, 4)
	if err != nil {
		t.Fatal(err)
	}
	e1 := []byte{1, 2, 3, 4}
	e2 := []byte{5, 6, 7, 8}
	base, err := plan.BaseCoinbase(e1, e2)
	if err != nil {
		t.Fatal(err)
	}
	joined := append(append(append([]byte{}, plan.Coinb1...), e1...), e2...)
	joined = append(joined, plan.Coinb2...)
	if !bytes.Equal(base, joined) {
		t.Fatal("coinb1/extranonce/coinb2 does not reconstruct base coinbase")
	}
	full, err := plan.FullCoinbase(e1, e2)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(base, full) {
		t.Fatal("witness coinbase unexpectedly equals base serialization")
	}
	if len(full) < 6 || full[4] != 0 || full[5] != 1 {
		t.Fatalf("missing segwit marker/flag: %x", full[:6])
	}
	witnessReserved := append([]byte{0x01, 0x20}, make([]byte, 32)...)
	if !bytes.Contains(full, witnessReserved) {
		t.Fatal("missing 32-byte witness reserved value")
	}
	if !bytes.Contains(base, witnessCommitment) || !bytes.Contains(full, witnessCommitment) {
		t.Fatal("witness commitment output missing")
	}
}

func TestCoinbaseMerkleBranchMatchesFullTree(t *testing.T) {
	coinbase := doubleSHA256([]byte("coinbase"))
	txs := make([]TemplateTx, 0, 7)
	leaves := []Hash{coinbase}
	for i := 0; i < 7; i++ {
		h := doubleSHA256([]byte{byte(i + 1), 0xaa, 0x55})
		txs = append(txs, TemplateTx{TxID: h, Data: []byte{byte(i)}})
		leaves = append(leaves, h)
	}
	branch := CoinbaseMerkleBranch(txs)
	viaBranch := MerkleRootFromBranch(coinbase, branch, 0)
	full, err := MerkleRoot(leaves)
	if err != nil {
		t.Fatal(err)
	}
	if viaBranch != full {
		t.Fatalf("branch root %x != full tree %x", viaBranch, full)
	}
}

func TestSerializeBlockMWEB(t *testing.T) {
	header := make([]byte, 80)
	coinbase := []byte{0xaa, 0xbb}
	txs := []TemplateTx{{Data: []byte{0xcc}, TxID: doubleSHA256([]byte("tx"))}}
	mweb := []byte{0xde, 0xad, 0xbe, 0xef}
	block, err := SerializeBlock(header, coinbase, txs, mweb, true)
	if err != nil {
		t.Fatal(err)
	}
	wantSuffix := append([]byte{0x01}, mweb...)
	if !bytes.HasSuffix(block, wantSuffix) {
		t.Fatalf("MWEB suffix %x want %x", block[len(block)-len(wantSuffix):], wantSuffix)
	}
	if _, err := SerializeBlock(header, coinbase, txs, mweb, false); err == nil {
		t.Fatal("accepted MWEB block without witness commitment")
	}
}
