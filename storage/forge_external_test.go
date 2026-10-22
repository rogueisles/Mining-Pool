package storage

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestForgeExternalObservationUpdateCannotCreditOrCloseRound(t *testing.T) {
	for _, compact := range []bool{false, true} {
		t.Run(map[bool]string{false: "live-dedup", true: "compacted-dedup"}[compact], func(t *testing.T) {
			reset()
			s := forgeReceipt("aa")
			s.Digibyte = ForgeSubmission{Attempted: true, BlockHash: "0x" + strings.Repeat("33", 32), Error: "unresolved"}
			if _, err := r.WriteForgeShare(s); err != nil {
				t.Fatal(err)
			}
			// A later native win closes the original round; the late external update
			// must leave both that round and the new round intact.
			win := forgeReceipt("bb")
			win.ProofHash = "0x" + strings.Repeat("44", 32)
			if _, err := r.WriteForgeShare(win); err != nil {
				t.Fatal(err)
			}
			later := forgeReceipt("cc")
			if _, err := r.WriteForgeShare(later); err != nil {
				t.Fatal(err)
			}
			before, err := r.GetCandidates(s.Height)
			if err != nil {
				t.Fatal(err)
			}
			if compact {
				p, err := r.PlanForgeDedup(s.Height, s.ID[:2])
				if err != nil {
					t.Fatal(err)
				}
				if _, err = r.ApplyForgeDedup(*p); err != nil {
					t.Fatal(err)
				}
			}
			s.Digibyte.Accepted = true
			s.Digibyte.Error = ""
			for i := 0; i < 2; i++ {
				if err := r.WriteForgeExternalOutcomes(s); err != nil {
					t.Fatal(err)
				}
			}
			after, err := r.GetCandidates(s.Height)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("external observation changed native candidate", err)
			}
			shares, err := r.GetRoundShares(s.Height, ForgeRoundID(win.ProofHash))
			if err != nil || shares[s.Login] != 20 {
				t.Fatal("native round changed", shares, err)
			}
			if got := r.client.HGet(r.formatKey("shares", "roundCurrent"), s.Login).Val(); got != "10" {
				t.Fatal("current round changed", got)
			}
			if got := r.client.ZCard(r.formatKey("hashrate")).Val(); got != 3 {
				t.Fatal("duplicate credit", got)
			}
			blob, err := r.client.HGet(r.formatKey("forge", "submissions", "dgb", s.Height), s.ID).Result()
			var outcome ForgeSubmission
			if err != nil || json.Unmarshal([]byte(blob), &outcome) != nil || !outcome.Accepted {
				t.Fatal("outcome not updated", err)
			}
		})
	}
}
func TestForgeExternalObservationPreflightNoPartialWrite(t *testing.T) {
	for _, bad := range []string{"uncredited", "wrong-type", "corrupt-packed"} {
		t.Run(bad, func(t *testing.T) {
			reset()
			s := forgeReceipt("aa")
			if bad != "uncredited" {
				if _, err := r.WriteForgeShare(s); err != nil {
					t.Fatal(err)
				}
			}
			dgb := r.formatKey("forge", "submissions", "dgb", s.Height)
			doge := r.formatKey("forge", "submissions", "doge", s.Height)
			if bad == "wrong-type" {
				r.client.Set(doge, "wrong", 0)
			}
			if bad == "corrupt-packed" {
				r.client.Set(r.forgeDedupArchiveKey(s.Height, s.ID[:2]), "bad", 0)
			}
			s.Digibyte = ForgeSubmission{Attempted: true, Accepted: true, BlockHash: "0x" + strings.Repeat("33", 32)}
			s.Dogecoin = s.Digibyte
			if err := r.WriteForgeExternalOutcomes(s); err == nil {
				t.Fatal("invalid update accepted")
			}
			if r.client.Exists(dgb).Val() {
				t.Fatal("partial external write before failed preflight")
			}
			if n := r.client.ZCard(r.formatKey("blocks", "candidates")).Val(); n != 0 {
				t.Fatal("native candidate fabricated")
			}
		})
	}
}
