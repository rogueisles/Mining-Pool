package rpc

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestValidationClientRejectsRemoteAndCredentialURLs(t *testing.T) {
	for _, endpoint := range []string{"http://example.com:8332", "https://127.0.0.1:8332", "http://user:pass@127.0.0.1:8332", "http://127.0.0.1:8332/?secret=x", "http://127.0.0.1:8332/#x", "http://127.0.0.1:8332/private"} {
		if _, err := NewValidationRPCClient(context.Background(), "native", endpoint, "1s", "", ""); err == nil {
			t.Fatalf("accepted %q", endpoint)
		}
	}
}

func TestValidationClientDoesNotFollowNativeRedirect(t *testing.T) {
	var reached int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&reached, 1)
		w.Write([]byte(`{"id":0,"result":null,"error":null}`))
	}))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	client, err := NewValidationRPCClient(context.Background(), "native", redirect.URL, "1s", "private-user", "private-password")
	if err != nil {
		t.Fatal(err)
	}
	if err := client.ValidateAuxiliaryProposal(context.Background(), make([]byte, 80)); err == nil || atomic.LoadInt32(&reached) != 0 {
		t.Fatal("native redirect followed or accepted")
	}
	if validationTransport.Proxy != nil {
		t.Fatal("native transport uses an environment proxy")
	}
}

func TestValidationBodyExactLimitAndOverflow(t *testing.T) {
	for _, input := range []string{"1234", "12345"} {
		body := &validationBody{ReadCloser: io.NopCloser(strings.NewReader(input)), remaining: 4}
		data, err := io.ReadAll(body)
		if string(data) != "1234" || (err != nil) != (len(input) > 4) {
			t.Fatal("native response bound not enforced", string(data), err)
		}
	}
}

func TestValidationClientPreservesIndividualRequestCancellation(t *testing.T) {
	var reached int32
	native := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&reached, 1)
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			return
		}
		select {
		case <-r.Context().Done():
		case <-time.After(time.Second):
		}
	}))
	defer native.Close()
	client, err := NewValidationRPCClient(context.Background(), "native", native.URL, "1s", "", "")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := client.ValidateAuxiliaryProposal(ctx, make([]byte, 80)); err == nil || atomic.LoadInt32(&reached) != 0 {
		t.Fatal("already canceled per-call context was replaced")
	}
	ctx, cancel = context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := client.ValidateAuxiliaryProposal(ctx, make([]byte, 80)); err == nil || time.Since(start) > 500*time.Millisecond {
		t.Fatal("shorter per-call deadline was replaced")
	}
}
