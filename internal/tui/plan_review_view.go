package tui

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/charmbracelet/bubbles/viewport"
	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/filediff"
	"github.com/vulnetix/signet/internal/modes"
	"github.com/vulnetix/signet/internal/plans"
	"github.com/vulnetix/signet/internal/session"
	"github.com/vulnetix/signet/internal/todos"
	"github.com/vulnetix/signet/internal/tui/components"
)

// Plan-review actions, in render order.
const (
	planReviewApproveHere = iota
	planReviewApproveNew
	planReviewEdit
	planReviewRefine
	planReviewCancel
)

// planReviewState tracks the interactive plan approval UI.
type planReviewState struct {
	name     string // plan slug, for State.ActivePlan
	path     string // absolute filesystem path displayed verbatim
	doc      plans.Doc
	prev     *plans.Doc // previous revision, when one exists
	raw      string     // fallback body when the file does not parse as a Doc
	selected int
	noteMode bool
	showDiff bool
	editPath string // scratch path handed to $EDITOR
	edited   bool
	vp       viewport.Model
}

// planEditedMsg carries the result of an external $EDITOR round-trip.
type planEditedMsg struct {
	path string
	err  error
}

// activePlanReviewDirective is sent back to the planner on refine so the next
// plan-mode turn revises the current file rather than restarting from scratch.
const activePlanReviewDirective = "Revise the approved plan above, incorporating the user's notes."

func newPlanReviewState(name, path string) planReviewState {
	return planReviewState{name: name, path: path, selected: planReviewApproveHere}
}

func (a *App) enterPlanReview() tea.Cmd {
	abs, err := filepath.Abs(a.planReview.path)
	if err != nil {
		abs = a.planReview.path
	}
	a.planReview.path = abs

	body, err := os.ReadFile(abs)
	if err != nil {
		a.planReview.raw = fmt.Sprintf("(could not read plan file: %v)", err)
	} else {
		a.planReview.raw = string(body)
	}
	if doc, err := plans.ParseDoc(a.planReview.raw); err == nil {
		a.planReview.doc = doc
	}

	w := a.planReviewWidth()
	h := a.planReviewHeight()
	a.planReview.vp = viewport.New(w, h)
	a.planReview.setContent()
	return nil
}

// setContent renders the structured plan (or the raw fallback) into the
// review viewport and re-wraps long lines.
func (s *planReviewState) setContent() {
	var body string
	if s.showDiff && s.prev != nil {
		ch := filediff.Preview("plan", s.prev.Render(), s.doc.Render())
		body = components.DiffView(&ch, s.vp.Width)
	} else if len(s.doc.Steps) > 0 {
		body = s.doc.Render()
	} else {
		body = s.raw
	}
	s.vp.SetContent(body)
	s.vp.GotoTop()
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
// planReviewChrome is every non-body row: padding, header, filename, the five
// actions, and the help bar.
const planReviewChrome = 14

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
	if a.planReview.edited {
		b.WriteString(components.MutedStyle.Render("  ·  (edited by user)"))
	}
	b.WriteString("\n")

	a.planReview.vp.Width = w
	a.planReview.vp.Height = a.planReviewHeight()
	if a.planReview.showDiff && a.planReview.prev != nil {
		a.planReview.setContent()
	}
	b.WriteString(a.planReview.vp.View())
	b.WriteString("\n")

	labels := []string{
		"approve — execute here",
		"approve — execute in a new session",
		"edit — open in $EDITOR, then approve",
		"refine — send notes back to the planner",
		"cancel — keep the file and stay in plan mode",
	}
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
		pairs := []string{"↑↓", "move", "enter", "confirm", "pgup/pgdn", "page", "shift+↑↓", "line", "ctrl+home/end", "top/bottom", "wheel", "scroll"}
		if a.planReview.prev != nil {
			pairs = append(pairs, "d", "diff")
		}
		pairs = append(pairs, "esc", "cancel")
		b.WriteString("\n" + components.HelpBar(pairs...) + "\n")
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
		if a.planReview.selected > planReviewApproveHere {
			a.planReview.selected--
		}
		return a, nil
	case "down", "j":
		if a.planReview.selected < planReviewCancel {
			a.planReview.selected++
		}
		return a, nil
	case "d":
		if a.planReview.prev != nil {
			a.planReview.showDiff = !a.planReview.showDiff
			a.planReview.setContent()
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
		case planReviewApproveHere:
			return a, a.submitPlanApprove()
		case planReviewApproveNew:
			return a, a.submitPlanApproveNew()
		case planReviewEdit:
			return a, a.openPlanEditor()
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
// immediately starts executing the plan in the current session.
func (a *App) submitPlanApprove() tea.Cmd {
	return a.executeApprovedPlan(modes.PlanExecute)
}

// submitPlanApproveNew forks the current session into a child and executes the
// plan there, leaving the parent's planning passes untouched.
func (a *App) submitPlanApproveNew() tea.Cmd {
	newID := session.MustID()
	if err := a.store.Fork(a.workdir, a.sessionID, newID); err != nil {
		a.addSystem("plan fork failed: " + err.Error())
		return nil
	}
	a.sessionID = newID
	a.lastEntryID = ""
	a.persistedUpTo = 0
	// Planning passes stay in the parent: the child begins a fresh transcript.
	// The fork keeps the history on disk for provenance; the live transcript
	// starts at the execute turn.
	a.messages = nil
	a.todos = nil
	a.subagents = nil
	a.subagentIdx = map[string]int{}
	a.stripFocus = false
	a.stripSel = 0
	a.threadFilter = ""
	a.saveSession()
	return a.executeApprovedPlan(modes.PlanExecuteNew)
}

// executeApprovedPlan is the shared execute arm: persist ActivePlan, route the
// choice through modes.RoutePlanOption, and submit the execution prompt.
func (a *App) executeApprovedPlan(opt modes.PlanOption) tea.Cmd {
	route, err := modes.RoutePlanOption(opt)
	if err != nil {
		a.addSystem("plan route failed: " + err.Error())
		return nil
	}
	a.state.ActivePlan = a.planReview.name
	_ = config.SaveState(a.state)

	a.mode = string(route.Mode)
	a.modeExplicit = true
	a.modeSticky = true
	a.pendingPlanExecute = true
	a.planExecuteName = a.planReview.name
	a.syncPlanMode()
	a.saveMode()
	a.persistCarrierMeta()

	// Execution tracks the approved plan's steps, not the planning checklist:
	// the approved Doc.Steps seed a fresh todo list for the execute turn.
	if len(a.planReview.doc.Steps) > 0 {
		steps := make([]string, len(a.planReview.doc.Steps))
		for i, st := range a.planReview.doc.Steps {
			steps[i] = st.Text
		}
		l := todos.New(a.planReview.doc.Title, steps)
		a.setTodos(&l)
	}

	a.pop()
	a.addSystem("plan approved: " + a.planReview.path + " — executing")
	return a.submitInput("Execute the approved plan.")
}

// openPlanEditor writes the canonical plan to a scratch path, opens it in
// $VISUAL/$EDITOR, and re-parses on return. An invalid edit keeps the previous
// text (Pi's behaviour); a changed plan is tagged "(edited by user)".
func (a *App) openPlanEditor() tea.Cmd {
	dir := config.ProjectPlansDir(a.workdir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		a.addSystem("edit failed: " + err.Error())
		return nil
	}
	path := filepath.Join(dir, a.planReview.name+".edit.md")
	content := a.planReview.doc.Render()
	if len(a.planReview.doc.Steps) == 0 {
		content = a.planReview.raw
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		a.addSystem("edit failed: " + err.Error())
		return nil
	}
	a.planReview.editPath = path

	bin, args, ok := resolveEditorCommand()
	if !ok {
		a.addSystem("no $VISUAL or $EDITOR found; refine instead")
		return nil
	}
	cmd := exec.Command(bin, append(args, path)...)
	return a.execEditor(cmd, func(err error) tea.Msg {
		return planEditedMsg{path: path, err: err}
	})
}

// handlePlanEdited re-parses the edited plan and keeps the previous text on an
// invalid edit.
func (a *App) handlePlanEdited(m planEditedMsg) tea.Cmd {
	if m.err != nil {
		a.addSystem("edit failed: " + m.err.Error())
		return nil
	}
	data, err := os.ReadFile(m.path)
	if err != nil {
		a.addSystem("edit failed: " + err.Error())
		return nil
	}
	doc, err := plans.ParseDoc(string(data))
	if err != nil {
		a.addSystem("edited plan is invalid (" + err.Error() + "); keeping the previous plan")
		return nil
	}
	prev := a.planReview.doc
	a.planReview.prev = &prev
	a.planReview.doc = doc
	a.planReview.edited = true
	a.planReview.showDiff = false
	// Persist the edited plan back to the recorded file.
	if err := os.WriteFile(a.planReview.path, []byte(doc.Render()), 0o600); err != nil {
		a.addSystem("edit save failed: " + err.Error())
		return nil
	}
	a.planReview.setContent()
	return nil
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
