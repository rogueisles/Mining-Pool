package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/HawyUkr/open-progpow-pool/forgepool"
	"github.com/ethereum/go-ethereum/common"
)

// Contact the required quorum concurrently, with bounded fallback requests.
// Slow validators must not serialize work or overload native validation RPCs.
func (rt *forgeRuntime) collectMultiSignatures(req forgepool.MultiSignRequest) ([][]byte, error) {
	if rt.templateQuorum == 0 || rt.templateQuorum > uint64(len(rt.signers)) {
		return nil, errors.New("invalid multi-chain signer quorum")
	}
	// Detach all evidence slices from the job before concurrent requests. Binding
	// the certificate after quorum must not race with late request serialization.
	body, err := json.Marshal(req)
	if err != nil || len(body) > 16*1024*1024 {
		return nil, errors.New("invalid multi-chain signing request")
	}
	var frozen forgepool.MultiSignRequest
	if err = json.Unmarshal(body, &frozen); err != nil {
		return nil, errors.New("invalid multi-chain signing evidence")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type result struct {
		address   common.Address
		signature []byte
		err       error
	}
	results := make(chan result, len(rt.signers))
	next, inflight := 0, 0
	launch := func() {
		client := rt.signers[next]
		next++
		inflight++
		go func(s *forgeSignerClient) {
			if s == nil {
				results <- result{err: errors.New("missing signer")}
				return
			}
			sig, err := s.signMultiContext(ctx, frozen)
			results <- result{address: s.address, signature: sig, err: err}
		}(client)
	}
	for next < len(rt.signers) && uint64(inflight) < rt.templateQuorum {
		launch()
	}
	// Native validation is shared infrastructure. Use the required quorum first,
	// then hedge one request at a time instead of flooding every validator.
	hedge := time.NewTimer(2 * time.Second)
	defer hedge.Stop()
	authorized := make(map[common.Address]bool, len(rt.authorized))
	for _, address := range rt.authorized {
		authorized[address] = true
	}
	seen := make(map[common.Address]bool)
	signatures := make([][]byte, 0, rt.templateQuorum)
	for inflight > 0 {
		select {
		case r := <-results:
			inflight--
			if r.err == nil && authorized[r.address] && !seen[r.address] {
				seen[r.address] = true
				signatures = append(signatures, r.signature)
				if uint64(len(signatures)) == rt.templateQuorum {
					return signatures, nil
				}
			}
			for next < len(rt.signers) && uint64(inflight) < rt.templateQuorum-uint64(len(signatures)) {
				launch()
			}
		case <-hedge.C:
			if next < len(rt.signers) && uint64(inflight) < rt.templateQuorum+1 {
				launch()
			}
			hedge.Reset(2 * time.Second)
		}
	}
	return nil, fmt.Errorf("multi-chain signer quorum unavailable (%d/%d)", len(signatures), rt.templateQuorum)
}
