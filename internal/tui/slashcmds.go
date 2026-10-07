package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/belai/internal/commandlib"
	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/sanitize"
)

// Custom slash commands (internal/commandlib): a /name that is not one of the
// built-in commands expands a Markdown template from ~/.vulnetix/belai/commands
// or the project's .vulnetix/belai/commands into an ordinary model turn. A
// built-in command always wins over a custom one of the same name, so a file
// can never replace /clear or /model.

// runCustomCommand expands a custom command and sends the result as a typed
// prompt would be sent: it is echoed, admitted by the turn (the sanitiser and
// the classifier run there) and rides the same mode selection. A template's
// "@path" text is plain text here: no file is attached by an expansion. While
// a turn runs or is being prepared the command is refused rather than queued.
func (a *App) runCustomCommand(c commandlib.Command, args string) tea.Cmd {
	if a.working() || a.preSend {
		a.addSystem("/" + c.Name + ": a turn is running; run it again when it ends")
		return nil
	}
	text, err := commandlib.Expand(c, args)
	if err != nil {
		a.addSystem("/" + c.Name + ": " + err.Error())
		return nil
	}
	if strings.TrimSpace(text) == "" {
		a.addSystem("/" + c.Name + ": the command expanded to nothing")
		return nil
	}
	line := "/" + c.Name
	if args = strings.TrimSpace(args); args != "" {
		line += " " + oneLine(args, 200)
	}
	a.addSystem("command: " + line)
	// Agent mode with no agent engaged waits on the picker, as a typed prompt
	// does; acceptAgent finishes the submit from the composer.
	if a.mode == "agent" && a.namedAgent == "" && !a.modeAuto && !a.agentPickerOpen {
		a.editor.SetValue(text)
		a.openAgentPicker()
		a.agentPickerSubmit = true
		a.relayout()
		return nil
	}
	return a.submitInput(text)
}

// oneLine flattens s to a single sanitized line of at most n runes.
func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(sanitize.Sanitize(s)), " ")
	if r := []rune(s); len(r) > n {
		s = string(r[:n]) + "…"
	}
	return s
}

// customCommandsCommand implements /commands: the custom slash commands in both
// layers and any file that was left out.
func (a *App) customCommandsCommand() tea.Cmd {
	a.addSystem(commandsListing(commandlib.Load(a.workdir), a.registry))
	return nil
}

// commandsListing renders /commands. A name a built-in command shadows is
// marked, since the built-in runs instead.
func commandsListing(set commandlib.Set, r *Registry) string {
	if len(set.Commands) == 0 && len(set.Skipped) == 0 {
		return "no custom commands · add a Markdown file under ~/.vulnetix/belai/commands/<name>.md (or the project's .vulnetix/belai/commands/), or install one from the website's library with `belai command import`"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d custom command(s):", len(set.Commands))
	for _, c := range set.Commands {
		hint := ""
		if c.ArgumentHint != "" {
			hint = " " + oneLine(c.ArgumentHint, 60)
		}
		note := ""
		if _, builtin := r.Command(r.Canonical(c.Name)); builtin {
			note = " (shadowed by the built-in /" + c.Name + ")"
		}
		fmt.Fprintf(&b, "\n  /%s%s [%s]%s: %s", c.Name, hint, scopeWord(c.Scope), note, oneLine(c.Description, 100))
	}
	for _, s := range set.Skipped {
		fmt.Fprintf(&b, "\n  skipped %s [%s]: %s", oneLine(s.Name, 64), scopeWord(s.Scope), oneLine(s.Reason, 120))
	}
	return b.String()
}

func scopeWord(s config.Scope) string {
	if s == config.ScopeProject {
		return "project"
	}
	return "global"
}
