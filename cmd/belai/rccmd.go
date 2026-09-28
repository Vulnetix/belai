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

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/credentials"
	"github.com/vulnetix/belai/internal/headless"
	"github.com/vulnetix/belai/internal/httpclient"
	"github.com/vulnetix/belai/internal/mcp"
	"github.com/vulnetix/belai/internal/modes"
	"github.com/vulnetix/belai/internal/posture"
	"github.com/vulnetix/belai/internal/proc"
	"github.com/vulnetix/belai/internal/rc"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/sandbox"
	"github.com/vulnetix/belai/internal/session"
	"github.com/vulnetix/belai/internal/sessionsync"
	"github.com/vulnetix/belai/internal/trustgate"
	"github.com/vulnetix/belai/internal/turnlog"
	"github.com/vulnetix/belai/internal/version"
)

// dirList is a repeatable --dir flag.
type dirList []string

func (d *dirList) String() string     { return strings.Join(*d, ",") }
func (d *dirList) Set(v string) error { *d = append(*d, v); return nil }

const rcUsage = `usage: belai rc [--dir PATH]... [--max N] [--idle DURATION] [--detach]
       belai rc --status
       belai rc --stop

Runs Belai remote control: this machine appears under Belai → Sessions on the
Vulnetix website, which can start headless Belai sessions here, follow them
live and prompt them. Sessions run only in projects already trusted on this
machine and in the directories given with --dir.

Needs a Vulnetix CLI browser login (vulnetix auth login).
`

// runRCCLI implements `belai rc`. It returns the exit code.
func runRCCLI(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("rc", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, rcUsage); fs.PrintDefaults() }
	var dirs dirList
	fs.Var(&dirs, "dir", "also offer this directory (repeatable); it is trusted, as with -trust-dir")
	max := fs.Int("max", rc.DefaultMax, "sessions to run at once; when given, also the fleet worker cap in place of agents.max_workers")
	idle := fs.Duration("idle", rc.DefaultIdle, "end a session after this long without a prompt")
	detach := fs.Bool("detach", false, "run in the background; logs go to ~/.vulnetix/belai/rc/rc.log")
	status := fs.Bool("status", false, "show whether remote control is running")
	stop := fs.Bool("stop", false, "stop the running remote control and its sessions")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "belai rc: unexpected argument %q\n", fs.Arg(0))
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

	wd, _ := os.Getwd()
	settings, err := config.LoadMerged(wd)
	if err != nil {
		fmt.Fprintln(stderr, "belai rc: load settings:", err)
		return 1
	}
	offered := rc.Collect(dirs)
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
	fmt.Fprintln(stderr, "Sessions run with asks off: the posture and permission rules decide. Ctrl+C stops remote control and its sessions.")
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
	d, err := rc.New(rc.Options{
		Exe: exe, Client: client, HostID: hostID, Host: host, Dirs: offered,
		Max: *max, MaxWorkers: explicitMax(fs, *max), Idle: *idle, URL: url, Out: stderr, LogPath: logPath,
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

// explicitMax is --max when it was given, else 0: only an explicit --max
// replaces agents.max_workers, so the session default never lowers it.
func explicitMax(fs *flag.FlagSet, max int) int {
	n := 0
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "max" {
			n = max
		}
	})
	return n
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
	modeFlag := fs.String("mode", "", "agent, plan or goal (default: classified)")
	idle := fs.Duration("idle", rc.DefaultIdle, "end after this long without a prompt")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *sessionID == "" || *dispatch == "" {
		fmt.Fprintln(stderr, "belai rc-session: -session-id and -dispatch are required (the rc daemon starts this)")
		return 2
	}
	mode := modes.Mode(*modeFlag)
	switch mode {
	case "", modes.ModeAgent, modes.ModePlan, modes.ModeGoal:
	default:
		fmt.Fprintf(stderr, "belai rc-session: unknown mode %q\n", *modeFlag)
		return 2
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
	if err := runRCSession(ctx, *dispatch, *sessionID, mode, prompt, *idle, stderr); err != nil {
		fmt.Fprintln(stderr, "belai rc-session:", err)
		return 1
	}
	return 0
}

func runRCSession(ctx context.Context, dispatch, sessionID string, mode modes.Mode, prompt string, idle time.Duration, stderr io.Writer) error {
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
	settings, err := config.LoadMerged(cwd)
	if err != nil {
		return fmt.Errorf("load settings: %w", err)
	}
	if !settings.GuardrailsEnabled() {
		return errors.New("guardrails are off; remote sessions never run without them")
	}
	if err := run.PreloadClassifier(run.ResolveSecurityClassifier(settings.Classifier)); err != nil {
		return fmt.Errorf("load embedded classifier: %w", err)
	}
	projectPol, _ := posture.Load(cwd)
	pol := posture.Defaults().Override(projectPol)
	resolver, err := newResolver(cwd)
	if err != nil {
		return err
	}
	cfg, err := run.ResolveWithSource("", "", os.Getenv, resolver)
	if err != nil {
		return err
	}
	cfg, err = withClassifier(cfg, settings, resolver)
	if err != nil {
		return err
	}

	mcpMgr := mcp.StartAsync(ctx, settings.MCP, mcp.Options{
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
	syncer := sessionsync.New(sessionsync.Options{
		Client: client, HostID: headless.HostID(),
		Host:          sessionsync.Host{Hostname: sessionsync.Hostname(), OS: runtime.GOOS, BelaiVersion: version.Version},
		RemotePrompts: settings.SyncRemotePromptsEnabled(),
		// Nobody answers asks in a remote session: they are off.
		RemoteAnswers: false,
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

	// Asks off exactly as for `belai -prompt`: nobody can answer one, so a
	// call that would ask is decided by the posture and permission rules.
	// (AskDisabled is left alone: it would resolve every ask to allow.)
	board, src := cliKanban(cwd, sessionID, settings)
	sess, err := headless.NewSession(ctx, headless.Params{
		Cfg: cfg, Client: httpclient.Default(), Posture: pol, Workdir: cwd, Settings: settings,
		PlanMode: mode == modes.ModePlan, SessionID: sessionID, AllowAsk: false,
		MCP: mcp.Active(), Kanban: board, KanbanSource: src,
	})
	if err != nil {
		w.System("remote session could not start: " + err.Error())
		syncer.Nudge()
		return err
	}
	return rc.RunSession(ctx, rc.SessionOptions{
		Agent: sess, Log: turnlog.New(w), Mirror: syncer, Dispatch: dispatch,
		Prompt: prompt, Mode: mode, Idle: idle, Out: stderr,
		Facts: map[string]any{
			"mode": string(mode), "provider": cfg.Provider, "model": cfg.Model, "effort": cfg.Effort,
			"guardrails": true,
		},
	})
}

func firstLine(s string, n int) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	r := []rune(strings.TrimSpace(s))
	if len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return string(r)
}
