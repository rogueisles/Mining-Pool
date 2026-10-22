package forgepool

import (
	"bytes"
	"testing"
)

func rebindParent(p *DogeAuxPow) {
	root := MerkleRootFromBranch(CoinbaseTxID(p.ParentCoinbase), p.ParentMerkleBranch, 0)
	copy(p.ParentHeader[36:68], root[:])
	p.ParentBlockHash = doubleSHA256(p.ParentHeader)
}

func TestDogeCommitmentMustBeUniqueInIssuedCoinbaseInput(t *testing.T) {
	for _, name := range []string{"output-only", "duplicate-magic", "changed-child", "changed-cached-commitment"} {
		t.Run(name, func(t *testing.T) {
			job := phase5DJob(t)
			p, err := job.BuildDogeAuxPow([]byte{1, 2, 3, 4}, []byte{5, 6, 7, 8}, job.Template.CurTime, 1)
			if err != nil {
				t.Fatal(err)
			}
			switch name {
			case "output-only":
				// Keep a self-consistent Merkle proof and put the right commitment
				// into an output. The old whole-transaction Contains check passed.
				plan, err := BuildCoinbasePlan(job.Template.Height, nil, nil, job.Commitment, 1, nil, 4, 4)
				if err != nil {
					t.Fatal(err)
				}
				p.ParentCoinbase, err = plan.BaseCoinbase([]byte{1, 2, 3, 4}, []byte{5, 6, 7, 8})
				if err != nil {
					t.Fatal(err)
				}
			case "duplicate-magic":
				copy(p.ParentCoinbase[len(job.Coinbase.Coinb1):], MergedMiningMagic)
			case "changed-child":
				job.DOGE.Hash[0] ^= 1
			case "changed-cached-commitment":
				i := bytes.Index(p.ParentCoinbase, job.Commitment)
				p.ParentCoinbase[i+4] ^= 1
				job.Commitment[4] ^= 1
			}
			rebindParent(p)
			if err := job.ValidateDogeAuxPow(p); err == nil {
				t.Fatal("accepted malformed binding")
			}
		})
	}
}

func TestDogeLegacyHashFieldUsesUint256WireOrder(t *testing.T) {
	job := phase5DJob(t)
	p, err := job.BuildDogeAuxPow([]byte{1, 2, 3, 4}, []byte{5, 6, 7, 8}, job.Template.CurTime, 1)
	if err != nil {
		t.Fatal(err)
	}
	if p.ParentBlockHash != doubleSHA256(p.ParentHeader) {
		t.Fatal("legacy field is in display byte order")
	}
	p.ChainMerkleBranch = make([]Hash, 31)
	if _, err := p.Serialize(); err == nil {
		t.Fatal("oversized DOGE branch serialized")
	}
}
