package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/credentials"
	"github.com/vulnetix/belai/internal/fleet"
	"github.com/vulnetix/belai/internal/gitinfo"
	"github.com/vulnetix/belai/internal/headless"
	"github.com/vulnetix/belai/internal/httpclient"
	"github.com/vulnetix/belai/internal/kanban"
	"github.com/vulnetix/belai/internal/mcp"
	"github.com/vulnetix/belai/internal/notify"
	"github.com/vulnetix/belai/internal/posture"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/sandbox"
	"github.com/vulnetix/belai/internal/session"
	"github.com/vulnetix/belai/internal/sessionctl"
	"github.com/vulnetix/belai/internal/trustgate"
)

const agentUsage = `usage: belai agent <command> [flags] [args]

  list [-json]                   agent profiles, with their mode
  show NAME                      one profile as JSON
  validate FILE                  check a .json or .md profile
  import [-force] FILE           validate a .json or .md profile and save it
  draft [-json] [-o FILE] PREMISE  draft a profile from a premise as markdown
                                 (every offer taken; -json: the offers and why)
  crews                          crews and their members
  crew import [-force] FILE | export NAME | delete NAME
                                 a crew as the JSON the library keeps
  files NAME                     the files an agent carries, and where each is found
  files add NAME FILE [-as PATH] attach a file to an agent (kept in the agent's
                                 own files and listed in its knowledge.paths)
  files rm NAME PATH             detach a file
  files adopt DIR [-dry-run]     attach each DIR/<agent>.md to the agent of that name
  memory NAME [-clear]           a worker's lessons
  knowledge [-index] [-json] [NAME]  the retrieval indexes: this project's
                                 .vulnetix output and NAME's listed documents;
                                 -index brings them up to date first
  run [flags] NAME               run a worker in the foreground
      -once                      work one item (or find none) and exit
      -item K-xxxxxx             work this item
      -trust-dir                 trust the repository first
      -provider P -model M       override the model
      -stay                      keep waiting for work instead of exiting
                                 once nothing is left to claim
      -drain                     exit once nothing is left to claim even
                                 with a cron schedule (not with -stay)
  start [flags] NAME | -crew C   start detached workers; prints their ids
      -replicas N                workers of NAME (default 1)
      -trust-dir, -provider, -model, -stay, -drain as for run
  ps [-all] [-json]              running workers (-all: recently stopped too)
  logs [-f] [-n N] ID            a worker's log
  pause ID|NAME                  finish the card in hand, then claim nothing
  resume ID|NAME                 take cards again
  stop ID|NAME | -all            stop workers; their items go back to the board
  status                         workers and this project's board

A worker claims kanban items that match its profile, works each as a goal in
its own git worktree, and moves it on. When nothing is left to claim and no
teammate in its crew is working, it exits and frees its slot (unless -stay or
a cron schedule keeps it). See docs/fleet.md.
`

// runAgentCLI implements `belai agent …` and returns the exit code.
func runAgentCLI(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "-help" || args[0] == "help" || args[0] == "--help" {
		fmt.Fprint(stderr, agentUsage)
		if len(args) == 0 {
			return 2
		}
		return 0
	}
	cmd, rest := args[0], args[1:]
	code, err := agentCommand(ctx, cmd, rest, stdin, stdout, stderr)
	if err != nil {
		fmt.Fprintln(stderr, "belai:", err)
		if code == 0 {
			code = 1
		}
	}
	return code
}

func agentCommand(ctx context.Context, cmd string, rest []string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	fs := flag.NewFlagSet("agent "+cmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "print JSON")
	switch cmd {
	case "list":
		if err := fs.Parse(rest); err != nil {
			return 2, nil
		}
		profiles, err := agentprofile.List()
		if err != nil {
			return 1, err
		}
		if *asJSON {
			return 0, jsonOut(stdout, profiles)
		}
		tw := tabwriter.NewWriter(stdout, 0, 2, 2, ' ', 0)
		fmt.Fprintln(tw, "NAME\tMODE\tDESCRIPTION")
		for _, p := range profiles {
			fmt.Fprintf(tw, "%s\t%s\t%s\n", p.Name, p.Mode, truncate(p.Description, 70))
		}
		return 0, tw.Flush()

	case "show":
		if err := parseInterleaved(fs, rest); err != nil || fs.NArg() != 1 {
			return 2, errors.New("usage: belai agent show NAME")
		}
		p, err := agentprofile.Load(fs.Arg(0))
		if err != nil {
			return 1, err
		}
		return 0, jsonOut(stdout, p)

	case "validate", "import":
		force := fs.Bool("force", false, "replace an existing profile of the same name")
		if err := parseInterleaved(fs, rest); err != nil || fs.NArg() != 1 {
			return 2, fmt.Errorf("usage: belai agent %s FILE", cmd)
		}
		data, err := os.ReadFile(fs.Arg(0))
		if err != nil {
			return 1, err
		}
		p, err := agentprofile.ParseFile(fs.Arg(0), data)
		if err != nil {
			return 1, fmt.Errorf("%s: %w", fs.Arg(0), err)
		}
		for _, w := range p.FactWarnings() {
			fmt.Fprintf(stderr, "%s: warning: %s\n", fs.Arg(0), w)
		}
		if cmd == "validate" {
			fmt.Fprintf(stdout, "%s: valid %s profile %q\n", fs.Arg(0), p.Mode, p.Name)
			return 0, nil
		}
		if _, err := agentprofile.Load(p.Name); err == nil && !*force {
			return 1, fmt.Errorf("a profile named %q exists; pass -force to replace it", p.Name)
		}
		path, err := agentprofile.Save(p)
		if err != nil {
			return 1, err
		}
		fmt.Fprintf(stdout, "saved %s\n", path)
		return 0, nil

	case "draft":
		return agentDraft(ctx, fs, rest, stdout, stderr, asJSON)

	case "crews":
		if err := fs.Parse(rest); err != nil {
			return 2, nil
		}
		crews := agentprofile.ListCrews()
		if *asJSON {
			return 0, jsonOut(stdout, crews)
		}
		for _, c := range crews {
			var members []string
			for _, m := range c.Members {
				members = append(members, fmt.Sprintf("%s ×%d", m.Profile, m.Count()))
			}
			fmt.Fprintf(stdout, "%s  %s\n    %s\n", c.Name, c.Description, strings.Join(members, ", "))
		}
		return 0, nil

	case "knowledge":
		return agentKnowledge(ctx, fs, rest, stdout, stderr, asJSON)

	case "files":
		return agentFiles(ctx, fs, rest, stdout, asJSON)

	case "crew":
		return agentCrew(fs, rest, stdout)

	case "memory":
		clear := fs.Bool("clear", false, "delete the lessons")
		if err := parseInterleaved(fs, rest); err != nil || fs.NArg() != 1 {
			return 2, errors.New("usage: belai agent memory NAME [-clear]")
		}
		if *clear {
			return 0, fleet.ClearMemory(fs.Arg(0))
		}
		text := fleet.ReadMemory(fs.Arg(0))
		if text == "" {
			fmt.Fprintln(stdout, "no lessons yet")
			return 0, nil
		}
		fmt.Fprint(stdout, cleanLog(text))
		return 0, nil

	case "run":
		return agentRun(ctx, fs, rest, stdout, stderr)
	case "start":
		return agentStart(ctx, fs, rest, stdout, stderr)
	case "ps":
		all := fs.Bool("all", false, "include stopped workers")
		if err := fs.Parse(rest); err != nil {
			return 2, nil
		}
		return agentPS(stdout, *all, *asJSON)
	case "logs":
		follow := fs.Bool("f", false, "follow")
		lines := fs.Int("n", 40, "lines from the end")
		if err := parseInterleaved(fs, rest); err != nil || fs.NArg() != 1 {
			return 2, errors.New("usage: belai agent logs [-f] [-n N] ID")
		}
		return agentLogs(ctx, fs.Arg(0), *lines, *follow, stdout)
	case "stop":
		all := fs.Bool("all", false, "stop every worker")
		if err := parseInterleaved(fs, rest); err != nil || (fs.NArg() != 1 && !*all) {
			return 2, errors.New("usage: belai agent stop ID|NAME | -all")
		}
		return agentStop(fs.Arg(0), *all, stdout)
	case "pause", "resume":
		if err := parseInterleaved(fs, rest); err != nil || fs.NArg() != 1 {
			return 2, errors.New("usage: belai agent " + cmd + " ID|NAME")
		}
		return agentPause(fs.Arg(0), cmd == "pause", stdout)
	case "status":
		if err := fs.Parse(rest); err != nil {
			return 2, nil
		}
		if code, err := agentPS(stdout, false, false); err != nil {
			return code, err
		}
		wd, _ := os.Getwd()
		store, err := kanban.OpenDefault()
		if err != nil {
			return 1, err
		}
		name, key := kanban.ProjectFor(wd)
		counts, err := store.Counts(key)
		if err != nil {
			return 1, err
		}
		fmt.Fprintf(stdout, "\nboard (%s): backlog %d · review %d · in_progress %d · blocked %d · done %d\n",
			name, counts[kanban.Backlog], counts[kanban.Review], counts[kanban.InProgress], counts[kanban.Blocked], counts[kanban.Done])
		return 0, nil
	}
	fmt.Fprint(stderr, agentUsage)
	return 2, nil
}

// repoRoot is the repository a worker started in dir belongs to.
func repoRoot(dir string) string {
	if info, ok := gitinfo.Detect(dir); ok && info.Root != "" {
		return info.Root
	}
	return dir
}

// trustRepo fails closed on an untrusted repository unless -trust-dir.
func trustRepo(repo string, grant bool) error {
	st, err := trustgate.Check(repo)
	if err != nil {
		return fmt.Errorf("check trust for %s: %w", repo, err)
	}
	if st.Trusted {
		return nil
	}
	if !grant {
		return fmt.Errorf("%s is not a trusted workspace: run `belai` there once to review and trust it, or pass -trust-dir", repo)
	}
	return trustgate.Grant(repo, nil)
}

// repoPolicy is the settings and posture a worker in repo runs under: the
// repository root's, never a worktree's.
func repoPolicy(repo string) (config.Settings, posture.Policy, error) {
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

func agentRun(ctx context.Context, fs *flag.FlagSet, rest []string, stdout, stderr io.Writer) (int, error) {
	once := fs.Bool("once", false, "work one item (or find none) and exit")
	item := fs.String("item", "", "work this item")
	trust := fs.Bool("trust-dir", false, "trust the repository first")
	providerName := fs.String("provider", "", "provider override")
	model := fs.String("model", "", "model override")
	id := fs.String("id", "", "worker id (set by `agent start`)")
	crew := fs.String("crew", "", "the crew this worker belongs to (set by `agent start`)")
	detached := fs.Bool("detached", false, "started by `agent start`")
	stay := fs.Bool("stay", false, "keep waiting for work instead of exiting once nothing is left to claim")
	drain := fs.Bool("drain", false, "exit once nothing is left to claim even when the profile has a cron schedule")
	maxWorkers := fs.Int("max-workers", 0, "worker cap to reserve under in place of agents.max_workers (set by `agent start`)")
	webControls := fs.Bool("web-controls", false, "take session controls from the website (set by `agent start`)")
	allowOff := fs.Bool("web-allow-guardrails-off", false, "with -web-controls, guardrails may be turned off from the website (set by `agent start`)")
	if err := parseInterleaved(fs, rest); err != nil || fs.NArg() != 1 {
		return 2, errors.New("usage: belai agent run [-once] [-item K-xxxxxx] [-stay | -drain] NAME")
	}
	if *stay && *drain {
		return 2, errors.New("-stay and -drain contradict each other")
	}
	name := fs.Arg(0)
	wd, _ := os.Getwd()
	repo := repoRoot(wd)
	if err := trustRepo(repo, *trust); err != nil {
		return 1, err
	}
	settings, pol, err := repoPolicy(repo)
	if err != nil {
		return 1, err
	}
	profile, err := agentprofile.Load(name)
	if err != nil {
		return 1, err
	}
	if err := fleet.Preflight(profile, settings, pol); err != nil {
		return 1, err
	}
	if err := fleet.CheckRepo(profile, repo); err != nil {
		return 1, err
	}
	if err := run.PreloadClassifier(run.ResolveSecurityClassifier(settings.Classifier)); err != nil {
		return 1, fmt.Errorf("load embedded classifier: %w", err)
	}
	resolver, err := newResolver(repo)
	if err != nil {
		return 1, err
	}
	wantProvider, wantModel := workerModel(*providerName, *model, settings, config.LoadState)
	cfg, err := run.ResolveWithSource(wantModel, wantProvider, os.Getenv, resolver)
	if err != nil {
		return 1, err
	}
	if *model == "" && *providerName == "" {
		if cfg, err = run.ApplyProfileOverride(cfg, run.ProfileOverride{Provider: profile.Provider, Model: profile.Model, Effort: profile.Effort}, settings.Classifier, resolver); err != nil {
			return 1, err
		}
	}
	if cfg, err = withClassifier(cfg, settings, resolver); err != nil {
		return 1, err
	}
	store, err := kanban.OpenDefault()
	if err != nil {
		return 1, err
	}
	reg, err := fleet.OpenRegistry(store)
	if err != nil {
		return 1, err
	}
	sessions, err := session.NewStore()
	if err != nil {
		return 1, err
	}
	workerID := *id
	if workerID == "" {
		workerID = fleet.NewID(profile.Name)
	}
	if !fleet.ValidID(workerID) {
		return 2, fmt.Errorf("invalid worker id %q", workerID)
	}

	// MCP servers start only for a profile that names their tools: an MCP
	// tool runs outside the worker's worktree.
	var mcpMgr *mcp.Manager
	for _, t := range profile.Tools {
		if strings.HasPrefix(t, "mcp__") {
			mcpMgr = mcp.StartAsync(ctx, settings.MCP, mcp.Options{
				Workdir: repo, HTTPClient: httpclient.Default(),
				VulnetixAuth: func() (string, error) { return credentials.VulnetixAuthHeader(repo) },
				Sandbox:      func() sandbox.Policy { return sandbox.FromSettings(settings.Sandbox, []string{repo}, pol) },
			})
			defer mcpMgr.Close()
			break
		}
	}
	stopTelemetry := startTelemetry(settings, repo)
	defer stopTelemetry()
	defer recordUsage(workerID, settings, nil)()

	logw := stdout
	w := &fleet.Worker{
		Profile: profile, Repo: repo, Settings: settings, Posture: pol,
		Cfg: cfg, Client: httpclient.Default(), Store: store, Registry: reg, MCP: mcpMgr,
		Sessions: sessions, Sync: headless.SyncClient(settings, repo), RemotePrompts: settings.SyncRemotePromptsEnabled(),
		Record: fleet.Record{ID: workerID, Profile: profile.Name, Crew: *crew, Detached: *detached, Log: logPath(reg, workerID, *detached)},
		Once:   *once, Item: *item, Stay: *stay, Drain: *drain, MaxWorkers: workerCap(settings, *maxWorkers), Log: logw,
		Notify: workerNotifier(settings, profile.Name, *detached),
	}
	if *webControls {
		// The model, effort, guardrails and caveman of the next turn, from the
		// website; a model is resolved when the control arrives, the way a
		// remote session resolves one (rccmd.go).
		w.Controls = fleet.NewWorkerControls(settings, cfg, rcControlEnv(cfg, *allowOff), func(st sessionctl.State) (run.Config, error) {
			nc, err := run.ResolveWithSource(st.Model, st.Provider, os.Getenv, resolver)
			if err != nil {
				return run.Config{}, err
			}
			return withClassifier(nc, settings, resolver)
		})
	}
	if err := w.Run(ctx); err != nil {
		return 1, err
	}
	return 0, nil
}

func logPath(reg *fleet.Registry, id string, detached bool) string {
	if !detached {
		return ""
	}
	return filepath.Join(reg.LogDir(), id+".log")
}

// workerNotifier sends worker notifications when the user turned them on,
// never through a terminal escape a detached process would write to its log.
func workerNotifier(s config.Settings, profile string, detached bool) func(string) {
	n := s.Notifications
	if n == nil || n.Enabled == nil || !*n.Enabled {
		return nil
	}
	env := notify.SystemEnv()
	backend := env.Resolve(n.Backend)
	if detached {
		backend = env.ResolveDetached(n.Backend)
	}
	if backend == "" {
		return nil
	}
	nt := notify.New(backend, env, os.Stderr)
	wanted := n.Events
	if wanted == nil {
		wanted = append(append([]string{}, notify.DefaultEvents...), notify.EventWorkerBlocked, notify.EventWorkerFailed)
	}
	return func(event string) {
		for _, e := range wanted {
			if e == event {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				_ = nt.Send(ctx, event, profile)
				cancel()
				return
			}
		}
	}
}

// workerCap is the worker cap for one start. An override (belai rc --max)
// replaces the user's agents.max_workers, but a repository that lowered the
// cap still holds it down: a project layer may only tighten.
func workerCap(settings config.Settings, override int) int {
	merged := settings.MaxWorkers()
	if override <= 0 {
		return merged
	}
	user := config.DefaultMaxWorkers
	if g, err := config.LoadGlobal(); err == nil {
		user = g.MaxWorkers()
	}
	if merged < user {
		return min(override, merged)
	}
	return override
}

func agentStart(ctx context.Context, fs *flag.FlagSet, rest []string, stdout, stderr io.Writer) (int, error) {
	crewName := fs.String("crew", "", "start every member of a crew")
	replicas := fs.Int("replicas", 1, "workers of the profile")
	trust := fs.Bool("trust-dir", false, "trust the repository first")
	providerName := fs.String("provider", "", "provider override")
	model := fs.String("model", "", "model override")
	maxWorkers := fs.Int("max-workers", 0, "worker cap for this start in place of agents.max_workers (set by `belai rc --max`)")
	stay := fs.Bool("stay", false, "keep the workers waiting for work instead of exiting once nothing is left to claim")
	drain := fs.Bool("drain", false, "workers exit once nothing is left to claim even when their profile has a cron schedule (set by `belai rc` for a stored schedule)")
	fill := fs.Bool("fill", false, "with -crew, start only the replicas the crew lacks in this repository")
	webControls := fs.Bool("web-controls", false, "the website may change the workers' model, effort, guardrails and caveman (set by `belai rc --web-controls`)")
	allowOff := fs.Bool("web-allow-guardrails-off", false, "with -web-controls, the website may also turn guardrails off")
	if err := parseInterleaved(fs, rest); err != nil || (fs.NArg() != 1) == (*crewName == "") {
		return 2, errors.New("usage: belai agent start NAME [-replicas N] | -crew CREW [-fill]")
	}
	if *stay && *drain {
		return 2, errors.New("-stay and -drain contradict each other")
	}
	if *fill && *crewName == "" {
		return 2, errors.New("-fill needs -crew")
	}
	wd, _ := os.Getwd()
	repo := repoRoot(wd)
	if err := trustRepo(repo, *trust); err != nil {
		return 1, err
	}
	settings, pol, err := repoPolicy(repo)
	if err != nil {
		return 1, err
	}
	type launch struct{ profile, crew string }
	var launches []launch
	var crew agentprofile.Crew
	if *crewName != "" {
		c, err := agentprofile.LoadCrew(*crewName)
		if err != nil {
			return 1, err
		}
		crew = c
		for _, m := range c.Members {
			for range m.Count() {
				launches = append(launches, launch{m.Profile, c.Name})
			}
		}
	} else {
		if *replicas < 1 || *replicas > agentprofile.MaxReplicas {
			return 2, fmt.Errorf("-replicas runs 1 to %d", agentprofile.MaxReplicas)
		}
		for range *replicas {
			launches = append(launches, launch{fs.Arg(0), ""})
		}
	}
	// Check every profile before starting any, so a crew starts whole or not
	// at all.
	for _, l := range launches {
		p, err := agentprofile.Load(l.profile)
		if err != nil {
			return 1, err
		}
		if err := fleet.Preflight(p, settings, pol); err != nil {
			return 1, fmt.Errorf("%s: %w", l.profile, err)
		}
		if err := fleet.CheckRepo(p, repo); err != nil {
			return 1, err
		}
	}
	store, err := kanban.OpenDefault()
	if err != nil {
		return 1, err
	}
	reg, err := fleet.OpenRegistry(store)
	if err != nil {
		return 1, err
	}
	onePerRepo := false
	if c, err := agentprofile.LoadCrew(*crewName); err == nil {
		onePerRepo = c.OnePerRepo
	}
	max := workerCap(settings, *maxWorkers)
	// Each worker reserves its own slot; it must do so under the cap this
	// start was checked against, or an rc --max start fails in every child.
	spawnMax := 0
	if *maxWorkers > 0 {
		spawnMax = max
	}
	exe, err := os.Executable()
	if err != nil {
		return 1, err
	}
	var started []string
	// The one-per-repository check, the worker cap and the spawns are one step
	// under the crew-start lock, so two starts fired together cannot both pass.
	// Filling a crew that is already live is the point of -fill, so it skips the
	// one-per-repository refusal; the cap still applies to what it starts.
	nothing := false
	if err := reg.WithCrewStart(*crewName, repo, onePerRepo && !*fill, func() error {
		live, _ := reg.Live()
		if *fill {
			launches = launches[:0]
			for _, p := range fleet.FillCrew(crew, live, repo) {
				launches = append(launches, launch{p, crew.Name})
			}
			if len(launches) == 0 {
				nothing = true
				return nil
			}
		}
		if len(live)+len(launches) > max {
			return fmt.Errorf("starting %d would run %d workers; the worker cap is %d (agents.max_workers, or --max-workers)", len(launches), len(live)+len(launches), max)
		}
		for _, l := range launches {
			id, err := reg.Spawn(fleet.SpawnOptions{Exe: exe, Repo: repo, Profile: l.profile, Crew: l.crew, Provider: *providerName, Model: *model, Stay: *stay, Drain: *drain, MaxWorkers: spawnMax,
				WebControls: *webControls, GuardrailsOff: *webControls && *allowOff})
			if err != nil {
				return err
			}
			started = append(started, id)
		}
		return nil
	}); err != nil {
		return 1, err
	}
	if nothing {
		fmt.Fprintf(stdout, "%s has every replica it asks for in this repository; nothing to fill\n", crew.Name)
		return 0, nil
	}
	// Wait briefly for each worker to register, so a worker that fails its
	// own start is reported here rather than discovered later.
	reg.WaitStarted(started, 8*time.Second)
	failed := 0
	for _, id := range started {
		rec, err := reg.Get(id)
		switch {
		case err != nil:
			fmt.Fprintf(stdout, "%s  not registered yet; see `belai agent logs %s`\n", id, id)
		case !rec.State.Live():
			failed++
			fmt.Fprintf(stdout, "%s  %s: %s\n", id, rec.State, rec.Reason)
		default:
			fmt.Fprintf(stdout, "%s  %s\n", id, rec.State)
		}
	}
	if failed > 0 {
		return 1, fmt.Errorf("%d of %d workers failed to start", failed, len(started))
	}
	return 0, nil
}

func agentPS(stdout io.Writer, all, asJSON bool) (int, error) {
	store, _ := kanban.OpenDefault()
	reg, err := fleet.OpenRegistry(store)
	if err != nil {
		return 1, err
	}
	recs, err := reg.List()
	if err != nil {
		return 1, err
	}
	reg.Prune(7 * 24 * time.Hour)
	var shown []fleet.Record
	for _, r := range recs {
		if all || r.State.Live() {
			shown = append(shown, r)
		}
	}
	if asJSON {
		return 0, jsonOut(stdout, shown)
	}
	if len(shown) == 0 {
		fmt.Fprintln(stdout, "no workers running")
		return 0, nil
	}
	tw := tabwriter.NewWriter(stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tPROFILE\tSTATE\tITEM\tDONE\tFAILED\tBEAT\tREPO")
	now := time.Now()
	for _, r := range shown {
		beat := "-"
		if r.Beat > 0 {
			beat = now.Sub(time.UnixMilli(r.Beat)).Round(time.Second).String()
		}
		item := r.Item
		if item == "" {
			item = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%d\t%s\t%s\n", r.ID, r.Profile, r.State, item, r.Done, r.Failed, beat, r.Repo)
	}
	return 0, tw.Flush()
}

func agentStop(ref string, all bool, stdout io.Writer) (int, error) {
	store, _ := kanban.OpenDefault()
	reg, err := fleet.OpenRegistry(store)
	if err != nil {
		return 1, err
	}
	var recs []fleet.Record
	if all {
		recs, err = reg.Live()
	} else {
		recs, err = reg.Resolve(ref)
	}
	if err != nil {
		return 1, err
	}
	if len(recs) == 0 {
		fmt.Fprintln(stdout, "no workers running")
	}
	for _, r := range recs {
		if err := reg.Stop(r, 20*time.Second); err != nil {
			return 1, err
		}
		fmt.Fprintf(stdout, "%s stopped\n", r.ID)
	}
	return 0, nil
}

func agentLogs(ctx context.Context, ref string, lines int, follow bool, stdout io.Writer) (int, error) {
	store, _ := kanban.OpenDefault()
	reg, err := fleet.OpenRegistry(store)
	if err != nil {
		return 1, err
	}
	recs, err := reg.Resolve(ref)
	if err != nil {
		return 1, err
	}
	rec := recs[0]
	path := filepath.Join(reg.LogDir(), rec.ID+".log")
	f, err := os.Open(path)
	if err != nil {
		return 1, fmt.Errorf("no log for %s (a foreground worker logs to its terminal)", rec.ID)
	}
	defer f.Close()
	var tail []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		tail = append(tail, sc.Text())
		if len(tail) > lines {
			tail = tail[1:]
		}
	}
	for _, l := range tail {
		fmt.Fprintln(stdout, cleanLog(l))
	}
	if !follow {
		return 0, nil
	}
	r := bufio.NewReader(f)
	for ctx.Err() == nil {
		line, err := r.ReadString('\n')
		if line != "" {
			fmt.Fprint(stdout, cleanLog(line))
		}
		if err == io.EOF {
			if cur, gerr := reg.Get(rec.ID); gerr == nil && !cur.State.Live() {
				return 0, nil
			}
			time.Sleep(300 * time.Millisecond)
			continue
		}
		if err != nil {
			return 1, err
		}
	}
	return 0, nil
}

// cleanLog strips escape sequences and control and bidi runes, so a log line
// can never drive the reader's terminal.
func cleanLog(s string) string {
	s = ansi.Strip(s)
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\n' || r == '\t':
			b.WriteRune(r)
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0):
		case r >= 0x202a && r <= 0x202e, r >= 0x2066 && r <= 0x2069, r == 0x200e, r == 0x200f, r == 0x061c:
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

func jsonOut(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// workerModel picks the provider and model a worker runs on when no flag
// names them: the settings file, then the saved TUI selection (state.json) —
// the same order the TUI uses — so a worker runs on the model the user chose,
// not on whichever provider happens to have a key in the environment. A
// profile's own provider/model still overrides this afterwards.
func workerModel(flagProvider, flagModel string, s config.Settings, loadState func() (config.State, error)) (provider, model string) {
	if flagProvider != "" || flagModel != "" {
		return flagProvider, flagModel
	}
	return config.SelectedModel(s, loadState)
}

// agentPause asks the workers a ref names to pause or resume. A paused worker
// finishes the card it holds and claims nothing until resumed.
func agentPause(ref string, pause bool, stdout io.Writer) (int, error) {
	store, _ := kanban.OpenDefault()
	reg, err := fleet.OpenRegistry(store)
	if err != nil {
		return 1, err
	}
	recs, err := reg.Resolve(ref)
	if err != nil {
		return 1, err
	}
	n := 0
	for _, r := range recs {
		if !r.State.Live() {
			continue
		}
		if err := reg.SetPaused(r.ID, pause); err != nil {
			return 1, err
		}
		if pause {
			fmt.Fprintf(stdout, "%s will pause after its current card\n", r.ID)
		} else {
			fmt.Fprintf(stdout, "%s resumed\n", r.ID)
		}
		n++
	}
	if n == 0 {
		fmt.Fprintln(stdout, "no workers running")
	}
	return 0, nil
}
