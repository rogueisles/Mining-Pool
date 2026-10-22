package rpc

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/HawyUkr/open-progpow-pool/forgepool"
)

// TestNativeTreasuryClaimBoundaries uses only explicitly provided disposable
// regtest endpoints. It never submits a native block or accesses a wallet.
func TestNativeTreasuryClaimBoundaries(t *testing.T) {
	path := os.Getenv("RGN_TREASURY_NATIVE_ENDPOINTS")
	if path == "" {
		t.Skip("disposable native regtest endpoints required")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var endpoints map[string]string
	if json.Unmarshal(data, &endpoints) != nil || len(endpoints) != 2 {
		t.Fatal("DGB and DOGE regtest endpoints required")
	}
	for _, symbol := range []string{"DGB", "DOGE"} {
		t.Run(symbol, func(t *testing.T) {
			u, err := url.Parse(endpoints[symbol])
			if err != nil || u.Scheme != "http" || u.User != nil || net.ParseIP(u.Hostname()) == nil || !net.ParseIP(u.Hostname()).IsLoopback() {
				t.Fatal("loopback regtest endpoint required")
			}
			client := NewRPCClient(symbol, u.String(), "10s", "test", "test-only")
			res, err := client.doPost(client.Url, "getblockchaininfo", []interface{}{})
			if err != nil {
				t.Fatal(err)
			}
			var info struct{ Chain string }
			if unmarshalRPCResult(res, &info) != nil || info.Chain != "regtest" {
				t.Fatal("refusing non-regtest endpoint")
			}
			var reply *BlockTemplateReply
			if symbol == "DGB" {
				reply, err = client.GetDigibyteBlockTemplate()
			} else {
				reply, err = client.GetAuxiliaryBlockTemplate(false)
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(reply.Transactions) == 0 {
				t.Fatal("native fee-bearing transaction required")
			}
			template, err := reply.ToParentTemplate()
			if err != nil {
				t.Fatal(err)
			}
			if symbol == "DGB" {
				hasWitness := false
				for _, tx := range template.Transactions {
					if len(tx.Data) > 6 && tx.Data[4] == 0 && tx.Data[5] == 1 {
						hasWitness = true
					}
				}
				if len(template.WitnessCommitment) != 38 || !hasWitness {
					t.Fatal("fee-bearing native witness fixture required")
				}
			}
			var fees int64
			for _, tx := range reply.Transactions {
				fees += tx.Fee
			}
			if fees <= 0 || fees >= template.CoinbaseValue {
				t.Fatal("native fixture fees missing")
			}
			scriptHex := "76a9144b1508ebe06723842994adf33bdba36b4eba44c488ac"
			if symbol == "DGB" {
				scriptHex = "0014706267a5255e3666936bcb9a777667599f387546"
			}
			script, _ := hex.DecodeString(scriptHex)
			if symbol == "DGB" {
				t.Run("completed-V4-extranonce-values", func(t *testing.T) {
					job, err := forgepool.BuildMultiParentV4(template, "0x"+strings.Repeat("0", 63)+"1", nil, script, 7, 4, 4, true)
					if err != nil {
						t.Fatal(err)
					}
					for value := 0; value < 256; value++ {
						extra := bytes.Repeat([]byte{byte(value)}, 8)
						block, err := job.FullBlock(extra[:4], extra[4:], template.CurTime, 0)
						if err != nil {
							t.Fatal(err)
						}
						probe, err := job.TreasuryClaimProbe(extra[:4], extra[4:], template.CurTime, 0)
						if err != nil || client.ValidateFullTreasuryClaim(context.Background(), block, probe) != nil {
							t.Fatal("completed native body or full treasury claim rejected", value, err)
						}
					}
				})
			}
			build := func(p forgepool.ParentTemplate) ([]byte, []byte, error) {
				if symbol == "DGB" {
					job, e := forgepool.BuildMultiParentV4(p, "0x"+strings.Repeat("0", 63)+"1", nil, script, 7, 4, 4, true)
					if e != nil {
						return nil, nil, e
					}
					block, e := job.FullBlock(make([]byte, 4), make([]byte, 4), p.CurTime, 0)
					if e != nil {
						return nil, nil, e
					}
					probe, e := job.TreasuryClaimProbe(make([]byte, 4), make([]byte, 4), p.CurTime, 0)
					return block, probe, e
				}
				candidate, e := forgepool.BuildAuxCandidate(p, script, 7, 98)
				if e != nil {
					return nil, nil, e
				}
				block, e := candidate.AuxValidationBlock()
				if e != nil {
					return nil, nil, e
				}
				probe, e := candidate.TreasuryClaimProbe(98, script)
				return block, probe, e
			}
			cases := []struct {
				name string
				p    forgepool.ParentTemplate
			}{{"included-fees", template}}
			empty := template
			empty.Transactions = nil
			empty.CoinbaseValue -= fees
			empty.WitnessCommitment = nil
			cases = append(cases, struct {
				name string
				p    forgepool.ParentTemplate
			}{"different-transaction-set", empty})
			if symbol == "DGB" {
				witness := empty
				// One zero coinbase witness leaf and its unchanged zero reserve.
				first := sha256.Sum256(make([]byte, 64))
				digest := sha256.Sum256(first[:])
				witness.WitnessCommitment = append([]byte{0x6a, 0x24, 0xaa, 0x21, 0xa9, 0xed}, digest[:]...)
				cases = append(cases, struct {
					name string
					p    forgepool.ParentTemplate
				}{"empty-witness", witness})
			}
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					for _, delta := range []int64{0, -1, -19, 1} {
						p := tc.p
						p.CoinbaseValue += delta
						block, probe, err := build(p)
						if err != nil {
							t.Fatal(err)
						}
						accepted := client.ValidateAuxiliaryProposal(context.Background(), block) == nil
						if accepted != (delta <= 0) {
							t.Fatalf("native original delta %d acceptance=%v", delta, accepted)
						}
						err = client.ValidateFullTreasuryClaim(context.Background(), block, probe)
						if (err == nil) != (delta == 0) {
							t.Fatalf("native amount boundary delta %d: %v", delta, err)
						}
						t.Logf("%s/%s claimDelta=%d originalAccepted=%v fullClaimAccepted=%v", symbol, tc.name, delta, accepted, err == nil)
					}
				})
			}
			fmt.Fprintf(os.Stdout, "Native treasury amount boundary passed: %s, included fees %d, transactions %d\n", symbol, fees, len(reply.Transactions))
		})
	}
}
