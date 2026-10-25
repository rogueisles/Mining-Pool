package rpc

import (
	"bytes"
	"encoding/json"
	"errors"
	"github.com/HawyUkr/open-progpow-pool/forgepool"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/rlp"
	"io"
	"io/ioutil"
	"math/big"
	"net/http"
)

type ForgeRegistryReply struct {
	ParentHash           common.Hash      `json:"parentHash"`
	ParentHeight         string           `json:"parentHeight"`
	ActivationBlock      string           `json:"activationBlock"`
	Root                 common.Hash      `json:"root"`
	Checkpoint           string           `json:"checkpoint"`
	SnapshotRLP          string           `json:"snapshotRlp"`
	Genesis              common.Hash      `json:"genesis"`
	ChainID              string           `json:"chainId"`
	WorkSigners          []common.Address `json:"workSigners"`
	MinDelay             string           `json:"minDelay"`
	MinOperationInterval string           `json:"minOperationInterval"`
}

func (r *RPCClient) ForgeRegistry(parent common.Hash) (*ForgeRegistryReply, error) {
	if parent == (common.Hash{}) {
		return nil, errors.New("missing exact registry parent")
	}
	payload, _ := json.Marshal(map[string]interface{}{"jsonrpc": "2.0", "id": 0, "method": "eth_forgeRegistry", "params": []interface{}{parent}})
	request, err := http.NewRequest(http.MethodPost, r.Url, bytes.NewReader(payload))
	if err != nil {
		return nil, errors.New("invalid registry RPC endpoint")
	}
	request.Header.Set("Content-Type", "application/json")
	if r.Username != "" {
		request.SetBasicAuth(r.Username, r.Password)
	}
	response, err := r.client.Do(request)
	if err != nil {
		return nil, errors.New("registry RPC unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, errors.New("registry RPC HTTP failure")
	}
	raw, err := ioutil.ReadAll(io.LimitReader(response.Body, 65537))
	if err != nil || len(raw) > 65536 {
		return nil, errors.New("registry RPC response exceeds bounded capacity")
	}
	var envelope struct {
		ID     int             `json:"id"`
		Result json.RawMessage `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	if json.Unmarshal(raw, &envelope) != nil || envelope.ID != 0 || len(envelope.Result) == 0 || (len(envelope.Error) > 0 && string(envelope.Error) != "null") {
		return nil, errors.New("invalid registry RPC response")
	}
	var reply ForgeRegistryReply
	if json.Unmarshal(envelope.Result, &reply) != nil {
		return nil, errors.New("invalid registry RPC context")
	}
	if reply.ParentHash != parent {
		return nil, errors.New("registry RPC returned different parent")
	}
	return &reply, nil
}
func (r *ForgeRegistryReply) Decode() (forgepool.RegistrySnapshot, forgepool.RegistryContext, uint64, uint64, error) {
	var snapshot forgepool.RegistrySnapshot
	var ctx forgepool.RegistryContext
	if r == nil {
		return snapshot, ctx, 0, 0, errors.New("missing registry state")
	}
	height, err := hexutil.DecodeUint64(r.ParentHeight)
	if err != nil {
		return snapshot, ctx, 0, 0, err
	}
	activation, err := hexutil.DecodeUint64(r.ActivationBlock)
	if err != nil || activation == 0 {
		return snapshot, ctx, 0, 0, errors.New("invalid registry activation")
	}
	checkpoint, err := hexutil.DecodeUint64(r.Checkpoint)
	if err != nil || checkpoint > height {
		return snapshot, ctx, 0, 0, errors.New("invalid registry checkpoint")
	}
	delay, err := hexutil.DecodeUint64(r.MinDelay)
	if err != nil {
		return snapshot, ctx, 0, 0, err
	}
	interval, err := hexutil.DecodeUint64(r.MinOperationInterval)
	if err != nil {
		return snapshot, ctx, 0, 0, err
	}
	chain, err := hexutil.DecodeBig(r.ChainID)
	if err != nil || chain.Sign() <= 0 {
		return snapshot, ctx, 0, 0, errors.New("invalid registry chain identity")
	}
	raw, err := hexutil.Decode(r.SnapshotRLP)
	if err != nil || len(raw) > forgepool.MaxOGGProofSize {
		return snapshot, ctx, 0, 0, errors.New("invalid bounded registry snapshot")
	}
	if err = rlp.DecodeBytes(raw, &snapshot); err != nil {
		return snapshot, ctx, 0, 0, err
	}
	ctx = forgepool.RegistryContext{ChainID: new(big.Int).Set(chain), Genesis: r.Genesis, WorkSigners: r.WorkSigners, MinDelay: delay, MinOperationInterval: interval, ParentCheckpoint: checkpoint}
	if err = forgepool.ValidateRegistrySnapshot(snapshot, ctx); err != nil {
		return snapshot, ctx, 0, 0, err
	}
	root, err := forgepool.RegistrySnapshotRoot(snapshot)
	if err != nil || root != r.Root || snapshot.Admin.LastOperationHeight != checkpoint {
		return snapshot, ctx, 0, 0, errors.New("registry snapshot does not match parent root/checkpoint")
	}
	return snapshot, ctx, height, activation, nil
}
