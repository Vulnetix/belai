package tui

import (
	"github.com/vulnetix/belai/internal/agent"
	"github.com/vulnetix/belai/internal/clefmcp"
	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/run"
)

// answerFromClef is the source of an ask the decision model answered in the
// user's place (mcp.builtin.clef.skip_ask).
const answerFromClef = "clef"

// askDeciderFor builds the decision engine a session built from this snapshot
// uses to answer asks. The settings and credentials are the snapshot's own: a
// change in /model rebuilds the session. It returns nil when no engine is
// wanted, so the session asks the user every time.
func askDeciderFor(s config.Settings, src run.CredentialSource) agent.AskDecider {
	eng := &clefmcp.Engine{
		Settings: func() config.Settings { return s },
		Creds:    func() run.CredentialSource { return src },
	}
	if _, on := eng.AskPolicy(); !on {
		return nil
	}
	return eng
}

// handleAskDecided records an ask the decision model answered, with its
// confidence and no question text, and says so in the transcript. The ask never
// reached the user, so there is no view to close.
func (a *App) handleAskDecided(d *agent.AskDecided) {
	if d == nil {
		return
	}
	kind := askClarify
	switch d.Kind {
	case agent.AskKindPermission:
		kind = askPermission
	case agent.AskKindModeChoice:
		kind = askModeChoice
	}
	meta := map[string]any{"decided_by": answerFromClef, "confidence": d.Confidence, "outcome": d.Outcome}
	id := a.recordAsk(kind, "decided by the decision model", map[string]any{"decided_by": answerFromClef})
	a.recordAskAnswer(id, kind, answerFromClef, "", "", meta)
	a.addSystem(d.Line())
}
