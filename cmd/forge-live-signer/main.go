// forge-live-signer binds the FORGE validator to operator-pinned native chain nodes.
package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/HawyUkr/open-progpow-pool/forgepool"
	"github.com/HawyUkr/open-progpow-pool/forgesigner"
	"github.com/HawyUkr/open-progpow-pool/rpc"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

type endpoint struct {
	URL      string `json:"url"`
	Username string `json:"username"`
	Password string `json:"password"`
	Genesis  string `json:"genesis"`
}
type config struct {
	RegistryBlock        uint64                `json:"registryBlock,omitempty"`
	RegistryChainID      uint64                `json:"registryChainId,omitempty"`
	RegistryRPC          endpoint              `json:"registryRpc,omitempty"`
	TreasuryDestinations []treasuryDestination `json:"treasuryDestinations,omitempty"`
	OptionalSOQBlock     uint64                `json:"optionalSoqBlock,omitempty"`
	FastDigibyteHealth   bool                  `json:"fastDigibyteHealth"`
	ExternalChainPolicy  string                `json:"externalChainPolicy,omitempty"`
	Multi                []multiEndpoint       `json:"multi,omitempty"`
	KeyFile              string                `json:"keyFile"`
	Listen               string                `json:"listen"`
	SignerAddress        string                `json:"signerAddress"`
	DogeScript           string                `json:"dogeScript"`
	Digibyte             endpoint              `json:"digibyte"`
	Dogecoin             endpoint              `json:"dogecoin"`
}

func (e endpoint) validate() error {
	u, err := url.Parse(e.URL)
	if err != nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return errors.New("RPC endpoint must be an explicit loopback HTTP URL")
	}
	ip := net.ParseIP(u.Hostname())
	port, err := strconv.Atoi(u.Port())
	if ip == nil || !ip.IsLoopback() || err != nil || port < 1 || port > 65535 {
		return errors.New("RPC endpoint must use a loopback IP and explicit port")
	}
	genesis, err := hex.DecodeString(e.Genesis)
	if err != nil || len(genesis) != 32 || strings.Trim(e.Genesis, "0") == "" {
		return errors.New("expected mainnet genesis hash required")
	}
	if e.Username == "" || e.Password == "" {
		return errors.New("RPC authentication required")
	}
	return nil
}
func (c config) validate() error {
	if _, err := forgepool.ExternalChainPolicyMask(c.ExternalChainPolicy); err != nil {
		return err
	}
	if c.ExternalChainPolicy == forgepool.ExternalChainPolicyDGBDOGE || c.ExternalChainPolicy == forgepool.ExternalChainPolicyDGBDOGESQ {
		if err := validateMultiConfig(c); err != nil {
			return err
		}
	}
	host, portText, err := net.SplitHostPort(c.Listen)
	port, portErr := strconv.Atoi(portText)
	ip := net.ParseIP(host)
	if err != nil || ip == nil || !ip.IsLoopback() || portErr != nil || port < 1 || port > 65535 {
		return errors.New("signer must listen on an explicit loopback IP and port")
	}
	if c.KeyFile == "" || !common.IsHexAddress(c.SignerAddress) || common.HexToAddress(c.SignerAddress) == (common.Address{}) {
		return errors.New("key path and expected signer address required")
	}
	if c.ExternalChainPolicy == forgepool.ExternalChainPolicyRegistry {
		return validateFlexibleConfig(c)
	}
	script, err := hex.DecodeString(c.DogeScript)
	if err != nil || len(script) != 25 || script[0] != 0x76 || script[1] != 0xa9 || script[2] != 0x14 || script[23] != 0x88 || script[24] != 0xac {
		return errors.New("pilot requires a standard DOGE P2PKH treasury script")
	}
	if c.Digibyte.Genesis != "7497ea1b465eb39f1c8f507bc877078fe016d6fcb6dfad3a64c98dcc6e1e8496" {
		return errors.New("D13 requires DigiByte mainnet genesis")
	}
	if err := c.Digibyte.validate(); err != nil {
		return err
	}
	return c.Dogecoin.validate()
}

// No environment proxy, redirects, unlimited responses, or RPC error bodies.
var checkHTTP = &http.Client{Timeout: 6 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("RPC redirect refused") }}

func (e endpoint) call(ctx context.Context, method string, params interface{}, out interface{}) error {
	payload, err := json.Marshal(map[string]interface{}{"jsonrpc": "1.0", "id": 1, "method": method, "params": params})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.URL, bytes.NewReader(payload))
	if err != nil {
		return errors.New("invalid RPC request")
	}
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(e.Username, e.Password)
	res, err := checkHTTP.Do(req)
	if err != nil {
		return errors.New("mainnet RPC transport unavailable")
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return errors.New("mainnet RPC HTTP failure")
	}
	body, err := ioutil.ReadAll(io.LimitReader(res.Body, 1024*1024+1))
	if err != nil || len(body) > 1024*1024 {
		return errors.New("mainnet RPC response size/read failure")
	}
	var reply struct {
		ID     int             `json:"id"`
		Result json.RawMessage `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	if json.Unmarshal(body, &reply) != nil || reply.ID != 1 || len(reply.Result) == 0 || (len(reply.Error) > 0 && string(reply.Error) != "null") {
		return errors.New("mainnet RPC invalid/error response")
	}
	return json.Unmarshal(reply.Result, out)
}
func (e endpoint) healthy(ctx context.Context) error {
	var info struct {
		Chain                string
		Blocks               int64
		Headers              int64
		InitialBlockDownload *bool  `json:"initialblockdownload"`
		BestBlockHash        string `json:"bestblockhash"`
	}
	if err := e.call(ctx, "getblockchaininfo", []interface{}{}, &info); err != nil {
		return err
	}
	if info.Chain != "main" || info.InitialBlockDownload == nil || *info.InitialBlockDownload || info.Blocks <= 0 || info.Headers < info.Blocks || info.Headers-info.Blocks > 2 {
		return errors.New("mainnet chain/sync check failed")
	}
	var genesis string
	if err := e.call(ctx, "getblockhash", []interface{}{0}, &genesis); err != nil {
		return err
	}
	if genesis != e.Genesis {
		return errors.New("mainnet genesis pin mismatch")
	}
	var network struct {
		Connections   int
		NetworkActive *bool `json:"networkactive"`
	}
	if err := e.call(ctx, "getnetworkinfo", []interface{}{}, &network); err != nil {
		return err
	}
	if network.Connections < 1 || (network.NetworkActive != nil && !*network.NetworkActive) {
		return errors.New("mainnet node has no active peers")
	}
	var header struct{ Time int64 }
	if err := e.call(ctx, "getblockheader", []interface{}{info.BestBlockHash, true}, &header); err != nil {
		return err
	}
	now := time.Now().Unix()
	if !rpc.NativeTipTimeValid(e.Genesis, now, header.Time) {
		return errors.New("mainnet tip age check failed")
	}
	return nil
}

type source struct {
	raw       forgesigner.RPCSource
	dgb, doge endpoint
}

func (s source) DigibyteTemplate(ctx context.Context) (*rpc.BlockTemplateReply, error) {
	if err := s.dgb.healthy(ctx); err != nil {
		return nil, err
	}
	return s.raw.DigibyteTemplate(ctx)
}
func (s source) DogecoinTemplate(ctx context.Context) (*rpc.BlockTemplateReply, error) {
	if err := s.doge.healthy(ctx); err != nil {
		return nil, err
	}
	return s.raw.DogecoinTemplate(ctx)
}
func (s source) Templates(ctx context.Context) (*rpc.BlockTemplateReply, *rpc.BlockTemplateReply, error) {
	l, e := s.DigibyteTemplate(ctx)
	if e != nil {
		return nil, nil, e
	}
	d, e := s.DogecoinTemplate(ctx)
	return l, d, e
}
func (s source) ValidateDogeProposal(ctx context.Context, b []byte) error {
	if e := s.doge.healthy(ctx); e != nil {
		return e
	}
	return s.raw.ValidateDogeProposal(ctx, b)
}

type backend struct{ k *ecdsa.PrivateKey }

func (b backend) Address() common.Address { return crypto.PubkeyToAddress(b.k.PublicKey) }
func (b backend) SignDigest(ctx context.Context, h common.Hash) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return crypto.Sign(h.Bytes(), b.k)
}

func run() error {
	pubkey := flag.String("pubkey", "", "print the public enode key for an existing key file")
	address := flag.String("address", "", "print the public approval address for an existing key file")
	check := flag.Bool("check", false, "validate configuration, signer identity and both mainnet nodes without listening")
	genkey := flag.String("genkey", "", "create a new exclusive mode-0600 signer key; prints only its public address")
	flag.Parse()
	if *address != "" {
		key, err := crypto.LoadECDSA(*address)
		if err != nil {
			return errors.New("cannot read approval key")
		}
		fmt.Println(crypto.PubkeyToAddress(key.PublicKey).Hex())
		return nil
	}
	if *pubkey != "" {
		k, err := crypto.LoadECDSA(*pubkey)
		if err != nil {
			return errors.New("cannot read node key")
		}
		fmt.Println(hex.EncodeToString(crypto.FromECDSAPub(&k.PublicKey)[1:]))
		return nil
	}
	if *genkey != "" {
		k, err := crypto.GenerateKey()
		if err != nil {
			return errors.New("key generation failed")
		}
		f, err := os.OpenFile(*genkey, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return errors.New("key destination must be new")
		}
		_, err = f.WriteString(hex.EncodeToString(crypto.FromECDSA(k)) + "\n")
		if err == nil {
			err = f.Sync()
		}
		closeErr := f.Close()
		if err != nil || closeErr != nil {
			return errors.New("key write failed")
		}
		fmt.Println(crypto.PubkeyToAddress(k.PublicKey).Hex())
		return nil
	}
	if flag.NArg() != 1 {
		return errors.New("usage: forge-live-signer [-check] CONFIG | -genkey NEW_PATH")
	}
	f, err := os.Open(flag.Arg(0))
	if err != nil {
		return errors.New("cannot open signer configuration")
	}
	defer f.Close()
	var c config
	d := json.NewDecoder(io.LimitReader(f, 65537))
	d.DisallowUnknownFields()
	if d.Decode(&c) != nil {
		return errors.New("invalid signer JSON")
	}
	var extra interface{}
	if d.Decode(&extra) != io.EOF {
		return errors.New("trailing signer JSON")
	}
	if err = c.validate(); err != nil {
		return err
	}
	k, err := crypto.LoadECDSA(c.KeyFile)
	if err != nil {
		return errors.New("cannot load signer key")
	}
	b := backend{k}
	if b.Address() != common.HexToAddress(c.SignerAddress) {
		return errors.New("signer key does not match expected public identity")
	}
	if len(c.Multi) > 0 {
		return runMulti(c, k, *check)
	}
	s := source{raw: forgesigner.RPCSource{Digibyte: rpc.NewRPCClient("dgb-mainnet", c.Digibyte.URL, "6s", c.Digibyte.Username, c.Digibyte.Password), Dogecoin: rpc.NewRPCClient("doge-mainnet", c.Dogecoin.URL, "6s", c.Dogecoin.Username, c.Dogecoin.Password)}, dgb: c.Digibyte, doge: c.Dogecoin}
	if err = c.Digibyte.healthy(context.Background()); err != nil {
		return fmt.Errorf("Digibyte: %w", err)
	}
	if err = c.Dogecoin.healthy(context.Background()); err != nil {
		return fmt.Errorf("Dogecoin: %w", err)
	}
	if *check {
		fmt.Println("PASS: mainnet RPC health/genesis pins and signer identity; mining acceptance remains untested")
		return nil
	}
	script, _ := hex.DecodeString(c.DogeScript)
	validator, err := forgesigner.New(s, b, forgesigner.Policy{DogePayoutScript: script, MaxValidity: 300, MaxAge: 300}, nil)
	if err != nil {
		return err
	}
	server := &http.Server{Addr: c.Listen, Handler: validator, ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 45 * time.Second, IdleTimeout: 15 * time.Second, MaxHeaderBytes: 8192}
	log.Printf("FORGE live pilot signer %s on %s; DGB+DOGE mainnet pinned", b.Address().Hex(), c.Listen)
	return server.ListenAndServe()
}
func main() {
	if err := run(); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}
