package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/vulnetix/signet/internal/agent"
	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/credentials"
	"github.com/vulnetix/signet/internal/guardrails"
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
	allowUnpermittedTools := flag.Bool("allow-unpermitted-tools", false, "ignore permission blocks")
	allowAskWithoutTTY := flag.Bool("allow-ask-without-tty", false, "ignore ask-without-tty blocks")
	allowInvalidSkills := flag.Bool("allow-invalid-skills", false, "ignore invalid skill validation")
	allowInvalidHooks := flag.Bool("allow-invalid-hooks", false, "ignore invalid hook validation")
	dangerouslyYolo := flag.Bool("dangerously-yolo-everything", false, "ignore every posture gate")
	enableTools := flag.Bool("tools", false, "enable tool execution")
	effort := flag.String("effort", "", "thinking effort level: low, medium, or high")
	caveman := flag.Bool("caveman", false, "enable caveman voice rewrite for this run")
	sessionRetentionDays := flag.Int("session-retention-days", 0, "idle session retention in days (default 28)")
	noPrune := flag.Bool("no-prune", false, "never prune idle sessions")
	planMode := flag.Bool("plan", false, "start in plan mode (read-only)")
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

	if *prompt != "" {
		if err := runPromptOrTUI(*prompt, *model, *provider, *detectMode, *verbose, workdir, pol, *enableTools, *planMode, settings); err != nil {
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

func runPromptOrTUI(prompt, model, providerName string, detectMode, verbose bool, workdir string, pol posture.Policy, enableTools, planMode bool, settings config.Settings) error {
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

	var res run.Result
	if enableTools {
		res, err = runAgent(cfg, prompt, http.DefaultClient, pol, workdir, settings, planMode)
	} else {
		res, err = run.EngageWithPosture(context.Background(), cfg, prompt, detectMode, http.DefaultClient, pol)
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

func runAgent(cfg run.Config, userPrompt string, client *http.Client, pol posture.Policy, workdir string, settings config.Settings, planMode bool) (run.Result, error) {
	reg := tools.Default(workdir, settings.BashReadOnlyEnabled())

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
	})
	if err != nil {
		return run.Result{}, err
	}
	return sess.Run(context.Background(), userPrompt)
}

// pruneSessions removes idle sessions older than the configured retention, in
// a best-effort goroutine so startup never blocks on it.
func pruneSessions(settings config.Settings) {
	store, err := session.NewStore()
	if err != nil {
		return
	}
	_, _ = store.Prune(time.Duration(settings.SessionRetention()) * 24 * time.Hour)
}
