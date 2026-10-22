package forgepool

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"strings"
	"testing"
)

func testTemplate(height int64, withWitness, withMWEB bool) ParentTemplate {
	var prev Hash
	for i := range prev {
		prev[i] = byte(i + 1)
	}
	txs := []TemplateTx{
		{Data: []byte{0x01, 0x02, 0x03}, TxID: doubleSHA256([]byte("tx1"))},
		{Data: []byte{0x04, 0x05}, TxID: doubleSHA256([]byte("tx2"))},
		{Data: []byte{0x06}, TxID: doubleSHA256([]byte("tx3"))},
	}
	tpl := ParentTemplate{
		Version:       0x20000000,
		Previous:      prev,
		Transactions:  txs,
		CoinbaseAux:   map[string][]byte{"flags": {0x51}},
		CoinbaseValue: 5000000000,
		CurTime:       1700000000,
		Bits:          0x1d00ffff,
		Height:        height,
	}
	if withWitness {
		tpl.WitnessCommitment = append([]byte{0x6a, 0x24, 0xaa, 0x21, 0xa9, 0xed}, bytes.Repeat([]byte{0x33}, 32)...)
	}
	if withMWEB {
		tpl.MWEB = []byte{0xca, 0xfe, 0xba, 0xbe}
	}
	return tpl
}

func TestBuildDogeCandidateAuxVersionAndPayout(t *testing.T) {
	tpl := testTemplate(12345, false, false)
	payout := []byte{0x76, 0xa9, 0x14, 1, 2, 3, 0x88, 0xac}
	cand, err := BuildDogeCandidate(tpl, payout, 0x11223344)
	if err != nil {
		t.Fatal(err)
	}
	v := uint32(cand.Version)
	if v&dogeAuxPowFlag == 0 {
		t.Fatal("DOGE AuxPoW flag not set")
	}
	if v>>16 != DOGEAuxChainID {
		t.Fatalf("DOGE chain ID %#x want %#x", v>>16, DOGEAuxChainID)
	}
	if len(cand.Header) != 80 {
		t.Fatalf("DOGE header len %d", len(cand.Header))
	}
	if !bytes.Contains(cand.Coinbase, payout) {
		t.Fatal("DOGE payout script missing from coinbase")
	}
	if cand.DisplayHash != DisplayHash(cand.Hash) {
		t.Fatal("DOGE display hash mismatch")
	}
	if binary.LittleEndian.Uint32(cand.Header[72:76]) != tpl.Bits {
		t.Fatal("DOGE header bits mismatch")
	}
}

func TestBuildDigibyteJobUsesRawOGGWorkHashAndMergedCommitment(t *testing.T) {
	doge, err := BuildDogeCandidate(testTemplate(200, false, false), []byte{0x51}, 7)
	if err != nil {
		t.Fatal(err)
	}
	dgb := testTemplate(300, true, true)
	workBytes := make([]byte, 32)
	for i := range workBytes {
		workBytes[i] = byte(i)
	}
	workHash := "0x" + hex.EncodeToString(workBytes)
	job, err := BuildDigibyteJob(dgb, workHash, doge, []byte{0x51}, 9, 4, 4)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(job.OGGLeaf[:], workBytes) {
		t.Fatalf("OGG work hash byte order changed: %x", job.OGGLeaf)
	}
	wantCommit, oi, di, err := AuxCommitment(job.OGGLeaf, doge.Hash, job.AuxNonce)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(job.Commitment, wantCommit) || job.OGGIndex != oi || job.DOGEIndex != di {
		t.Fatal("merged commitment mismatch")
	}
	e1 := []byte{1, 2, 3, 4}
	e2 := []byte{5, 6, 7, 8}
	coinbase, err := job.Coinbase.BaseCoinbase(e1, e2)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Count(coinbase, MergedMiningMagic) != 1 {
		t.Fatalf("merged-mining magic count %d", bytes.Count(coinbase, MergedMiningMagic))
	}
	header, err := job.Header(e1, e2, dgb.CurTime+1, 0x11223344)
	if err != nil {
		t.Fatal(err)
	}
	if len(header) != 80 {
		t.Fatalf("DGB header len %d", len(header))
	}
	full, err := job.FullBlock(e1, e2, dgb.CurTime+1, 0x11223344)
	if err != nil {
		t.Fatal(err)
	}
	wantSuffix := append([]byte{0x01}, dgb.MWEB...)
	if !bytes.HasSuffix(full, wantSuffix) {
		t.Fatalf("DGB MWEB suffix missing: %x", full[len(full)-len(wantSuffix):])
	}
}

func TestBuildDigibyteJobRejectsMWEBWithoutWitness(t *testing.T) {
	doge, err := BuildDogeCandidate(testTemplate(2, false, false), []byte{0x51}, 1)
	if err != nil {
		t.Fatal(err)
	}
	dgb := testTemplate(3, false, true)
	work := "0x" + strings.Repeat("11", 32)
	if _, err := BuildDigibyteJob(dgb, work, doge, []byte{0x51}, 0, 4, 4); err == nil {
		t.Fatal("accepted MWEB template without witness commitment")
	}
}

func TestDigibyteStratumSplitMatchesHeaderCoinbase(t *testing.T) {
	doge, err := BuildDogeCandidate(testTemplate(2, false, false), []byte{0x51}, 2)
	if err != nil {
		t.Fatal(err)
	}
	dgb := testTemplate(3, true, false)
	job, err := BuildDigibyteJob(dgb, "0x"+strings.Repeat("22", 32), doge, []byte{0x51}, 3, 4, 4)
	if err != nil {
		t.Fatal(err)
	}
	e1 := []byte{0xaa, 0xbb, 0xcc, 0xdd}
	e2 := []byte{0x01, 0x02, 0x03, 0x04}
	base, err := job.Coinbase.BaseCoinbase(e1, e2)
	if err != nil {
		t.Fatal(err)
	}
	joined := append(append(append([]byte{}, job.Coinbase.Coinb1...), e1...), e2...)
	joined = append(joined, job.Coinbase.Coinb2...)
	if !bytes.Equal(joined, base) {
		t.Fatal("Stratum coinbase split does not reconstruct coinbase")
	}
	header, err := job.Header(e1, e2, dgb.CurTime, 0)
	if err != nil {
		t.Fatal(err)
	}
	root := MerkleRootFromBranch(CoinbaseTxID(base), job.MerkleBranch, 0)
	if !bytes.Equal(header[36:68], root[:]) {
		t.Fatal("header Merkle root does not match reconstructed Stratum coinbase")
	}
}
