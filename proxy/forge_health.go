package proxy

import "sync"

const (
	forgeChainDisableFailures = 3
	forgeChainEnableSuccesses = 2
)

// forgeChainHealth applies hysteresis independently to one external chain.
//
// Security/economic policy:
//
// External chains are REQUIRED by default. A fresh process must not silently
// begin in OGG-only/reduced mode merely because health has not yet been
// measured.
//
// Three consecutive failures are required before the chain may be temporarily
// excluded. Once excluded, two consecutive successes are required before the
// chain becomes mandatory again.
type forgeChainHealth struct {
	mu sync.RWMutex

	// active means this external chain is mandatory for FORGE work.
	//
	// zero-value construction is handled by required() below: an untouched
	// health object is treated as required until an actual disable transition.
	active      bool
	initialized bool

	failures  int
	successes int
}

type forgeChainHealthSnapshot struct {
	Active      bool
	Initialized bool
	Failures    int
	Successes   int
}

// required reports whether the chain must participate in FORGE work.
//
// Before the first health observation the answer is deliberately true.
func (h *forgeChainHealth) required() bool {
	h.mu.RLock()
	defer h.mu.RUnlock()

	if !h.initialized {
		return true
	}
	return h.active
}

func (h *forgeChainHealth) success() bool {
	h.mu.Lock()
	defer h.mu.Unlock()

	// First successful observation confirms the default-required state.
	if !h.initialized {
		h.initialized = true
		h.active = true
		h.failures = 0
		h.successes = 0
		return false
	}

	h.failures = 0

	if h.active {
		h.successes = 0
		return false
	}

	h.successes++
	if h.successes >= forgeChainEnableSuccesses {
		h.active = true
		h.successes = 0
		return true
	}

	return false
}

func (h *forgeChainHealth) failure() bool {
	h.mu.Lock()
	defer h.mu.Unlock()

	// Failure observations initialize the chain as required. It is not allowed
	// to become optional until the full three-failure threshold is reached.
	if !h.initialized {
		h.initialized = true
		h.active = true
	}

	h.successes = 0

	if !h.active {
		h.failures = 0
		return false
	}

	h.failures++
	if h.failures >= forgeChainDisableFailures {
		h.active = false
		h.failures = 0
		return true
	}

	return false
}

func (h *forgeChainHealth) isActive() bool {
	return h.required()
}

func (h *forgeChainHealth) snapshot() forgeChainHealthSnapshot {
	h.mu.RLock()
	defer h.mu.RUnlock()

	active := h.active
	if !h.initialized {
		active = true
	}

	return forgeChainHealthSnapshot{
		Active:      active,
		Initialized: h.initialized,
		Failures:    h.failures,
		Successes:   h.successes,
	}
}
