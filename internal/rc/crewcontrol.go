package rc

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"time"

	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/fleet"
	"github.com/vulnetix/belai/internal/sessionsync"
)

// A "crew_control" request is the website's crew control room: one change to
// a running crew run, from a closed set of operations, applied the way the TUI
// control room applies it (the run file through fleet.Registry.UpdateRun, a
// worker's control file through SetControl, a stop through StopRun) and
// recorded as a crew.control fact by the web. Every field is untrusted: the
// operation is looked up in a fixed table, every operand is checked for
// shape, and a run is only changed when belai rc runs with --web-controls and
// the run itself was started with them.

// Crew control operations.
const (
	OpScopeWeight  = "scope_weight"
	OpScopePause   = "scope_pause"
	OpScopeAdd     = "scope_add"
	OpScopeRemove  = "scope_remove"
	OpReplicas     = "replicas"
	OpPool         = "pool"
	OpSwitchAfter  = "switch_after"
	OpFinalRetry   = "final_retry"
	OpMaxWorkers   = "max_workers"
	OpStop         = "stop"
	OpWorkerDrain  = "worker_drain"
	OpWorkerStop   = "worker_stop"
	OpWorkerTarget = "worker_target"
	OpWorkerEffort = "worker_effort"
	OpWorkerAuto   = "worker_auto"
)

// CrewControlOps lists every operation, for the docs.
var CrewControlOps = []string{OpScopeWeight, OpScopePause, OpScopeAdd, OpScopeRemove, OpReplicas, OpPool,
	OpSwitchAfter, OpFinalRetry, OpMaxWorkers, OpStop, OpWorkerDrain, OpWorkerStop, OpWorkerTarget, OpWorkerEffort, OpWorkerAuto}

// Crew control limits.
const (
	maxSwitchAfter    = 50
	minFinalRetrySecs = 10
	maxFinalRetrySecs = 24 * 60 * 60
	maxRunWorkers     = 256
)

var (
	runIDShape    = regexp.MustCompile(`^[a-z0-9-]{1,64}$`)
	providerShape = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	modelShape    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@+-]{0,127}$`)
	effortShape   = regexp.MustCompile(`^[a-z]{1,16}$`)
	profileShape  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,63}$`)
)

// crewControlEnv is what applying a crew control needs from the daemon.
type crewControlEnv struct {
	Registry *fleet.Registry
	// Controls is belai rc's --web-controls.
	Controls bool
	// Dirs are the directories the host offers; a directory added to a scope
	// must be one of them.
	Dirs []Dir
	// CheckDir checks a directory can join the run (fleet.CheckScopeDir).
	CheckDir func(dir string, members []agentprofile.Member) error
	// Grace is how long a stopped worker has to hand its card back.
	Grace time.Duration
}

// applyCrewControl applies one website change to a crew run. It returns a
// short report, or an error whose text is the refusal reason.
func applyCrewControl(env crewControlEnv, c *sessionsync.RCCrewControl) (string, error) {
	if c == nil {
		return "", errors.New("the request names no change")
	}
	if !runIDShape.MatchString(c.Run) {
		return "", errors.New("that is not a crew run id")
	}
	if !slices.Contains(CrewControlOps, c.Op) {
		return "", errors.New("this Belai does not know that crew change; update Belai on the host")
	}
	if !env.Controls {
		return "", errors.New("belai rc on this host was not started with --web-controls")
	}
	reg := env.Registry
	run, err := reg.GetRun(c.Run)
	if err != nil {
		return "", errors.New("no such crew run on this host")
	}
	if !run.State.Live() {
		return "", errors.New("that crew run has ended")
	}
	if !run.WebControls {
		return "", errors.New("that crew run was not started with web controls")
	}
	switch c.Op {
	case OpStop:
		if err := reg.StopRun(run.ID, "web", env.Grace); err != nil {
			return "", err
		}
		fleet.RecordWebControl(run.ID, run.Crew, "stop-run", nil)
		return run.ID + " stopping", nil
	case OpWorkerDrain, OpWorkerStop, OpWorkerTarget, OpWorkerEffort, OpWorkerAuto:
		return workerControl(env, run, c)
	}
	return runControl(env, run, c)
}

// runControl changes the run file.
func runControl(env crewControlEnv, run fleet.CrewRun, c *sessionsync.RCCrewControl) (string, error) {
	var action string
	data := map[string]string{}
	var edit func(*fleet.CrewRun) error
	scopeDir := func(cr *fleet.CrewRun) (int, error) {
		i := slices.IndexFunc(cr.Scope, func(d fleet.ScopeDir) bool { return d.Path == c.Dir })
		if i < 0 {
			return 0, errors.New("that directory is not in the crew run's scope")
		}
		return i, nil
	}
	switch c.Op {
	case OpScopeWeight:
		if c.Weight < fleet.MinWeight || c.Weight > fleet.MaxWeight {
			return "", fmt.Errorf("a weight runs %d to %d", fleet.MinWeight, fleet.MaxWeight)
		}
		action, data["weight"] = "weight", strconv.Itoa(c.Weight)
		edit = func(cr *fleet.CrewRun) error {
			i, err := scopeDir(cr)
			if err == nil {
				cr.Scope[i].Weight = c.Weight
			}
			return err
		}
	case OpScopePause:
		action = "resume-dir"
		if c.Paused {
			action = "pause-dir"
		}
		edit = func(cr *fleet.CrewRun) error {
			i, err := scopeDir(cr)
			if err == nil {
				cr.Scope[i].Paused = c.Paused
			}
			return err
		}
	case OpScopeRemove:
		action = "remove-dir"
		edit = func(cr *fleet.CrewRun) error {
			i, err := scopeDir(cr)
			if err != nil {
				return err
			}
			if len(cr.Scope) == 1 {
				return errors.New("a crew run keeps at least one directory; stop the run instead")
			}
			cr.Scope = slices.Delete(cr.Scope, i, i+1)
			return nil
		}
	case OpScopeAdd:
		dir, ok := Allowed(env.Dirs, c.Dir)
		if !ok {
			return "", errors.New("that directory is not one this host offers")
		}
		if env.CheckDir != nil {
			if err := env.CheckDir(dir, run.Members); err != nil {
				// Trust is never granted from the website.
				return "", fmt.Errorf("that directory cannot join the crew: %s", clip(err.Error()))
			}
		}
		weight := c.Weight
		if weight == 0 {
			weight = fleet.MinWeight
		}
		if weight < fleet.MinWeight || weight > fleet.MaxWeight {
			return "", fmt.Errorf("a weight runs %d to %d", fleet.MinWeight, fleet.MaxWeight)
		}
		action = "add-dir"
		edit = func(cr *fleet.CrewRun) error {
			if _, ok := cr.Dir(dir); ok {
				return errors.New("that directory is already in the crew run's scope")
			}
			cr.Scope = append(cr.Scope, fleet.ScopeDir{Path: dir, Weight: weight})
			return nil
		}
	case OpReplicas:
		if !profileShape.MatchString(c.Profile) {
			return "", errors.New("that is not a profile name")
		}
		if c.Replicas < 0 || c.Replicas > agentprofile.MaxReplicas {
			return "", fmt.Errorf("replicas run 0 to %d", agentprofile.MaxReplicas)
		}
		action, data["profile"], data["replicas"] = "replicas", c.Profile, strconv.Itoa(c.Replicas)
		edit = func(cr *fleet.CrewRun) error {
			i := slices.IndexFunc(cr.Members, func(m agentprofile.Member) bool { return m.Profile == c.Profile })
			if i < 0 {
				return errors.New("that profile is not a member of the crew")
			}
			cr.Members[i].Replicas = c.Replicas
			return nil
		}
	case OpPool:
		if len(c.Pool) > fleet.MaxPool {
			return "", fmt.Errorf("a pool holds at most %d targets", fleet.MaxPool)
		}
		pool := make([]fleet.Target, 0, len(c.Pool))
		for _, t := range c.Pool {
			ft, err := checkTarget(&t)
			if err != nil {
				return "", err
			}
			pool = append(pool, ft)
		}
		action = "pool"
		edit = func(cr *fleet.CrewRun) error { cr.Pool = pool; return nil }
	case OpSwitchAfter:
		if c.SwitchAfter < 1 || c.SwitchAfter > maxSwitchAfter {
			return "", fmt.Errorf("the switch streak runs 1 to %d", maxSwitchAfter)
		}
		action, data["retries"] = "switch_after", strconv.Itoa(c.SwitchAfter)
		edit = func(cr *fleet.CrewRun) error { cr.SwitchAfter = c.SwitchAfter; return nil }
	case OpFinalRetry:
		if c.FinalRetrySecs < minFinalRetrySecs || c.FinalRetrySecs > maxFinalRetrySecs {
			return "", fmt.Errorf("the final retry sleep runs %d to %d seconds", minFinalRetrySecs, maxFinalRetrySecs)
		}
		action = "final_retry"
		edit = func(cr *fleet.CrewRun) error { cr.FinalRetrySecs = c.FinalRetrySecs; return nil }
	case OpMaxWorkers:
		if c.MaxWorkers < 0 || c.MaxWorkers > maxRunWorkers {
			return "", fmt.Errorf("max workers runs 0 (the host's cap) to %d", maxRunWorkers)
		}
		action = "max-workers"
		edit = func(cr *fleet.CrewRun) error { cr.MaxWorkers = c.MaxWorkers; return nil }
	default:
		return "", errors.New("this Belai does not know that crew change; update Belai on the host")
	}
	up, err := env.Registry.UpdateRun(run.ID, "web", func(cr *fleet.CrewRun) error {
		if !cr.State.Live() {
			return errors.New("that crew run has ended")
		}
		return edit(cr)
	})
	if err != nil {
		return "", errors.New(clip(err.Error()))
	}
	if len(data) == 0 {
		data = nil
	}
	fleet.RecordWebControl(up.ID, up.Crew, action, data)
	return fmt.Sprintf("%s saved (edit %d); it applies at the next claim", action, up.Seq), nil
}

// workerControl writes one worker's control file, or stops it.
func workerControl(env crewControlEnv, run fleet.CrewRun, c *sessionsync.RCCrewControl) (string, error) {
	if !fleet.ValidID(c.Worker) {
		return "", errors.New("that is not a worker id")
	}
	rec, err := env.Registry.Get(c.Worker)
	if err != nil || rec.Run != run.ID || !rec.State.Live() {
		return "", errors.New("that worker is not running in this crew run")
	}
	data := map[string]string{"worker": rec.ID}
	if c.Op == OpWorkerStop {
		go func() { _ = env.Registry.Stop(rec, env.Grace) }()
		fleet.RecordWebControl(run.ID, run.Crew, "stop-worker", data)
		return rec.ID + " stopping", nil
	}
	var action string
	var fn func(*fleet.Control)
	switch c.Op {
	case OpWorkerDrain:
		action, fn = "drain", func(ct *fleet.Control) { ct.Drain = true }
	case OpWorkerTarget:
		t, err := checkTarget(c.Target)
		if err != nil {
			return "", err
		}
		action, data["target"] = "model", t.String()
		fn = func(ct *fleet.Control) { ct.Target, ct.Auto = t, false }
	case OpWorkerEffort:
		if !effortShape.MatchString(c.Effort) {
			return "", errors.New("that is not an effort level")
		}
		action, fn = "effort", func(ct *fleet.Control) { ct.Effort = c.Effort }
	case OpWorkerAuto:
		action, fn = "auto", func(ct *fleet.Control) { ct.Target, ct.Effort, ct.Auto = fleet.Target{}, "", true }
	}
	ct, err := env.Registry.SetControl(rec.ID, "web", fn)
	if err != nil {
		return "", errors.New(clip(err.Error()))
	}
	fleet.RecordWebControl(run.ID, run.Crew, action, data)
	return fmt.Sprintf("%s: control %d written to %s; it applies before the next round", action, ct.Seq, rec.ID), nil
}

// checkTarget checks a pool or model target's shape. Whether the host can
// run it is the worker's to say: a model it cannot resolve is refused there
// and acknowledged as such.
func checkTarget(t *sessionsync.RCTarget) (fleet.Target, error) {
	if t == nil || !modelShape.MatchString(t.Model) || (t.Provider != "" && !providerShape.MatchString(t.Provider)) {
		return fleet.Target{}, errors.New("that is not a provider and model")
	}
	return fleet.Target{Provider: t.Provider, Model: t.Model}, nil
}
