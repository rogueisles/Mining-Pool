package forgepool

import (
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"

	"golang.org/x/crypto/scrypt"
)

// Hash is a Bitcoin-family digest in raw/internal byte order.
type Hash [32]byte

var diffOneTarget = mustHexBig("00000000ffff0000000000000000000000000000000000000000000000000000")

func mustHexBig(s string) *big.Int {
	v, ok := new(big.Int).SetString(s, 16)
	if !ok {
		panic("invalid FORGE integer")
	}
	return v
}

// ScryptHash computes Digibyte-compatible scrypt_1024_1_1_256 over an
// 80-byte parent header. The returned bytes are the raw/internal digest bytes.
func ScryptHash(header []byte) (Hash, error) {
	var outHash Hash
	if len(header) != 80 {
		return outHash, fmt.Errorf("invalid Scrypt header length %d", len(header))
	}
	out, err := scrypt.Key(header, header, 1024, 1, 1, 32)
	if err != nil {
		return outHash, err
	}
	copy(outHash[:], out)
	return outHash, nil
}

// HashValue converts raw Bitcoin-family digest bytes into the integer used for
// proof-of-work comparisons (little-endian uint256 interpretation).
func HashValue(raw Hash) *big.Int {
	rev := make([]byte, len(raw))
	for i := range raw {
		rev[i] = raw[len(raw)-1-i]
	}
	return new(big.Int).SetBytes(rev)
}

// ScryptHashValue computes the Scrypt digest and returns its PoW integer.
func ScryptHashValue(header []byte) (*big.Int, error) {
	raw, err := ScryptHash(header)
	if err != nil {
		return nil, err
	}
	return HashValue(raw), nil
}

// TargetForDifficulty maps integer Scrypt difficulty 1 to Bitcoin/Digibyte's
// canonical 0x1d00ffff target and scales inversely for higher difficulty.
func TargetForDifficulty(diff *big.Int) (*big.Int, error) {
	if diff == nil || diff.Sign() <= 0 {
		return nil, errors.New("invalid Scrypt difficulty")
	}
	return new(big.Int).Div(new(big.Int).Set(diffOneTarget), diff), nil
}

// StratumTargetForDifficulty uses Scrypt Stratum's 65536 multiplier relative
// to Bitcoin difficulty (pooler/cpuminer stratum_gen_work). This is a SHARE
// target only: chain nBits and the node-issued OGG target are unchanged.
func StratumTargetForDifficulty(diff *big.Int) (*big.Int, error) {
	if diff == nil || diff.Sign() <= 0 {
		return nil, errors.New("invalid Scrypt Stratum difficulty")
	}
	base := new(big.Int).Lsh(new(big.Int).Set(diffOneTarget), 16)
	return base.Div(base, diff), nil
}

func MeetsDifficulty(raw Hash, diff *big.Int) (bool, error) {
	target, err := TargetForDifficulty(diff)
	if err != nil {
		return false, err
	}
	return HashValue(raw).Cmp(target) <= 0, nil
}

// CompactTarget decodes Bitcoin's unsigned compact nBits target encoding.
func CompactTarget(bits uint32) (*big.Int, error) {
	size := uint(bits >> 24)
	word := bits & 0x007fffff
	negative := bits&0x00800000 != 0
	if negative || word == 0 {
		return nil, errors.New("invalid compact target")
	}

	target := new(big.Int).SetUint64(uint64(word))
	if size <= 3 {
		target.Rsh(target, 8*(3-size))
	} else {
		target.Lsh(target, 8*(size-3))
	}
	if target.Sign() <= 0 || target.BitLen() > 256 {
		return nil, errors.New("compact target out of range")
	}
	return target, nil
}

func MeetsCompactTarget(raw Hash, bits uint32) (bool, error) {
	target, err := CompactTarget(bits)
	if err != nil {
		return false, err
	}
	return HashValue(raw).Cmp(target) <= 0, nil
}

// DisplayHash returns the conventional reversed hexadecimal block/hash form.
func DisplayHash(raw Hash) string {
	rev := make([]byte, len(raw))
	for i := range raw {
		rev[i] = raw[len(raw)-1-i]
	}
	return hex.EncodeToString(rev)
}

// ParseDisplayHash converts a conventional big-endian display hash into the
// raw/internal 32-byte Bitcoin-family digest representation.
func ParseDisplayHash(s string) (Hash, error) {
	var raw Hash
	if len(s) >= 2 && s[:2] == "0x" {
		s = s[2:]
	}
	b, err := hex.DecodeString(s)
	if err != nil {
		return raw, err
	}
	if len(b) != 32 {
		return raw, fmt.Errorf("invalid hash length %d", len(b))
	}
	for i := range b {
		raw[i] = b[len(b)-1-i]
	}
	return raw, nil
}
