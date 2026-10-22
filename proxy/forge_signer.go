package proxy

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/HawyUkr/open-progpow-pool/forgepool"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

type forgeSignerClient struct {
	name    string
	url     string
	address common.Address
	client  *http.Client
}

type forgeSignRequest = forgepool.TemplateSignRequest
type forgeSignResponse = forgepool.TemplateSignResponse

func newForgeSigner(c ForgeSigner) (*forgeSignerClient, error) {
	if strings.TrimSpace(c.URL) == "" || !common.IsHexAddress(c.Address) {
		return nil, errors.New("external signer requires URL and valid address")
	}
	endpoint, err := url.Parse(c.URL)
	if err != nil || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return nil, errors.New("invalid signer endpoint")
	}
	ip := net.ParseIP(endpoint.Hostname())
	if endpoint.Scheme != "https" && (endpoint.Scheme != "http" || ip == nil || !ip.IsLoopback()) {
		return nil, errors.New("remote signers require HTTPS")
	}
	d := 5 * time.Second
	if c.Timeout != "" {
		var err error
		d, err = time.ParseDuration(c.Timeout)
		if err != nil || d <= 0 {
			return nil, errors.New("invalid external signer timeout")
		}
	}
	transport := &http.Transport{Proxy: nil,
		DialContext:         (&net.Dialer{Timeout: d, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout: d, ResponseHeaderTimeout: d,
		MaxIdleConns: 4, MaxIdleConnsPerHost: 2, IdleConnTimeout: 30 * time.Second,
		DisableCompression: true}
	return &forgeSignerClient{name: c.Name, url: c.URL, address: common.HexToAddress(c.Address),
		client: &http.Client{Timeout: d, Transport: transport,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

func (s *forgeSignerClient) sign(t forgepool.ExternalTemplate, evidence *forgepool.DogeSigningEvidence, workHash *common.Hash) ([]byte, error) {
	var bound common.Hash
	if workHash != nil {
		bound = *workHash
	}

	digest, err := forgepool.TemplateSigningDigest(t, bound)
	if err != nil {
		return nil, err
	}

	reqBody, err := json.Marshal(forgeSignRequest{
		Domain:   forgepool.TemplateDomain(t),
		Digest:   digest.Hex(),
		Template: t,
		WorkHash: workHash,
		Doge:     evidence,
	})
	if err != nil {
		return nil, err
	}
	if len(reqBody) > forgepool.MaxSignRequestBytes {
		return nil, errors.New("signer proposal too large")
	}
	req, err := http.NewRequest("POST", s.url, bytes.NewReader(reqBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("signer %s returned HTTP %d", s.name, resp.StatusCode)
	}
	var out forgeSignResponse
	decoder := json.NewDecoder(io.LimitReader(resp.Body, 4097))
	if err := decoder.Decode(&out); err != nil {
		return nil, err
	}
	var trailing interface{}
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, errors.New("invalid trailing signer response")
	}
	if !common.IsHexAddress(out.Signer) || common.HexToAddress(out.Signer) != s.address {
		return nil, errors.New("signer response address mismatch")
	}
	raw, err := hex.DecodeString(strings.TrimPrefix(out.Signature, "0x"))
	if err != nil || len(raw) != 65 {
		return nil, errors.New("signer response must contain a 65-byte signature")
	}
	if raw[64] >= 27 {
		raw[64] -= 27
	}
	pub, err := crypto.SigToPub(digest.Bytes(), raw)
	if err != nil || crypto.PubkeyToAddress(*pub) != s.address {
		return nil, errors.New("template signature does not match configured signer")
	}
	return raw, nil
}
