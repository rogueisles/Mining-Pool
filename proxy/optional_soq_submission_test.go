package proxy

import (
	"encoding/hex"
	"github.com/HawyUkr/open-progpow-pool/forgepool"
	"github.com/HawyUkr/open-progpow-pool/rpc"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSOQSubmissionLostReplyRecoveryDoesNotDuplicateCredit(t *testing.T) {
	f := newIntentFixture(t)
	w := &creditTestWriter{}
	f.s.forge.account.writer = w
	script, _ := hex.DecodeString("51205dbc511fd411101f2baee0fd1914568c5437b01693b100c7786a3d19809c7596")
	c, e := forgepool.BuildAuxCandidate(f.job.Digibyte.DOGE.Template, script, 17, 21329)
	if e != nil {
		t.Fatal(e)
	}
	if e = f.job.Digibyte.SetAdditionalAux(map[uint32]*forgepool.DogeCandidate{21329: c}); e != nil {
		t.Fatal(e)
	}
	f.job.AuxSymbols = map[string]uint32{"SOQ": 21329}
	d := &intentTestDaemon{chain: "SOQ", reply: "lost"}
	server := httptest.NewServer(http.HandlerFunc(d.serve))
	t.Cleanup(server.Close)
	f.router.auxClients = map[string]*rpc.RPCClient{"SOQ": rpc.NewRPCClient("SOQ", server.URL, "1s", "test", "test-only")}
	f.daemons["SOQ"] = d
	f.sol.Header, e = f.job.Digibyte.Header(f.sol.ExtraNonce1, f.sol.ExtraNonce2, f.sol.NTime, f.sol.Nonce)
	if e != nil {
		t.Fatal(e)
	}
	f.receipt.ID = forgePayloadDigest(f.sol.Header)
	hits := forgeHits{Digibyte: true, Dogecoin: true, Soqucoin: true}
	in := f.router.prepareIntent(f.job, f.sol, hits)
	if len(in.Blocks) != 3 {
		t.Fatal("native reward routes missing")
	}
	if e = in.validate(f.receipt); e != nil {
		t.Fatal(e)
	}
	for _, b := range in.Blocks {
		daemon := f.daemons[b.Chain]
		daemon.reply = "lost"
		daemon.blockHash = b.BlockHash
		daemon.proof = b.Payload
	}
	_, _, _, _ = f.s.processForgeIntent(f.router, f.job, f.sol, hits, f.receipt)
	for _, b := range in.Blocks {
		daemon := f.daemons[b.Chain]
		daemon.mu.Lock()
		daemon.canonical = true
		daemon.mu.Unlock()
	}
	q := f.restart(t)
	q.writer = w
	for i := 0; i < 2; i++ {
		if e = q.recoverSubmissions(f.router); e != nil {
			t.Fatal(e)
		}
		if e = q.flush(); e != nil {
			t.Fatal(e)
		}
	}
	if len(w.receipts) != 1 {
		t.Fatal("lost or duplicated miner credit", len(w.receipts))
	}
	if len(w.updates) != 1 {
		t.Fatal("missing final external observation", len(w.updates))
	}
	out := w.updates[0]
	if !out.Soqucoin.Attempted || !out.Soqucoin.Accepted || out.ProofHash != "" {
		t.Fatal("SOQ recovery changed native RGN credit", out)
	}
	for _, b := range in.Blocks {
		daemon := f.daemons[b.Chain]
		if daemon.submits != 1 || daemon.lookups != 1 {
			t.Fatal("native block resubmitted after recovery", b.Chain)
		}
	}
}
