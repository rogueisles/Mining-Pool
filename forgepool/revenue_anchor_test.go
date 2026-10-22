package forgepool

import "testing"

func TestRevenueAnchorAllMasks(t *testing.T) {
	for i := 0; i < 256; i++ {
		want := i <= 31 && (i&3) != 0
		if RevenueAnchorPresent(uint8(i)) != want {
			t.Fatalf("mask %02x: anchor acceptance differs", i)
		}
	}
}
