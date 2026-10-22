package proxy

import (
	"testing"
	"time"

	"github.com/HawyUkr/open-progpow-pool/forgepool"
	"github.com/HawyUkr/open-progpow-pool/rpc"
)

func TestForgeRefreshDecision(t *testing.T) {
	full := forgepool.ExternalMask
	dgb := forgepool.ExternalDGB
	doge := forgepool.ExternalDOGE

	tests := []struct {
		name      string
		required  uint8
		available uint8
		publish   uint8
		probe     uint8
		pause     bool
	}{
		{
			name:      "healthy-full",
			required:  full,
			available: full,
			publish:   full,
		},
		{
			name:      "doge-failure-before-threshold",
			required:  full,
			available: dgb,
			probe:     dgb,
		},
		{
			name:      "dgb-failure-before-threshold",
			required:  full,
			available: doge,
			probe:     doge,
		},
		{
			name:      "both-failing-before-threshold",
			required:  full,
			available: 0,
		},
		{
			name:      "dgb-only-degraded",
			required:  dgb,
			available: dgb,
			publish:   dgb,
		},
		{
			name:      "dgb-only-first-doge-recovery",
			required:  dgb,
			available: full,
			publish:   dgb,
		},
		{
			name:      "doge-only-degraded",
			required:  doge,
			available: doge,
			publish:   doge,
		},
		{
			name:      "both-excluded",
			required:  0,
			available: 0,
			pause:     true,
		},
		{
			name:      "both-excluded-dgb-first-recovery",
			required:  0,
			available: dgb,
			probe:     dgb,
			pause:     true,
		},
		{
			name:      "both-excluded-doge-first-recovery",
			required:  0,
			available: doge,
			probe:     doge,
			pause:     true,
		},
		{
			name:      "both-excluded-both-first-recovery",
			required:  0,
			available: full,
			probe:     full,
			pause:     true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			publish, probe, pause, err := forgeRefreshDecision(
				tc.required,
				tc.available,
			)
			if err != nil {
				t.Fatal(err)
			}
			if publish != tc.publish ||
				probe != tc.probe ||
				pause != tc.pause {
				t.Fatalf(
					"got publish=%#x probe=%#x pause=%v; want %#x/%#x/%v",
					publish,
					probe,
					pause,
					tc.publish,
					tc.probe,
					tc.pause,
				)
			}
		})
	}
}

func TestForgeRefreshDecisionNeverPublishesZeroMask(t *testing.T) {
	publish, _, pause, err := forgeRefreshDecision(0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if publish != 0 || !pause {
		t.Fatalf(
			"both-down decision publish=%#x pause=%v",
			publish,
			pause,
		)
	}
}

func TestAllUnavailableSignerProbeIsNotOGGOnly(t *testing.T) {
	rt := &forgeRuntime{
		templateValidity: 60 * time.Second,
	}

	ogg := &rpc.OGGAuxBlock{
		WorkHash:            "0x1111111111111111111111111111111111111111111111111111111111111111",
		Timestamp:           "0x6553f100",
		TemplateMaxValidity: "0x12c",
	}

	tmpl, workHash, err := rt.allUnavailableSignerProbe(ogg)
	if err != nil {
		t.Fatal(err)
	}

	mask, err := forgepool.TemplateExternalMask(tmpl)
	if err != nil {
		t.Fatal(err)
	}

	if mask != forgepool.ExternalDGB {
		t.Fatalf("health probe mask=%#x want DGB bit", mask)
	}

	if tmpl.Version == forgepool.OGGTemplateVersionV2Base {
		t.Fatal("health probe accidentally created forbidden OGG-only version")
	}

	if workHash == ([32]byte{}) {
		t.Fatal("health probe omitted exact OGG WorkHash")
	}
}
