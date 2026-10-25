package rpc

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/HawyUkr/open-progpow-pool/forgepool"
	"github.com/btcsuite/btcd/wire"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rlp"
	"math/big"
	"net"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

// No mainnet endpoints accepted. Each case mines one shared Scrypt solution
// and checks all participating native daemons, canonical hashes and UTXOs.
func TestOptionalSOQNativeMergedMining(t *testing.T) {
	path := os.Getenv("RGN_SOQ_NATIVE_LAB")
	if path == "" {
		t.Skip("explicit disposable regression nodes required")
	}
	b, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	var urls map[string]string
	if json.Unmarshal(b, &urls) != nil || len(urls) != 3 {
		t.Fatal("three native regression endpoints required")
	}
	clients := map[string]*RPCClient{}
	for _, sym := range []string{"DGB", "DOGE", "SOQ"} {
		u, e := url.Parse(urls[sym])
		if e != nil || u.Scheme != "http" || u.User != nil || net.ParseIP(u.Hostname()) == nil || !net.ParseIP(u.Hostname()).IsLoopback() {
			t.Fatal("loopback only")
		}
		c := NewRPCClient(sym, u.String(), "10s", "test", "test-only")
		r, e := c.doPost(c.Url, "getblockchaininfo", []interface{}{})
		if e != nil {
			t.Fatal(e)
		}
		var state struct{ Chain string }
		if unmarshalRPCResult(r, &state) != nil || state.Chain != "regtest" {
			t.Fatal("refusing non-regtest submission")
		}
		clients[sym] = c
	}
	address := "sq1ptk79z875zygp72awur73j9zk332r0vqkjwcsp3mcdg73nqyuwktq87gczj"
	scripts := map[string][]byte{}
	for sym, s := range map[string]string{"DGB": "0014706267a5255e3666936bcb9a777667599f387546", "DOGE": "76a9144b1508ebe06723842994adf33bdba36b4eba44c488ac", "SOQ": "51205dbc511fd411101f2baee0fd1914568c5437b01693b100c7786a3d19809c7596"} {
		scripts[sym], _ = hex.DecodeString(s)
	}
	if e = clients["SOQ"].ValidateSOQPayout(address, scripts["SOQ"]); e != nil {
		t.Fatal(e)
	}
	for _, mask := range []uint8{35, 33, 34, 32, 1, 2, 3} {
		parents := map[string]forgepool.ParentTemplate{}
		candidates := map[uint32]*forgepool.DogeCandidate{}
		bits := map[string]uint8{"DGB": 1, "DOGE": 2, "SOQ": 32}
		ids := map[string]uint32{"DOGE": 98, "SOQ": 21329}
		for _, sym := range []string{"DGB", "DOGE", "SOQ"} {
			if mask&bits[sym] == 0 {
				continue
			}
			var reply *BlockTemplateReply
			if sym == "DGB" {
				reply, e = clients[sym].GetDigibyteBlockTemplate()
			} else {
				reply, e = clients[sym].GetAuxiliaryBlockTemplate(sym == "SOQ")
			}
			if e != nil {
				t.Fatal(sym, e)
			}
			parents[sym], e = reply.ToParentTemplate()
			if e != nil {
				t.Fatal(e)
			}
			if sym != "DGB" {
				candidates[ids[sym]], e = forgepool.BuildAuxCandidate(parents[sym], scripts[sym], 17, ids[sym])
				if e != nil {
					t.Fatal(e)
				}
			}
		}
		stamp := uint64(time.Now().Unix())
		height := uint64(1125600) + uint64(mask)
		fields := []interface{}{common.Hash{}, common.Hash{}, common.Address{}, common.Hash{}, common.Hash{}, common.Hash{}, [256]byte{}, big.NewInt(1), new(big.Int).SetUint64(height), uint64(8000000), uint64(0), stamp, []byte{}, common.Hash{}, [8]byte{}}
		header, _ := rlp.EncodeToBytes(fields)
		work := crypto.Keccak256Hash(header)
		if e = forgepool.ValidateOptionalSOQHeader(header, work, stamp, 1125600); e != nil {
			t.Fatal(e)
		}
		parent, script := parents["DGB"], scripts["DGB"]
		if mask&1 == 0 {
			parent, _ = forgepool.SyntheticParentTemplate(1, uint32(stamp))
			script = []byte{0x6a}
		}
		job, e := forgepool.BuildMultiParentV4(parent, work.Hex(), candidates, script, 17, 4, 4, mask&1 != 0)
		if e != nil {
			t.Fatal(e)
		}
		tmpl, e := job.MultiTemplate(stamp, stamp+300)
		if e != nil || tmpl.Mask != mask {
			t.Fatal(mask, e)
		}
		digest, e := forgepool.MultiDigest(tmpl, work)
		if e != nil {
			t.Fatal(e)
		}
		var sigs [][]byte
		for _, suffix := range []string{"1", "2"} {
			key, _ := crypto.HexToECDSA(strings.Repeat("0", 63) + suffix)
			sig, e := crypto.Sign(digest[:], key)
			if e != nil {
				t.Fatal(e)
			}
			sigs = append(sigs, sig)
		}
		sigs, e = forgepool.CanonicalMultiSignatures(tmpl, work, sigs)
		if e != nil {
			t.Fatal(e)
		}
		if e = job.BindCertificate(tmpl, sigs); e != nil {
			t.Fatal(e)
		}
		zero := make([]byte, 4)
		var nonce uint32
		for ; nonce < 1000000; nonce++ {
			h, e := job.Header(zero, zero, parent.CurTime, nonce)
			if e != nil {
				t.Fatal(e)
			}
			hash, e := forgepool.ScryptHash(h)
			if e != nil {
				t.Fatal(e)
			}
			ok := true
			for _, p := range parents {
				hit, e := forgepool.MeetsCompactTarget(hash, p.Bits)
				if e != nil {
					t.Fatal(e)
				}
				ok = ok && hit
			}
			if ok {
				break
			}
		}
		if nonce == 1000000 {
			t.Fatal("private CPU budget exhausted")
		}
		for _, sym := range []string{"DGB", "DOGE", "SOQ"} {
			if mask&bits[sym] == 0 {
				continue
			}
			var body, coinbase []byte
			var hash string
			if sym == "DGB" {
				body, e = job.FullBlock(zero, zero, parent.CurTime, nonce)
				if e != nil {
					t.Fatal(e)
				}
				cb, _ := job.Coinbase.BaseCoinbase(zero, zero)
				coinbase = cb
				one := sha256.Sum256(body[:80])
				two := sha256.Sum256(one[:])
				hash = forgepool.DisplayHash(forgepool.Hash(two))
				probe, e := job.TreasuryClaimProbe(zero, zero, parent.CurTime, nonce)
				if e != nil {
					t.Fatal(e)
				}
				if e = clients[sym].ValidateFullTreasuryClaim(context.Background(), body, probe); e != nil {
					t.Fatal(sym, "full native claim", e)
				}
			} else {
				c := candidates[ids[sym]]
				body, e = job.AuxBlockFor(ids[sym], zero, zero, parent.CurTime, nonce)
				if e != nil {
					t.Fatal(e)
				}
				coinbase = c.Coinbase
				hash = c.DisplayHash
				proposal, e := c.AuxValidationBlock()
				if e != nil {
					t.Fatal(e)
				}
				probe, e := c.TreasuryClaimProbe(ids[sym], scripts[sym])
				if e != nil {
					t.Fatal(e)
				}
				if e = clients[sym].ValidateFullTreasuryClaim(context.Background(), proposal, probe); e != nil {
					t.Fatal(sym, "full native claim", e)
				}
			}
			accepted, e := clients[sym].SubmitRawBlock(hex.EncodeToString(body))
			if e != nil || !accepted {
				t.Fatal(mask, sym, "native submit", e)
			}
			r, e := clients[sym].doPost(clients[sym].Url, "getblockhash", []interface{}{parents[sym].Height})
			if e != nil {
				t.Fatal(e)
			}
			var canonical string
			if unmarshalRPCResult(r, &canonical) != nil || canonical != hash {
				t.Fatal(mask, sym, "not canonical")
			}
			var cb wire.MsgTx
			if e = cb.Deserialize(bytes.NewReader(coinbase)); e != nil {
				t.Fatal(e)
			}
			paid := int64(0)
			index := -1
			for n, out := range cb.TxOut {
				if bytes.Equal(out.PkScript, scripts[sym]) {
					paid += out.Value
					index = n
				} else if out.Value != 0 {
					t.Fatal("reward diverted", sym)
				}
			}
			if paid != parents[sym].CoinbaseValue || index < 0 {
				t.Fatal(sym, "full payout missing")
			}
			r, e = clients[sym].doPost(clients[sym].Url, "gettxout", []interface{}{cb.TxHash().String(), index, true})
			if e != nil {
				t.Fatal(e)
			}
			var utxo struct {
				Confirmations int
				Coinbase      bool
				Script        struct{ Hex string } `json:"scriptPubKey"`
			}
			if unmarshalRPCResult(r, &utxo) != nil || utxo.Confirmations < 1 || !utxo.Coinbase || utxo.Script.Hex != hex.EncodeToString(scripts[sym]) {
				t.Fatal(sym, "reward UTXO missing")
			}
			t.Logf("REGTEST ONLY mask=%d %s height=%d canonical=%s fullRewardBaseUnits=%d confirmations=%d", mask, sym, parents[sym].Height, hash, paid, utxo.Confirmations)
		}
	}
}
