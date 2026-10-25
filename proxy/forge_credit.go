package proxy

import (
	"encoding/hex"
	"fmt"
	"github.com/HawyUkr/open-progpow-pool/storage"
)

// Build an immutable accounting snapshot only after submission attempts finish.
// External uncertainty is explicit, and never treated as accepted revenue.
func (item *forgeQueuedShare) creditSnapshot() (storage.ForgeShare, error) {
	s := item.receipt
	if item.intent == nil {
		return s, nil
	}
	for _, b := range item.intent.Blocks {
		if b.Chain == "OGG" {
			if b.State == "pending" {
				return s, fmt.Errorf("FORGE native submission reconciliation pending")
			}
			if b.State == "accepted" {
				raw, err := hex.DecodeString(b.Payload)
				if err != nil {
					return s, err
				}
				s.ProofHash = storage.ForgeProofHash(raw)
			} else {
				s.OGGError = b.Error
			}
			continue
		}
		if !b.Sent && b.State == "pending" {
			return s, fmt.Errorf("FORGE external submission not attempted")
		}
		outcome := storage.ForgeSubmission{Attempted: true, Accepted: b.State == "accepted", BlockHash: b.BlockHash, Error: b.Error}
		if b.State == "noncanonical" {
			outcome.Observation = "noncanonical"
		}
		if b.State == "pending" {
			outcome.Error = "submission unresolved; durable journal retained"
		}
		if b.Chain == "DGB" {
			s.Digibyte = outcome
		} else if b.Chain == "DOGE" {
			s.Dogecoin = outcome
		} else if b.Chain == "PEP" {
			s.Pepecoin = outcome
		} else if b.Chain == "DINGO" {
			s.Dingocoin = outcome
		} else if b.Chain == "BELLS" {
			s.Bellscoin = outcome
		} else if b.Chain == "SOQ" {
			s.Soqucoin = outcome
		} else if storage.ValidRegisteredSymbol(b.Chain) {
			if err := s.SetRegistered(b.Chain, b.ChainID, outcome); err != nil {
				return s, err
			}
		}
	}
	return s, s.Validate()
}

func sameNativeCredit(a, b storage.ForgeShare) bool {
	a.Digibyte = storage.ForgeSubmission{}
	a.Dogecoin = storage.ForgeSubmission{}
	b.Digibyte = storage.ForgeSubmission{}
	b.Dogecoin = storage.ForgeSubmission{}
	a.Pepecoin = storage.ForgeSubmission{}
	a.Dingocoin = storage.ForgeSubmission{}
	a.Bellscoin = storage.ForgeSubmission{}
	a.Soqucoin = storage.ForgeSubmission{}
	b.Pepecoin = storage.ForgeSubmission{}
	b.Dingocoin = storage.ForgeSubmission{}
	b.Bellscoin = storage.ForgeSubmission{}
	b.Soqucoin = storage.ForgeSubmission{}
	a.Registered = [5]storage.ForgeRegisteredSubmission{}
	b.Registered = [5]storage.ForgeRegisteredSubmission{}
	return a == b
}

// Last external observations published to Redis. Never used for share credit.
type forgeExternalObservation struct {
	Registered [5]storage.ForgeRegisteredSubmission `json:"registered,omitempty"`
	Soqucoin   storage.ForgeSubmission              `json:"soqucoin"`
	Pepecoin   storage.ForgeSubmission              `json:"pepecoin"`
	Dingocoin  storage.ForgeSubmission              `json:"dingocoin"`
	Bellscoin  storage.ForgeSubmission              `json:"bellscoin"`
	Digibyte   storage.ForgeSubmission              `json:"digibyte"`
	Dogecoin   storage.ForgeSubmission              `json:"dogecoin"`
}

func externalObservation(s storage.ForgeShare) forgeExternalObservation {
	return forgeExternalObservation{Registered: s.Registered, Digibyte: s.Digibyte, Dogecoin: s.Dogecoin, Pepecoin: s.Pepecoin, Dingocoin: s.Dingocoin, Bellscoin: s.Bellscoin, Soqucoin: s.Soqucoin}
}
