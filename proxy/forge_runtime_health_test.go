package proxy

import (
	"testing"

	"github.com/HawyUkr/open-progpow-pool/forgepool"
)

func TestForgeRuntimeHealthMaskStartsFull(t *testing.T) {
	var rt forgeRuntime

	want := forgepool.ExternalDGB | forgepool.ExternalDOGE
	if got := rt.requiredExternalMask(); got != want {
		t.Fatalf("startup mask=%#x want %#x", got, want)
	}
}

func TestForgeRuntimeHealthMaskDOGEExcluded(t *testing.T) {
	var rt forgeRuntime

	rt.dogeHealth.failure()
	rt.dogeHealth.failure()

	if got := rt.requiredExternalMask(); got != forgepool.ExternalMask {
		t.Fatalf("DOGE excluded before third failure: %#x", got)
	}

	rt.dogeHealth.failure()

	if got := rt.requiredExternalMask(); got != forgepool.ExternalDGB {
		t.Fatalf("DOGE exclusion mask=%#x want DGB-only %#x", got, forgepool.ExternalDGB)
	}
}

func TestForgeRuntimeHealthMaskDGBExcluded(t *testing.T) {
	var rt forgeRuntime

	rt.dgbHealth.failure()
	rt.dgbHealth.failure()
	rt.dgbHealth.failure()

	if got := rt.requiredExternalMask(); got != forgepool.ExternalDOGE {
		t.Fatalf("DGB exclusion mask=%#x want DOGE-only %#x", got, forgepool.ExternalDOGE)
	}
}

func TestForgeRuntimeHealthMaskBothExcludedIsZero(t *testing.T) {
	var rt forgeRuntime

	for i := 0; i < forgeChainDisableFailures; i++ {
		rt.dgbHealth.failure()
		rt.dogeHealth.failure()
	}

	if got := rt.requiredExternalMask(); got != 0 {
		t.Fatalf("both-down mask=%#x want zero/pause", got)
	}
}

func TestForgeRuntimeHealthMaskRestoresAfterTwoSuccesses(t *testing.T) {
	var rt forgeRuntime

	for i := 0; i < forgeChainDisableFailures; i++ {
		rt.dogeHealth.failure()
	}

	if got := rt.requiredExternalMask(); got != forgepool.ExternalDGB {
		t.Fatalf("setup mask=%#x want DGB-only", got)
	}

	rt.dogeHealth.success()

	if got := rt.requiredExternalMask(); got != forgepool.ExternalDGB {
		t.Fatalf("DOGE restored after one success: mask=%#x", got)
	}

	rt.dogeHealth.success()

	if got := rt.requiredExternalMask(); got != forgepool.ExternalMask {
		t.Fatalf("DOGE not restored after two successes: mask=%#x", got)
	}
}
