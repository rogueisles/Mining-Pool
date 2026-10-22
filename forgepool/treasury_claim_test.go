package forgepool

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"io"
	"math"
	"reflect"
	"testing"

	"github.com/btcsuite/btcd/wire"
)

func claimTransactions(t *testing.T, block []byte, auxiliary bool) []*wire.MsgTx {
	t.Helper()
	r := bytes.NewReader(block[80:])
	if auxiliary {
		var parent wire.MsgTx
		if err := parent.Deserialize(r); err != nil {
			t.Fatal(err)
		}
		if _, err := r.Seek(32, io.SeekCurrent); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 2; i++ {
			n, err := wire.ReadVarInt(r, 0)
			if err != nil || n > 30 {
				t.Fatal("invalid auxiliary branch", err)
			}
			if _, err := r.Seek(int64(n*32+4), io.SeekCurrent); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := r.Seek(80, io.SeekCurrent); err != nil {
			t.Fatal(err)
		}
	}
	n, err := wire.ReadVarInt(r, 0)
	if err != nil || n < 1 || n > 100 {
		t.Fatal("invalid fixture transaction count", err)
	}
	txs := make([]*wire.MsgTx, n)
	var leaves []Hash
	for i := range txs {
		tx := wire.NewMsgTx(2)
		if err := tx.Deserialize(r); err != nil {
			t.Fatal(err)
		}
		txs[i] = tx
		h := tx.TxHash()
		var leaf Hash
		copy(leaf[:], h[:])
		leaves = append(leaves, leaf)
	}
	if r.Len() != 0 {
		t.Fatal("unparsed native block bytes")
	}
	for len(leaves) > 1 {
		var next []Hash
		for i := 0; i < len(leaves); i += 2 {
			other := i + 1
			if other == len(leaves) {
				other = i
			}
			next = append(next, doubleSHA256(append(append([]byte(nil), leaves[i][:]...), leaves[other][:]...)))
		}
		leaves = next
	}
	if !bytes.Equal(leaves[0][:], block[36:68]) {
		t.Fatal("native Merkle root not rebuilt")
	}
	return txs
}

func TestTreasuryClaimProbeChangesOnlyAmountAndDerivedHashes(t *testing.T) {
	ordinary := wire.NewMsgTx(2)
	ordinary.AddTxIn(wire.NewTxIn(&wire.OutPoint{Index: 3}, []byte{0x51}, nil))
	ordinary.AddTxOut(wire.NewTxOut(17, []byte{0x51}))
	var serialized bytes.Buffer
	if err := ordinary.Serialize(&serialized); err != nil {
		t.Fatal(err)
	}
	txhash := ordinary.TxHash()
	var id Hash
	copy(id[:], txhash[:])
	for _, auxiliary := range []bool{false, true} {
		for _, witness := range []bool{false, true} {
			t.Run(fmt.Sprintf("aux-%v-witness-%v", auxiliary, witness), func(t *testing.T) {
				p := ParentTemplate{Version: 4, Height: 300001, CurTime: 1700000000, Bits: 0x207fffff, CoinbaseValue: 1000000123,
					Transactions: []TemplateTx{{Data: serialized.Bytes(), TxID: id}}}
				if witness {
					root := doubleSHA256(append(make([]byte, 32), id[:]...))
					digest := doubleSHA256(append(append([]byte(nil), root[:]...), make([]byte, 32)...))
					p.WitnessCommitment = append([]byte{0x6a, 0x24, 0xaa, 0x21, 0xa9, 0xed}, digest[:]...)
				}
				script, _ := hex.DecodeString("76a9144b1508ebe06723842994adf33bdba36b4eba44c488ac")
				var original, probe, after []byte
				var err error
				if auxiliary {
					candidate, e := BuildAuxCandidate(p, script, 9, 98)
					if e != nil {
						t.Fatal(e)
					}
					original, err = candidate.AuxValidationBlock()
					if err != nil {
						t.Fatal(err)
					}
					probe, err = candidate.TreasuryClaimProbe(98, script)
					if err != nil {
						t.Fatal(err)
					}
					after, err = candidate.AuxValidationBlock()
				} else {
					job, e := BuildMultiParentV4(p, "0x0000000000000000000000000000000000000000000000000000000000000001", nil, script, 9, 4, 4, true)
					if e != nil {
						t.Fatal(e)
					}
					original, err = job.FullBlock(make([]byte, 4), make([]byte, 4), p.CurTime, 0)
					if err != nil {
						t.Fatal(err)
					}
					probe, err = job.TreasuryClaimProbe(make([]byte, 4), make([]byte, 4), p.CurTime, 0)
					if err != nil {
						t.Fatal(err)
					}
					after, err = job.FullBlock(make([]byte, 4), make([]byte, 4), p.CurTime, 0)
				}
				if err != nil || !bytes.Equal(original, after) {
					t.Fatal("original candidate changed", err)
				}
				a, b := claimTransactions(t, original, auxiliary), claimTransactions(t, probe, auxiliary)
				if len(a) != 2 || len(b) != 2 || b[0].TxOut[0].Value != a[0].TxOut[0].Value+1 {
					t.Fatal("incorrect claim increment")
				}
				b[0].TxOut[0].Value--
				if !reflect.DeepEqual(a, b) {
					t.Fatal("probe modified transactions or other coinbase fields")
				}
				if !bytes.Equal(original[:36], probe[:36]) || !bytes.Equal(original[68:80], probe[68:80]) {
					t.Fatal("native header context changed")
				}
			})
		}
	}
}

func TestTreasuryClaimProbeRejectsNonpositiveAndOverflow(t *testing.T) {
	for _, value := range []int64{-1, 0, math.MaxInt64} {
		candidate := &DogeCandidate{Template: ParentTemplate{CoinbaseValue: value}}
		if _, err := candidate.TreasuryClaimProbe(98, []byte{0x51}); err == nil {
			t.Fatal("invalid auxiliary claim accepted", value)
		}
		job := &DigibyteJob{DigibyteActive: true, Template: ParentTemplate{CoinbaseValue: value}, Coinbase: &CoinbasePlan{Outputs: []TxOutput{{Value: value}}}}
		if _, err := job.TreasuryClaimProbe(nil, nil, 1, 0); err == nil {
			t.Fatal("invalid parent claim accepted", value)
		}
	}
	var candidate *DogeCandidate
	if _, err := candidate.TreasuryClaimProbe(98, nil); err == nil {
		t.Fatal("nil auxiliary accepted")
	}
	var job *DigibyteJob
	if _, err := job.TreasuryClaimProbe(nil, nil, 1, 0); err == nil {
		t.Fatal("nil parent accepted")
	}
	job = &DigibyteJob{DigibyteActive: false}
	if _, err := job.TreasuryClaimProbe(nil, nil, 1, 0); err == nil {
		t.Fatal("synthetic carrier treated as native reward")
	}
}
