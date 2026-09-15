package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"

	"github.com/vulnetix/signet/internal/run"
	"github.com/vulnetix/signet/internal/version"
)

func main() {
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

	if *prompt != "" {
		if err := runPrompt(*prompt, *model, *provider, *detectMode, *verbose); err != nil {
			fmt.Fprintln(os.Stderr, "signet:", err)
			os.Exit(1)
		}
		os.Exit(0)
	}

	fmt.Println("signet", version.Version)
}

func runPrompt(prompt, model, providerName string, detectMode, verbose bool) error {
	cfg, err := run.Resolve(model, providerName, os.Getenv)
	if err != nil {
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
