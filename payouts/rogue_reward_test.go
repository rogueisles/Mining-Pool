package payouts

import (
	"encoding/json"
	"fmt"
	"github.com/HawyUkr/open-progpow-pool/rpc"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRogueUnlockerNeverDistributesTreasuryAllocations(t *testing.T) {
	for _, row := range []struct {
		height         uint64
		total, special string
	}{
		{1100100, "1100000000000000000000", "0"},
		{1100110, "1099997886585437782765", "300000000000000000000000000"},
		{1319250, "1054645991375788345337", "50000000000000000000000000"},
		{13580767, "100000000000000000000", "0"},
		{33098927, "2010331721973212466", "0"},
		{33098928, "0", "0"},
	} {
		total, _ := new(big.Int).SetString(row.total, 10)
		miner := new(big.Int).Set(total)
		share := func(p int64) *big.Int {
			return new(big.Int).Quo(new(big.Int).Mul(total, big.NewInt(p)), big.NewInt(100))
		}
		staking, p2e, treasury := share(30), share(20), share(10)
		miner.Sub(miner, staking).Sub(miner, p2e).Sub(miner, treasury)
		asHex := func(n *big.Int) string { return "0x" + n.Text(16) }
		special, _ := new(big.Int).SetString(row.special, 10)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var req struct{ Method string }
			json.NewDecoder(r.Body).Decode(&req)
			if req.Method != "eth_getBlockReward" {
				t.Error("unexpected RPC", req.Method)
			}
			json.NewEncoder(w).Encode(map[string]interface{}{"jsonrpc": "2.0", "id": 0, "result": rpc.OGGReward{BlockNumber: fmt.Sprintf("0x%x", row.height), Total: asHex(total), Miner: asHex(miner), Staking: asHex(staking), P2E: asHex(p2e), Treasury: asHex(treasury), Tribe: "0x0", Maintenance: "0x0", SpecialIssuance: asHex(special), CurrencySymbol: "RGN"}})
		}))
		u := &BlockUnlocker{config: &UnlockerConfig{ForgeBlock: 1100100}, rpc: rpc.NewRPCClient("test", srv.URL, "2s", "", "")}
		block, candidate := forgeIdentityFixture(t)
		block.Number = fmt.Sprintf("0x%x", row.height)
		if err := u.handleBlock(block, candidate); err != nil {
			t.Fatal(err)
		}
		if candidate.Reward.Cmp(miner) != 0 || candidate.ExtraReward.Sign() != 0 {
			t.Fatal("treasury or non-miner share credited to miners")
		}
		srv.Close()
	}
}

func TestRogueUnlockerPreservesTransactionFeePolicy(t *testing.T) {
	for _, keep := range []bool{false, true} {
		t.Run(fmt.Sprint(keep), func(t *testing.T) {
			const miner = "440000000000000000000"
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req struct{ Method string }
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
				}
				var result interface{}
				switch req.Method {
				case "eth_getBlockReward":
					value, _ := new(big.Int).SetString(miner, 10)
					result = rpc.OGGReward{BlockNumber: "0x10c944", Miner: "0x" + value.Text(16)}
				case "eth_getTransactionReceipt":
					result = rpc.TxReceipt{GasUsed: "0x5208", TxHash: "0x1", BlockHash: "0x" + strings.Repeat("aa", 32)}
				default:
					t.Error("unexpected RPC", req.Method)
				}
				json.NewEncoder(w).Encode(map[string]interface{}{"id": 0, "result": result})
			}))
			defer srv.Close()
			u := &BlockUnlocker{config: &UnlockerConfig{ForgeBlock: 1100100, KeepTxFees: keep}, rpc: rpc.NewRPCClient("test", srv.URL, "2s", "", "")}
			block, candidate := forgeIdentityFixture(t)
			block.Number = fmt.Sprintf("0x%x", 1100100)
			block.Transactions = []rpc.Tx{{Hash: "0x1", GasPrice: "0x3b9aca00"}}
			if err := u.handleBlock(block, candidate); err != nil {
				t.Fatal(err)
			}
			want, _ := new(big.Int).SetString(miner, 10)
			fee := big.NewInt(21000000000000)
			if keep {
				if candidate.ExtraReward.Cmp(fee) != 0 {
					t.Fatal("pool fee not accounted", candidate.ExtraReward)
				}
			} else {
				want.Add(want, fee)
				if candidate.ExtraReward.Sign() != 0 {
					t.Fatal("fee counted twice")
				}
			}
			if candidate.Reward.Cmp(want) != 0 {
				t.Fatal("miner fee policy changed", candidate.Reward, want)
			}
		})
	}
}

func TestUnlockerRejectsUnavailableOrInvalidTransactionFees(t *testing.T) {
	for _, height := range []uint64{1, 1100100} {
		for _, fault := range []string{"missing", "wrong transaction", "wrong block", "invalid gas used", "negative gas used", "invalid gas price", "negative gas price"} {
			t.Run(fmt.Sprintf("%d/%s", height, fault), func(t *testing.T) {
				block, candidate := forgeIdentityFixture(t)
				block.Number = fmt.Sprintf("0x%x", height)
				block.Transactions = []rpc.Tx{{Hash: "0x1", GasPrice: "0x3b9aca00"}}
				receipt := &rpc.TxReceipt{GasUsed: "0x5208", TxHash: "0x1", BlockHash: block.Hash}
				switch fault {
				case "missing":
					receipt = nil
				case "wrong transaction":
					receipt.TxHash = "0x2"
				case "wrong block":
					receipt.BlockHash = "0x2"
				case "invalid gas used":
					receipt.GasUsed = "invalid"
				case "negative gas used":
					receipt.GasUsed = "-0x1"
				case "invalid gas price":
					block.Transactions[0].GasPrice = "invalid"
				case "negative gas price":
					block.Transactions[0].GasPrice = "-0x1"
				}
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var req struct{ Method string }
					json.NewDecoder(r.Body).Decode(&req)
					var result interface{} = receipt
					if req.Method == "eth_getBlockReward" {
						result = rpc.OGGReward{BlockNumber: block.Number, Miner: "0x1"}
					}
					json.NewEncoder(w).Encode(map[string]interface{}{"id": 0, "result": result})
				}))
				defer srv.Close()
				u := &BlockUnlocker{config: &UnlockerConfig{ForgeBlock: 1100100}, rpc: rpc.NewRPCClient("test", srv.URL, "2s", "", "")}
				if err := u.handleBlock(block, candidate); err == nil {
					t.Fatal("unresolved fee evidence accepted as zero")
				}
			})
		}
	}
}
