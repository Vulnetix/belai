package headless

import (
	"context"
	"net/http"
	"strings"

	"github.com/vulnetix/belai/internal/agent"
	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/permissions"
	"github.com/vulnetix/belai/internal/posture"
	"github.com/vulnetix/belai/internal/repomap"
	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/sandbox"
	"github.com/vulnetix/belai/internal/testpass"
)

// PostEnd is one headless post-end test pass: the CLI's `-prompt` run and an
// ACP prompt turn share it, so both apply the same gates.
type PostEnd struct {
	Cfg      run.Config
	Client   *http.Client
	Posture  posture.Policy
	Workdir  string
	Settings config.Settings
	// Session is the session the run used. A fail-branch loop continues on it,
	// so every gate, permission rule and sandbox of the run applies.
	Session *agent.Session
	Trigger testpass.Trigger
	// Observe receives the fail-branch loop's events (the transcript, the
	// editor's stream). Optional.
	Observe func(agent.Event)
	// Fix overrides the fail-branch loop. ACP sets it so the loop's events
	// and permission asks reach the editor; nil continues on Session.
	Fix testpass.Fixer
	// Notify receives one harness line per step. Optional.
	Notify func(string)
}

// ShouldPostEnd reports whether the user's settings run the pass at trigger t
// for a working directory with changes. A session-end pass only runs when the
// tree has something to test, so an idle run never spends a suite.
func ShouldPostEnd(ctx context.Context, s config.Settings, workdir string, t testpass.Trigger) bool {
	if !testpass.Should(s, t) {
		return false
	}
	if t != testpass.TriggerSession {
		return true
	}
	m := repomap.Scan(ctx, workdir)
	return m.Dirty
}

// RunPostEnd runs the pass and returns its outcome. The suites come from a
// fresh repository scan (the run may have changed what is detected), the
// guardrails switch decides the posture the gate and the sandbox use, and the
// classifier for the fast report is the role classifier for the run's config.
func RunPostEnd(ctx context.Context, p PostEnd) testpass.Outcome {
	// Guardrails off means AllIgnore everywhere, as on every other surface.
	pol := p.Posture
	if !p.Settings.GuardrailsEnabled() {
		pol = posture.AllIgnore()
	}
	m := repomap.Scan(ctx, p.Workdir)
	perms := permissions.From(p.Settings.Permissions.Allow, p.Settings.Permissions.Ask, p.Settings.Permissions.Deny)
	cfg := testpass.Config{
		Settings: p.Settings,
		Dir:      p.Workdir,
		Suites:   m.TestSuites,
		Changed: func() []string {
			m.RefreshStatus(ctx)
			var out []string
			for _, c := range m.Changed {
				out = append(out, strings.TrimSpace(c.Path))
			}
			return out
		},
		Perms:   perms,
		Sandbox: sandbox.FromSettings(p.Settings.Sandbox, []string{p.Workdir}, pol),
		Gate:    testpass.NewGate(p.Cfg, p.Client, nil, pol),
		Report:  reportClassifier(p),
		Notify:  p.Notify,
	}
	if p.Settings.TestsOnFail() != "off" {
		switch {
		case p.Fix != nil:
			cfg.Fix = p.Fix
		case p.Session != nil:
			cfg.Fix = testpass.SessionFixer(p.Session, p.Observe)
		}
	}
	return testpass.Run(ctx, cfg, p.Trigger)
}

// reportClassifier is the fast-tier classifier for the pass report, or nil
// when the run has no usable classifier (the harness line is used then).
func reportClassifier(p PostEnd) rolemanager.Classifier {
	if p.Client == nil {
		return nil
	}
	return run.NewRoleClassifier(p.Cfg, p.Client, nil)
}
