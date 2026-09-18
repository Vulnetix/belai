package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/charmbracelet/bubbles/viewport"
	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/modes"
	"github.com/vulnetix/signet/internal/plans"
	"github.com/vulnetix/signet/internal/tui/components"
)

// planReviewApprove/refine/cancel are the three actions in the review pane.
const (
	planReviewApprove = iota
	planReviewRefine
	planReviewCancel
)

// planReviewState tracks the interactive plan approval UI.
type planReviewState struct {
	name     string // plan slug, for State.ActivePlan
	path     string // absolute filesystem path displayed verbatim
	selected int
	noteMode bool
	vp       viewport.Model
}

// activePlanReviewDirective is sent back to the planner on refine so the next
// plan-mode turn revises the current file rather than restarting from scratch.
const activePlanReviewDirective = "Revise the approved plan above, incorporating the user's notes."

func newPlanReviewState(name, path string) planReviewState {
	return planReviewState{name: name, path: path, selected: planReviewApprove}
}

func (a *App) enterPlanReview() tea.Cmd {
	abs, err := filepath.Abs(a.planReview.path)
	if err != nil {
		abs = a.planReview.path
	}
	a.planReview.path = abs

	body, err := os.ReadFile(abs)
	if err != nil {
		body = []byte(fmt.Sprintf("(could not read plan file: %v)", err))
	}

	w := a.planReviewWidth()
	h := a.planReviewHeight()
	a.planReview.vp = viewport.New(w, h)
	a.planReview.vp.SetContent(string(body))
	a.planReview.vp.GotoTop()
	return nil
}

// planReviewWidth is the usable interior width of the pane.
func (a *App) planReviewWidth() int {
	w := a.contentWidth() - 2
	if w < 1 {
		w = 1
	}
	return w
}

// planReviewHeight is the number of rows available for the plan body.
// planReviewChrome is every non-body row: two padding rows, header, blank,
// filename line, blank, three options, blank, help bar.
const planReviewChrome = 11

func (a *App) planReviewHeight() int {
	h := a.height - planReviewChrome
	if a.planReview.noteMode {
		h -= a.editor.Height() + 3
	}
	if h < 5 {
		h = 5
	}
	return h
}

func (a *App) planReviewView() string {
	w := a.planReviewWidth()
	var b strings.Builder
	b.WriteString(components.SectionHeader("Review plan", "esc cancel", w))
	b.WriteString("\n")
	b.WriteString(components.MutedStyle.Render("file: " + filepath.Clean(a.planReview.path)))
	b.WriteString("\n")

	// Resize the viewport on every render so terminal changes are reflected.
	a.planReview.vp.Width = w
	a.planReview.vp.Height = a.planReviewHeight()
	b.WriteString(a.planReview.vp.View())
	b.WriteString("\n")

	labels := []string{"approve — execute this plan now", "refine  — send notes back to the planner", "cancel  — keep the file and stay in plan mode"}
	for i, label := range labels {
		selected := i == a.planReview.selected
		box := "○"
		if selected {
			box = "●"
		}
		line := box + " " + label
		if selected {
			line = components.EmphStyle.Render(line)
		}
		b.WriteString(line + "\n")
	}

	if a.planReview.noteMode {
		b.WriteString("\n" + a.renderFieldEditor("notes", w) + "\n")
		b.WriteString(components.HelpBar("enter", "save", "esc", "cancel") + "\n")
	} else {
		b.WriteString("\n" + components.HelpBar(
			"↑↓", "move", "enter", "confirm", "pgup/pgdn", "page",
			"shift+↑↓", "line", "ctrl+home/end", "top/bottom",
			"wheel", "scroll", "esc", "cancel") + "\n")
	}

	return lipgloss.NewStyle().Padding(1).Render(b.String())
}

func (a *App) handlePlanReviewKey(m tea.KeyMsg) (tea.Model, tea.Cmd) {
	if a.planReview.noteMode {
		switch m.String() {
		case "esc":
			a.editor.Reset()
			a.planReview.noteMode = false
			return a, nil
		case "enter":
			notes := a.editor.Value()
			a.editor.Reset()
			a.planReview.noteMode = false
			if notes == "" {
				return a, nil
			}
			return a, a.submitPlanRefine(notes)
		default:
			cmd := a.editor.Update(m)
			return a, cmd
		}
	}

	switch m.String() {
	case "esc":
		return a, a.submitPlanStay()
	case "up", "k":
		if a.planReview.selected > planReviewApprove {
			a.planReview.selected--
		}
		return a, nil
	case "down", "j":
		if a.planReview.selected < planReviewCancel {
			a.planReview.selected++
		}
		return a, nil
	case "pgup":
		a.planReview.vp.PageUp()
		return a, nil
	case "pgdown":
		a.planReview.vp.PageDown()
		return a, nil
	case "shift+up":
		a.planReview.vp.ScrollUp(1)
		return a, nil
	case "shift+down":
		a.planReview.vp.ScrollDown(1)
		return a, nil
	case "ctrl+home":
		a.planReview.vp.GotoTop()
		return a, nil
	case "ctrl+end":
		a.planReview.vp.GotoBottom()
		return a, nil
	case "enter":
		switch a.planReview.selected {
		case planReviewApprove:
			return a, a.submitPlanApprove()
		case planReviewRefine:
			a.planReview.noteMode = true
			a.editor.Reset()
			_ = a.editor.Focus()
			return a, nil
		case planReviewCancel:
			return a, a.submitPlanStay()
		}
	}
	return a, nil
}

// submitPlanApprove persists State.ActivePlan, switches to agent mode, and
// immediately starts executing the plan.
func (a *App) submitPlanApprove() tea.Cmd {
	a.state.ActivePlan = a.planReview.name
	_ = config.SaveState(a.state)

	a.mode = string(modes.ModeAgent)
	a.modeExplicit = true
	a.modeSticky = true
	a.pendingPlanExecute = true
	a.planExecuteName = a.planReview.name
	a.syncPlanMode()
	a.saveMode()
	a.persistCarrierMeta()

	a.pop()
	a.addSystem("plan approved: " + a.planReview.path + " — executing")
	return a.submitInput("Execute the approved plan.")
}

// submitPlanRefine stays in plan mode and sends the user's notes back to the
// planner. The next plan-mode turn is recorded as a new revision of the same
// plan.
func (a *App) submitPlanRefine(notes string) tea.Cmd {
	a.pop()
	a.pendingDirective = activePlanReviewDirective

	base := planRevisionBase(a.planReview.name)
	a.pendingPlanRevision = plans.NextRevision(a.workdir, base)

	return a.submitInput(notes)
}

// planRevisionBase strips a trailing -rN revision suffix from a recorded plan
// name so refinements continue the same sequence.
var planRevisionSuffix = regexp.MustCompile(`-r\d+$`)

func planRevisionBase(name string) string {
	return planRevisionSuffix.ReplaceAllString(name, "")
}

// submitPlanStay leaves the pane without changing mode or ActivePlan.
func (a *App) submitPlanStay() tea.Cmd {
	a.pop()
	a.addSystem("plan not approved; file kept at " + a.planReview.path)
	return nil
}
