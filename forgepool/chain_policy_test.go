package forgepool

import "testing"

func TestExternalChainPolicy(t *testing.T) {
	for policy, expected := range map[string]uint8{"": 31, ExternalChainPolicyDGBDOGE: 3} {
		mask, err := ExternalChainPolicyMask(policy)
		if err != nil || mask != expected {
			t.Fatal(policy, mask, err)
		}
	}
	if _, err := ExternalChainPolicyMask("unknown"); err == nil {
		t.Fatal("unknown policy silently accepted")
	}
}
