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
	if cfg.ExternalChainPolicy == forgepool.ExternalChainPolicyRegistry {
		if !cfg.MultiChain || cfg.RegistryBlock == 0 || cfg.OptionalSOQBlock != 0 || len(cfg.AdditionalAux) > 5 {
			return 0, errors.New("registry policy requires explicit activation, bounded endpoints and no legacy SOQ height")
		}
		return mask, nil
	}
	if cfg.RegistryBlock != 0 || cfg.RegistryOperationFile != "" || len(cfg.TreasuryScripts) != 0 {
		return 0, errors.New("registry configuration requires explicit registry-v5 policy")
	}
	if cfg.ExternalChainPolicy == forgepool.ExternalChainPolicyDGBDOGESQ {
		if !cfg.MultiChain || cfg.OptionalSOQBlock <= 1100100 || len(cfg.AdditionalAux) != 1 || cfg.AdditionalAux[0].Symbol != "SOQ" {
			return 0, errors.New("optional SOQ policy requires one SOQ endpoint and a future RGN activation height")
		}
	} else if cfg.OptionalSOQBlock != 0 {
		return 0, errors.New("optional SOQ height requires the explicit dgb-doge-soq policy")
	} else if cfg.ExternalChainPolicy == forgepool.ExternalChainPolicyDGBDOGE {
		if !cfg.MultiChain || len(cfg.AdditionalAux) != 0 {
			return 0, errors.New("dgb-doge policy requires V3 proofs and no additional auxiliary endpoints")
		}
	} else if cfg.MultiChain && len(cfg.AdditionalAux) != 3 {
		return 0, errors.New("default V3 policy requires all three additional auxiliary endpoints")
	}
	return mask, nil
}
