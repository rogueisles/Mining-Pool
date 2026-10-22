package payouts

import (
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/HawyUkr/open-progpow-pool/forgepool"
	"github.com/HawyUkr/open-progpow-pool/rpc"
	"github.com/HawyUkr/open-progpow-pool/storage"
)

// This fixture tests accounting identity, not consensus validity of the proof.
func forgeIdentityFixture(t *testing.T) (*rpc.GetBlockReply, *storage.BlockData) {
	t.Helper()
	blob, err := forgepool.EncodeOGGProof(&forgepool.OGGAuxPowProof{MinerExtra: []byte("accounting-test")})
	if err != nil {
		t.Fatal(err)
	}
	proofHash := storage.ForgeProofHash(blob)
	return &rpc.GetBlockReply{Number: "0x64", Hash: "0x" + strings.Repeat("aa", 32), Nonce: "0x0000000000000000", ExtraData: "0x" + hex.EncodeToString(blob), Uncles: []string{"must-not-query"}},
		&storage.BlockData{Height: 100, RoundHeight: 100, Nonce: storage.ForgeRoundID(proofHash), MixDigest: proofHash}
}

func TestForgeCandidateRequiresExactProofHeightAndMatureHash(t *testing.T) {
	b, c := forgeIdentityFixture(t)
	if !matchForgeCandidate(b, c) {
		t.Fatal("exact proof not recognized")
	}
	copyBlock := *b
	copyBlock.ExtraData = "0x00"
	if matchForgeCandidate(&copyBlock, c) {
		t.Fatal("wrong proof accepted")
	}
	copyBlock = *b
	copyBlock.Number = "0x65"
	if matchForgeCandidate(&copyBlock, c) {
		t.Fatal("wrong height accepted")
	}
	copyCandidate := *c
	copyCandidate.Nonce = b.Nonce
	if matchForgeCandidate(b, &copyCandidate) {
		t.Fatal("shared zero nonce matched")
	}
	c.Hash = b.Hash
	if !matchForgeCandidate(b, c) {
		t.Fatal("exact immature hash not recognized")
	}
	copyBlock = *b
	copyBlock.Hash = "0x" + strings.Repeat("bb", 32)
	if matchForgeCandidate(&copyBlock, c) || matchCandidate(&copyBlock, c) {
		t.Fatal("reorg fell back to nonce or proof")
	}
}

func TestForgeUnlockCandidatesExactHeightNoUncleRPC(t *testing.T) {
	b, c := forgeIdentityFixture(t)
	var methods []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string
			Params []json.RawMessage
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		methods = append(methods, req.Method)
		var result interface{}
		switch req.Method {
		case "eth_getBlockByNumber":
			if string(req.Params[0]) != `"0x64"` {
				t.Errorf("searched unissued height %s", req.Params[0])
			}
			result = b
		case "eth_getBlockReward":
			result = &rpc.OGGReward{BlockNumber: "0x64", Miner: "0x1c2"}
		default:
			t.Errorf("unexpected RPC %s", req.Method)
		}
		json.NewEncoder(w).Encode(map[string]interface{}{"jsonrpc": "2.0", "id": 0, "result": result})
	}))
	defer srv.Close()
	u := &BlockUnlocker{config: &UnlockerConfig{ForgeBlock: 100}, rpc: rpc.NewRPCClient("test", srv.URL, "2s", "", "")}
	result, err := u.unlockCandidates([]*storage.BlockData{c})
	if err != nil || result.blocks != 1 || result.uncles != 0 || result.orphans != 0 {
		t.Fatalf("unlock %+v %v", result, err)
	}
	if c.Reward.String() != "450" || len(methods) != 2 {
		t.Fatalf("reward/RPC %v %v", c.Reward, methods)
	}
	// Reorg at the same height, with the same Ethereum nonce and proof data,
	// must orphan the previously recognized hash without querying a reward.
	b.Hash = "0x" + strings.Repeat("bb", 32)
	methods = nil
	result, err = u.unlockCandidates([]*storage.BlockData{c})
	if err != nil || result.orphans != 1 || len(methods) != 1 {
		t.Fatalf("reorg %+v %v methods=%v", result, err, methods)
	}
}

func TestForgeCandidateFailsClosedWithoutForkConfig(t *testing.T) {
	_, c := forgeIdentityFixture(t)
	u := &BlockUnlocker{config: &UnlockerConfig{}}
	if _, err := u.unlockCandidates([]*storage.BlockData{c}); err == nil {
		t.Fatal("FORGE candidate routed through legacy reward math")
	}
}

func TestForgeCanonicalRewardRejectsWrongHeightAndMalformedMiner(t *testing.T) {
	for _, reward := range []rpc.OGGReward{{BlockNumber: "0x65", Miner: "0x1c2"}, {BlockNumber: "0x64", Miner: "1c2"}, {BlockNumber: "0x64", Miner: "-0x1"}} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode(map[string]interface{}{"jsonrpc": "2.0", "id": 0, "result": reward})
		}))
		u := &BlockUnlocker{config: &UnlockerConfig{ForgeBlock: 100}, rpc: rpc.NewRPCClient("test", srv.URL, "2s", "", "")}
		b, c := forgeIdentityFixture(t)
		if err := u.handleBlock(b, c); err == nil {
			t.Errorf("accepted bad canonical reward %+v", reward)
		}
		srv.Close()
	}
}
