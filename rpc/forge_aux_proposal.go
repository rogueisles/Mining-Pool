package rpc

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/HawyUkr/open-progpow-pool/forgepool"
	"io"
	"io/ioutil"
	"net/http"
)

// CheckTreasuryProposal independently builds from the node's own template.
// Eligibility must include a usable treasury-paying body, not merely node
// uptime. A malformed caller proposal cannot influence this observation.
func (r *RPCClient) CheckTreasuryProposal(ctx context.Context, t forgepool.ParentTemplate, script []byte, chainID uint32) error {
	var block []byte
	var err error
	if chainID == 0 {
		job, e := forgepool.BuildMultiParent(t, "0x0000000000000000000000000000000000000000000000000000000000000001", nil, script, 0, 4, 4, true)
		if e != nil {
			return e
		}
		block, err = job.FullBlock(make([]byte, 4), make([]byte, 4), t.CurTime, 0)
	} else {
		candidate, e := forgepool.BuildAuxCandidate(t, script, 0, chainID)
		if e != nil {
			return e
		}
		block, err = candidate.AuxValidationBlock()
	}
	if err != nil {
		return err
	}
	return r.ValidateAuxiliaryProposal(ctx, block)
}

// ValidateAuxiliaryProposal accepts ONLY an explicit BIP23 null result from
// the independently configured daemon. Missing results, duplicates, errors,
// inconclusive replies and transport failures all fail closed.
func (r *RPCClient) ValidateAuxiliaryProposal(ctx context.Context, block []byte) error {
	result, err := r.nativeProposalResult(ctx, block)
	if err != nil {
		return err
	}
	if !bytes.Equal(bytes.TrimSpace(result), []byte("null")) {
		return errors.New("native proposal not validated")
	}
	return nil
}

// ValidateFullTreasuryClaim requires acceptance of the original native body and
// an exact overclaim rejection for its one-base-unit increment. The caller must
// construct the second body with TreasuryClaimProbe and keep the signed body
// unchanged. No mining solution or native block submission is involved.
func (r *RPCClient) ValidateFullTreasuryClaim(ctx context.Context, block, incremented []byte) error {
	if len(block) < 80 || len(incremented) < 80 || bytes.Equal(block, incremented) ||
		!bytes.Equal(block[:36], incremented[:36]) || !bytes.Equal(block[68:80], incremented[68:80]) {
		return errors.New("treasury claim probe changed native header context")
	}
	if err := r.ValidateAuxiliaryProposal(ctx, block); err != nil {
		return err
	}
	result, err := r.nativeProposalResult(ctx, incremented)
	if err != nil {
		return err
	}
	var reason string
	if json.Unmarshal(result, &reason) != nil || reason != "bad-cb-amount" {
		return errors.New("full native treasury claim not established")
	}
	return ctx.Err()
}

func (r *RPCClient) nativeProposalResult(ctx context.Context, block []byte) (json.RawMessage, error) {
	if len(block) < 80 || len(block) > 4000000 {
		return nil, errors.New("invalid proposal length")
	}
	body, err := json.Marshal(map[string]interface{}{"jsonrpc": "1.0", "id": 0, "method": "getblocktemplate", "params": []interface{}{map[string]interface{}{"mode": "proposal", "data": hex.EncodeToString(block)}}})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.Url, bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("invalid proposal RPC endpoint")
	}
	req.Header.Set("Content-Type", "application/json")
	if r.Username != "" {
		req.SetBasicAuth(r.Username, r.Password)
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, errors.New("proposal RPC transport failure")
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("proposal RPC HTTP %d", resp.StatusCode)
	}
	blob, err := ioutil.ReadAll(io.LimitReader(resp.Body, 8193))
	if err != nil {
		return nil, errors.New("proposal RPC response read failure")
	}
	if len(blob) > 8192 {
		return nil, errors.New("proposal RPC response too large")
	}
	d := json.NewDecoder(bytes.NewReader(blob))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return nil, errors.New("invalid proposal RPC response")
	}
	fields := make(map[string]json.RawMessage)
	for d.More() {
		token, err = d.Token()
		key, ok := token.(string)
		if err != nil || !ok {
			return nil, errors.New("invalid proposal RPC field")
		}
		if _, exists := fields[key]; exists {
			return nil, errors.New("duplicate proposal RPC field")
		}
		var value json.RawMessage
		if d.Decode(&value) != nil {
			return nil, errors.New("invalid proposal RPC value")
		}
		fields[key] = value
	}
	if token, err = d.Token(); err != nil || token != json.Delim('}') {
		return nil, errors.New("invalid proposal RPC end")
	}
	var tail interface{}
	if d.Decode(&tail) != io.EOF {
		return nil, errors.New("trailing proposal RPC data")
	}
	if !bytes.Equal(bytes.TrimSpace(fields["id"]), []byte("0")) || len(fields["result"]) == 0 {
		return nil, errors.New("missing or mismatched proposal RPC result")
	}
	if !bytes.Equal(bytes.TrimSpace(fields["error"]), []byte("null")) {
		return nil, errors.New("proposal RPC error or missing error field")
	}
	return fields["result"], nil
}
