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
	"github.com/vulnetix/belai/internal/models"
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
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	// Nothing but protocol may follow on stdout, so a stray word is refused
	// here rather than starting a server the caller did not mean to run.
	if fs.NArg() > 0 {
		if fs.Arg(0) == "help" {
			fs.Usage()
			return 0
		}
		fmt.Fprintf(stderr, "belai acp: unexpected argument %q\n", fs.Arg(0))
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
	mcpMgr := mcp.StartAsync(ctx, effectiveMCP(global, wd), withBuiltinMCP(mcp.Options{
		Workdir:    wd,
		HTTPClient: httpclient.Default(),
		VulnetixAuth: func() (string, error) {
			return credentials.VulnetixAuthHeader(wd)
		},
		Sandbox: func() sandbox.Policy {
			return sandbox.FromSettings(global.Sandbox, []string{wd}, posture.Defaults())
		},
	}, global, wd))
	mcp.SetActive(mcpMgr)
	defer mcpMgr.Close()
	defer startTelemetry(global, wd)()
	// Kanban writes made over this connection are pushed once at the end.
	defer flushKanban(global, wd)
	defer recordUsage(session.MustID(), global, nil)()

	build := func(ctx context.Context, cwd, sessionID string) (*agent.Session, error) {
		return buildACPSession(ctx, cwd, sessionID, *providerName, *model)
	}
	opts := acp.Options{
		PostEnd: acpPostEnd(*providerName, *model),
		Models:  acpModelChoices,
		Switch: func(ctx context.Context, cwd, id, provider, model string, t acp.Toggles) (*agent.Session, error) {
			return buildACPSessionWith(ctx, cwd, id, provider, model, &t)
		},
		Toggles: acpToggles,
	}
	if !*noTranscript {
		// The transcript is the same private JSONL a TUI session keeps; it
		// writes to the state directory, never to stdout.
		opts.Transcript = func(cwd, id string) *turnlog.Log {
			l, _ := turnlog.Open(cwd, id, session.Meta{Cwd: cwd, Mode: "agent"})
			return l
		}
		// The transcript is mirrored to the Vulnetix website when session sync
		// is on and the Vulnetix CLI is logged in; never without a transcript.
		if m := newACPMirror(global, wd, *providerName, *model); m != nil {
			opts.Mirror = m
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
	return buildACPSessionWith(ctx, cwd, sessionID, providerName, model, nil)
}

// buildACPSessionWith is buildACPSession with the editor's session-only
// toggles applied over the user's settings. A nil t changes nothing.
func buildACPSessionWith(ctx context.Context, cwd, sessionID, providerName, model string, t *acp.Toggles) (*agent.Session, error) {
	st, err := trustgate.Check(cwd)
	if err != nil {
		return nil, fmt.Errorf("check trust for %s: %w", cwd, err)
	}
	if !st.Trusted {
		return nil, fmt.Errorf("%s is not trusted yet: run `belai` there once to review and trust it, or `belai -trust-dir` from that directory", cwd)
	}
	cfg, settings, pol, err := acpConfigWith(cwd, providerName, model, t)
	if err != nil {
		return nil, err
	}
	// No git sync: an editor owns this working copy and its open buffers.
	return newCLISession(ctx, cfg, httpclient.Default(), pol, cwd, settings, false, sessionID, true, nil)
}

// acpConfig resolves the model config, merged settings and effective posture
// for an editor session's directory. The trust check is the caller's.
func acpConfig(cwd, providerName, model string) (run.Config, config.Settings, posture.Policy, error) {
	return acpConfigWith(cwd, providerName, model, nil)
}

// acpConfigWith is acpConfig with an editor's session-only toggles applied
// over the merged settings before the posture and the session are derived from
// them, so a toggle reaches every gate the setting feeds. Nothing is saved.
func acpConfigWith(cwd, providerName, model string, t *acp.Toggles) (run.Config, config.Settings, posture.Policy, error) {
	settings, err := config.LoadMerged(cwd)
	if err != nil {
		return run.Config{}, config.Settings{}, nil, fmt.Errorf("load settings: %w", err)
	}
	if t != nil {
		settings.Guardrails, settings.AskPermission, settings.Caveman = &t.Guardrails, &t.Ask, &t.Caveman
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
	// No flag: the settings file, then the model the user last chose in the
	// TUI, exactly as a fleet worker resolves. Without this the provider
	// falls back to OpenAI whatever the user configured.
	wantProvider, wantModel := workerModel(providerName, model, settings, config.LoadState)
	cfg, err := run.ResolveWithSource(wantModel, wantProvider, os.Getenv, resolver)
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
		cfg, settings, pol, err := acpConfigWith(cwd, providerName, model, nil)
		if err != nil || !headless.ShouldPostEnd(ctx, settings, cwd, testpass.TriggerGoal) {
			return testpass.Outcome{}, false
		}
		return headless.RunPostEnd(ctx, headless.PostEnd{
			Cfg: cfg, Client: httpclient.Default(), Posture: pol, Workdir: cwd, Settings: settings,
			Session: sess, Trigger: testpass.TriggerGoal, Fix: fix, Notify: notify,
		}), true
	}
}

// acpModelChoices lists the provider and model pairs an editor may pick for a
// session: the catalogue of every provider the user has credentials for, plus
// the pair the session already runs on. Nothing here comes from the editor,
// and a pick is only ever matched against this list.
func acpModelChoices(cwd, curProvider, curModel string) []acp.ModelChoice {
	seen := map[string]bool{}
	var out []acp.ModelChoice
	add := func(provider, model, label string) {
		if provider == "" || model == "" || seen[provider+"/"+model] {
			return
		}
		seen[provider+"/"+model] = true
		if label == "" {
			label = model
		}
		out = append(out, acp.ModelChoice{Provider: provider, Model: model, Label: provider + " · " + label})
	}
	add(curProvider, curModel, curModel)
	if resolver, err := newResolver(cwd); err == nil {
		for _, name := range resolver.ConfiguredProviders() {
			for _, m := range models.Catalog(name) {
				add(name, m.ID, m.Label)
			}
		}
	}
	return out
}

// acpToggles is where an editor session's switches start: the user's own
// settings for the directory. A settings file that cannot load reads as the
// defaults (guardrails and asks on, caveman off), the fail-closed side.
func acpToggles(cwd string) acp.Toggles {
	s, err := config.LoadMerged(cwd)
	if err != nil {
		return acp.Toggles{Guardrails: true, Ask: true}
	}
	return acp.Toggles{Guardrails: s.GuardrailsEnabled(), Ask: s.AskPermissionEnabled(), Caveman: s.CavemanEnabled()}
}
