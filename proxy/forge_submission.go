package proxy

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"sort"
	"time"

	"github.com/HawyUkr/open-progpow-pool/forgepool"
	"github.com/HawyUkr/open-progpow-pool/rpc"
	"github.com/HawyUkr/open-progpow-pool/storage"
)

type forgeJournalRecord struct {
	Version  int                       `json:"version"`
	Receipt  storage.ForgeShare        `json:"receipt"`
	Intent   *forgeSubmissionIntent    `json:"intent,omitempty"`
	Credit   *storage.ForgeShare       `json:"credit,omitempty"`
	Credited bool                      `json:"credited,omitempty"`
	External *forgeExternalObservation `json:"external,omitempty"`
}

// Only public submission material and an endpoint fingerprint are persisted.
// Never serialize RPCClient: it contains credentials. Payloads are immutable.
type forgePendingBlock struct {
	ChainID   uint32
	Chain     string
	Endpoint  string
	Payload   string
	Digest    string
	BlockHash string
	NotBefore int64
	Expires   int64
	Sent      bool
	State     string
	Error     string
}

type forgeSubmissionIntent struct{ Blocks []forgePendingBlock }

func forgeEndpointID(c *rpc.RPCClient) string {
	if c == nil {
		return ""
	}
	h := sha256.Sum256([]byte(c.Name + "\x00" + c.Url))
	return hex.EncodeToString(h[:])
}

func forgePayloadDigest(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func (in *forgeSubmissionIntent) validate(receipt storage.ForgeShare) error {
	if len(in.Blocks) < 1 || len(in.Blocks) > 7 || receipt.ProofHash != "" {
		return errors.New("invalid FORGE intent")
	}
	seen := make(map[string]bool)
	for _, b := range in.Blocks {
		if seen[b.Chain] || (b.Chain != "DGB" && b.Chain != "DOGE" && b.Chain != "OGG" && !knownExtraChain(b.Chain)) {
			return errors.New("invalid intent chain")
		}
		seen[b.Chain] = true
		if b.State != "pending" && b.State != "accepted" && b.State != "rejected" && b.State != "noncanonical" {
			return errors.New("invalid intent state")
		}
		if b.State == "noncanonical" && (b.Chain == "OGG" || !b.Sent || b.Payload == "" || b.Error == "") {
			return errors.New("invalid external observation")
		}
		if b.State == "accepted" && (!b.Sent || b.Error != "") {
			return errors.New("invalid intent acceptance")
		}
		if b.Payload == "" && b.State == "rejected" {
			continue
		} // Local assembly failure, no RPC.
		endpoint, err := hex.DecodeString(b.Endpoint)
		if err != nil || len(endpoint) != 32 || b.Expires <= 0 || b.NotBefore < 0 || b.NotBefore >= b.Expires {
			return errors.New("invalid intent endpoint/window")
		}
		data, err := hex.DecodeString(b.Payload)
		if err != nil || len(data) < 80 || len(data) > 16*1024*1024 || forgePayloadDigest(data) != b.Digest {
			return errors.New("corrupt intent payload")
		}
		if storage.ValidRegisteredSymbol(b.Chain) {
			if b.ChainID == 0 || b.ChainID > 65535 || b.ChainID == 98 || b.ChainID == 21329 || b.ChainID == 20295 {
				return errors.New("invalid registered intent identity")
			}
			first := sha256.Sum256(data[:80])
			hash := sha256.Sum256(first[:])
			if b.BlockHash != "0x"+forgepool.DisplayHash(forgepool.Hash(hash)) {
				return errors.New("registered intent hash mismatch")
			}
		}
		switch b.Chain {
		case "DGB":
			if forgePayloadDigest(data[:80]) != receipt.ID {
				return errors.New("DGB intent share mismatch")
			}
			first := sha256.Sum256(data[:80])
			hash := sha256.Sum256(first[:])
			if b.BlockHash != "0x"+forgepool.DisplayHash(forgepool.Hash(hash)) {
				return errors.New("DGB intent hash mismatch")
			}
		case "DOGE", "PEP", "DINGO", "BELLS", "SOQ":
			first := sha256.Sum256(data[:80])
			hash := sha256.Sum256(first[:])
			if b.BlockHash != "0x"+forgepool.DisplayHash(forgepool.Hash(hash)) {
				return errors.New("DOGE intent hash mismatch")
			}
		case "OGG":
			proof, err := forgepool.DecodeOGGProof(data)
			if err != nil || forgePayloadDigest(proof.ParentHeader) != receipt.ID {
				return errors.New("OGG intent proof/share mismatch")
			}
			if proof.Template.ValidFrom != uint64(b.NotBefore) || proof.Template.Expires != uint64(b.Expires) {
				return errors.New("OGG intent validity mismatch")
			}
		}
	}
	return nil
}

func (r *rpcForgeSubmitter) prepareIntent(job *forgeJob, sol *forgeSolution, hits forgeHits) *forgeSubmissionIntent {
	in := &forgeSubmissionIntent{}
	chains := []string{"DGB", "DOGE", "PEP", "DINGO", "BELLS", "SOQ", "OGG"}
	if job != nil && job.Registry != nil {
		chains = []string{"OGG", "DGB", "DOGE", "PEP", "DINGO", "BELLS", "SOQ"}
	}
	for _, v := range hits.Registered {
		if v.Hit {
			chains = append(chains, v.Symbol)
		}
	}
	for _, chain := range chains {
		if (chain == "DGB" && !hits.Digibyte) || (chain == "DOGE" && !hits.Dogecoin) || (chain == "OGG" && !hits.OGG) || (knownExtraChain(chain) && !hits.extraFor(chain)) {
			continue
		}
		b := forgePendingBlock{Chain: chain, State: "pending", Expires: forgeNow(r.clock).Unix() + 300}
		var data []byte
		var err error
		switch chain {
		case "DGB":
			if job == nil || job.Digibyte == nil || !job.Digibyte.DigibyteActive {
				err = errors.New("inactive Digibyte FORGE submission")
				break
			}
			b.Endpoint = forgeEndpointID(r.dgb)
			data, err = job.Digibyte.FullBlock(sol.ExtraNonce1, sol.ExtraNonce2, sol.NTime, sol.Nonce)
			first := sha256.Sum256(sol.Header)
			hash := sha256.Sum256(first[:])
			b.BlockHash = "0x" + forgepool.DisplayHash(forgepool.Hash(hash))
		case "DOGE":
			if job == nil || job.Digibyte == nil || job.Digibyte.DOGE == nil {
				err = errors.New("inactive Dogecoin FORGE submission")
				break
			}
			b.Endpoint = forgeEndpointID(r.doge)
			b.BlockHash = "0x" + job.Digibyte.DOGE.DisplayHash
			var proof *forgepool.DogeAuxPow
			proof, err = job.Digibyte.BuildDogeAuxPow(sol.ExtraNonce1, sol.ExtraNonce2, sol.NTime, sol.Nonce)
			if err == nil {
				err = job.Digibyte.ValidateDogeAuxPow(proof)
			}
			if err == nil {
				data, err = job.Digibyte.DogeAuxPowBlock(sol.ExtraNonce1, sol.ExtraNonce2, sol.NTime, sol.Nonce)
			}
		case "PEP", "DINGO", "BELLS", "SOQ":
			id, ok := job.AuxSymbols[chain]
			if !ok || job.Digibyte.AdditionalAux[id] == nil {
				err = errors.New("missing additional auxiliary candidate")
				break
			}
			b.Endpoint = forgeEndpointID(r.auxClients[chain])
			b.BlockHash = "0x" + job.Digibyte.AdditionalAux[id].DisplayHash
			data, err = job.Digibyte.AuxBlockFor(id, sol.ExtraNonce1, sol.ExtraNonce2, sol.NTime, sol.Nonce)
		case "OGG":
			b.Endpoint = forgeEndpointID(job.OGGRPC)
			err = job.Signed.validate(job, r.authorized, r.quorum, forgeNow(r.clock))
			if err == nil {
				b.NotBefore = int64(job.Signed.Template.ValidFrom)
				b.Expires = int64(job.Signed.Template.Expires)
				data, err = forgeEncodeJobProof(job, sol)
			}
		}
		if storage.ValidRegisteredSymbol(chain) {
			id, ok := job.AuxSymbols[chain]
			if !ok || job.Digibyte.AdditionalAux[id] == nil {
				err = errors.New("missing registered auxiliary candidate")
			} else {
				b.ChainID = id
				b.Endpoint = forgeEndpointID(r.auxClients[chain])
				b.BlockHash = "0x" + job.Digibyte.AdditionalAux[id].DisplayHash
				data, err = job.Digibyte.AuxBlockFor(id, sol.ExtraNonce1, sol.ExtraNonce2, sol.NTime, sol.Nonce)
			}
		}
		if err != nil || b.Endpoint == "" {
			b.State, b.Error = "rejected", "local block assembly or endpoint validation failed"
		} else {
			b.Payload, b.Digest = hex.EncodeToString(data), forgePayloadDigest(data)
		}
		in.Blocks = append(in.Blocks, b)
	}
	return in
}

func (r *rpcForgeSubmitter) intentClient(b forgePendingBlock) *rpc.RPCClient {
	var clients []*rpc.RPCClient
	switch b.Chain {
	case "DGB":
		clients = []*rpc.RPCClient{r.dgb}
	case "DOGE":
		clients = []*rpc.RPCClient{r.doge}
	case "PEP", "DINGO", "BELLS", "SOQ":
		clients = []*rpc.RPCClient{r.auxClients[b.Chain]}
	case "OGG":
		clients = r.issuers
	}
	if storage.ValidRegisteredSymbol(b.Chain) {
		clients = []*rpc.RPCClient{r.auxClients[b.Chain]}
	}
	for _, c := range clients {
		if c != nil && forgeEndpointID(c) == b.Endpoint {
			return c
		}
	}
	return nil
}

func (q *forgeAccountingQueue) beginIntent(receipt storage.ForgeShare, in *forgeSubmissionIntent) (*forgeQueuedShare, error) {
	if err := receipt.Validate(); err != nil {
		return nil, err
	}
	if err := in.validate(receipt); err != nil {
		return nil, err
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.byID[receipt.ID] != nil {
		return nil, errForgeDuplicate
	}
	wasPaused := q.pausedLocked(time.Now())
	if wasPaused {
		q.priorityOverflow++
	}
	item := q.appendLocked(receipt)
	item.intent, item.active = in, true
	err := q.persist(item)
	if !wasPaused && (len(q.pending) > q.limits.MaxPendingRecords || q.pendingBytes > q.limits.MaxPendingBytes) {
		q.priorityOverflow++
	}
	q.pausedLocked(time.Now())
	return item, err
}

func (q *forgeAccountingQueue) saveIntent(item *forgeQueuedShare, in forgeSubmissionIntent) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	copyIn := in
	copyIn.Blocks = append([]forgePendingBlock(nil), in.Blocks...)
	item.intent = &copyIn
	return q.persist(item)
}

func (q *forgeAccountingQueue) runIntent(item *forgeQueuedShare, r *rpcForgeSubmitter) error {
	// active grants exclusive ownership while network I/O runs without queue locks.
	in := forgeSubmissionIntent{Blocks: append([]forgePendingBlock(nil), item.intent.Blocks...)}
	var diskErr error
	attempted := false
	for i := range in.Blocks {
		b := &in.Blocks[i]
		if b.State != "pending" && b.State != "noncanonical" {
			continue
		}
		client := r.intentClient(*b)
		if client == nil {
			b.Error = "original RPC endpoint unavailable"
			continue
		}
		wasSent := b.Sent
		if wasSent {
			var found bool
			if b.Chain == "OGG" {
				found, _ = client.HasForgeProof(item.receipt.Height, b.Payload)
			} else {
				observation, _ := client.ForgeBlockObservation(b.BlockHash)
				found = observation == "canonical"
				if observation == "noncanonical" {
					b.State, b.Error = "noncanonical", "observed outside active chain; full validation and acceptance unproven"
					continue
				}
			}
			if found {
				b.State, b.Error = "accepted", ""
				if err := q.saveIntent(item, in); err != nil {
					diskErr = err
				}
				continue
			}
		}
		// A prior noncanonical observation remains evidence, not rejection.
		// Recheck it during recovery but never submit its payload again.
		if b.State == "noncanonical" {
			continue
		}
		if forgeNow(r.clock).Unix() >= b.Expires {
			if wasSent && b.Chain == "OGG" {
				depth := r.maturityDepth
				if depth < 32 {
					depth = 32
				}
				excluded, lookupErr := client.ForgeProofExcludedAtMaturity(item.receipt.Height, b.Payload, depth)
				if lookupErr == nil && excluded {
					b.State, b.Error = "rejected", "not canonical at pool maturity"
					if err := q.saveIntent(item, in); err != nil {
						diskErr = err
					}
					continue
				}
			}
			b.Error = "submission window expired; acceptance unresolved"
			if !wasSent {
				b.State, b.Error = "rejected", "expired before first submission"
			}
			continue
		}
		if forgeNow(r.clock).Unix() < b.NotBefore {
			b.Error = "waiting for template validity window"
			continue
		}
		b.Sent = true
		if err := q.saveIntent(item, in); err != nil {
			diskErr = err
			// No RPC may precede its durable send marker. Otherwise a restart
			// could mistake a delivered block for an unattempted submission.
			b.Sent = wasSent
			b.Error = "submission deferred: send marker not durable"
			continue
		}
		// Disk sync can take long enough to cross the template boundary.
		if now := forgeNow(r.clock).Unix(); now < b.NotBefore || now >= b.Expires {
			b.Error = "validity window changed before RPC"
			if !wasSent {
				b.Sent = false
				if now >= b.Expires {
					b.State = "rejected"
				}
			}
			continue
		}
		var accepted, rejected bool
		var err error
		attempted = true
		submittedAt := time.Now()
		if b.Chain == "OGG" {
			accepted, rejected, err = client.SubmitForgeOGG(item.receipt.WorkHash, b.Payload)
		} else {
			accepted, rejected, err = client.SubmitForgeBlock(b.Payload)
		}
		if b.Chain != "OGG" {
			log.Printf("FORGE native submission chain=%s block=%s rgn_height=%d queued_ms=%d rpc_ms=%d accepted=%t rejected=%t", b.Chain, b.BlockHash, item.receipt.Height, submittedAt.UnixNano()/int64(time.Millisecond)-item.receipt.TimestampMS, time.Since(submittedAt).Milliseconds(), accepted, rejected)
		}
		if accepted {
			b.State, b.Error = "accepted", ""
		} else if rejected && !wasSent {
			b.State, b.Error = "rejected", "daemon rejected first submission"
		} else {
			b.Error = "submission outcome uncertain"
			if err != nil {
				b.Error = err.Error()
			}
		}
		if err := q.saveIntent(item, in); err != nil {
			diskErr = err
		}
	}
	if attempted && r.refresh != nil {
		r.refresh()
	}
	return q.finishIntent(item, in, diskErr)
}

func (q *forgeAccountingQueue) finishIntent(item *forgeQueuedShare, in forgeSubmissionIntent, diskErr error) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	item.intent = &in
	complete := true
	for _, b := range in.Blocks {
		if b.State == "pending" || b.State == "noncanonical" {
			complete = false
		}
	}
	if complete {
		for _, b := range in.Blocks {
			sub := storage.ForgeSubmission{Attempted: true, Accepted: b.State == "accepted", BlockHash: b.BlockHash, Error: b.Error}
			switch b.Chain {
			case "DGB":
				item.receipt.Digibyte = sub
			case "DOGE":
				item.receipt.Dogecoin = sub
			case "PEP":
				item.receipt.Pepecoin = sub
			case "DINGO":
				item.receipt.Dingocoin = sub
			case "BELLS":
				item.receipt.Bellscoin = sub
			case "SOQ":
				item.receipt.Soqucoin = sub
			default:
				if storage.ValidRegisteredSymbol(b.Chain) {
					if err := item.receipt.SetRegistered(b.Chain, b.ChainID, sub); err != nil {
						return err
					}
				}
			case "OGG":
				if b.State == "accepted" {
					data, _ := hex.DecodeString(b.Payload)
					item.receipt.ProofHash = storage.ForgeProofHash(data)
				} else {
					item.receipt.OGGError = b.Error
				}
			}
		}
		item.intent = nil
	}
	item.active = false
	if err := q.persist(item); err != nil {
		diskErr = err
	}
	return diskErr
}

func (q *forgeAccountingQueue) recoverSubmissions(r *rpcForgeSubmitter) error {
	q.mu.Lock()
	var items []*forgeQueuedShare
	start := sort.Search(len(q.pending), func(i int) bool { return q.pending[i].path > q.recoveryAfter })
	for n := 0; n < len(q.pending) && len(items) < q.limits.RecoveryBatchSize; n++ {
		item := q.pending[(start+n)%len(q.pending)]
		if item.intent != nil && !item.active {
			item.active = true
			items = append(items, item)
			q.recoveryAfter = item.path
		}
	}
	q.mu.Unlock()
	var result error
	for _, item := range items {
		if err := q.runIntent(item, r); err != nil {
			result = err
		}
	}
	q.mu.Lock()
	q.lastRecoveryCompletedMS = time.Now().UnixNano() / int64(time.Millisecond)
	q.mu.Unlock()
	return result
}

func (s *ProxyServer) processForgeIntent(r *rpcForgeSubmitter, job *forgeJob, sol *forgeSolution, hits forgeHits, receipt storage.ForgeShare) (bool, bool, forgeHits, error) {
	in := r.prepareIntent(job, sol, hits)
	if len(in.Blocks) == 0 { // Credit stays ordered even when a durable share is acknowledged promptly.
		enqueue := s.forge.account.enqueue
		if s.forge.durableShareAcknowledgements {
			enqueue = s.forge.account.enqueueDurable
		}
		exists, err := enqueue(receipt)
		return err == nil && !exists, exists, hits, err
	}
	item, persistErr := s.forge.account.beginIntent(receipt, in)
	if item == nil {
		return false, errors.Is(persistErr, errForgeDuplicate), hits, persistErr
	}
	if err := s.forge.account.runIntent(item, r); err != nil {
		persistErr = err
	}
	if s.forge.durableShareAcknowledgements && persistErr == nil {
		s.forge.account.mu.Lock()
		durable := item.durable
		s.forge.account.mu.Unlock()
		if durable {
			return true, false, hits, nil
		}
	}
	exists, err := s.forge.account.flushFor(receipt.ID)
	if persistErr != nil {
		return false, false, hits, fmt.Errorf("FORGE submission journal durability failure: %w", persistErr)
	}
	return err == nil && !exists, exists, hits, err
}
