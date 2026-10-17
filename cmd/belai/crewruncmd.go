package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/fleet"
	"github.com/vulnetix/belai/internal/kanban"
)

// listFlag is a repeatable string flag.
type listFlag []string

func (l *listFlag) String() string     { return strings.Join(*l, ",") }
func (l *listFlag) Set(s string) error { *l = append(*l, s); return nil }

// crewLaunch is what `agent start -crew` asks for: the same settings the
// control room edits on a running crew.
type crewLaunch struct {
	crew                    string
	dirs                    []string
	trust                   bool
	provider, model         string
	effort                  string
	pool                    []string
	switchAfter, finalRetry int
	maxWorkers              int
	stay, fill, webCtl      bool
	stdout                  io.Writer
	startSupervisor         fleet.SupervisorStarter // nil starts `belai agent supervise`
	registry                *fleet.Registry         // nil opens the default
	checkDir                func(dir string, members []agentprofile.Member) error
	waitForWorkers          time.Duration
}

// parseTarget reads "provider/model" or a bare model.
func parseTarget(s string) fleet.Target {
	if p, m, ok := strings.Cut(s, "/"); ok {
		return fleet.Target{Provider: p, Model: m}
	}
	return fleet.Target{Model: s}
}

// startCrewRun launches a crew over its scope, or adds directories to the run
// already going: one crew runs once, over every directory it was given.
func startCrewRun(l crewLaunch) (int, error) {
	c, err := agentprofile.LoadCrew(l.crew)
	if err != nil {
		return 1, err
	}
	var scope []fleet.ScopeDir
	if len(l.dirs) == 0 {
		wd, _ := os.Getwd()
		l.dirs = []string{wd}
	}
	for _, d := range l.dirs {
		sd, err := fleet.ParseScopeDir(d)
		if err != nil {
			return 2, fmt.Errorf("-dir %s: %w", d, err)
		}
		sd.Path = repoRoot(sd.Path)
		if err := trustRepo(sd.Path, l.trust); err != nil {
			return 1, err
		}
		check := l.checkDir
		if check == nil {
			check = checkCrewDir
		}
		if err := check(sd.Path, c.Members); err != nil {
			return 1, err
		}
		scope = append(scope, sd)
	}
	reg := l.registry
	if reg == nil {
		store, err := kanban.OpenDefault()
		if err != nil {
			return 1, err
		}
		if reg, err = fleet.OpenRegistry(store); err != nil {
			return 1, err
		}
	}
	if live, ok := reg.LiveRun(c.Name); ok {
		return joinCrewRun(reg, live, scope, l)
	}
	run := fleet.NewCrewRun(c, scope)
	if l.model != "" {
		// -model is the pool's first target, where the workers start.
		run.Pool = append(run.Pool, fleet.Target{Provider: l.provider, Model: l.model})
	}
	run.Effort, run.MaxWorkers, run.Stay, run.WebControls = l.effort, l.maxWorkers, l.stay, l.webCtl
	run.SwitchAfter, run.FinalRetrySecs = l.switchAfter, l.finalRetry
	for _, t := range l.pool {
		run.Pool = append(run.Pool, parseTarget(t))
	}
	start := l.startSupervisor
	if start == nil {
		exe, err := os.Executable()
		if err != nil {
			return 1, err
		}
		start = reg.StartSupervisor(exe)
	}
	run, err = reg.LaunchCrew(run, start)
	if err != nil {
		return 1, err
	}
	fmt.Fprintf(l.stdout, "%s  crew %s over %d %s; open the control room with /crew in belai, or `belai agent runs`\n",
		run.ID, run.Crew, len(run.Scope), plural(len(run.Scope), "directory", "directories"))
	for _, d := range run.Scope {
		fmt.Fprintf(l.stdout, "    %s (weight %d)\n", d.Path, d.Weight)
	}
	if l.waitForWorkers > 0 {
		deadline := time.Now().Add(l.waitForWorkers)
		for time.Now().Before(deadline) && len(reg.RunWorkers(run.ID)) == 0 {
			time.Sleep(200 * time.Millisecond)
		}
		for _, rec := range reg.RunWorkers(run.ID) {
			fmt.Fprintf(l.stdout, "%s  %s in %s\n", rec.ID, rec.Profile, rec.Repo)
		}
	}
	return 0, nil
}

// joinCrewRun adds directories to a crew that already runs. Nothing new to
// add is a refusal, unless -fill asked only to top the crew up (the
// supervisor keeps it full on its own).
func joinCrewRun(reg *fleet.Registry, live fleet.CrewRun, scope []fleet.ScopeDir, l crewLaunch) (int, error) {
	var added []string
	_, err := reg.UpdateRun(live.ID, "launch", func(cr *fleet.CrewRun) error {
		for _, d := range scope {
			if _, ok := cr.Dir(d.Path); !ok {
				cr.Scope = append(cr.Scope, d)
				added = append(added, d.Path)
			} else if i := slices.IndexFunc(cr.Scope, func(s fleet.ScopeDir) bool { return s.Path == d.Path }); i >= 0 && cr.Scope[i].Paused {
				cr.Scope[i].Paused = false
				added = append(added, d.Path+" (resumed)")
			}
		}
		return nil
	})
	if err != nil {
		return 1, err
	}
	switch {
	case len(added) > 0:
		fmt.Fprintf(l.stdout, "%s  crew %s is running; added to its scope: %s\n", live.ID, live.Crew, strings.Join(added, ", "))
		return 0, nil
	case l.fill:
		fmt.Fprintf(l.stdout, "%s  crew %s is running over this scope; its supervisor keeps it full\n", live.ID, live.Crew)
		return 0, nil
	}
	return 1, fmt.Errorf("%w (%s)", fleet.ErrRunLive, live.ID)
}

// checkCrewDir checks every member can work in dir under dir's own settings;
// trustRepo already ran, granting trust when -trust-dir asked for it.
func checkCrewDir(dir string, members []agentprofile.Member) error {
	return fleet.CheckScopeDir(dir, members)
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// agentSupervise runs a crew run's supervisor in the foreground (`agent
// start -crew` starts it detached).
func agentSupervise(ctx context.Context, fs *flag.FlagSet, rest []string, stdout io.Writer) (int, error) {
	runID := fs.String("run", "", "the crew run to supervise")
	if err := parseInterleaved(fs, rest); err != nil || *runID == "" {
		return 2, errors.New("usage: belai agent supervise -run ID")
	}
	store, err := kanban.OpenDefault()
	if err != nil {
		return 1, err
	}
	reg, err := fleet.OpenRegistry(store)
	if err != nil {
		return 1, err
	}
	run, err := reg.GetRun(*runID)
	if err != nil {
		return 1, err
	}
	settings, _, err := repoPolicy(run.Scope[0].Path)
	if err != nil {
		return 1, err
	}
	exe, err := os.Executable()
	if err != nil {
		return 1, err
	}
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	s := &fleet.Supervisor{Registry: reg, Store: store, RunID: run.ID, Exe: exe, Max: workerCap(settings, 0), Log: stdout}
	if err := s.Run(ctx); err != nil {
		return 1, err
	}
	return 0, nil
}

// agentRuns lists crew runs.
func agentRuns(stdout io.Writer, all, asJSON bool) (int, error) {
	store, _ := kanban.OpenDefault()
	reg, err := fleet.OpenRegistry(store)
	if err != nil {
		return 1, err
	}
	runs, err := reg.ListRuns()
	if err != nil {
		return 1, err
	}
	var shown []fleet.CrewRun
	for _, r := range runs {
		if all || r.State.Live() {
			shown = append(shown, r)
		}
	}
	if asJSON {
		return 0, jsonOut(stdout, shown)
	}
	if len(shown) == 0 {
		fmt.Fprintln(stdout, "no crews running")
		return 0, nil
	}
	tw := tabwriter.NewWriter(stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "RUN\tCREW\tSTATE\tWORKERS\tSCOPE\tPOOL")
	for _, r := range shown {
		var dirs []string
		for _, d := range r.Scope {
			s := fmt.Sprintf("%s:%d", d.Path, d.Weight)
			if d.Paused {
				s += " (paused)"
			}
			dirs = append(dirs, s)
		}
		var pool []string
		for _, t := range r.Pool {
			pool = append(pool, t.String())
		}
		poolText := strings.Join(pool, " ")
		if poolText == "" {
			poolText = "profiles'"
		}
		if r.PoolPhase != fleet.PoolHealthy {
			poolText += " (" + r.PoolPhase + ")"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d/%d\t%s\t%s\n", r.ID, r.Crew, r.State, len(reg.RunWorkers(r.ID)), r.Replicas(), strings.Join(dirs, ", "), poolText)
	}
	return 0, tw.Flush()
}

// agentControl changes running workers from the command line: the same
// controls the control room writes.
func agentControl(fs *flag.FlagSet, rest []string, stdout io.Writer) (int, error) {
	model := fs.String("model", "", "the model the next turn runs on (provider/model or model)")
	effort := fs.String("effort", "", "the effort of the next turn")
	drain := fs.Bool("drain", false, "finish the card in hand, then stop")
	auto := fs.Bool("auto", false, "hand the model back to the crew's pool")
	const usage = "usage: belai agent control [-model P/M] [-effort E] [-drain] [-auto] ID|NAME|RUN"
	if err := parseInterleaved(fs, rest); err != nil || fs.NArg() != 1 || (*model == "" && *effort == "" && !*drain && !*auto) {
		return 2, errors.New(usage)
	}
	store, _ := kanban.OpenDefault()
	reg, err := fleet.OpenRegistry(store)
	if err != nil {
		return 1, err
	}
	set := func(c *fleet.Control) {
		if *model != "" {
			c.Target = parseTarget(*model)
		}
		c.Effort, c.Drain, c.Auto = *effort, c.Drain || *drain, *auto
	}
	ref := fs.Arg(0)
	action := controlAction(*model, *effort, *drain, *auto)
	if strings.HasPrefix(ref, "run-") {
		ids, err := reg.ControlRun(ref, "", "human", set)
		if run, gerr := reg.GetRun(ref); gerr == nil {
			fleet.RecordControl(run.ID, run.Crew, action, "", map[string]string{"target": *model})
		}
		for _, id := range ids {
			fmt.Fprintf(stdout, "%s  control written\n", id)
		}
		return boolCode(err), err
	}
	recs, err := reg.Resolve(ref)
	if err != nil {
		return 1, err
	}
	for _, r := range recs {
		if !r.State.Live() {
			continue
		}
		c, err := reg.SetControl(r.ID, "human", set)
		if err != nil {
			return 1, err
		}
		fleet.RecordControl(r.Run, r.Crew, action, "", map[string]string{"worker": r.ID, "target": *model})
		fmt.Fprintf(stdout, "%s  control %d written; it applies before the next round\n", r.ID, c.Seq)
	}
	return 0, nil
}

// agentStopRun stops a crew run and its workers.
func agentStopRun(id string, stdout io.Writer) (int, error) {
	store, _ := kanban.OpenDefault()
	reg, err := fleet.OpenRegistry(store)
	if err != nil {
		return 1, err
	}
	if err := reg.StopRun(id, "human", agentStopGrace); err != nil {
		return 1, err
	}
	if run, err := reg.GetRun(id); err == nil {
		fleet.RecordControl(run.ID, run.Crew, "stop", "", nil)
	}
	fmt.Fprintf(stdout, "%s stopping; its workers finish or hand back their cards\n", id)
	return 0, nil
}

func boolCode(err error) int {
	if err != nil {
		return 1
	}
	return 0
}

// controlAction names a control for the crew log: a fixed word.
func controlAction(model, effort string, drain, auto bool) string {
	switch {
	case drain:
		return "drain"
	case model != "":
		return "model"
	case effort != "":
		return "effort"
	case auto:
		return "auto"
	}
	return "control"
}
