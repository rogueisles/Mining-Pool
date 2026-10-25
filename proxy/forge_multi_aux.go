package proxy

import (
	"errors"
	"fmt"
	"github.com/HawyUkr/open-progpow-pool/forgepool"
	"github.com/HawyUkr/open-progpow-pool/rpc"
	"github.com/HawyUkr/open-progpow-pool/storage"
	"net"
	"net/url"
	"strconv"
	"strings"
)

type ForgeAuxEndpoint struct {
	PayoutAddress string             `json:"payoutAddress,omitempty"`
	Symbol        string             `json:"symbol"`
	ChainID       uint32             `json:"chainId"`
	Genesis       string             `json:"genesis"`
	Endpoint      ForgeChainEndpoint `json:"endpoint"`
}
type forgeAuxRuntime struct {
	symbol  string
	chainID uint32
	genesis string
	client  *rpc.RPCClient
	script  []byte
	address string
}

func knownExtraChain(symbol string) bool {
	return symbol == "PEP" || symbol == "DINGO" || symbol == "BELLS" || symbol == "SOQ" || storage.ValidRegisteredSymbol(symbol)
}
func (h forgeHits) extra() bool {
	for _, v := range h.Registered {
		if v.Hit {
			return true
		}
	}
	return h.Pepecoin || h.Dingocoin || h.Bellscoin || h.Soqucoin
}
func (h forgeHits) extraFor(symbol string) bool {
	switch symbol {
	case "PEP":
		return h.Pepecoin
	case "DINGO":
		return h.Dingocoin
	case "BELLS":
		return h.Bellscoin
	case "SOQ":
		return h.Soqucoin
	}
	for _, v := range h.Registered {
		if v.Symbol == symbol {
			return v.Hit
		}
	}
	return false
}

func auxiliaryClients(cfg []ForgeAuxEndpoint, flexible ...bool) ([]forgeAuxRuntime, error) {
	registered := len(flexible) > 0 && flexible[0]
	if registered && len(cfg) > 5 {
		return nil, errors.New("too many registered auxiliary endpoints")
	}
	if !registered && len(cfg) != 0 && len(cfg) != 3 && !(len(cfg) == 1 && cfg[0].Symbol == "SOQ") {
		return nil, errors.New("D14 requires all three additional auxiliary endpoints")
	}
	seen := map[uint32]bool{forgepool.OGGAuxChainID: true, forgepool.DOGEAuxChainID: true}
	symbols := map[string]bool{}
	var out []forgeAuxRuntime
	for _, c := range cfg {
		if (!registered && !knownExtraChain(c.Symbol)) || (registered && !validRegisteredSymbol(c.Symbol)) || symbols[c.Symbol] || c.ChainID == 0 || c.ChainID > 65535 || seen[c.ChainID] || len(c.Genesis) != 64 {
			return nil, errors.New("invalid or duplicate auxiliary configuration")
		}
		ids := map[string]uint32{"PEP": 63, "DINGO": 50, "BELLS": 16, "SOQ": forgepool.SOQAuxChainID}
		genesis := map[string]string{"PEP": "37981c0c48b8d48965376c8a42ece9a0838daadb93ff975cb091f57f8c2a5faa", "DINGO": "1a91e3dace36e2be3bf030a65679fe821aa1d6ef92e7c9902eb318182c355691", "BELLS": "e5be24df57c43a82d15c2f06bda961296948f8f8eb48501bed1efb929afe0698", "SOQ": forgepool.SOQMainnetGenesis}
		if registered {
			for symbol, id := range ids {
				if (c.Symbol == symbol) != (c.ChainID == id) {
					return nil, errors.New("registered conventional symbol and identity differ")
				}
			}
		}
		if registered && ((c.Symbol == "SOQ") != (c.ChainID == forgepool.SOQAuxChainID)) {
			return nil, errors.New("registered SOQ symbol and identity differ")
		}
		if !registered && (c.ChainID != ids[c.Symbol] || c.Genesis != genesis[c.Symbol]) {
			return nil, errors.New("auxiliary mainnet identity not pinned")
		}
		script, err := parseForgeScript(c.Endpoint.PayoutScript)
		if err != nil {
			return nil, err
		}
		if c.Symbol == "SOQ" && (!forgepool.ValidSOQScript(script) || (!registered && !strings.HasPrefix(c.PayoutAddress, "sq1p")) || c.PayoutAddress == "" || len(c.PayoutAddress) > 90) {
			return nil, errors.New("SOQ requires a native witness-v1 payout script and sq1p address")
		}
		ep := forgeEndpointDefaults(c.Endpoint, c.Symbol)
		u, e := url.Parse(ep.Url)
		if e != nil {
			return nil, errors.New("invalid auxiliary RPC URL")
		}
		ip := net.ParseIP(u.Hostname())
		port, e := strconv.Atoi(u.Port())
		if u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || ip == nil || !ip.IsLoopback() || e != nil || port < 1 || port > 65535 || ep.Username == "" || ep.Password == "" {
			return nil, errors.New("auxiliary RPC requires authenticated loopback endpoint")
		}
		seen[c.ChainID] = true
		symbols[c.Symbol] = true
		out = append(out, forgeAuxRuntime{c.Symbol, c.ChainID, c.Genesis, rpc.NewRPCClient(ep.Name, ep.Url, ep.Timeout, ep.Username, ep.Password), script, c.PayoutAddress})
	}
	return out, nil
}

func (rt *forgeRuntime) attachAdditionalAux(job *forgeJob, seq uint32) error {
	if len(rt.additionalAux) == 0 {
		return nil
	}
	if job.ExternalMask != forgepool.ExternalMask {
		return errors.New("D14 expanded jobs require DGB and DOGE together")
	}
	leaves := map[uint32]*forgepool.DogeCandidate{}
	symbols := map[string]uint32{}
	for _, aux := range rt.additionalAux {
		if err := aux.client.CheckAuxiliaryMainnet(aux.genesis); err != nil {
			return fmt.Errorf("%s: %w", aux.symbol, err)
		}
		if aux.symbol == "DINGO" {
			if err := aux.client.CheckDingocoinIdentity(); err != nil {
				return err
			}
		}
		tpl, err := aux.client.GetAuxiliaryBlockTemplate(aux.symbol == "BELLS")
		if err != nil {
			return fmt.Errorf("%s: %w", aux.symbol, err)
		}
		parent, err := tpl.ToParentTemplate()
		if err != nil {
			return err
		}
		candidate, err := forgepool.BuildAuxCandidate(parent, aux.script, seq, aux.chainID)
		if err != nil {
			return err
		}
		leaves[aux.chainID] = candidate
		symbols[aux.symbol] = aux.chainID
	}
	if err := job.Digibyte.SetAdditionalAux(leaves); err != nil {
		return err
	}
	job.AuxSymbols = symbols
	return nil
}

func validRegisteredSymbol(symbol string) bool {
	if len(symbol) < 2 || len(symbol) > 12 || symbol == "DGB" || symbol == "DOGE" || symbol == "OGG" {
		return false
	}
	for _, r := range symbol {
		if (r < 'A' || r > 'Z') && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}
