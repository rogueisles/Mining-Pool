package proxy

import (
	"testing"

	"github.com/HawyUkr/open-progpow-pool/forgepool"
	"github.com/HawyUkr/open-progpow-pool/rpc"
)

func TestForgeAvailableExternalMask(t *testing.T) {
	tests := []struct {
		dgb, doge bool
		want      uint8
	}{
		{true, true, forgepool.ExternalMask},
		{true, false, forgepool.ExternalDGB},
		{false, true, forgepool.ExternalDOGE},
		{false, false, 0},
	}

	for _, tc := range tests {
		if got := forgeAvailableExternalMask(tc.dgb, tc.doge); got != tc.want {
			t.Fatalf(
				"dgb=%v doge=%v mask=%#x want %#x",
				tc.dgb,
				tc.doge,
				got,
				tc.want,
			)
		}
	}
}

func TestForgeClearExternalJobsPausesWithoutOGGOnlyFallback(t *testing.T) {
	rt := &forgeRuntime{
		jobs: map[string]*forgeJob{
			"one": {ID: "one"},
		},
		order:      []string{"one"},
		currentID:  "one",
		duplicates: map[string]struct{}{"one:test": {}},
	}

	if !rt.clearExternalJobs() {
		t.Fatal("existing FORGE work was not reported as cleared")
	}

	if rt.currentID != "" {
		t.Fatalf("current job remains after pause: %q", rt.currentID)
	}
	if len(rt.jobs) != 0 || len(rt.order) != 0 || len(rt.duplicates) != 0 {
		t.Fatal("FORGE pause retained stale work")
	}

	if rt.requiredExternalMask() != forgepool.ExternalMask {
		t.Fatal("clearing jobs unexpectedly changed chain health")
	}
}

func TestForgeJobNeedsCleanOnParticipationChange(t *testing.T) {
	old := &forgeJob{
		ExternalMask: forgepool.ExternalMask,
		OGG: &rpc.OGGAuxBlock{
			PreviousBlockHash: "0x11",
		},
		Digibyte: &forgepool.DigibyteJob{},
	}

	next := &forgeJob{
		ExternalMask: forgepool.ExternalDGB,
		OGG: &rpc.OGGAuxBlock{
			PreviousBlockHash: "0x11",
		},
		Digibyte: &forgepool.DigibyteJob{},
	}

	if !forgeJobNeedsClean(old, next) {
		t.Fatal("0x23 -> 0x21 participation change was not clean")
	}

	next.ExternalMask = forgepool.ExternalDOGE

	if !forgeJobNeedsClean(old, next) {
		t.Fatal("0x23 -> 0x22 participation change was not clean")
	}
}

func TestForgeJobNeedsCleanOnOGGParentChange(t *testing.T) {
	old := &forgeJob{
		ExternalMask: forgepool.ExternalDGB,
		OGG: &rpc.OGGAuxBlock{
			PreviousBlockHash: "0x11",
		},
		Digibyte: &forgepool.DigibyteJob{
			DigibyteActive: true,
		},
	}

	next := &forgeJob{
		ExternalMask: forgepool.ExternalDGB,
		OGG: &rpc.OGGAuxBlock{
			PreviousBlockHash: "0x22",
		},
		Digibyte: &forgepool.DigibyteJob{
			DigibyteActive: true,
		},
	}

	if !forgeJobNeedsClean(old, next) {
		t.Fatal("OGG parent change was not clean")
	}
}

func TestForgeJobNeedsCleanAllowsSameDGBOnlyParents(t *testing.T) {
	var prev forgepool.Hash
	prev[0] = 0x42

	old := &forgeJob{
		ExternalMask: forgepool.ExternalDGB,
		OGG: &rpc.OGGAuxBlock{
			PreviousBlockHash: "0x11",
		},
		Digibyte: &forgepool.DigibyteJob{
			Template: forgepool.ParentTemplate{
				Previous: prev,
			},
			DigibyteActive: true,
		},
	}

	next := &forgeJob{
		ExternalMask: forgepool.ExternalDGB,
		OGG: &rpc.OGGAuxBlock{
			PreviousBlockHash: "0x11",
		},
		Digibyte: &forgepool.DigibyteJob{
			Template: forgepool.ParentTemplate{
				Previous: prev,
			},
			DigibyteActive: true,
		},
	}

	if forgeJobNeedsClean(old, next) {
		t.Fatal("unchanged DGB-only parents incorrectly forced clean job")
	}
}
