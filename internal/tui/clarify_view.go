package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/vulnetix/signet/internal/clarify"
	"github.com/vulnetix/signet/internal/sanitize"
	"github.com/vulnetix/signet/internal/tui/components"
)

// clarifyViewState tracks the interactive questionnaire UI.
type clarifyViewState struct {
	q        clarify.Questionnaire
	reply    chan clarify.Answers
	rows     []clarifyRow
	selected int            // index into rows; selects only option rows
	chosen   []map[int]bool // group index -> chosen option indices
	notes    map[[2]int]string
	skipped  []bool
	noteMode bool
}

type clarifyRowKind int

const (
	clarifyRowHeader clarifyRowKind = iota
	clarifyRowOption
)

type clarifyRow struct {
	kind      clarifyRowKind
	groupIdx  int
	optionIdx int
}

func newClarifyState(q clarify.Questionnaire, reply chan clarify.Answers) clarifyViewState {
	state := clarifyViewState{
		q:       q,
		reply:   reply,
		chosen:  make([]map[int]bool, len(q.Groups)),
		notes:   map[[2]int]string{},
		skipped: make([]bool, len(q.Groups)),
	}
	for gi := range q.Groups {
		state.rows = append(state.rows, clarifyRow{kind: clarifyRowHeader, groupIdx: gi})
		for oi := range q.Groups[gi].Options {
			state.rows = append(state.rows, clarifyRow{kind: clarifyRowOption, groupIdx: gi, optionIdx: oi})
		}
	}
	// Start on the first option row.
	state.selected = state.nextSelectable(0)
	if state.selected < 0 {
		state.selected = 0
	}
	return state
}

func (s *clarifyViewState) currentRow() *clarifyRow {
	if s.selected < 0 || s.selected >= len(s.rows) {
		return nil
	}
	return &s.rows[s.selected]
}

func (s *clarifyViewState) nextSelectable(start int) int {
	for i := start; i < len(s.rows); i++ {
		if s.rows[i].kind == clarifyRowOption {
			return i
		}
	}
	return -1
}

func (s *clarifyViewState) prevSelectable(start int) int {
	for i := start; i >= 0; i-- {
		if s.rows[i].kind == clarifyRowOption {
			return i
		}
	}
	return -1
}

func (s *clarifyViewState) selectedGroup() int {
	if r := s.currentRow(); r != nil {
		return r.groupIdx
	}
	return -1
}

func (s *clarifyViewState) isSelected(rowIdx int) bool {
	return rowIdx == s.selected
}

func (s *clarifyViewState) toggleChoice(rowIdx int) {
	r := &s.rows[rowIdx]
	if r.kind != clarifyRowOption {
		return
	}
	g := r.groupIdx
	o := r.optionIdx
	if s.skipped[g] {
		s.skipped[g] = false
	}
	if s.q.Groups[g].Multi {
		if s.chosen[g] == nil {
			s.chosen[g] = map[int]bool{}
		}
		s.chosen[g][o] = !s.chosen[g][o]
		if !s.chosen[g][o] {
			delete(s.chosen[g], o)
		}
	} else {
		s.chosen[g] = map[int]bool{o: true}
	}
}

// chooseHighlighted selects the highlighted option when its group has no
// answer yet. Enter on an option is the natural way to pick it; enter used to
// submit without selecting, so a user who moved to a file and pressed enter
// sent "chose: (none)" and the clarifier asked the same question again.
func (s *clarifyViewState) chooseHighlighted() {
	r := s.currentRow()
	if r == nil || r.kind != clarifyRowOption {
		return
	}
	g := r.groupIdx
	if s.skipped[g] || len(s.chosen[g]) > 0 {
		return
	}
	s.toggleChoice(s.selected)
}

func (s *clarifyViewState) skipGroup(gi int) {
	s.skipped[gi] = true
	s.chosen[gi] = map[int]bool{}
}

func (s *clarifyViewState) note(rowIdx int) string {
	r := s.rows[rowIdx]
	if r.kind != clarifyRowOption {
		return ""
	}
	return s.notes[[2]int{r.groupIdx, r.optionIdx}]
}

func (s *clarifyViewState) setNote(rowIdx int, text string) {
	r := s.rows[rowIdx]
	if r.kind != clarifyRowOption {
		return
	}
	text = strings.TrimSpace(sanitize.Sanitize(text))
	if text == "" {
		delete(s.notes, [2]int{r.groupIdx, r.optionIdx})
		return
	}
	s.notes[[2]int{r.groupIdx, r.optionIdx}] = text
}

func (s *clarifyViewState) buildAnswers() clarify.Answers {
	var items []clarify.Answer
	for gi := range s.q.Groups {
		ans := clarify.Answer{GroupIndex: gi, Skipped: s.skipped[gi]}
		if !s.skipped[gi] {
			for oi := range s.q.Groups[gi].Options {
				if s.chosen[gi][oi] {
					ans.Chosen = append(ans.Chosen, oi)
				}
			}
		}
		// Attach any note stored for this group (use the first option that
		// carries one, consistent with the note being per-group in the UI).
		for oi := range s.q.Groups[gi].Options {
			if note := s.notes[[2]int{gi, oi}]; note != "" {
				ans.Note = note
				break
			}
		}
		items = append(items, ans)
	}
	return clarify.Answers{Items: items}
}

// clarifyPanel renders the interactive questionnaire as a bottom-sheet that
// replaces the composer and footer in the chat view. It is the inner content
// without the full-screen padding so it nests cleanly under the transcript.
func (a *App) clarifyPanel() string {
	w := a.contentWidth()
	var b strings.Builder
	b.WriteString(components.SectionHeader("Clarify", "esc cancel", w))

	for i, row := range a.clarifyState.rows {
		switch row.kind {
		case clarifyRowHeader:
			b.WriteString("\n" + components.EmphStyle.Render(a.clarifyState.q.Groups[row.groupIdx].Context) + "\n")
		case clarifyRowOption:
			opt := a.clarifyState.q.Groups[row.groupIdx].Options[row.optionIdx]
			selected := a.clarifyState.isSelected(i)
			checked := a.clarifyState.chosen[row.groupIdx][row.optionIdx]
			multi := a.clarifyState.q.Groups[row.groupIdx].Multi

			box := "○"
			if checked {
				box = "●"
			}
			if multi {
				box = "☐"
				if checked {
					box = "☑"
				}
			}
			if a.clarifyState.skipped[row.groupIdx] {
				box = "○"
			}

			marker := components.Cursor(selected) + box + " "
			line := opt.Label
			if opt.Description != "" {
				line += "  " + components.MutedStyle.Render(opt.Description)
			}
			if selected {
				line = components.EmphStyle.Render(line)
			} else if a.clarifyState.skipped[row.groupIdx] {
				line = components.MutedStyle.Render(line)
			}
			b.WriteString(marker + line + "\n")

			if note := a.clarifyState.notes[[2]int{row.groupIdx, row.optionIdx}]; note != "" {
				b.WriteString(components.MutedStyle.Render("        · note: "+note) + "\n")
			}
		}
	}

	if a.clarifyState.noteMode {
		b.WriteString("\n" + a.renderFieldEditor("note", w) + "\n")
		b.WriteString("\n" + components.HelpBar("enter", "save", "esc", "cancel") + "\n")
	} else {
		b.WriteString("\n" + components.HelpBar(
			"↑↓", "move", "space", "select", "n", "note", "s", "skip",
			"enter", "submit", "esc", "cancel") + "\n")
	}

	return b.String()
}

func (a *App) clarifyView() string {
	return lipgloss.NewStyle().Padding(1).Render(a.clarifyPanel())
}

func (a *App) handleClarifyKey(m tea.KeyMsg) (tea.Model, tea.Cmd) {
	if a.clarifyState.noteMode {
		switch m.String() {
		case "esc":
			a.editor.Reset()
			a.clarifyState.noteMode = false
			return a, nil
		case "enter":
			a.clarifyState.setNote(a.clarifyState.selected, a.editor.Value())
			a.editor.Reset()
			a.clarifyState.noteMode = false
			return a, nil
		default:
			cmd := a.editor.Update(m)
			return a, cmd
		}
	}

	switch m.String() {
	case "esc":
		a.pop()
		a.addSystem("clarification cancelled")
		if a.cancel != nil {
			a.cancel()
			a.cancel = nil
			a.endPhase()
			a.events = nil
		} else if a.clarifyState.reply != nil {
			// Unconditional drop: with no turn to cancel, answer the (buffered)
			// channel so the agent's askUser unblocks instead of parking forever.
			select {
			case a.clarifyState.reply <- clarify.Answers{}:
			default:
			}
		}
		return a, nil
	case "up", "k":
		if prev := a.clarifyState.prevSelectable(a.clarifyState.selected - 1); prev >= 0 {
			a.clarifyState.selected = prev
		}
		return a, nil
	case "down", "j":
		if next := a.clarifyState.nextSelectable(a.clarifyState.selected + 1); next >= 0 {
			a.clarifyState.selected = next
		}
		return a, nil
	case " ":
		a.clarifyState.toggleChoice(a.clarifyState.selected)
		return a, nil
	case "n":
		a.clarifyState.noteMode = true
		a.editor.Reset()
		if note := a.clarifyState.note(a.clarifyState.selected); note != "" {
			a.editor.SetValue(note)
		}
		_ = a.editor.Focus()
		return a, nil
	case "s":
		if gi := a.clarifyState.selectedGroup(); gi >= 0 {
			a.clarifyState.skipGroup(gi)
		}
		return a, nil
	case "enter":
		a.clarifyState.chooseHighlighted()
		answers := a.clarifyState.buildAnswers()
		if a.clarifyState.reply != nil {
			go func() { a.clarifyState.reply <- answers }()
		}
		a.pop()
		// The questionnaire is already in the transcript; the answers
		// belong beside it so the session record shows what was chosen.
		a.addSystem(answers.Render(a.clarifyState.q))
		return a, a.nextAgent()
	}

	return a, nil
}

// formatQuestionnaire renders a questionnaire as a system notice for the
// transcript, so the exchange survives in the session record.
func formatQuestionnaire(q clarify.Questionnaire) string {
	var b strings.Builder
	b.WriteString("Clarification needed:\n")
	for i, g := range q.Groups {
		fmt.Fprintf(&b, "%d. %s\n", i+1, g.Context)
		for _, o := range g.Options {
			b.WriteString("   - " + o.Label)
			if o.Description != "" {
				b.WriteString(": " + o.Description)
			}
			b.WriteString("\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}
