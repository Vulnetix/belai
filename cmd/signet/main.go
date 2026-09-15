package main

import (
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/vulnetix/signet/internal/agent"
	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/credentials"
	"github.com/vulnetix/signet/internal/permissions"
	"github.com/vulnetix/signet/internal/posture"
	"github.com/vulnetix/signet/internal/prompt"
	"github.com/vulnetix/signet/internal/run"
	"github.com/vulnetix/signet/internal/tools"
	"github.com/vulnetix/signet/internal/tui"
	"github.com/vulnetix/signet/internal/version"
)

func main() {
	_, _ = config.Migrate()

	showVersion := flag.Bool("version", false, "print version and exit")
	prompt := flag.String("prompt", "", "send a noninteractive prompt and print the reply, then exit")
	model := flag.String("model", "", "model id (defaults per provider)")
	provider := flag.String("provider", "", "provider: openai, anthropic, cloudflare-workers-ai, or cloudflare-ai-gateway")
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

	if *prompt != "" {
		if err := runPromptOrTUI(*prompt, *model, *provider, *detectMode, *verbose, workdir, pol, *enableTools, settings); err != nil {
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
		if err := tui.Start(tui.Options{Workdir: workdir, Resolver: resolver, Provider: *provider, Model: *model, Settings: &settings}); err != nil {
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

func runPromptOrTUI(prompt, model, providerName string, detectMode, verbose bool, workdir string, pol posture.Policy, enableTools bool, settings config.Settings) error {
	cfg, err := run.Resolve(model, providerName, os.Getenv)
	if err != nil {
		var nce *run.NotConfiguredError
		if errors.As(err, &nce) && interactive(isCharDevice(os.Stdout), isCharDevice(os.Stdin), os.Getenv) {
			fmt.Fprintf(os.Stderr, "signet: no credentials for %s (missing %s). Opening the credential manager…\n",
				nce.Provider, strings.Join(nce.Missing, ", "))
			resolver, err := credentials.NewResolver(workdir)
			if err != nil {
				return err
			}
			return tui.Start(tui.Options{Workdir: workdir, Resolver: resolver, Prompt: prompt, Provider: providerName, Model: model, Settings: &settings})
		}
		// Without a TTY, fail closed listing every location searched.
		var nce2 *run.NotConfiguredError
		if errors.As(err, &nce2) {
			return fmt.Errorf("%s requires %s (looked in: %s)", nce2.Provider, strings.Join(nce2.EnvHints, ", "), strings.Join(nce2.Searched, ", "))
		}
		return err
	}

	var res run.Result
	if enableTools {
		res, err = runAgent(cfg, prompt, http.DefaultClient, pol, workdir, settings)
	} else {
		res, err = run.EngageWithPosture(cfg, prompt, detectMode, http.DefaultClient, pol)
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

func runAgent(cfg run.Config, userPrompt string, client *http.Client, pol posture.Policy, workdir string, settings config.Settings) (run.Result, error) {
	reg := tools.Default(workdir)

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
		Workdir:       workdir,
		Settings:      settings,
		PromptOptions: promptOpts,
	})
	if err != nil {
		return run.Result{}, err
	}
	return sess.Run(nil, userPrompt)
}
