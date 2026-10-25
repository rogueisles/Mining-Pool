package proxy

import (
	"github.com/HawyUkr/open-progpow-pool/forgepool"
	"testing"
)

func TestOptionalSOQPolicyRequiresExplicitFutureConfiguration(t *testing.T) {
	c := ForgeConfig{ExternalChainPolicy: forgepool.ExternalChainPolicyDGBDOGESQ, MultiChain: true, OptionalSOQBlock: 1125600, AdditionalAux: []ForgeAuxEndpoint{{Symbol: "SOQ", ChainID: 21329}}}
	if mask, e := validateForgeExternalPolicy(c); e != nil || mask != 35 {
		t.Fatal(mask, e)
	}
	for _, height := range []uint64{0, 1100100} {
		bad := c
		bad.OptionalSOQBlock = height
		if _, e := validateForgeExternalPolicy(bad); e == nil {
			t.Fatal("invalid activation permitted")
		}
	}
	bad := c
	bad.ExternalChainPolicy = forgepool.ExternalChainPolicyDGBDOGE
	if _, e := validateForgeExternalPolicy(bad); e == nil {
		t.Fatal("legacy policy admits SOQ")
	}
	bad = c
	bad.AdditionalAux = nil
	if _, e := validateForgeExternalPolicy(bad); e == nil {
		t.Fatal("missing optional backend admitted")
	}
	if !forgepool.RevenueAnchorPresent(32) || !forgepool.RevenueAnchorPresent(35) || forgepool.RevenueAnchorPresent(0) || forgepool.RevenueAnchorPresent(4) {
		t.Fatal("wrong upgraded native participation policy")
	}
}
