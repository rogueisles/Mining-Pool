package proxy

import (
	"encoding/json"
	"errors"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/HawyUkr/open-progpow-pool/rpc"
)

type ForgeReconcilePlan struct {
	Version       int    `json:"version"`
	CreatedUnixMS int64  `json:"createdUnixMS"`
	ReceiptID     string `json:"receiptID"`
	Chain         string `json:"chain"`
	JournalDigest string `json:"journalDigest"`
	Endpoint      string `json:"endpointFingerprint"`
	PayloadDigest string `json:"payloadDigest"`
	BlockHint     string `json:"blockHint,omitempty"`
	EvidenceBlock string `json:"evidenceBlock,omitempty"`
	Decision      string `json:"decision"`
	Operator      string `json:"operator"`
	Reason        string `json:"reason"`
	Digest        string `json:"digest"`
}

func (p ForgeReconcilePlan) digest() string {
	p.Digest = ""
	blob, _ := json.Marshal(p)
	return forgePayloadDigest(blob)
}

func operatorRouter(cfg *Config) *rpcForgeSubmitter {
	r := &rpcForgeSubmitter{auxClients: map[string]*rpc.RPCClient{}}
	aux, err := auxiliaryClients(cfg.Proxy.Forge.AdditionalAux)
	if err != nil {
		return r
	}
	for _, a := range aux {
		r.auxClients[a.symbol] = a.client
	}
	dgb := forgeEndpointDefaults(cfg.Proxy.Forge.Digibyte, "digibyte")
	doge := forgeEndpointDefaults(cfg.Proxy.Forge.Dogecoin, "dogecoin")
	r.dgb = rpc.NewRPCClient(dgb.Name, dgb.Url, dgb.Timeout, dgb.Username, dgb.Password)
	r.doge = rpc.NewRPCClient(doge.Name, doge.Url, doge.Timeout, doge.Username, doge.Password)
	for _, c := range cfg.Upstream {
		timeout := c.Timeout
		if timeout == "" {
			timeout = "5s"
		}
		r.issuers = append(r.issuers, rpc.NewRPCClient(c.Name, c.Url, timeout, c.Username, c.Password))
	}
	return r
}

func (q *forgeAccountingQueue) reconcilePlan(cfg *Config, id, chain, hint, actor, reason string) (*ForgeReconcilePlan, *forgeQueuedShare, error) {
	if len(actor) < 1 || len(actor) > 128 || len(reason) < 1 || len(reason) > 1024 || strings.ContainsAny(actor+reason, "\r\n") {
		return nil, nil, errors.New("operator and single-line reason required")
	}
	item := q.byID[id]
	if item == nil || item.intent == nil {
		return nil, nil, errors.New("unresolved receipt not found")
	}
	if err := item.intent.validate(item.receipt); err != nil {
		return nil, nil, err
	}
	var block *forgePendingBlock
	for i := range item.intent.Blocks {
		if item.intent.Blocks[i].Chain == chain {
			block = &item.intent.Blocks[i]
		}
	}
	if block == nil || block.State != "pending" || !block.Sent {
		return nil, nil, errors.New("chain has no uncertain send")
	}
	client := operatorRouter(cfg).intentClient(*block)
	if client == nil {
		return nil, nil, errors.New("original configured RPC endpoint unavailable")
	}
	blob, err := ioutil.ReadFile(item.path)
	if err != nil {
		return nil, nil, err
	}
	p := &ForgeReconcilePlan{Version: 1, CreatedUnixMS: time.Now().UnixNano() / int64(time.Millisecond), ReceiptID: id, Chain: chain, JournalDigest: forgePayloadDigest(blob), Endpoint: block.Endpoint, PayloadDigest: block.Digest, BlockHint: hint, Operator: actor, Reason: reason, Decision: "unresolved"}
	if chain == "OGG" {
		p.EvidenceBlock, err = client.FindForgeProofBlock(item.receipt.Height, block.Payload, hint)
	} else {
		if hint != "" {
			return nil, nil, errors.New("external candidate identity cannot be overridden")
		}
		var found bool
		found, err = client.HasForgeBlock(block.BlockHash)
		if found {
			p.EvidenceBlock = block.BlockHash
		}
	}
	if err != nil {
		return nil, nil, err
	}
	if p.EvidenceBlock != "" {
		p.Decision = "accept"
	}
	p.Digest = p.digest()
	return p, item, nil
}

// Requires the pool to be stopped: the same exclusive journal lock is used.
// Preview does not credit shares, submit blocks or mutate an outcome.
func PrepareForgeReconcile(cfg *Config, id, chain, hint, actor, reason string) (*ForgeReconcilePlan, error) {
	q, err := newForgeAccountingQueue(cfg.Proxy.Forge.AccountingJournalDir, nil, cfg.Proxy.Forge.Accounting)
	if err != nil {
		return nil, err
	}
	defer q.close()
	p, _, err := q.reconcilePlan(cfg, id, chain, hint, actor, reason)
	return p, err
}

// SaveForgeReview persists a review/audit document before a state-changing
// operation. Immutable names prevent replacing a different reviewed decision.
func SaveForgeReview(dir, name string, value interface{}) error {
	if !filepath.IsAbs(dir) || filepath.Base(name) != name {
		return errors.New("invalid review path")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	blob, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	blob = append(blob, '\n')
	path := filepath.Join(dir, name)
	if previous, err := ioutil.ReadFile(path); err == nil {
		if string(previous) == string(blob) {
			// A previous publication may have lost its directory-sync reply.
			return syncForgeReviewDirs(dir)
		}
		return errors.New("immutable review already exists")
	} else if !os.IsNotExist(err) {
		return err
	}
	f, err := ioutil.TempFile(dir, ".review-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(blob); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	// Link is an atomic no-replace publication on the same filesystem.
	if err = os.Link(f.Name(), path); err != nil {
		return err
	}
	return syncForgeReviewDirs(dir)
}

func syncForgeReviewDirs(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	if err = d.Sync(); err != nil {
		return err
	}
	parent, err := os.Open(filepath.Dir(dir))
	if err != nil {
		return err
	}
	defer parent.Close()
	return parent.Sync()
}

func ApplyForgeReconcile(cfg *Config, review ForgeReconcilePlan, confirmedDigest string) error {
	if review.Version != 1 || review.Digest != review.digest() || confirmedDigest != review.Digest || review.Decision != "accept" {
		return errors.New("reviewed positive reconciliation digest required")
	}
	age := time.Now().UnixNano()/int64(time.Millisecond) - review.CreatedUnixMS
	if age < 0 || age > 3600000 {
		return errors.New("reconciliation review expired or future-dated")
	}
	q, err := newForgeAccountingQueue(cfg.Proxy.Forge.AccountingJournalDir, nil, cfg.Proxy.Forge.Accounting)
	if err != nil {
		return err
	}
	defer q.close()
	fresh, item, err := q.reconcilePlan(cfg, review.ReceiptID, review.Chain, review.BlockHint, review.Operator, review.Reason)
	if err != nil {
		return err
	}
	fresh.CreatedUnixMS = review.CreatedUnixMS
	fresh.Digest = fresh.digest()
	if fresh.Digest != review.Digest {
		return errors.New("journal or chain evidence changed since review")
	}
	// Intent journal bytes are changed only after the reviewed decision is durable.
	if err := SaveForgeReview(filepath.Join(q.dir, "audit"), "reconcile-"+review.Digest+".json", review); err != nil {
		return err
	}
	in := forgeSubmissionIntent{Blocks: append([]forgePendingBlock(nil), item.intent.Blocks...)}
	for i := range in.Blocks {
		if in.Blocks[i].Chain == review.Chain {
			in.Blocks[i].State, in.Blocks[i].Error = "accepted", ""
		}
	}
	if err := q.finishIntent(item, in, nil); err != nil {
		return err
	}
	// No Redis writes here. Normal ordered replay handles the final receipt.
	return q.writeStatus(time.Now())
}
