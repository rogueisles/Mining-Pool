package payouts

import (
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/HawyUkr/open-progpow-pool/rpc"
	"github.com/HawyUkr/open-progpow-pool/storage"
)

func TestPostForgeUnlockerUsesCanonicalMinerRewardAndNoUncles(t *testing.T) {
	var method string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		method = req.Method
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":0,"result":{"blockNumber":"0x64","total":"0x3e8","miner":"0x1c2","staking":"0x190","tribe":"0x46","maintenance":"0x50"}}`))
	}))
	defer srv.Close()
	u := &BlockUnlocker{config: &UnlockerConfig{ForgeBlock: 100}, rpc: rpc.NewRPCClient("test", srv.URL, "2s", "", "")}
	c := &storage.BlockData{Height: 100, Uncle: true, UncleHeight: 99}
	b := &rpc.GetBlockReply{Number: "0x64", Hash: "0xabc", Uncles: []string{"0xdead"}}
	if err := u.handleBlock(b, c); err != nil {
		t.Fatal(err)
	}
	if method != "eth_getBlockReward" {
		t.Fatalf("RPC method %q", method)
	}
	if c.Reward.String() != "450" {
		t.Fatalf("miner reward %s", c.Reward)
	}
	if c.Uncle || c.UncleHeight != 0 {
		t.Fatal("post-FORGE uncle accounting remained enabled")
	}
}

func TestPreForgeUnlockerPreservesLegacyRewardPath(t *testing.T) {
	u := &BlockUnlocker{config: &UnlockerConfig{ForgeBlock: 100}}
	c := &storage.BlockData{Height: 99}
	b := &rpc.GetBlockReply{Number: "0x63", Hash: "0xabc"}
	if err := u.handleBlock(b, c); err != nil {
		t.Fatal(err)
	}
	if c.Reward == nil || c.Reward.Sign() <= 0 {
		t.Fatal("legacy reward path not preserved")
	}
}

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}

func TestCalculateRewards(t *testing.T) {
	blockReward, _ := new(big.Rat).SetString("5000000000000000000")
	shares := map[string]int64{"0x0": 1000000, "0x1": 20000, "0x2": 5000, "0x3": 10, "0x4": 1}
	expectedRewards := map[string]int64{"0x0": 4877996431, "0x1": 97559929, "0x2": 24389982, "0x3": 48780, "0x4": 4878}
	totalShares := int64(1025011)

	rewards := calculateRewardsForShares(shares, totalShares, blockReward)
	expectedTotalAmount := int64(5000000000)

	totalAmount := int64(0)
	for login, amount := range rewards {
		totalAmount += amount

		if expectedRewards[login] != amount {
			t.Errorf("Amount for %v must be equal to %v vs %v", login, expectedRewards[login], amount)
		}
	}
	if totalAmount != expectedTotalAmount {
		t.Errorf("Total reward must be equal to block reward in Shannon: %v vs %v", expectedTotalAmount, totalAmount)
	}
}

func TestChargeFee(t *testing.T) {
	orig, _ := new(big.Rat).SetString("5000000000000000000")
	value, _ := new(big.Rat).SetString("5000000000000000000")
	expectedNewValue, _ := new(big.Rat).SetString("3750000000000000000")
	expectedFee, _ := new(big.Rat).SetString("1250000000000000000")
	newValue, fee := chargeFee(orig, 25.0)

	if orig.Cmp(value) != 0 {
		t.Error("Must not change original value")
	}
	if newValue.Cmp(expectedNewValue) != 0 {
		t.Error("Must charge and deduct correct fee")
	}
	if fee.Cmp(expectedFee) != 0 {
		t.Error("Must charge fee")
	}
}

func TestWeiToShannonInt64(t *testing.T) {
	wei, _ := new(big.Rat).SetString("1000000000000000000")
	origWei, _ := new(big.Rat).SetString("1000000000000000000")
	shannon := int64(1000000000)

	if weiToShannonInt64(wei) != shannon {
		t.Error("Must convert to Shannon")
	}
	if wei.Cmp(origWei) != 0 {
		t.Error("Must charge original value")
	}
}

func TestGetUncleReward(t *testing.T) {
	rewards := make(map[int64]string)
	expectedRewards := make(map[int64]string)
	for i := int64(1); i < 8; i++ {
		expectedRewards[i] = legacyOGGVectorAt(t, i+1).UncleRewards[i]
		rewards[i] = getUncleReward(1, i+1).String()
	}
	for i, reward := range rewards {
		if expectedRewards[i] != rewards[i] {
			t.Errorf("Incorrect uncle reward for %v, expected %v vs %v", i, expectedRewards[i], reward)
		}
	}
}

// Ethereum's fixed Byzantium subsidy is not the inherited OGG helper. Exercise
// the native OGG migration heights against independent exact-integer vectors.
func TestLegacyOGGUncleRewardAtMigration(t *testing.T) {
	rewards := make(map[int64]string)
	expectedRewards := make(map[int64]string)
	for i := int64(1); i < 8; i++ {
		expectedRewards[i] = legacyOGGVectorAt(t, 21000+i).UncleRewards[i]
		rewards[i] = getUncleReward(21000, 21000+i).String()
	}
	for i, reward := range rewards {
		if expectedRewards[i] != rewards[i] {
			t.Errorf("Incorrect uncle reward for %v, expected %v vs %v", i, expectedRewards[i], reward)
		}
	}
}

func TestGetRewardForUngle(t *testing.T) {
	reward := getRewardForUncle(1).String()
	expectedReward := legacyOGGVectorAt(t, 1).InclusionReward
	if expectedReward != reward {
		t.Errorf("Incorrect uncle bonus for height %v, expected %v vs %v", 1, expectedReward, reward)
	}
}

func TestLegacyOGGInclusionRewardAtMigration(t *testing.T) {
	reward := getRewardForUncle(21000).String()
	expectedReward := legacyOGGVectorAt(t, 21000).InclusionReward
	if expectedReward != reward {
		t.Errorf("Incorrect uncle bonus at OGG migration height, expected %v vs %v", expectedReward, reward)
	}
}

func TestMatchCandidate(t *testing.T) {
	gethBlock := &rpc.GetBlockReply{Hash: "0x12345A", Nonce: "0x1A"}
	parityBlock := &rpc.GetBlockReply{Hash: "0x12345A", SealFields: []string{"0x0A", "0x1A"}}
	candidate := &storage.BlockData{Nonce: "0x1a"}
	orphan := &storage.BlockData{Nonce: "0x1abc"}

	if !matchCandidate(gethBlock, candidate) {
		t.Error("Must match with nonce")
	}
	if !matchCandidate(parityBlock, candidate) {
		t.Error("Must match with seal fields")
	}
	if matchCandidate(gethBlock, orphan) {
		t.Error("Must not match with orphan with nonce")
	}
	if matchCandidate(parityBlock, orphan) {
		t.Error("Must not match orphan with seal fields")
	}

	block := &rpc.GetBlockReply{Hash: "0x12345A"}
	immature := &storage.BlockData{Hash: "0x12345a", Nonce: "0x0"}
	if !matchCandidate(block, immature) {
		t.Error("Must match with hash")
	}
}
