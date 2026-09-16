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
	"strings"
	"syscall"
	"time"

	"github.com/vulnetix/signet/internal/agent"
	"github.com/vulnetix/signet/internal/agentprofile"
	"github.com/vulnetix/signet/internal/bgagent"
	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/credentials"
	"github.com/vulnetix/signet/internal/guardrails"
	"github.com/vulnetix/signet/internal/httpclient"
	"github.com/vulnetix/signet/internal/permissions"
	"github.com/vulnetix/signet/internal/posture"
	"github.com/vulnetix/signet/internal/prompt"
	"github.com/vulnetix/signet/internal/run"
	"github.com/vulnetix/signet/internal/session"
	"github.com/vulnetix/signet/internal/tools"
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
	prompt := flag.String("prompt", "", "send a noninteractive prompt and print the reply, then exit")
	model := flag.String("model", "", "model id (defaults per provider)")
	provider := flag.String("provider", "", "provider: openai, anthropic, cloudflare-workers-ai, cloudflare-ai-gateway, openrouter, google-gemini, ollama, github-copilot, or a custom name from settings.json")
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
	enableTools := flag.Bool("tools", false, "enable tool execution")
	effort := flag.String("effort", "", "thinking effort level: low, medium, or high")
	classifierProvider := flag.String("classifier-provider", "", "security-classifier provider (default: the main provider)")
	classifierModel := flag.String("classifier-model", "", "security-classifier model (default: the main model)")
	classifierEffort := flag.String("classifier-effort", "", "security-classifier thinking effort (default: none)")
	caveman := flag.Bool("caveman", false, "enable caveman voice rewrite for this run")
	sessionRetentionDays := flag.Int("session-retention-days", 0, "idle session retention in days (default 28)")
	noPrune := flag.Bool("no-prune", false, "never prune idle sessions")
	planMode := flag.Bool("plan", false, "start in plan mode (read-only)")
	agentName := flag.String("agent", "", "start a background agent by name in foreground mode")
	agentCreate := flag.String("agent-create", "", "create an agent profile from a description and save to disk")
	flag.Parse()

	if *showVersion {
		fmt.Println(version.Version)
		os.Exit(0)
	}

	workdir, _ := os.Getwd()

	settings, err := config.LoadMerged(workdir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "signet: load settings:", err)
		os.Exit(1)
	}
	if *effort != "" {
		settings.Effort = *effort
	}
	if *classifierProvider != "" || *classifierModel != "" || *classifierEffort != "" {
		if settings.Classifier == nil {
			settings.Classifier = &config.ClassifierSettings{}
		}
		settings.Classifier.Provider = *classifierProvider
		settings.Classifier.Model = *classifierModel
		settings.Classifier.Effort = *classifierEffort
	}
	if *caveman {
		t := true
		settings.Caveman = &t
	}
	if *sessionRetentionDays > 0 {
		settings.SessionRetentionDays = sessionRetentionDays
	}

	if !*noPrune {
		go pruneSessions(settings)
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
	posture.PrintBanner(pol, os.Stderr)

	// Discover the guardrails provider when credentials are present and no
	// explicit provider or base URL was given.
	if *provider == "" && os.Getenv("SIGNET_BASE_URL") == "" &&
		os.Getenv("VULNETIX_API_KEY") != "" && os.Getenv("VULNETIX_ORG") != "" {
		if _, err := guardrails.Configure(os.Getenv); err != nil {
			fmt.Fprintln(os.Stderr, "signet: guardrails discovery:", err)
		}
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
		if err := tui.Start(tui.Options{Workdir: workdir, Resolver: resolver, Provider: *provider, Model: *model, Settings: &settings, Posture: pol, PlanMode: *planMode}); err != nil {
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
	if enableTools {
		res, err = runAgent(ctx, cfg, prompt, httpclient.Default(), pol, workdir, settings, planMode)
	} else {
		res, err = run.EngageWithPosture(ctx, cfg, prompt, detectMode, httpclient.Default(), pol)
	}
	if err != nil {
		return err
	}
	if verbose {
		fmt.Fprintf(os.Stderr, "security: %s\n", res.SecuritySentinel)
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
	reg := tools.Default(workdir, settings.ReadOnlyEnabled())

	perms := permissions.From(settings.Permissions.Allow, settings.Permissions.Ask, settings.Permissions.Deny)

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
		// Top-level session: explore subagents may fan out from here. A
		// subagent sets this false so it can never fan out again.
		AllowExplore: true,
		// Top-level goal-mode prompts may run the unbounded pass loop; a
		// subagent never does.
		AllowPassLoop: true,
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
	classifier := run.NewClassifier(cfg, httpclient.Default())
	b := agentprofile.Builder{Classifier: classifier, MaxAttempts: 3}
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

func pruneSessions(settings config.Settings) {
	store, err := session.NewStore()
	if err != nil {
		return
	}
	_, _ = store.Prune(time.Duration(settings.SessionRetention()) * 24 * time.Hour)
}
