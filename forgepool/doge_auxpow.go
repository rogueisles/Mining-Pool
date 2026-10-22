package forgepool

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
)

// DogeAuxPow is the exact Dogecoin/Namecoin CAuxPow wire object.  Hashes are
// stored in Bitcoin's internal byte order and indexes are signed int32 values
// on the wire, matching Dogecoin Core's CDataStream serialization.
type DogeAuxPow struct {
	ParentCoinbase     []byte
	ParentBlockHash    Hash
	ParentMerkleBranch []Hash
	ParentMerkleIndex  uint32
	ChainMerkleBranch  []Hash
	ChainMerkleIndex   uint32
	ParentHeader       []byte
}

func writeHashVector(b *bytes.Buffer, branch []Hash) {
	writeVarInt(b, uint64(len(branch)))
	for _, h := range branch {
		b.Write(h[:])
	}
}

// Serialize returns the bytes accepted by Dogecoin getauxblock/submitauxblock.
func (p *DogeAuxPow) Serialize() ([]byte, error) {
	if p == nil {
		return nil, errors.New("nil Dogecoin AuxPoW")
	}
	if len(p.ParentCoinbase) == 0 {
		return nil, errors.New("missing parent coinbase")
	}
	if len(p.ParentHeader) != 80 {
		return nil, fmt.Errorf("invalid parent header length %d", len(p.ParentHeader))
	}
	if len(p.ChainMerkleBranch) > 30 {
		return nil, errors.New("DOGE chain Merkle branch exceeds 30 levels")
	}
	if p.ParentMerkleIndex > uint32(^uint32(0)>>1) || p.ChainMerkleIndex > uint32(^uint32(0)>>1) {
		return nil, errors.New("AuxPoW index exceeds signed int32")
	}
	var b bytes.Buffer
	b.Write(p.ParentCoinbase)
	b.Write(p.ParentBlockHash[:])
	writeHashVector(&b, p.ParentMerkleBranch)
	_ = binary.Write(&b, binary.LittleEndian, int32(p.ParentMerkleIndex))
	writeHashVector(&b, p.ChainMerkleBranch)
	_ = binary.Write(&b, binary.LittleEndian, int32(p.ChainMerkleIndex))
	b.Write(p.ParentHeader)
	return b.Bytes(), nil
}

// BuildDogeAuxPow binds the solved Digibyte parent to the exact DOGE child
// candidate committed in its coinbase.  DOGE's chain branch contains the OGG
// sibling because the fixed FORGE tree has two leaves.
func (j *DigibyteJob) BuildDogeAuxPow(extra1, extra2 []byte, ntime, nonce uint32) (*DogeAuxPow, error) {
	if j != nil && j.AuxTree != nil {
		return j.AuxPowFor(DOGEAuxChainID, extra1, extra2, ntime, nonce)
	}
	if j == nil || j.DOGE == nil {
		return nil, errors.New("missing merged-mining job")
	}
	header, err := j.Header(extra1, extra2, ntime, nonce)
	if err != nil {
		return nil, err
	}
	coinbase, err := j.Coinbase.BaseCoinbase(extra1, extra2)
	if err != nil {
		return nil, err
	}
	return &DogeAuxPow{
		ParentCoinbase: coinbase,
		// CMerkleTx::InitMerkleBranch stores GetHash() as uint256; its wire
		// representation is the raw double-SHA256 digest, not display order.
		// Dogecoin does not use this legacy field when checking AuxPoW.
		ParentBlockHash:    doubleSHA256(header),
		ParentMerkleBranch: append([]Hash(nil), j.MerkleBranch...),
		ParentMerkleIndex:  0,
		ChainMerkleBranch:  []Hash{j.OGGLeaf},
		ChainMerkleIndex:   j.DOGEIndex,
		ParentHeader:       append([]byte(nil), header...),
	}, nil
}

// DogeAuxPowBlock serializes a complete AuxPoW DOGE block for submitblock:
// child header, CAuxPow proof, then the child transaction vector.
func (j *DigibyteJob) DogeAuxPowBlock(extra1, extra2 []byte, ntime, nonce uint32) ([]byte, error) {
	if j != nil && j.AuxTree != nil {
		return j.AuxBlockFor(DOGEAuxChainID, extra1, extra2, ntime, nonce)
	}
	p, err := j.BuildDogeAuxPow(extra1, extra2, ntime, nonce)
	if err != nil {
		return nil, err
	}
	proof, err := p.Serialize()
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	b.Write(j.DOGE.Header)
	b.Write(proof)
	writeVarInt(&b, uint64(1+len(j.DOGE.Template.Transactions)))
	b.Write(j.DOGE.Coinbase)
	for _, tx := range j.DOGE.Template.Transactions {
		b.Write(tx.Data)
	}
	return b.Bytes(), nil
}

// ValidateDogeAuxPow performs the binding checks the pool can assert before
// sending a proof to a daemon.  Dogecoin Core remains the final authority.
func (j *DigibyteJob) ValidateDogeAuxPow(p *DogeAuxPow) error {
	if j != nil && j.AuxTree != nil {
		return j.validateExpandedAux(p, DOGEAuxChainID)
	}
	if j == nil || j.DOGE == nil || j.Coinbase == nil || p == nil {
		return errors.New("missing Dogecoin AuxPoW data")
	}
	if len(j.DOGE.Header) != 80 || doubleSHA256(j.DOGE.Header) != j.DOGE.Hash {
		return errors.New("DOGE candidate hash does not match header")
	}
	commitment, _, index, err := AuxCommitment(j.OGGLeaf, j.DOGE.Hash, j.AuxNonce)
	if err != nil || index != j.DOGEIndex || !bytes.Equal(commitment, j.Commitment) {
		return errors.New("DOGE candidate does not match job commitment")
	}
	if p.ParentMerkleIndex != 0 {
		return errors.New("parent coinbase Merkle index must be zero")
	}
	if p.ChainMerkleIndex != j.DOGEIndex {
		return errors.New("wrong DOGE chain Merkle index")
	}
	if len(p.ChainMerkleBranch) != 1 || p.ChainMerkleBranch[0] != j.OGGLeaf {
		return errors.New("wrong DOGE chain Merkle branch")
	}
	if len(p.ParentHeader) != 80 || p.ParentBlockHash != doubleSHA256(p.ParentHeader) {
		return errors.New("invalid parent proof")
	}
	if binary.LittleEndian.Uint32(p.ParentHeader[:4])>>16 == DOGEAuxChainID {
		return errors.New("parent has DOGE chain ID")
	}
	parentID := CoinbaseTxID(p.ParentCoinbase)
	root := MerkleRootFromBranch(parentID, p.ParentMerkleBranch, p.ParentMerkleIndex)
	var inHeader Hash
	copy(inHeader[:], p.ParentHeader[36:68])
	if root != inHeader {
		return errors.New("invalid parent coinbase Merkle proof")
	}
	// This pool emits one-input base coinbases with a 2..100-byte scriptSig.
	// Match the immutable split bytes and exact extranonce hole before reading
	// that script; a commitment in an output or a second transaction is invalid.
	plan := j.Coinbase
	if len(p.ParentCoinbase) != len(plan.Coinb1)+plan.ExtraNonce1Size+plan.ExtraNonce2Size+len(plan.Coinb2) ||
		!bytes.HasPrefix(p.ParentCoinbase, plan.Coinb1) || !bytes.HasSuffix(p.ParentCoinbase, plan.Coinb2) || len(p.ParentCoinbase) < 42 || p.ParentCoinbase[4] != 1 {
		return errors.New("parent coinbase differs from issued job")
	}
	scriptLen := int(p.ParentCoinbase[41])
	if scriptLen < 2 || scriptLen > 100 || len(p.ParentCoinbase) < 42+scriptLen {
		return errors.New("invalid parent coinbase script length")
	}
	script := p.ParentCoinbase[42 : 42+scriptLen]
	if bytes.Count(script, MergedMiningMagic) != 1 || !bytes.Contains(script, commitment) {
		return errors.New("wrong merged-mining coinbase commitment")
	}
	return nil
}
