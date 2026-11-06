package budget

import (
	"errors"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/transcript"
)

// Run caps (docs/run-caps.md): ceilings on one run's spend, tokens, passes and
// wall time. The meter only measures. It returns a verdict, and the layer that
// owns the run (the agent, the fleet worker) decides what to do with it, so
// this package imports neither.

// Reasons name the dimension a verdict is about. They are the words
// agent.FinalizeReasons accepts.
const (
	ReasonCost   = "cost"
	ReasonTokens = "tokens"
	ReasonPasses = "passes"
	ReasonWall   = "wall"
)

// Defaults for the caps the settings do not give.
const (
	DefaultFinalizeAt = config.DefaultRunCapFinalizeAt
	DefaultMaxExtends = config.DefaultRunCapMaxExtends
	// ExtendFraction is how much of its original ceiling one extension adds.
	ExtendFraction = 0.5
)

// Caps are the ceilings of a run. A zero ceiling is off.
type Caps struct {
	CostMicroUSD int64
	Tokens       int
	Passes       int
	Wall         time.Duration
	// FinalizeAt is the share of a ceiling at which the run is told to finalise.
	// Zero is DefaultFinalizeAt.
	FinalizeAt float64
	// MaxExtends is how many times the ceilings may be raised. Zero is none.
	MaxExtends int
}

// Active reports whether any ceiling is set.
func (c Caps) Active() bool {
	return c.CostMicroUSD > 0 || c.Tokens > 0 || c.Passes > 0 || c.Wall > 0
}

func (c Caps) finalizeAt() float64 {
	if c.FinalizeAt == 0 {
		return DefaultFinalizeAt
	}
	return c.FinalizeAt
}

// Validate rejects a malformed ceiling set: a negative number, a non-finite
// finalize_at, a finalize_at outside (0, 1) or negative extensions.
func (c Caps) Validate() error {
	switch {
	case c.CostMicroUSD < 0, c.Tokens < 0, c.Passes < 0, c.Wall < 0:
		return errors.New("a run cap must not be negative")
	case c.MaxExtends < 0:
		return errors.New("max extends must not be negative")
	case math.IsNaN(c.FinalizeAt) || c.FinalizeAt < 0 || c.FinalizeAt >= 1:
		return errors.New("finalize_at must be greater than 0 and less than 1")
	}
	return nil
}

// CapsFromSettings builds the caps of a session from the settings. The pass
// ceiling is resilience.max_passes.
func CapsFromSettings(s config.Settings) Caps {
	return Caps{
		CostMicroUSD: s.RunCapCostMicroUSD(),
		Tokens:       s.RunCapTokens(),
		Passes:       s.Resilience.MaxPassesOr(),
		Wall:         time.Duration(s.RunCapWallMinutes()) * time.Minute,
		FinalizeAt:   s.RunCapFinalizeAt(),
		MaxExtends:   s.RunCapMaxExtends(),
	}
}

// Level is a verdict's level.
type Level int

// The verdict levels.
const (
	// OK means no ceiling is near.
	OK Level = iota
	// Finalise means a ceiling reached FinalizeAt for the first time since the
	// run began or was last extended. It is reported once per reason.
	Finalise
	// Hit means a ceiling is reached. It is reported on every check until the
	// ceiling is raised.
	Hit
)

func (s Level) String() string {
	switch s {
	case Finalise:
		return "finalise"
	case Hit:
		return "hit"
	}
	return "ok"
}

// Verdict is the meter's answer: a level and, when it is not OK, the dimension
// that caused it. Warned, on a Hit, says the run was told to finalise for that
// reason before the ceiling was reached.
type Verdict struct {
	Level  Level
	Reason string
	Warned bool
}

// Snapshot is the counters a run carries across a resume. It holds numbers
// only.
type Snapshot struct {
	CostMicroUSD int64 `json:"cost_micro_usd"`
	Tokens       int   `json:"tokens"`
	Passes       int   `json:"passes"`
	Extends      int   `json:"extends"`
	WallMS       int64 `json:"wall_ms"`
}

// Meter adds up a run's spend and compares it with the ceilings. Its methods
// are safe to call from several goroutines, which is how subagents spend
// against their parent's meter.
type Meter struct {
	mu      sync.Mutex
	base    Caps // the ceilings as first set; an extension adds a share of these
	caps    Caps // the ceilings in force
	now     func() time.Time
	cost    int64
	tokens  int
	passes  int
	extends int
	// active is wall time already run, turnStart when a turn is running.
	active    time.Duration
	turnStart time.Time
	inTurn    bool
	warned    map[string]bool
	unpriced  bool
	stateKey  string // session id of the state file; "" keeps nothing
	forgotten bool   // Forget was called this turn: the turn's closing Save writes nothing
}

// NewMeter builds a meter for caps. It does not check them: call Validate.
func NewMeter(c Caps) *Meter {
	return &Meter{base: c, caps: c, now: time.Now, warned: map[string]bool{}}
}

// SetClock replaces the clock, for tests.
func (m *Meter) SetClock(now func() time.Time) {
	m.mu.Lock()
	m.now = now
	m.mu.Unlock()
}

// Caps is the ceilings in force.
func (m *Meter) Caps() Caps {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.caps
}

// SetCaps replaces the ceilings, keeping the counters. A user who changes a
// setting mid-session sets both the original and the ceilings in force, and an
// earlier extension is dropped with them.
func (m *Meter) SetCaps(c Caps) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.base, m.caps = c, c
	m.extends = 0
	m.warned = map[string]bool{}
}

// Spend adds one call's cost and tokens and returns the verdict.
func (m *Meter) Spend(costMicroUSD int64, tokens int) Verdict {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cost += max(costMicroUSD, 0)
	m.tokens += max(tokens, 0)
	return m.evaluate()
}

// Add adds one model call's usage, priced at p, and returns no verdict. A
// subagent spends against its parent's meter this way: a verdict is reported
// once, and only the run's own loop may take it. A nil price (a model with no
// known price) leaves the cost out, so the cost ceiling cannot act on a spend
// it cannot measure; the tokens still count.
func (m *Meter) Add(u transcript.Usage, p *Price) {
	var cost int64
	m.mu.Lock()
	defer m.mu.Unlock()
	if p != nil {
		cost = p.MicroUSD(u)
	} else {
		m.unpriced = true
	}
	m.cost += max(cost, 0)
	m.tokens += max(u.Total(), 0)
}

// Call is Add that also returns the verdict, for a run with one consumer.
func (m *Meter) Call(u transcript.Usage, p *Price) Verdict {
	m.Add(u, p)
	return m.Check()
}

// Unpriced reports whether a call was ever made with no price, so a caller can
// say once that the cost cap is off for that model.
func (m *Meter) Unpriced() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.unpriced
}

// Observe sets the token and pass totals from a loop that keeps its own count,
// never lowering either, and returns the verdict.
func (m *Meter) Observe(tokens, passes int) Verdict {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tokens = max(m.tokens, tokens)
	m.passes = max(m.passes, passes)
	return m.evaluate()
}

// SetPasses sets the turn's pass count from a loop that keeps its own, without
// taking a verdict.
func (m *Meter) SetPasses(n int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.passes = max(n, 0)
}

// ExtendBy is what one extension adds to the ceiling for reason, in its unit:
// millionths of a dollar, tokens or milliseconds.
func (m *Meter) ExtendBy(reason string) int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	step := func(base int64) int64 {
		if base <= 0 {
			return 0
		}
		return max(int64(math.Ceil(float64(base)*ExtendFraction)), 1)
	}
	switch reason {
	case ReasonCost:
		return step(m.base.CostMicroUSD)
	case ReasonTokens:
		return step(int64(m.base.Tokens))
	case ReasonWall:
		return step(m.base.Wall.Milliseconds())
	}
	return 0
}

// Pass counts one pass and returns the verdict.
func (m *Meter) Pass() Verdict {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.passes++
	return m.evaluate()
}

// Check returns the verdict now, which is how a wall ceiling is noticed.
func (m *Meter) Check() Verdict {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.evaluate()
}

// BeginTurn starts the wall clock and the turn's pass count. Time between
// turns does not count, and the pass ceiling is per turn.
func (m *Meter) BeginTurn() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.inTurn {
		return
	}
	m.inTurn, m.turnStart = true, m.now()
	m.forgotten = false
	m.passes = 0
	delete(m.warned, ReasonPasses)
}

// EndTurn stops the wall clock and adds the turn's time.
func (m *Meter) EndTurn() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.inTurn {
		return
	}
	m.active += max(m.now().Sub(m.turnStart), 0)
	m.inTurn = false
}

func (m *Meter) wallLocked() time.Duration {
	w := m.active
	if m.inTurn {
		w += max(m.now().Sub(m.turnStart), 0)
	}
	return w
}

// Reached reports whether used is at least frac of limit, rounded up so a
// small limit still finalises before its last unit. A zero limit is off.
func Reached(used, limit int64, frac float64) bool {
	return limit > 0 && used >= int64(math.Ceil(float64(limit)*frac))
}

type dimension struct {
	reason string
	used   int64
	limit  int64
}

func (m *Meter) dims() [4]dimension {
	return [4]dimension{
		{ReasonCost, m.cost, m.caps.CostMicroUSD},
		{ReasonTokens, int64(m.tokens), int64(m.caps.Tokens)},
		{ReasonPasses, int64(m.passes), int64(m.caps.Passes)},
		{ReasonWall, int64(m.wallLocked()), int64(m.caps.Wall)},
	}
}

// evaluate is the verdict for the counters now. A hit outranks a finalise.
// Callers hold mu.
func (m *Meter) evaluate() Verdict {
	fa := m.caps.finalizeAt()
	var fin *Verdict
	for _, d := range m.dims() {
		if d.limit <= 0 {
			continue
		}
		if d.used >= d.limit {
			warned := m.warned[d.reason]
			m.warned[d.reason] = true
			return Verdict{Level: Hit, Reason: d.reason, Warned: warned}
		}
		if fin == nil && !m.warned[d.reason] && Reached(d.used, d.limit, fa) {
			m.warned[d.reason] = true
			fin = &Verdict{Level: Finalise, Reason: d.reason}
		}
	}
	if fin != nil {
		return *fin
	}
	return Verdict{}
}

// ExtendsLeft is how many extensions remain.
func (m *Meter) ExtendsLeft() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return max(m.caps.MaxExtends-m.extends, 0)
}

// Extend raises the cost, token and wall ceilings by half of their original
// values, once, while extensions remain, and lets the run be told to finalise
// again at the new ceilings. The pass ceiling is resilience.max_passes and is
// not extended. It reports whether it extended. Only the surface that heard the
// user's own answer calls it.
func (m *Meter) Extend() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.extendLocked()
}

func (m *Meter) extendLocked() bool {
	if m.extends >= m.caps.MaxExtends {
		return false
	}
	m.extends++
	step := func(base int64) int64 {
		if base <= 0 {
			return 0
		}
		return max(int64(math.Ceil(float64(base)*ExtendFraction)), 1)
	}
	m.caps.CostMicroUSD += step(m.base.CostMicroUSD)
	m.caps.Tokens += int(step(int64(m.base.Tokens)))
	m.caps.Wall += time.Duration(step(int64(m.base.Wall)))
	delete(m.warned, ReasonCost)
	delete(m.warned, ReasonTokens)
	delete(m.warned, ReasonWall)
	return true
}

// Snapshot is the counters now.
func (m *Meter) Snapshot() Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	return Snapshot{CostMicroUSD: m.cost, Tokens: m.tokens, Passes: m.passes, Extends: m.extends, WallMS: m.wallLocked().Milliseconds()}
}

// Restore loads the counters of a resumed session and raises the ceilings by
// the extensions it had already used.
func (m *Meter) Restore(s Snapshot) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cost, m.tokens, m.passes = max(s.CostMicroUSD, 0), max(s.Tokens, 0), max(s.Passes, 0)
	m.active = time.Duration(max(s.WallMS, 0)) * time.Millisecond
	m.extends = 0
	m.caps = m.base
	m.warned = map[string]bool{}
	for i := 0; i < s.Extends; i++ {
		if !m.extendLocked() {
			break
		}
	}
	// A ceiling the restored counters are already past was warned about.
	for _, d := range m.dims() {
		if d.limit > 0 && Reached(d.used, d.limit, m.caps.finalizeAt()) {
			m.warned[d.reason] = true
		}
	}
}

// Describe is the meter's state as one line of numbers and enums, for the
// footer chip and /caps. It holds no text a model or tool wrote.
func (m *Meter) Describe() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var parts []string
	for _, d := range m.dims() {
		if d.limit <= 0 {
			continue
		}
		switch d.reason {
		case ReasonCost:
			parts = append(parts, fmt.Sprintf("cost $%.2f of $%.2f", float64(d.used)/1e6, float64(d.limit)/1e6))
		case ReasonTokens:
			parts = append(parts, fmt.Sprintf("tokens %d of %d", d.used, d.limit))
		case ReasonPasses:
			parts = append(parts, fmt.Sprintf("passes %d of %d", d.used, d.limit))
		case ReasonWall:
			parts = append(parts, fmt.Sprintf("wall %s of %s", time.Duration(d.used).Round(time.Second), time.Duration(d.limit).Round(time.Second)))
		}
	}
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += ", "
		}
		out += p
	}
	return out
}

// Share is the largest used share of any set cost, token or wall ceiling, as a
// whole percent, with the word of the dimension it is for. It is 0 and "" when
// no such ceiling is set.
func (m *Meter) Share() (reason string, pct int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	best := -1.0
	for _, d := range m.dims() {
		if d.limit <= 0 || d.reason == ReasonPasses {
			continue
		}
		if f := float64(d.used) / float64(d.limit); f > best {
			best, reason = f, d.reason
		}
	}
	if best < 0 {
		return "", 0
	}
	return reason, int(math.Floor(min(best, 9.99) * 100))
}

// MeterFor builds the run-cap meter of a session from its settings and restores
// the counters saved under sessionID. It returns nil with no error when no
// spend, token or wall cap is set, and an error, so the run refuses to start,
// when a cap is malformed.
func MeterFor(s config.Settings, sessionID string) (*Meter, error) {
	if err := config.ValidateRunCaps(s); err != nil {
		return nil, err
	}
	if !s.RunCapsActive() {
		return nil, nil
	}
	c := CapsFromSettings(s)
	if err := c.Validate(); err != nil {
		return nil, err
	}
	m := NewMeter(c)
	m.Persist(sessionID)
	return m, nil
}
