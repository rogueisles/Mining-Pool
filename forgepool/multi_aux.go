package forgepool

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
)

// AuxTree is immutable after construction. Empty slots have a zero leaf.
// It contains one OGG work leaf and any compatible external auxiliary leaves.
type AuxTree struct {
	Size     uint32
	Nonce    uint32
	Root     Hash
	Indices  map[uint32]uint32
	Branches map[uint32][]Hash
}

func NewAuxTree(leaves map[uint32]Hash, nonce uint32) (*AuxTree, error) {
	if len(leaves) < 1 || len(leaves) > 16 {
		return nil, errors.New("auxiliary tree requires 1..16 leaves")
	}
	var size uint32 = 2
	var indices map[uint32]uint32
	for ; size <= 65536; size <<= 1 {
		slots := map[uint32]bool{}
		indices = map[uint32]uint32{}
		for id, h := range leaves {
			if id == 0 || h == (Hash{}) {
				return nil, errors.New("zero auxiliary identity or leaf")
			}
			i := ExpectedChainIndex(nonce, id, size)
			if slots[i] {
				break
			}
			slots[i] = true
			indices[id] = i
		}
		if len(indices) == len(leaves) {
			break
		}
	}
	if size > 65536 {
		return nil, errors.New("auxiliary chain IDs cannot occupy distinct bounded slots")
	}
	level := make([]Hash, size)
	for id, h := range leaves {
		level[indices[id]] = h
	}
	tree := &AuxTree{Size: size, Nonce: nonce, Indices: indices, Branches: map[uint32][]Hash{}}
	for depth := uint32(0); len(level) > 1; depth++ {
		for id, index := range indices {
			tree.Branches[id] = append(tree.Branches[id], level[(index>>depth)^1])
		}
		next := make([]Hash, len(level)/2)
		for i := range next {
			next[i] = doubleSHA256(append(append([]byte{}, level[2*i][:]...), level[2*i+1][:]...))
		}
		level = next
	}
	tree.Root = level[0]
	return tree, nil
}

func (t *AuxTree) Commitment() []byte {
	reversed := reverseHash(t.Root)
	out := append(append([]byte{}, MergedMiningMagic...), reversed[:]...)
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], t.Size)
	out = append(out, b[:]...)
	binary.LittleEndian.PutUint32(b[:], t.Nonce)
	return append(out, b[:]...)
}

func BuildAuxCandidate(tpl ParentTemplate, script []byte, nonce, chainID uint32) (*DogeCandidate, error) {
	if chainID == 0 || chainID > 65535 || chainID == OGGAuxChainID {
		return nil, errors.New("invalid auxiliary chain ID")
	}
	c, err := BuildDogeCandidate(tpl, script, nonce)
	if err != nil {
		return nil, err
	}
	c.Version = int32((uint32(tpl.Version) & 0xffff) | dogeAuxPowFlag | (chainID << 16))
	c.Header = SerializeHeader(c.Version, tpl.Previous, c.MerkleRoot, tpl.CurTime, tpl.Bits, 0)
	c.Hash = doubleSHA256(c.Header)
	c.DisplayHash = DisplayHash(c.Hash)
	return c, nil
}

// SetAdditionalAux rebuilds the immutable parent coinbase before a job is signed
// or published. The signed OGG and DOGE leaves retain their original meaning.
func (j *DigibyteJob) SetAdditionalAux(extra map[uint32]*DogeCandidate) error {
	if j == nil || j.DOGE == nil || !j.DigibyteActive || len(extra) < 1 || len(extra) > 8 {
		return errors.New("additional aux requires DGB and DOGE")
	}
	leaves := map[uint32]Hash{OGGAuxChainID: j.OGGLeaf, DOGEAuxChainID: j.DOGE.Hash}
	copied := map[uint32]*DogeCandidate{}
	for id, c := range extra {
		if c == nil || id == OGGAuxChainID || id == DOGEAuxChainID || id == 0 || uint32(c.Version)>>16 != id {
			return errors.New("duplicate or mismatched auxiliary identity")
		}
		leaves[id] = c.Hash
		copied[id] = c
	}
	tree, err := NewAuxTree(leaves, j.AuxNonce)
	if err != nil {
		return err
	}
	commitment := tree.Commitment()
	plan, err := BuildCoinbasePlan(j.Template.Height, j.Template.CoinbaseAux, commitment, j.PayoutScript, j.Template.CoinbaseValue,
		j.Template.WitnessCommitment, j.ExtraNonce1Size, j.ExtraNonce2Size)
	if err != nil {
		return err
	}
	j.AuxTree = tree
	j.AdditionalAux = copied
	j.Commitment = commitment
	j.Coinbase = plan
	j.OGGIndex = tree.Indices[OGGAuxChainID]
	j.DOGEIndex = tree.Indices[DOGEAuxChainID]
	return nil
}

func (j *DigibyteJob) AuxPowFor(chainID uint32, extra1, extra2 []byte, ntime, nonce uint32) (*DogeAuxPow, error) {
	if j == nil || j.AuxTree == nil {
		return nil, errors.New("missing expanded auxiliary tree")
	}
	branch, ok := j.AuxTree.Branches[chainID]
	if !ok {
		return nil, errors.New("chain absent from auxiliary tree")
	}
	header, err := j.Header(extra1, extra2, ntime, nonce)
	if err != nil {
		return nil, err
	}
	cb, err := j.Coinbase.BaseCoinbase(extra1, extra2)
	if err != nil {
		return nil, err
	}
	return &DogeAuxPow{ParentCoinbase: cb, ParentBlockHash: doubleSHA256(header), ParentMerkleBranch: append([]Hash{}, j.MerkleBranch...),
		ParentMerkleIndex: 0, ChainMerkleBranch: append([]Hash{}, branch...), ChainMerkleIndex: j.AuxTree.Indices[chainID], ParentHeader: header}, nil
}

func (j *DigibyteJob) AuxBlockFor(chainID uint32, extra1, extra2 []byte, ntime, nonce uint32) ([]byte, error) {
	c := j.AdditionalAux[chainID]
	if chainID == DOGEAuxChainID {
		c = j.DOGE
	}
	if c == nil {
		return nil, errors.New("missing auxiliary candidate")
	}
	proof, err := j.AuxPowFor(chainID, extra1, extra2, ntime, nonce)
	if err != nil {
		return nil, err
	}
	wire, err := proof.Serialize()
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	out.Write(c.Header)
	out.Write(wire)
	writeVarInt(&out, uint64(len(c.Template.Transactions)+1))
	if len(c.FullCoinbase) > 0 {
		out.Write(c.FullCoinbase)
	} else {
		out.Write(c.Coinbase)
	}
	for _, tx := range c.Template.Transactions {
		out.Write(tx.Data)
	}
	return out.Bytes(), nil
}

func (j *DigibyteJob) validateExpandedAux(p *DogeAuxPow, chainID uint32) error {
	if p == nil || j.AuxTree == nil {
		return errors.New("missing auxiliary proof")
	}
	if p.ChainMerkleIndex != j.AuxTree.Indices[chainID] || len(p.ChainMerkleBranch) != len(j.AuxTree.Branches[chainID]) {
		return errors.New("auxiliary branch mismatch")
	}
	for i, h := range p.ChainMerkleBranch {
		if h != j.AuxTree.Branches[chainID][i] {
			return errors.New("auxiliary branch differs from issued job")
		}
	}
	if len(p.ParentHeader) != 80 || p.ParentBlockHash != doubleSHA256(p.ParentHeader) || p.ParentMerkleIndex != 0 {
		return errors.New("invalid parent proof")
	}
	root := MerkleRootFromBranch(CoinbaseTxID(p.ParentCoinbase), p.ParentMerkleBranch, 0)
	if !bytes.Equal(root[:], p.ParentHeader[36:68]) {
		return errors.New("invalid parent coinbase Merkle root")
	}
	if uint32(binary.LittleEndian.Uint32(p.ParentHeader[:4]))>>16 == chainID {
		return errors.New("parent and auxiliary chain ID collide")
	}
	plan := j.Coinbase
	if len(p.ParentCoinbase) != len(plan.Coinb1)+plan.ExtraNonce1Size+plan.ExtraNonce2Size+len(plan.Coinb2) ||
		!bytes.HasPrefix(p.ParentCoinbase, plan.Coinb1) || !bytes.HasSuffix(p.ParentCoinbase, plan.Coinb2) {
		return fmt.Errorf("auxiliary parent coinbase differs from issued job")
	}
	if len(p.ParentCoinbase) < 42 || p.ParentCoinbase[4] != 1 {
		return errors.New("invalid parent coinbase input")
	}
	n := int(p.ParentCoinbase[41])
	if n < 2 || n > 100 || len(p.ParentCoinbase) < 42+n {
		return errors.New("invalid parent coinbase script")
	}
	script := p.ParentCoinbase[42 : 42+n]
	if bytes.Count(script, MergedMiningMagic) != 1 || !bytes.Contains(script, j.Commitment) {
		return errors.New("ambiguous auxiliary commitment")
	}
	return nil
}
