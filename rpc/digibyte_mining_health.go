package rpc

import (
	"errors"
	"strings"
	"time"
)

const digibyteMainnetGenesis = "7497ea1b465eb39f1c8f507bc877078fe016d6fcb6dfad3a64c98dcc6e1e8496"

// GetHealthyDigibyteMiningTemplate avoids getblockchaininfo's unrelated
// algorithm/pruning summaries. DigiByte Core's template RPC itself refuses
// mainnet work during initial download or without peers. All other identity,
// header-lag and tip-age checks remain explicit, without a health-result cache.
// Opt-in callers require getchainstates support; errors never fall back open.
func (r *RPCClient) GetHealthyDigibyteMiningTemplate() (*BlockTemplateReply, error) {
	res, err := r.doPost(r.Url, "getchainstates", []interface{}{})
	if err != nil {
		return nil, err
	}
	var state struct {
		Headers     *int64 `json:"headers"`
		Chainstates []struct {
			Blocks        *int64 `json:"blocks"`
			BestBlockHash string `json:"bestblockhash"`
			Validated     *bool  `json:"validated"`
		} `json:"chainstates"`
	}
	if unmarshalRPCResult(res, &state) != nil || state.Headers == nil || len(state.Chainstates) == 0 {
		return nil, errors.New("missing DigiByte active chainstate")
	}
	// Core returns the active, most-work chainstate last.
	active := state.Chainstates[len(state.Chainstates)-1]
	if active.Blocks == nil || active.Validated == nil || !*active.Validated || *active.Blocks <= 0 ||
		*state.Headers < *active.Blocks || *state.Headers-*active.Blocks > 2 || len(active.BestBlockHash) != 64 || validateHexBytes(active.BestBlockHash) != nil {
		return nil, errors.New("DigiByte active chainstate is not synchronized and validated")
	}
	res, err = r.doPost(r.Url, "getblockhash", []interface{}{0})
	if err != nil {
		return nil, err
	}
	var genesis string
	if unmarshalRPCResult(res, &genesis) != nil || genesis != digibyteMainnetGenesis {
		return nil, errors.New("DigiByte mainnet genesis mismatch")
	}
	res, err = r.doPost(r.Url, "getnetworkinfo", []interface{}{})
	if err != nil {
		return nil, err
	}
	var network struct {
		Connections   int
		NetworkActive *bool `json:"networkactive"`
	}
	if unmarshalRPCResult(res, &network) != nil || network.Connections < 1 || (network.NetworkActive != nil && !*network.NetworkActive) {
		return nil, errors.New("DigiByte mainnet has no active peers")
	}
	res, err = r.doPost(r.Url, "getblockheader", []interface{}{active.BestBlockHash, true})
	if err != nil {
		return nil, err
	}
	var head struct{ Time int64 }
	if unmarshalRPCResult(res, &head) != nil || !NativeTipTimeValid(digibyteMainnetGenesis, time.Now().Unix(), head.Time) {
		return nil, errors.New("DigiByte mainnet tip age check failed")
	}
	template, err := r.GetDigibyteBlockTemplate()
	if err != nil {
		return nil, err
	} // Includes Core's IBD refusal and Scrypt identity checks.
	if template.Height != *active.Blocks+1 || !strings.EqualFold(template.PreviousBlockHash, active.BestBlockHash) {
		return nil, errors.New("DigiByte tip changed while fetching work")
	}
	return template, nil
}
