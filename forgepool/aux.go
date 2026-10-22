package forgepool

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
)

const (
	OGGAuxChainID  uint32 = 0x4F47
	DOGEAuxChainID uint32 = 0x0062
	AuxMerkleSize  uint32 = 2
)

var MergedMiningMagic = []byte{0xfa, 0xbe, 0x6d, 0x6d}

func doubleSHA256(b []byte) Hash {
	one := sha256.Sum256(b)
	two := sha256.Sum256(one[:])
	return Hash(two)
}

func reverseHash(h Hash) Hash {
	var out Hash
	for i := range h {
		out[i] = h[len(h)-1-i]
	}
	return out
}

// ExpectedChainIndex is the standard Namecoin/Dogecoin AuxPoW LCG chain-slot
// function.
func ExpectedChainIndex(auxNonce, chainID, merkleSize uint32) uint32 {
	rand := auxNonce
	rand = rand*1103515245 + 12345
	rand += chainID
	rand = rand*1103515245 + 12345
	return rand % merkleSize
}

// BuildAuxRoot constructs the fixed two-leaf OGG+DOGE merged-mining root.
// Both leaves use raw/internal digest byte order.
func BuildAuxRoot(oggLeaf, dogeLeaf Hash, auxNonce uint32) (root Hash, oggIndex, dogeIndex uint32, err error) {
	oggIndex = ExpectedChainIndex(auxNonce, OGGAuxChainID, AuxMerkleSize)
	dogeIndex = ExpectedChainIndex(auxNonce, DOGEAuxChainID, AuxMerkleSize)
	if oggIndex == dogeIndex {
		return Hash{}, oggIndex, dogeIndex, errors.New("OGG and DOGE AuxPoW slots collide")
	}
	var leaves [2]Hash
	leaves[oggIndex] = oggLeaf
	leaves[dogeIndex] = dogeLeaf
	buf := make([]byte, 0, 64)
	buf = append(buf, leaves[0][:]...)
	buf = append(buf, leaves[1][:]...)
	return doubleSHA256(buf), oggIndex, dogeIndex, nil
}

// AuxCommitment returns the standard merged-mining coinbase payload:
// magic || display/reversed aux-root || little-endian tree-size || nonce.
func AuxCommitment(oggLeaf, dogeLeaf Hash, auxNonce uint32) ([]byte, uint32, uint32, error) {
	root, oi, di, err := BuildAuxRoot(oggLeaf, dogeLeaf, auxNonce)
	if err != nil {
		return nil, oi, di, err
	}
	committedRoot := reverseHash(root)
	out := make([]byte, 0, 4+32+4+4)
	out = append(out, MergedMiningMagic...)
	out = append(out, committedRoot[:]...)
	var tmp [4]byte
	binary.LittleEndian.PutUint32(tmp[:], AuxMerkleSize)
	out = append(out, tmp[:]...)
	binary.LittleEndian.PutUint32(tmp[:], auxNonce)
	out = append(out, tmp[:]...)
	return out, oi, di, nil
}

// DeterministicAuxNonce returns the first nonce at or after seed that produces
// distinct OGG and DOGE slots. With the fixed current chain IDs/tree size this
// is deterministic and normally the seed itself.

// AuxCommitmentV2 builds the V2 merged-mining commitment.
//
// OGG is always present. When dogeLeaf is nil the Aux tree contains a single
// OGG leaf. When DOGE participates the exact legacy two-leaf construction is
// retained.
func AuxCommitmentV2(oggLeaf Hash, dogeLeaf *Hash, auxNonce uint32) ([]byte, uint32, uint32, error) {
	if dogeLeaf != nil {
		return AuxCommitment(oggLeaf, *dogeLeaf, auxNonce)
	}

	committedRoot := reverseHash(oggLeaf)
	out := make([]byte, 0, 4+32+4+4)
	out = append(out, MergedMiningMagic...)
	out = append(out, committedRoot[:]...)

	var tmp [4]byte
	binary.LittleEndian.PutUint32(tmp[:], 1)
	out = append(out, tmp[:]...)

	binary.LittleEndian.PutUint32(tmp[:], auxNonce)
	out = append(out, tmp[:]...)

	return out, 0, 0, nil
}

func DeterministicAuxNonce(seed uint32) uint32 {
	for nonce := seed; ; nonce++ {
		if ExpectedChainIndex(nonce, OGGAuxChainID, AuxMerkleSize) != ExpectedChainIndex(nonce, DOGEAuxChainID, AuxMerkleSize) {
			return nonce
		}
	}
}

// ScriptHash is the exact SHA-256 commitment used by node consensus for DGB
// and DOGE payout scripts.
func ScriptHash(script []byte) Hash {
	return Hash(sha256.Sum256(script))
}
