package tui

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/signet/internal/agentprofile"
	"github.com/vulnetix/signet/internal/commands"
	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/fuzzy"
	"github.com/vulnetix/signet/internal/profiles"
	"github.com/vulnetix/signet/internal/provider"
	"github.com/vulnetix/signet/internal/vulnetixcli"
)

// Handler runs a slash command with its argument.
type Handler func(*App, string) tea.Cmd

// Command is one registered slash command.
type Command struct {
	Name        string
	Description string
	// Args returns dynamic argument suggestions (e.g. memorised goal names).
	Args    func() []string
	Run     Handler // panics on Register if nil
	AliasOf string  // canonical target; empty for canonical commands
	Hidden  bool    // hidden commands dispatch but are absent from Names/Complete
}

// vulnetixDoneMsg carries the result of an async /vulnetix run.
type vulnetixDoneMsg struct {
	report commands.Report
	err    error
}

// Registry holds the slash commands and provides autocomplete.
type Registry struct {
	commands map[string]Command
	workdir  string

	// profileNames caches the /profile completion candidates. Listing profiles
	// is a directory scan plus a parse/validate per file; Complete runs on
	// every keystroke, so it must not re-scan. The list is loaded once per
	// Registry and is stable because nothing in-process writes the flat
	// profile directory (agent profiles live in a separate tree).
	profileNames       []string
	profileNamesLoaded bool
}

// profileNameList returns the memoised /profile completion names, loading
// them on first use.
func (r *Registry) profileNameList() []string {
	if !r.profileNamesLoaded {
		r.profileNamesLoaded = true
		names, err := profiles.List()
		if err != nil {
			return nil
		}
		out := make([]string, 0, len(names))
		for _, p := range names {
			out = append(out, p.Name)
		}
		r.profileNames = out
	}
	return r.profileNames
}

// NewRegistry returns a Registry with the built-in commands registered.
func NewRegistry(workdir string) *Registry {
	r := &Registry{commands: map[string]Command{}, workdir: workdir}

	r.Register("profile", "switch agent profile", func() []string {
		return r.profileNameList()
	}, func(a *App, arg string) tea.Cmd {
		if arg == "" {
			names, err := profiles.List()
			if err != nil {
				a.addSystem("profile list failed: " + err.Error())
				return nil
			}
			var parts []string
			for _, p := range names {
				marker := ""
				if p.Builtin {
					marker = " (built-in)"
				}
				parts = append(parts, p.Name+marker)
			}
			a.addSystem("profiles: " + strings.Join(parts, ", "))
			return nil
		}
		p, err := profiles.Load(arg)
		if err != nil {
			a.addSystem("profile: " + err.Error())
			return nil
		}
		a.agentExplicit = true
		a.setNamedAgent(p.Name)
		a.mode = "agent"
		a.modeExplicit = true
		a.modeSticky = true
		// Re-resolve the carrier and reseal the system prompt on the next send.
		a.syncPlanMode()
		a.addSystem("profile: " + p.Name)
		return nil
	})
	r.Register("model", "pick provider and model for each role", nil, func(a *App, arg string) tea.Cmd {
		return a.push(viewModel)
	})
	r.Register("mode", "show or set operating mode", nil, func(a *App, arg string) tea.Cmd {
		if arg != "" {
			a.mode = arg
			a.modeExplicit = true
			a.modeSticky = true
			a.syncPlanMode()
			a.saveMode()
			a.persistCarrierMeta()
			a.addSystem("mode: " + arg)
		} else {
			a.addSystem("mode: " + a.mode)
		}
		return nil
	})
	r.Register("todos", "show plan progress", nil, func(a *App, arg string) tea.Cmd {
		if a.todos == nil || len(a.todos.Items) == 0 {
			a.addSystem("todos: no plan tracked yet")
			return nil
		}
		p := a.todos.Progress()
		a.addSystem(fmt.Sprintf("todos: %d/%d done", p.Completed(), p.Total))
		return nil
	})
	r.Register("execute", "leave plan mode and execute the plan", nil, func(a *App, arg string) tea.Cmd {
		if a.planReview.name == "" || a.planReview.path == "" {
			a.mode = "agent"
			a.modeExplicit = true
			a.modeSticky = true
			a.syncPlanMode()
			a.saveMode()
			a.addSystem("plan mode off — executing")
			return nil
		}
		return a.submitPlanApprove()
	})
	r.Register("refine", "refine the extracted plan", nil, func(a *App, arg string) tea.Cmd {
		if a.planReview.name == "" || a.planReview.path == "" {
			a.editor.SetValue(a.lastPlanText)
			a.addSystem("refine the plan, then submit")
			return nil
		}
		a.editor.SetValue("")
		a.planReview.selected = planReviewRefine
		return a.submitPlanRefine("")
	})
	r.Register("vulnetix", "Vulnetix code review and firewall", func() []string {
		return []string{"review", "configure", "list", "status", "firewall", "help"}
	}, func(a *App, arg string) tea.Cmd {
		// A review starts on the UI loop: it registers its progress, prints
		// its start line and reports each scanner as it finishes (review.go).
		if inv, err := commands.ParseInvocation(arg); err == nil && inv.Action == commands.ActionRun {
			return a.startReview()
		}
		return func() tea.Msg {
			inv, err := commands.ParseInvocation(arg)
			if err != nil {
				return vulnetixDoneMsg{err: err}
			}
			switch inv.Action {
			case commands.ActionConfigure:
				return a.push(viewVulnetixConfig)()
			case commands.ActionList:
				return a.push(viewVulnetixList)()
			case commands.ActionStatus:
				cli, err := vulnetixcli.Detect()
				if err != nil {
					return vulnetixDoneMsg{err: err}
				}
				if !a.vulnetixConfigState.probedAt.IsZero() && time.Since(a.vulnetixConfigState.probedAt) < 5*time.Minute {
					return vulnetixDoneMsg{report: commands.Report{Status: commands.Vulnetix{}.StatusText(a.vulnetixConfigState.cap)}}
				}
				return func() tea.Msg {
					cap := vulnetixcli.Probe(context.Background(), *cli, vulnetixcli.ProbeOptions{
						Observer: quietObserver{a},
					})
					return vulnetixDoneMsg{report: commands.Report{Status: commands.Vulnetix{}.StatusText(cap)}}
				}
			case commands.ActionFirewall:
				return a.toggleFirewall()
			case commands.ActionHelp:
				return vulnetixDoneMsg{report: commands.Report{Status: "/vulnetix review | configure | list | status | firewall"}}
			default:
				return nil // ActionRun is handled above, on the UI loop
			}
		}
	})
	r.Register("settings", "view and edit settings", nil, func(a *App, arg string) tea.Cmd {
		// Default the settings scope to project so a bare /settings edit
		// lands where the user expects (repo/.vulnetix/settings.json) and
		// the provenance label is honest — unless the user already chose a
		// scope this session, which reopening must not silently undo.
		if !a.settingsState.scopeChosen {
			a.settingsState.scope = config.ScopeProject
		}
		a.settingsState.notice = ""
		return a.push(viewSettings)
	})
	r.Register("providers", "manage providers, credentials and local models", func() []string {
		return []string{"report", "status", "launch", "download", "stop"}
	}, func(a *App, arg string) tea.Cmd {
		sub, flags := parseLocalModelArgs(arg)
		switch sub {
		case "":
			// Bare /providers opens the provider management screen.
			return a.push(viewProviders)
		case "report":
			return a.localModelReportCmd(flags.repo)
		case "status":
			return tea.Batch(a.push(viewProviders), a.localModelStatusCmd())
		case "launch":
			if flags.repo == "" {
				a.addSystem("usage: /providers launch <repo> [--port N] [--quant Q]")
				return nil
			}
			return tea.Batch(a.push(viewProviders), a.localModelLaunchCmd(flags.repo, flags.port, flags.quant))
		case "download":
			if flags.repo == "" {
				a.addSystem("usage: /providers download <repo> [--quant Q]")
				return nil
			}
			return tea.Batch(a.push(viewProviders), a.localModelDownloadCmd(flags.repo, flags.quant))
		case "stop":
			return tea.Batch(a.push(viewProviders), a.localModelStopCmd(flags.port))
		default:
			// Deep-link to a provider detail page.
			_, hasProfile := a.settings.Providers[sub]
			if provider.Builtin(sub) || hasProfile {
				a.openProviderDetail(sub)
				return a.push(viewProviderDetail)
			}
			a.addSystem("unknown /providers subcommand: " + sub)
			return nil
		}
	})
	r.Register("permissions", "edit tool permissions", nil, func(a *App, arg string) tea.Cmd {
		return a.push(viewPermissions)
	})
	r.Register("lsp", "manage language-server diagnostics", nil, func(a *App, arg string) tea.Cmd {
		return a.push(viewLSP)
	})
	r.Register("budgets", "manage token budgets per provider and model", nil, func(a *App, arg string) tea.Cmd {
		return a.openBudgets()
	})
	r.Register("prompts", "manage the prompt library", nil, func(a *App, arg string) tea.Cmd {
		return a.push(viewPrompts)
	})
	r.Register("processes", "manage the process library", nil, func(a *App, arg string) tea.Cmd {
		return a.push(viewProcesses)
	})
	r.RegisterAlias("process", "processes")
	r.Register("yolo", "toggle guardrails and ask together", func() []string {
		return []string{"on", "off"}
	}, func(a *App, arg string) tea.Cmd {
		switch strings.TrimSpace(arg) {
		case "on":
			return a.setYolo(true)
		case "off":
			return a.setYolo(false)
		default:
			a.addSystem("yolo: on turns both guardrails and ask off; off restores the settings-file values")
			return nil
		}
	})
	r.Register("add-dir", "add a directory to the current workspace", nil, func(a *App, arg string) tea.Cmd {
		arg = strings.TrimSpace(arg)
		if arg == "" {
			return a.openAddDirPicker()
		}
		return a.addWorkspaceDirCmd(arg, true)
	})
	r.Register("help", "show commands and keyboard shortcuts", nil, func(a *App, arg string) tea.Cmd {
		a.addSystem(helpText(a.registry))
		return nil
	})
	r.Register("clear", "start a new session", nil, func(a *App, arg string) tea.Cmd {
		a.startNewSession()
		return nil
	})
	r.RegisterAlias("new", "clear")
	r.Register("exit", "quit and print the resume card", nil, func(a *App, arg string) tea.Cmd {
		// Typing a whole command is confirmation enough: no two-press arm, and
		// the exit card prints exactly as it does after the armed ctrl+d.
		if a.rmCancel != nil {
			a.rmCancel()
		}
		a.stopLocalServers()
		if a.procManager != nil {
			a.procManager.Shutdown()
		}
		return tea.Quit
	})
	r.RegisterAlias("quit", "exit")
	r.Register("compact", "summarise the session into a new one", nil, func(a *App, arg string) tea.Cmd {
		return a.compactCmd()
	})
	r.Register("resume", "resume a session by id or browse sessions on disk", nil, func(a *App, arg string) tea.Cmd {
		if strings.TrimSpace(arg) != "" {
			return a.resumeByID(arg)
		}
		return a.push(viewResume)
	})
	r.Register("rename", "rename this session", nil, func(a *App, arg string) tea.Cmd {
		return a.renameSession(arg)
	})
	r.Register("export", "export this session (or one by id) as Markdown", nil, func(a *App, arg string) tea.Cmd {
		return a.exportSessionCmd(arg)
	})
	r.Register("agent", "pick an agent profile or manage background agents", func() []string {
		return []string{"create", "list", "edit", "start", "stop", "pause", "resume", "log"}
	}, func(a *App, arg string) tea.Cmd {
		if strings.TrimSpace(arg) == "" {
			a.openAgentPicker()
			return nil
		}
		sub, rest, _ := strings.Cut(arg, " ")
		switch sub {
		case "create":
			name := strings.TrimSpace(rest)
			if name == "" {
				a.addSystem("agent create <name>")
				return nil
			}
			// Fail locally before any classifier round trip: the name the user
			// typed must be the name on disk, so it has to validate first.
			if err := agentprofile.Stub(name).Validate(); err != nil {
				a.addSystem("agent create: " + err.Error())
				return nil
			}
			if a.classifier == nil {
				a.addSystem("agent create: no classifier configured")
				return nil
			}

			ctx, cancel := context.WithCancel(context.Background())
			handle := a.registerAgentDesignActivity(name, cancel)
			a.setPhaseRoleManager("agent designer")

			b := agentprofile.Builder{Classifier: a.classifier, MaxAttempts: 3, Caveman: a.settings.ClassifierCavemanEnabled()}
			b.OnAttempt = func(attempt int, note string) {
				if handle == nil {
					return
				}
				if note == "" {
					note = fmt.Sprintf("attempt %d/%d", attempt, b.MaxAttempts)
				} else {
					note = fmt.Sprintf("attempt %d/%d: %s", attempt, b.MaxAttempts, note)
				}
				handle.Append(note)
			}

			buildCmd := func() tea.Msg {
				prompt := fmt.Sprintf("Design an agent profile named %q. Infer a meaningful description and a tailored system prompt from the name.", name)
				p, err := b.Build(ctx, prompt)
				if err != nil {
					return agentBuilderDoneMsg{name: name, handle: handle, err: err}
				}
				// The LLM designs the fields but must never rename the user's
				// profile: /agent create <name> is name-first.
				p.Name = name
				path, err := agentprofile.Save(p)
				if err != nil {
					return agentBuilderDoneMsg{name: name, handle: handle, err: err}
				}
				return agentBuilderDoneMsg{name: name, handle: handle, profile: p, path: path}
			}
			return tea.Batch(buildCmd, a.workSpin.Tick)
		case "list":
			return a.openAgentsTab(agentTabProfiles)
		case "edit":
			name := strings.TrimSpace(rest)
			if name == "" {
				a.addSystem("agent edit <name>")
				return nil
			}
			if _, err := agentprofile.Load(name); err != nil {
				a.addSystem("agent edit: " + err.Error())
				return nil
			}
			a.agentState.pendingEditProfile = name
			return a.push(viewAgent)
		case "start", "stop", "pause", "resume", "log":
			name := strings.TrimSpace(rest)
			if name == "" {
				a.addSystem("agent " + sub + " <name>")
				return nil
			}
			switch sub {
			case "start":
				return a.startAgentProfile(name)
			case "stop":
				a.stopAgent(name)
			case "pause":
				a.pauseAgent(name)
			case "resume":
				return a.resumeAgent(name)
			case "log":
				// The log is the agent's audit trail on the /agents screen.
				a.agentState.auditFilter = "bg:" + name
				a.agentState.auditSel = 0
				return a.openAgentsTab(agentTabAudit)
			}
			return nil
		default:
			a.addSystem("agent: unknown subcommand " + sub)
			return nil
		}
	})
	r.Register("agents", "running agents, profiles and the audit trail", func() []string {
		return []string{"running", "profiles", "audit"}
	}, func(a *App, arg string) tea.Cmd {
		switch strings.TrimSpace(arg) {
		case "profiles":
			return a.openAgentsTab(agentTabProfiles)
		case "audit":
			a.agentState.auditFilter = ""
			return a.openAgentsTab(agentTabAudit)
		case "running":
			return a.openAgentsTab(agentTabLive)
		default:
			// Nothing has run yet: the profiles are the useful first view.
			if len(a.ledger.order) == 0 {
				return a.openAgentsTab(agentTabProfiles)
			}
			return a.openAgentsTab(agentTabLive)
		}
	})
	// Hidden alias: dispatchable, absent from Names() and autocomplete.
	r.RegisterHiddenAlias("provider", "providers")
	return r
}

// Register adds or replaces a command. It panics on a nil handler, making a
// "registered but unknown" command structurally impossible to construct.
func (r *Registry) Register(name, description string, args func() []string, run Handler) {
	if run == nil {
		panic("tui: command " + name + " registered with nil handler")
	}
	r.commands[name] = Command{Name: name, Description: description, Args: args, Run: run}
}

// RegisterAlias registers alias as a first-class command dispatching as target.
// Aliases appear in Names() and autocomplete so they stay discoverable.
func (r *Registry) RegisterAlias(alias, target string) {
	r.commands[alias] = Command{Name: alias, AliasOf: target, Description: "alias of /" + target}
}

// RegisterHiddenAlias registers an alias that dispatches but is absent from
// Names() and Complete().
func (r *Registry) RegisterHiddenAlias(alias, target string) {
	r.commands[alias] = Command{Name: alias, AliasOf: target, Description: "alias of /" + target, Hidden: true}
}

// Command returns a command by name.
func (r *Registry) Command(name string) (Command, bool) {
	c, ok := r.commands[name]
	return c, ok
}

// Canonical resolves an alias to its handler name, returning the input
// unchanged when it is not an alias.
func (r *Registry) Canonical(name string) string {
	if c, ok := r.commands[name]; ok && c.AliasOf != "" {
		return c.AliasOf
	}
	return name
}

// Names returns all visible command names, sorted.
func (r *Registry) Names() []string {
	out := make([]string, 0, len(r.commands))
	for n, c := range r.commands {
		if c.Hidden {
			continue
		}
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Complete returns slash-command completions for a partial input such as
// "/pro" (command name) or "/agent st" (argument completion), ranked by
// fuzzy match so "/pmt" still finds /prompts. It returns nil when there is
// nothing to suggest.
func (r *Registry) Complete(input string) []string {
	if !strings.HasPrefix(input, "/") {
		return nil
	}
	body := strings.TrimPrefix(input, "/")
	if !strings.Contains(body, " ") {
		return r.completeCommand(body)
	}
	name, argQuery, _ := strings.Cut(body, " ")
	cmd, ok := r.commands[name]
	if !ok || cmd.Args == nil {
		return nil
	}
	args := slices.Clone(cmd.Args())
	sort.Strings(args)
	var out []string
	for _, a := range fuzzy.Strings(argQuery, args) {
		out = append(out, "/"+name+" "+a)
	}
	return out
}

func (r *Registry) completeCommand(query string) []string {
	var out []string
	for _, n := range fuzzy.Strings(query, r.Names()) {
		out = append(out, "/"+n)
	}
	return out
}
