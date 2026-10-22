package forgepool

import (
	"math/big"
	"testing"
)

func TestScryptStratumDifficultyUnits(t *testing.T) {
	// Independent constants matching cpuminer's diff / 65536.0 convention.
	for _, tc := range []struct {
		diff int64
		hex  string
	}{
		{1, "0000ffff00000000000000000000000000000000000000000000000000000000"},
		{65536, "00000000ffff0000000000000000000000000000000000000000000000000000"},
	} {
		got, err := StratumTargetForDifficulty(big.NewInt(tc.diff))
		want, _ := new(big.Int).SetString(tc.hex, 16)
		if err != nil || got.Cmp(want) != 0 {
			t.Fatalf("difficulty %d target %x: %v", tc.diff, got, err)
		}
	}
	for _, diff := range []*big.Int{nil, big.NewInt(0), big.NewInt(-1)} {
		if _, err := StratumTargetForDifficulty(diff); err == nil {
			t.Fatal("invalid difficulty accepted")
		}
	}
	network, err := TargetForDifficulty(big.NewInt(1))
	if err != nil {
		t.Fatal(err)
	}
	compact, err := CompactTarget(0x1d00ffff)
	if err != nil || network.Cmp(compact) != 0 {
		t.Fatal("network target changed")
	}
}
