package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/vulnetix/belai/internal/agent"
	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/bgagent"
	"github.com/vulnetix/belai/internal/budget"
	"github.com/vulnetix/belai/internal/calltrace"
	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/credentials"
	"github.com/vulnetix/belai/internal/decisionserver"
	"github.com/vulnetix/belai/internal/gitsync"
	"github.com/vulnetix/belai/internal/headless"
	"github.com/vulnetix/belai/internal/httpclient"
	"github.com/vulnetix/belai/internal/mcp"
	"github.com/vulnetix/belai/internal/modes"
	"github.com/vulnetix/belai/internal/nonce"
	"github.com/vulnetix/belai/internal/posture"
	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/sandbox"
	"github.com/vulnetix/belai/internal/session"
	"github.com/vulnetix/belai/internal/testpass"
	"github.com/vulnetix/belai/internal/trustgate"
	"github.com/vulnetix/belai/internal/tui"
	"github.com/vulnetix/belai/internal/turnlog"
	"github.com/vulnetix/belai/internal/version"
)

func main() {
	// Stop any local decision server this process launched, on every exit.
	defer decisionserver.StopAll()
	_, _ = config.Migrate()
	activatePlugins()
	// Remember providers without a nonce endpoint across runs, so a session
	// does not spend a round trip before its first model call relearning it.
	if dir, err := config.GlobalDir(); err == nil {
		nonce.SetNegativeCacheFile(filepath.Join(dir, "cache", "nonce-unsupported.json"))
	}

	// One root context for every non-TUI entry point. Goal mode's pass loop is
	// unbounded by design, so an interruptible context is the only thing that
	// can stop it: without this, SIGINT kills the process outright, leaving no
	// clean stop and no session entry. A second signal hard-exits, because a
	// pass boundary may still be seconds away.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go hardExitOnSecondSignal(ctx)

	// `belai help <command>` is `belai <command> -h`. Bare `belai help` needs
	// the flags defined below, so it is answered after they are.
	if len(os.Args) > 2 && os.Args[1] == "help" {
		target, ok := helpTarget(os.Args[2:])
		if !ok {
			fmt.Fprintf(os.Stderr, "belai help: %q is not a command; run `belai -help` for the list\n", os.Args[2])
			exitProcess(2)
		}
		// The command prints its usage to stderr; here it is the answer, so it
		// goes to stdout where a pager can take it.
		os.Stderr = os.Stdout
		os.Args = append([]string{os.Args[0]}, target...)
	}
	// `belai acp` serves the Agent Client Protocol to an editor.
	if len(os.Args) > 1 && os.Args[1] == "acp" {
		exitProcess(runACP(ctx, os.Args[2:], os.Stdin, os.Stdout, os.Stderr))
	}
	// `belai login kiro` signs in to Kiro with an AWS Builder ID.
	if len(os.Args) > 1 && os.Args[1] == "login" {
		exitProcess(runLoginCLI(ctx, os.Args[2:], os.Stdin, os.Stdout, os.Stderr, isCharDevice(os.Stdin)))
	}
	// `belai agent …` manages fleet workers; `belai kanban …` edits the board.
	// Both parse their own flags, so they dispatch before flag.Parse.
	if len(os.Args) > 1 && os.Args[1] == "agent" {
		exitProcess(runAgentCLI(ctx, os.Args[2:], os.Stdin, os.Stdout, os.Stderr))
	}
	if len(os.Args) > 1 && os.Args[1] == "kanban" {
		exitProcess(runKanbanCLI(os.Args[2:], os.Stdin, os.Stdout, os.Stderr))
	}
	// `belai skill|prompt|process|repo|budget|rewrite|provider …` read and write library items, the documents the
	// website's library keeps (docs/library-items.md).
	if len(os.Args) > 1 && (os.Args[1] == "skill" || os.Args[1] == "prompt" || os.Args[1] == "process" || os.Args[1] == "repo" || os.Args[1] == "budget" || os.Args[1] == "rewrite" || os.Args[1] == "provider") {
		exitProcess(runLibraryCLI(ctx, libraryCommands[os.Args[1]], os.Args[2:], os.Stdin, os.Stdout, os.Stderr))
	}
	// `belai rc` runs remote control; `belai rc-session` is one session it
	// started (hidden: only the daemon runs it).
	if len(os.Args) > 1 && os.Args[1] == "rc" {
		exitProcess(runRCCLI(ctx, os.Args[2:], os.Stdout, os.Stderr))
	}
	if len(os.Args) > 1 && os.Args[1] == "rc-session" {
		exitProcess(runRCSessionCLI(ctx, os.Args[2:], os.Stdin, os.Stderr))
	}
	// `belai plugin …` is a subcommand with its own flags.
	if len(os.Args) > 1 && os.Args[1] == "plugin" {
		exitProcess(runPluginCLI(ctx, os.Args[2:], os.Stdin, os.Stdout, os.Stderr, isCharDevice(os.Stdin)))
	}

	showVersion := flag.Bool("version", false, "print version and exit")
	trustDir := flag.Bool("trust-dir", false, "trust the current directory without prompting")
	noGitSync := flag.Bool("no-git-sync", false, "do not rebase the branch onto origin's default branch before a turn (git.sync in settings; /gitsync in the TUI)")
	prompt := flag.String("prompt", "", "send a noninteractive prompt and print the reply, then exit")
	model := flag.String("model", "", "model id (defaults per provider)")
	provider := flag.String("provider", "", "provider (default openrouter): openai, anthropic, cloudflare-workers-ai, cloudflare-ai-gateway, openrouter, google-gemini, ollama, llama-server, github-copilot, huggingface, kiro, or a custom name from settings.json")
	detectMode := flag.Bool("detect-mode", false, "run the operating-mode classifier and report the decision")
	verbose := flag.Bool("verbose", false, "print role-manager decisions to stderr")

	allowUnsafeToolResult := flag.Bool("allow-unsafe-tool-result", false, "ignore unsafe tool results")
	allowMalformedToolResult := flag.Bool("allow-malformed-tool-result", false, "ignore malformed tool results")
	allowUnsafePrompt := flag.Bool("allow-unsafe-prompt", false, "ignore unsafe prompt classification")
	allowMalformedPrompt := flag.Bool("allow-malformed-prompt", false, "ignore malformed prompt classification")
	toolCallMismatch := flag.String("tool-call-mismatch", "", "abort|strip|ignore tool-call mismatches")
	allowUnpermittedTools := flag.Bool("allow-unpermitted-tools", false, "allow tool calls matching no permission rule (default)")
	allowAskWithoutTTY := flag.Bool("allow-ask-without-tty", false, "ignore ask-without-tty blocks")
	allowInvalidSkills := flag.Bool("allow-invalid-skills", false, "ignore invalid skill validation")
	allowInvalidHooks := flag.Bool("allow-invalid-hooks", false, "ignore invalid hook validation")
	dangerouslyYolo := flag.Bool("dangerously-yolo-everything", false, "ignore every posture gate")
	guardrails := flag.Bool("guardrails", true, "enable the posture guardrails; -guardrails=false is the guardrails-off half of YOLO")
	askPermission := flag.Bool("ask-permission", true, "enable the permission-ask gate; -ask-permission=false resolves asks to allow")
	firewall := flag.Bool("firewall", false, "route LLM traffic through the active AI Firewall (see /firewall)")
	enableTools := flag.Bool("tools", true, "enable tool execution; pass -tools=false to disable")
	effort := flag.String("effort", "", "thinking effort level: low, medium, or high")
	classifierProvider := flag.String("classifier-provider", "", "security-classifier provider (default: the main provider)")
	classifierModel := flag.String("classifier-model", "", "security-classifier model (default: the main model)")
	classifierEffort := flag.String("classifier-effort", "", "security-classifier thinking effort (default: none)")
	classifierKind := flag.String("classifier-kind", "", "security-classifier stack: llm, jev or models (default: models when the binary embeds a model, else llm)")
	classifierPhase1Model := flag.String("classifier-phase1-model", "", "phase-1 prompt-saturation model id")
	classifierPhase1Source := flag.String("classifier-phase1-source", "", "phase-1 source: embedded or huggingface")
	classifierPhase1Threshold := flag.Float64("classifier-phase1-threshold", 0, "phase-1 attack threshold (default 0.75)")
	classifierPhase2Model := flag.String("classifier-phase2-model", "", "phase-2 jailbreak model id")
	classifierPhase2Source := flag.String("classifier-phase2-source", "", "phase-2 source: embedded, huggingface, or disabled")
	classifierPhase2Threshold := flag.Float64("classifier-phase2-threshold", 0, "phase-2 attack threshold (default 0.75)")
	caveman := flag.Bool("caveman", false, "enable caveman voice rewrite for this run")
	sessionRetentionDays := flag.Int("session-retention-days", 0, "idle session retention in days (default 28)")
	noPrune := flag.Bool("no-prune", false, "never prune idle sessions")
	flag.BoolVar(&noTranscript, "no-transcript", false, "with -prompt, do not keep a session transcript of the run")
	planMode := flag.Bool("plan", false, "start in plan mode (read-only)")
	modeFlag := flag.String("mode", "", "operating mode for -prompt: agent, plan or goal (default: classified from the prompt)")
	deferTools := flag.Bool("defer-tools", true, "advertise core tools in full and load the rest on demand with ToolSearch; -defer-tools=false sends every tool definition on every request")
	agentName := flag.String("agent", "", "start a background agent by name in foreground mode")
	agentCreate := flag.String("agent-create", "", "create an agent profile from a description and save to disk")
	resume := flag.String("resume", "", "resume a session by id or unique id prefix")
	flag.StringVar(resume, "r", "", "shorthand for -resume")
	continueLast := flag.String("continue", "", "continue the most recent session for this project")
	flag.StringVar(continueLast, "c", "", "shorthand for -continue")
	exportID := flag.String("export", "", "export a session by id or unique id prefix as Markdown and exit")
	flag.StringVar(&usageJSONPath, "usage-json", "", "with -prompt, write a JSON summary of the run's token usage (per role, per model, request composition) to this path on exit")
	flag.CommandLine.Init(os.Args[0], flag.ContinueOnError)
	if len(os.Args) == 2 && os.Args[1] == "help" {
		printHelp(os.Stdout, flag.CommandLine)
		exitProcess(0)
	}
	if ok, code := parseTopLevel(flag.CommandLine, os.Args[1:], os.Stdout, os.Stderr); !ok {
		exitProcess(code)
	}

	if *showVersion {
		fmt.Println(version.Version)
		exitProcess(0)
	}
	switch modes.Mode(*modeFlag) {
	case "", modes.ModeAgent, modes.ModeGoal:
	case modes.ModePlan:
		*planMode = true
	default:
		fmt.Fprintf(os.Stderr, "belai: -mode must be agent, plan or goal, not %q\n", *modeFlag)
		exitProcess(2)
	}

	workdir, _ := os.Getwd()
	if *exportID != "" {
		if err := exportSessionCLI(*exportID, workdir); err != nil {
			fmt.Fprintln(os.Stderr, "belai:", err)
			exitProcess(1)
		}
		exitProcess(0)
	}

	if *resume != "" && *prompt != "" {
		fmt.Fprintln(os.Stderr, "belai: -resume requires the interactive TUI (not supported with -prompt)")
		exitProcess(1)
	}
	if *continueLast != "" && *resume != "" {
		fmt.Fprintln(os.Stderr, "belai: -continue cannot be combined with -resume")
		exitProcess(1)
	}
	if *continueLast != "" && *prompt != "" {
		fmt.Fprintln(os.Stderr, "belai: -continue requires the interactive TUI (not supported with -prompt)")
		exitProcess(1)
	}

	// First-run trust gate: block on an unknown directory before any repo
	// content is read, any process is auto-started, or any model turn runs.
	st, terr := trustgate.Check(workdir)
	switch {
	case terr != nil || st.NeedsPrompt():
		if *trustDir && !st.Trusted {
			// Grant trust to the directory only; proposed workspace_dirs are
			// not accepted, so the flag can never silently widen the sandbox.
			if err := trustgate.Grant(workdir, nil); err != nil {
				fmt.Fprintln(os.Stderr, "belai: trust directory:", err)
				exitProcess(1)
			}
			if len(st.NewDirs) > 0 {
				fmt.Fprintf(os.Stderr, "belai: trusted %s; skipping proposed workspace directories: %s\n",
					workdir, strings.Join(st.NewDirs, ", "))
			}
		} else if interactive(isCharDevice(os.Stdout), isCharDevice(os.Stdin), os.Getenv) {
			ok, err := tui.RunTrustGate(st)
			if err != nil {
				fmt.Fprintln(os.Stderr, "belai: trust dialog:", err)
				exitProcess(1)
			}
			if !ok && !st.Trusted {
				exitProcess(1)
			}
		} else {
			// Headless fails closed: no model turn runs in an untrusted
			// directory without an explicit opt-in.
			fmt.Fprintf(os.Stderr, "belai: %s is not a trusted workspace.\n"+
				"Run `belai` here once to review and trust it, or `belai -trust-dir`.\n", workdir)
			exitProcess(1)
		}
	}

	settings, err := config.LoadMerged(workdir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "belai: load settings:", err)
		exitProcess(1)
	}
	if *effort != "" {
		settings.Effort = *effort
	}
	if *noGitSync {
		off := false
		settings.Git = &config.GitSettings{Sync: &off}
	}
	if *classifierProvider != "" || *classifierModel != "" || *classifierEffort != "" || *classifierKind != "" ||
		*classifierPhase1Model != "" || *classifierPhase1Source != "" || *classifierPhase1Threshold != 0 ||
		*classifierPhase2Model != "" || *classifierPhase2Source != "" || *classifierPhase2Threshold != 0 {
		if settings.Classifier == nil {
			settings.Classifier = &config.ClassifierSettings{}
		}
		settings.Classifier.Provider = *classifierProvider
		settings.Classifier.Model = *classifierModel
		settings.Classifier.Effort = *classifierEffort
		settings.Classifier.Kind = *classifierKind
		settings.Classifier.Phase1 = config.ClassifierPhaseSettings{
			Model:     *classifierPhase1Model,
			Source:    *classifierPhase1Source,
			Threshold: *classifierPhase1Threshold,
		}
		settings.Classifier.Phase2 = config.ClassifierPhaseSettings{
			Model:     *classifierPhase2Model,
			Source:    *classifierPhase2Source,
			Threshold: *classifierPhase2Threshold,
		}
	}
	if *caveman {
		t := true
		settings.Caveman = &t
	}
	if *dangerouslyYolo {
		f := false
		settings.Guardrails = &f
		settings.AskPermission = &f
	} else {
		if !*guardrails {
			f := false
			settings.Guardrails = &f
		}
		if !*askPermission {
			f := false
			settings.AskPermission = &f
		}
	}
	if *firewall || os.Getenv("BELAI_FIREWALL") == "1" || os.Getenv("BELAI_FIREWALL") == "true" {
		if settings.Firewall == nil {
			settings.Firewall = &config.FirewallSettings{}
		}
		t := true
		settings.Firewall.Enabled = &t
		// The resolver loads its own settings: tell it too, so the flag
		// routes headless runs and not only the TUI.
		forceFirewall = true
	}
	if !*deferTools {
		f := false
		settings.DeferTools = &f
	}
	if *sessionRetentionDays > 0 {
		settings.SessionRetentionDays = sessionRetentionDays
	}

	fs := posture.FlagSet{
		AllowUnsafeToolResult:    allowUnsafeToolResult,
		AllowMalformedToolResult: allowMalformedToolResult,
		AllowUnsafePrompt:        allowUnsafePrompt,
		AllowMalformedPrompt:     allowMalformedPrompt,
		ToolCallMismatch:         toolCallMismatch,
		AllowUnpermittedTools:    allowUnpermittedTools,
		AllowAskWithoutTTY:       allowAskWithoutTTY,
		AllowInvalidSkills:       allowInvalidSkills,
		AllowInvalidHooks:        allowInvalidHooks,
		DangerouslyYolo:          *dangerouslyYolo,
	}
	cliPol := fs.ToPolicy()
	projectPol, _ := posture.Load(workdir)
	pol := posture.Defaults().Override(projectPol).Override(cliPol)
	// settings.GuardrailsEnabled rather than the flag alone: the flag has
	// already been folded into settings above, and the setting can also come
	// from a settings.json the operator wrote or from the TUI's own toggle.
	// Reading only the flag here meant `"guardrails": false` on disk left
	// every gate enforcing on the CLI path while the TUI honoured it.
	if !settings.GuardrailsEnabled() {
		pol = posture.AllIgnore()
	}
	posture.PrintBanner(pol, os.Stderr)

	// Eagerly load the embedded classifier models so a variant binary whose
	// embedded model fails to load or verify is a hard startup error, never a
	// silent downgrade to the LLM sentinel path. A vanilla binary resolves to
	// kind "llm" and this is a no-op.
	if err := run.PreloadClassifier(run.ResolveSecurityClassifier(settings.Classifier)); err != nil {
		fmt.Fprintln(os.Stderr, "belai: load embedded classifier:", err)
		exitProcess(1)
	}

	// Resolve --resume / --continue before the TUI starts so a bad id (or an
	// empty project) exits non-zero with a message instead of dropping the user
	// into a TUI to discover the failure.
	var resumeKey session.Key
	var resumeID string
	if *resume != "" || *continueLast != "" {
		store, err := session.NewStore()
		if err != nil {
			fmt.Fprintln(os.Stderr, "belai:", err)
			exitProcess(1)
		}
		cur, _ := session.KeyFor(workdir)
		if *resume != "" {
			resumeKey, resumeID, err = store.ResolveAnywhere(cur, *resume)
			if err != nil {
				fmt.Fprintln(os.Stderr, "belai:", err)
				exitProcess(1)
			}
		} else {
			resumeKey, resumeID, err = continueLatest(store, cur)
			if err != nil {
				fmt.Fprintln(os.Stderr, "belai:", err)
				exitProcess(1)
			}
		}
	}

	if !*noPrune {
		go pruneSessions(settings, resumeKey, resumeID)
	}

	if *agentCreate != "" {
		if err := runAgentCreate(ctx, *agentCreate, *model, *provider, workdir, pol, settings); err != nil {
			fmt.Fprintln(os.Stderr, "belai:", err)
			exitProcess(1)
		}
		exitProcess(0)
	}

	if *agentName != "" {
		if err := runAgentForeground(ctx, *agentName, *model, *provider, workdir, pol, settings); err != nil {
			fmt.Fprintln(os.Stderr, "belai:", err)
			exitProcess(1)
		}
		exitProcess(0)
	}

	// MCP servers start only here: past the trust gate, from the user's own
	// settings, and in the background so a slow server never holds startup.
	mcpMgr := mcp.StartAsync(ctx, settings.MCP, mcp.Options{
		Workdir:    workdir,
		HTTPClient: httpclient.Default(),
		VulnetixAuth: func() (string, error) {
			return credentials.VulnetixAuthHeader(workdir)
		},
		Sandbox: func() sandbox.Policy {
			return sandbox.FromSettings(settings.Sandbox, []string{workdir}, pol)
		},
	})
	mcp.SetActive(mcpMgr)
	stopTelemetry := startTelemetry(settings, workdir)
	shutdown := func() {
		mcpMgr.Close()
		stopTelemetry()
	}

	if *prompt != "" {
		err := runPromptOrTUI(ctx, *prompt, *model, *provider, *detectMode, *verbose, workdir, pol, *enableTools, *planMode, modes.Mode(*modeFlag), settings)
		shutdown()
		if err != nil {
			fmt.Fprintln(os.Stderr, "belai:", err)
			exitProcess(1)
		}
		exitProcess(0)
	}

	if interactive(isCharDevice(os.Stdout), isCharDevice(os.Stdin), os.Getenv) {
		resolver, err := newResolver(workdir)
		if err != nil {
			fmt.Fprintln(os.Stderr, "belai:", err)
			exitProcess(1)
		}
		err = tui.Start(tui.Options{Workdir: workdir, Resolver: resolver, Provider: *provider, Model: *model, Settings: &settings, Posture: pol, PlanMode: *planMode, Firewall: forceFirewall, ResumeKey: resumeKey, ResumeSession: resumeID})
		shutdown()
		if err != nil {
			fmt.Fprintln(os.Stderr, "belai:", err)
			exitProcess(1)
		}
		exitProcess(0)
	}

	shutdown()
	fmt.Println("belai", version.Version)
}

// hardExitOnSecondSignal waits for the first signal to cancel ctx, then exits
// immediately on the next one. The graceful path unwinds at a pass boundary,
// which can be seconds away; a user pressing ctrl+c twice means "now".
func hardExitOnSecondSignal(ctx context.Context) {
	<-ctx.Done()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
	exitProcess(130)
}

// exportSessionCLI resolves a session id or prefix (preferring the current
// project) and prints its Markdown export to stdout. It reads only the global
// session store, never repository content, so it needs no trust gate.
func exportSessionCLI(idOrPrefix, workdir string) error {
	store, err := session.NewStore()
	if err != nil {
		return err
	}
	cur, _ := session.KeyFor(workdir)
	key, id, err := store.ResolveAnywhere(cur, idOrPrefix)
	if err != nil {
		return err
	}
	entries, err := store.ReadFrom(key, id)
	if err != nil {
		return err
	}
	fmt.Print(session.ExportMarkdown(entries, session.ExportOptions{ID: id}))
	return nil
}

func interactive(stdoutTTY, stdinTTY bool, env func(string) string) bool {
	if env("BELAI_NO_TUI") != "" || env("CI") != "" {
		return false
	}
	return stdoutTTY && stdinTTY
}

func isCharDevice(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// withClassifier resolves the classifier config from settings (including any
// -classifier-* flags folded in by main) and stores it on cfg. A nil settings
// classifier yields the default: the main provider/model with reasoning off.
func withClassifier(cfg run.Config, settings config.Settings, resolver *credentials.Resolver) (run.Config, error) {
	var src run.CredentialSource
	if resolver != nil {
		src = resolver
	}
	cc, err := run.ResolveClassifier(cfg, settings.Classifier, src)
	if err != nil {
		return cfg, err
	}
	cfg.Classifier = cc
	cfg.Security = run.ResolveSecurityClassifier(settings.Classifier)
	if n := run.WarmDecisions(cfg); n != "" {
		decisionNoticeOnce.Do(func() { fmt.Fprintln(os.Stderr, "belai: "+n) })
	}
	if n := cfg.Security.Fallback; n != "" {
		securityFallbackOnce.Do(func() { fmt.Fprintln(os.Stderr, "belai: "+n) })
	}
	if rc, err := run.ResolveRouting(cfg, settings.Routing, src); err == nil {
		cfg.Routing = rc
	}
	return cfg, nil
}

// securityFallbackOnce keeps the fallback notice to one line per process,
// however many sessions (ACP, a worker's items) resolve the classifier.
var securityFallbackOnce sync.Once

// decisionNoticeOnce keeps the decision-server notice to one line per process.
var decisionNoticeOnce sync.Once

func runPromptOrTUI(ctx context.Context, prompt, model, providerName string, detectMode, verbose bool, workdir string, pol posture.Policy, enableTools, planMode bool, forceMode modes.Mode, settings config.Settings) error {
	resolver, err := newResolver(workdir)
	if err != nil {
		return err
	}
	cfg, err := run.ResolveWithSource(model, providerName, os.Getenv, resolver)
	if err != nil {
		var nce *run.NotConfiguredError
		if errors.As(err, &nce) && interactive(isCharDevice(os.Stdout), isCharDevice(os.Stdin), os.Getenv) {
			fmt.Fprintf(os.Stderr, "belai: no credentials for %s (missing %s). Opening the credential manager…\n",
				nce.Provider, strings.Join(nce.Missing, ", "))
			return tui.Start(tui.Options{Workdir: workdir, Resolver: resolver, Prompt: prompt, Provider: providerName, Model: model, Settings: &settings, Posture: pol, PlanMode: planMode, Firewall: forceFirewall})
		}
		// Without a TTY, fail closed naming every location searched.
		return err
	}
	cfg, err = withClassifier(cfg, settings, resolver)
	if err != nil {
		return err
	}

	// A headless prompt gets its own session id so its provider requests and
	// tool calls share one trace, and keeps a transcript like any other session
	// (it resumes, exports and searches, and records every role-manager
	// decision it made). -no-transcript opts out; the file is private to the
	// user, pruned by session_retention_days, and never synced.
	sessionID := session.MustID()
	ctx = calltrace.WithSession(ctx, sessionID)
	tlog := turnlog.New(nil)
	if !noTranscript {
		var terr error
		tlog, terr = turnlog.Open(workdir, sessionID, session.Meta{Cwd: workdir, Mode: string(forceMode)})
		if terr != nil && verbose {
			fmt.Fprintf(os.Stderr, "belai: no transcript: %v\n", terr)
		}
	}
	defer tlog.AttachRoleManager()()
	tlog.User(prompt, nil)
	// A headless prompt spends tokens like any other session: record them so
	// day and month budgets see it. Nothing is printed; the TUI shows budgets.
	// With -usage-json the same events are summarised to a file on exit, a
	// failed run included, so a benchmark can price every attempt.
	var sum *run.UsageSummary
	if usageJSONPath != "" {
		sum = &run.UsageSummary{}
		defer func() {
			if werr := sum.WriteFile(usageJSONPath); werr != nil {
				fmt.Fprintf(os.Stderr, "belai: write usage summary: %v\n", werr)
			}
		}()
	}
	defer recordUsage(sessionID, settings, sum)()
	// Firewall events print one line each to stderr; stdout stays the reply.
	defer watchFirewallHeadless(os.Stderr)()

	var res run.Result
	if detectMode || !enableTools {
		res, err = run.EngageWithPosture(ctx, cfg, prompt, detectMode, httpclient.Default(), pol)
		if err == nil {
			if w := tlog.Writer(); w != nil {
				w.Assistant(res.Reply, nil, nil)
			}
		}
	} else {
		res, err = runAgent(ctx, cfg, prompt, httpclient.Default(), pol, workdir, settings, planMode, forceMode, sessionID, tlog)
	}
	if err != nil {
		return err
	}
	if verbose {
		fmt.Fprintf(os.Stderr, "security: %s\n", res.SecuritySentinel.Label())
		if detectMode {
			fmt.Fprintf(os.Stderr, "mode: %s\n", res.ModeDecision.Mode)
			if res.ModeDecision.AgentName != "" {
				fmt.Fprintf(os.Stderr, "agent: %s\n", res.ModeDecision.AgentName)
			}
			if res.ModeDecision.Warning != "" {
				fmt.Fprintf(os.Stderr, "warning: %s\n", res.ModeDecision.Warning)
			}
			if res.ModeDecision.Explore {
				fmt.Fprintln(os.Stderr, "explore: true")
			}
		}
	}
	fmt.Println(res.Reply)
	return nil
}

func runAgent(ctx context.Context, cfg run.Config, userPrompt string, client *http.Client, pol posture.Policy, workdir string, settings config.Settings, planMode bool, forceMode modes.Mode, sessionID string, tlog *turnlog.Log) (run.Result, error) {
	// A headless prompt starts from a branch that is current with origin's
	// default branch, unless git.sync (or -no-git-sync) says not to.
	var gs *gitsync.Hygiene
	if settings.GitSyncEnabled() {
		gs = gitsync.New(workdir, true, nil)
	}
	sess, err := newCLISession(ctx, cfg, client, pol, workdir, settings, planMode, sessionID, false, gs)
	if err != nil {
		return run.Result{}, err
	}
	defer flushKanban(settings, workdir)
	// An explicit -mode is the user's choice and outranks the classifier,
	// exactly as a mode picked in the TUI does.
	res, err := sess.RunInputObserved(ctx, nil, agent.TurnInput{Prompt: userPrompt, ForceMode: forceMode}, tlog.Observe)
	tlog.Flush()
	if err == nil {
		postEndTests(ctx, cfg, client, pol, workdir, settings, planMode, sess, res, tlog)
	}
	return res, err
}

// postEndTests runs the post-end test pass for a finished headless run when
// the user's `tests.post_end` setting asks for it. The trigger is a completed
// goal, or the end of the run when the level is "session" and the tree has
// changes. Its lines go to stderr so stdout stays the reply. A plan-mode run
// changes nothing, so it never triggers a pass.
func postEndTests(ctx context.Context, cfg run.Config, client *http.Client, pol posture.Policy, workdir string, settings config.Settings, planMode bool, sess *agent.Session, res run.Result, tlog *turnlog.Log) {
	if planMode || ctx.Err() != nil {
		return
	}
	trigger := testpass.TriggerSession
	if res.GoalSentinel == rolemanager.GoalComplete {
		trigger = testpass.TriggerGoal
	}
	if !headless.ShouldPostEnd(ctx, settings, workdir, trigger) {
		return
	}
	out := headless.RunPostEnd(ctx, headless.PostEnd{
		Cfg: cfg, Client: client, Posture: pol, Workdir: workdir, Settings: settings,
		Session: sess, Trigger: trigger, Observe: tlog.Observe,
		Notify: func(s string) { fmt.Fprintln(os.Stderr, s) },
	})
	tlog.Flush()
	fmt.Fprintln(os.Stderr, out.Line())
	if out.Report != "" {
		fmt.Fprintln(os.Stderr, out.Report)
	}
}

// newCLISession builds a top-level agent session outside the TUI: the
// headless -prompt run (no asks: allowAsk false) and each ACP session (the
// editor answers asks: allowAsk true).
func newCLISession(ctx context.Context, cfg run.Config, client *http.Client, pol posture.Policy, workdir string, settings config.Settings, planMode bool, sessionID string, allowAsk bool, gs *gitsync.Hygiene) (*agent.Session, error) {
	store, src := cliKanban(workdir, sessionID, settings)
	return headless.NewSession(ctx, headless.Params{
		Cfg: cfg, Client: client, Posture: pol, Workdir: workdir, Settings: settings,
		PlanMode: planMode, SessionID: sessionID, AllowAsk: allowAsk,
		MCP: mcp.Active(), Kanban: store, KanbanSource: src, GitSync: gs,
	})
}

// pruneSessions removes idle sessions older than the configured retention, in
// a best-effort goroutine so startup never blocks on it.
func runAgentCreate(ctx context.Context, description, model, providerName, workdir string, pol posture.Policy, settings config.Settings) error {
	resolver, err := newResolver(workdir)
	if err != nil {
		return err
	}
	cfg, err := run.ResolveWithSource(model, providerName, os.Getenv, resolver)
	if err != nil {
		return err
	}
	cfg, err = withClassifier(cfg, settings, resolver)
	if err != nil {
		return err
	}
	classifier := run.NewRoleClassifier(cfg, httpclient.Default(), nil)
	b := agentprofile.Builder{Classifier: classifier, MaxAttempts: 3, Caveman: settings.ClassifierCavemanEnabled()}
	profile, err := b.Build(ctx, description)
	if err != nil {
		return err
	}
	path, err := agentprofile.Save(profile)
	if err != nil {
		return err
	}
	fmt.Println(path)
	return nil
}

func runAgentForeground(ctx context.Context, name, model, providerName, workdir string, pol posture.Policy, settings config.Settings) error {
	resolver, err := newResolver(workdir)
	if err != nil {
		return err
	}
	cfg, err := run.ResolveWithSource(model, providerName, os.Getenv, resolver)
	if err != nil {
		return err
	}
	cfg, err = withClassifier(cfg, settings, resolver)
	if err != nil {
		return err
	}
	profile, err := agentprofile.Load(name)
	if err != nil {
		return err
	}
	mgr := bgagent.NewManager(workdir, cfg, httpclient.Default(), settings, pol)
	mgr.SetCredentialSource(resolver)
	sessionID := session.MustID()
	mgr.SetSessionID(sessionID)
	// The board, as every other headless session has it: without it a
	// foreground agent had no kanban tools at all.
	mgr.SetKanban(cliKanban(workdir, sessionID, settings))
	defer flushKanban(settings, workdir)
	if err := mgr.Start(name, profile); err != nil {
		return err
	}
	inst, ok := mgr.Lookup(name)
	if !ok {
		return fmt.Errorf("agent %q not found after start", name)
	}
	enc := json.NewEncoder(os.Stdout)
	for e := range inst.Events {
		if err := enc.Encode(e); err != nil {
			return err
		}
	}
	return nil
}

func pruneSessions(settings config.Settings, skipKey session.Key, skipID string) {
	store, err := session.NewStore()
	if err != nil {
		return
	}
	var skip string
	if skipKey != "" && skipID != "" {
		skip = filepath.Join(store.Root, string(skipKey), skipID+".jsonl")
	}
	_, _ = store.Prune(time.Duration(settings.SessionRetention())*24*time.Hour, skip)
}

// continueLatest resolves the most recent session for the current project, the
// -continue / -c entry point. AllSessions sorts each group's sessions by
// ModTime desc, so the first entry of the current group is the newest.
func continueLatest(store *session.Store, cur session.Key) (session.Key, string, error) {
	groups, err := store.AllSessions(cur)
	if err != nil {
		return "", "", err
	}
	for _, g := range groups {
		if g.Key != cur {
			continue
		}
		if len(g.Sessions) == 0 {
			return "", "", errors.New("no sessions to continue for this project")
		}
		return g.Key, g.Sessions[0].ID, nil
	}
	return "", "", errors.New("no sessions to continue for this project")
}

// usageJSONPath is the -usage-json flag: where a headless -prompt run writes
// its token-usage summary. Empty means no summary.
var usageJSONPath string

// noTranscript is the -no-transcript flag: a headless -prompt run keeps no
// session transcript.
var noTranscript bool

// recordUsage opens the token-usage ledger for a headless run and registers
// the run package's usage observer, returning the func that detaches it and
// flushes the ledger. A ledger that cannot be opened disables recording for
// this run rather than failing it. A non-nil sum also receives every event
// (the -usage-json summary), whether or not the ledger opened.
func recordUsage(sessionID string, settings config.Settings, sum *run.UsageSummary) func() {
	var rec *budget.Recorder
	if path, err := budget.DefaultPath(); err == nil {
		retention := 0
		if settings.SessionRetentionDays != nil {
			retention = *settings.SessionRetentionDays
		}
		if r, err := budget.Open(path, sessionID, retention); err == nil {
			rec = r
		}
	}
	if rec == nil && sum == nil {
		return func() {}
	}
	cancel := run.SetUsageObserver(func(ev run.UsageEvent) {
		if rec != nil {
			rec.Add(ev.Provider, ev.Model, int64(ev.Tokens))
		}
		if sum != nil {
			sum.Add(ev)
		}
	})
	return func() {
		cancel()
		if rec != nil {
			_ = rec.Close()
		}
	}
}

// exitProcess stops any local decision server this process launched, then
// exits. A server another belai process launched is left running for it.
func exitProcess(code int) {
	decisionserver.StopAll()
	os.Exit(code)
}
