package tui

import (
	"sort"
	"strings"

	"github.com/vulnetix/signet/internal/goals"
)

// Command is one registered slash command.
type Command struct {
	Name        string
	Description string
	// Args returns dynamic argument suggestions (e.g. memorised goal names).
	Args func() []string
}

// Registry holds the slash commands and provides autocomplete.
type Registry struct {
	commands map[string]Command
	workdir  string
}

// NewRegistry returns a Registry with the built-in commands registered.
func NewRegistry(workdir string) *Registry {
	r := &Registry{commands: map[string]Command{}, workdir: workdir}
	r.Register("profile", "switch agent profile", nil)
	r.Register("model", "pick provider and model", nil)
	r.Register("mode", "show or set operating mode", nil)
	r.Register("plan", "toggle plan mode", nil)
	r.Register("todos", "show plan progress", nil)
	r.Register("goal", "memorise or replay a goal", func() []string {
		names, _ := goals.Names(workdir)
		return names
	})
	r.Register("code-review", "run Vulnetix code review", nil)
	r.Register("settings", "view and edit settings", nil)
	r.Register("credentials", "manage provider credentials", func() []string {
		return []string{"openai", "anthropic", "cloudflare-workers-ai", "cloudflare-ai-gateway"}
	})
	r.Register("help", "show available commands", nil)
	return r
}

// Register adds or replaces a command.
func (r *Registry) Register(name, description string, args func() []string) {
	r.commands[name] = Command{Name: name, Description: description, Args: args}
}

// Command returns a command by name.
func (r *Registry) Command(name string) (Command, bool) {
	c, ok := r.commands[name]
	return c, ok
}

// Names returns all registered command names, sorted.
func (r *Registry) Names() []string {
	out := make([]string, 0, len(r.commands))
	for n := range r.commands {
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
	for name := range r.commands {
		if strings.HasPrefix(name, prefix) {
			out = append(out, "/"+name)
		}
	}
	sort.Strings(out)
	return out
}
