package forgepool

import "errors"

const ExternalChainPolicyDGBDOGE = "dgb-doge"

// ExternalChainPolicyMask returns the permitted participation bits. An empty
// policy preserves the existing five-chain configuration.
func ExternalChainPolicyMask(policy string) (uint8, error) {
	switch policy {
	case "":
		return 31, nil
	case ExternalChainPolicyDGBDOGE:
		return ExternalDGB | ExternalDOGE, nil
	default:
		return 0, errors.New("unknown external chain policy")
	}
}
