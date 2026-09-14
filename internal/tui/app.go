// Package tui implements the Codex-style terminal UI: a message list, a
// streaming assistant output, a slash-command editor with autocomplete, and a
// status footer.
package tui

import (
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/vulnetix/signet/internal/modelselect"
	"github.com/vulnetix/signet/internal/tui/components"
)

// App is the Bubble Tea model for the Signet TUI.
type App struct {
	registry     *Registry
	messages     []components.Message
	editor       components.Editor
	footer       components.Footer
	width        int
	mode         string
	providerKey  string
	autocomplete []string

	// simulated streaming state
	streaming  bool
	words      []string
	wordIdx    int
	streamText string
}

// NewApp builds a TUI app for a working directory. providerKey may be empty;
// with no key the app streams a simulated reply so the UI is smoke-testable.
func NewApp(workdir, providerKey string) *App {
	sel, err := modelselect.Restore()
	if err != nil || sel.Model == "" {
		sel = modelselect.Default()
	}
	a := &App{
		registry:    NewRegistry(workdir),
		editor:      components.NewEditor(),
		footer:      components.Footer{Session: "new", Model: sel.Model, Cost: "$0.00"},
		mode:        "agent",
		providerKey: providerKey,
	}
	_ = a.editor.Focus()
	return a
}

type tickMsg struct{}

func tick() tea.Cmd {
	return tea.Tick(40*time.Millisecond, func(time.Time) tea.Msg { return tickMsg{} })
}

// Init implements tea.Model.
func (a *App) Init() tea.Cmd { return nil }

// Update implements tea.Model.
func (a *App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch m := msg.(type) {
	case tea.WindowSizeMsg:
		a.width = m.Width
		if m.Width > 4 {
			a.editor.SetWidth(m.Width - 4)
		}
		return a, nil

	case tea.KeyMsg:
		switch m.String() {
		case "ctrl+c":
			return a, tea.Quit
		case "enter":
			if a.streaming {
				return a, nil
			}
			input := strings.TrimSpace(a.editor.Value())
			a.editor.Reset()
			a.autocomplete = nil
			if input == "" {
				return a, nil
			}
			if strings.HasPrefix(input, "/") {
				a.handleCommand(input)
				return a, nil
			}
			a.messages = append(a.messages, components.Message{Role: "user", Content: input})
			return a, a.startStream(replyText(input, a.providerKey))
		}

	case tickMsg:
		if a.streaming {
			return a.advanceStream()
		}
		return a, nil
	}

	cmd := a.editor.Update(msg)
	a.autocomplete = a.registry.Complete(a.editor.Value())
	return a, cmd
}

// View implements tea.Model.
func (a *App) View() string {
	list := components.MessageList{Messages: a.messages, Width: a.width}
	var b strings.Builder
	b.WriteString(list.View())
	if len(a.autocomplete) > 0 {
		b.WriteString("suggestions: " + strings.Join(a.autocomplete, "  ") + "\n")
	}
	b.WriteString(a.editor.View())
	b.WriteString("\n")
	b.WriteString(a.footer.View())
	return lipgloss.NewStyle().Padding(1).Render(b.String())
}

// startStream begins a word-by-word simulated stream of text.
func (a *App) startStream(text string) tea.Cmd {
	a.streaming = true
	a.words = strings.Fields(text)
	a.wordIdx = 0
	a.streamText = ""
	a.messages = append(a.messages, components.Message{Role: "assistant"})
	return tick()
}

// advanceStream appends the next word; when done it stops streaming.
func (a *App) advanceStream() (tea.Model, tea.Cmd) {
	if a.wordIdx < len(a.words) {
		if a.streamText != "" {
			a.streamText += " "
		}
		a.streamText += a.words[a.wordIdx]
		a.wordIdx++
		a.messages[len(a.messages)-1].Content = a.streamText
		return a, tick()
	}
	a.streaming = false
	return a, nil
}

// handleCommand dispatches a slash command.
func (a *App) handleCommand(input string) {
	name, arg, _ := strings.Cut(strings.TrimPrefix(input, "/"), " ")
	switch name {
	case "plan":
		if a.mode == "plan" {
			a.mode = "agent"
			a.addSystem("plan mode off")
		} else {
			a.mode = "plan"
			a.addSystem("plan mode on (read-only)")
		}
	case "todos":
		a.addSystem("todos: no plan tracked yet")
	case "profile":
		a.addSystem("profile: use /profile <name> to switch")
	case "goal":
		if arg != "" {
			a.addSystem("goal replay: " + arg)
		} else {
			a.addSystem("goal: use /goal <name> to replay a memorised goal")
		}
	case "code-review":
		a.addSystem("code-review: running Vulnetix CLI (integration point)")
	default:
		a.addSystem("unknown command: " + input)
	}
}

func (a *App) addSystem(text string) {
	a.messages = append(a.messages, components.Message{Role: "system", Content: text})
}

// replyText produces the streamed reply. Without a provider key it returns a
// simulated message; with a key it marks the provider-stream integration point.
func replyText(input, providerKey string) string {
	if providerKey == "" {
		return "This is a simulated streaming reply. Set a provider key to stream from the model."
	}
	return "Provider streaming integration point for: " + input
}
