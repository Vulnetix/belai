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
	"github.com/vulnetix/belai/internal/headless"
	"github.com/vulnetix/belai/internal/httpclient"
	"github.com/vulnetix/belai/internal/mcp"
	"github.com/vulnetix/belai/internal/posture"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/sandbox"
	"github.com/vulnetix/belai/internal/session"
	"github.com/vulnetix/belai/internal/testpass"
	"github.com/vulnetix/belai/internal/trustgate"
	"github.com/vulnetix/belai/internal/turnlog"
)

// runACP implements `belai acp`: the Agent Client Protocol on stdin and
// stdout. Nothing else may be written to stdout, so diagnostics go to
// stderr. It returns the exit code.
func runACP(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("acp", flag.ContinueOnError)
	fs.SetOutput(stderr)
	providerName := fs.String("provider", "", "provider (default: as for the TUI)")
	model := fs.String("model", "", "model id (default: the provider's)")
	noTranscript := fs.Bool("no-transcript", false, "do not keep a session transcript of editor sessions")
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
	// Kanban writes made over this connection are pushed once at the end.
	defer flushKanban(global, wd)
	defer recordUsage(session.MustID(), global, nil)()

	build := func(ctx context.Context, cwd, sessionID string) (*agent.Session, error) {
		return buildACPSession(ctx, cwd, sessionID, *providerName, *model)
	}
	opts := acp.Options{PostEnd: acpPostEnd(*providerName, *model)}
	if !*noTranscript {
		// The transcript is the same private JSONL a TUI session keeps; it
		// writes to the state directory, never to stdout.
		opts.Transcript = func(cwd, id string) *turnlog.Log {
			l, _ := turnlog.Open(cwd, id, session.Meta{Cwd: cwd, Mode: "agent"})
			return l
		}
	}
	if err := acp.ServeWith(ctx, stdin, stdout, build, opts); err != nil {
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
	cfg, settings, pol, err := acpConfig(cwd, providerName, model)
	if err != nil {
		return nil, err
	}
	return newCLISession(ctx, cfg, httpclient.Default(), pol, cwd, settings, false, sessionID, true)
}

// acpConfig resolves the model config, merged settings and effective posture
// for an editor session's directory. The trust check is the caller's.
func acpConfig(cwd, providerName, model string) (run.Config, config.Settings, posture.Policy, error) {
	settings, err := config.LoadMerged(cwd)
	if err != nil {
		return run.Config{}, config.Settings{}, nil, fmt.Errorf("load settings: %w", err)
	}
	projectPol, _ := posture.Load(cwd)
	pol := posture.Defaults().Override(projectPol)
	if !settings.GuardrailsEnabled() {
		pol = posture.AllIgnore()
	}
	resolver, err := newResolver(cwd)
	if err != nil {
		return run.Config{}, settings, pol, err
	}
	cfg, err := run.ResolveWithSource(model, providerName, os.Getenv, resolver)
	if err != nil {
		return run.Config{}, settings, pol, err
	}
	cfg, err = withClassifier(cfg, settings, resolver)
	if err != nil {
		return run.Config{}, settings, pol, err
	}
	return cfg, settings, pol, nil
}

// acpPostEnd is the ACP server's post-end test hook. It re-resolves the
// session directory's settings (the session was built from the same ones), so
// the user's `tests` block and posture apply exactly as for a headless run,
// and returns false when the settings do not run the pass.
func acpPostEnd(providerName, model string) func(context.Context, string, *agent.Session, testpass.Fixer, func(string)) (testpass.Outcome, bool) {
	return func(ctx context.Context, cwd string, sess *agent.Session, fix testpass.Fixer, notify func(string)) (testpass.Outcome, bool) {
		cfg, settings, pol, err := acpConfig(cwd, providerName, model)
		if err != nil || !headless.ShouldPostEnd(ctx, settings, cwd, testpass.TriggerGoal) {
			return testpass.Outcome{}, false
		}
		return headless.RunPostEnd(ctx, headless.PostEnd{
			Cfg: cfg, Client: httpclient.Default(), Posture: pol, Workdir: cwd, Settings: settings,
			Session: sess, Trigger: testpass.TriggerGoal, Fix: fix, Notify: notify,
		}), true
	}
}
