package proxy

import "testing"

func TestForgeChainHealthStartupRequired(t *testing.T) {
	var h forgeChainHealth

	if !h.required() {
		t.Fatal("fresh external chain is not mandatory")
	}
	if !h.isActive() {
		t.Fatal("fresh external chain does not report active/required")
	}

	s := h.snapshot()
	if !s.Active {
		t.Fatalf("unexpected initial state: %+v", s)
	}
	if s.Initialized {
		t.Fatalf("fresh health state unexpectedly initialized: %+v", s)
	}
}

func TestForgeChainHealthNeedsThreeFailuresToExclude(t *testing.T) {
	var h forgeChainHealth

	if changed := h.failure(); changed {
		t.Fatal("excluded after first failure")
	}
	if !h.required() {
		t.Fatal("chain optional after first failure")
	}

	if changed := h.failure(); changed {
		t.Fatal("excluded after second failure")
	}
	if !h.required() {
		t.Fatal("chain optional after second failure")
	}

	if changed := h.failure(); !changed {
		t.Fatal("third consecutive failure did not exclude chain")
	}
	if h.required() {
		t.Fatal("chain still mandatory after third failure")
	}
}

func TestForgeChainHealthNeedsTwoSuccessesToRestore(t *testing.T) {
	var h forgeChainHealth

	h.failure()
	h.failure()
	h.failure()

	if h.required() {
		t.Fatal("setup failed to exclude chain")
	}

	if changed := h.success(); changed {
		t.Fatal("restored after only one success")
	}
	if h.required() {
		t.Fatal("chain mandatory after only one recovery success")
	}

	if changed := h.success(); !changed {
		t.Fatal("second recovery success did not restore chain")
	}
	if !h.required() {
		t.Fatal("chain not mandatory after two successes")
	}
}

func TestForgeChainHealthSuccessResetsFailureStreak(t *testing.T) {
	var h forgeChainHealth

	h.failure()
	h.failure()

	h.success()

	h.failure()
	h.failure()

	if !h.required() {
		t.Fatal("non-consecutive failures excluded chain")
	}

	if changed := h.failure(); !changed {
		t.Fatal("third new consecutive failure did not exclude chain")
	}
}

func TestForgeChainHealthFailureResetsRecoveryStreak(t *testing.T) {
	var h forgeChainHealth

	h.failure()
	h.failure()
	h.failure()

	if h.required() {
		t.Fatal("setup failed to exclude chain")
	}

	h.success()

	if s := h.snapshot(); s.Successes != 1 {
		t.Fatalf("success streak=%d want 1", s.Successes)
	}

	h.failure()

	if s := h.snapshot(); s.Successes != 0 {
		t.Fatalf("failure did not reset recovery streak: %+v", s)
	}

	h.success()

	if h.required() {
		t.Fatal("restored after interrupted recovery streak")
	}

	h.success()

	if !h.required() {
		t.Fatal("did not restore after two new consecutive successes")
	}
}

func TestForgeIndependentDGBAndDOGEHealth(t *testing.T) {
	var dgb forgeChainHealth
	var doge forgeChainHealth

	if !dgb.required() || !doge.required() {
		t.Fatal("external chains are not required at startup")
	}

	doge.failure()
	doge.failure()
	doge.failure()

	if !dgb.required() {
		t.Fatal("DOGE failure affected Digibyte state")
	}
	if doge.required() {
		t.Fatal("DOGE did not exclude independently")
	}

	dgb.failure()
	dgb.failure()
	dgb.failure()

	if dgb.required() {
		t.Fatal("Digibyte did not exclude independently")
	}

	doge.success()

	if doge.required() {
		t.Fatal("DOGE restored after only one success")
	}

	doge.success()

	if !doge.required() {
		t.Fatal("DOGE did not recover independently")
	}
	if dgb.required() {
		t.Fatal("DOGE recovery incorrectly restored Digibyte")
	}
}
