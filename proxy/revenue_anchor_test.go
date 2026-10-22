package proxy

import (
	"strings"
	"testing"
	"time"
)

func TestRevenueAnchorBonusOnlyJobCannotReachSigner(t *testing.T) {
	s, _, job, _, _, _ := testForgeServer(t)
	multiTemplateTestSigner(t, s.forge)
	job.ProtocolV3 = true
	for mask := uint8(0); mask <= 31; mask++ {
		if mask&3 != 0 {
			continue
		}
		job.ExternalMask = mask
		if err := s.forge.signJob(job); err == nil || !strings.Contains(err.Error(), "invalid multi-chain signing policy") {
			t.Fatalf("bonus-only mask %02x was not stopped before signing: %v", mask, err)
		}
	}
}

func TestRevenueAnchorRetainedJobsCannotBeBonusOnly(t *testing.T) {
	s, _, job, _, _, _ := testForgeServer(t)
	multiTemplateTestSigner(t, s.forge)
	job.ProtocolV3 = true
	job.ExternalMask = 3
	if err := s.forge.signJob(job); err != nil {
		t.Fatal(err)
	}
	for _, mask := range []uint8{4, 8, 12, 16, 20, 24, 28} {
		job.ExternalMask = mask
		job.Signed.Multi.Mask = mask
		if err := job.Signed.validate(job, s.forge.authorized, 1, time.Now()); err == nil {
			t.Fatalf("bonus-only retained authorization accepted: %02x", mask)
		}
	}
}
