package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/belai/internal/agent"
	"github.com/vulnetix/belai/internal/clefmcp"
	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/sanitize"
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

// askDeciderFactory builds the engine for the plan review; a test replaces it.
var askDeciderFactory = askDeciderFor

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

// planDecidedMsg carries a plan-review choice the decision model was confident
// about, for the review that was open when it was asked.
type planDecidedMsg struct {
	path, askID string
	choice      string
	confidence  float64
}

// The plan review's options as the decision model sees them, and the choice each
// stands for. "Refine" is not one of them: it needs notes only a person can write.
var (
	planDecideOptions = []string{
		"Approve the plan and execute it in this session",
		"Approve the plan and execute it in a new session",
		"Do not approve it yet and keep planning",
	}
	planDecideChoices = []string{planChoiceApproveHere, planChoiceApproveNew, planChoiceStay}
)

// decidePlanReviewCmd puts an open plan review to the decision model, off the UI
// goroutine. It sees the user's request, the plan's title and its step count,
// never the plan's text. A confident answer arrives as a planDecidedMsg; anything
// else leaves the review for the user.
func (a *App) decidePlanReviewCmd() tea.Cmd {
	if !a.viewOpen(viewPlanReview) || a.planReview.askID == "" || a.planReview.noteMode {
		return nil
	}
	d := askDeciderFactory(a.settings, credentialSourceOf(a.resolver))
	if d == nil {
		return nil
	}
	th, _ := d.AskPolicy()
	path, askID := a.planReview.path, a.planReview.askID
	var b strings.Builder
	if a.lastPrompt != nil {
		b.WriteString("The user's request: " + sanitize.Line(a.lastPrompt.input, 1500) + "\n")
	}
	if t := a.planReview.doc.Title; t != "" {
		b.WriteString("The plan: " + sanitize.Line(t, 200) + "\n")
	}
	fmt.Fprintf(&b, "The plan has %d steps.\n", len(a.planReview.doc.Steps))
	ctxText := b.String()
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		w, err := d.Choose(ctx, "The plan is written. What should happen to it?", ctxText, planDecideOptions, nil)
		if err != nil || len(w) != len(planDecideChoices) {
			return nil
		}
		best := 0
		for i := range w {
			if w[i] > w[best] {
				best = i
			}
		}
		if w[best] < th {
			return nil
		}
		return planDecidedMsg{path: path, askID: askID, choice: planDecideChoices[best], confidence: w[best]}
	}
}

// handlePlanDecided applies a decided plan-review choice through the arms the
// host's keys use, only while the review it was asked about is still open and
// unanswered and no turn is running.
func (a *App) handlePlanDecided(m planDecidedMsg) tea.Cmd {
	if !a.viewOpen(viewPlanReview) || a.planReview.askID != m.askID || a.planReview.path != m.path ||
		a.working() || a.preSend || a.planReview.noteMode {
		return nil
	}
	a.recordPlanChoice(m.choice, answerFromClef, "", "")
	d := &agent.AskDecided{Kind: agent.AskKindPlanReview, Outcome: strings.ReplaceAll(m.choice, "_", " "), Confidence: m.confidence}
	a.addSystem(d.Line())
	// The arms pop the review themselves; put it on top first so they pop exactly it.
	a.dropView(viewPlanReview)
	a.viewStack = append(a.viewStack, viewPlanReview)
	a.view = viewPlanReview
	switch m.choice {
	case planChoiceApproveHere:
		return a.submitPlanApprove()
	case planChoiceApproveNew:
		return a.submitPlanApproveNew()
	}
	return a.submitPlanStay()
}
