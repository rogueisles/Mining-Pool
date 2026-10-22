package forgepool

import (
	"bytes"
	"errors"
)

// ValidateDogeProposalSize bounds untrusted work BEFORE constructing Merkle
// trees or serializing a block. The daemon remains the consensus validator.
func ValidateDogeProposalSize(t ParentTemplate) error {
	if len(t.Transactions) > 10000 || len(t.CoinbaseAux) > 100 || len(t.WitnessCommitment) != 0 || len(t.MWEB) != 0 {
		return errors.New("unsupported or oversized DOGE proposal")
	}
	size := 0
	for _, tx := range t.Transactions {
		if len(tx.Data) == 0 || len(tx.Data) > MaxDogeProposalBytes-size {
			return errors.New("invalid DOGE proposal transaction size")
		}
		size += len(tx.Data)
	}
	size = 0
	for _, flags := range t.CoinbaseAux {
		if len(flags) > 100-size {
			return errors.New("oversized coinbase flags")
		}
		size += len(flags)
	}
	return nil
}

// ValidationBlock serializes an unmined candidate for BIP23 mode=proposal ONLY.
// The placeholder AuxPoW is structurally decodable, not valid mining evidence.
// The independent daemon checks the body and contextual consensus with PoW
// disabled as defined by its proposal RPC. Never submit this as a mined block.
func (d *DogeCandidate) ValidationBlock() ([]byte, error) {
	if d == nil || len(d.Header) != 80 || len(d.Coinbase) == 0 {
		return nil, errors.New("missing DOGE candidate")
	}
	if err := ValidateDogeProposalSize(d.Template); err != nil {
		return nil, err
	}
	placeholder := &DogeAuxPow{ParentCoinbase: d.Coinbase, ParentHeader: make([]byte, 80)}
	aux, err := placeholder.Serialize()
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	b.Write(d.Header)
	b.Write(aux)
	writeVarInt(&b, uint64(1+len(d.Template.Transactions)))
	b.Write(d.Coinbase)
	for _, tx := range d.Template.Transactions {
		b.Write(tx.Data)
	}
	if b.Len() > MaxDogeProposalBytes {
		return nil, errors.New("DOGE proposal block too large")
	}
	return b.Bytes(), nil
}
