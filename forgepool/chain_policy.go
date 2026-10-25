package forgepool

import "errors"

const ExternalChainPolicyRegistry = "registry-v5"
const ExternalChainPolicyDGBDOGE = "dgb-doge"
const ExternalChainPolicyDGBDOGESQ = "dgb-doge-soq"

// ExternalChainPolicyMask returns the permitted participation bits. An empty
// policy preserves the existing five-chain configuration.
func ExternalChainPolicyMask(policy string) (uint8, error) {
	switch policy {
	case "":
		return 31, nil
	case ExternalChainPolicyRegistry:
		return FlexibleCoreMask, nil
	case ExternalChainPolicyDGBDOGESQ:
		return ExternalDGB | ExternalDOGE | ExternalSOQ, nil
	case ExternalChainPolicyDGBDOGE:
		return ExternalDGB | ExternalDOGE, nil
	default:
		return 0, errors.New("unknown external chain policy")
	}
}
