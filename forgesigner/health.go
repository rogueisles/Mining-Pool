package forgesigner

import "sync"

const (
	signerDisableFailures = 3
	signerEnableSuccesses = 2
)

// signerChainHealth is signer-owned external-chain health state.
//
// External chains are required by default. Three consecutive independently
// observed failures are required to permit exclusion. Once excluded, two
// consecutive successful observations restore mandatory participation.
type signerChainHealth struct {
	mu sync.RWMutex

	active      bool
	initialized bool

	failures  int
	successes int
}

func (h *signerChainHealth) required() bool {
	h.mu.RLock()
	defer h.mu.RUnlock()

	if !h.initialized {
		return true
	}
	return h.active
}

func (h *signerChainHealth) observe(ok bool) bool {
	h.mu.Lock()
	defer h.mu.Unlock()

	oldRequired := true
	if h.initialized {
		oldRequired = h.active
	}

	if !h.initialized {
		h.initialized = true
		h.active = true
	}

	if ok {
		h.failures = 0

		if h.active {
			h.successes = 0
		} else {
			h.successes++
			if h.successes >= signerEnableSuccesses {
				h.active = true
				h.successes = 0
			}
		}
	} else {
		h.successes = 0

		if h.active {
			h.failures++
			if h.failures >= signerDisableFailures {
				h.active = false
				h.failures = 0
			}
		} else {
			h.failures = 0
		}
	}

	return oldRequired != h.active
}
