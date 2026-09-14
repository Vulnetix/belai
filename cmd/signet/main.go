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
	flag.Parse()

	if *showVersion {
		fmt.Println(version.Version)
		os.Exit(0)
	}

	if *prompt != "" {
		if err := runPrompt(*prompt, *model, *provider); err != nil {
			fmt.Fprintln(os.Stderr, "signet:", err)
			os.Exit(1)
		}
		os.Exit(0)
	}

	fmt.Println("signet", version.Version)
}

func runPrompt(prompt, model, providerName string) error {
	cfg, err := run.Resolve(model, providerName, os.Getenv)
	if err != nil {
		return err
	}
	out, err := run.Run(cfg, prompt, http.DefaultClient)
	if err != nil {
		return err
	}
	fmt.Println(out)
	return nil
}
