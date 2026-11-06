package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/vulnetix/belai/internal/checkpoint"
	"github.com/vulnetix/belai/internal/sanitize"
	"github.com/vulnetix/belai/internal/tui/components"
)

// rewindModes lists the choices of the second step.
var rewindModes = []struct {
	mode rewindMode
	name string
	help string
}{
	{rewindCode, "code", "put the files back; the conversation stays"},
	{rewindConversation, "conversation", "return to just before that prompt; the files stay"},
	{rewindBoth, "both", "put the files back and return to just before that prompt"},
}

func (a *App) rewindView() string {
	w := a.contentWidth()
	var b strings.Builder
	title := "Rewind"
	if a.rewind.undo {
		title = "Undo"
	}
	b.WriteString(components.SectionHeader(title, "esc close", w))
	b.WriteString("\n")
	switch a.rewind.stage {
	case rewindPick:
		b.WriteString(a.rewindPickBody(w))
	case rewindChoose:
		b.WriteString(a.rewindChooseBody())
	default:
		b.WriteString(a.rewindConfirmBody(w))
	}
	if a.rewind.err != "" {
		b.WriteString("\n" + components.DangerStyle.Render("✗ "+oneLine(a.rewind.err, w)) + "\n")
	}
	b.WriteString("\n" + a.rewindHelp() + "\n")
	return lipgloss.NewStyle().Padding(1).Render(b.String())
}

func (a *App) rewindHelp() string {
	switch a.rewind.stage {
	case rewindPick:
		return components.HelpBar("↑↓", "row", "enter", "choose", "esc", "close")
	case rewindChoose:
		return components.HelpBar("↑↓", "row", "enter", "continue", "esc", "back")
	}
	return components.HelpBar("y", "restore", "n", "cancel", "esc", "back")
}

func (a *App) rewindPickBody(w int) string {
	var b strings.Builder
	b.WriteString(components.MutedStyle.Render("a checkpoint is made when a prompt first changes a file; choosing one returns to just before that prompt") + "\n\n")
	rows := a.rewind.infos
	nrows := 12
	if a.height > 0 {
		nrows = max(3, a.height-10)
	}
	a.rewind.scroll = windowStart(a.rewind.scroll, a.rewind.cursor, len(rows), nrows)
	end := min(a.rewind.scroll+nrows, len(rows))
	for i := a.rewind.scroll; i < end; i++ {
		in := rows[i]
		label := in.Label
		if in.Kind == checkpoint.KindRestore {
			label = "(safety copy) " + in.Label
		}
		if label == "" {
			label = "(no prompt text)"
		}
		counts := fmt.Sprintf("%d files", in.Files)
		if n := in.Files - in.Restorable; n > 0 {
			counts += fmt.Sprintf(", %d not restorable", n)
		}
		if in.Gaps > 0 {
			counts += ", incomplete"
		}
		if in.Undone {
			counts += ", undone"
		}
		line := components.Cursor(i == a.rewind.cursor) + in.At.Format("15:04") + "  " + sanitize.Line(label, max(20, w-45)) + "  " + components.MutedStyle.Render(counts)
		b.WriteString(line + "\n")
	}
	return b.String()
}

func (a *App) rewindChooseBody() string {
	var b strings.Builder
	b.WriteString(components.MutedStyle.Render("restore to before: "+sanitize.Line(a.rewind.target.Label, 80)) + "\n\n")
	_, convOK := a.rewindEntry(a.rewind.target)
	for i, m := range rewindModes {
		help := m.help
		if m.mode != rewindCode && !convOK {
			help = "not available: this point has no earlier message to return to"
		}
		line := components.Cursor(i == a.rewind.cursor) + m.name + "  " + components.MutedStyle.Render(help)
		b.WriteString(line + "\n")
	}
	return b.String()
}

func (a *App) rewindConfirmBody(w int) string {
	var b strings.Builder
	st := a.rewind
	b.WriteString(components.EmphStyle.Render("Restore "+st.mode.word()+" to before: ") + sanitize.Line(st.target.Label, 80) + "\n")
	if st.mode != rewindConversation {
		act, skip := st.plan.Count()
		b.WriteString(fmt.Sprintf("%d files will be written or removed, %d left as they are\n", act, skip))
		b.WriteString(components.MutedStyle.Render("changes you made yourself since then are overwritten; they are kept as a safety checkpoint you can restore") + "\n\n")
		rows := st.plan.Changes
		nrows := 12
		if a.height > 0 {
			nrows = max(3, a.height-14)
		}
		for i, c := range rows {
			if i == nrows {
				b.WriteString(components.MutedStyle.Render(fmt.Sprintf("… and %d more", len(rows)-nrows)) + "\n")
				break
			}
			tail := ""
			if c.Why != "" {
				tail = "  " + components.MutedStyle.Render(sanitize.Line(c.Why, 60))
			}
			b.WriteString(fmt.Sprintf("  %-8s %s%s\n", c.Action, a.rewindRel(c.Path), tail))
		}
		if st.plan.Gaps > 0 {
			b.WriteString("\n" + components.WarnStyle.Render(fmt.Sprintf("%d effects of shell commands could not be seen, so some changes may not come back", st.plan.Gaps)) + "\n")
		}
	}
	if st.mode != rewindCode {
		b.WriteString("\nthe conversation returns to just before that prompt, and the prompt goes back into the editor\n")
	}
	return b.String()
}

func (a *App) handleRewindKey(m tea.KeyMsg) (tea.Model, tea.Cmd) {
	st := &a.rewind
	switch m.String() {
	case "esc", "q":
		if st.stage == rewindPick || st.undo {
			a.pop()
			return a, nil
		}
		st.err = ""
		st.cursor = 0
		if st.stage == rewindConfirm {
			st.stage = rewindChoose
		} else {
			st.stage = rewindPick
			st.cursor = 0
		}
		return a, nil
	case "up", "k":
		if st.cursor > 0 {
			st.cursor--
		}
	case "down", "j":
		limit := len(st.infos)
		if st.stage == rewindChoose {
			limit = len(rewindModes)
		}
		if st.cursor < limit-1 {
			st.cursor++
		}
	case "n":
		if st.stage == rewindConfirm {
			a.pop()
		}
	case "y":
		if st.stage == rewindConfirm {
			return a, a.rewindApply()
		}
	case "enter":
		return a, a.rewindEnter()
	}
	return a, nil
}

// rewindEnter advances the view one step.
func (a *App) rewindEnter() tea.Cmd {
	st := &a.rewind
	st.err = ""
	switch st.stage {
	case rewindPick:
		if st.cursor >= len(st.infos) {
			return nil
		}
		st.target = st.infos[st.cursor]
		st.stage, st.cursor = rewindChoose, 0
	case rewindChoose:
		st.mode = rewindModes[st.cursor].mode
		if st.mode != rewindCode {
			if _, ok := a.rewindEntry(st.target); !ok {
				st.err = "this point has no earlier message to return to"
				return nil
			}
		}
		if st.mode != rewindConversation {
			t := a.checkpointTracker()
			if t == nil {
				st.err = "file checkpoints are not available"
				return nil
			}
			plan, err := t.Plan(st.target.ID)
			if err != nil {
				st.err = sanitize.Line(err.Error(), 160)
				return nil
			}
			st.plan = plan
			if len(plan.Changes) == 0 && st.mode == rewindCode {
				st.err = "the files already match what they held before that prompt"
				return nil
			}
		}
		st.stage = rewindConfirm
	}
	return nil
}
