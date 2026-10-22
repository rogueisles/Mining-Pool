package rpc

import (
	"errors"
	"time"
)

// DigiByte Core v9.26.7 chain.h permits a two-hour future timestamp horizon.
// This bounded signer-clock health policy does not replace native adjusted-time validation.
func NativeTipTimeValid(genesis string, now, timestamp int64) bool {
	if now <= 0 || timestamp <= 0 {
		return false
	}
	futureLimit := int64(120)
	if genesis == "7497ea1b465eb39f1c8f507bc877078fe016d6fcb6dfad3a64c98dcc6e1e8496" {
		futureLimit = 2 * 60 * 60
	}
	if timestamp > now {
		return timestamp-now <= futureLimit
	}
	return now-timestamp <= 3600
}

func (r *RPCClient) GetAuxiliaryBlockTemplate(segwit ...bool) (*BlockTemplateReply, error) {
	// Bells requires SegWit negotiation. Dogecoin-family nodes with SegWit
	// disabled can advertise an unusable witness commitment if asked for it.
	request := map[string]interface{}{}
	if len(segwit) == 0 || segwit[0] {
		request["rules"] = []string{"segwit"}
	}
	t, err := r.getBlockTemplate(request)
	if err != nil {
		return nil, err
	}
	if t.Height <= 0 || t.PreviousBlockHash == "" || t.MWEB != "" {
		return nil, errors.New("invalid auxiliary template")
	}
	for _, rule := range t.Rules {
		if len(rule) > 0 && rule[0] == '!' && rule != "!segwit" && rule != "!csv" && rule != "!taproot" {
			return nil, errors.New("unsupported mandatory auxiliary rule")
		}
	}
	return t, nil
}

func (r *RPCClient) CheckAuxiliaryMainnet(genesis string) error {
	res, err := r.doPost(r.Url, "getblockchaininfo", []interface{}{})
	if err != nil {
		return err
	}
	var info struct {
		Chain                string
		Blocks, Headers      int64
		InitialBlockDownload *bool  `json:"initialblockdownload"`
		BestBlockHash        string `json:"bestblockhash"`
	}
	if unmarshalRPCResult(res, &info) != nil || info.Chain != "main" || info.Blocks <= 0 || info.InitialBlockDownload == nil || *info.InitialBlockDownload || info.Headers < info.Blocks || info.Headers-info.Blocks > 2 {
		return errors.New("auxiliary mainnet synchronization check failed")
	}
	res, err = r.doPost(r.Url, "getblockhash", []interface{}{0})
	if err != nil {
		return err
	}
	var actual string
	if unmarshalRPCResult(res, &actual) != nil || actual != genesis || len(genesis) != 64 {
		return errors.New("auxiliary mainnet genesis mismatch")
	}
	res, err = r.doPost(r.Url, "getnetworkinfo", []interface{}{})
	if err != nil {
		return err
	}
	var net struct{ Connections int }
	if unmarshalRPCResult(res, &net) != nil || net.Connections < 1 {
		return errors.New("auxiliary node has no peers")
	}
	res, err = r.doPost(r.Url, "getblockheader", []interface{}{info.BestBlockHash, true})
	if err != nil {
		return err
	}
	var head struct{ Time int64 }
	if unmarshalRPCResult(res, &head) != nil || !NativeTipTimeValid(genesis, time.Now().Unix(), head.Time) {
		return errors.New("auxiliary mainnet tip stale")
	}
	return nil
}

// Dingocoin inherited Dogecoin's genesis. A post-genesis checkpoint distinguishes
// their networks before any treasury work can be published.
func (r *RPCClient) CheckDingocoinIdentity() error {
	res, err := r.doPost(r.Url, "getblockhash", []interface{}{1})
	if err != nil {
		return err
	}
	var hash string
	if unmarshalRPCResult(res, &hash) != nil || hash != "594a42d8fe16382085dc982135df72cf8fcea12d34e6efd566e2f9e442e2136f" {
		return errors.New("Dingocoin checkpoint mismatch")
	}
	return nil
}

// Dogecoin and Dingocoin share genesis, so both identities need an independent
// post-genesis pin. Dogecoin Core v1.14.9 chainparams.cpp, main checkpoint 104679.
func (r *RPCClient) CheckDogecoinIdentity() error {
	res, err := r.doPost(r.Url, "getblockhash", []interface{}{104679})
	if err != nil {
		return err
	}
	var hash string
	if unmarshalRPCResult(res, &hash) != nil || hash != "35eb87ae90d44b98898fec8c39577b76cb1eb08e1261cfc10706c8ce9a1d01cf" {
		return errors.New("Dogecoin checkpoint mismatch")
	}
	return nil
}
