package proxy

import (
	"encoding/hex"
	"strings"

	"github.com/HawyUkr/open-progpow-pool/forgepool"
)

// This is a pool policy only. Signed template expiry and participation remain
// mandatory. In particular, enabling retention does not authorize OGG-only work.
func forgeRetentionHash(value string) bool {
	raw, err := hex.DecodeString(strings.TrimPrefix(value, "0x"))
	if err != nil || len(raw) != 32 {
		return false
	}
	for _, b := range raw {
		if b != 0 {
			return true
		}
	}
	return false
}

func forgeExternalWorkCompatible(old, next *forgeJob) bool {
	if old != nil && next != nil && (old.ProtocolV3 || next.ProtocolV3) {
		return forgeMultiCompatible(old, next)
	}
	if old == nil || next == nil || old.OGG == nil || next.OGG == nil ||
		old.Digibyte == nil || next.Digibyte == nil ||
		old.Signed == nil || next.Signed == nil ||
		old.ExternalMask == 0 || old.ExternalMask != next.ExternalMask ||
		old.ExternalMask & ^forgepool.ExternalMask != 0 ||
		old.OGGRPC != next.OGGRPC ||
		!forgeRetentionHash(old.OGG.PreviousBlockHash) ||
		!forgeRetentionHash(next.OGG.PreviousBlockHash) {
		return false
	}
	for _, job := range []*forgeJob{old, next} {
		if job.Signed.Template.Version != forgepool.OGGTemplateVersionV2Base|job.ExternalMask ||
			job.Digibyte.DigibyteActive != (job.ExternalMask&forgepool.ExternalDGB != 0) ||
			(job.Digibyte.DOGE != nil) != (job.ExternalMask&forgepool.ExternalDOGE != 0) {
			return false
		}
	}
	if old.ExternalMask&forgepool.ExternalDGB != 0 &&
		(old.Digibyte.Template.Previous != next.Digibyte.Template.Previous ||
			old.Digibyte.Template.Bits != next.Digibyte.Template.Bits ||
			old.Digibyte.Template.Height != next.Digibyte.Template.Height) {
		return false
	}
	if old.ExternalMask&forgepool.ExternalDOGE != 0 &&
		(old.Digibyte.DOGE.Template.Previous != next.Digibyte.DOGE.Template.Previous ||
			old.Digibyte.DOGE.Bits != next.Digibyte.DOGE.Bits ||
			old.Digibyte.DOGE.Template.Height != next.Digibyte.DOGE.Template.Height) {
		return false
	}
	if len(old.AuxSymbols) != len(next.AuxSymbols) {
		return false
	}
	for symbol, id := range old.AuxSymbols {
		if next.AuxSymbols[symbol] != id {
			return false
		}
		a, b := old.Digibyte.AdditionalAux[id], next.Digibyte.AdditionalAux[id]
		if a == nil || b == nil || a.Template.Previous != b.Template.Previous || a.Template.Height != b.Template.Height || a.Bits != b.Bits {
			return false
		}
	}
	return true
}

func (rt *forgeRuntime) jobNeedsClean(old, next *forgeJob) bool {
	if rt.retainExternalWork {
		return !forgeExternalWorkCompatible(old, next)
	}
	return forgeJobNeedsClean(old, next)
}

// Jobs are immutable after publication. Snapshot both pointers under one lock
// so validation never compares work with a partially observed publication.
func (rt *forgeRuntime) shareJobs(id string) (*forgeJob, *forgeJob) {
	rt.mu.RLock()
	defer rt.mu.RUnlock()
	return rt.jobs[id], rt.jobs[rt.currentID]
}
