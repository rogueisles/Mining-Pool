package proxy

import (
	"encoding/json"
	"errors"
	"log"
	"math"
	"math/big"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/HawyUkr/open-progpow-pool/forgepool"
)

// Share settings never change a chain's consensus difficulty or reward rules.
type ForgeVarDiffConfig struct {
	Enabled         bool  `json:"enabled"`
	Minimum         int64 `json:"minimum"`
	Maximum         int64 `json:"maximum"`
	TargetSeconds   int64 `json:"targetSeconds"`
	RetargetSeconds int64 `json:"retargetSeconds"`
}

func (v ForgeVarDiffConfig) validate(base int64) error {
	if !v.Enabled {
		return nil
	}
	if v.Minimum < 1 || v.Maximum < v.Minimum || v.Maximum > 1<<53-1 || base < v.Minimum || base > v.Maximum ||
		v.TargetSeconds < 1 || v.TargetSeconds > 30 || v.RetargetSeconds < 15 || v.RetargetSeconds > 300 {
		return errors.New("invalid FORGE variable share difficulty settings")
	}
	return nil
}

type forgeShareAssignment struct {
	difficulty int64
	target     *big.Int
}
type forgeDifficultyState struct {
	sync.Mutex
	desired      int64
	fixed        bool
	started      time.Time
	acceptedWork float64
	lastLogged   int64
	assignments  map[string]forgeShareAssignment
	order        []string
}

func (s *ProxyServer) forgeVarDiffEnabled() bool {
	return s.config.Proxy.Forge.VariableDifficulty.Enabled
}

// Find the hardest share difficulty which still reports EVERY active chain
// winner. In particular, an easy OGG target must not be hidden behind a rental
// provider's preferred harder share target. Integer division rounds safely down.
func forgeSafeShareDifficulty(job *forgeJob) (int64, error) {
	if job == nil || job.OGG == nil || job.Digibyte == nil {
		return 0, errors.New("missing share target inputs")
	}
	easiest, err := parseHexTarget(job.OGG.Target)
	if err != nil || easiest.Sign() <= 0 || easiest.BitLen() > 256 {
		return 0, errors.New("invalid OGG share coverage target")
	}
	include := func(bits uint32) error {
		t, e := forgepool.CompactTarget(bits)
		if e != nil {
			return e
		}
		if t.Cmp(easiest) > 0 {
			easiest = t
		}
		return nil
	}
	if job.Digibyte.DigibyteActive {
		if err = include(job.Digibyte.Template.Bits); err != nil {
			return 0, err
		}
	}
	if job.Digibyte.DOGE != nil {
		if err = include(job.Digibyte.DOGE.Bits); err != nil {
			return 0, err
		}
	}
	for _, aux := range job.Digibyte.AdditionalAux {
		if err = include(aux.Bits); err != nil {
			return 0, err
		}
	}
	base, _ := forgepool.StratumTargetForDifficulty(big.NewInt(1))
	limit := new(big.Int).Div(base, easiest)
	if limit.Sign() <= 0 {
		return 0, errors.New("active chain target cannot be covered by integer Scrypt shares")
	}
	if !limit.IsInt64() || limit.Cmp(big.NewInt(1<<53-1)) > 0 {
		return 1<<53 - 1, nil
	}
	return limit.Int64(), nil
}

func forgePasswordDifficulty(password string) (int64, bool, error) {
	var requested int64
	found := false
	for _, part := range strings.Split(password, ",") {
		part = strings.TrimSpace(part)
		if !strings.HasPrefix(part, "d=") {
			continue
		}
		if found {
			return 0, false, errors.New("duplicate difficulty request")
		}
		n, err := strconv.ParseInt(strings.TrimPrefix(part, "d="), 10, 64)
		if err != nil || n < 1 || n > 1<<53-1 {
			return 0, false, errors.New("invalid difficulty request")
		}
		requested, found = n, true
	}
	return requested, found, nil
}

func (cs *Session) requestForgeDifficulty(s *ProxyServer, requested int64, now time.Time) {
	v := s.config.Proxy.Forge.VariableDifficulty
	if requested < v.Minimum {
		requested = v.Minimum
	}
	if requested > v.Maximum {
		requested = v.Maximum
	}
	cs.forgeDiff.Lock()
	defer cs.forgeDiff.Unlock()
	cs.forgeDiff.desired, cs.forgeDiff.fixed = requested, true
	cs.forgeDiff.started, cs.forgeDiff.acceptedWork = now, 0
}

// This state is per connection and per job, so a retarget never changes the
// acceptance threshold or credited work for an already issued job.
func (cs *Session) forgeAssignment(s *ProxyServer, job *forgeJob, now time.Time) (forgeShareAssignment, error) {
	v := s.config.Proxy.Forge.VariableDifficulty
	safe, err := forgeSafeShareDifficulty(job)
	if err != nil {
		return forgeShareAssignment{}, err
	}
	d := &cs.forgeDiff
	d.Lock()
	defer d.Unlock()
	if a, ok := d.assignments[job.ID]; ok {
		return a, nil
	}
	if d.desired == 0 {
		d.desired = s.config.Proxy.Difficulty
	}
	if d.started.IsZero() {
		d.started = now
	}
	elapsed := now.Sub(d.started).Seconds()
	if !d.fixed && elapsed >= float64(v.RetargetSeconds) {
		candidate := d.acceptedWork / elapsed * float64(v.TargetSeconds)
		// Bounded changes and a 25% deadband avoid chasing individual lucky shares.
		low, high := math.Max(float64(v.Minimum), float64(d.desired)/4), math.Min(float64(v.Maximum), float64(d.desired)*4)
		candidate = math.Max(low, math.Min(high, candidate))
		if candidate < float64(d.desired)*0.75 || candidate > float64(d.desired)*1.25 {
			d.desired = int64(candidate)
		}
		d.started, d.acceptedWork = now, 0
	}
	diff := d.desired
	if diff < v.Minimum {
		diff = v.Minimum
	}
	if diff > v.Maximum {
		diff = v.Maximum
	}
	if diff > safe {
		diff = safe
	}
	target, err := forgepool.StratumTargetForDifficulty(big.NewInt(diff))
	if err != nil {
		return forgeShareAssignment{}, err
	}
	a := forgeShareAssignment{diff, target}
	if d.lastLogged != diff {
		log.Printf("FORGE share difficulty assigned=%d preferred=%d coverageLimit=%d automatic=%t", diff, d.desired, safe, !d.fixed)
		d.lastLogged = diff
	}
	if d.assignments == nil {
		d.assignments = make(map[string]forgeShareAssignment)
	}
	d.assignments[job.ID] = a
	d.order = append(d.order, job.ID)
	limit := s.forge.jobBacklog + 2
	if limit < 4 {
		limit = 4
	}
	for len(d.order) > limit {
		delete(d.assignments, d.order[0])
		d.order = d.order[1:]
	}
	return a, nil
}

func (cs *Session) issuedForgeAssignment(jobID string) (forgeShareAssignment, bool) {
	cs.forgeDiff.Lock()
	defer cs.forgeDiff.Unlock()
	a, ok := cs.forgeDiff.assignments[jobID]
	return a, ok
}

func (cs *Session) recordForgeAccepted(s *ProxyServer, jobID string) {
	if !s.forgeVarDiffEnabled() {
		return
	}
	cs.forgeDiff.Lock()
	defer cs.forgeDiff.Unlock()
	if a, ok := cs.forgeDiff.assignments[jobID]; ok {
		cs.forgeDiff.acceptedWork += float64(a.difficulty)
	}
}

// Keep difficulty + its following job indivisible against concurrent broadcasts.
// The encoder lock is also used by normal replies; no second socket writer exists.
func (cs *Session) pushForgeWork(s *ProxyServer, job *forgeJob) error {
	cs.Lock()
	defer cs.Unlock()
	diff := s.config.Proxy.Difficulty
	if s.forgeVarDiffEnabled() {
		a, err := cs.forgeAssignment(s, job, time.Now())
		if err != nil {
			return err
		}
		diff = a.difficulty
	}
	if err := cs.enc.Encode(StratumPushMessage{Id: nil, Method: "mining.set_difficulty", Params: []interface{}{diff}}); err != nil {
		return err
	}
	return cs.enc.Encode(StratumPushMessage{Id: nil, Method: "mining.notify", Params: forgeNotifyParams(job)})
}

func decodeForgeSuggestedDifficulty(raw json.RawMessage) (int64, error) {
	var values []json.Number
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	if dec.Decode(&values) != nil || len(values) != 1 {
		return 0, errors.New("invalid difficulty request")
	}
	n, err := values[0].Int64()
	if err != nil || n < 1 || n > 1<<53-1 {
		return 0, errors.New("invalid difficulty request")
	}
	return n, nil
}
