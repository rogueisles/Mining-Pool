package proxy

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/HawyUkr/open-progpow-pool/storage"
)

type forgeShareWriter interface {
	WriteForgeShare(storage.ForgeShare) (bool, error)
}

type forgeQueuedShare struct {
	path     string
	receipt  storage.ForgeShare
	durable  bool
	intent   *forgeSubmissionIntent
	credit   *storage.ForgeShare
	credited bool
	external *forgeExternalObservation
	active   bool // Owned by a foreground submitter or one recovery pass.
	bytes    int64
}

// The queue preserves accounting order across Redis outages and restarts.
// Intents precede network submissions; share credit follows resolved native outcomes.
// External uncertainty stays durable independently of the ordered credit stream.
// One directory belongs to one
// pool instance and must live on persistent storage, not a temporary filesystem.
type forgeAccountingQueue struct {
	mu                                                                         sync.Mutex
	flushMu                                                                    sync.Mutex // Never hold mu across a Redis call.
	dir                                                                        string
	lock                                                                       *os.File
	writer                                                                     forgeShareWriter
	sequence                                                                   uint64
	pending                                                                    []*forgeQueuedShare
	byID                                                                       map[string]*forgeQueuedShare
	limits                                                                     ForgeAccountingLimits
	pendingBytes                                                               int64
	undurable                                                                  int
	paused                                                                     bool
	rejectedOrdinary, priorityOverflow, persistenceFailures                    uint64
	lastPersistenceFailureMS, lastAccountingSuccessMS, lastRecoveryCompletedMS int64
	accountingUnavailable                                                      bool
	recoveryAfter                                                              string
}

func newForgeAccountingQueue(dir string, writer forgeShareWriter, options ...ForgeAccountingLimits) (*forgeAccountingQueue, error) {
	var limits ForgeAccountingLimits
	if len(options) > 1 {
		return nil, fmt.Errorf("multiple FORGE accounting limits")
	}
	if len(options) == 1 {
		limits = options[0]
	}
	limits, err := limits.defaults()
	if err != nil {
		return nil, err
	}
	if dir == "" || !filepath.IsAbs(dir) {
		return nil, fmt.Errorf("FORGE accountingJournalDir must be an absolute persistent path")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(filepath.Join(dir, "LOCK"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		return nil, fmt.Errorf("FORGE accounting journal in use: %w", err)
	}
	q := &forgeAccountingQueue{dir: dir, lock: lock, writer: writer, limits: limits, byID: make(map[string]*forgeQueuedShare)}
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		q.close()
		return nil, err
	}
	sort.Strings(files)
	for _, path := range files {
		seq, err := strconv.ParseUint(strings.SplitN(filepath.Base(path), "-", 2)[0], 10, 64)
		if err != nil {
			q.close()
			return nil, fmt.Errorf("invalid FORGE journal filename %s", path)
		}
		blob, err := ioutil.ReadFile(path)
		if err != nil {
			q.close()
			return nil, err
		}
		var record forgeJournalRecord
		if err := json.Unmarshal(blob, &record); err != nil {
			q.close()
			return nil, fmt.Errorf("corrupt FORGE journal %s: %w", path, err)
		}
		var receipt storage.ForgeShare
		if record.Version == 0 {
			if record.Intent != nil || record.Receipt.ID != "" {
				q.close()
				return nil, fmt.Errorf("missing journal version")
			}
			if err := json.Unmarshal(blob, &receipt); err != nil {
				q.close()
				return nil, err
			}
		} else {
			if record.Version != 1 && record.Version != 2 && record.Version != 3 {
				q.close()
				return nil, fmt.Errorf("unsupported FORGE journal version")
			}
			receipt = record.Receipt
			if record.Intent != nil {
				if err := record.Intent.validate(receipt); err != nil {
					q.close()
					return nil, err
				}
			}
		}
		if err := receipt.Validate(); err != nil {
			q.close()
			return nil, fmt.Errorf("invalid FORGE journal %s: %w", path, err)
		}
		if filepath.Base(path) != fmt.Sprintf("%020d-%s.json", seq, receipt.ID) {
			q.close()
			return nil, fmt.Errorf("FORGE journal identity mismatch %s", path)
		}
		if q.byID[receipt.ID] != nil {
			q.close()
			return nil, fmt.Errorf("duplicate FORGE journal identity")
		}
		q.sequence = seq
		item := &forgeQueuedShare{path: path, receipt: receipt, durable: true, intent: record.Intent, bytes: int64(len(blob)), credit: record.Credit, credited: record.Credited, external: record.External}
		if (record.Version < 2 && (item.credit != nil || item.credited)) || (item.credited && item.credit == nil) {
			q.close()
			return nil, fmt.Errorf("invalid credit journal version/state")
		}
		if item.credit != nil {
			expected, err := item.creditSnapshot()
			if err != nil || item.credit.Validate() != nil || !sameNativeCredit(*item.credit, expected) {
				q.close()
				return nil, fmt.Errorf("invalid frozen credit")
			}
		}
		if item.external != nil {
			if record.Version < 3 || !item.credited {
				q.close()
				return nil, fmt.Errorf("invalid observation journal state")
			}
			checked := *item.credit
			checked.Digibyte, checked.Dogecoin = item.external.Digibyte, item.external.Dogecoin
			if checked.Validate() != nil {
				q.close()
				return nil, fmt.Errorf("invalid recorded observation")
			}
		}
		q.pending = append(q.pending, item)
		q.byID[receipt.ID] = item
		q.pendingBytes += item.bytes
	}
	return q, nil
}

func (q *forgeAccountingQueue) close() error { return q.lock.Close() }

func (q *forgeAccountingQueue) syncDir() error {
	f, err := os.Open(q.dir)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

func (q *forgeAccountingQueue) persist(item *forgeQueuedShare) (result error) {
	q.dirtyLocked(item)
	defer func() {
		if result != nil {
			q.persistenceFailures++
			q.lastPersistenceFailureMS = time.Now().UnixNano() / int64(time.Millisecond)
		}
	}()
	blob, err := json.Marshal(forgeJournalRecord{Version: 3, Receipt: item.receipt, Intent: item.intent, Credit: item.credit, Credited: item.credited, External: item.external})
	if err != nil {
		return err
	}
	q.pendingBytes += int64(len(blob)) - item.bytes
	item.bytes = int64(len(blob))
	f, err := ioutil.TempFile(q.dir, ".receipt-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(blob); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), item.path); err != nil {
		return err
	}
	if err := q.syncDir(); err != nil {
		return err
	}
	item.durable = true
	q.undurable--
	return nil
}

func (q *forgeAccountingQueue) enqueue(receipt storage.ForgeShare) (bool, error) {
	if err := receipt.Validate(); err != nil {
		return false, err
	}
	q.mu.Lock()
	if q.byID[receipt.ID] != nil {
		q.mu.Unlock()
		return true, nil
	}
	blob, err := json.Marshal(forgeJournalRecord{Version: 2, Receipt: receipt, Credit: &receipt, Credited: true})
	if err != nil {
		q.mu.Unlock()
		return false, err
	}
	// A block-bearing receipt can come from a compatibility router. It receives
	// the same priority as an intent; an ordinary share cannot consume overflow.
	priority := receipt.Digibyte.Attempted || receipt.Dogecoin.Attempted || receipt.Pepecoin.Attempted || receipt.Dingocoin.Attempted || receipt.Bellscoin.Attempted || receipt.ProofHash != "" || receipt.OGGError != ""
	if q.pausedLocked(time.Now()) || q.pendingBytes+int64(len(blob)) > q.limits.MaxPendingBytes {
		q.paused = true
		if !priority {
			q.rejectedOrdinary++
			q.mu.Unlock()
			return false, errForgeBackpressure
		}
		q.priorityOverflow++
	}
	item := q.appendLocked(receipt)
	// Persist even when older records are waiting for Redis. On a disk failure
	// the receipt remains in memory for retry, but restart durability is lost.
	err = q.persist(item)
	q.mu.Unlock()
	if err != nil {
		return false, fmt.Errorf("FORGE receipt not durable (retained in memory): %w", err)
	}
	return q.flushFor(receipt.ID)
}

func (q *forgeAccountingQueue) appendLocked(receipt storage.ForgeShare) *forgeQueuedShare {
	seq := uint64(time.Now().UnixNano())
	if seq <= q.sequence {
		seq = q.sequence + 1
	}
	q.sequence = seq
	item := &forgeQueuedShare{path: filepath.Join(q.dir, fmt.Sprintf("%020d-%s.json", seq, receipt.ID)), receipt: receipt}
	q.pending = append(q.pending, item)
	q.byID[receipt.ID] = item
	q.undurable++
	return item
}

func (q *forgeAccountingQueue) flush() error {
	_, err := q.flushFor("")
	return err
}

// External-only uncertainty must not hold native share accounting hostage.
// OGG uncertainty and active submission remain ordering barriers.
func (q *forgeAccountingQueue) flushFor(wantID string) (bool, error) {
	q.flushMu.Lock()
	defer q.flushMu.Unlock()
	duplicate := false
	for index := 0; ; {
		q.mu.Lock()
		if index >= len(q.pending) {
			q.mu.Unlock()
			return duplicate, nil
		}
		item := q.pending[index]
		if item.active && item.credited {
			index++ // Recovery cannot change this already frozen native credit.
			q.mu.Unlock()
			continue
		}
		if item.active {
			q.mu.Unlock()
			return duplicate, fmt.Errorf("FORGE submission active")
		}
		if item.credit == nil {
			snapshot, err := item.creditSnapshot()
			if err != nil {
				q.mu.Unlock()
				return duplicate, err
			}
			item.credit = &snapshot
			q.dirtyLocked(item)
		}
		desired, snapshotErr := item.creditSnapshot()
		if snapshotErr != nil {
			q.mu.Unlock()
			return false, snapshotErr
		}
		unchanged := false
		if item.credited && item.intent != nil {
			prior := externalObservation(*item.credit)
			if item.external != nil {
				prior = *item.external
			}
			unchanged = prior == externalObservation(desired)
		}
		if !item.durable {
			if err := q.persist(item); err != nil {
				q.mu.Unlock()
				return false, err
			}
		}
		if unchanged {
			index++
			q.mu.Unlock()
			continue
		}
		item.active = true // prevent recovery mutation during Redis I/O
		credit, wasCredited := *item.credit, item.credited
		receipt := desired
		q.mu.Unlock()
		var exists bool
		var err error
		if !wasCredited {
			exists, err = q.writer.WriteForgeShare(credit)
		} else {
			updater, ok := q.writer.(interface {
				WriteForgeExternalOutcomes(storage.ForgeShare) error
			})
			if !ok {
				err = fmt.Errorf("FORGE writer lacks external outcome update")
			} else {
				err = updater.WriteForgeExternalOutcomes(receipt)
			}
		}
		q.mu.Lock()
		item.active = false
		q.accountingUnavailable = err != nil
		if err != nil {
			q.mu.Unlock()
			return false, err
		}
		q.lastAccountingSuccessMS = time.Now().UnixNano() / int64(time.Millisecond)
		if !wasCredited {
			item.credited = true
			if err := q.persist(item); err != nil {
				q.mu.Unlock()
				return false, err
			}
		}
		if wasCredited {
			observed := externalObservation(receipt)
			item.external = &observed
			if err := q.persist(item); err != nil {
				q.mu.Unlock()
				return false, err
			}
		}
		if item.receipt.ID == wantID {
			duplicate = exists
		}
		if !wasCredited && externalObservation(credit) != externalObservation(receipt) {
			q.mu.Unlock()
			continue
		}
		if item.intent != nil {
			index++
			q.mu.Unlock()
			if item.receipt.ID == wantID {
				return duplicate, nil
			}
			continue
		}
		if err := os.Remove(item.path); err != nil && !os.IsNotExist(err) {
			q.mu.Unlock()
			return false, err
		}
		if err := q.syncDir(); err != nil {
			q.mu.Unlock()
			return false, err
		}
		q.pendingBytes -= item.bytes
		delete(q.byID, item.receipt.ID)
		copy(q.pending[index:], q.pending[index+1:])
		q.pending[len(q.pending)-1] = nil
		q.pending = q.pending[:len(q.pending)-1]
		q.mu.Unlock()
		if item.receipt.ID == wantID {
			return duplicate, nil
		}
	}
}
