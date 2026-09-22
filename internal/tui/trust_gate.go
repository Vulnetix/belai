package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/vulnetix/signet/internal/trustgate"
	"github.com/vulnetix/signet/internal/tui/components"
)

// trustGateModel is the standalone first-run confirmation program. It runs
// before tui.Start because everything dangerous (settings merge, posture load,
// repo-map scan, auto-started processes) happens during App construction.
type trustGateModel struct {
	st       trustgate.Status
	selected int
	width    int
	height   int
	proceed  bool
	err      error
}

const (
	trustGateDecline = iota
	trustGateAccept
)

// RunTrustGate blocks on a minimal confirmation dialog and reports whether the
// app may proceed. Granting trust and recording declines happen here, so the
// caller never needs to know which option the user picked.
func RunTrustGate(st trustgate.Status) (bool, error) {
	m := trustGateModel{st: st, selected: trustGateDecline}
	p := tea.NewProgram(m, tea.WithoutSignalHandler())
	out, err := p.Run()
	if err != nil {
		return false, err
	}
	fm, _ := out.(trustGateModel)
	return fm.proceed, fm.err
}

func (m trustGateModel) Init() tea.Cmd { return nil }

func (m trustGateModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil
	case tea.KeyMsg:
		switch msg.String() {
		case "esc", "ctrl+c":
			return m.finish(false)
		case "up", "k":
			if m.selected > trustGateDecline {
				m.selected--
			}
			return m, nil
		case "down", "j":
			if m.selected < trustGateAccept {
				m.selected++
			}
			return m, nil
		case "enter":
			return m.finish(m.selected == trustGateAccept)
		}
	}
	return m, nil
}

// finish applies the decision: accept grants trust (and the proposed dirs),
// decline records the refusal when already trusted and exits when not.
func (m trustGateModel) finish(accept bool) (tea.Model, tea.Cmd) {
	if accept {
		if err := trustgate.Grant(m.st.Workdir, m.st.NewDirs); err != nil {
			m.err = err
			m.proceed = false
		} else {
			m.proceed = true
		}
		return m, tea.Quit
	}
	if m.st.Trusted {
		if err := trustgate.Refuse(m.st.Workdir, m.st.NewDirs); err != nil {
			m.err = err
		}
		m.proceed = true
		return m, tea.Quit
	}
	m.proceed = false
	return m, tea.Quit
}

func (m trustGateModel) View() string {
	width := m.width
	if width < 40 {
		width = 80
	}
	var b strings.Builder
	if m.st.Trusted {
		b.WriteString(components.SectionHeader("This folder proposes new workspace directories", "esc cancel", width))
		b.WriteString("\n" + components.EmphStyle.Render(m.st.Workdir) + "\n\n")
	} else {
		b.WriteString(components.SectionHeader("Accessing workspace", "esc exit", width))
		b.WriteString("\n" + components.EmphStyle.Render(m.st.Workdir) + "\n")
		b.WriteString("\n" + components.WarnStyle.Render("Signet has not seen this directory before. It may contain files that\nconfigure what Signet reads and starts before you interact with it.") + "\n\n")
	}

	if len(m.st.NewDirs) > 0 {
		body := fmt.Sprintf("This folder adds %d director%s to the workspace:", len(m.st.NewDirs), plural(len(m.st.NewDirs)))
		for _, d := range m.st.NewDirs {
			body += "\n  • " + d
		}
		b.WriteString(components.Panel{
			Title:  "workspace directories",
			Body:   body,
			Width:  width,
			Accent: lipgloss.TerminalColor(components.ColorAmber),
			Raw:    true,
		}.View() + "\n\n")
	}

	opts := []string{"No, exit", "Yes, I trust this folder"}
	if m.st.Trusted {
		opts = []string{"No, keep the current workspace", "Yes, add them"}
	}
	for i, o := range opts {
		selected := i == m.selected
		box := "○"
		if selected {
			box = "●"
		}
		line := components.Cursor(selected) + box + " " + o
		if selected {
			line = components.EmphStyle.Render(line)
		}
		b.WriteString("\n" + line)
	}
	b.WriteString("\n\n" + components.HelpBar("↑↓", "move", "enter", "confirm", "esc", "cancel") + "\n")
	return lipgloss.NewStyle().Padding(1).Render(b.String())
}

func plural(n int) string {
	if n == 1 {
		return "y"
	}
	return "ies"
}
