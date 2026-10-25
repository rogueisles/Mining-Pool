package forgepool

import (
	"errors"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rlp"
)

func ValidSOQScript(script []byte) bool {
	return len(script) == 34 && script[0] == 0x51 && script[1] == 0x20
}

// ValidateOptionalSOQHeader binds the activation check to the RGN work leaf.
// A caller cannot relabel old work with a later height. These are the existing
// fifteen RGN header fields, not a new consensus proof or signing domain.
func ValidateOptionalSOQHeader(header []byte, work common.Hash, stamp, activation uint64) error {
	if activation <= 1100100 || len(header) == 0 || len(header) > 4096 || work == (common.Hash{}) || crypto.Keccak256Hash(header) != work {
		return errors.New("missing or mismatched optional SOQ RGN work header")
	}
	var fields []rlp.RawValue
	if rlp.DecodeBytes(header, &fields) != nil || len(fields) != 15 {
		return errors.New("invalid optional SOQ RGN header")
	}
	var number, timestamp uint64
	var extra []byte
	if rlp.DecodeBytes(fields[8], &number) != nil || rlp.DecodeBytes(fields[11], &timestamp) != nil || rlp.DecodeBytes(fields[12], &extra) != nil ||
		number < activation || timestamp != stamp || len(extra) > 32 {
		return errors.New("optional SOQ is not active for this RGN work")
	}
	return nil
}
