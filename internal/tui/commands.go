package tui

import (
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/signet/internal/goals"
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

// Registry holds the slash commands and provides autocomplete.
type Registry struct {
	commands map[string]Command
	workdir  string
}

// NewRegistry returns a Registry with the built-in commands registered.
func NewRegistry(workdir string) *Registry {
	r := &Registry{commands: map[string]Command{}, workdir: workdir}

	r.Register("profile", "switch agent profile", nil, func(a *App, arg string) tea.Cmd {
		a.addSystem("profile: use /profile <name> to switch")
		return nil
	})
	r.Register("model", "pick provider and model", nil, func(a *App, arg string) tea.Cmd {
		return a.push(viewModel)
	})
	r.Register("mode", "show or set operating mode", nil, func(a *App, arg string) tea.Cmd {
		if arg != "" {
			a.mode = arg
			a.modeExplicit = true
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
		a.saveMode()
		return nil
	})
	r.Register("todos", "show plan progress", nil, func(a *App, arg string) tea.Cmd {
		a.addSystem("todos: no plan tracked yet")
		return nil
	})
	r.Register("goal", "memorise or replay a goal", func() []string {
		names, _ := goals.Names(workdir)
		return names
	}, func(a *App, arg string) tea.Cmd {
		if arg != "" {
			a.addSystem("goal replay: " + arg)
		} else {
			a.addSystem("goal: use /goal <name> to replay a memorised goal")
		}
		return nil
	})
	r.Register("code-review", "run Vulnetix code review", nil, func(a *App, arg string) tea.Cmd {
		a.addSystem("code-review: running Vulnetix CLI (integration point)")
		return nil
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
