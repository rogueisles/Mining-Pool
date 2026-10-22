package rpc

import (
	"encoding/hex"
	"github.com/HawyUkr/open-progpow-pool/forgepool"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"math/big"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// This process-level lab uses a public development signer and fabricated
// external templates. It must never contact a production chain or signer.
func TestRevenueAnchorPrivateP2PSubmit(t *testing.T) {
	endpoint := os.Getenv("FORGE_ANCHOR_PRIVATE_P2P_RPC")
	if endpoint == "" {
		t.Skip("explicit isolated process lab required")
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" {
		t.Fatal("private loopback RPC required")
	}
	c := NewRPCClient("private-p2p-lab", endpoint, "10s", "", "")
	response, err := c.doPost(endpoint, "admin_nodeInfo", []interface{}{})
	if err != nil {
		t.Fatal(err)
	}
	var info struct {
		Protocols struct {
			Eth struct {
				Network int
				Config  struct {
					Forge struct {
						Private bool `json:"privateCpuTest"`
						Anchor  bool `json:"requireDgbOrDogeWork"`
					}
				}
			}
		}
	}
	if unmarshalRPCResult(response, &info) != nil || info.Protocols.Eth.Network != 7008902 || !info.Protocols.Eth.Config.Forge.Private || !info.Protocols.Eth.Config.Forge.Anchor {
		t.Fatal("explicit private CPU network and anchor rule required")
	}
	var work *OGGAuxBlock
	for i := 0; i < 40; i++ {
		work, err = c.CreateOGGAuxBlock()
		if err == nil {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	stamp, err := strconv.ParseUint(strings.TrimPrefix(work.Timestamp, "0x"), 16, 64)
	if err != nil {
		t.Fatal(err)
	}
	target, ok := new(big.Int).SetString(strings.TrimPrefix(work.Target, "0x"), 16)
	if !ok || target.BitLen() > 241 || target.BitLen() < 235 {
		t.Fatal("unexpected private target")
	}
	parent, err := forgepool.SyntheticParentTemplate(1, uint32(stamp))
	if err != nil {
		t.Fatal(err)
	}
	native := forgepool.ParentTemplate{Version: 4, Previous: forgepool.Hash{98}, Height: 111, Bits: 0x207fffff, CurTime: uint32(stamp), CoinbaseValue: 100000000}
	script, _ := hex.DecodeString("76a9144b1508ebe06723842994adf33bdba36b4eba44c488ac")
	aux, err := forgepool.BuildAuxCandidate(native, script, 19, 98)
	if err != nil {
		t.Fatal(err)
	}
	job, err := forgepool.BuildMultiParent(parent, work.WorkHash, map[uint32]*forgepool.DogeCandidate{98: aux}, []byte{0x6a}, 19, 4, 4, false)
	if err != nil {
		t.Fatal(err)
	}
	tmpl, err := job.MultiTemplate(stamp, stamp+300)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := forgepool.MultiDigest(tmpl, common.HexToHash(work.WorkHash))
	if err != nil {
		t.Fatal(err)
	}
	key, _ := crypto.HexToECDSA(strings.Repeat("0", 63) + "1") // Public development fixture only.
	sig, err := crypto.Sign(digest.Bytes(), key)
	if err != nil {
		t.Fatal(err)
	}
	extra := make([]byte, 4)
	var nonce uint32
	for ; nonce < 2000000; nonce++ {
		header, e := job.Header(extra, extra, parent.CurTime, nonce)
		if e != nil {
			t.Fatal(e)
		}
		hash, e := forgepool.ScryptHash(header)
		if e != nil {
			t.Fatal(e)
		}
		if forgepool.HashValue(hash).Cmp(target) <= 0 {
			break
		}
	}
	if nonce == 2000000 {
		t.Fatal("bounded private work budget exhausted")
	}
	proof, err := job.BuildMultiProof(extra, extra, parent.CurTime, nonce, tmpl, [][]byte{sig})
	if err != nil {
		t.Fatal(err)
	}
	blob, err := forgepool.EncodeMultiProof(proof)
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := c.SubmitOGGAuxBlock(work.WorkHash, hex.EncodeToString(blob))
	if err != nil || !accepted {
		t.Fatal("private block submission rejected", accepted, err)
	}
	t.Log("Submitted real Scrypt proof to compiled OGG process", work.Height, "mask", tmpl.Mask)
}
