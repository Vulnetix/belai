package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/vulnetix/belai/internal/agent"
	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/tui/components"
)

// permissionAskViewState tracks the interactive mutating-tool approval UI.
type permissionAskViewState struct {
	ask      *agent.AskRequest
	reply    chan agent.PermissionAskReply
	selected int
}

// Answer choices, in render order.
const (
	permAskAllow = iota
	permAskAllowAlways
	permAskDeny
)

func newPermissionAskState(ask *agent.AskRequest, reply chan agent.PermissionAskReply) permissionAskViewState {
	return permissionAskViewState{ask: ask, reply: reply, selected: permAskAllow}
}

func (s permissionAskViewState) options() []string {
	return []string{"allow once", "allow always", "deny"}
}

// permissionAskView renders the tool name, normalised subject, and the diff
// the call would make, so the user approves a concrete change rather than a
// bare path.
func (a *App) permissionAskView() string {
	w := a.contentWidth()
	st := a.permAskState
	var b strings.Builder
	b.WriteString(components.SectionHeader("Approve change", "esc deny", w))

	if st.ask == nil {
		b.WriteString("\n(no pending request)\n")
		return lipgloss.NewStyle().Padding(1).Render(b.String())
	}

	b.WriteString("\n" + components.EmphStyle.Render("⌁ "+st.ask.Name))
	if st.ask.Subject != "" {
		b.WriteString("  " + components.MutedStyle.Render(st.ask.Subject))
	}
	b.WriteString("\n")

	if st.ask.Preview != nil {
		if diff := components.DiffView(st.ask.Preview, w); diff != "" {
			b.WriteString("\n" + diff)
		}
	}

	for i, opt := range st.options() {
		selected := i == st.selected
		box := "○"
		if selected {
			box = "●"
		}
		line := components.Cursor(selected) + box + " " + opt
		if selected {
			line = components.EmphStyle.Render(line)
		}
		b.WriteString("\n" + line)
	}

	b.WriteString("\n" + components.HelpBar("↑↓", "move", "enter", "confirm", "esc", "deny") + "\n")
	return lipgloss.NewStyle().Padding(1).Render(b.String())
}

func (a *App) handlePermissionAskKey(m tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.String() {
	case "esc":
		a.answerPermissionAsk(false)
		a.pop()
		return a, nil
	case "up", "k":
		if a.permAskState.selected > permAskAllow {
			a.permAskState.selected--
		}
		return a, nil
	case "down", "j":
		if a.permAskState.selected < permAskDeny {
			a.permAskState.selected++
		}
		return a, nil
	case "enter":
		allowAlways := a.permAskState.selected == permAskAllowAlways
		allow := a.permAskState.selected == permAskAllow || allowAlways
		if allowAlways && a.permAskState.ask != nil {
			a.allowAlwaysRule(a.permAskState.ask.Name, a.permAskState.ask.Subject)
		}
		a.answerPermissionAsk(allow)
		a.pop()
		return a, a.nextAgent()
	}
	return a, nil
}

// answerPermissionAsk sends the decision on the agent's reply channel in a
// goroutine, exactly as clarify_view does: the agent is blocked on the channel
// and must not wait for the TUI to finish updating.
func (a *App) answerPermissionAsk(allow bool) {
	if a.permAskState.reply != nil {
		reply := a.permAskState.reply
		go func() { reply <- agent.PermissionAskReply{Allow: allow} }()
	}
}

// resolvePendingAsk answers and dismisses a live permission-ask prompt, if one
// is on screen. It is the shared answer-and-dismiss half of the approval view,
// factored out so the ask-off toggle can resolve the pending prompt exactly as
// the view's own keys do: send once on the reply channel the agent loop is
// blocked on, then pop back to the parent view.
func (a *App) resolvePendingAsk(allow bool) {
	if a.permAskState.reply == nil {
		return
	}
	a.answerPermissionAsk(allow)
	a.permAskState = permissionAskViewState{}
	if a.view == viewPermissionAsk {
		a.pop()
	}
}

// allowAlwaysRule writes a scoped Allow rule before answering, so the next
// matching call short-circuits the prompt.
func (a *App) allowAlwaysRule(name, subject string) {
	_ = a.mutateSetting(func(s *config.Settings) {
		rule := name
		if subject != "" {
			rule = name + "(" + subject + ")"
		}
		s.Permissions.Allow = appendIfMissing(s.Permissions.Allow, rule)
	})
}

func appendIfMissing(list []string, v string) []string {
	for _, s := range list {
		if s == v {
			return list
		}
	}
	return append(list, v)
}
