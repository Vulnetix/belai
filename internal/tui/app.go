package tui

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/credentials"
	"github.com/vulnetix/signet/internal/modelselect"
	"github.com/vulnetix/signet/internal/rolemanager"
	"github.com/vulnetix/signet/internal/run"
	"github.com/vulnetix/signet/internal/tui/components"
)

// Options configures a new TUI app.
type Options struct {
	Workdir  string
	Client   *http.Client
	Resolver *credentials.Resolver // nil means environment only
	Provider string
	Model    string
	Prompt   string // optional seed turn
}

// streamChunkMsg wraps one chunk from the streaming channel.
type streamChunkMsg run.Chunk

// App is the Bubble Tea model for the Signet TUI.
type App struct {
	registry     *Registry
	messages     []components.Message
	editor       components.Editor
	footer       components.Footer
	width        int
	mode         string
	autocomplete []string

	// mode classification (optional; nil skips auto-detection)
	classifier  rolemanager.Classifier
	namedAgent  string
	modeWarning string

	// provider & streaming state
	ctx      context.Context
	cfg      run.Config
	status   run.Status
	client   *http.Client
	resolver *credentials.Resolver
	stream   <-chan run.Chunk
	pending  string // pending prompt to send once configured

	// view state
	view            viewState
	credentialState credentialViewState
}

// New builds a TUI app from Options.
func New(opts Options) *App {
	workdir := opts.Workdir
	if workdir == "" {
		workdir, _ = os.Getwd()
	}

	sel, err := modelselect.Restore()
	if err != nil || sel.Model == "" {
		sel = modelselect.Default()
	}
	if merged, err := config.LoadMerged(workdir); err == nil && merged.Model != "" {
		sel.Model = merged.Model
	}
	if opts.Model != "" {
		sel.Model = opts.Model
	}

	src := run.CredentialSource(run.EnvSource(os.Getenv))
	if opts.Resolver != nil {
		src = opts.Resolver
	}
	name := opts.Provider
	if name == "" {
		name = os.Getenv("SIGNET_PROVIDER")
	}
	if name == "" {
		name = os.Getenv("PI_PROVIDER")
	}
	cfg, status := run.Prepare(sel.Model, name, src)

	a := &App{
		registry: NewRegistry(workdir),
		editor:   components.NewEditor(),
		footer:   components.Footer{Session: "new", Model: sel.Model, Cost: "$0.00"},
		mode:     "agent",
		ctx:      context.Background(),
		cfg:      cfg,
		status:   status,
		client:   opts.Client,
		resolver: opts.Resolver,
		pending:  opts.Prompt,
	}
	if a.status.Configured {
		a.SetClassifier(run.NewClassifier(a.cfg, a.client))
	}
	_ = a.editor.Focus()

	if !status.Configured {
		a.addSystem(fmt.Sprintf("%s credentials missing (%s). Type /credentials to configure.", cfg.Provider, strings.Join(status.Missing, ", ")))
	}

	if opts.Prompt != "" && status.Configured {
		a.messages = append(a.messages, components.Message{Role: "user", Content: opts.Prompt})
	}

	a.initCredentialState()
	return a
}

// NewApp is a deprecated shim; use New(Options{}) instead.
func NewApp(workdir, providerKey string) *App {
	return New(Options{Workdir: workdir})
}

// SetClassifier installs the operating-mode classifier.
func (a *App) SetClassifier(c rolemanager.Classifier) {
	a.classifier = c
}

// Init implements tea.Model.
func (a *App) Init() tea.Cmd {
	if a.pending != "" && a.status.Configured {
		return a.sendPending()
	}
	return nil
}

func (a *App) sendPending() tea.Cmd {
	prompt := a.pending
	a.pending = ""
	return a.send([]run.Turn{{Role: "user", Content: prompt}})
}

// send starts a streaming request with the given conversation turns.
func (a *App) send(turns []run.Turn) tea.Cmd {
	if !a.status.Configured {
		return func() tea.Msg {
			return streamChunkMsg{Err: fmt.Errorf("%s credentials missing (%s). Type /credentials to configure.", a.cfg.Provider, strings.Join(a.status.Missing, ", ")), Done: true}
		}
	}
	ch, err := run.Stream(a.ctx, a.cfg, turns, a.client)
	if err != nil {
		return func() tea.Msg {
			return streamChunkMsg{Err: err, Done: true}
		}
	}
	a.stream = ch
	a.messages = append(a.messages, components.Message{Role: "assistant"})
	return a.next()
}

func (a *App) next() tea.Cmd {
	return func() tea.Msg {
		c, ok := <-a.stream
		if !ok {
			c.Done = true
		}
		return streamChunkMsg(c)
	}
}

// Update implements tea.Model.
func (a *App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch m := msg.(type) {
	case tea.WindowSizeMsg:
		a.width = m.Width
		if m.Width > 4 {
			a.editor.SetWidth(m.Width - 4)
		}
		return a, nil

	case streamChunkMsg:
		if m.Err != nil {
			a.addSystem("provider error: " + m.Err.Error())
			return a, nil
		}
		if len(a.messages) > 0 && a.messages[len(a.messages)-1].Role == "assistant" {
			a.messages[len(a.messages)-1].Content += m.Text
		}
		if !m.Done {
			return a, a.next()
		}
		return a, nil

	case tea.KeyMsg:
		if a.view == viewCredentials {
			return a.handleCredentialKey(m)
		}

		switch m.String() {
		case "ctrl+c":
			return a, tea.Quit
		case "enter":
			if a.view != viewChat {
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
			a.classifyMode(input)
			a.messages = append(a.messages, components.Message{Role: "user", Content: input})
			return a, a.send(a.buildTurns())
		}
	}

	cmd := a.editor.Update(msg)
	a.autocomplete = a.registry.Complete(a.editor.Value())
	return a, cmd
}

func (a *App) buildTurns() []run.Turn {
	var turns []run.Turn
	for _, m := range a.messages {
		if m.Role == "user" || m.Role == "assistant" {
			turns = append(turns, run.Turn{Role: m.Role, Content: m.Content})
		}
	}
	return turns
}

func (a *App) handleCredentialKey(m tea.KeyMsg) (tea.Model, tea.Cmd) {
	if a.credentialState.setMode {
		switch m.String() {
		case "esc":
			a.credentialState.setMode = false
			a.editor.Masked = false
			a.editor.Reset()
			return a, nil
		case "enter":
			val := a.editor.Value()
			p := a.credentialState.providers[a.credentialState.selectedIdx]
			spec := credentials.Spec(p)
			if len(spec) > 0 && a.resolver != nil {
				_ = a.resolver.Store(p, spec[0].Name, val, a.credentialState.backend)
			}
			a.editor.Reset()
			a.editor.Masked = false
			a.credentialState.setMode = false
			a.refreshCredentials()
			// Re-prepare so status reflects the new credential.
			if a.resolver != nil {
				cfg, status := run.Prepare(a.cfg.Model, a.cfg.Provider, a.resolver)
				a.cfg = cfg
				a.status = status
			}
			if a.pending != "" && a.status.Configured {
				return a, a.sendPending()
			}
			return a, nil
		default:
			cmd := a.editor.Update(m)
			return a, cmd
		}
	}

	switch m.String() {
	case "up", "k":
		if a.credentialState.selectedIdx > 0 {
			a.credentialState.selectedIdx--
		}
		return a, nil
	case "down", "j":
		if a.credentialState.selectedIdx < len(a.credentialState.providers)-1 {
			a.credentialState.selectedIdx++
		}
		return a, nil
	case "esc":
		a.view = viewChat
		return a, nil
	case "s":
		a.credentialState.setMode = true
		a.editor.Masked = true
		_ = a.editor.Focus()
		return a, nil
	case "c":
		if a.resolver != nil {
			p := a.credentialState.providers[a.credentialState.selectedIdx]
			for _, f := range credentials.Spec(p) {
				_ = a.resolver.Clear(p, f.Name, a.credentialState.backend)
			}
			a.refreshCredentials()
		}
		return a, nil
	case "b":
		if a.resolver != nil {
			backends := a.resolver.Backends()
			var writable []credentials.Source
			for _, be := range backends {
				if be.Writable && be.Available {
					writable = append(writable, credentials.Source(be.Name))
				}
			}
			if len(writable) == 0 {
				return a, nil
			}
			for i, s := range writable {
				if s == a.credentialState.backend {
					a.credentialState.backend = writable[(i+1)%len(writable)]
					break
				}
			}
		}
		return a, nil
	}

	cmd := a.editor.Update(m)
	return a, cmd
}

// View implements tea.Model.
func (a *App) View() string {
	switch a.view {
	case viewCredentials:
		return a.credentialView()
	case viewSettings:
		return "Settings view (placeholder)\n\nPress esc to return."
	}

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
	case "settings":
		a.view = viewSettings
	case "credentials":
		a.view = viewCredentials
	default:
		a.addSystem("unknown command: " + input)
	}
}

func (a *App) addSystem(text string) {
	a.messages = append(a.messages, components.Message{Role: "system", Content: text})
}

// classifyMode runs the operating-mode classifier on a user prompt that did
// not explicitly specify a mode. The classifier sees only the prompt text.
func (a *App) classifyMode(input string) {
	if a.classifier == nil {
		return
	}
	d, err := rolemanager.Select(a.classifier, rolemanager.ModeInput{Prompt: input})
	if err != nil {
		a.mode = "agent"
		a.modeWarning = "mode classifier error: " + err.Error()
		a.addSystem(a.modeWarning)
		return
	}
	a.mode = string(d.Mode)
	a.namedAgent = d.AgentName
	a.modeWarning = d.Warning
	if d.AgentName != "" {
		a.addSystem("engaged agent: " + d.AgentName)
	} else {
		msg := "mode: " + string(d.Mode)
		if d.Explore {
			msg += " (launch explore agents)"
		}
		a.addSystem(msg)
	}
	if d.Warning != "" {
		a.addSystem(d.Warning)
	}
}

func (a *App) refreshCredentials() {
	a.credentialState.sets = nil
}
