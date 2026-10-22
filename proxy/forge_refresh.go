package proxy

import "time"

// One slot coalesces bursts without adding a goroutine per winning share.
// Calling after runIntent ensures all applicable chains were attempted first.
// Assembly failures with no sent payload do not trigger refreshes.
func (rt *forgeRuntime) requestJobRefresh() {
	if rt == nil {
		return
	}
	select {
	case rt.refreshRequests <- struct{}{}:
	default:
	}
}

// The existing periodic fallback and the block-triggered signal share one
// worker. A failed immediate refresh gets one short retry, then normal polling.
func (s *ProxyServer) runTemplateRefresh(interval time.Duration, stop <-chan struct{}) {
	timer := time.NewTimer(interval)
	defer timer.Stop()
	var requests <-chan struct{}
	if s.forge != nil {
		requests = s.forge.refreshRequests
	}
	for {
		select {
		case <-stop:
			return
		case <-timer.C:
			s.fetchBlockTemplate()
			timer.Reset(interval)
		case <-requests:
			delay := interval
			if !s.fetchForgeBlockTemplate() {
				delay = 500 * time.Millisecond
			}
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(delay)
		}
	}
}
