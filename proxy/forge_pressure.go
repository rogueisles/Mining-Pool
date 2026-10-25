package proxy

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"time"
)

var errForgeBackpressure = errors.New("FORGE accounting capacity unavailable")

// Limits apply to ordinary shares. Valid network-target hits always retain
// routing and journaling priority, even beyond these limits.
type ForgeAccountingLimits struct {
	MaxPendingRecords int   `json:"maxPendingRecords"`
	MaxPendingBytes   int64 `json:"maxPendingBytes"`
	MaxPendingAgeSec  int64 `json:"maxPendingAgeSec"`
	RecoveryBatchSize int   `json:"recoveryBatchSize"`
}

func (l ForgeAccountingLimits) defaults() (ForgeAccountingLimits, error) {
	if l.MaxPendingRecords == 0 {
		l.MaxPendingRecords = 10000
	}
	if l.MaxPendingBytes == 0 {
		l.MaxPendingBytes = 64 * 1024 * 1024
	}
	if l.MaxPendingAgeSec == 0 {
		l.MaxPendingAgeSec = 300
	}
	if l.RecoveryBatchSize == 0 {
		l.RecoveryBatchSize = 8
	}
	if l.MaxPendingRecords < 1 || l.MaxPendingRecords > 1000000 || l.MaxPendingBytes < 1024 || l.MaxPendingBytes > 1<<40 || l.MaxPendingAgeSec < 1 || l.MaxPendingAgeSec > 86400 || l.RecoveryBatchSize < 1 || l.RecoveryBatchSize > 128 {
		return l, errors.New("invalid FORGE accounting limits")
	}
	return l, nil
}

func (q *forgeAccountingQueue) oldestAgeLocked(now time.Time) int64 {
	for _, item := range q.pending {
		if item.credited {
			continue
		}
		age := (now.UnixNano()/int64(time.Millisecond) - item.receipt.TimestampMS) / 1000
		if age < 0 {
			return 0
		}
		return age
	}
	return 0
}

func (q *forgeAccountingQueue) pausedLocked(now time.Time) bool {
	age := q.oldestAgeLocked(now)
	if len(q.pending) >= q.limits.MaxPendingRecords || q.pendingBytes >= q.limits.MaxPendingBytes || age >= q.limits.MaxPendingAgeSec || q.undurable > 0 {
		q.paused = true
	} else if len(q.pending) <= q.limits.MaxPendingRecords/2 && q.pendingBytes <= q.limits.MaxPendingBytes/2 && (len(q.pending) == 0 || age < (q.limits.MaxPendingAgeSec+1)/2) {
		q.paused = false
	}
	return q.paused
}

func (q *forgeAccountingQueue) workPaused() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.pausedLocked(time.Now())
}

func (q *forgeAccountingQueue) rejectOrdinaryIfPaused() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if !q.pausedLocked(time.Now()) {
		return false
	}
	q.rejectedOrdinary++
	return true
}

func (q *forgeAccountingQueue) dirtyLocked(item *forgeQueuedShare) {
	if item.durable {
		item.durable = false
		q.undurable++
	}
}

type ForgePendingOutcome struct {
	ID         string            `json:"id"`
	Height     int64             `json:"height"`
	AgeSeconds int64             `json:"ageSeconds"`
	Active     bool              `json:"active"`
	Credited   bool              `json:"credited"`
	Chains     map[string]string `json:"chains"`
}

// This snapshot contains no accounts, RPC endpoints, credentials, signatures
// or raw proof material. Outcome details are capped independently of backlog.
type ForgeAccountingStatus struct {
	Version                  int                   `json:"version"`
	UpdatedUnixMS            int64                 `json:"updatedUnixMS"`
	Health                   string                `json:"health"`
	WorkPaused               bool                  `json:"workPaused"`
	Limits                   ForgeAccountingLimits `json:"limits"`
	UncreditedRecords        int                   `json:"uncreditedRecords"`
	PendingRecords           int                   `json:"pendingRecords"`
	PendingBytes             int64                 `json:"pendingBytes"`
	UndurableRecords         int                   `json:"undurableRecords"`
	ObservedIntents          int                   `json:"observedIntents"`
	NoncanonicalChains       map[string]int        `json:"noncanonicalChains"`
	PendingIntents           int                   `json:"pendingIntents"`
	ActiveIntents            int                   `json:"activeIntents"`
	UnresolvedChains         map[string]int        `json:"unresolvedChains"`
	OldestAgeSeconds         int64                 `json:"oldestAgeSeconds"`
	HeadID                   string                `json:"headID,omitempty"`
	HeadState                string                `json:"headState,omitempty"`
	RejectedOrdinary         uint64                `json:"rejectedOrdinary"`
	PriorityOverflow         uint64                `json:"priorityOverflow"`
	PersistenceFailures      uint64                `json:"persistenceFailures"`
	LastPersistenceFailureMS int64                 `json:"lastPersistenceFailureMS"`
	LastAccountingSuccessMS  int64                 `json:"lastAccountingSuccessMS"`
	AccountingUnavailable    bool                  `json:"accountingUnavailable"`
	LastRecoveryCompletedMS  int64                 `json:"lastRecoveryCompletedMS"`
	Outcomes                 []ForgePendingOutcome `json:"outcomes"`
	OutcomesTruncated        bool                  `json:"outcomesTruncated"`
}

func (q *forgeAccountingQueue) status(now time.Time) ForgeAccountingStatus {
	q.mu.Lock()
	defer q.mu.Unlock()
	s := ForgeAccountingStatus{Version: 1, UpdatedUnixMS: now.UnixNano() / int64(time.Millisecond), Health: "healthy", WorkPaused: q.pausedLocked(now), Limits: q.limits, PendingRecords: len(q.pending), PendingBytes: q.pendingBytes, UndurableRecords: q.undurable, OldestAgeSeconds: q.oldestAgeLocked(now), RejectedOrdinary: q.rejectedOrdinary, PriorityOverflow: q.priorityOverflow, PersistenceFailures: q.persistenceFailures, LastPersistenceFailureMS: q.lastPersistenceFailureMS, LastAccountingSuccessMS: q.lastAccountingSuccessMS, AccountingUnavailable: q.accountingUnavailable, LastRecoveryCompletedMS: q.lastRecoveryCompletedMS, NoncanonicalChains: map[string]int{"DGB": 0, "DOGE": 0, "PEP": 0, "DINGO": 0, "BELLS": 0, "SOQ": 0}, UnresolvedChains: map[string]int{"DGB": 0, "DOGE": 0, "PEP": 0, "DINGO": 0, "BELLS": 0, "SOQ": 0, "OGG": 0}, Outcomes: []ForgePendingOutcome{}}
	if len(q.pending) > 0 {
		s.HeadID = q.pending[0].receipt.ID
		s.HeadState = "accounting"
		if q.pending[0].intent != nil {
			s.HeadState = "submission_unresolved"
			if q.pending[0].credited {
				s.HeadState = "external_reconciliation"
			}
		}
		if q.pending[0].active {
			s.HeadState = "submission_active"
		}
	}
	for _, item := range q.pending {
		if !item.credited {
			s.UncreditedRecords++
		}
		if item.intent == nil {
			continue
		}
		hasPending, hasObserved := false, false
		if item.active {
			s.ActiveIntents++
		}
		outcome := ForgePendingOutcome{ID: item.receipt.ID, Height: item.receipt.Height, AgeSeconds: (s.UpdatedUnixMS - item.receipt.TimestampMS) / 1000, Active: item.active, Credited: item.credited, Chains: make(map[string]string)}
		if outcome.AgeSeconds < 0 {
			outcome.AgeSeconds = 0
		}
		for _, b := range item.intent.Blocks {
			state := b.State
			if state == "noncanonical" {
				hasObserved = true
				s.NoncanonicalChains[b.Chain]++
			}
			if state == "pending" {
				hasPending = true
				s.UnresolvedChains[b.Chain]++
				switch {
				case !b.Sent:
					state = "not_sent"
				case now.Unix() >= b.Expires:
					state = "expired_unresolved"
				default:
					state = "acceptance_unknown"
				}
			}
			outcome.Chains[b.Chain] = state
		}
		if hasPending {
			s.PendingIntents++
		}
		if hasObserved {
			s.ObservedIntents++
		}
		if len(s.Outcomes) < 20 {
			s.Outcomes = append(s.Outcomes, outcome)
		} else {
			s.OutcomesTruncated = true
		}
	}
	if len(q.pending) > 0 || q.accountingUnavailable {
		s.Health = "degraded"
	}
	if s.WorkPaused {
		s.Health = "paused"
	}
	if q.undurable > 0 || len(q.pending) > q.limits.MaxPendingRecords || q.pendingBytes > q.limits.MaxPendingBytes {
		s.Health = "critical"
	}
	return s
}

// STATUS is outside the *.json journal namespace. A stale/missing snapshot is
// never proof of health: the reader must check updatedUnixMS independently.
func (q *forgeAccountingQueue) writeStatus(now time.Time) error {
	blob, err := json.MarshalIndent(q.status(now), "", "  ")
	if err != nil {
		return err
	}
	f, err := ioutil.TempFile(q.dir, ".status-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(append(blob, '\n')); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	// Monitoring is replaceable derived data, not a durable accounting record.
	return os.Rename(f.Name(), filepath.Join(q.dir, "STATUS"))
}

// ReadForgeAccountingStatus is read-only and safe while the pool owns LOCK.
// It cannot release an unresolved outcome or alter any accounting record.
func ReadForgeAccountingStatus(dir string, now time.Time, maxAge time.Duration) (*ForgeAccountingStatus, error) {
	if maxAge <= 0 || !filepath.IsAbs(dir) {
		return nil, errors.New("status max age must be positive")
	}
	f, err := os.Open(filepath.Join(dir, "STATUS"))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 65536 {
		return nil, errors.New("invalid FORGE status file")
	}
	var s ForgeAccountingStatus
	blob, err := ioutil.ReadAll(io.LimitReader(f, 65537))
	if err != nil || len(blob) > 65536 {
		return nil, errors.New("invalid FORGE status length")
	}
	if err := json.Unmarshal(blob, &s); err != nil {
		return nil, err
	}
	age := now.UnixNano()/int64(time.Millisecond) - s.UpdatedUnixMS
	if s.Version != 1 || s.UpdatedUnixMS <= 0 || age < 0 || age > int64(maxAge/time.Millisecond) {
		return nil, fmt.Errorf("stale or incompatible FORGE status")
	}
	if s.Health != "healthy" && s.Health != "degraded" && s.Health != "paused" && s.Health != "critical" {
		return nil, errors.New("invalid FORGE health state")
	}
	if s.PendingRecords < 0 || s.PendingBytes < 0 || s.UndurableRecords < 0 {
		return nil, errors.New("invalid FORGE accounting counters")
	}
	return &s, nil
}
