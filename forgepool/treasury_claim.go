package forgepool

import (
	"errors"
	"math"
)

// TreasuryClaimProbe constructs an unmined native proposal claiming one base
// unit more than this candidate. It never changes the candidate being signed.
func (d *DogeCandidate) TreasuryClaimProbe(chainID uint32, script []byte) ([]byte, error) {
	if d == nil || d.Template.CoinbaseValue <= 0 || d.Template.CoinbaseValue == math.MaxInt64 {
		return nil, errors.New("native treasury claim cannot be incremented")
	}
	template := d.Template
	template.CoinbaseValue++
	probe, err := BuildAuxCandidate(template, script, d.JobNonce, chainID)
	if err != nil {
		return nil, err
	}
	return probe.AuxValidationBlock()
}

// TreasuryClaimProbe preserves the parent body, certificate and auxiliary tree,
// changing only the treasury amount and its derived transaction/header hashes.
// The returned block is for proposal validation only, never block submission.
func (j *DigibyteJob) TreasuryClaimProbe(extra1, extra2 []byte, ntime, nonce uint32) ([]byte, error) {
	if j == nil || !j.DigibyteActive || j.Coinbase == nil || len(j.Coinbase.Outputs) == 0 {
		return nil, errors.New("native parent treasury claim missing")
	}
	value := j.Coinbase.Outputs[0].Value
	if value <= 0 || value == math.MaxInt64 || value != j.Template.CoinbaseValue {
		return nil, errors.New("native treasury claim cannot be incremented")
	}
	plan := *j.Coinbase
	plan.Outputs = append([]TxOutput(nil), j.Coinbase.Outputs...)
	plan.Outputs[0].Value++
	scriptLen := len(plan.ScriptPrefix) + plan.ExtraNonce1Size + plan.ExtraNonce2Size + len(plan.ScriptSuffix)
	plan.Coinb1, plan.Coinb2 = plan.baseSplit(scriptLen)
	probe := *j
	probe.Template.CoinbaseValue++
	probe.Coinbase = &plan
	return probe.FullBlock(extra1, extra2, ntime, nonce)
}
