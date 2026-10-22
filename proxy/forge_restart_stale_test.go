package proxy

import (
	"testing"

	"github.com/HawyUkr/open-progpow-pool/forgepool"
)

func TestForgePreThresholdOutageNeverServesExpiredOldJob(t *testing.T) {
	s, rt, _, doge := newForgeTransitionHarness(t)

	// Establish valid full 0x23 work.
	s.fetchForgeBlockTemplate()

	full := requireForgeCurrentMask(
		t,
		rt,
		forgepool.ExternalMask,
	)

	if full.Signed == nil {
		t.Fatal("healthy full job was not signed")
	}

	fullID := full.ID

	// Force the already-published job to be expired.
	//
	// During DOGE failure #1/#2 the refresh policy is allowed to retain only
	// previously issued FRESH work. An expired job must immediately disappear
	// from currentJob() even though hysteresis has not yet excluded DOGE.
	full.Signed.Template.Expires = 1

	if job := rt.currentJob(); job != nil {
		t.Fatal("expired full job remained mineable before outage")
	}

	doge.setHealthy(false)

	// Failure #1: do not prematurely issue 0x21, but also never return stale 0x23.
	s.fetchForgeBlockTemplate()

	if !rt.dogeHealth.required() {
		t.Fatal("DOGE excluded after only one failure")
	}
	if job := rt.currentJob(); job != nil {
		t.Fatalf(
			"failure #1 exposed stale/replacement work %s before threshold",
			job.ID,
		)
	}
	if rt.currentID != fullID {
		t.Fatalf(
			"failure #1 unexpectedly replaced retained job id %q with %q",
			fullID,
			rt.currentID,
		)
	}

	// Failure #2: still no new work and still no stale work exposed.
	s.fetchForgeBlockTemplate()

	if !rt.dogeHealth.required() {
		t.Fatal("DOGE excluded after only two failures")
	}
	if job := rt.currentJob(); job != nil {
		t.Fatalf(
			"failure #2 exposed stale/replacement work %s before threshold",
			job.ID,
		)
	}

	// Failure #3: reduced mode is now authorized and a fresh 0x21 may replace it.
	s.fetchForgeBlockTemplate()

	reduced := requireForgeCurrentMask(
		t,
		rt,
		forgepool.ExternalDGB,
	)

	if reduced.ID == fullID {
		t.Fatal("third failure did not replace stale full job")
	}
	if !reduced.Clean {
		t.Fatal("stale 0x23 -> fresh 0x21 transition was not clean")
	}
	if reduced.Signed == nil {
		t.Fatal("fresh 0x21 job is unsigned")
	}
}

func TestForgePoolRestartStartsFailClosed(t *testing.T) {
	// Simulate a runtime that had already degraded to DGB-only.
	var degraded forgeRuntime

	for i := 0; i < forgeChainDisableFailures; i++ {
		degraded.dogeHealth.failure()
	}

	if got := degraded.requiredExternalMask(); got != forgepool.ExternalDGB {
		t.Fatalf(
			"setup degraded mask=%#x want DGB-only %#x",
			got,
			forgepool.ExternalDGB,
		)
	}

	// A process restart creates fresh in-memory health state. Reduced
	// authorization must NOT survive the restart.
	var restarted forgeRuntime

	if got := restarted.requiredExternalMask(); got != forgepool.ExternalMask {
		t.Fatalf(
			"restart inherited degraded mask=%#x want fail-closed full %#x",
			got,
			forgepool.ExternalMask,
		)
	}

	// Same rule if the previous process had both chains excluded / paused.
	var paused forgeRuntime

	for i := 0; i < forgeChainDisableFailures; i++ {
		paused.dgbHealth.failure()
		paused.dogeHealth.failure()
	}

	if got := paused.requiredExternalMask(); got != 0 {
		t.Fatalf("setup paused mask=%#x want 0", got)
	}

	var restartedFromPause forgeRuntime

	if got := restartedFromPause.requiredExternalMask(); got != forgepool.ExternalMask {
		t.Fatalf(
			"restart inherited paused/degraded state %#x want full %#x",
			got,
			forgepool.ExternalMask,
		)
	}
}
