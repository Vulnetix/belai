package tui

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/credentials"
	"github.com/vulnetix/signet/internal/gitinfo"
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
	height       int
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

	// workdir and session
	workdir   string
	sessionID string

	// settings / state persistence
	settings config.Settings
	state    config.State

	// layout
	vp viewport.Model
}

type tickMsg time.Time

func tickCmd() tea.Cmd {
	return tea.Tick(2*time.Second, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

// New builds a TUI app from Options.
func New(opts Options) *App {
	workdir := opts.Workdir
	if workdir == "" {
		workdir, _ = os.Getwd()
	}

	st, _ := config.LoadState()
	settings, _ := config.LoadMerged(workdir)

	sel, err := modelselect.Restore()
	if err != nil || sel.Model == "" {
		sel = modelselect.Default()
	}
	if settings.Model != "" {
		sel.Model = settings.Model
	}
	if st.Model != "" {
		sel.Model = st.Model
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
	if name == "" && settings.Provider != "" {
		name = settings.Provider
	}
	if name == "" && st.Provider != "" {
		name = st.Provider
	}
	if name == "" && opts.Resolver != nil {
		configured := opts.Resolver.ConfiguredProviders()
		if len(configured) == 1 {
			name = configured[0]
		}
	}
	cfg, status := run.Prepare(sel.Model, name, src)

	mode := st.LastMode
	if mode == "" {
		mode = "agent"
	}

	a := &App{
		registry: NewRegistry(workdir),
		editor:   components.NewEditor(),
		footer:   components.Footer{Session: "new", Model: sel.Model, Cost: "$0.00"},
		mode:     mode,
		ctx:      context.Background(),
		cfg:      cfg,
		status:   status,
		client:   opts.Client,
		resolver: opts.Resolver,
		pending:  opts.Prompt,
		workdir:  workdir,
		settings: settings,
		state:    st,
		vp:       viewport.New(80, 24),
	}
	if a.status.Configured {
		a.SetClassifier(run.NewClassifier(a.cfg, a.client))
	}
	_ = a.editor.Focus()

	if !status.Configured {
		a.showCredentialMessage(cfg.Provider, opts.Resolver)
	}

	if opts.Prompt != "" && status.Configured {
		a.messages = append(a.messages, components.Message{Role: "user", Content: opts.Prompt})
	}

	a.initCredentialState()
	return a
}

func (a *App) showCredentialMessage(provider string, resolver *credentials.Resolver) {
	if resolver == nil {
		a.addSystem(fmt.Sprintf("no provider credentials found. Type /credentials to configure."))
		return
	}
	configured := resolver.ConfiguredProviders()
	if len(configured) == 0 {
		a.addSystem("no provider credentials found. Type /credentials to configure.")
		return
	}
	var others []string
	for _, p := range configured {
		if p != provider {
			others = append(others, p)
		}
	}
	if len(others) > 0 {
		a.addSystem(fmt.Sprintf("%s is configured; %s is not. Type /model to switch provider.", strings.Join(others, ", "), provider))
	} else {
		a.addSystem(fmt.Sprintf("%s credentials missing (%s). Type /credentials to configure.", provider, strings.Join(a.status.Missing, ", ")))
	}
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
	cmds := []tea.Cmd{tickCmd()}
	if a.pending != "" && a.status.Configured {
		cmds = append(cmds, a.sendPending())
	}
	return tea.Batch(cmds...)
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
		a.height = m.Height
		if m.Width > 4 {
			a.editor.SetWidth(m.Width - 4)
		}
		// Reserve rows for padding(2) + editor + status(1) + gap(1)
		vpHeight := m.Height - 8
		if vpHeight < 5 {
			vpHeight = 5
		}
		a.vp.Width = m.Width - 2
		a.vp.Height = vpHeight
		return a, nil
	case tickMsg:
		return a, tickCmd()

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
		if a.view == viewSettings {
			if m.String() == "esc" {
				a.view = viewChat
				return a, nil
			}
			return a, nil
		}

		switch m.String() {
		case "ctrl+c":
			text := a.editor.Value()
			if text != "" {
				termenv.Copy(text)
			}
			return a, nil
		case "ctrl+d":
			if strings.TrimSpace(a.editor.Value()) == "" {
				return a, tea.Quit
			}
			return a, nil
		case "shift+tab":
			a.cycleMode()
			return a, nil
		case "ctrl+l":
			a.messages = nil
			return a, nil
		case "esc":
			if a.view != viewChat {
				a.view = viewChat
				return a, nil
			}
			return a, nil
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

	var b strings.Builder
	for _, m := range a.messages {
		b.WriteString("[" + m.Role + "] ")
		b.WriteString(m.Content)
		b.WriteString("\n\n")
	}
	a.vp.SetContent(b.String())

	var sb strings.Builder
	if len(a.messages) < 3 {
		banner := components.Banner{Width: a.width}
		sb.WriteString(banner.View())
		sb.WriteString("\n")
	}
	sb.WriteString(a.vp.View())
	if len(a.autocomplete) > 0 {
		sb.WriteString("suggestions: " + strings.Join(a.autocomplete, "  ") + "\n")
	}
	sb.WriteString(a.editor.View())
	sb.WriteString("\n")
	a.footer.Width = a.width
	a.footer.Mode = a.mode
	a.footer.Provider = a.cfg.Provider
	a.footer.Model = a.cfg.Model
	if info, ok := gitinfo.Detect(a.workdir); ok {
		a.footer.Branch = info.Branch
		if a.footer.Cwd == "" {
			a.footer.Cwd = a.workdir
		}
	}
	sb.WriteString(a.footer.View())
	return lipgloss.NewStyle().Padding(1).Render(sb.String())
}

// handleCommand dispatches a slash command.
func (a *App) handleCommand(input string) {
	name, arg, _ := strings.Cut(strings.TrimPrefix(input, "/"), " ")
	cmd, ok := a.registry.Command(name)
	if !ok {
		a.addSystem("unknown command: " + input)
		return
	}
	_ = cmd
	switch name {
	case "plan":
		if a.mode == "plan" {
			a.mode = "agent"
			a.addSystem("plan mode off")
		} else {
			a.mode = "plan"
			a.addSystem("plan mode on (read-only)")
		}
		a.saveMode()
	case "mode":
		if arg != "" {
			a.mode = arg
			a.saveMode()
			a.addSystem("mode: " + arg)
		} else {
			a.addSystem("mode: " + a.mode)
		}
	case "help":
		var lines []string
		lines = append(lines, "commands:")
		for _, n := range a.registry.Names() {
			if c, ok := a.registry.Command(n); ok {
				lines = append(lines, "  /"+n+" — "+c.Description)
			}
		}
		a.addSystem(strings.Join(lines, "\n"))
	case "model":
		if arg != "" {
			a.switchProvider(arg)
		} else {
			a.addSystem("model: " + a.cfg.Provider + " / " + a.cfg.Model + "\nType /model <provider> to switch.")
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

func (a *App) cycleMode() {
	switch a.mode {
	case "agent":
		a.mode = "plan"
		a.addSystem("plan mode on (read-only)")
	case "plan":
		a.mode = "goal"
		a.addSystem("goal mode on")
	case "goal":
		a.mode = "agent"
		a.addSystem("agent mode on")
	}
	a.saveMode()
}

func (a *App) saveMode() {
	st, _ := config.LoadState()
	st.LastMode = a.mode
	_ = config.SaveState(st)
}

func (a *App) saveState() {
	st, _ := config.LoadState()
	st.Model = a.cfg.Model
	st.Provider = a.cfg.Provider
	st.LastMode = a.mode
	_ = config.SaveState(st)
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

func (a *App) switchProvider(name string) {
	src := run.CredentialSource(run.EnvSource(os.Getenv))
	if a.resolver != nil {
		src = a.resolver
	}
	cfg, status := run.Prepare(a.cfg.Model, name, src)
	a.cfg = cfg
	a.status = status
	if a.status.Configured {
		a.SetClassifier(run.NewClassifier(a.cfg, a.client))
	}
	a.footer.Model = cfg.Model
	a.footer.Provider = cfg.Provider
	a.saveState()
	if a.status.Configured {
		a.addSystem("switched to " + cfg.Provider + " / " + cfg.Model)
	} else {
		a.showCredentialMessage(cfg.Provider, a.resolver)
	}
}

func (a *App) refreshCredentials() {
	a.credentialState.sets = nil
}
