package main

import (
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/credentials"
	"github.com/vulnetix/signet/internal/run"
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
	flag.Parse()

	if *showVersion {
		fmt.Println(version.Version)
		os.Exit(0)
	}

	workdir, _ := os.Getwd()

	if *prompt != "" {
		if err := runPromptOrTUI(*prompt, *model, *provider, *detectMode, *verbose, workdir); err != nil {
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
		if err := tui.Start(tui.Options{Workdir: workdir, Resolver: resolver}); err != nil {
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

func runPromptOrTUI(prompt, model, providerName string, detectMode, verbose bool, workdir string) error {
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
			return tui.Start(tui.Options{Workdir: workdir, Resolver: resolver, Prompt: prompt})
		}
		// Without a TTY, fail closed listing every location searched.
		var nce2 *run.NotConfiguredError
		if errors.As(err, &nce2) {
			return fmt.Errorf("%s requires %s (looked in: %s)", nce2.Provider, strings.Join(nce2.EnvHints, ", "), strings.Join(nce2.Searched, ", "))
		}
		return err
	}
	res, err := run.Engage(cfg, prompt, detectMode, http.DefaultClient)
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
