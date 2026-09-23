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
	"syscall"
	"time"

	"github.com/vulnetix/signet/internal/agent"
	"github.com/vulnetix/signet/internal/agentprofile"
	"github.com/vulnetix/signet/internal/bgagent"
	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/credentials"
	"github.com/vulnetix/signet/internal/httpclient"
	"github.com/vulnetix/signet/internal/permissions"
	"github.com/vulnetix/signet/internal/posture"
	"github.com/vulnetix/signet/internal/prompt"
	"github.com/vulnetix/signet/internal/repoindex"
	"github.com/vulnetix/signet/internal/repomap"
	"github.com/vulnetix/signet/internal/rolemanager"
	"github.com/vulnetix/signet/internal/run"
	"github.com/vulnetix/signet/internal/session"
	"github.com/vulnetix/signet/internal/tools"
	"github.com/vulnetix/signet/internal/trustgate"
	"github.com/vulnetix/signet/internal/tui"
	"github.com/vulnetix/signet/internal/version"
)

func main() {
	_, _ = config.Migrate()

	// One root context for every non-TUI entry point. Goal mode's pass loop is
	// unbounded by design, so an interruptible context is the only thing that
	// can stop it: without this, SIGINT kills the process outright, leaving no
	// clean stop and no session entry. A second signal hard-exits, because a
	// pass boundary may still be seconds away.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go hardExitOnSecondSignal(ctx)

	showVersion := flag.Bool("version", false, "print version and exit")
	trustDir := flag.Bool("trust-dir", false, "trust the current directory without prompting")
	prompt := flag.String("prompt", "", "send a noninteractive prompt and print the reply, then exit")
	model := flag.String("model", "", "model id (defaults per provider)")
	provider := flag.String("provider", "", "provider (default openrouter): openai, anthropic, cloudflare-workers-ai, cloudflare-ai-gateway, openrouter, google-gemini, ollama, llama-server, github-copilot, huggingface, or a custom name from settings.json")
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
	firewall := flag.Bool("firewall", false, "route LLM traffic through the Vulnetix AI Firewall")
	enableTools := flag.Bool("tools", true, "enable tool execution; pass -tools=false to disable")
	effort := flag.String("effort", "", "thinking effort level: low, medium, or high")
	classifierProvider := flag.String("classifier-provider", "", "security-classifier provider (default: the main provider)")
	classifierModel := flag.String("classifier-model", "", "security-classifier model (default: the main model)")
	classifierEffort := flag.String("classifier-effort", "", "security-classifier thinking effort (default: none)")
	classifierKind := flag.String("classifier-kind", "", "security-classifier stack: llm or models (default: models when the binary embeds a model, else llm)")
	classifierPhase1Model := flag.String("classifier-phase1-model", "", "phase-1 prompt-saturation model id")
	classifierPhase1Source := flag.String("classifier-phase1-source", "", "phase-1 source: embedded or huggingface")
	classifierPhase1Threshold := flag.Float64("classifier-phase1-threshold", 0, "phase-1 attack threshold (default 0.75)")
	classifierPhase2Model := flag.String("classifier-phase2-model", "", "phase-2 jailbreak model id")
	classifierPhase2Source := flag.String("classifier-phase2-source", "", "phase-2 source: embedded, huggingface, or disabled")
	classifierPhase2Threshold := flag.Float64("classifier-phase2-threshold", 0, "phase-2 attack threshold (default 0.75)")
	caveman := flag.Bool("caveman", false, "enable caveman voice rewrite for this run")
	sessionRetentionDays := flag.Int("session-retention-days", 0, "idle session retention in days (default 28)")
	noPrune := flag.Bool("no-prune", false, "never prune idle sessions")
	planMode := flag.Bool("plan", false, "start in plan mode (read-only)")
	agentName := flag.String("agent", "", "start a background agent by name in foreground mode")
	agentCreate := flag.String("agent-create", "", "create an agent profile from a description and save to disk")
	resume := flag.String("resume", "", "resume a session by id or unique id prefix")
	flag.StringVar(resume, "r", "", "shorthand for -resume")
	continueLast := flag.String("continue", "", "continue the most recent session for this project")
	flag.StringVar(continueLast, "c", "", "shorthand for -continue")
	flag.Parse()

	if *showVersion {
		fmt.Println(version.Version)
		os.Exit(0)
	}

	if *resume != "" && *prompt != "" {
		fmt.Fprintln(os.Stderr, "signet: -resume requires the interactive TUI (not supported with -prompt)")
		os.Exit(1)
	}
	if *continueLast != "" && *resume != "" {
		fmt.Fprintln(os.Stderr, "signet: -continue cannot be combined with -resume")
		os.Exit(1)
	}
	if *continueLast != "" && *prompt != "" {
		fmt.Fprintln(os.Stderr, "signet: -continue requires the interactive TUI (not supported with -prompt)")
		os.Exit(1)
	}

	workdir, _ := os.Getwd()

	// First-run trust gate: block on an unknown directory before any repo
	// content is read, any process is auto-started, or any model turn runs.
	st, terr := trustgate.Check(workdir)
	switch {
	case terr != nil || st.NeedsPrompt():
		if *trustDir && !st.Trusted {
			// Grant trust to the directory only; proposed workspace_dirs are
			// not accepted, so the flag can never silently widen the sandbox.
			if err := trustgate.Grant(workdir, nil); err != nil {
				fmt.Fprintln(os.Stderr, "signet: trust directory:", err)
				os.Exit(1)
			}
			if len(st.NewDirs) > 0 {
				fmt.Fprintf(os.Stderr, "signet: trusted %s; skipping proposed workspace directories: %s\n",
					workdir, strings.Join(st.NewDirs, ", "))
			}
		} else if interactive(isCharDevice(os.Stdout), isCharDevice(os.Stdin), os.Getenv) {
			ok, err := tui.RunTrustGate(st)
			if err != nil {
				fmt.Fprintln(os.Stderr, "signet: trust dialog:", err)
				os.Exit(1)
			}
			if !ok && !st.Trusted {
				os.Exit(1)
			}
		} else {
			// Headless fails closed: no model turn runs in an untrusted
			// directory without an explicit opt-in.
			fmt.Fprintf(os.Stderr, "signet: %s is not a trusted workspace.\n"+
				"Run `signet` here once to review and trust it, or `signet -trust-dir`.\n", workdir)
			os.Exit(1)
		}
	}

	settings, err := config.LoadMerged(workdir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "signet: load settings:", err)
		os.Exit(1)
	}
	if *effort != "" {
		settings.Effort = *effort
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
	if *firewall || os.Getenv("SIGNET_FIREWALL") == "1" || os.Getenv("SIGNET_FIREWALL") == "true" {
		if settings.Vulnetix == nil {
			settings.Vulnetix = &config.VulnetixSettings{}
		}
		t := true
		settings.Vulnetix.FirewallEnabled = &t
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
		fmt.Fprintln(os.Stderr, "signet: load embedded classifier:", err)
		os.Exit(1)
	}

	// Resolve --resume / --continue before the TUI starts so a bad id (or an
	// empty project) exits non-zero with a message instead of dropping the user
	// into a TUI to discover the failure.
	var resumeKey session.Key
	var resumeID string
	if *resume != "" || *continueLast != "" {
		store, err := session.NewStore()
		if err != nil {
			fmt.Fprintln(os.Stderr, "signet:", err)
			os.Exit(1)
		}
		cur, _ := session.KeyFor(workdir)
		if *resume != "" {
			resumeKey, resumeID, err = store.ResolveAnywhere(cur, *resume)
			if err != nil {
				fmt.Fprintln(os.Stderr, "signet:", err)
				os.Exit(1)
			}
		} else {
			resumeKey, resumeID, err = continueLatest(store, cur)
			if err != nil {
				fmt.Fprintln(os.Stderr, "signet:", err)
				os.Exit(1)
			}
		}
	}

	if !*noPrune {
		go pruneSessions(settings, resumeKey, resumeID)
	}

	if *agentCreate != "" {
		if err := runAgentCreate(ctx, *agentCreate, *model, *provider, workdir, pol, settings); err != nil {
			fmt.Fprintln(os.Stderr, "signet:", err)
			os.Exit(1)
		}
		os.Exit(0)
	}

	if *agentName != "" {
		if err := runAgentForeground(ctx, *agentName, *model, *provider, workdir, pol, settings); err != nil {
			fmt.Fprintln(os.Stderr, "signet:", err)
			os.Exit(1)
		}
		os.Exit(0)
	}

	if *prompt != "" {
		if err := runPromptOrTUI(ctx, *prompt, *model, *provider, *detectMode, *verbose, workdir, pol, *enableTools, *planMode, settings); err != nil {
			fmt.Fprintln(os.Stderr, "signet:", err)
			os.Exit(1)
		}
		os.Exit(0)
	}

	if interactive(isCharDevice(os.Stdout), isCharDevice(os.Stdin), os.Getenv) {
		resolver, err := credentials.NewResolver(workdir)
		if err != nil {
			fmt.Fprintln(os.Stderr, "signet:", err)
			os.Exit(1)
		}
		if err := tui.Start(tui.Options{Workdir: workdir, Resolver: resolver, Provider: *provider, Model: *model, Settings: &settings, Posture: pol, PlanMode: *planMode, ResumeKey: resumeKey, ResumeSession: resumeID}); err != nil {
			fmt.Fprintln(os.Stderr, "signet:", err)
			os.Exit(1)
		}
		os.Exit(0)
	}

	fmt.Println("signet", version.Version)
}

// hardExitOnSecondSignal waits for the first signal to cancel ctx, then exits
// immediately on the next one. The graceful path unwinds at a pass boundary,
// which can be seconds away; a user pressing ctrl+c twice means "now".
func hardExitOnSecondSignal(ctx context.Context) {
	<-ctx.Done()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
	os.Exit(130)
}

func interactive(stdoutTTY, stdinTTY bool, env func(string) string) bool {
	if env("SIGNET_NO_TUI") != "" || env("CI") != "" {
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
	if rc, err := run.ResolveRouting(cfg, settings.Routing, src); err == nil {
		cfg.Routing = rc
	}
	return cfg, nil
}

func runPromptOrTUI(ctx context.Context, prompt, model, providerName string, detectMode, verbose bool, workdir string, pol posture.Policy, enableTools, planMode bool, settings config.Settings) error {
	resolver, err := credentials.NewResolver(workdir)
	if err != nil {
		return err
	}
	cfg, err := run.ResolveWithSource(model, providerName, os.Getenv, resolver)
	if err != nil {
		var nce *run.NotConfiguredError
		if errors.As(err, &nce) && interactive(isCharDevice(os.Stdout), isCharDevice(os.Stdin), os.Getenv) {
			fmt.Fprintf(os.Stderr, "signet: no credentials for %s (missing %s). Opening the credential manager…\n",
				nce.Provider, strings.Join(nce.Missing, ", "))
			return tui.Start(tui.Options{Workdir: workdir, Resolver: resolver, Prompt: prompt, Provider: providerName, Model: model, Settings: &settings, Posture: pol, PlanMode: planMode})
		}
		// Without a TTY, fail closed naming every location searched.
		return err
	}
	cfg, err = withClassifier(cfg, settings, resolver)
	if err != nil {
		return err
	}

	var res run.Result
	if detectMode || !enableTools {
		res, err = run.EngageWithPosture(ctx, cfg, prompt, detectMode, httpclient.Default(), pol)
	} else {
		res, err = runAgent(ctx, cfg, prompt, httpclient.Default(), pol, workdir, settings, planMode)
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

func runAgent(ctx context.Context, cfg run.Config, userPrompt string, client *http.Client, pol posture.Policy, workdir string, settings config.Settings, planMode bool) (run.Result, error) {
	caps := tools.DetectDefault()
	ix := repoindex.Scan(ctx, workdir)
	reg := tools.DefaultWithCaps(workdir, settings.ReadOnlyEnabled(), caps, ix)

	perms := permissions.From(settings.Permissions.Allow, settings.Permissions.Ask, settings.Permissions.Deny)
	repoMap := repomap.Scan(ctx, workdir)

	var promptOpts prompt.Options
	if settings.Caveman != nil && *settings.Caveman {
		promptOpts.Caveman = true
	}

	sess, err := agent.NewSession(agent.Options{
		Cfg:           cfg,
		Client:        client,
		Registry:      reg,
		Perms:         perms,
		Posture:       pol,
		PlanMode:      planMode,
		Workdir:       workdir,
		Settings:      settings,
		PromptOptions: promptOpts,
		Caps:          caps,
		RepoIndex:     ix,
		PlanSurface:   tools.PlanSurface{GuardrailsOff: !settings.GuardrailsEnabled(), Perms: perms},
		AskDisabled:   !settings.AskPermissionEnabled(),
		// Top-level session: explore subagents may fan out from here. A
		// subagent sets this false so it can never fan out again.
		AllowExplore: true,
		// Top-level goal-mode prompts may run the unbounded pass loop; a
		// subagent never does.
		AllowPassLoop: true,
		RepoMap:       &repoMap,
		// Headless CLI: live language servers are off, but fallback syntax
		// checks still run when enabled in settings.
		Diagnostics: rolemanager.DiagnosticsGateFromSettings(settings, reg.Cwd().Roots(), false),
	})
	if err != nil {
		return run.Result{}, err
	}
	return sess.Run(ctx, userPrompt)
}

// pruneSessions removes idle sessions older than the configured retention, in
// a best-effort goroutine so startup never blocks on it.
func runAgentCreate(ctx context.Context, description, model, providerName, workdir string, pol posture.Policy, settings config.Settings) error {
	resolver, err := credentials.NewResolver(workdir)
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
	resolver, err := credentials.NewResolver(workdir)
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
