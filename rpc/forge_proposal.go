package rpc

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"

	"github.com/HawyUkr/open-progpow-pool/forgepool"
)

// ValidateDogecoinProposal accepts ONLY an explicit BIP23 null result from
// the independently configured daemon. Missing results, duplicates, errors,
// inconclusive replies and transport failures all fail closed.
func (r *RPCClient) ValidateDogecoinProposal(ctx context.Context, block []byte) error {
	if len(block) < 80 || len(block) > forgepool.MaxDogeProposalBytes {
		return errors.New("invalid proposal length")
	}
	body, err := json.Marshal(map[string]interface{}{"jsonrpc": "1.0", "id": 0, "method": "getblocktemplate", "params": []interface{}{map[string]interface{}{"mode": "proposal", "data": hex.EncodeToString(block)}}})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.Url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if r.Username != "" {
		req.SetBasicAuth(r.Username, r.Password)
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("proposal RPC HTTP %d", resp.StatusCode)
	}
	blob, err := ioutil.ReadAll(io.LimitReader(resp.Body, 8193))
	if err != nil {
		return err
	}
	if len(blob) > 8192 {
		return errors.New("proposal RPC response too large")
	}
	var out struct {
		ID     json.RawMessage `json:"id"`
		Result json.RawMessage `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	if err = json.Unmarshal(blob, &out); err != nil {
		return err
	}
	if !bytes.Equal(bytes.TrimSpace(out.ID), []byte("0")) {
		return errors.New("proposal RPC response ID mismatch")
	}
	if len(out.Error) > 0 && !bytes.Equal(bytes.TrimSpace(out.Error), []byte("null")) {
		return errors.New("proposal RPC error")
	}
	if !bytes.Equal(bytes.TrimSpace(out.Result), []byte("null")) {
		return errors.New("DOGE proposal not validated")
	}
	return nil
}
