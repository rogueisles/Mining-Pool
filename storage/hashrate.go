package storage

import (
	"math"
	"math/big"
	"strconv"
)

// Only dashboard samples are converted. Payout weights, round shares and
// journal/Redis dedup identities are never rewritten. FORGE rows have the
// exact receipt ID appended to the inherited legacy row layout.
func hashrateWork(parts []string, legacyFields int) *big.Int {
	if len(parts) != legacyFields && len(parts) != legacyFields+1 {
		return nil
	}
	n, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || n < 0 {
		return nil
	}
	work := big.NewInt(n)
	if len(parts) == legacyFields+1 {
		if !validHash(parts[legacyFields], false) {
			return nil
		}
		// Conventional Scrypt Stratum work units (~65536 hashes at diff 1).
		work.Lsh(work, 16)
	}
	return work
}

// Sum in arbitrary precision and divide before bounding the legacy int64 API.
// Overflow is explicit rather than wrapping or silently reporting zero.
func workRate(work *big.Int, seconds int64) (int64, bool) {
	if work == nil {
		return 0, false
	}
	if seconds <= 0 {
		return 0, true
	}
	rate := new(big.Int).Quo(work, big.NewInt(seconds))
	if !rate.IsInt64() {
		return math.MaxInt64, true
	}
	return rate.Int64(), false
}

func addHashrates(a, b int64) (int64, bool) {
	if a > math.MaxInt64-b {
		return math.MaxInt64, true
	}
	return a + b, false
}
