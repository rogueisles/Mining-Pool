package forgesigner

import (
	"context"
	"errors"
	"github.com/HawyUkr/open-progpow-pool/forgepool"
	"testing"
)

type proposalTestSource struct {
	*testSource
	calls  int
	failAt int
	hook   func()
}

func (s *proposalTestSource) ValidateDogeProposal(context.Context, []byte) error {
	s.calls++
	if s.hook != nil {
		s.hook()
	}
	if s.calls == s.failAt {
		return errors.New("independent daemon rejection")
	}
	return nil
}

func TestProposalSignerRefusesWithoutIndependentValidator(t *testing.T) {
	s, src, b, r, _ := fixture(t)
	tpl, err := src.doge.ToParentTemplate()
	if err != nil {
		t.Fatal(err)
	}
	r.Doge.Candidate = &tpl
	if out, err := s.Sign(context.Background(), r); err == nil || out != nil || b.calls != 0 {
		t.Fatal("proposal signed without validator")
	}
}

func TestProposalSignerRevalidatesBeforeAndAfterSigning(t *testing.T) {
	for _, tc := range []struct {
		name   string
		failAt int
		keys   int
	}{{"valid", 0, 1}, {"before-key", 1, 0}, {"after-key", 2, 1}} {
		t.Run(tc.name, func(t *testing.T) {
			s, src, b, r, _ := fixture(t)
			tpl, err := src.doge.ToParentTemplate()
			if err != nil {
				t.Fatal(err)
			}
			r.Doge.Candidate = &tpl
			validator := &proposalTestSource{testSource: src, failAt: tc.failAt}
			s.source = validator
			// An independently changed CurTime or template body is not itself a
			// rejection in proposal mode; the real daemon validates the proposal.
			src.doge.CurTime++
			src.doge.CoinbaseValue++
			out, err := s.Sign(context.Background(), r)
			if tc.failAt == 0 {
				if err != nil || out == nil || validator.calls != 2 {
					t.Fatal(err)
				}
			} else if err == nil || out != nil {
				t.Fatal("unvalidated signature released")
			}
			if b.calls != tc.keys {
				t.Fatal("wrong key call count")
			}
		})
	}
}

func TestProposalBoundsAndBindingsBeforeKey(t *testing.T) {
	for _, name := range []string{"height", "tip", "time", "nonce", "payout", "zero-payout", "witness", "tx-count", "tx-bytes", "flags", "expiry-during-validation"} {
		t.Run(name, func(t *testing.T) {
			s, src, b, r, now := fixture(t)
			tpl, err := src.doge.ToParentTemplate()
			if err != nil {
				t.Fatal(err)
			}
			r.Doge.Candidate = &tpl
			validator := &proposalTestSource{testSource: src}
			s.source = validator
			switch name {
			case "height":
				tpl.Height++
			case "tip":
				tpl.Previous[0] ^= 1
			case "time":
				tpl.CurTime--
			case "nonce":
				r.Doge.JobNonce++
			case "payout":
				s.policy.DogePayoutScript = []byte{0x53}
			case "zero-payout":
				tpl.CoinbaseValue = 0
			case "witness":
				tpl.WitnessCommitment = []byte{1}
			case "tx-count":
				tpl.Transactions = make([]forgepool.TemplateTx, 10001)
			case "tx-bytes":
				tpl.Transactions = []forgepool.TemplateTx{{Data: make([]byte, forgepool.MaxDogeProposalBytes+1)}}
			case "flags":
				tpl.CoinbaseAux = map[string][]byte{"flags": make([]byte, 101)}
			case "expiry-during-validation":
				validator.hook = func() { *now += 60 }
			}
			if out, err := s.Sign(context.Background(), r); err == nil || out != nil || b.calls != 0 {
				t.Fatalf("invalid proposal reached key: %v", err)
			}
		})
	}
}
