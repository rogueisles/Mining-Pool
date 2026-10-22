package rpc

import "testing"

func TestNativeTipTimeBounds(t *testing.T) {
	const dgb = "7497ea1b465eb39f1c8f507bc877078fe016d6fcb6dfad3a64c98dcc6e1e8496"
	const doge = "1a91e3dace36e2be3bf030a65679fe821aa1d6ef92e7c9902eb318182c355691"
	const now int64 = 1792611200
	for _, test := range []struct {
		name, genesis string
		timestamp     int64
		want          bool
	}{
		{"DGB observed accepted future tip", dgb, now + 283, true},
		{"DGB native future boundary", dgb, now + 7200, true},
		{"DGB beyond native future boundary", dgb, now + 7201, false},
		{"DGB stale boundary", dgb, now - 3600, true},
		{"DGB stale rejected", dgb, now - 3601, false},
		{"DOGE future boundary preserved", doge, now + 120, true},
		{"DOGE future rejection preserved", doge, now + 121, false},
		{"Unknown chain limit preserved", "unknown", now + 283, false},
		{"Missing timestamp rejected", dgb, 0, false},
		{"Negative timestamp rejected", dgb, -1, false},
		{"Extreme future rejected", dgb, 9223372036854775807, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := NativeTipTimeValid(test.genesis, now, test.timestamp); got != test.want {
				t.Fatalf("tip eligibility = %v, want %v", got, test.want)
			}
		})
	}
	if NativeTipTimeValid(dgb, 0, now) {
		t.Fatal("invalid local clock accepted")
	}
}
