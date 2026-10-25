package rpc

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/HawyUkr/open-progpow-pool/forgepool"
	"github.com/btcsuite/btcd/wire"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// This test mines only easy, disposable native regression chains. It rejects
// all non-loopback endpoints and checks both chain identities before submission.
func TestV4NativeDGBAndDogeTreasuryAcceptance(t *testing.T) {
	path := os.Getenv("RGN_V4_NATIVE_LAB")
	if path == "" {
		t.Skip("explicit disposable native regtest endpoints required")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var urls map[string]string
	if json.Unmarshal(b, &urls) != nil || len(urls) != 2 {
		t.Fatal("two native regression endpoints required")
	}
	clients := map[string]*RPCClient{}
	for _, sym := range []string{"DGB", "DOGE"} {
		u, e := url.Parse(urls[sym])
		if e != nil || u.Scheme != "http" || u.User != nil || net.ParseIP(u.Hostname()) == nil || !net.ParseIP(u.Hostname()).IsLoopback() {
			t.Fatal("loopback only")
		}
		c := NewRPCClient(sym, u.String(), "5s", "test", "test-only")
		r, e := c.doPost(c.Url, "getblockchaininfo", []interface{}{})
		if e != nil {
			t.Fatal(e)
		}
		var state struct{ Chain string }
		if unmarshalRPCResult(r, &state) != nil || state.Chain != "regtest" {
			t.Fatal("refusing any non-regtest native submission")
		}
		clients[sym] = c
	}
	dt, err := clients["DGB"].GetDigibyteBlockTemplate()
	if err != nil {
		t.Fatal(err)
	}
	gt, err := clients["DOGE"].GetAuxiliaryBlockTemplate(false)
	if err != nil {
		t.Fatal(err)
	}
	dgb, err := dt.ToParentTemplate()
	if err != nil {
		t.Fatal(err)
	}
	doge, err := gt.ToParentTemplate()
	if err != nil {
		t.Fatal(err)
	}
	dgbScript, _ := hex.DecodeString("0014706267a5255e3666936bcb9a777667599f387546")
	dogeScript, _ := hex.DecodeString("76a9144b1508ebe06723842994adf33bdba36b4eba44c488ac")
	aux, err := forgepool.BuildAuxCandidate(doge, dogeScript, 17, 98)
	if err != nil {
		t.Fatal(err)
	}
	work := common.HexToHash("0x" + strings.Repeat("42", 32))
	job, err := forgepool.BuildMultiParentV4(dgb, work.Hex(), map[uint32]*forgepool.DogeCandidate{98: aux}, dgbScript, 17, 4, 4, true)
	if err != nil {
		t.Fatal(err)
	}
	stamp := uint64(time.Now().Unix())
	tmpl, err := job.MultiTemplate(stamp, stamp+300)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := forgepool.MultiDigest(tmpl, work)
	if err != nil {
		t.Fatal(err)
	}
	var sigs [][]byte
	for _, suffix := range []string{"1", "2"} {
		key, e := crypto.HexToECDSA(strings.Repeat("0", 63) + suffix)
		if e != nil {
			t.Fatal(e)
		} // Public, disposable DEV fixture identities.
		sig, e := crypto.Sign(digest.Bytes(), key)
		if e != nil {
			t.Fatal(e)
		}
		sigs = append(sigs, sig)
	}
	sigs, err = forgepool.CanonicalMultiSignatures(tmpl, work, sigs)
	if err != nil {
		t.Fatal(err)
	}
	if err = job.BindCertificate(tmpl, sigs); err != nil {
		t.Fatal(err)
	}
	zero := make([]byte, 4)
	var nonce uint32
	for ; nonce < 1000000; nonce++ {
		h, e := job.Header(zero, zero, dgb.CurTime, nonce)
		if e != nil {
			t.Fatal(e)
		}
		raw, e := forgepool.ScryptHash(h)
		if e != nil {
			t.Fatal(e)
		}
		okD, e := forgepool.MeetsCompactTarget(raw, dgb.Bits)
		if e != nil {
			t.Fatal(e)
		}
		okG, e := forgepool.MeetsCompactTarget(raw, doge.Bits)
		if e != nil {
			t.Fatal(e)
		}
		if okD && okG {
			break
		}
	}
	if nonce == 1000000 {
		t.Fatal("bounded private CPU mining budget exhausted")
	}
	block, err := job.FullBlock(zero, zero, dgb.CurTime, nonce)
	if err != nil {
		t.Fatal(err)
	}
	probe, err := job.TreasuryClaimProbe(zero, zero, dgb.CurTime, nonce)
	if err != nil {
		t.Fatal(err)
	}
	if err = clients["DGB"].ValidateFullTreasuryClaim(context.Background(), block, probe); err != nil {
		t.Fatal("native DGB full-claim validation", err)
	}
	dogeBlock, err := job.DogeAuxPowBlock(zero, zero, dgb.CurTime, nonce)
	if err != nil {
		t.Fatal(err)
	}
	first := sha256.Sum256(block[:80])
	hash := sha256.Sum256(first[:])
	dgbHash := forgepool.DisplayHash(forgepool.Hash(hash))
	var nativeBlock wire.MsgBlock
	if err = nativeBlock.Deserialize(bytes.NewReader(block)); err != nil {
		t.Fatal(err)
	}
	var nativeDogeCoinbase wire.MsgTx
	if err = nativeDogeCoinbase.Deserialize(bytes.NewReader(aux.Coinbase)); err != nil {
		t.Fatal(err)
	}
	for _, sym := range []string{"DGB", "DOGE"} {
		c := clients[sym]
		body := block
		hash := dgbHash
		height := dgb.Height
		cb := nativeBlock.Transactions[0]
		script := dgbScript
		value := dgb.CoinbaseValue
		if sym == "DOGE" {
			body = dogeBlock
			hash = aux.DisplayHash
			height = doge.Height
			cb = &nativeDogeCoinbase
			script = dogeScript
			value = doge.CoinbaseValue
		}
		accepted, e := c.SubmitRawBlock(hex.EncodeToString(body))
		if e != nil || !accepted {
			t.Fatal(sym, "native submission failed", e)
		}
		r, e := c.doPost(c.Url, "getblockhash", []interface{}{height})
		if e != nil {
			t.Fatal(e)
		}
		var canonical string
		if unmarshalRPCResult(r, &canonical) != nil || canonical != hash {
			t.Fatal(sym, "candidate is not canonical")
		}
		paid := int64(0)
		index := -1
		for n, out := range cb.TxOut {
			if bytes.Equal(out.PkScript, script) {
				paid += out.Value
				index = n
			} else if out.Value != 0 {
				t.Fatal(sym, "value diverted from treasury")
			}
		}
		if paid != value || index < 0 {
			t.Fatal(sym, "full native claim did not pay treasury", paid, value)
		}
		r, e = c.doPost(c.Url, "gettxout", []interface{}{cb.TxHash().String(), index, true})
		if e != nil {
			t.Fatal(e)
		}
		var utxo struct {
			Confirmations int
			Coinbase      bool
			Script        struct{ Hex string } `json:"scriptPubKey"`
		}
		if unmarshalRPCResult(r, &utxo) != nil || utxo.Confirmations < 1 || !utxo.Coinbase || utxo.Script.Hex != hex.EncodeToString(script) {
			t.Fatal(sym, "canonical treasury coinbase UTXO missing")
		}
		t.Logf("REGTEST ONLY: %s V4 shared Scrypt solution accepted at height %d, canonical hash %s, full treasury value %d, confirmations %d", sym, height, hash, paid, utxo.Confirmations)
	}
}
