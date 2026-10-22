package rpc

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

const maxValidationReplyBytes = 32 * 1024 * 1024

var validationTransport = &http.Transport{
	Proxy:        nil,
	DialContext:  (&net.Dialer{Timeout: 3 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
	MaxIdleConns: 16, MaxIdleConnsPerHost: 4,
	IdleConnTimeout: 30 * time.Second, ResponseHeaderTimeout: 6 * time.Second,
	DisableCompression: true,
}

type validationRoundTripper struct{ ctx context.Context }

func (t validationRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	if err := t.ctx.Err(); err != nil {
		return nil, err
	}
	if err := request.Context().Err(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(request.Context())
	if t.ctx.Done() != nil {
		go func() {
			select {
			case <-t.ctx.Done():
				cancel()
			case <-ctx.Done():
			}
		}()
	}
	response, err := validationTransport.RoundTrip(request.WithContext(ctx))
	if err != nil {
		cancel()
		return nil, err
	}
	response.Body = &validationBody{ReadCloser: response.Body, remaining: maxValidationReplyBytes, cancel: cancel}
	return response, nil
}

type validationBody struct {
	io.ReadCloser
	remaining int64
	cancel    context.CancelFunc
}

func (b *validationBody) Close() error {
	if b.cancel != nil {
		b.cancel()
	}
	return b.ReadCloser.Close()
}

func (b *validationBody) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if b.remaining == 0 {
		var probe [1]byte
		n, err := b.ReadCloser.Read(probe[:])
		if n != 0 {
			return 0, errors.New("native validation response exceeds limit")
		}
		return 0, err
	}
	if int64(len(p)) > b.remaining {
		p = p[:b.remaining]
	}
	n, err := b.ReadCloser.Read(p)
	b.remaining -= int64(n)
	return n, err
}

// NewValidationRPCClient confines native checks to their request lifetime and
// operator-configured loopback endpoint. Redirects and environment proxies are
// excluded so native RPC credentials cannot follow a different destination.
func NewValidationRPCClient(ctx context.Context, name, endpoint, timeout, username, password string) (*RPCClient, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, errors.New("native validation requires a loopback RPC endpoint")
	}
	ip := net.ParseIP(u.Hostname())
	port, portErr := strconv.Atoi(u.Port())
	duration, durationErr := time.ParseDuration(timeout)
	if ctx == nil || ip == nil || !ip.IsLoopback() || portErr != nil || port < 1 || port > 65535 || durationErr != nil || duration <= 0 || duration > 6*time.Second {
		return nil, errors.New("invalid native validation context, endpoint or timeout")
	}
	return &RPCClient{Name: name, Url: endpoint, Username: username, Password: password,
		client: &http.Client{Timeout: duration, Transport: validationRoundTripper{ctx},
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
