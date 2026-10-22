package proxy

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"github.com/HawyUkr/open-progpow-pool/forgepool"
	"github.com/HawyUkr/open-progpow-pool/rpc"
	"github.com/HawyUkr/open-progpow-pool/storage"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestV4BoundIntentLostRepliesRestartWithoutDuplicateCredit(t *testing.T) {
	f := newIntentFixture(t)
	multiTemplateTestSigner(t, f.s.forge)
	parent, err := forgepool.BuildMultiParentV4(f.job.Digibyte.Template, f.job.OGG.WorkHash,
		map[uint32]*forgepool.DogeCandidate{98: f.job.Digibyte.DOGE}, []byte{0x51}, 9, 4, 4, true)
	if err != nil {
		t.Fatal(err)
	}
	f.job.Digibyte, f.job.ProtocolV3, f.job.ExternalMask, f.job.Signed = parent, true, 3, nil
	if err := f.s.forge.signJob(f.job); err != nil {
		t.Fatal(err)
	}
	f.router.authorized = f.s.forge.authorized
	if err := parent.CheckCertificate(*f.job.Signed.Multi, f.job.Signed.Signatures); err != nil {
		t.Fatal("published authorization is not mined into the parent", err)
	}
	f.sol.Header, err = parent.Header(f.sol.ExtraNonce1, f.sol.ExtraNonce2, f.sol.NTime, f.sol.Nonce)
	if err != nil {
		t.Fatal(err)
	}
	f.receipt.ID = forgePayloadDigest(f.sol.Header)
	hits := forgeHits{Digibyte: true, Dogecoin: true, OGG: true}
	in := f.router.prepareIntent(f.job, f.sol, hits)
	if err := in.validate(f.receipt); err != nil || len(in.Blocks) != 3 {
		t.Fatal("invalid bound intent", err)
	}
	var original []byte
	for _, block := range in.Blocks {
		d := f.daemons[block.Chain]
		d.blockHash, d.proof, d.reply = block.BlockHash, block.Payload, "lost"
		if block.Chain == "OGG" {
			original, err = hex.DecodeString(block.Payload)
			if err != nil || !bytes.HasPrefix(original, []byte{'O', 'G', 'G', 'F', 4}) {
				t.Fatal("saved intent did not contain V4 proof", err)
			}
		}
	}
	if _, _, _, err := f.s.processForgeIntent(f.router, f.job, f.sol, hits, f.receipt); err == nil || f.writer.calls != 0 {
		t.Fatal("uncertain native replies credited")
	}
	for _, daemon := range f.daemons {
		daemon.mu.Lock()
		daemon.canonical = true
		daemon.mu.Unlock()
	}
	queue := f.restart(t)
	for i := 0; i < 2; i++ {
		if err := queue.recoverSubmissions(f.router); err != nil {
			t.Fatal(err)
		}
		if err := queue.flush(); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.writer.receipts) != 1 || f.writer.receipts[0].ProofHash != storage.ForgeProofHash(original) {
		t.Fatal("V4 proof lost or share credit duplicated")
	}
	for _, daemon := range f.daemons {
		if daemon.submits != 1 || daemon.lookups != 1 {
			t.Fatal("recovery resubmitted a confirmed native payload")
		}
	}
	if strings.TrimPrefix(f.daemons["OGG"].payloads[0], "0x") != hex.EncodeToString(original) {
		t.Fatal("mined certificate changed in saved submission")
	}
}

func TestMultiAuxAccountingStatusIncludesEveryChain(t *testing.T) {
	f := newIntentFixture(t)
	status := f.s.forge.account.status(time.Now())
	if len(status.UnresolvedChains) != 6 || len(status.NoncanonicalChains) != 5 {
		t.Fatal("missing auxiliary chain in admission status")
	}
	if path := os.Getenv("FORGE_MULTI_ACCOUNTING_STATUS_FIXTURE"); path != "" {
		blob, _ := json.Marshal(status)
		if e := os.WriteFile(path, blob, 0600); e != nil {
			t.Fatal(e)
		}
	}
}

func TestMultiAuxAllSixIntentsRecoverWithoutDuplicateCredit(t *testing.T) {
	multiSixIntentRecovery(t, false)
}
func TestMultiV3AllSixIntentsRecoverWithoutDuplicateCredit(t *testing.T) {
	multiSixIntentRecovery(t, true)
}
func multiSixIntentRecovery(t *testing.T, v3 bool) {
	f := newIntentFixture(t)
	f.router.auxClients = map[string]*rpc.RPCClient{}
	f.job.AuxSymbols = map[string]uint32{"PEP": 63, "DINGO": 50, "BELLS": 16}
	leaves := map[uint32]*forgepool.DogeCandidate{}
	for symbol, id := range f.job.AuxSymbols {
		c, e := forgepool.BuildAuxCandidate(f.job.Digibyte.DOGE.Template, []byte{0x51}, 12, id)
		if e != nil {
			t.Fatal(e)
		}
		leaves[id] = c
		daemon := &intentTestDaemon{chain: symbol, reply: "lost"}
		server := httptest.NewServer(http.HandlerFunc(daemon.serve))
		t.Cleanup(server.Close)
		f.router.auxClients[symbol] = rpc.NewRPCClient(symbol, server.URL, "1s", "private", "private")
		f.daemons[symbol] = daemon
	}
	if e := f.job.Digibyte.SetAdditionalAux(leaves); e != nil {
		t.Fatal(e)
	}
	if v3 {
		f.job.ProtocolV3 = true
		f.job.ExternalMask = 31
		multiTemplateTestSigner(t, f.s.forge)
		if e := f.s.forge.signJob(f.job); e != nil {
			t.Fatal(e)
		}
		f.router.authorized = f.s.forge.authorized
	}
	var e error
	f.sol.Header, e = f.job.Digibyte.Header(f.sol.ExtraNonce1, f.sol.ExtraNonce2, f.sol.NTime, f.sol.Nonce)
	if e != nil {
		t.Fatal(e)
	}
	f.receipt.ID = forgePayloadDigest(f.sol.Header)
	hits := forgeHits{Digibyte: true, Dogecoin: true, Pepecoin: true, Dingocoin: true, Bellscoin: true, OGG: true}
	in := f.router.prepareIntent(f.job, f.sol, hits)
	if len(in.Blocks) != 6 {
		t.Fatal("not all chain wins routed")
	}
	if e := in.validate(f.receipt); e != nil {
		t.Fatal(e)
	}
	for _, b := range in.Blocks {
		d := f.daemons[b.Chain]
		d.blockHash = b.BlockHash
		d.proof = b.Payload
		d.reply = "lost"
	}
	if _, _, _, e := f.s.processForgeIntent(f.router, f.job, f.sol, hits, f.receipt); e == nil {
		t.Fatal("unacknowledged native credit must wait")
	}
	if f.writer.calls != 0 {
		t.Fatal("unresolved native round credited")
	}
	for _, d := range f.daemons {
		d.mu.Lock()
		d.canonical = true
		d.mu.Unlock()
	}
	q := f.restart(t)
	if e := q.recoverSubmissions(f.router); e != nil {
		t.Fatal(e)
	}
	if e := q.flush(); e != nil {
		t.Fatal(e)
	}
	if len(f.writer.receipts) != 1 {
		t.Fatal("lost or duplicated share credit")
	}
	s := f.writer.receipts[0]
	if !s.Pepecoin.Accepted || !s.Dingocoin.Accepted || !s.Bellscoin.Accepted || !s.Digibyte.Accepted || !s.Dogecoin.Accepted || s.ProofHash == "" {
		t.Fatal("outcomes mixed or lost")
	}
	if e := q.recoverSubmissions(f.router); e != nil {
		t.Fatal(e)
	}
	if e := q.flush(); e != nil {
		t.Fatal(e)
	}
	if len(f.writer.receipts) != 1 {
		t.Fatal("replay duplicated credit")
	}
	for _, d := range f.daemons {
		if d.submits != 1 || d.lookups != 1 {
			t.Fatal("canonical acceptance submitted again")
		}
	}
	t.Log("all six durable intents recovered after lost replies; one share credited once; five external outcomes retained separately")
}
