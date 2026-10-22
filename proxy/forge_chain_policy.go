package proxy

import (
	"errors"
	"github.com/HawyUkr/open-progpow-pool/forgepool"
)

// Validate the configured endpoint set before opening the submission journal.
func validateForgeExternalPolicy(cfg ForgeConfig) (uint8, error) {
	mask, err := forgepool.ExternalChainPolicyMask(cfg.ExternalChainPolicy)
	if err != nil {
		return 0, err
	}
	if cfg.ExternalChainPolicy == forgepool.ExternalChainPolicyDGBDOGE {
		if !cfg.MultiChain || len(cfg.AdditionalAux) != 0 {
			return 0, errors.New("dgb-doge policy requires V3 proofs and no additional auxiliary endpoints")
		}
	} else if cfg.MultiChain && len(cfg.AdditionalAux) != 3 {
		return 0, errors.New("default V3 policy requires all three additional auxiliary endpoints")
	}
	return mask, nil
}
