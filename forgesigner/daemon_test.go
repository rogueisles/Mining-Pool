package forgesigner

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/HawyUkr/open-progpow-pool/forgepool"
	"github.com/HawyUkr/open-progpow-pool/rpc"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

func daemonCall(t *testing.T, url, method string, params ...interface{}) json.RawMessage {
	t.Helper()
	body, _ := json.Marshal(map[string]interface{}{"id": 1, "method": method, "params": params})
	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.SetBasicAuth("forge-regtest", "disposable-test-only")
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		Result json.RawMessage
		Error  interface{}
	}
	if err = json.NewDecoder(resp.Body).Decode(&out); err != nil || out.Error != nil {
		t.Fatalf("%s: %v / %v", method, err, out.Error)
	}
	return out.Result
}

func TestOfficialDogecoinSignerProposal(t *testing.T) {
	poolURL, signerURL := os.Getenv("FORGE_DOGE_TEST_RPC"), os.Getenv("FORGE_DOGE_SIGNER_RPC")
	if poolURL == "" || signerURL == "" {
		t.Skip("run isolated Dogecoin harness")
	}
	for _, url := range []string{poolURL, signerURL} {
		var info struct{ Chain string }
		if err := json.Unmarshal(daemonCall(t, url, "getblockchaininfo"), &info); err != nil || info.Chain != "regtest" {
			t.Fatal("regtest required")
		}
	}
	// Mature disposable coins so the independent daemon can have a different
	// transaction set while validating the older pool candidate correctly.
	daemonCall(t, poolURL, "generate", 80)
	wantTip := string(daemonCall(t, poolURL, "getbestblockhash"))
	deadline := time.Now().Add(10 * time.Second)
	for string(daemonCall(t, signerURL, "getbestblockhash")) != wantTip {
		if time.Now().After(deadline) {
			t.Fatal("independent node did not synchronize")
		}
		time.Sleep(20 * time.Millisecond)
	}
	poolRPC := rpc.NewRPCClient("pool-doge", poolURL, "5s", "forge-regtest", "disposable-test-only")
	trustedRPC := rpc.NewRPCClient("signer-doge", signerURL, "5s", "forge-regtest", "disposable-test-only")
	gbt, err := poolRPC.GetDogecoinBlockTemplate()
	if err != nil {
		t.Fatal(err)
	}
	tpl, err := gbt.ToParentTemplate()
	if err != nil {
		t.Fatal(err)
	}
	if len(tpl.Transactions) != 0 {
		t.Fatal("expected initially empty mempool")
	}
	now := int64(tpl.CurTime) + 3
	daemonCall(t, signerURL, "setmocktime", now)
	defer daemonCall(t, signerURL, "setmocktime", 0)
	var address string
	if err = json.Unmarshal(daemonCall(t, poolURL, "getnewaddress"), &address); err != nil {
		t.Fatal(err)
	}
	var txid, rawTx string
	if err = json.Unmarshal(daemonCall(t, poolURL, "sendtoaddress", address, 1), &txid); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(daemonCall(t, poolURL, "getrawtransaction", txid), &rawTx); err != nil {
		t.Fatal(err)
	}
	// Avoid dependence on randomized P2P relay timers under a frozen mocktime.
	// The second full node still validates the transaction independently.
	daemonCall(t, signerURL, "sendrawtransaction", rawTx)
	trustedGBT, err := trustedRPC.GetDogecoinBlockTemplate()
	if err != nil {
		t.Fatal(err)
	}
	trustedTpl, err := trustedGBT.ToParentTemplate()
	if err != nil {
		t.Fatal(err)
	}
	if trustedTpl.CurTime == tpl.CurTime || len(trustedTpl.Transactions) == 0 {
		t.Fatal("test did not create independent time/body differences")
	}
	// DGB data is an isolated RPC fixture; this test targets the actual DOGE
	// daemon proposal-validation boundary, not Digibyte network acceptance.
	dgbGBT := rpc.BlockTemplateReply{PowAlgo: "scrypt", PowAlgoID: func() *int { n:=1; return &n }(),Version: 2, PreviousBlockHash: strings.Repeat("12", 32), Bits: "1d00ffff", Height: 200, CurTime: now, CoinbaseValue: 5000000000}
	dgb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{"id": 0, "result": dgbGBT})
	}))
	defer dgb.Close()
	source := RPCSource{Digibyte: rpc.NewRPCClient("fixture-dgb", dgb.URL, "1s", "", ""), Dogecoin: trustedRPC}
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	backend := &devBackend{key: key}
	service, err := New(source, backend, Policy{DogePayoutScript: []byte{0x52}, MaxValidity: 60, MaxAge: 30}, func() time.Time { return time.Unix(now, 0) })
	if err != nil {
		t.Fatal(err)
	}
	proposal := func(candidate forgepool.ParentTemplate) forgepool.TemplateSignRequest {
		doge, err := forgepool.BuildDogeCandidate(candidate, []byte{0x52}, 77)
		if err != nil {
			t.Fatal(err)
		}
		r := forgepool.TemplateSignRequest{Domain: "OGG-FORGE-TEMPLATE-V1", Template: forgepool.ExternalTemplate{Version: 1, DGBPrevHash: common.HexToHash(dgbGBT.PreviousBlockHash), DGBBits: 0x1d00ffff, DogeAuxHash: common.HexToHash(doge.DisplayHash), ValidFrom: uint64(tpl.CurTime), Expires: uint64(tpl.CurTime) + 60}, Doge: &forgepool.DogeSigningEvidence{JobNonce: 77, Time: candidate.CurTime, Candidate: &candidate}}
		r.Digest = forgepool.TemplateDigest(r.Template).Hex()
		return r
	}
	request := proposal(tpl)
	old := request
	oldEvidence := *request.Doge
	oldEvidence.Candidate = nil
	old.Doge = &oldEvidence
	if out, err := service.Sign(context.Background(), old); err == nil || out != nil || backend.calls != 0 {
		t.Fatal("legacy exact-GBT path should reject this mismatch")
	}
	out, err := service.Sign(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	sig, err := hex.DecodeString(strings.TrimPrefix(out.Signature, "0x"))
	if err != nil {
		t.Fatal(err)
	}
	if err = forgepool.ValidateTemplateSignatures(request.Template, [][]byte{sig}, []common.Address{backend.Address()}, 1, time.Unix(now, 0)); err != nil {
		t.Fatal(err)
	}
	t.Log("real independent DOGE daemon accepted older timestamp and empty candidate despite newer nonempty GBT")
	if out, err := service.Sign(context.Background(), proposal(trustedTpl)); err != nil || out == nil {
		t.Fatalf("valid nonempty candidate rejected: %v", err)
	}
	t.Log("real independent DOGE daemon also accepted the valid nonempty candidate")
	for _, name := range []string{"excess-reward", "wrong-bits", "wrong-txid", "invalid-spend"} {
		t.Run(name, func(t *testing.T) {
			bad := tpl
			switch name {
			case "excess-reward":
				bad.CoinbaseValue++
			case "wrong-bits":
				bad.Bits = 0x1d00ffff
			case "wrong-txid":
				bad.Transactions = append([]forgepool.TemplateTx(nil), trustedTpl.Transactions...)
				bad.Transactions[0].TxID[0] ^= 1
			case "invalid-spend":
				bad.Transactions = append([]forgepool.TemplateTx(nil), trustedTpl.Transactions...)
				bad.Transactions[0].Data = append([]byte(nil), bad.Transactions[0].Data...)
				if len(bad.Transactions[0].Data) < 40 {
					t.Fatal("unexpected transaction")
				}
				bad.Transactions[0].Data[10] ^= 1
				bad.Transactions[0].TxID = forgepool.CoinbaseTxID(bad.Transactions[0].Data)
			}
			before := backend.calls
			if out, err := service.Sign(context.Background(), proposal(bad)); err == nil || out != nil || backend.calls != before {
				t.Fatalf("invalid body signed: %v", err)
			}
		})
	}
	before := backend.calls
	backend.hook = func() { daemonCall(t, signerURL, "generate", 1) }
	if out, err := service.Sign(context.Background(), request); err == nil || out != nil || backend.calls != before+1 {
		t.Fatalf("signature released across tip change: %v", err)
	}
	t.Log("independent daemon rejected invalid reward, difficulty, Merkle data and spend; tip change withheld signature")
}
