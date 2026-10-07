package tui

import (
	"fmt"
	"strings"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/vulnetix/belai/internal/tui/components"
)

// /help is a full-screen, scrollable reference: the command list and every key
// binding, the same text helpText composes. It is the user's own view, so it
// is never added to the transcript, the session record or a model's turn.

// helpChrome is what the screen spends outside the body: the view padding (2),
// the header and rule (2) and the key line.
const helpChrome = 5

type helpViewState struct {
	scroll int

	// lines is the wrapped help body, cached by width. sections holds the line
	// index of each section title, for tab and shift+tab.
	lines    []string
	sections []int
	wide     int
	macHints bool
}

func (a *App) enterHelp() tea.Cmd {
	a.helpState = helpViewState{}
	return nil
}

func (a *App) helpBodyHeight() int {
	if a.height <= 0 {
		return 20
	}
	return max(a.height-helpChrome, 3)
}

// helpLines wraps the help body to w columns, once per width and hint setting.
// A binding row keeps a hanging indent, so a long description reads as one entry.
func (a *App) helpLines(w int) ([]string, []int) {
	st := &a.helpState
	hints := components.MacKeyHintsOn()
	if st.lines != nil && st.wide == w && st.macHints == hints {
		return st.lines, st.sections
	}
	var lines []string
	var sections []int
	for _, raw := range strings.Split(helpText(a.registry), "\n") {
		switch {
		case raw == "":
			lines = append(lines, "")
		case !strings.HasPrefix(raw, " ") && strings.HasSuffix(raw, ":"):
			sections = append(sections, len(lines))
			for _, l := range strings.Split(ansi.Wrap(raw, w, ""), "\n") {
				lines = append(lines, components.AccentStyle.Bold(true).Render(l))
			}
		case strings.HasPrefix(raw, "  "):
			lines = append(lines, wrapHelpRow(raw, w)...)
		default:
			lines = append(lines, strings.Split(ansi.Wrap(raw, w, ""), "\n")...)
		}
	}
	st.lines, st.sections, st.wide, st.macHints = lines, sections, w, hints
	return lines, sections
}

// wrapHelpRow wraps "  keys — description" to w, indenting the continuation
// lines under the description. A prefix wider than half the line would leave
// too little room, so it falls back to a short indent.
func wrapHelpRow(raw string, w int) []string {
	const sep = " — "
	i := strings.Index(raw, sep)
	if i < 0 {
		return strings.Split(ansi.Wrap(raw, w, ""), "\n")
	}
	prefix := raw[:i+len(sep)]
	pw := utf8.RuneCountInString(prefix)
	if pw > w/2 {
		pw = 4
		body := strings.Split(ansi.Wrap(raw, w, ""), "\n")
		for j := 1; j < len(body); j++ {
			body[j] = strings.Repeat(" ", pw) + strings.TrimLeft(body[j], " ")
		}
		return body
	}
	trim := strings.TrimSpace(raw[:i])
	padN := utf8.RuneCountInString(raw[:i]) - 2 - utf8.RuneCountInString(trim)
	lead := "  " + components.KeyStyle.Render(trim) + strings.Repeat(" ", max(padN, 0)) + sep
	desc := strings.Split(ansi.Wrap(raw[i+len(sep):], max(w-pw, 10), ""), "\n")
	out := make([]string, 0, len(desc))
	for j, d := range desc {
		if j == 0 {
			out = append(out, lead+d)
			continue
		}
		out = append(out, strings.Repeat(" ", pw)+d)
	}
	return out
}

func (a *App) helpView() string {
	w := a.contentWidth()
	st := &a.helpState
	lines, _ := a.helpLines(w)
	h := a.helpBodyHeight()
	maxScroll := max(len(lines)-h, 0)
	st.scroll = min(max(st.scroll, 0), maxScroll)
	end := min(st.scroll+h, len(lines))

	var b strings.Builder
	b.WriteString(components.SectionHeader("Help", "commands and keys · read only", w))
	b.WriteString(strings.Join(lines[st.scroll:end], "\n"))
	b.WriteString("\n")
	pos := ""
	if len(lines) > h {
		pos = fmt.Sprintf("%d–%d/%d", st.scroll+1, end, len(lines))
	}
	b.WriteString(glHelpLine([]glKey{
		{"↑↓", "scroll", 1}, {"esc", "back", 1}, {"pgup/pgdn", "page", 2},
		{"tab", "next section", 2}, {"g/G", "ends", 3},
	}, max(w-len(pos)-2, 20)))
	if pos != "" {
		b.WriteString("  " + components.MutedStyle.Render(pos))
	}
	return lipgloss.NewStyle().Padding(1).Render(b.String())
}

func (a *App) handleHelpKey(m tea.KeyMsg) (tea.Model, tea.Cmd) {
	st := &a.helpState
	_, sections := a.helpLines(a.contentWidth())
	page := max(a.helpBodyHeight()-1, 1)
	scrollTo := func(v int) { st.scroll = max(v, 0) } // clamped in the render
	switch m.String() {
	case "esc", "q":
		a.pop()
	case "down", "j":
		scrollTo(st.scroll + 1)
	case "up", "k":
		scrollTo(st.scroll - 1)
	case "pgdown", "ctrl+d", " ":
		scrollTo(st.scroll + page)
	case "pgup", "ctrl+u":
		scrollTo(st.scroll - page)
	case "home", "g":
		scrollTo(0)
	case "end", "G":
		scrollTo(1 << 30)
	case "tab", "n":
		for _, at := range sections {
			if at > st.scroll {
				scrollTo(at)
				return a, nil
			}
		}
	case "shift+tab", "p":
		for i := len(sections) - 1; i >= 0; i-- {
			if sections[i] < st.scroll {
				scrollTo(sections[i])
				return a, nil
			}
		}
		scrollTo(0)
	}
	return a, nil
}

// scrollHelp moves the help screen by one wheel notch, three lines.
func (a *App) scrollHelp(m tea.MouseEvent) {
	switch m.Button {
	case tea.MouseButtonWheelUp:
		a.helpState.scroll -= 3
	case tea.MouseButtonWheelDown:
		a.helpState.scroll += 3
	}
	a.helpState.scroll = max(a.helpState.scroll, 0) // the top is clamped in the render
}
