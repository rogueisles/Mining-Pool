// forge-soq-preflight validates an unsolved SOQ candidate without submitting it.
// Credentials are read from stdin and are never included in the public report.
package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/HawyUkr/open-progpow-pool/forgepool"
	"github.com/HawyUkr/open-progpow-pool/rpc"
	"io"
	"os"
	"time"
)

func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, "SOQ preflight failed:", e)
		os.Exit(1)
	}
}
func run() error {
	var cfg struct{ URL, Username, Password, Address, Script string }
	d := json.NewDecoder(io.LimitReader(os.Stdin, 16384))
	d.DisallowUnknownFields()
	if e := d.Decode(&cfg); e != nil {
		return fmt.Errorf("invalid private stdin configuration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c, e := rpc.NewValidationRPCClient(ctx, "SOQ", cfg.URL, "4s", cfg.Username, cfg.Password)
	if e != nil {
		return e
	}
	script, e := hex.DecodeString(cfg.Script)
	if e != nil {
		return e
	}
	if e = c.CheckAuxiliaryMainnet(forgepool.SOQMainnetGenesis); e != nil {
		return e
	}
	if e = c.ValidateSOQPayout(cfg.Address, script); e != nil {
		return e
	}
	reply, e := c.GetAuxiliaryBlockTemplate(true)
	if e != nil {
		return e
	}
	parent, e := reply.ToParentTemplate()
	if e != nil {
		return e
	}
	candidate, e := forgepool.BuildAuxCandidate(parent, script, 17, forgepool.SOQAuxChainID)
	if e != nil {
		return e
	}
	proposal, e := candidate.AuxValidationBlock()
	if e != nil {
		return e
	}
	probe, e := candidate.TreasuryClaimProbe(forgepool.SOQAuxChainID, script)
	if e != nil {
		return e
	}
	if e = c.ValidateFullTreasuryClaim(ctx, proposal, probe); e != nil {
		return e
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]interface{}{"passed": true, "nativeMainnetIdentityChecked": true, "payoutAddress": cfg.Address, "payoutScript": cfg.Script, "candidateHeight": parent.Height, "fullCoinbaseBaseUnits": parent.CoinbaseValue, "witnessCommitment": len(parent.WitnessCommitment) > 0, "nativeUnsolvedProposalAccepted": true, "oneBaseUnitOverclaimRejected": true, "blocksSubmitted": 0, "transactionsSent": 0, "liveRGNConfigurationChanged": false, "checkedUnix": time.Now().Unix()})
}
