package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/vulnetix/belai/internal/attention"
	"github.com/vulnetix/belai/internal/kanban"
	"github.com/vulnetix/belai/internal/tui/components"
)

// The inbox (/inbox) is the queue of cards waiting on a person: a flow stage
// holding its card at a checkpoint, a card assigned to a person, and a card a
// worker blocked (internal/attention orders them, the longest wait first within
// each kind). It reads the board and nothing else, so it works the same with no
// session, no rc and no network. A checkpoint is decided here: a approves the
// card, x sends it back to the backlog with the reason typed in the composer.

type inboxViewState struct {
	items            []attention.Item
	selected, scroll int
	// mode is "" when browsing, or "reject" while the editor holds the reason.
	mode    string
	note    string
	noteErr bool
	off     bool // the kanban board is switched off
}

func (a *App) openInbox() tea.Cmd { return a.push(viewInbox) }

func (a *App) enterInbox() tea.Cmd {
	a.inboxState = inboxViewState{}
	a.loadInbox()
	return nil
}

// loadInbox reads the board, which is a local file like the other settings
// screens' sources, so it runs on the UI goroutine.
func (a *App) loadInbox() {
	st := &a.inboxState
	store, _ := a.kanbanStore()
	if store == nil {
		st.items, st.off = nil, true
		return
	}
	st.off = false
	cards, err := store.Search(kanban.Query{Lists: attention.Lists, Limit: kanban.MaxItems})
	if err != nil {
		st.note, st.noteErr = err.Error(), true
		return
	}
	st.items = attention.FromCards(cards)
	a.clampInbox()
}

func (a *App) clampInbox() {
	st := &a.inboxState
	st.selected = max(0, min(st.selected, len(st.items)-1))
}

// inboxCount is how many cards wait on a person, for the footer and the
// switcher row. It reads nothing when the board is off.
func (a *App) inboxCount() int {
	store, _ := a.kanbanStore()
	if store == nil {
		return 0
	}
	cards, err := store.Search(kanban.Query{Lists: attention.Lists, Limit: kanban.MaxItems})
	if err != nil {
		return 0
	}
	return attention.Count(cards)
}

// pollInbox refreshes the count the footer shows, at most every few seconds
// unless forced.
func (a *App) pollInbox(force bool) {
	if !force && time.Since(a.inboxPolled) < 5*time.Second {
		return
	}
	a.inboxPolled = time.Now()
	a.inboxWaiting = a.inboxCount()
}

// inboxFooterLabel is the footer's inbox segment: empty while nothing waits.
func (a *App) inboxFooterLabel() string {
	if a.inboxWaiting == 0 {
		return ""
	}
	return fmt.Sprintf("inbox %d", a.inboxWaiting)
}

func (a *App) inboxStatus() string {
	if n := a.inboxCount(); n > 0 {
		return fmt.Sprintf("%d waiting on you", n)
	}
	return ""
}

func (a *App) decideInbox(approve bool, reason string) {
	st := &a.inboxState
	if st.selected >= len(st.items) {
		return
	}
	it := st.items[st.selected]
	store, _ := a.kanbanStore()
	if store == nil {
		return
	}
	d := kanban.Decision{Approve: approve, Back: kanban.Backlog, Note: reason}
	got, err := store.Decide(it.ID, d, a.sessionID)
	if err != nil {
		st.note, st.noteErr = err.Error(), true
		return
	}
	verb := "Approved"
	if !approve {
		verb = "Sent back to the backlog:"
	}
	st.note, st.noteErr = fmt.Sprintf("%s %s.", verb, got.Short()), false
	a.loadInbox()
}

func (a *App) handleInboxKey(m tea.KeyMsg) (tea.Model, tea.Cmd) {
	st := &a.inboxState
	key := m.String()
	if st.mode == "reject" {
		switch key {
		case "esc":
			st.mode = ""
			a.editor.Reset()
			return a, nil
		case "enter":
			reason := strings.TrimSpace(a.editor.Value())
			if reason == "" {
				st.note, st.noteErr = "Say what is wrong, so the earlier stage can fix it.", true
				return a, nil
			}
			st.mode = ""
			a.editor.Reset()
			a.decideInbox(false, reason)
			return a, nil
		}
		return a, a.editor.Update(m)
	}

	switch key {
	case "esc", "q":
		a.pop()
	case "up", "k":
		st.selected--
	case "down", "j":
		st.selected++
	case "home", "g":
		st.selected = 0
	case "end", "G":
		st.selected = len(st.items) - 1
	case "r":
		st.note = ""
		a.loadInbox()
	case "a":
		if it, ok := a.inboxSelected(); ok && it.Decidable() {
			a.decideInbox(true, "")
		} else if ok {
			st.note, st.noteErr = "Only a checkpoint is approved here. Move or release this card from /kanban.", false
		}
	case "x":
		if it, ok := a.inboxSelected(); ok && it.Decidable() {
			st.mode, st.note = "reject", ""
			a.editor.Masked = false
			a.editor.Reset()
			_ = a.editor.Focus()
		} else if ok {
			st.note, st.noteErr = "Only a checkpoint is rejected here. Move or release this card from /kanban.", false
		}
	case "t":
		a.pop()
		return a, a.handleCommand("/kanban")
	}
	a.clampInbox()
	return a, nil
}

func (a *App) inboxSelected() (attention.Item, bool) {
	st := &a.inboxState
	if st.selected < 0 || st.selected >= len(st.items) {
		return attention.Item{}, false
	}
	return st.items[st.selected], true
}

// inboxKindColor is amber for what holds a flow up and danger for a failure, as
// the design system has it: amber means the harness is waiting for you.
func inboxKindColor(k attention.Kind) lipgloss.TerminalColor {
	if k == attention.KindBlocked {
		return components.ColorDanger
	}
	return components.ColorAmber
}

func inboxLine(it attention.Item, selected bool, w int) string {
	prefix := components.Cursor(selected) + components.Chip(it.Label(), inboxKindColor(it.Kind)) + " " +
		components.MutedStyle.Render(it.Short) + " "
	var suffix string
	if it.Project != "" {
		suffix += "  " + components.MutedStyle.Render(mcpClean(it.Project, 40))
	}
	if age := ageLabel(it.Since); age != "" {
		suffix += "  " + components.MutedStyle.Render(age)
	}
	// The title gives way, so the reason and the age stay readable.
	title := ansi.Truncate(mcpClean(it.Title, 200), max(10, w-lipgloss.Width(prefix)-lipgloss.Width(suffix)), "…")
	if selected {
		title = components.AccentStyle.Bold(true).Render(title)
	}
	return ansi.Truncate(prefix+title+suffix, w, "…")
}

func (a *App) inboxHead(w int) string {
	st := &a.inboxState
	var b strings.Builder
	b.WriteString(components.SectionHeader("Waiting on you", fmt.Sprintf("%d", len(st.items)), w))
	b.WriteString(ansi.Truncate(components.MutedStyle.Render("Checkpoints a flow holds for a decision, cards assigned to a person, and cards a worker blocked."), w, "…") + "\n")
	return b.String()
}

// inboxDetail shows the selected card's reason and latest note, so a decision
// is made on what the stage left, not on the title alone.
func (a *App) inboxDetail(w int) string {
	it, ok := a.inboxSelected()
	if !ok {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n" + components.EmphStyle.Render(mcpClean(it.Title, 200)) + "\n")
	facts := []string{string(it.List)}
	if it.Reason != "" {
		facts = append(facts, it.Reason)
	}
	b.WriteString(components.MutedStyle.Render(strings.Join(facts, " · ")) + "\n")
	if it.Note != "" {
		b.WriteString(lipgloss.NewStyle().Width(w).Render(components.MutedStyle.Render(mcpClean(it.Note, 600))) + "\n")
	}
	return b.String()
}

func (a *App) inboxTail(w int) string {
	st := &a.inboxState
	var b strings.Builder
	if st.note != "" {
		style := components.MutedStyle
		if st.noteErr {
			style = components.DangerStyle
		}
		b.WriteString("\n" + lipgloss.NewStyle().Width(w).Render(style.Render(mcpClean(st.note, 400))) + "\n")
	}
	b.WriteString("\n")
	if st.mode == "reject" {
		b.WriteString(a.renderFieldEditor("why is it sent back? (the earlier stage reads this)", w) + "\n")
		return b.String()
	}
	b.WriteString(lipgloss.NewStyle().Width(w).Render(components.HelpBar("↑↓", "move", "a", "approve", "x", "send back", "t", "kanban", "r", "refresh", "esc", "back")) + "\n")
	return b.String()
}

func (a *App) inboxView() string {
	w := a.contentWidth()
	st := &a.inboxState
	var body strings.Builder
	switch {
	case st.off:
		body.WriteString(components.MutedStyle.Render("  The kanban board is off. Turn it on in /settings.") + "\n")
	case len(st.items) == 0:
		body.WriteString(components.MutedStyle.Render("  Nothing is waiting on you.") + "\n")
	default:
		rows := max(3, a.height-lipgloss.Height(a.inboxHead(w))-lipgloss.Height(a.inboxTail(w))-10)
		if a.height <= 0 {
			rows = 12
		}
		st.scroll = windowStart(st.scroll, st.selected, len(st.items), rows)
		end := min(st.scroll+rows, len(st.items))
		for i := st.scroll; i < end; i++ {
			body.WriteString(inboxLine(st.items[i], i == st.selected, w) + "\n")
		}
		if n := len(st.items) - end; n > 0 {
			body.WriteString(components.MutedStyle.Render(fmt.Sprintf("  ↓ %d more", n)) + "\n")
		}
		body.WriteString(a.inboxDetail(w))
	}
	return lipgloss.NewStyle().Padding(1).Render(a.inboxHead(w) + "\n" + body.String() + a.inboxTail(w))
}
