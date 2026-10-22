package rpc

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"strings"
)

// forgeCall deliberately distinguishes a present JSON null from a missing
// result. An uncertain reply is never an acknowledgement. Errors exclude URLs,
// credentials and untrusted daemon text because callers persist them.
func (r *RPCClient) forgeCall(method string, params interface{}) (json.RawMessage, error) {
	body, err := json.Marshal(map[string]interface{}{"jsonrpc": "2.0", "id": 0, "method": method, "params": params})
	if err != nil {
		return nil, errors.New("cannot encode FORGE RPC")
	}
	req, err := http.NewRequest(http.MethodPost, r.Url, bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("invalid FORGE RPC endpoint")
	}
	req.Header.Set("Content-Type", "application/json")
	if r.Username != "" {
		req.SetBasicAuth(r.Username, r.Password)
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, errors.New("FORGE RPC transport failure")
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("FORGE RPC HTTP %d", resp.StatusCode)
	}
	const maxReply = 8 * 1024 * 1024
	blob, err := ioutil.ReadAll(io.LimitReader(resp.Body, maxReply+1))
	if err != nil || len(blob) > maxReply {
		return nil, errors.New("invalid FORGE RPC response length")
	}
	d := json.NewDecoder(bytes.NewReader(blob))
	tok, err := d.Token()
	if err != nil || tok != json.Delim('{') {
		return nil, errors.New("invalid FORGE RPC response")
	}
	fields := make(map[string]json.RawMessage)
	for d.More() {
		tok, err = d.Token()
		if err != nil {
			return nil, errors.New("invalid FORGE RPC field")
		}
		key, ok := tok.(string)
		if !ok {
			return nil, errors.New("invalid FORGE RPC key")
		}
		if _, exists := fields[key]; exists {
			return nil, errors.New("duplicate FORGE RPC field")
		}
		var value json.RawMessage
		if d.Decode(&value) != nil {
			return nil, errors.New("invalid FORGE RPC value")
		}
		fields[key] = value
	}
	if _, err = d.Token(); err != nil {
		return nil, errors.New("invalid FORGE RPC end")
	}
	var tail interface{}
	if d.Decode(&tail) != io.EOF {
		return nil, errors.New("trailing FORGE RPC data")
	}
	if string(fields["id"]) != "0" || len(fields["result"]) == 0 {
		return nil, errors.New("missing or mismatched FORGE RPC result")
	}
	if e := fields["error"]; len(e) != 0 && string(e) != "null" {
		return nil, errors.New("FORGE daemon RPC error")
	}
	return fields["result"], nil
}

// Rejected is conclusive only for a first attempt with no earlier uncertain
// delivery. A retry rejection cannot erase an earlier possible acceptance.
func (r *RPCClient) SubmitForgeBlock(blockHex string) (accepted, rejected bool, err error) {
	if validateHexBytes(blockHex) != nil {
		return false, true, errors.New("invalid raw block")
	}
	result, err := r.forgeCall("submitblock", []interface{}{strings.TrimPrefix(blockHex, "0x")})
	if err != nil {
		return false, false, err
	}
	if string(result) == "null" {
		return true, false, nil
	}
	var reason string
	if json.Unmarshal(result, &reason) != nil || reason == "" {
		return false, false, errors.New("uncertain submitblock result")
	}
	if strings.HasPrefix(reason, "duplicate") || strings.Contains(reason, "inconclusive") {
		return false, false, errors.New("submitblock requires reconciliation")
	}
	return false, true, errors.New("submitblock rejected block")
}

func (r *RPCClient) SubmitForgeOGG(workHash, proofHex string) (accepted, rejected bool, err error) {
	if validateHexBytes(workHash) != nil || len(strings.TrimPrefix(workHash, "0x")) != 64 || validateHexBytes(proofHex) != nil {
		return false, true, errors.New("invalid OGG submission")
	}
	result, err := r.forgeCall("eth_submitAuxBlock", []interface{}{"0x" + strings.TrimPrefix(workHash, "0x"), "0x" + strings.TrimPrefix(proofHex, "0x")})
	if err != nil {
		return false, false, err
	}
	switch string(result) {
	case "true":
		return true, false, nil
	case "false":
		return false, true, errors.New("OGG rejected proof")
	default:
		return false, false, errors.New("uncertain OGG submission result")
	}
}

// Canonical body presence is positive evidence. Not found, headers-only,
// side-chain and RPC errors remain uncertain; none proves non-acceptance.
func (r *RPCClient) HasForgeBlock(hash string) (bool, error) {
	observation, err := r.ForgeBlockObservation(hash)
	return observation == "canonical", err
}

// ForgeBlockObservation describes the trusted daemon's stored block body.
// It does not turn a side-chain body into acceptance or payable revenue.
func (r *RPCClient) ForgeBlockObservation(hash string) (string, error) {
	result, err := r.forgeCall("getblock", []interface{}{strings.TrimPrefix(hash, "0x"), true})
	if err != nil {
		return "unknown", err
	}
	var block struct {
		Hash          string
		Confirmations *int64
		Tx            []json.RawMessage
	}
	if err := json.Unmarshal(result, &block); err != nil {
		return "unknown", errors.New("invalid block lookup")
	}
	if !strings.EqualFold(block.Hash, strings.TrimPrefix(hash, "0x")) ||
		block.Confirmations == nil ||
		len(block.Tx) == 0 {
		return "unknown", nil
	}
	if *block.Confirmations > 0 {
		return "canonical", nil
	}
	if *block.Confirmations == -1 {
		return "noncanonical", nil
	}
	return "unknown", nil
}

func (r *RPCClient) HasForgeProof(height int64, proofHex string) (bool, error) {
	hash, err := r.FindForgeProofBlock(height, proofHex, "")
	return hash != "", err
}

// A supplied hash is only a lookup hint. The trusted node must return the
// exact height, hash and saved proof, including for a known noncanonical block.
func (r *RPCClient) FindForgeProofBlock(height int64, proofHex, hint string) (string, error) {
	method := "eth_getBlockByNumber"
	selector := fmt.Sprintf("0x%x", height)
	if hint != "" {
		if len(hint) != 66 || !strings.HasPrefix(hint, "0x") || validateHexBytes(hint) != nil {
			return "", errors.New("invalid OGG block hint")
		}
		method, selector = "eth_getBlockByHash", hint
	}
	result, err := r.forgeCall(method, []interface{}{selector, false})
	if err != nil {
		return "", err
	}
	var block struct{ Number, Hash, ExtraData string }
	if err := json.Unmarshal(result, &block); err != nil {
		return "", errors.New("invalid OGG block lookup")
	}
	if block.Number != fmt.Sprintf("0x%x", height) || len(block.Hash) != 66 || !strings.HasPrefix(block.Hash, "0x") || validateHexBytes(block.Hash) != nil || !strings.EqualFold(block.ExtraData, "0x"+strings.TrimPrefix(proofHex, "0x")) || (hint != "" && !strings.EqualFold(hint, block.Hash)) {
		return "", nil
	}
	return strings.ToLower(block.Hash), nil
}
