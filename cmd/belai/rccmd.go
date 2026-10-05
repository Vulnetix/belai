package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/vulnetix/belai/internal/agent"
	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/audit"
	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/credentials"
	"github.com/vulnetix/belai/internal/fleet"
	"github.com/vulnetix/belai/internal/forge"
	"github.com/vulnetix/belai/internal/gitsync"
	"github.com/vulnetix/belai/internal/headless"
	"github.com/vulnetix/belai/internal/httpclient"
	"github.com/vulnetix/belai/internal/kanban"
	"github.com/vulnetix/belai/internal/mcp"
	"github.com/vulnetix/belai/internal/models"
	"github.com/vulnetix/belai/internal/modes"
	"github.com/vulnetix/belai/internal/netguard"
	"github.com/vulnetix/belai/internal/posture"
	"github.com/vulnetix/belai/internal/proc"
	"github.com/vulnetix/belai/internal/profiles"
	"github.com/vulnetix/belai/internal/rc"
	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/sandbox"
	"github.com/vulnetix/belai/internal/schedule"
	"github.com/vulnetix/belai/internal/session"
	"github.com/vulnetix/belai/internal/sessionctl"
	"github.com/vulnetix/belai/internal/sessionsync"
	"github.com/vulnetix/belai/internal/testpass"
	"github.com/vulnetix/belai/internal/tools"
	"github.com/vulnetix/belai/internal/trustgate"
	"github.com/vulnetix/belai/internal/turnlog"
	"github.com/vulnetix/belai/internal/version"
)

// dirList is a repeatable --dir flag.
type dirList []string

func (d *dirList) String() string     { return strings.Join(*d, ",") }
func (d *dirList) Set(v string) error { *d = append(*d, v); return nil }

// cidrList is a repeatable --allow-private-cidr flag.
type cidrList []string

func (c *cidrList) String() string     { return strings.Join(*c, ",") }
func (c *cidrList) Set(v string) error { *c = append(*c, v); return nil }

const rcUsage = `usage: belai rc [--dir PATH]... [--max N] [--max-workers N] [--idle DURATION] [--detach]
                [--web-controls [--web-allow-guardrails-off]] [--web-project-settings]
                [--web-shell] [--allow-private-cidr CIDR]...
       belai rc --status
       belai rc --stop

Runs Belai remote control: this machine appears under Belai → Sessions on the
Vulnetix website, which can start headless Belai sessions here, follow them
live and prompt them. Sessions run only in projects already trusted on this
machine and in the directories given with --dir.

--web-controls lets a web session change its own mode, model and switches
with the TUI's slash commands and keys, for that session only.
--web-project-settings lets the website edit each offered directory's project
preferences on this host.

--web-shell lets a web session run one shell line at a time on this host. A line
runs out of band under the same permission rules and OS sandbox as the TUI's
!cmd, and lands in the session's transcript. The composer's !cmd attaches its
classified output to the model's next turn; the console drawer's never does.

--allow-private-cidr names one private range that WebFetch may reach, for a host
whose own network answers with addresses there (the Pix Sandbox's egress
gateway). Only a private-use range is accepted, never loopback or link-local;
every other private address stays refused. Sessions and workers inherit it.

Needs a Vulnetix CLI browser login (vulnetix auth login).
`

// runRCCLI implements `belai rc`. It returns the exit code.
func runRCCLI(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("rc", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, rcUsage); fs.PrintDefaults() }
	var dirs dirList
	fs.Var(&dirs, "dir", "also offer this directory (repeatable); it is trusted, as with -trust-dir")
	max := fs.Int("max", rc.DefaultMax, "sessions to run at once; when given without --max-workers, also the fleet worker cap in place of agents.max_workers")
	maxWorkers := fs.Int("max-workers", 0, "fleet workers to run at once, in place of agents.max_workers; sessions stay capped by --max")
	idle := fs.Duration("idle", rc.DefaultIdle, "end a session after this long without a prompt")
	detach := fs.Bool("detach", false, "run in the background; logs go to ~/.vulnetix/belai/rc/rc.log")
	status := fs.Bool("status", false, "show whether remote control is running")
	stop := fs.Bool("stop", false, "stop the running remote control and its sessions")
	webControls := fs.Bool("web-controls", false, "let web sessions change their mode, model, guardrails, ask and display with the TUI's slash commands and keys (that session only)")
	webGuardrailsOff := fs.Bool("web-allow-guardrails-off", false, "with --web-controls, let a web session turn its guardrails off")
	webProjectSettings := fs.Bool("web-project-settings", false, "let the website read and edit each offered directory's project preferences on this host")
	webShell := fs.Bool("web-shell", false, "let web sessions run a shell line on this host, under the TUI's `!cmd` permission rules and OS sandbox")
	var allowCIDRs cidrList
	fs.Var(&allowCIDRs, "allow-private-cidr", "let WebFetch reach this private-use range (repeatable); loopback and link-local are never allowed, and every other private address stays refused")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	allowed, err := netguard.ParseAllowCIDRs(strings.Join(allowCIDRs, ","))
	if err != nil {
		fmt.Fprintln(stderr, "belai rc:", err)
		return 2
	}
	if *webGuardrailsOff && !*webControls {
		fmt.Fprintln(stderr, "belai rc: --web-allow-guardrails-off needs --web-controls")
		return 2
	}
	if fs.NArg() > 0 {
		if fs.Arg(0) == "help" {
			fs.Usage()
			return 0
		}
		fmt.Fprintf(stderr, "belai rc: unexpected argument %q\n", fs.Arg(0))
		return 2
	}
	if flagGiven(fs, "max-workers") && *maxWorkers < 1 {
		fmt.Fprintln(stderr, "belai rc: --max-workers must be at least 1")
		return 2
	}
	url := rc.ManageURL(os.Getenv("VULNETIX_WEB_URL"))

	switch {
	case *status:
		r, live := rc.ReadRecord()
		fmt.Fprint(stdout, rcStatusText(r, live, url))
		if !live {
			return 1
		}
		return 0
	case *stop:
		r, err := rc.Stop(15 * time.Second)
		if err != nil {
			fmt.Fprintln(stderr, "belai rc:", err)
			return 1
		}
		fmt.Fprintf(stdout, "stopped belai rc (pid %d)\n", r.PID)
		return 0
	}

	if r, live := rc.ReadRecord(); live {
		fmt.Fprintf(stderr, "belai rc is already running (pid %d). Manage hosts at %s\n", r.PID, url)
		return 1
	}

	for _, d := range dirs {
		p, granted, err := rc.TrustArg(d)
		if err != nil {
			fmt.Fprintf(stderr, "belai rc: --dir %s: %v\n", d, err)
			return 2
		}
		if granted {
			fmt.Fprintf(stderr, "trusted %s (from --dir)\n", p)
		}
	}

	if *detach {
		return rcDetach(args, stdout, stderr, url)
	}

	// The sessions and workers this process starts inherit the environment, so
	// the allow list reaches every process that can run WebFetch.
	if len(allowed) > 0 {
		names := make([]string, len(allowed))
		for i, p := range allowed {
			names[i] = p.String()
		}
		netguard.SetAllowedPrefixes(allowed)
		if err := os.Setenv(netguard.EnvAllowPrivateCIDRs, strings.Join(names, ",")); err != nil {
			fmt.Fprintln(stderr, "belai rc: --allow-private-cidr:", err)
			return 1
		}
		fmt.Fprintf(stderr, "WebFetch may reach %s (from --allow-private-cidr); every other private address stays refused.\n", strings.Join(names, ", "))
	}

	wd, _ := os.Getwd()
	settings, err := config.LoadMerged(wd)
	if err != nil {
		fmt.Fprintln(stderr, "belai rc: load settings:", err)
		return 1
	}
	offered, skipped := rc.Collect(dirs, wd)
	hostID := headless.HostID()
	host := sessionsync.Host{Hostname: sessionsync.Hostname(), OS: runtime.GOOS, BelaiVersion: version.Version}
	client, clientErr := rcClient(wd)

	fmt.Fprintf(stderr, "belai rc · %s\n\n", host.Hostname)
	checks := rc.Preflight(ctx, rc.PreflightOptions{
		Workdir: wd, Settings: settings, Dirs: offered,
		Server: func(ctx context.Context, _ string) error {
			if clientErr != nil {
				return clientErr
			}
			if hostID == "" {
				return errors.New("could not create this host's id under ~/.vulnetix/belai/sync")
			}
			return client.PutHost(ctx, hostID, host)
		},
	})
	fmt.Fprint(stderr, checks.Render())
	fmt.Fprintln(stderr)
	for _, s := range skipped {
		fmt.Fprintf(stderr, "Not offered: %s (its repository %s is not trusted; run `belai` there once, or pass --dir %s)\n",
			s.Dir.Path, s.Root, s.Root)
	}
	if len(skipped) > 0 {
		fmt.Fprintln(stderr)
	}
	if !checks.OK() {
		fmt.Fprintln(stderr, "Remote control is not running. Fix the ✗ lines above and run `belai rc` again.")
		fmt.Fprintf(stderr, "Once it runs, manage this host at %s\n", url)
		return 1
	}
	fmt.Fprintln(stderr, "Offering:")
	for _, d := range offered {
		fmt.Fprintf(stderr, "  %s  (%s)\n", d.Path, d.Source)
	}
	fmt.Fprintf(stderr, "\nManage hosts and start sessions: %s\n", url)
	switch {
	case *webControls && *webGuardrailsOff:
		fmt.Fprintln(stderr, "Web sessions take session controls, including turning guardrails off. Asks go to the web while ask is on.")
	case *webControls:
		fmt.Fprintln(stderr, "Web sessions take session controls (guardrails stay on). Asks go to the web while ask is on.")
	default:
		fmt.Fprintln(stderr, "Sessions run with asks off: the posture and permission rules decide.")
	}
	if *webShell {
		fmt.Fprintln(stderr, "Web sessions may run a shell line on this host, under the TUI's `!cmd` permission rules and OS sandbox.")
	}
	if *webProjectSettings {
		fmt.Fprintln(stderr, "The website may edit the offered directories' project preferences on this host.")
	}
	fmt.Fprintln(stderr, "Ctrl+C stops remote control and its sessions.")
	fmt.Fprintln(stderr)

	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintln(stderr, "belai rc: locate belai:", err)
		return 1
	}
	logPath := ""
	if os.Getenv("BELAI_RC_DETACHED") == "1" {
		logPath = rcLogPath()
	}
	// A profile the website lists needs the same identity every time, so give
	// older profiles their id before the catalogue is first advertised.
	if n, err := agentprofile.EnsureIDs(); err != nil {
		fmt.Fprintln(stderr, "belai rc: could not give every agent profile an id:", err)
	} else if n > 0 {
		fmt.Fprintf(stderr, "Gave %d agent profile(s) an id.\n", n)
	}
	if n, err := agentprofile.EnsureCrewIDs(); err != nil {
		fmt.Fprintln(stderr, "belai rc: could not give every crew an id:", err)
	} else if n > 0 {
		fmt.Fprintf(stderr, "Gave %d crew(s) an id.\n", n)
	}
	schedules, err := schedule.Open()
	if err != nil {
		fmt.Fprintln(stderr, "belai rc: schedules:", err)
		return 1
	}
	d, err := rc.New(rc.Options{
		Exe: exe, Client: client, HostID: hostID, Host: host, Dirs: offered,
		Max: *max, MaxWorkers: workerOverride(fs, *max, *maxWorkers), Idle: *idle, URL: url, Out: stderr, LogPath: logPath,
		Schedules: schedules, DrawAvatar: rcDrawAvatar(wd), Distill: rcDistill(wd), TeleportPush: teleportPushPolicy(wd),
		Controls: *webControls, GuardrailsOff: *webGuardrailsOff, ProjectSettings: *webProjectSettings,
		Shell: *webShell,
	})
	if err != nil {
		fmt.Fprintln(stderr, "belai rc:", err)
		return 1
	}
	if err := d.Run(ctx); err != nil {
		fmt.Fprintln(stderr, "belai rc:", err)
		return 1
	}
	return 0
}

// workerOverride is the fleet worker cap passed to the daemon, or 0 to keep
// agents.max_workers: --max-workers when given, else an explicit --max. The
// session default never lowers the worker cap.
func workerOverride(fs *flag.FlagSet, max, maxWorkers int) int {
	switch {
	case flagGiven(fs, "max-workers"):
		return maxWorkers
	case flagGiven(fs, "max"):
		return max
	}
	return 0
}

// flagGiven reports whether the named flag was set on the command line.
func flagGiven(fs *flag.FlagSet, name string) bool {
	given := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			given = true
		}
	})
	return given
}

// rcClient builds the sync client with the Vulnetix CLI credential, re-read
// every five minutes so a fresh `vulnetix auth login` is picked up.
func rcClient(wd string) (*sessionsync.Client, error) {
	var mu sync.Mutex
	var header string
	var at time.Time
	auth := func() (string, error) {
		mu.Lock()
		defer mu.Unlock()
		if header != "" && time.Since(at) < 5*time.Minute {
			return header, nil
		}
		h, err := credentials.VulnetixAuthHeader(wd)
		if err != nil {
			return "", err
		}
		if err := sessionsync.UsableCredential(h); err != nil {
			return "", err
		}
		header, at = h, time.Now()
		return header, nil
	}
	return sessionsync.NewClient(sessionsync.BaseURL(os.Getenv("VULNETIX_WEB_URL")), auth, httpclient.Default())
}

func rcLogPath() string {
	dir, err := rc.StateDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "rc.log")
}

// rcDetach re-runs `belai rc` without --detach in its own session, logging to
// rc.log, and waits for it to come up or fail.
func rcDetach(args []string, stdout, stderr io.Writer, url string) int {
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintln(stderr, "belai rc:", err)
		return 1
	}
	var rest []string
	for _, a := range args {
		if a == "-detach" || a == "--detach" || a == "-detach=true" || a == "--detach=true" {
			continue
		}
		rest = append(rest, a)
	}
	logPath := rcLogPath()
	if logPath == "" {
		fmt.Fprintln(stderr, "belai rc: no state directory for the log")
		return 1
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		fmt.Fprintln(stderr, "belai rc:", err)
		return 1
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		fmt.Fprintln(stderr, "belai rc:", err)
		return 1
	}
	defer logFile.Close()
	start, _ := logFile.Seek(0, io.SeekEnd)
	cmd := exec.Command(exe, append([]string{"rc"}, rest...)...)
	cmd.Stdout, cmd.Stderr = logFile, logFile
	cmd.Env = append(os.Environ(), "BELAI_RC_DETACHED=1")
	if wd, err := os.Getwd(); err == nil {
		cmd.Dir = wd
	}
	if err := proc.Detach(cmd); err != nil {
		fmt.Fprintln(stderr, "belai rc: start in background:", err)
		return 1
	}
	pid := cmd.Process.Pid
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if r, live := rc.ReadRecord(); live && r.PID == pid {
			fmt.Fprintf(stdout, "belai rc is running in the background (pid %d).\n", pid)
			fmt.Fprintf(stdout, "Manage hosts and start sessions: %s\n", url)
			fmt.Fprintf(stdout, "Log: %s · stop with `belai rc --stop`\n", logPath)
			return 0
		}
		if !proc.Alive(pid) {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	// It exited (a failed preflight) or never came up: show what it said.
	if b, err := os.ReadFile(logPath); err == nil && int64(len(b)) > start {
		fmt.Fprint(stderr, string(b[start:]))
	}
	fmt.Fprintln(stderr, "belai rc did not start; see", logPath)
	return 1
}

// rcStatusText describes the daemon for `belai rc --status`.
func rcStatusText(r rc.Record, live bool, url string) string {
	var b strings.Builder
	if !live {
		fmt.Fprintf(&b, "belai rc is not running. Start it with `belai rc` (or /rc in Belai).\nManage hosts: %s\n", url)
		return b.String()
	}
	state := "connected"
	if !r.Online {
		state = "not connected"
		if r.Error != "" {
			state += " (" + r.Error + ")"
		}
	}
	fmt.Fprintf(&b, "belai rc is running (pid %d, since %s) · %s\n", r.PID, r.Started.Format(time.RFC822), state)
	fmt.Fprintf(&b, "sessions: %d of %d\n", len(r.Sessions), r.Max)
	for _, s := range r.Sessions {
		fmt.Fprintf(&b, "  %s  %s  started %s\n", s.ID[:8], s.Cwd, s.Started.Format("15:04"))
	}
	fmt.Fprintf(&b, "offering %d director%s\n", len(r.Dirs), map[bool]string{true: "y", false: "ies"}[len(r.Dirs) == 1])
	fmt.Fprintf(&b, "manage hosts: %s\n", r.URL)
	if r.LogPath != "" {
		fmt.Fprintf(&b, "log: %s\n", r.LogPath)
	}
	return b.String()
}

// runRCSessionCLI implements the hidden `belai rc-session`: one headless,
// synced session the rc daemon started. Its prompt arrives on stdin.
func runRCSessionCLI(ctx context.Context, args []string, stdin io.Reader, stderr io.Writer) int {
	fs := flag.NewFlagSet("rc-session", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dispatch := fs.String("dispatch", "", "the website request this session answers")
	sessionID := fs.String("session-id", "", "the session id the daemon minted")
	modeFlag := fs.String("mode", "", "agent, plan, goal or code (default: classified)")
	idle := fs.Duration("idle", rc.DefaultIdle, "end after this long without a prompt")
	providerFlag := fs.String("provider", "", "provider for this session (default: the host's own)")
	modelFlag := fs.String("model", "", "model for this session (default: the host's own)")
	effortFlag := fs.String("effort", "", "effort for this session (default: the host's own)")
	gitSyncFlag := fs.String("git-sync", "", "on or off: sync the branch with origin's default branch before turns (default: git.sync in settings)")
	controls := fs.Bool("controls", false, "take session controls from the web (belai rc --web-controls)")
	allowGuardrailsOff := fs.Bool("allow-guardrails-off", false, "with -controls, a web session may turn guardrails off")
	shell := fs.Bool("shell", false, "run shell lines from the web (belai rc --web-shell)")
	profileFlag := fs.String("profile", "", "engage this agent profile for agent-mode turns (a web request's choice, with -mode agent)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *allowGuardrailsOff && !*controls {
		fmt.Fprintln(stderr, "belai rc-session: -allow-guardrails-off needs -controls")
		return 2
	}
	if *sessionID == "" || *dispatch == "" {
		fmt.Fprintln(stderr, "belai rc-session: -session-id and -dispatch are required (the rc daemon starts this)")
		return 2
	}
	mode := modes.Mode(*modeFlag)
	switch mode {
	case "", modes.ModeAgent, modes.ModePlan, modes.ModeGoal, modes.ModeCode:
	default:
		fmt.Fprintf(stderr, "belai rc-session: unknown mode %q\n", *modeFlag)
		return 2
	}
	if *profileFlag != "" {
		if mode != modes.ModeAgent {
			fmt.Fprintln(stderr, "belai rc-session: -profile needs -mode agent")
			return 2
		}
		if !rc.ValidAgentName(*profileFlag) {
			fmt.Fprintf(stderr, "belai rc-session: %q is not an agent profile name\n", *profileFlag)
			return 2
		}
	}
	raw, err := io.ReadAll(io.LimitReader(stdin, 2*sessionsync.MaxPromptBytes))
	if err != nil {
		fmt.Fprintln(stderr, "belai rc-session: read prompt:", err)
		return 1
	}
	prompt := sessionsync.CleanPrompt(string(raw))
	if prompt == "" {
		fmt.Fprintln(stderr, "belai rc-session: empty prompt")
		return 1
	}
	pick := rcModelPick{Provider: *providerFlag, Model: *modelFlag, Effort: *effortFlag, Profile: *profileFlag,
		Controls: *controls, AllowGuardrailsOff: *allowGuardrailsOff, Shell: *shell}
	switch *gitSyncFlag {
	case "":
	case "on", "off":
		on := *gitSyncFlag == "on"
		pick.GitSync = &on
	default:
		fmt.Fprintf(stderr, "belai rc-session: -git-sync must be on or off, not %q\n", *gitSyncFlag)
		return 2
	}
	if err := runRCSession(ctx, *dispatch, *sessionID, mode, prompt, *idle, pick, stderr); err != nil {
		fmt.Fprintln(stderr, "belai rc-session:", err)
		return 1
	}
	return 0
}

// rcModelPick is what a web request chose for the session: provider, model
// and effort (empty means the host's own default) and the git sync switch
// (nil means git.sync in settings).
type rcModelPick struct {
	Provider, Model, Effort string
	// Profile is the agent profile engaged for agent-mode turns; empty is the
	// default agent.
	Profile string
	GitSync *bool
	// Controls and AllowGuardrailsOff are the daemon's --web-controls and
	// --web-allow-guardrails-off, passed on as fixed argv.
	Controls, AllowGuardrailsOff bool
	// Shell is the daemon's --web-shell: the session runs shell lines the
	// website sends (internal/rc shell.go).
	Shell bool
}

func runRCSession(ctx context.Context, dispatch, sessionID string, mode modes.Mode, prompt string, idle time.Duration, pick rcModelPick, stderr io.Writer) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	// The daemon already checked the directory; check again here, where the
	// work happens, and fail closed.
	st, err := trustgate.Check(cwd)
	if err != nil {
		return fmt.Errorf("check trust for %s: %w", cwd, err)
	}
	if !st.Trusted {
		return fmt.Errorf("%s is not trusted on this host", cwd)
	}
	// The resolved settings, the host's per-project preferences included (the
	// project settings page edits those), as a TUI session reads them.
	eff, err := config.Resolve(cwd, os.Getenv, config.Settings{})
	if err != nil {
		return fmt.Errorf("load settings: %w", err)
	}
	settings := eff.Settings
	var startNote string
	if !settings.GuardrailsEnabled() {
		switch {
		case pick.AllowGuardrailsOff:
			// The host said a web session may run without them.
		case eff.Origin["guardrails"] == config.SourceProjectPrefs:
			// A preference (f3 in the TUI, or the project settings page) is
			// not the host's consent for remote sessions: they run with
			// guardrails on.
			on := true
			settings.Guardrails = &on
			startNote = "guardrails are on for this remote session: the project's preference to turn them off applies only where the host allows it"
		default:
			return errors.New("guardrails are off; remote sessions never run without them")
		}
	}
	if err := run.PreloadClassifier(run.ResolveSecurityClassifier(settings.Classifier)); err != nil {
		return fmt.Errorf("load embedded classifier: %w", err)
	}
	projectPol, _ := posture.Load(cwd)
	basePol := posture.Defaults().Override(projectPol)
	pol := basePol
	if !settings.GuardrailsEnabled() {
		pol = posture.AllIgnore()
	}
	resolver, err := newResolver(cwd)
	if err != nil {
		return err
	}
	// The host's own choice, as every other headless entry point resolves it:
	// settings, then the saved TUI selection. Without this an empty provider
	// falls through to the built-in openai default.
	if pick.Effort != "" {
		settings.Effort = pick.Effort
	}
	// A model named on the web is the user's explicit choice for the whole
	// session, so the host's routing table does not second-guess it.
	if pick.Provider != "" || pick.Model != "" {
		settings.Routing = nil
	}
	wantProvider, wantModel := workerModel(pick.Provider, pick.Model, settings, config.LoadState)
	// A model alone belongs to the host's default provider, not to openai.
	if pick.Provider == "" && pick.Model != "" {
		wantProvider, _ = workerModel("", "", settings, config.LoadState)
		wantModel = pick.Model
	}
	cfg, err := run.ResolveWithSource(wantModel, wantProvider, os.Getenv, resolver)
	if err != nil {
		return err
	}
	cfg, err = withClassifier(cfg, settings, resolver)
	if err != nil {
		return err
	}

	mcpMgr := mcp.StartAsync(ctx, settings.MCP, mcp.Options{
		Builtins:     builtinMCP(settings, cwd),
		Workdir:      cwd,
		HTTPClient:   httpclient.Default(),
		VulnetixAuth: func() (string, error) { return credentials.VulnetixAuthHeader(cwd) },
		Sandbox: func() sandbox.Policy {
			return sandbox.FromSettings(settings.Sandbox, []string{cwd}, pol)
		},
	})
	mcp.SetActive(mcpMgr)
	defer mcpMgr.Close()
	defer startTelemetry(settings, cwd)()
	defer flushKanban(settings, cwd)
	defer recordUsage(sessionID, settings, nil)()

	// The transcript: the same JSONL a TUI session keeps, so the session
	// resumes with `belai -r`, and the syncer mirrors it to the website.
	store, err := session.NewStore()
	if err != nil {
		return err
	}
	key, err := session.KeyFor(cwd)
	if err != nil {
		return err
	}
	w, err := session.NewWriter(store, key, sessionID, session.Meta{Cwd: cwd, Mode: string(mode)})
	if err != nil {
		return err
	}
	name := "web · " + firstLine(prompt, 60)
	w.Name(name)

	client, err := rcClient(cwd)
	if err != nil {
		return err
	}
	// Git hygiene: the session's branch is brought up to date with origin's
	// default branch before its first turn and after each commit. The request
	// (or git.sync in settings) says whether; the website can switch it for the
	// running session, and what it reads of the repository flows back with the
	// session's registration.
	gitOn := settings.GitSyncEnabled()
	if pick.GitSync != nil {
		gitOn = *pick.GitSync
	}
	gs := gitsync.New(cwd, gitOn, nil)
	go gs.Watch(ctx, gitsync.InfoEvery, nil)
	syncer := sessionsync.New(sessionsync.Options{
		Client: client, HostID: headless.HostID(),
		Host:          sessionsync.Host{Hostname: sessionsync.Hostname(), OS: runtime.GOOS, BelaiVersion: version.Version},
		RemotePrompts: settings.SyncRemotePromptsEnabled(),
		// Without session controls nobody answers asks in a remote session:
		// they are off. With them, the ask control turns web answers on.
		RemoteAnswers:  false,
		RemoteCommands: pick.Controls,
		RemoteShell:    pick.Shell,
		Git:            gs.Raw,
		OnControls: func(_ string, c sessionsync.Controls) {
			if c.GitSync != nil && gs.Enabled() != *c.GitSync {
				gs.SetEnabled(*c.GitSync)
				gs.Kick()
			}
		},
	})
	// The mirror outlives ctx (a stop cancels it) long enough to upload the
	// last lines and end the session on the website.
	syncer.Start(context.Background())
	defer syncer.Close(5 * time.Second)
	syncer.Activate(sessionsync.SessionInfo{
		ID: sessionID, Path: store.SessionPath(key, sessionID), ProjectKey: string(key),
		ProjectName: key.Project(), Cwd: cwd, Name: name, Model: cfg.Model, Provider: cfg.Provider,
		Mode: string(mode), DispatchID: dispatch,
	})

	board, src := cliKanban(cwd, sessionID, settings)
	tlog := turnlog.New(w)
	if startNote != "" {
		tlog.System(startNote)
	}
	// Every role-manager decision this session makes is written to its
	// transcript, shown or not.
	defer tlog.AttachRoleManager()()

	opts := rc.SessionOptions{
		Log: tlog, Mirror: syncer, Dispatch: dispatch,
		Prompt: prompt, Mode: mode, Profile: pick.Profile, Idle: idle, Out: stderr,
		Facts: map[string]any{
			"mode": string(mode), "provider": cfg.Provider, "model": cfg.Model, "effort": cfg.Effort,
			"guardrails": settings.GuardrailsEnabled(), "routing": cfg.Routing.Kind,
		},
	}
	if !pick.Controls {
		// Asks off exactly as for `belai -prompt`: nobody can answer one, so a
		// call that would ask is decided by the posture and permission rules.
		// (AskDisabled is left alone: it would resolve every ask to allow.)
		sess, err := headless.NewSession(ctx, headless.Params{
			Cfg: cfg, Client: httpclient.Default(), Posture: pol, Workdir: cwd, Settings: settings,
			PlanMode: mode == modes.ModePlan, SessionID: sessionID, AllowAsk: false,
			MCP: mcp.Active(), Kanban: board, KanbanSource: src, GitSync: gs,
			Narrow: rcProfileNarrow(pick.Profile),
		})
		if err != nil {
			w.System("remote session could not start: " + err.Error())
			syncer.Nudge()
			return err
		}
		opts.Agent = sess
		if pick.Shell {
			// Shell lines arrive on the commands channel; there are no
			// controls to take, so the settings and posture are fixed.
			opts.Commands = syncer.Commands()
			opts.AckCommand = syncer.AckCommand
			opts.Shell = rcShellRunner(cwd, func() (config.Settings, posture.Policy) { return settings, pol },
				cfg, httpclient.Default(), rcShellCache())
		}
		return rc.RunSession(ctx, opts)
	}

	// Session controls from the web: the state starts from the resolved
	// settings and the request, and each change rebuilds the agent session
	// from a copy of the settings with the state overlaid. Nothing is written.
	modeName := string(mode)
	if modeName == "" {
		modeName = "auto"
	}
	ctlState := sessionctl.FromSettings(settings, modeName, cfg.Provider, cfg.Model, settings.Effort)
	env := rcControlEnv(cfg, pick.AllowGuardrailsOff)
	var prevDiag io.Closer
	build := func(st sessionctl.State) (rc.Runner, error) {
		s := st.Apply(settings)
		p := basePol
		if !s.GuardrailsEnabled() {
			p = posture.AllIgnore()
		}
		c := cfg
		if st.Provider != cfg.Provider || st.Model != cfg.Model {
			// A model picked on the web outranks the routing table.
			s.Routing = nil
			nc, err := run.ResolveWithSource(st.Model, st.Provider, os.Getenv, resolver)
			if err != nil {
				return nil, err
			}
			if nc, err = withClassifier(nc, s, resolver); err != nil {
				return nil, err
			}
			c = nc
		}
		config.SetActiveJevThresholds(s.JevThresholds())
		// Language servers run only when the web turned them on, in this
		// directory the host trusts (internal/lsp scrubs their environment,
		// gives them their own process group and refuses every applyEdit).
		gate := rolemanager.DiagnosticsGateFromSettings(s, []string{cwd}, st.LSP && s.LSPEnabled())
		askOff := !st.Ask
		sess, err := headless.NewSession(ctx, headless.Params{
			Cfg: c, Client: httpclient.Default(), Posture: p, Workdir: cwd, Settings: s,
			SessionID: sessionID, AllowAsk: st.Ask, AskDisabled: &askOff,
			MCP: mcp.Active(), Kanban: board, KanbanSource: src, GitSync: gs,
			Diagnostics: &gate, Narrow: rcProfileNarrow(pick.Profile),
		})
		if err != nil {
			closeDiagnoser(gate.Diagnoser)
			return nil, err
		}
		if prevDiag != nil {
			_ = prevDiag.Close()
		}
		prevDiag, _ = gate.Diagnoser.(io.Closer)
		return sess, nil
	}
	defer func() {
		if prevDiag != nil {
			_ = prevDiag.Close()
		}
	}()
	ctl, err := rc.NewController(ctlState, env, build)
	if err != nil {
		w.System("remote session could not start: " + err.Error())
		syncer.Nudge()
		return err
	}
	opts.Controls = ctl
	opts.Commands = syncer.Commands()
	opts.AckCommand = syncer.AckCommand
	opts.Changed = syncer.SetSessionControls
	opts.Answers = syncer
	if pick.Shell {
		// A web control can turn guardrails off or change the mode, so a
		// line is judged by the session's settings and posture as they are now.
		opts.Shell = rcShellRunner(cwd, func() (config.Settings, posture.Policy) {
			s := ctl.State().Apply(settings)
			p := basePol
			if !s.GuardrailsEnabled() {
				p = posture.AllIgnore()
			}
			return s, p
		}, cfg, httpclient.Default(), rcShellCache())
	}
	opts.AfterTurn = func(ctx context.Context, prompt string, res run.Result, paths []string) {
		rcAfterTurn(ctx, ctl, settings, cfg, pol, cwd, sessionID, tlog, prompt, res, paths)
	}
	return rc.RunSession(ctx, opts)
}

// rcProfileNarrow limits a web session engaged with an agent profile to the
// tools that profile lists (a flat profile's, or the definition's), exactly as
// the TUI does for an engaged agent. No profile, or one that lists none, keeps
// every tool.
func rcProfileNarrow(name string) func(*tools.Registry) *tools.Registry {
	if name == "" {
		return nil
	}
	var allow []string
	if p, err := profiles.Load(name); err == nil {
		allow = p.Tools
	} else if p, err := agentprofile.Load(name); err == nil {
		allow = p.Tools
	}
	if len(allow) == 0 {
		return nil
	}
	return func(r *tools.Registry) *tools.Registry { return fleet.NarrowTools(r, allow) }
}

// rcControlEnv is what a web session's controls may name on this host: a
// provider it holds credentials for, the effort levels the model takes, the
// fast tier for ctrl+q, and guardrails off only with the host's flag.
func rcControlEnv(cfg run.Config, allowGuardrailsOff bool) sessionctl.Env {
	main := [2]string{cfg.Provider, cfg.Model}
	return sessionctl.Env{
		AllowGuardrailsOff: allowGuardrailsOff,
		CheckModel:         rc.CheckModel,
		Efforts: func(p, m string) []string {
			if l := models.Efforts(p, m); len(l) > 0 {
				return l
			}
			return models.DefaultEfforts()
		},
		Swap: func(st sessionctl.State) (string, string, bool) {
			f := cfg.Routing.Fast
			if f == nil || f.Model == "" {
				return "", "", false
			}
			if st.Provider == f.Provider && st.Model == f.Model {
				return main[0], main[1], true
			}
			return f.Provider, f.Model, true
		},
	}
}

// rcAfterTurn runs the auto-commit and the post-end test pass for a web
// session's finished turn, each when its control is on. Both go through the
// same paths as the TUI's and the CLI's: the commit is the harness's own, of
// the paths the goal's tools changed, and the pass runs under the session's
// gates. Their lines go to the transcript.
func rcAfterTurn(ctx context.Context, ctl *rc.Controller, settings config.Settings, cfg run.Config, pol posture.Policy, cwd, sessionID string, tlog *turnlog.Log, prompt string, res run.Result, paths []string) {
	st := ctl.State()
	s := st.Apply(settings)
	if !s.GuardrailsEnabled() {
		pol = posture.AllIgnore()
	}
	if st.AutoCommit && res.GoalSentinel == rolemanager.GoalComplete && len(paths) > 0 {
		msg := forge.TaskCommitMessage(firstLine(prompt, 200), paths)
		sha, err := forge.CommitPaths(ctx, forge.NonInteractiveRunner, cwd, paths, msg)
		switch {
		case err != nil:
			tlog.System("auto-commit: " + forge.CleanErr(err))
		case sha != "":
			tlog.System(fmt.Sprintf("auto-commit %s %s", sha, msg))
			project, _ := kanban.ProjectFor(cwd)
			audit.Emit(audit.Fact{Kind: audit.RepoCommit, ActorKind: audit.ActorAgent, Actor: "belai",
				SessionID: sessionID, Repo: project, Commit: forge.HeadCommit(ctx, forge.NonInteractiveRunner, cwd), Outcome: "auto_commit"})
		}
	}
	if st.Mode == "plan" {
		return
	}
	trigger := testpass.TriggerSession
	if res.GoalSentinel == rolemanager.GoalComplete {
		trigger = testpass.TriggerGoal
	}
	if !headless.ShouldPostEnd(ctx, s, cwd, trigger) {
		return
	}
	sess, _ := ctl.Runner().(*agent.Session)
	out := headless.RunPostEnd(ctx, headless.PostEnd{
		Cfg: cfg, Client: httpclient.Default(), Posture: pol, Workdir: cwd, Settings: s,
		Session: sess, Trigger: trigger, Observe: tlog.Observe,
		Notify: tlog.System,
	})
	tlog.Flush()
	tlog.System(out.Line())
	if out.Report != "" {
		tlog.System(out.Report)
	}
}

func closeDiagnoser(d rolemanager.Diagnoser) {
	if c, ok := d.(io.Closer); ok {
		_ = c.Close()
	}
}

func firstLine(s string, n int) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	r := []rune(strings.TrimSpace(s))
	if len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return string(r)
}
