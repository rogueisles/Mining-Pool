package rpc

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// A different canonical proof at the configured pool maturity is not payable.
// This uses the same finality assumption as the existing block unlocker. RPC
// errors, young blocks and a changing canonical hash remain unresolved.
func (r *RPCClient) ForgeProofExcludedAtMaturity(height int64, proof string, depth int64) (bool, error) {
	if height < 0 || depth < 32 || validateHexBytes(proof) != nil {
		return false, errors.New("invalid maturity lookup")
	}
	type block struct{ Number, Hash, ExtraData string }
	read := func() (block, error) {
		var b block
		v, e := r.forgeCall("eth_getBlockByNumber", []interface{}{fmt.Sprintf("0x%x", height), false})
		if e != nil {
			return b, e
		}
		if json.Unmarshal(v, &b) != nil || b.Number != fmt.Sprintf("0x%x", height) || len(b.Hash) != 66 || !strings.HasPrefix(b.Hash, "0x") || validateHexBytes(b.Hash) != nil || !strings.HasPrefix(b.ExtraData, "0x") || validateHexBytes(b.ExtraData) != nil {
			return b, errors.New("invalid canonical maturity block")
		}
		return b, nil
	}
	b, e := read()
	if e != nil {
		return false, e
	}
	if strings.EqualFold(b.ExtraData, "0x"+strings.TrimPrefix(proof, "0x")) {
		return false, nil
	}
	v, e := r.forgeCall("eth_blockNumber", []interface{}{})
	if e != nil {
		return false, e
	}
	var head string
	if json.Unmarshal(v, &head) != nil || !strings.HasPrefix(head, "0x") {
		return false, errors.New("invalid maturity head")
	}
	h, e := strconv.ParseUint(strings.TrimPrefix(head, "0x"), 16, 64)
	if e != nil {
		return false, errors.New("invalid maturity height")
	}
	if h < uint64(height) || h-uint64(height) < uint64(depth) {
		return false, nil
	}
	check, e := read()
	if e != nil {
		return false, e
	}
	return strings.EqualFold(check.Hash, b.Hash) && strings.EqualFold(check.ExtraData, b.ExtraData), nil
}
