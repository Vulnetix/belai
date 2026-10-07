package main

import (
	"flag"
	"fmt"
	"io"
	"strings"
)

// helpWidth is the column the help text wraps at.
const helpWidth = 78

// helpFlagColumn is the width of the flag column. A flag whose name and type
// are wider puts its description on the next line.
const helpFlagColumn = 28

// topCommand is one subcommand listed by `belai -help`. rc-session is hidden
// (only the rc daemon runs it) and help is the entry point itself, so neither
// is listed.
type topCommand struct {
	name, summary string
}

var topCommands = []topCommand{
	{"agent", "list, run and manage background agents and fleet workers"},
	{"kanban", "read and edit the global kanban board"},
	{"skill", "list, import and export skills (library items)"},
	{"prompt", "list, import and export prompts (library items)"},
	{"command", "list, import and export custom slash commands (library items)"},
	{"process", "list, import and export supervised processes (library items)"},
	{"repo", "list, sync, import and export git repositories to keep cloned"},
	{"budget", "list, import and export the token budgets (library item)"},
	{"rewrite", "list, import and export the Bash rewrite table (library item)"},
	{"provider", "list, import and export provider sets (library item)"},
	{"mcp", "list, import and export MCP servers (library items)"},
	{"rc", "remote control: let the Vulnetix website start sessions here"},
	{"plugin", "install and manage plugins"},
	{"acp", "serve the Agent Client Protocol to an editor"},
	{"login", "sign in to a provider (kiro)"},
}

// hiddenCommands are dispatched in main but not listed.
var hiddenCommands = []string{"rc-session", "help"}

// helpExamples are real invocations, one per line.
var helpExamples = []struct{ cmd, note string }{
	{"belai", "open the terminal UI in this directory"},
	{`belai -prompt "what does internal/run do?"`, "one answer, then exit"},
	{`belai -provider anthropic -model claude-sonnet-4-5 -prompt "review this diff"`, "pick the model"},
	{"belai -plan", "start in read-only plan mode"},
	{"belai -resume 3f2a", "resume a session by id or prefix"},
	{"belai -teleport 3f2a9c10-...", "continue a session from another host, sandbox or the web"},
	{"belai -export 3f2a", "write a session as Markdown"},
	{"belai agent list", "agent profiles"},
	{`belai kanban add "fix the flaky test"`, "file a card"},
	{"belai skill export release release.md", "write a skill to a file"},
	{"belai rc --detach", "run remote control in the background"},
	{"belai plugin install <git-url|dir>", "install a plugin"},
}

// flagGroup is a titled run of top-level flags, in display order. An alias
// (r for resume) is shown with its long flag and is not listed here.
type flagGroup struct {
	title string
	names []string
}

var flagGroups = []flagGroup{
	{"Session", []string{"prompt", "mode", "plan", "resume", "continue", "teleport", "teleport-ref", "teleport-push", "export", "no-transcript", "usage-json", "detect-mode", "no-git-sync"}},
	{"Model", []string{"provider", "model", "effort", "firewall", "caveman", "defer-tools", "tools", "agent", "agent-create"}},
	{"Safety", []string{
		"guardrails", "ask-permission", "trust-dir", "dangerously-yolo-everything",
		"allow-unsafe-prompt", "allow-malformed-prompt", "allow-unsafe-tool-result", "allow-malformed-tool-result",
		"allow-unpermitted-tools", "allow-ask-without-tty", "allow-invalid-skills", "allow-invalid-hooks",
		"tool-call-mismatch",
	}},
	{"Security classifier", []string{
		"classifier-kind", "classifier-provider", "classifier-model", "classifier-effort",
		"classifier-phase1-model", "classifier-phase1-source", "classifier-phase1-threshold",
		"classifier-phase2-model", "classifier-phase2-source", "classifier-phase2-threshold",
	}},
	{"Other", []string{"verbose", "session-retention-days", "no-prune", "version"}},
}

// flagAliases maps a long flag to its short forms.
var flagAliases = map[string][]string{
	"resume":   {"r"},
	"continue": {"c"},
}

// printHelp writes the top-level help. Flag text comes from the flag
// definitions in main, so a description is written once.
func printHelp(w io.Writer, fs *flag.FlagSet) {
	fmt.Fprint(w, "usage: belai [flags]\n       belai <command> [flags] [args]\n\n")
	fmt.Fprint(w, "With no command, belai opens the terminal UI (or answers one -prompt and exits).\n\n")

	fmt.Fprintln(w, "Commands:")
	for _, c := range topCommands {
		fmt.Fprintf(w, "  %-10s %s\n", c.name, c.summary)
	}
	fmt.Fprintln(w, "\nRun `belai <command> -h` (or `belai help <command>`) for a command's own flags.")

	fmt.Fprintln(w, "\nExamples:")
	for _, e := range helpExamples {
		fmt.Fprintf(w, "  %s\n", e.cmd)
		fmt.Fprintf(w, "      %s\n", e.note)
	}

	for _, g := range flagGroups {
		fmt.Fprintf(w, "\n%s flags:\n", g.title)
		for _, name := range g.names {
			f := fs.Lookup(name)
			if f == nil {
				continue
			}
			printFlag(w, f)
		}
	}
	helpRow(w, "-help, -h", "show this help")
}

// printFlag writes one flag, folding its short forms into the name.
func printFlag(w io.Writer, f *flag.Flag) {
	label := "-" + f.Name
	for _, a := range flagAliases[f.Name] {
		label += ", -" + a
	}
	typ, usage := flag.UnquoteUsage(f)
	if typ != "" {
		label += " " + typ
	}
	switch f.DefValue {
	case "", "false", "0":
	default:
		if !strings.Contains(usage, "default") {
			usage += " (default " + f.DefValue + ")"
		}
	}
	helpRow(w, label, usage)
}

// helpRow writes a label and its description in two columns. A label too wide
// for the first puts the description on the next line.
func helpRow(w io.Writer, label, usage string) {
	const indent = 2
	const descIndent = indent + helpFlagColumn
	lines := wrap(usage, helpWidth-descIndent)
	if len(label) <= helpFlagColumn-2 {
		fmt.Fprintf(w, "%*s%-*s%s\n", indent, "", helpFlagColumn, label, lines[0])
		lines = lines[1:]
	} else {
		fmt.Fprintf(w, "%*s%s\n", indent, "", label)
	}
	for _, l := range lines {
		fmt.Fprintf(w, "%*s%s\n", descIndent, "", l)
	}
}

// wrap breaks s into lines of at most width runes at spaces. A word longer
// than width stands alone.
func wrap(s string, width int) []string {
	var lines []string
	cur := ""
	for _, word := range strings.Fields(s) {
		switch {
		case cur == "":
			cur = word
		case len(cur)+1+len(word) <= width:
			cur += " " + word
		default:
			lines = append(lines, cur)
			cur = word
		}
	}
	if cur != "" || len(lines) == 0 {
		lines = append(lines, cur)
	}
	return lines
}

// parseTopLevel parses the main command's flags. Explicit help goes to stdout
// and exits 0, so `belai -help | less` works; any other error has already been
// reported by the flag package and gets a pointer to the help, with exit 2.
// It returns false when the process should exit with code.
func parseTopLevel(fs *flag.FlagSet, args []string, stdout, stderr io.Writer) (ok bool, code int) {
	fs.SetOutput(stderr)
	fs.Usage = func() {}
	err := fs.Parse(args)
	switch {
	case err == nil:
		return true, 0
	case err == flag.ErrHelp:
		printHelp(stdout, fs)
		return false, 0
	default:
		fmt.Fprintln(stderr, "Run `belai -help` for usage.")
		return false, 2
	}
}

// helpTarget turns `belai help <command>` into the `belai <command> -h` that
// prints that command's own usage. It reports false for a word that names no
// listed command.
func helpTarget(args []string) ([]string, bool) {
	for _, c := range topCommands {
		if c.name == args[0] {
			return []string{c.name, "-h"}, true
		}
	}
	return nil, false
}
