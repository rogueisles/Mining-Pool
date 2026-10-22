package rpc

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

type testRPCRequest struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

func newRPCServer(t *testing.T, handler func(testRPCRequest) interface{}) (*httptest.Server, *RPCClient) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var call testRPCRequest
		if err := json.NewDecoder(req.Body).Decode(&call); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		result := handler(call)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      0,
			"result":  result,
		})
	}))
	return srv, NewRPCClient("test", srv.URL, "2s", "", "")
}

func TestCreateOGGAuxBlockRPC(t *testing.T) {
	srv, client := newRPCServer(t, func(call testRPCRequest) interface{} {
		if call.Method != "eth_createAuxBlock" {
			t.Fatalf("method %s", call.Method)
		}
		return map[string]interface{}{
			"workHash":            "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			"target":              "0x00000000ffff0000000000000000000000000000000000000000000000000000",
			"height":              "0x64",
			"timestamp":           "0x6553f100",
			"templateMaxValidity": "0x12c",
			"auxChainId":          "0x4f47",
			"dogeAuxChainId":      "0x62",
			"previousBlockHash":   "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			"difficulty":          "0x1",
			"minerReward":         "0x2a",
		}
	})
	defer srv.Close()

	work, err := client.CreateOGGAuxBlock()
	if err != nil {
		t.Fatal(err)
	}
	if work.Height != "0x64" || work.AuxChainID != "0x4f47" || work.DogeAuxChainID != "0x62" {
		t.Fatalf("bad aux work: %+v", work)
	}
	if work.Timestamp != "0x6553f100" || work.TemplateMaxValidity != "0x12c" {
		t.Fatalf("missing work time metadata %+v", work)
	}
}

func TestSubmitOGGAuxBlockRPC(t *testing.T) {
	workHash := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	proof := "0x4f47474601"
	srv, client := newRPCServer(t, func(call testRPCRequest) interface{} {
		if call.Method != "eth_submitAuxBlock" {
			t.Fatalf("method %s", call.Method)
		}
		var params []interface{}
		if err := json.Unmarshal(call.Params, &params); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(params, []interface{}{workHash, proof}) {
			t.Fatalf("params %#v", params)
		}
		return true
	})
	defer srv.Close()

	ok, err := client.SubmitOGGAuxBlock(workHash, proof)
	if err != nil || !ok {
		t.Fatalf("submit ok=%v err=%v", ok, err)
	}
	if _, err := client.SubmitOGGAuxBlock("0x01", proof); err == nil {
		t.Fatal("accepted malformed work hash")
	}
}

func TestGetOGGBlockRewardRPC(t *testing.T) {
	srv, client := newRPCServer(t, func(call testRPCRequest) interface{} {
		if call.Method != "eth_getBlockReward" {
			t.Fatalf("method %s", call.Method)
		}
		var params []string
		if err := json.Unmarshal(call.Params, &params); err != nil {
			t.Fatal(err)
		}
		if len(params) != 1 || params[0] != "0x5208" {
			t.Fatalf("params %#v", params)
		}
		return map[string]interface{}{
			"blockNumber": "0x5208", "total": "0x64", "miner": "0x2d",
			"staking": "0x28", "tribe": "0x7", "maintenance": "0x8",
		}
	})
	defer srv.Close()

	reward, err := client.GetOGGBlockReward(21000)
	if err != nil {
		t.Fatal(err)
	}
	if reward.Miner != "0x2d" || reward.Staking != "0x28" {
		t.Fatalf("bad reward %+v", reward)
	}
}

func TestDigibyteGBTPinsScryptAndRejectsOtherAlgorithms(t *testing.T) {
	for _, kind := range []string{"scrypt", "sha256d", "missing", "wrong-version", "mweb", "unknown-rule"} {
		t.Run(kind, func(t *testing.T) {
			srv, c := newRPCServer(t, func(call testRPCRequest) interface{} {
				var params []interface{}
				if json.Unmarshal(call.Params, &params) != nil || len(params) != 2 || params[1] != "scrypt" {
					t.Fatal("algorithm not explicitly selected")
				}
				rules := params[0].(map[string]interface{})["rules"].([]interface{})
				if len(rules) != 1 || rules[0] != "segwit" {
					t.Fatal("DGB must not request MWEB")
				}
				out := map[string]interface{}{"pow_algo": "scrypt", "pow_algo_id": 1, "version": 536870916, "rules": []string{"!segwit"}}
				switch kind {
				case "sha256d":
					out["pow_algo"] = "sha256d"
					out["pow_algo_id"] = 0
				case "missing":
					delete(out, "pow_algo_id")
				case "wrong-version":
					out["version"] = 536871428
				case "mweb":
					out["mweb"] = "00"
				case "unknown-rule":
					out["rules"] = []string{"!unimplemented"}
				}
				return out
			})
			defer srv.Close()
			_, err := c.GetDigibyteBlockTemplate()
			if (err == nil) != (kind == "scrypt") {
				t.Fatalf("kind=%s err=%v", kind, err)
			}
		})
	}
}

func TestDogecoinGBTDoesNotInheritDigibyteRules(t *testing.T) {
	srv, client := newRPCServer(t, func(call testRPCRequest) interface{} {
		var params []map[string]interface{}
		if err := json.Unmarshal(call.Params, &params); err != nil {
			t.Fatal(err)
		}
		if len(params) != 1 || len(params[0]) != 0 {
			t.Fatalf("DOGE params %#v", params)
		}
		return map[string]interface{}{"version": 1, "height": 3, "bits": "1d00ffff"}
	})
	defer srv.Close()

	if _, err := client.GetDogecoinBlockTemplate(); err != nil {
		t.Fatal(err)
	}
}

func TestSubmitRawBlockNullMeansAccepted(t *testing.T) {
	srv, client := newRPCServer(t, func(call testRPCRequest) interface{} {
		if call.Method != "submitblock" {
			t.Fatalf("method %s", call.Method)
		}
		return nil
	})
	defer srv.Close()

	ok, err := client.SubmitRawBlock("00")
	if err != nil || !ok {
		t.Fatalf("submit ok=%v err=%v", ok, err)
	}
}

func TestDogecoinAuxRPC(t *testing.T) {
	calls := 0
	srv, client := newRPCServer(t, func(call testRPCRequest) interface{} {
		calls++
		if calls == 1 {
			return map[string]interface{}{"hash": "aa", "chainid": 98, "height": 4, "bits": "1d00ffff"}
		}
		return true
	})
	defer srv.Close()

	tpl, err := client.CreateAuxBlock()
	if err != nil || tpl.ChainID != 98 {
		t.Fatalf("template=%+v err=%v", tpl, err)
	}
	hash := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	ok, err := client.SubmitAuxBlock(hash, "00")
	if err != nil || !ok {
		t.Fatalf("submit ok=%v err=%v", ok, err)
	}
}

func TestBlockTemplateToParentTemplateByteOrder(t *testing.T) {
	txid := "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"
	prev := "1f1e1d1c1b1a191817161514131211100f0e0d0c0b0a09080706050403020100"
	tpl := &BlockTemplateReply{
		Version: 2, PreviousBlockHash: prev,
		Transactions: []GBTTransaction{{Data: "010203", TxID: txid}},
		CoinbaseAux:  map[string]string{"flags": "fabe"}, CoinbaseValue: 50,
		CurTime: 1700000000, Bits: "1d00ffff", Height: 123,
		DefaultWitnessCommitment: "6a01aa", MWEB: "deadbeef",
	}
	parent, err := tpl.ToParentTemplate()
	if err != nil {
		t.Fatal(err)
	}
	if parent.Previous[0] != 0x00 || parent.Previous[31] != 0x1f {
		t.Fatalf("previous hash not converted from display order: %x", parent.Previous)
	}
	if parent.Transactions[0].TxID[0] != 0x1f || parent.Transactions[0].TxID[31] != 0x00 {
		t.Fatalf("txid not converted from display order: %x", parent.Transactions[0].TxID)
	}
	if parent.Bits != 0x1d00ffff || parent.CurTime != 1700000000 || parent.Height != 123 {
		t.Fatalf("numeric template fields wrong: %+v", parent)
	}
	if !reflect.DeepEqual(parent.CoinbaseAux["flags"], []byte{0xfa, 0xbe}) ||
		!reflect.DeepEqual(parent.WitnessCommitment, []byte{0x6a, 0x01, 0xaa}) ||
		!reflect.DeepEqual(parent.MWEB, []byte{0xde, 0xad, 0xbe, 0xef}) {
		t.Fatalf("decoded template fields wrong: %+v", parent)
	}
}
