package proxy

import (
	"github.com/HawyUkr/open-progpow-pool/api"
	"github.com/HawyUkr/open-progpow-pool/payouts"
	"github.com/HawyUkr/open-progpow-pool/policy"
	"github.com/HawyUkr/open-progpow-pool/storage"
)

type Config struct {
	Name                  string        `json:"name"`
	Proxy                 Proxy         `json:"proxy"`
	Api                   api.ApiConfig `json:"api"`
	Upstream              []Upstream    `json:"upstream"`
	UpstreamCheckInterval string        `json:"upstreamCheckInterval"`

	Threads int `json:"threads"`

	Coin  string         `json:"coin"`
	Redis storage.Config `json:"redis"`

	BlockUnlocker payouts.UnlockerConfig `json:"unlocker"`
	Payouts       payouts.PayoutsConfig  `json:"payouts"`

	NewrelicName    string `json:"newrelicName"`
	NewrelicKey     string `json:"newrelicKey"`
	NewrelicVerbose bool   `json:"newrelicVerbose"`
	NewrelicEnabled bool   `json:"newrelicEnabled"`
}

type Proxy struct {
	Enabled              bool   `json:"enabled"`
	Listen               string `json:"listen"`
	LimitHeadersSize     int    `json:"limitHeadersSize"`
	LimitBodySize        int64  `json:"limitBodySize"`
	BehindReverseProxy   bool   `json:"behindReverseProxy"`
	BlockRefreshInterval string `json:"blockRefreshInterval"`
	Difficulty           int64  `json:"difficulty"`
	StateUpdateInterval  string `json:"stateUpdateInterval"`
	HashrateExpiration   string `json:"hashrateExpiration"`

	Policy policy.Config `json:"policy"`

	MaxFails    int64 `json:"maxFails"`
	HealthCheck bool  `json:"healthCheck"`

	Stratum Stratum     `json:"stratum"`
	Forge   ForgeConfig `json:"forge"`
}

type Stratum struct {
	Enabled  bool   `json:"enabled"`
	Listen   string `json:"listen"`
	Timeout  string `json:"timeout"`
	MaxConn  int    `json:"maxConn"`
	TLS      bool   `json:"tls"`
	CertFile string `json:"certFile"`
	KeyFile  string `json:"keyFile"`
}

type Upstream struct {
	Name     string `json:"name"`
	Url      string `json:"url"`
	Timeout  string `json:"timeout"`
	Username string `json:"username"`
	Password string `json:"password"`
}

// ForgeConfig enables the Scrypt merged-mining path. When disabled the pool
// retains its legacy ProgPoW behavior unchanged.
type ForgeConfig struct {
	ExternalChainPolicy  string                `json:"externalChainPolicy,omitempty"`
	VariableDifficulty   ForgeVarDiffConfig    `json:"variableDifficulty"`
	MultiChain           bool                  `json:"multiChain"`
	AdditionalAux        []ForgeAuxEndpoint    `json:"additionalAux"`
	Enabled              bool                  `json:"enabled"`
	RetainExternalWork   bool                  `json:"retainExternalWork"`
	AccountingJournalDir string                `json:"accountingJournalDir"`
	Accounting           ForgeAccountingLimits `json:"accounting"`
	Digibyte             ForgeChainEndpoint    `json:"digibyte"`
	Dogecoin             ForgeChainEndpoint    `json:"dogecoin"`
	ExtraNonce1Size      int                   `json:"extranonce1Size"`
	ExtraNonce2Size      int                   `json:"extranonce2Size"`
	JobBacklog           int                   `json:"jobBacklog"`
	MaxNTimeDriftSec     int64                 `json:"maxNTimeDriftSec"`
	TemplateValiditySec  int64                 `json:"templateValiditySec"`
	TemplateQuorum       uint64                `json:"templateQuorum"`
	TemplateSigners      []ForgeSigner         `json:"templateSigners"`
}

// ForgeSigner is an external quorum signer.  Only its public Ethereum-style
// address and service URL belong in pool configuration; private keys must
// never be present in the pool process or repository.
type ForgeSigner struct {
	Name    string `json:"name"`
	URL     string `json:"url"`
	Address string `json:"address"`
	Timeout string `json:"timeout"`
}

// ForgeChainEndpoint is a Bitcoin-family JSON-RPC endpoint plus the exact
// protocol-owned payout script that must appear in parent/candidate coinbases.
type ForgeChainEndpoint struct {
	Name         string `json:"name"`
	Url          string `json:"url"`
	Timeout      string `json:"timeout"`
	Username     string `json:"username"`
	Password     string `json:"password"`
	PayoutScript string `json:"payoutScript"`
}
