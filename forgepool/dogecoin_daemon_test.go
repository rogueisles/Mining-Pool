package forgepool_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/HawyUkr/open-progpow-pool/forgepool"
	"github.com/HawyUkr/open-progpow-pool/rpc"
)

func dogeRPC(t *testing.T, url, method string, params ...interface{}) json.RawMessage {
	t.Helper()
	body, err := json.Marshal(map[string]interface{}{"id": 1, "method": method, "params": params})
	if err != nil {
		t.Fatal(err)
	}
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
	if err = json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Error != nil {
		t.Fatalf("%s: %v", method, out.Error)
	}
	return out.Result
}

func mineDogeParent(t *testing.T, p *forgepool.DogeAuxPow, bits uint32, valid bool) {
	t.Helper()
	for nonce := uint32(0); nonce < 100000; nonce++ {
		binary.LittleEndian.PutUint32(p.ParentHeader[76:], nonce)
		hash, err := forgepool.ScryptHash(p.ParentHeader)
		if err != nil {
			t.Fatal(err)
		}
		ok, err := forgepool.MeetsCompactTarget(hash, bits)
		if err != nil {
			t.Fatal(err)
		}
		if ok == valid {
			a := sha256.Sum256(p.ParentHeader)
			p.ParentBlockHash = sha256.Sum256(a[:])
			return
		}
	}
	t.Fatal("regtest mining bound exhausted")
}

func TestOfficialDogecoinAuxPowAcceptance(t *testing.T) {
	url := os.Getenv("FORGE_DOGE_TEST_RPC")
	if url == "" {
		t.Skip("run scripts/test-forge-dogecoin.sh for isolated official daemon")
	}
	var info struct {
		Chain  string
		Blocks int64
	}
	if err := json.Unmarshal(dogeRPC(t, url, "getblockchaininfo"), &info); err != nil {
		t.Fatal(err)
	}
	if info.Chain != "regtest" || info.Blocks != 40 {
		t.Fatal("requires fresh isolated regtest at height 40")
	}
	client := rpc.NewRPCClient("isolated-doge", url, "5s", "forge-regtest", "disposable-test-only")
	for n, name := range []string{"parent-merkle", "chain-index", "commitment", "duplicate-magic", "bad-pow", "valid"} {
		t.Run(name, func(t *testing.T) {
			gbt, err := client.GetDogecoinBlockTemplate()
			if err != nil {
				t.Fatal(err)
			}
			tpl, err := gbt.ToParentTemplate()
			if err != nil {
				t.Fatal(err)
			}
			if len(tpl.Transactions) != 0 {
				t.Fatal("unexpected regtest mempool")
			}
			doge, err := forgepool.BuildDogeCandidate(tpl, []byte{0x51}, uint32(n+1))
			if err != nil {
				t.Fatal(err)
			}
			parent := tpl
			parent.Version = 2
			parent.Previous = forgepool.Hash{0x44}
			// A synthetic Digibyte-format parent exercises a nonempty coinbase
			// Merkle branch. This gate does not claim Digibyte block acceptance.
			parent.Transactions = []forgepool.TemplateTx{{TxID: forgepool.Hash{0x55}}, {TxID: forgepool.Hash{0x66}}}
			job, err := forgepool.BuildDigibyteJob(parent, "0x"+strings.Repeat("11", 32), doge, []byte{0x51}, 9, 4, 4)
			if err != nil {
				t.Fatal(err)
			}
			proof, err := job.BuildDogeAuxPow([]byte{1, 2, 3, 4}, []byte{5, 6, 7, 8}, tpl.CurTime, 0)
			if err != nil {
				t.Fatal(err)
			}
			switch name {
			case "parent-merkle":
				proof.ParentHeader[36] ^= 1
			case "chain-index":
				proof.ChainMerkleIndex ^= 1
			case "commitment":
				i := bytes.Index(proof.ParentCoinbase, job.Commitment)
				if i < 0 {
					t.Fatal("no commitment")
				}
				proof.ParentCoinbase[i+4] ^= 1
			case "duplicate-magic":
				copy(proof.ParentCoinbase[len(job.Coinbase.Coinb1):], forgepool.MergedMiningMagic)
			}
			if name == "commitment" || name == "duplicate-magic" {
				root := forgepool.MerkleRootFromBranch(forgepool.CoinbaseTxID(proof.ParentCoinbase), proof.ParentMerkleBranch, 0)
				copy(proof.ParentHeader[36:68], root[:])
			}
			mineDogeParent(t, proof, doge.Bits, name != "bad-pow")
			wire, err := proof.Serialize()
			if err != nil {
				t.Fatal(err)
			}
			block := append(append(append(append([]byte{}, doge.Header...), wire...), 1), doge.Coinbase...)
			if name == "valid" {
				built, err := job.DogeAuxPowBlock([]byte{1, 2, 3, 4}, []byte{5, 6, 7, 8}, tpl.CurTime, binary.LittleEndian.Uint32(proof.ParentHeader[76:]))
				if err != nil || !bytes.Equal(built, block) {
					t.Fatalf("submission builder differs from canonical proof: %v", err)
				}
				if err := job.ValidateDogeAuxPow(proof); err != nil {
					t.Fatal(err)
				}
			}
			ok, err := client.SubmitRawBlock(hex.EncodeToString(block))
			if name != "valid" {
				if err == nil || ok {
					t.Fatalf("daemon accepted %s", name)
				}
				t.Logf("daemon rejected %s: %v", name, err)
				return
			}
			if err != nil || !ok {
				t.Fatalf("valid AuxPoW rejected: %v", err)
			}
			var best string
			if err = json.Unmarshal(dogeRPC(t, url, "getbestblockhash"), &best); err != nil {
				t.Fatal(err)
			}
			if best != doge.DisplayHash {
				t.Fatalf("wrong accepted candidate: %s != %s", best, doge.DisplayHash)
			}
			if found, err := client.HasForgeBlock("0x" + best); err != nil || !found {
				t.Fatalf("canonical-body recovery lookup failed: %v", err)
			}
			if accepted, rejected, err := client.SubmitForgeBlock(hex.EncodeToString(block)); accepted || rejected || err == nil {
				t.Fatalf("duplicate must require reconciliation, got %v %v %v", accepted, rejected, err)
			}
			var raw string
			if err = json.Unmarshal(dogeRPC(t, url, "getblock", best, false), &raw); err != nil {
				t.Fatal(err)
			}
			if raw != hex.EncodeToString(block) {
				t.Fatal("daemon wire roundtrip differs")
			}
			t.Logf("accepted exact pool-built DOGE AuxPoW at height %d: %s", gbt.Height, best)
		})
	}
	var after struct{ Blocks int64 }
	if err := json.Unmarshal(dogeRPC(t, url, "getblockchaininfo"), &after); err != nil {
		t.Fatal(err)
	}
	if after.Blocks != 41 {
		t.Fatal(fmt.Sprintf("unexpected final height %d", after.Blocks))
	}
	// Exercise getauxblock only with a hash actually issued by that daemon.
	// This candidate is suitable for the compatibility RPC test, not OGG proof
	// assembly: getauxblock does not disclose the full child coinbase/header.
	aux, err := client.CreateAuxBlock()
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := forgepool.ParseDisplayHash(aux.Hash)
	if err != nil {
		t.Fatal(err)
	}
	bits, err := forgepool.ParseBitsHex(aux.Bits)
	if err != nil {
		t.Fatal(err)
	}
	gbt, err := client.GetDogecoinBlockTemplate()
	if err != nil {
		t.Fatal(err)
	}
	parent, err := gbt.ToParentTemplate()
	if err != nil {
		t.Fatal(err)
	}
	parent.Version = 2
	job, err := forgepool.BuildDigibyteJob(parent, "0x"+strings.Repeat("11", 32), &forgepool.DogeCandidate{Hash: leaf, Bits: bits}, []byte{0x51}, 9, 4, 4)
	if err != nil {
		t.Fatal(err)
	}
	proof, err := job.BuildDogeAuxPow([]byte{1, 2, 3, 4}, []byte{5, 6, 7, 8}, parent.CurTime, 0)
	if err != nil {
		t.Fatal(err)
	}
	mineDogeParent(t, proof, bits, true)
	// Official validation ignores this legacy CMerkleTx field. Zero remains
	// accepted; the pool builder nevertheless now emits canonical uint256 bytes.
	proof.ParentBlockHash = forgepool.Hash{}
	wire, err := proof.Serialize()
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := client.SubmitAuxBlock(strings.Repeat("aa", 32), hex.EncodeToString(wire)); err == nil || ok {
		t.Fatal("unissued DOGE candidate accepted")
	}
	if ok, err := client.SubmitAuxBlock(aux.Hash, hex.EncodeToString(wire)); err != nil || !ok {
		t.Fatalf("getauxblock rejected valid proof: %v", err)
	}
	var best string
	if err := json.Unmarshal(dogeRPC(t, url, "getbestblockhash"), &best); err != nil {
		t.Fatal(err)
	}
	if best != aux.Hash {
		t.Fatal("getauxblock accepted wrong candidate")
	}
	t.Logf("accepted issued getauxblock candidate at height 42: %s", best)
}
