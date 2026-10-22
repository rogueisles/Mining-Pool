package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/HawyUkr/open-progpow-pool/forgepool"
	"github.com/ethereum/go-ethereum/common"
)

type observedRequestBody struct{ reads int32 }

func (b *observedRequestBody) Read([]byte) (int, error) {
	atomic.AddInt32(&b.reads, 1)
	return 0, io.EOF
}
func (*observedRequestBody) Close() error { return nil }

func TestMultiSignerOverloadDoesNotDecodeOrQueue(t *testing.T) {
	s := &multiService{}
	if !s.admit() {
		t.Fatal("first admission refused")
	}
	defer func() { <-s.admission }()
	for i := 0; i < 32; i++ {
		body := &observedRequestBody{}
		r := httptest.NewRequest(http.MethodPost, "/", nil)
		r.Body = body
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != http.StatusServiceUnavailable || w.Header().Get("Retry-After") == "" || atomic.LoadInt32(&body.reads) != 0 {
			t.Fatal("overloaded request read its body or was not refused")
		}
	}
}

func TestMultiSignerCanceledRequestDoesNotDecode(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	body := &observedRequestBody{}
	r := httptest.NewRequest(http.MethodPost, "/", nil).WithContext(ctx)
	r.Body = body
	w := httptest.NewRecorder()
	(&multiService{}).ServeHTTP(w, r)
	if w.Code != http.StatusRequestTimeout || atomic.LoadInt32(&body.reads) != 0 {
		t.Fatal("canceled request reached decoding")
	}
}

func TestMultiSignerCancellationStopsNativeReadAndReleasesAdmission(t *testing.T) {
	entered := make(chan struct{}, 1)
	canceled := make(chan struct{}, 1)
	native := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			return
		}
		entered <- struct{}{}
		select {
		case <-r.Context().Done():
			canceled <- struct{}{}
		case <-time.After(3 * time.Second):
		}
	}))
	defer native.Close()
	s := &multiService{cfg: config{Multi: []multiEndpoint{{Symbol: "DGB", endpoint: endpoint{URL: native.URL}}}}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		now := uint64(time.Now().Unix())
		request := forgepool.MultiSignRequest{WorkHash: common.HexToHash("0x01"), DGB: &forgepool.ParentTemplate{},
			Template: forgepool.MultiTemplate{Version: 4, Mask: 1, ValidFrom: now - 1, Expires: now + 60,
				ParentCoinbaseHash: common.HexToHash("0x02"), ParentBranchHash: common.HexToHash("0x03"),
				ExtraSize: 8, ExtraOffset: 90, DGBPrevHash: common.HexToHash("0x04"), DGBBits: 1, ParentMinTime: uint32(now)}}
		body, err := json.Marshal(request)
		if err != nil {
			t.Error(err)
			return
		}
		r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(string(body))).WithContext(ctx)
		s.ServeHTTP(httptest.NewRecorder(), r)
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("native read did not begin")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("canceled signer handler remained active")
	}
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("native HTTP request retained a canceled caller")
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/", strings.NewReader("invalid")))
	if w.Code != http.StatusBadRequest {
		t.Fatal("admission was not released after cancellation")
	}
}
