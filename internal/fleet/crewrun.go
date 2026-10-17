package fleet

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/posture"
	"github.com/vulnetix/belai/internal/proc"
	"github.com/vulnetix/belai/internal/trustgate"
)

// A crew run is one launch of a crew over a scope of repositories. The crew is
// one crew however many repositories it works: its supervisor shares the
// members' workers between the repositories by their weight and by how much
// claimable work each has, and moves them as that changes. A worker still
// runs in one repository at a time, because each repository's settings,
// posture, trust and MCP servers are its own (worker.go).
//
// The run file is the single source of truth for the crew's settings. A launch
// writes it, and the control room edits it while the crew runs; the supervisor
// and the workers re-read it, so an edit applies at the next claim.

// RunState is a crew run's lifecycle state.
type RunState string

// Crew run states.
const (
	RunRunning  RunState = "running"
	RunStopping RunState = "stopping"
	RunStopped  RunState = "stopped"
	RunFailed   RunState = "failed"
	// RunOrphaned is a run whose supervisor died while the run was still
	// meant to run. Its workers carry on, and the first thing to notice (one
	// of its workers, belai rc, a relaunch or the control room) starts a new
	// supervisor (AdoptRun).
	RunOrphaned RunState = "orphaned"
)

// Live reports whether the run's supervisor is meant to be running.
func (s RunState) Live() bool { return s == RunRunning || s == RunStopping || s == RunOrphaned }

// Crew run limits.
const (
	MinWeight         = 1
	MaxWeight         = 10
	MaxPool           = 8
	DefaultSwitch     = 3
	DefaultFinalRetry = 15 * time.Minute
	// SupervisorStale is how old a supervisor's heartbeat may be before the
	// supervisor counts as gone, whatever its pid says (a reused pid, a
	// zombie, a hung process).
	SupervisorStale = 30 * time.Second
	// MaxSupervisorRestarts supervisors started for one run within
	// SupervisorRestartWindow, after which the run fails: a supervisor that
	// keeps dying will not be fixed by another restart.
	MaxSupervisorRestarts   = 5
	SupervisorRestartWindow = 10 * time.Minute
)

// Pool phases: the crew's state against its provider pool.
const (
	// PoolHealthy: workers cycle the pool as each target fails.
	PoolHealthy = ""
	// PoolSleeping: every target in the pool failed; nobody claims until
	// SleepUntil, then the crew makes its final retry.
	PoolSleeping = "sleeping"
	// PoolFinalRetry: the crew's last pass over the pool. A success returns it
	// to healthy; the whole pool failing again ends the run.
	PoolFinalRetry = "final_retry"
)

// ScopeDir is one repository in a crew run's scope.
type ScopeDir struct {
	Path string `json:"path"`
	// Weight ranks the repository against the others in the scope: a heavier
	// one gets a larger share of the workers for the same amount of work.
	Weight int `json:"weight,omitempty"`
	// Paused takes the repository out of the allocation: its workers finish
	// the card they hold and leave, and none are started for it.
	Paused bool `json:"paused,omitempty"`
}

// Target is a provider and model a worker can run on.
type Target struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

// String is "provider/model".
func (t Target) String() string {
	if t.Provider == "" {
		return t.Model
	}
	return t.Provider + "/" + t.Model
}

// Zero reports whether the target names nothing.
func (t Target) Zero() bool { return t.Provider == "" && t.Model == "" }

// CrewRun is a launched crew: what it runs, where, and on which models.
type CrewRun struct {
	ID      string                `json:"id"`
	Crew    string                `json:"crew"`
	Scope   []ScopeDir            `json:"scope"`
	Members []agentprofile.Member `json:"members"`
	// Pool is the providers and models the crew runs on. Workers start on
	// the first; after SwitchAfter outages in a row on one, a worker moves to
	// the next, wrapping round, so it comes back to a target that failed
	// before. Empty keeps each profile's own model, with no switching.
	Pool        []Target `json:"pool,omitempty"`
	SwitchAfter int      `json:"switch_after,omitempty"`
	// FinalRetrySecs is how long the crew sleeps once every target in the
	// pool failed, before its final retry.
	FinalRetrySecs int    `json:"final_retry_secs,omitempty"`
	Effort         string `json:"effort,omitempty"`
	// MaxWorkers caps the run's workers; zero is agents.max_workers.
	MaxWorkers int `json:"max_workers,omitempty"`
	// Stay keeps the run going when nothing is left to claim anywhere in
	// the scope; by default it ends after a quiet window.
	Stay bool `json:"stay,omitempty"`
	// WebControls lets the website change the workers' controls.
	WebControls bool `json:"web_controls,omitempty"`

	State      RunState `json:"state"`
	Reason     string   `json:"reason,omitempty"`
	Supervisor int      `json:"supervisor,omitempty"`
	Beat       int64    `json:"heartbeat,omitempty"`
	Started    int64    `json:"started"`
	Stopped    int64    `json:"stopped,omitempty"`
	Updated    int64    `json:"updated,omitempty"`
	// Seq counts the edits; UpdatedBy says who made the last one ("launch",
	// "human", "supervisor").
	Seq       int64  `json:"seq"`
	UpdatedBy string `json:"updated_by,omitempty"`
	// Restarts counts the supervisors started for the run since
	// RestartWindow, by AdoptRun.
	Restarts      int   `json:"restarts,omitempty"`
	RestartWindow int64 `json:"restart_window,omitempty"`

	// The pool's state, kept by the supervisor (supervisor.go, poolStep).
	// PoolEpoch counts the final retries started: a worker that sees it move
	// forgets which targets failed and starts the pool again. LastSuccess is
	// the newest card any worker finished without a provider outage.
	// RetryFrom is when the crew went to sleep: a success after it recovers
	// the pool.
	PoolPhase   string `json:"pool_phase,omitempty"`
	SleepUntil  int64  `json:"sleep_until,omitempty"`
	PoolEpoch   int    `json:"pool_epoch,omitempty"`
	RetryFrom   int64  `json:"retry_from,omitempty"`
	LastSuccess int64  `json:"last_success,omitempty"`
}

// SwitchAfterOr is the outage streak that moves a worker to the next target
// in the pool.
func (r CrewRun) SwitchAfterOr() int {
	if r.SwitchAfter <= 0 {
		return DefaultSwitch
	}
	return r.SwitchAfter
}

// FinalRetry is how long the crew sleeps before its final retry.
func (r CrewRun) FinalRetry() time.Duration {
	if r.FinalRetrySecs <= 0 {
		return DefaultFinalRetry
	}
	return time.Duration(r.FinalRetrySecs) * time.Second
}

// First is the pool's first target, zero for an empty pool.
func (r CrewRun) First() Target {
	if len(r.Pool) == 0 {
		return Target{}
	}
	return r.Pool[0]
}

// InPool reports whether t is one of the pool's targets.
func (r CrewRun) InPool(t Target) bool { return slices.Contains(r.Pool, t) }

// Dir returns the scope entry for path.
func (r CrewRun) Dir(path string) (ScopeDir, bool) {
	for _, d := range r.Scope {
		if d.Path == path {
			return d, true
		}
	}
	return ScopeDir{}, false
}

// Replicas is the run's total worker count across its members.
func (r CrewRun) Replicas() int {
	n := 0
	for _, m := range r.Members {
		n += m.Count()
	}
	return n
}

// Normalize cleans the run in place: absolute, de-duplicated paths, weights
// in range, and the defaults written out so the control room shows them.
func (r *CrewRun) Normalize() {
	seen := map[string]bool{}
	scope := r.Scope[:0]
	for _, d := range r.Scope {
		if abs, err := filepath.Abs(d.Path); err == nil {
			d.Path = abs
		}
		d.Path = filepath.Clean(d.Path)
		if d.Path == "" || seen[d.Path] {
			continue
		}
		seen[d.Path] = true
		d.Weight = min(max(d.Weight, MinWeight), MaxWeight)
		scope = append(scope, d)
	}
	r.Scope = scope
	for i := range r.Members {
		r.Members[i].Replicas = min(max(r.Members[i].Replicas, 0), agentprofile.MaxReplicas)
	}
	if r.SwitchAfter <= 0 {
		r.SwitchAfter = DefaultSwitch
	}
	if r.FinalRetrySecs <= 0 {
		r.FinalRetrySecs = int(DefaultFinalRetry / time.Second)
	}
	// A target named twice would only be tried twice in a row.
	var pool []Target
	for _, t := range r.Pool {
		if !t.Zero() && !slices.Contains(pool, t) {
			pool = append(pool, t)
		}
	}
	r.Pool = pool
}

// Validate checks a normalized run.
func (r CrewRun) Validate() error {
	switch {
	case strings.TrimSpace(r.Crew) == "":
		return errors.New("a crew run needs a crew")
	case len(r.Scope) == 0:
		return errors.New("a crew run needs at least one directory in its scope")
	case len(r.Members) == 0:
		return errors.New("a crew run needs at least one member")
	case len(r.Pool) > MaxPool:
		return fmt.Errorf("a crew run's pool holds at most %d targets", MaxPool)
	case r.MaxWorkers < 0:
		return errors.New("max workers cannot be negative")
	}
	for _, t := range r.Pool {
		if t.Model == "" {
			return errors.New("a pool target needs a model")
		}
	}
	return nil
}

// NewCrewRun builds a run of crew over dirs, with the crew's members.
func NewCrewRun(c agentprofile.Crew, dirs []ScopeDir) CrewRun {
	run := CrewRun{Crew: c.Name, Scope: slices.Clone(dirs), Members: slices.Clone(c.Members)}
	run.Normalize()
	return run
}

// ParseScopeDir reads "PATH" or "PATH:WEIGHT".
func ParseScopeDir(s string) (ScopeDir, error) {
	d := ScopeDir{Path: s, Weight: MinWeight}
	if i := strings.LastIndexByte(s, ':'); i > 0 {
		var w int
		if _, err := fmt.Sscanf(s[i+1:], "%d", &w); err == nil && fmt.Sprint(w) == s[i+1:] {
			if w < MinWeight || w > MaxWeight {
				return ScopeDir{}, fmt.Errorf("a weight runs %d to %d", MinWeight, MaxWeight)
			}
			d.Path, d.Weight = s[:i], w
		}
	}
	if strings.TrimSpace(d.Path) == "" {
		return ScopeDir{}, errors.New("an empty directory")
	}
	return d, nil
}

var runShape = idShape

func newRunID(crew string) string {
	var b [3]byte
	_, _ = rand.Read(b[:])
	base := strings.Trim(idUnsafe.ReplaceAllString(strings.ToLower(crew), "-"), "-")
	if base == "" {
		base = "crew"
	}
	if len(base) > 40 {
		base = base[:40]
	}
	return "run-" + base + "-" + hex.EncodeToString(b[:])
}

func (r *Registry) runsDir() string { return filepath.Join(r.dir, "crews") }

func (r *Registry) runPath(id string) string { return filepath.Join(r.runsDir(), id+".json") }

// RunLogPath is where a run's supervisor writes its log.
func (r *Registry) RunLogPath(id string) string { return filepath.Join(r.LogDir(), id+".log") }

// writeJSONAtomic writes v to path through a temporary file in dir.
func writeJSONAtomic(dir, path string, v any) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

// GetRun reads one crew run.
func (r *Registry) GetRun(id string) (CrewRun, error) {
	if !runShape.MatchString(id) {
		return CrewRun{}, fmt.Errorf("fleet: invalid crew run id %q", id)
	}
	data, err := os.ReadFile(r.runPath(id))
	if errors.Is(err, os.ErrNotExist) {
		return CrewRun{}, fmt.Errorf("no crew run %q", id)
	}
	if err != nil {
		return CrewRun{}, err
	}
	var run CrewRun
	return run, json.Unmarshal(data, &run)
}

func (r *Registry) saveRun(run CrewRun) error {
	if !runShape.MatchString(run.ID) {
		return fmt.Errorf("fleet: invalid crew run id %q", run.ID)
	}
	return writeJSONAtomic(r.runsDir(), r.runPath(run.ID), run)
}

// SupervisorAlive reports whether the run's supervisor is running: its
// process exists and it beat within SupervisorStale (a supervisor that has
// not beaten yet is judged from when the run started or was adopted).
func (r *Registry) SupervisorAlive(run CrewRun) bool {
	if run.Supervisor <= 0 || !proc.Alive(run.Supervisor) {
		return false
	}
	last := max(run.Beat, run.Started)
	return r.now().Sub(time.UnixMilli(last)) < SupervisorStale
}

// orphaned reports whether a live run has lost its supervisor.
func (r *Registry) orphaned(run CrewRun) bool {
	return run.State.Live() && run.Supervisor > 0 && !r.SupervisorAlive(run)
}

// ListRuns returns every crew run, newest first. A live run whose supervisor
// is gone is marked orphaned; its workers carry on, and AdoptRun starts it a
// new supervisor.
func (r *Registry) ListRuns() ([]CrewRun, error) {
	entries, err := os.ReadDir(r.runsDir())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []CrewRun
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".json") || strings.HasPrefix(name, ".") {
			continue
		}
		run, err := r.GetRun(strings.TrimSuffix(name, ".json"))
		if err != nil {
			continue
		}
		if run.State == RunRunning && r.orphaned(run) {
			if up, err := r.UpdateRun(run.ID, "harness", func(cr *CrewRun) error {
				if cr.State == RunRunning && r.orphaned(*cr) {
					cr.State, cr.Reason = RunOrphaned, ReasonOrphaned
				}
				return nil
			}); err == nil {
				run = up
			}
		}
		out = append(out, run)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Started > out[j].Started })
	return out, nil
}

// LiveRun returns the live run of a crew, if any.
func (r *Registry) LiveRun(crew string) (CrewRun, bool) {
	runs, _ := r.ListRuns()
	for _, run := range runs {
		if run.Crew == crew && run.State.Live() {
			return run, true
		}
	}
	return CrewRun{}, false
}

// UpdateRun edits a run under the runs lock: fn sees the current run, and the
// result is normalized, validated, stamped and saved. by says who changed it.
func (r *Registry) UpdateRun(id, by string, fn func(*CrewRun) error) (CrewRun, error) {
	if err := os.MkdirAll(r.runsDir(), 0o700); err != nil {
		return CrewRun{}, err
	}
	release, err := config.AcquireFileLock(filepath.Join(r.runsDir(), ".lock"))
	if err != nil {
		return CrewRun{}, err
	}
	defer release()
	run, err := r.GetRun(id)
	if err != nil {
		return CrewRun{}, err
	}
	if err := fn(&run); err != nil {
		return CrewRun{}, err
	}
	run.Normalize()
	if err := run.Validate(); err != nil {
		return CrewRun{}, err
	}
	run.Seq++
	run.Updated, run.UpdatedBy = r.now().UnixMilli(), by
	return run, r.saveRun(run)
}

// ErrRunLive refuses a second launch of a crew that already runs.
var ErrRunLive = errors.New("fleet: this crew is already running; change its scope or settings in the control room")

// SupervisorStarter starts a run's supervisor and returns its pid.
type SupervisorStarter func(run CrewRun) (int, error)

// LaunchCrew starts a crew run: it records the run and starts its supervisor,
// which starts the workers. A crew runs once at a time, whatever its scope.
func (r *Registry) LaunchCrew(run CrewRun, start SupervisorStarter) (CrewRun, error) {
	run.Normalize()
	if err := run.Validate(); err != nil {
		return CrewRun{}, err
	}
	if err := os.MkdirAll(r.dir, 0o700); err != nil {
		return CrewRun{}, err
	}
	release, err := config.AcquireFileLock(filepath.Join(r.dir, ".crewstart"))
	if err != nil {
		return CrewRun{}, err
	}
	defer release()
	if live, ok := r.LiveRun(run.Crew); ok {
		return CrewRun{}, fmt.Errorf("%w (%s)", ErrRunLive, live.ID)
	}
	now := r.now().UnixMilli()
	run.ID, run.State, run.Started, run.Updated, run.UpdatedBy, run.Seq = newRunID(run.Crew), RunRunning, now, now, "launch", 1
	run.Reason, run.Stopped, run.Supervisor = "", 0, 0
	if err := r.saveRun(run); err != nil {
		return CrewRun{}, err
	}
	if start == nil {
		return run, nil
	}
	pid, err := start(run)
	if err != nil {
		run, _ = r.UpdateRun(run.ID, "launch", func(cr *CrewRun) error {
			cr.State, cr.Reason, cr.Stopped = RunFailed, "the supervisor did not start", r.now().UnixMilli()
			return nil
		})
		return run, err
	}
	return r.UpdateRun(run.ID, "launch", func(cr *CrewRun) error {
		cr.Supervisor = pid
		return nil
	})
}

// StartSupervisor returns a SupervisorStarter that runs `belai agent
// supervise -run ID` detached, logging beside the workers.
func (r *Registry) StartSupervisor(exe string) SupervisorStarter {
	return func(run CrewRun) (int, error) {
		if err := os.MkdirAll(r.LogDir(), 0o700); err != nil {
			return 0, err
		}
		logFile, err := os.OpenFile(r.RunLogPath(run.ID), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return 0, err
		}
		defer logFile.Close()
		cmd := exec.Command(exe, "agent", "supervise", "-run", run.ID)
		cmd.Dir = run.Scope[0].Path
		cmd.Stdout, cmd.Stderr = logFile, logFile
		return proc.DetachPID(cmd)
	}
}

// StopRun asks a run to stop: its supervisor stops the workers and ends. A
// run whose supervisor is gone has its workers stopped here.
func (r *Registry) StopRun(id, by string, grace time.Duration) error {
	run, err := r.UpdateRun(id, by, func(cr *CrewRun) error {
		if cr.State == RunRunning {
			cr.State = RunStopping
		}
		return nil
	})
	if err != nil {
		return err
	}
	if r.SupervisorAlive(run) {
		return nil
	}
	r.stopRunWorkers(run.ID, grace)
	_, err = r.UpdateRun(id, by, func(cr *CrewRun) error {
		if cr.State.Live() {
			cr.State, cr.Reason, cr.Stopped = RunStopped, "stopped", r.now().UnixMilli()
		}
		return nil
	})
	return err
}

// RunWorkers returns the live workers of a run.
func (r *Registry) RunWorkers(id string) []Record {
	live, _ := r.Live()
	var out []Record
	for _, rec := range live {
		if rec.Run == id {
			out = append(out, rec)
		}
	}
	return out
}

func (r *Registry) stopRunWorkers(id string, grace time.Duration) {
	recs := r.RunWorkers(id)
	done := make(chan struct{}, len(recs))
	for _, rec := range recs {
		go func() { _ = r.Stop(rec, grace); done <- struct{}{} }()
	}
	for range recs {
		<-done
	}
}

// RepoPolicy is the settings and posture a worker in repo runs under: the
// repository root's, never a worktree's.
func RepoPolicy(repo string) (config.Settings, posture.Policy, error) {
	settings, err := config.LoadMerged(repo)
	if err != nil {
		return config.Settings{}, nil, fmt.Errorf("load settings: %w", err)
	}
	projectPol, _ := posture.Load(repo)
	pol := posture.Defaults().Override(projectPol)
	if !settings.GuardrailsEnabled() {
		pol = posture.AllIgnore()
	}
	return settings, pol, nil
}

// CheckScopeDir checks a directory can join a crew run's scope: it is
// trusted, and every member preflights under its own settings. The CLI trusts
// a directory first when asked (-trust-dir); the control room asks the user
// to trust an untrusted one before it calls this.
func CheckScopeDir(dir string, members []agentprofile.Member) error {
	st, err := trustgate.Check(dir)
	if err != nil {
		return fmt.Errorf("check trust for %s: %w", dir, err)
	}
	if !st.Trusted {
		return fmt.Errorf("%s is not a trusted workspace: run `belai` there once to review and trust it", dir)
	}
	settings, pol, err := RepoPolicy(dir)
	if err != nil {
		return fmt.Errorf("%s: %w", dir, err)
	}
	for _, m := range members {
		p, err := agentprofile.Load(m.Profile)
		if err != nil {
			return err
		}
		if err := Preflight(p, settings, pol); err != nil {
			return fmt.Errorf("%s in %s: %w", m.Profile, dir, err)
		}
		if err := CheckRepo(p, dir); err != nil {
			return err
		}
	}
	return nil
}
