package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/signet/internal/agentprofile"
	"github.com/vulnetix/signet/internal/commands"
	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/goals"
	"github.com/vulnetix/signet/internal/profiles"
	"github.com/vulnetix/signet/internal/sanitize"
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

// codeReviewDoneMsg carries the result of an async /code-review run.
type codeReviewDoneMsg struct {
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
		a.namedAgent = p.Name
		a.mode = "agent"
		a.modeExplicit = true
		// Re-resolve the carrier and reseal the system prompt on the next send.
		a.syncPlanMode()
		a.addSystem("profile: " + p.Name)
		return nil
	})
	r.Register("model", "pick provider and model", nil, func(a *App, arg string) tea.Cmd {
		return a.push(viewModel)
	})
	r.Register("mode", "show or set operating mode", nil, func(a *App, arg string) tea.Cmd {
		if arg != "" {
			a.mode = arg
			a.modeExplicit = true
			a.syncPlanMode()
			a.saveMode()
			a.addSystem("mode: " + arg)
		} else {
			a.addSystem("mode: " + a.mode)
		}
		return nil
	})
	r.Register("plan", "toggle plan mode", nil, func(a *App, arg string) tea.Cmd {
		if a.mode == "plan" {
			a.mode = "agent"
			a.addSystem("plan mode off")
		} else {
			a.mode = "plan"
			a.addSystem("plan mode on (read-only)")
		}
		a.modeExplicit = true
		a.syncPlanMode()
		a.saveMode()
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
	r.Register("goal", "memorise or replay a goal", func() []string {
		names, _ := goals.Names(workdir)
		return names
	}, func(a *App, arg string) tea.Cmd {
		if arg == "" {
			a.addSystem("goal: use /goal <name> to replay, or /goal memorise <name> <content>")
			return nil
		}
		if rest, ok := strings.CutPrefix(arg, "memorise "); ok {
			name, body, _ := strings.Cut(rest, " ")
			if name == "" || body == "" {
				a.addSystem("goal memorise <name> <content>")
				return nil
			}
			if _, err := goals.Memorise(workdir, goals.Goal{Name: name, Content: sanitize.Sanitize(body)}); err != nil {
				a.addSystem("goal memorise failed: " + err.Error())
				return nil
			}
			a.addSystem("goal memorised: " + name)
			return nil
		}
		g, err := goals.Load(workdir, arg)
		if err != nil {
			a.addSystem("goal: " + err.Error())
			return nil
		}
		a.state.ActiveGoal = arg
		_ = config.SaveState(a.state)
		a.invalidateAgentSession()
		a.addSystem("goal replaying: " + g.Name)
		return nil
	})
	r.Register("execute", "leave plan mode and execute the plan", nil, func(a *App, arg string) tea.Cmd {
		a.mode = "agent"
		a.modeExplicit = true
		a.syncPlanMode()
		a.saveMode()
		a.addSystem("plan mode off — executing")
		return nil
	})
	r.Register("stay", "stay in plan mode", nil, func(a *App, arg string) tea.Cmd {
		a.mode = "plan"
		a.modeExplicit = true
		a.syncPlanMode()
		a.addSystem("staying in plan mode")
		return nil
	})
	r.Register("refine", "refine the extracted plan", nil, func(a *App, arg string) tea.Cmd {
		a.editor.SetValue(a.lastPlanText)
		a.addSystem("refine the plan, then submit")
		return nil
	})
	r.Register("code-review", "run Vulnetix code review", nil, func(a *App, arg string) tea.Cmd {
		return func() tea.Msg {
			cli, err := vulnetixcli.Detect()
			if err != nil {
				return codeReviewDoneMsg{err: err}
			}
			rep, err := commands.CodeReview{CLI: cli, Workdir: a.workdir}.Run()
			return codeReviewDoneMsg{report: rep, err: err}
		}
	})
	r.Register("settings", "view and edit settings", nil, func(a *App, arg string) tea.Cmd {
		return a.push(viewSettings)
	})
	r.Register("credentials", "manage provider credentials", nil, func(a *App, arg string) tea.Cmd {
		return a.push(viewCredentials)
	})
	r.Register("permissions", "edit tool permissions", nil, func(a *App, arg string) tea.Cmd {
		return a.push(viewPermissions)
	})
	r.Register("help", "show available commands", nil, func(a *App, arg string) tea.Cmd {
		var lines []string
		lines = append(lines, "commands:")
		for _, n := range a.registry.Names() {
			c, ok := a.registry.Command(n)
			if !ok {
				continue
			}
			if c.AliasOf != "" {
				lines = append(lines, "  /"+n+" — alias of /"+c.AliasOf)
			} else {
				lines = append(lines, "  /"+n+" — "+c.Description)
			}
		}
		a.addSystem(strings.Join(lines, "\n"))
		return nil
	})
	r.Register("clear", "start a new session", nil, func(a *App, arg string) tea.Cmd {
		a.startNewSession()
		return nil
	})
	r.RegisterAlias("new", "clear")
	r.Register("compact", "summarise the session into a new one", nil, func(a *App, arg string) tea.Cmd {
		return a.compactCmd()
	})
	r.Register("rename", "rename this session", nil, func(a *App, arg string) tea.Cmd {
		return a.renameSession(arg)
	})
	r.Register("agent", "manage background agents", func() []string {
		return []string{"create", "list", "start", "stop", "pause", "resume", "log"}
	}, func(a *App, arg string) tea.Cmd {
		if arg == "" {
			a.addSystem("agent: subcommands: create, list, start <name>, stop <name>, pause <name>, resume <name>, log <name>")
			return nil
		}
		sub, rest, _ := strings.Cut(arg, " ")
		switch sub {
		case "create":
			desc := strings.TrimSpace(rest)
			if desc == "" {
				a.addSystem("agent create <description>")
				return nil
			}
			return func() tea.Msg {
				b := agentprofile.Builder{Classifier: a.classifier, MaxAttempts: 3}
				p, err := b.Build(context.Background(), desc)
				if err != nil {
					return agentBuilderDoneMsg{err: err}
				}
				path, err := agentprofile.Save(p)
				if err != nil {
					return agentBuilderDoneMsg{err: err}
				}
				return agentBuilderDoneMsg{profile: p, path: path}
			}
		case "list":
			return a.push(viewAgent)
		case "start":
			name := strings.TrimSpace(rest)
			if name == "" {
				a.addSystem("agent start <name>")
				return nil
			}
			if a.bgManager == nil {
				a.addSystem("agent: no background manager configured")
				return nil
			}
			p, err := agentprofile.Load(name)
			if err != nil {
				a.addSystem("agent start: " + err.Error())
				return nil
			}
			if err := a.bgManager.Start(name, p); err != nil {
				a.addSystem("agent start: " + err.Error())
				return nil
			}
			a.addSystem("agent started: " + name)
			return a.watchAgentEvents(name)
		case "stop":
			name := strings.TrimSpace(rest)
			if name == "" {
				a.addSystem("agent stop <name>")
				return nil
			}
			if a.bgManager == nil {
				a.addSystem("agent: no background manager configured")
				return nil
			}
			if err := a.bgManager.Stop(name); err != nil {
				a.addSystem("agent stop: " + err.Error())
				return nil
			}
			a.addSystem("agent stopped: " + name)
			return nil
		case "pause":
			name := strings.TrimSpace(rest)
			if name == "" {
				a.addSystem("agent pause <name>")
				return nil
			}
			if a.bgManager == nil {
				a.addSystem("agent: no background manager configured")
				return nil
			}
			if err := a.bgManager.Pause(name); err != nil {
				a.addSystem("agent pause: " + err.Error())
				return nil
			}
			a.addSystem("agent paused: " + name)
			return nil
		case "resume":
			name := strings.TrimSpace(rest)
			if name == "" {
				a.addSystem("agent resume <name>")
				return nil
			}
			if a.bgManager == nil {
				a.addSystem("agent: no background manager configured")
				return nil
			}
			if err := a.bgManager.Resume(name); err != nil {
				a.addSystem("agent resume: " + err.Error())
				return nil
			}
			a.addSystem("agent resumed: " + name)
			return a.watchAgentEvents(name)
		case "log":
			name := strings.TrimSpace(rest)
			if name == "" {
				a.addSystem("agent log <name>")
				return nil
			}
			inst, ok := a.bgManager.Lookup(name)
			if !ok {
				a.addSystem("agent log: " + name + " not found")
				return nil
			}
			out := inst.LastOutput()
			if out == "" {
				a.addSystem("agent log: " + name + " — no output yet")
			} else {
				a.addSystem("agent log: " + name + "\n" + out)
			}
			return nil
		default:
			a.addSystem("agent: unknown subcommand " + sub)
			return nil
		}
	})
	// Hidden alias: dispatchable, absent from Names() and autocomplete.
	r.RegisterHiddenAlias("provider", "model")
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
// "/pro" (command name) or "/goal al" (argument completion). It returns nil
// when there is nothing to suggest.
func (r *Registry) Complete(input string) []string {
	if !strings.HasPrefix(input, "/") {
		return nil
	}
	body := strings.TrimPrefix(input, "/")
	if !strings.Contains(body, " ") {
		return r.completeCommand(body)
	}
	name, argPrefix, _ := strings.Cut(body, " ")
	cmd, ok := r.commands[name]
	if !ok || cmd.Args == nil {
		return nil
	}
	var out []string
	for _, a := range cmd.Args() {
		if strings.HasPrefix(a, argPrefix) {
			out = append(out, "/"+name+" "+a)
		}
	}
	sort.Strings(out)
	return out
}

func (r *Registry) completeCommand(prefix string) []string {
	var out []string
	for name, c := range r.commands {
		if c.Hidden {
			continue
		}
		if strings.HasPrefix(name, prefix) {
			out = append(out, "/"+name)
		}
	}
	sort.Strings(out)
	return out
}
