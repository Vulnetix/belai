package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/vulnetix/belai/internal/acp"
	"github.com/vulnetix/belai/internal/agent"
	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/credentials"
	"github.com/vulnetix/belai/internal/httpclient"
	"github.com/vulnetix/belai/internal/mcp"
	"github.com/vulnetix/belai/internal/posture"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/sandbox"
	"github.com/vulnetix/belai/internal/session"
	"github.com/vulnetix/belai/internal/trustgate"
)

// runACP implements `belai acp`: the Agent Client Protocol on stdin and
// stdout. Nothing else may be written to stdout, so diagnostics go to
// stderr. It returns the exit code.
func runACP(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("acp", flag.ContinueOnError)
	fs.SetOutput(stderr)
	providerName := fs.String("provider", "", "provider (default: as for the TUI)")
	model := fs.String("model", "", "model id (default: the provider's)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	wd, _ := os.Getwd()
	global, err := config.LoadMerged(wd)
	if err != nil {
		fmt.Fprintln(stderr, "belai acp: load settings:", err)
		return 1
	}
	if err := run.PreloadClassifier(run.ResolveSecurityClassifier(global.Classifier)); err != nil {
		fmt.Fprintln(stderr, "belai acp: load embedded classifier:", err)
		return 1
	}
	// The mcp key is read from the user's own settings only, so one set of
	// servers serves every session on this connection.
	mcpMgr := mcp.StartAsync(ctx, global.MCP, mcp.Options{
		Workdir:    wd,
		HTTPClient: httpclient.Default(),
		VulnetixAuth: func() (string, error) {
			return credentials.VulnetixAuthHeader(wd)
		},
		Sandbox: func() sandbox.Policy {
			return sandbox.FromSettings(global.Sandbox, []string{wd}, posture.Defaults())
		},
	})
	mcp.SetActive(mcpMgr)
	defer mcpMgr.Close()
	defer startTelemetry(global, wd)()
	defer recordUsage(session.MustID(), global)()

	build := func(ctx context.Context, cwd, sessionID string) (*agent.Session, error) {
		return buildACPSession(ctx, cwd, sessionID, *providerName, *model)
	}
	if err := acp.Serve(ctx, stdin, stdout, build); err != nil {
		fmt.Fprintln(stderr, "belai acp:", err)
		return 1
	}
	return 0
}

// buildACPSession builds one editor session. The directory must already be
// trusted: the trust prompt never runs over ACP, so an untrusted directory
// fails closed with instructions.
func buildACPSession(ctx context.Context, cwd, sessionID, providerName, model string) (*agent.Session, error) {
	st, err := trustgate.Check(cwd)
	if err != nil {
		return nil, fmt.Errorf("check trust for %s: %w", cwd, err)
	}
	if !st.Trusted {
		return nil, fmt.Errorf("%s is not trusted yet: run `belai` there once to review and trust it, or `belai -trust-dir` from that directory", cwd)
	}
	settings, err := config.LoadMerged(cwd)
	if err != nil {
		return nil, fmt.Errorf("load settings: %w", err)
	}
	projectPol, _ := posture.Load(cwd)
	pol := posture.Defaults().Override(projectPol)
	if !settings.GuardrailsEnabled() {
		pol = posture.AllIgnore()
	}
	resolver, err := credentials.NewResolver(cwd)
	if err != nil {
		return nil, err
	}
	cfg, err := run.ResolveWithSource(model, providerName, os.Getenv, resolver)
	if err != nil {
		return nil, err
	}
	cfg, err = withClassifier(cfg, settings, resolver)
	if err != nil {
		return nil, err
	}
	return newCLISession(ctx, cfg, httpclient.Default(), pol, cwd, settings, false, sessionID, true)
}
