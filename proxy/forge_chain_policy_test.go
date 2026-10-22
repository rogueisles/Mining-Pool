package proxy

import (
	"encoding/json"
	"github.com/HawyUkr/open-progpow-pool/forgepool"
	"testing"
)

func TestForgeTwoChainPolicy(t *testing.T) {
	var cfg ForgeConfig
	if err := json.Unmarshal([]byte(`{"externalChainPolicy":"dgb-doge","multiChain":true}`), &cfg); err != nil {
		t.Fatal(err)
	}
	mask, err := validateForgeExternalPolicy(cfg)
	if err != nil || mask != 3 {
		t.Fatal("two-chain policy rejected", mask, err)
	}
	for _, bad := range []ForgeConfig{
		{ExternalChainPolicy: forgepool.ExternalChainPolicyDGBDOGE},
		{ExternalChainPolicy: "unknown", MultiChain: true},
		{ExternalChainPolicy: forgepool.ExternalChainPolicyDGBDOGE, MultiChain: true, AdditionalAux: []ForgeAuxEndpoint{{Symbol: "PEP"}}},
		{MultiChain: true},
	} {
		if _, err := validateForgeExternalPolicy(bad); err == nil {
			t.Fatal("incompatible endpoint/proof policy accepted")
		}
	}
	mask, err = validateForgeExternalPolicy(ForgeConfig{MultiChain: true, AdditionalAux: make([]ForgeAuxEndpoint, 3)})
	if err != nil || mask != 31 {
		t.Fatal("default five-chain policy changed", mask, err)
	}
}

func TestForgeTwoChainPolicyRejectsAdditionalJobBeforeSigning(t *testing.T) {
	rt := &forgeRuntime{multi: true, allowedExternalMask: 3}
	if err := rt.signMultiJob(&forgeJob{ExternalMask: 7}); err == nil {
		t.Fatal("excluded auxiliary chain reached signing")
	}
}
