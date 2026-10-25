package rpc

import (
	"bytes"
	"encoding/hex"
	"errors"
	"github.com/HawyUkr/open-progpow-pool/forgepool"
)

// ValidateSOQPayout uses the native Bech32m decoder, rather than a legacy
// Bitcoin address decoder which cannot recognize SOQ ML-DSA addresses.
func (r *RPCClient) ValidateSOQPayout(address string, script []byte) error {
	res, err := r.doPost(r.Url, "validateaddress", []interface{}{address})
	if err != nil {
		return err
	}
	var out struct {
		IsValid     bool   `json:"isvalid"`
		IsDilithium bool   `json:"isdilithium"`
		Address     string `json:"address"`
		Script      string `json:"scriptPubKey"`
	}
	if unmarshalRPCResult(res, &out) != nil || !out.IsValid || !out.IsDilithium || out.Address != address {
		return errors.New("native SOQ payout address validation failed")
	}
	decoded, err := hex.DecodeString(out.Script)
	if err != nil || !forgepool.ValidSOQScript(decoded) || !bytes.Equal(decoded, script) {
		return errors.New("SOQ payout address and configured script differ")
	}
	return nil
}
