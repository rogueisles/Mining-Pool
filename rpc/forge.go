package rpc

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/HawyUkr/open-progpow-pool/forgepool"
)

// OGGAuxBlock mirrors the Phase 4 eth_createAuxBlock response. Hex quantities
// and hashes are kept as strings so the pool never silently truncates a
// consensus value while crossing the JSON-RPC boundary.
type OGGAuxBlock struct {
	ProofVersion        string `json:"proofVersion"`
	WorkHash            string `json:"workHash"`
	Target              string `json:"target"`
	Height              string `json:"height"`
	Timestamp           string `json:"timestamp"`
	TemplateMaxValidity string `json:"templateMaxValidity"`
	AuxChainID          string `json:"auxChainId"`
	DogeAuxChainID      string `json:"dogeAuxChainId"`
	PreviousBlockHash   string `json:"previousBlockHash"`
	Difficulty          string `json:"difficulty"`
	MinerReward         string `json:"minerReward"`
}

// OGGReward mirrors the canonical eth_getBlockReward response from the node.
type OGGReward struct {
	BlockNumber     string `json:"blockNumber"`
	Total           string `json:"total"`
	Miner           string `json:"miner"`
	Staking         string `json:"staking"`
	Tribe           string `json:"tribe"`
	Maintenance     string `json:"maintenance"`
	P2E             string `json:"p2e"`
	Treasury        string `json:"treasury"`
	SpecialIssuance string `json:"specialIssuance"`
	CurrencySymbol  string `json:"currencySymbol"`
}

// GBTTransaction is the Bitcoin-family getblocktemplate transaction shape used
// by Digibyte and Dogecoin. Additional daemon fields are intentionally ignored.
type GBTTransaction struct {
	Data    string  `json:"data"`
	TxID    string  `json:"txid"`
	Hash    string  `json:"hash"`
	Depends []int64 `json:"depends"`
	Fee     int64   `json:"fee"`
	SigOps  int64   `json:"sigops"`
	Weight  int64   `json:"weight"`
}

// BlockTemplateReply captures the fields needed to construct a merged-mining
// parent/candidate job. MWEB/witness fields are included here so later job
// construction can remain wire-compatible with current Digibyte templates.
type BlockTemplateReply struct {
	Capabilities             []string          `json:"capabilities"`
	Version                  int32             `json:"version"`
	Rules                    []string          `json:"rules"`
	VBAvailable              map[string]int32  `json:"vbavailable"`
	VBRequired               uint32            `json:"vbrequired"`
	PreviousBlockHash        string            `json:"previousblockhash"`
	Transactions             []GBTTransaction  `json:"transactions"`
	CoinbaseAux              map[string]string `json:"coinbaseaux"`
	CoinbaseValue            int64             `json:"coinbasevalue"`
	LongPollID               string            `json:"longpollid"`
	Target                   string            `json:"target"`
	MinTime                  int64             `json:"mintime"`
	Mutable                  []string          `json:"mutable"`
	NonceRange               string            `json:"noncerange"`
	SigOpLimit               int64             `json:"sigoplimit"`
	SizeLimit                int64             `json:"sizelimit"`
	WeightLimit              int64             `json:"weightlimit"`
	CurTime                  int64             `json:"curtime"`
	Bits                     string            `json:"bits"`
	Height                   int64             `json:"height"`
	DefaultWitnessCommitment string            `json:"default_witness_commitment"`
	PowAlgo                  string            `json:"pow_algo"`
	PowAlgoID                *int              `json:"pow_algo_id"`
	MWEB                     string            `json:"mweb"`
}

// ToParentTemplate converts JSON-RPC getblocktemplate fields into the exact
// raw/internal byte order used by Bitcoin-family Merkle and header hashing.
func (t *BlockTemplateReply) ToParentTemplate() (forgepool.ParentTemplate, error) {
	var out forgepool.ParentTemplate
	if t == nil {
		return out, errors.New("nil block template")
	}
	previous, err := forgepool.ParseDisplayHash(t.PreviousBlockHash)
	if err != nil {
		return out, fmt.Errorf("invalid previousblockhash: %v", err)
	}
	bits, err := forgepool.ParseBitsHex(t.Bits)
	if err != nil {
		return out, fmt.Errorf("invalid nBits: %v", err)
	}
	if t.CurTime < 0 || uint64(t.CurTime) > uint64(^uint32(0)) {
		return out, errors.New("curtime outside uint32 range")
	}
	if t.Height < 0 {
		return out, errors.New("negative template height")
	}

	out.Version = t.Version
	out.Previous = previous
	out.CoinbaseValue = t.CoinbaseValue
	out.CurTime = uint32(t.CurTime)
	out.Bits = bits
	out.Height = t.Height
	out.CoinbaseAux = make(map[string][]byte, len(t.CoinbaseAux))
	for k, v := range t.CoinbaseAux {
		b, err := hex.DecodeString(strings.TrimPrefix(v, "0x"))
		if err != nil {
			return forgepool.ParentTemplate{}, fmt.Errorf("invalid coinbaseaux %q: %v", k, err)
		}
		out.CoinbaseAux[k] = b
	}
	if t.DefaultWitnessCommitment != "" {
		out.WitnessCommitment, err = hex.DecodeString(strings.TrimPrefix(t.DefaultWitnessCommitment, "0x"))
		if err != nil {
			return forgepool.ParentTemplate{}, fmt.Errorf("invalid witness commitment: %v", err)
		}
	}
	if t.MWEB != "" {
		out.MWEB, err = hex.DecodeString(strings.TrimPrefix(t.MWEB, "0x"))
		if err != nil {
			return forgepool.ParentTemplate{}, fmt.Errorf("invalid MWEB block: %v", err)
		}
	}
	out.Transactions = make([]forgepool.TemplateTx, 0, len(t.Transactions))
	for i, tx := range t.Transactions {
		if tx.TxID == "" {
			return forgepool.ParentTemplate{}, fmt.Errorf("template transaction %d missing txid", i)
		}
		txid, err := forgepool.ParseDisplayHash(tx.TxID)
		if err != nil {
			return forgepool.ParentTemplate{}, fmt.Errorf("invalid transaction %d txid: %v", i, err)
		}
		data, err := hex.DecodeString(strings.TrimPrefix(tx.Data, "0x"))
		if err != nil || len(data) == 0 {
			if err == nil {
				err = errors.New("empty transaction data")
			}
			return forgepool.ParentTemplate{}, fmt.Errorf("invalid transaction %d data: %v", i, err)
		}
		out.Transactions = append(out.Transactions, forgepool.TemplateTx{Data: data, TxID: txid})
	}
	return out, nil
}

// AuxBlockTemplate is the Dogecoin/Namecoin-compatible getauxblock template
// shape retained as a compatibility/fallback path.
type AuxBlockTemplate struct {
	Hash              string `json:"hash"`
	ChainID           int64  `json:"chainid"`
	PreviousBlockHash string `json:"previousblockhash"`
	CoinbaseValue     int64  `json:"coinbasevalue"`
	Bits              string `json:"bits"`
	Height            int64  `json:"height"`
	Target            string `json:"target"`
}

func unmarshalRPCResult(resp *JSONRpcResp, out interface{}) error {
	if resp == nil || resp.Result == nil {
		return errors.New("missing JSON-RPC result")
	}
	return json.Unmarshal(*resp.Result, out)
}

func validateHexBytes(s string) error {
	raw := strings.TrimPrefix(s, "0x")
	if raw == "" || len(raw)%2 != 0 {
		return errors.New("invalid hex byte string")
	}
	_, err := hex.DecodeString(raw)
	return err
}

// CreateOGGAuxBlock requests the current post-FORGE work package from the OGG
// node. The node itself enforces that FORGE is active for the candidate height.
func (r *RPCClient) CreateOGGAuxBlock() (*OGGAuxBlock, error) {
	resp, err := r.doPost(r.Url, "eth_createAuxBlock", []interface{}{})
	if err != nil {
		return nil, err
	}
	var reply OGGAuxBlock
	if err := unmarshalRPCResult(resp, &reply); err != nil {
		return nil, err
	}
	return &reply, nil
}

// SubmitOGGAuxBlock submits the bounded/versioned OGGF proof envelope for an
// issued work hash. The proof is encoded as JSON-RPC hex bytes.
func (r *RPCClient) SubmitOGGAuxBlock(workHash, proofHex string) (bool, error) {
	if validateHexBytes(workHash) != nil || len(strings.TrimPrefix(workHash, "0x")) != 64 {
		return false, errors.New("invalid OGG work hash")
	}
	if err := validateHexBytes(proofHex); err != nil {
		return false, fmt.Errorf("invalid OGG AuxPoW proof: %v", err)
	}
	if !strings.HasPrefix(workHash, "0x") {
		workHash = "0x" + workHash
	}
	if !strings.HasPrefix(proofHex, "0x") {
		proofHex = "0x" + proofHex
	}
	accepted, _, err := r.SubmitForgeOGG(workHash, proofHex)
	return accepted, err
}

// GetOGGBlockReward obtains the reward split from the node's canonical
// consensus/oggreward package instead of duplicating reward math in the pool.
func (r *RPCClient) GetOGGBlockReward(height uint64) (*OGGReward, error) {
	resp, err := r.doPost(r.Url, "eth_getBlockReward", []interface{}{fmt.Sprintf("0x%x", height)})
	if err != nil {
		return nil, err
	}
	var reply OGGReward
	if err := unmarshalRPCResult(resp, &reply); err != nil {
		return nil, err
	}
	return &reply, nil
}

func (r *RPCClient) getBlockTemplate(request map[string]interface{}) (*BlockTemplateReply, error) {
	resp, err := r.doPost(r.Url, "getblocktemplate", []interface{}{request})
	if err != nil {
		return nil, err
	}
	var reply BlockTemplateReply
	if err := unmarshalRPCResult(resp, &reply); err != nil {
		return nil, err
	}
	return &reply, nil
}

// GetDigibyteBlockTemplate is the Digibyte GBT entry point. Phase 5B will use
// the MWEB/SegWit fields above when assembling the actual parent block.
// D13 has a native DigiByte Scrypt parent, never an LTC RPC alias.
func (r *RPCClient) GetDigibyteBlockTemplate() (*BlockTemplateReply, error) {
	resp, err := r.doPost(r.Url, "getblocktemplate", []interface{}{
		map[string]interface{}{"rules": []string{"segwit"}}, "scrypt",
	})
	if err != nil {
		return nil, err
	}
	var reply BlockTemplateReply
	if err := unmarshalRPCResult(resp, &reply); err != nil {
		return nil, err
	}
	if err := ValidateDigibyteScryptTemplate(&reply); err != nil {
		return nil, err
	}
	return &reply, nil
}

func ValidateDigibyteScryptTemplate(t *BlockTemplateReply) error {
	if t == nil || t.PowAlgo != "scrypt" || t.PowAlgoID == nil || *t.PowAlgoID != 1 ||
		uint32(t.Version)&0xf00 != 0 || t.MWEB != "" {
		return errors.New("D13 requires an explicitly identified DigiByte Scrypt template")
	}
	for _, rule := range t.Rules {
		if strings.HasPrefix(rule, "!") && rule != "!segwit" && rule != "!csv" && rule != "!taproot" && rule != "!algolock" {
			return errors.New("unsupported mandatory DigiByte template rule")
		}
	}
	return nil
}

// GetDogecoinBlockTemplate deliberately does not send Digibyte-specific rules.
func (r *RPCClient) GetDogecoinBlockTemplate() (*BlockTemplateReply, error) {
	return r.getBlockTemplate(map[string]interface{}{})
}

// GetBlockTemplate remains a compatibility alias for code paths that only need
// a Digibyte-family parent template.
func (r *RPCClient) GetBlockTemplate() (*BlockTemplateReply, error) {
	return r.GetDigibyteBlockTemplate()
}

// SubmitRawBlock submits a fully serialized Bitcoin-family block. A null result
// means acceptance; a non-empty string result is a daemon rejection reason.
func (r *RPCClient) SubmitRawBlock(blockHex string) (bool, error) {
	if err := validateHexBytes(blockHex); err != nil {
		return false, fmt.Errorf("invalid raw block: %v", err)
	}
	accepted, _, err := r.SubmitForgeBlock(blockHex)
	return accepted, err
}

// CreateAuxBlock exposes Dogecoin's getauxblock compatibility path.
func (r *RPCClient) CreateAuxBlock() (*AuxBlockTemplate, error) {
	resp, err := r.doPost(r.Url, "getauxblock", []interface{}{})
	if err != nil {
		return nil, err
	}
	var reply AuxBlockTemplate
	if err := unmarshalRPCResult(resp, &reply); err != nil {
		return nil, err
	}
	return &reply, nil
}

// SubmitAuxBlock submits a serialized Dogecoin AuxPoW proof through getauxblock.
func (r *RPCClient) SubmitAuxBlock(hash, auxpowHex string) (bool, error) {
	if validateHexBytes(hash) != nil || len(strings.TrimPrefix(hash, "0x")) != 64 {
		return false, errors.New("invalid aux block hash")
	}
	if err := validateHexBytes(auxpowHex); err != nil {
		return false, fmt.Errorf("invalid auxpow: %v", err)
	}
	hash = strings.TrimPrefix(hash, "0x")
	auxpowHex = strings.TrimPrefix(auxpowHex, "0x")
	resp, err := r.doPost(r.Url, "getauxblock", []interface{}{hash, auxpowHex})
	if err != nil {
		return false, err
	}
	var accepted bool
	if err := unmarshalRPCResult(resp, &accepted); err != nil {
		return false, err
	}
	return accepted, nil
}
