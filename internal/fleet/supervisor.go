package fleet

import (
	"context"
	"fmt"
	"io"
	"os"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/crewlog"
	"github.com/vulnetix/belai/internal/headless"
	"github.com/vulnetix/belai/internal/kanban"
	"github.com/vulnetix/belai/internal/notify"
)

// Slot is one place a crew run's workers go: a member profile in a
// repository of the scope.
type Slot struct {
	Profile string
	Repo    string
}

// Allocate shares each member's replicas between the run's unpaused
// repositories. A repository's share follows its weight times the work it has
// for that profile (demand), by largest remainder, so the crew goes where the
// work is and a heavier repository wins a tie. A profile with no work anywhere
// is spread by weight alone, so a scout still surveys. The total is then cut to
// max, dropping the slots with the least work per worker first.
func Allocate(run CrewRun, demand func(profile, repo string) int, max int) map[Slot]int {
	out := map[Slot]int{}
	var active []ScopeDir
	for _, d := range run.Scope {
		if !d.Paused {
			active = append(active, d)
		}
	}
	if len(active) == 0 {
		return out
	}
	score := map[Slot]int{}
	for _, m := range run.Members {
		r := m.Count()
		scores := make([]int, len(active))
		total := 0
		for i, d := range active {
			scores[i] = d.Weight * demand(m.Profile, d.Path)
			total += scores[i]
		}
		if total == 0 {
			for i, d := range active {
				scores[i] = d.Weight
				total += d.Weight
			}
		}
		type rem struct {
			i    int
			frac int // remainder numerator, compared over the same total
		}
		var rems []rem
		given := 0
		for i := range active {
			q := r * scores[i] / total
			if q > 0 {
				out[Slot{m.Profile, active[i].Path}] += q
				given += q
			}
			if scores[i] > 0 {
				rems = append(rems, rem{i, r * scores[i] % total})
			}
		}
		sort.SliceStable(rems, func(a, b int) bool {
			if rems[a].frac != rems[b].frac {
				return rems[a].frac > rems[b].frac
			}
			return active[rems[a].i].Weight > active[rems[b].i].Weight
		})
		for k := 0; given < r && k < len(rems); k++ {
			out[Slot{m.Profile, active[rems[k].i].Path}]++
			given++
		}
		for i, d := range active {
			score[Slot{m.Profile, d.Path}] = scores[i]
		}
	}
	if max <= 0 {
		return out
	}
	n := 0
	for _, c := range out {
		n += c
	}
	for n > max {
		var worst Slot
		found := false
		for s, c := range out {
			if c == 0 {
				continue
			}
			// The least work per worker goes first; ties break on the name so
			// the cut is the same every time.
			if !found || score[s]*out[worst] < score[worst]*c ||
				(score[s]*out[worst] == score[worst]*c && s.Repo+s.Profile > worst.Repo+worst.Profile) {
				worst, found = s, true
			}
		}
		out[worst]--
		if out[worst] == 0 {
			delete(out, worst)
		}
		n--
	}
	return out
}

// Supervisor runs a crew run: it keeps the workers where Allocate says, and
// stops them when the run stops. It is `belai agent supervise`.
type Supervisor struct {
	Registry *Registry
	Store    *kanban.Store
	RunID    string
	// Exe is the belai binary the workers are started with.
	Exe string
	// Max is the worker cap when the run sets none.
	Max int
	Log io.Writer
	// Demand counts a profile's claimable work in a repository; nil asks the
	// board. Spawn starts a worker; nil uses the registry.
	Demand func(profile, repo string) int
	Spawn  func(SpawnOptions) (string, error)
	// Interval is the reconcile period (default 5s); Grace the stop grace.
	Interval time.Duration
	Grace    time.Duration
	// MinTenure keeps a worker this long before it may be moved (default 1m).
	MinTenure time.Duration
	// Notify sends a notification about the crew (crew_sleeping when its
	// pool failed, crew_stopped when the run ends on its own); nil sends
	// nothing.
	Notify func(event string)
	// PID is this supervisor's process id (default os.Getpid()): the run
	// names its supervisor, and one that finds another named stands down.
	PID   int
	now   func() time.Time
	quiet time.Time
}

func (s *Supervisor) logf(format string, args ...any) {
	if s.Log != nil {
		fmt.Fprintf(s.Log, "%s %s\n", s.clock().Format(time.RFC3339), fmt.Sprintf(format, args...))
	}
}

func (s *Supervisor) notify(event string) {
	if s.Notify != nil {
		s.Notify(event)
	}
}

func (s *Supervisor) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// Run reconciles until the run stops or ctx ends; ctx ending stops the run.
func (s *Supervisor) Run(ctx context.Context) error {
	if s.Interval <= 0 {
		s.Interval = 5 * time.Second
	}
	if s.PID <= 0 {
		s.PID = os.Getpid()
	}
	s.quiet = s.clock()
	if err := s.claim(); err != nil {
		return err
	}
	s.logf("supervising crew run %s", s.RunID)
	for {
		run, err := s.Registry.GetRun(s.RunID)
		if err != nil {
			return err
		}
		if run.Supervisor != s.PID {
			// Another supervisor took the run over while this one was not
			// beating (hung, or thought dead): it owns the workers now.
			s.logf("crew run %s is supervised by %d now; standing down", run.ID, run.Supervisor)
			return nil
		}
		if ctx.Err() != nil || (run.State != RunRunning && run.State != RunOrphaned) {
			return s.finish(run, ctx.Err() != nil)
		}
		_, _ = s.Registry.UpdateRun(s.RunID, "supervisor", func(cr *CrewRun) error {
			cr.Beat = s.clock().UnixMilli()
			if cr.State == RunOrphaned && cr.Supervisor == s.PID {
				// Marked orphaned while a slow reconcile kept this supervisor
				// from beating: it is alive, so the run is its again.
				cr.State, cr.Reason = RunRunning, ""
			}
			return nil
		})
		if done := s.Reconcile(run); done != "" {
			_, _ = s.Registry.UpdateRun(s.RunID, "supervisor", func(cr *CrewRun) error {
				cr.State, cr.Reason = RunStopping, done
				return nil
			})
			continue
		}
		if !sleep(ctx, s.Interval) {
			continue
		}
	}
}

// claim names this process the run's supervisor. It refuses when another
// live supervisor runs it, so a second `agent supervise` (or two adopters
// racing) never doubles the crew.
func (s *Supervisor) claim() error {
	_, err := s.Registry.UpdateRun(s.RunID, "supervisor", func(cr *CrewRun) error {
		if !cr.State.Live() {
			return nil
		}
		if cr.Supervisor > 0 && cr.Supervisor != s.PID && s.Registry.SupervisorAlive(*cr) {
			return ErrSupervised
		}
		cr.Supervisor, cr.Beat = s.PID, s.clock().UnixMilli()
		if cr.State == RunOrphaned {
			cr.State, cr.Reason = RunRunning, ""
		}
		return nil
	})
	return err
}

func (s *Supervisor) finish(run CrewRun, cancelled bool) error {
	grace := s.Grace
	if grace <= 0 {
		grace = 20 * time.Second
	}
	s.Registry.stopRunWorkers(run.ID, grace)
	reason := run.Reason
	if reason == "" {
		reason = "stopped"
	}
	if cancelled {
		reason = "the supervisor was stopped"
	}
	_, err := s.Registry.UpdateRun(run.ID, "supervisor", func(cr *CrewRun) error {
		if cr.State.Live() {
			cr.State, cr.Reason, cr.Stopped = RunStopped, reason, s.clock().UnixMilli()
		}
		return nil
	})
	s.logf("crew run %s ended: %s", run.ID, reason)
	return err
}

// Reconcile starts the workers the allocation lacks and drains the surplus.
// It returns a reason to end the run when the scope has run dry (a run that
// does not stay ends after a quiet window with nothing to claim anywhere).
func (s *Supervisor) Reconcile(run CrewRun) string {
	run, why := s.poolStep(run)
	if why != "" {
		return why
	}
	demand := s.Demand
	if demand == nil {
		demand = s.boardDemand(run)
	}
	cached := map[Slot]int{}
	dem := func(profile, repo string) int {
		k := Slot{profile, repo}
		if v, ok := cached[k]; ok {
			return v
		}
		v := demand(profile, repo)
		cached[k] = v
		return v
	}
	max := run.MaxWorkers
	if max <= 0 {
		max = s.Max
	}
	want := Allocate(run, dem, max)
	live := s.Registry.RunWorkers(run.ID)
	have := map[Slot][]Record{}
	for _, rec := range live {
		k := Slot{rec.Profile, rec.Repo}
		have[k] = append(have[k], rec)
	}
	// Drain first, so the cap has room for the starts.
	tenure := s.MinTenure
	if tenure <= 0 {
		tenure = time.Minute
	}
	busy := 0
	draining := 0
	for k, recs := range have {
		surplus := len(recs) - want[k]
		// Idle workers go first, the youngest of them last.
		slices.SortFunc(recs, func(a, b Record) int {
			ai, bi := a.State == StateWorking, b.State == StateWorking
			switch {
			case ai != bi && ai:
				return 1
			case ai != bi:
				return -1
			}
			return int(a.Started - b.Started)
		})
		for _, rec := range recs {
			if rec.State == StateWorking {
				busy++
			}
			if rec.Draining {
				draining++
				surplus--
				continue
			}
			if surplus <= 0 {
				continue
			}
			if s.clock().Sub(time.UnixMilli(rec.Started)) < tenure && want[k] > 0 {
				continue
			}
			if _, err := s.Registry.SetControl(rec.ID, "supervisor", func(c *Control) { c.Drain = true }); err != nil {
				s.logf("drain %s: %v", rec.ID, err)
				continue
			}
			s.logf("drain %s (%s in %s): the crew needs it elsewhere", rec.ID, rec.Profile, rec.Repo)
			draining++
			surplus--
		}
	}
	running := len(live) - draining
	for _, k := range sortedSlots(want) {
		need := want[k] - (len(have[k]) - countDraining(have[k]))
		for range max0(need) {
			if max > 0 && running >= max {
				break
			}
			spawn := s.Spawn
			if spawn == nil {
				spawn = s.Registry.Spawn
			}
			id, err := spawn(SpawnOptions{Exe: s.Exe, Repo: k.Repo, Profile: k.Profile, Crew: run.Crew, Run: run.ID,
				Provider: run.First().Provider, Model: run.First().Model, Effort: run.Effort, Stay: true,
				MaxWorkers: max, WebControls: run.WebControls})
			if err != nil {
				s.logf("start %s in %s: %v", k.Profile, k.Repo, err)
				break
			}
			s.logf("start %s (%s in %s)", id, k.Profile, k.Repo)
			running++
		}
	}
	if run.Stay {
		return ""
	}
	total := 0
	for k := range cached {
		total += cached[k]
	}
	if total > 0 || busy > 0 {
		s.quiet = s.clock()
		return ""
	}
	poll := agentprofile.DefaultPoll
	if s.clock().Sub(s.quiet) >= QuietWindow(poll) {
		return fmt.Sprintf("done: nothing left to claim in the scope for %s", QuietWindow(poll))
	}
	return ""
}

func countDraining(recs []Record) int {
	n := 0
	for _, r := range recs {
		if r.Draining {
			n++
		}
	}
	return n
}

func max0(n int) int { return max(n, 0) }

func sortedSlots(m map[Slot]int) []Slot {
	out := make([]Slot, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Repo != out[j].Repo {
			return out[i].Repo < out[j].Repo
		}
		return out[i].Profile < out[j].Profile
	})
	return out
}

// boardDemand counts, per profile and repository, the cards a worker of the
// profile in that repository would claim now.
func (s *Supervisor) boardDemand(run CrewRun) func(profile, repo string) int {
	profiles := map[string]agentprofile.AgentProfile{}
	return func(profile, repo string) int {
		if s.Store == nil {
			return 0
		}
		p, ok := profiles[profile]
		if !ok {
			var err error
			if p, err = agentprofile.Load(profile); err != nil {
				return 0
			}
			profiles[profile] = p
		}
		r := kanban.ClaimRequest{Profile: p.Name, Crew: run.Crew, Worker: "supervisor", Host: headless.HostID(), Lease: p.LeaseDuration()}
		if k := p.Kanban; k != nil {
			r.Lists, r.Labels, r.AssignedOnly = k.ClaimLists(), k.Labels, k.AssignedOnly
			switch strings.ToLower(strings.TrimSpace(k.Project)) {
			case "all", "*":
			case "", "current", ".":
				_, r.Project = kanban.ProjectFor(repo)
			default:
				r.Project = k.Project
			}
		} else {
			_, r.Project = kanban.ProjectFor(repo)
		}
		n, _ := s.Store.Demand(r)
		return n
	}
}

// poolStep keeps the crew's pool phase. A worker that finds every target in
// the pool failed since its last success, with no other worker getting a card
// through after, puts the crew to sleep for FinalRetry; when the sleep ends
// the crew makes its final retry (a new epoch: every worker starts the pool
// again); a success after the crew went to sleep, in that pass or before it,
// makes the crew healthy again, and the pool failing once more ends the run
// with a reason.
func (s *Supervisor) poolStep(run CrewRun) (CrewRun, string) {
	if len(run.Pool) == 0 {
		return run, ""
	}
	now := s.clock().UnixMilli()
	last := run.LastSuccess
	for _, rec := range s.Registry.RunWorkers(run.ID) {
		last = max(last, rec.LastSuccess)
	}
	exhausted := false
	for _, rec := range s.Registry.RunWorkers(run.ID) {
		if rec.PoolExhausted && rec.PoolEpoch == run.PoolEpoch && rec.PoolExhaustedAt > last {
			exhausted = true
		}
	}
	phase, action, why := run.PoolPhase, "", ""
	switch run.PoolPhase {
	case PoolHealthy:
		if exhausted {
			phase, action = PoolSleeping, "pool_sleep"
			s.logf("every provider and model in the pool failed; the crew sleeps %s before its final retry", run.FinalRetry())
		}
	case PoolSleeping:
		switch {
		case last > run.RetryFrom:
			// A card got through after the crew went to sleep (a worker whose
			// wait ended first, or one mid-card on a target that works): the
			// pool is back without a final retry.
			phase, action = PoolHealthy, "pool_recovered"
			s.logf("the pool recovered while the crew slept")
		case now >= run.SleepUntil:
			phase, action = PoolFinalRetry, "final_retry"
			s.logf("final retry over the pool")
		}
	case PoolFinalRetry:
		switch {
		case last > run.RetryFrom:
			phase, action = PoolHealthy, "pool_recovered"
			s.logf("the pool recovered on the final retry")
		case exhausted:
			action = "pool_exhausted"
			why = fmt.Sprintf("every provider and model in the pool failed, and again after the final retry (%s later)", run.FinalRetry())
		}
	}
	if phase == run.PoolPhase && last == run.LastSuccess && action == "" {
		return run, ""
	}
	next, err := s.Registry.UpdateRun(run.ID, "supervisor", func(cr *CrewRun) error {
		cr.LastSuccess = max(cr.LastSuccess, last)
		switch action {
		case "pool_sleep":
			// RetryFrom marks when the crew went to sleep: any success after it,
			// while asleep or on the final retry, means the pool works again.
			cr.PoolPhase, cr.SleepUntil, cr.RetryFrom = PoolSleeping, s.clock().Add(cr.FinalRetry()).UnixMilli(), now
		case "final_retry":
			cr.PoolPhase, cr.PoolEpoch, cr.SleepUntil = PoolFinalRetry, cr.PoolEpoch+1, 0
		case "pool_recovered":
			cr.PoolPhase, cr.RetryFrom, cr.SleepUntil = PoolHealthy, 0, 0
		}
		return nil
	})
	if err != nil {
		s.logf("pool: %v", err)
		return run, why
	}
	if action != "" {
		recordPool(next, action)
	}
	switch {
	case why != "":
		s.notify(notify.EventCrewStopped)
	case action == "pool_sleep":
		s.notify(notify.EventCrewSleeping)
	}
	if why != "" {
		if l := openCrewLog(run.ID); l != nil {
			_ = l.AppendFault(crewlog.Fault{Run: run.ID, Crew: run.Crew, Class: FaultPoolExhausted, Outcome: "terminated", Detail: why})
			l.Close()
		}
	}
	return next, why
}
