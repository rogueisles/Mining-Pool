package proxy

import (
	"github.com/HawyUkr/open-progpow-pool/forgepool"
	"testing"
)

func TestSOQCannotBeAddedToCurrentRGNPoolPolicy(t *testing.T) {
	c := ForgeConfig{ExternalChainPolicy: forgepool.ExternalChainPolicyDGBDOGE, MultiChain: true}
	if mask, err := validateForgeExternalPolicy(c); err != nil || mask != 3 {
		t.Fatal("baseline policy failed", err)
	}
	c.AdditionalAux = []ForgeAuxEndpoint{{Symbol: "SOQ", ChainID: 21329}}
	if _, err := validateForgeExternalPolicy(c); err == nil {
		t.Fatal("SOQ unexpectedly admitted by unchanged mainnet pool policy")
	} else {
		t.Log("Pool rejects SOQ before opening accounting or contacting signers:", err)
	}
}
