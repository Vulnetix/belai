// Package commandlib loads the custom slash commands a person or a library
// installs: one Markdown file per command, <name>.md, whose body is a prompt
// template that /name expands into an ordinary model turn.
//
// There are two layers. The global layer is ~/.vulnetix/belai/commands, where a
// library install writes the canonical document byte for byte
// (internal/libstore). The project layer is <project>/.vulnetix/belai/commands,
// read and never written. A project command with the name of a global one wins.
// Built-in slash commands win over both; the TUI resolves them first.
//
// Every file is validated whole by internal/libitem on load, so a document the
// library would refuse is skipped here too and never reaches a model, and it
// passes the same untrusted-text gate an install does (delimiter markup, control,
// escape, bidirectional and invisible characters). A file that is a symbolic
// link, is not a regular file or is over MaxFileBytes is skipped.
//
// A command's allowed-tools are parsed and kept in the document but never
// applied: a command cannot grant a tool, and this package does not narrow one.
package commandlib

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/libitem"
	"github.com/vulnetix/belai/internal/libstore"
)

// Limits on what a layer holds and what an invocation carries.
const (
	// MaxFileBytes bounds one command file read from disk: the library's limit
	// with room for the whitespace canonicalisation removes.
	MaxFileBytes = 64 << 10
	// MaxPerLayer bounds the commands read from one directory.
	MaxPerLayer = libitem.MaxItemsPerKind
	// MaxArgsBytes bounds the arguments of one invocation.
	MaxArgsBytes = 4 << 10
)

// Command is one loaded custom slash command.
type Command struct {
	Name         string
	Description  string
	ArgumentHint string
	// AllowedTools is parsed and carried; it is never applied (it cannot grant).
	AllowedTools []string
	// Body is the template after the front matter.
	Body  string
	Scope config.Scope
	// Path is the file the command came from.
	Path string
}

// Skipped is a file in a command directory that was not loaded, and why.
type Skipped struct {
	Name   string
	Scope  config.Scope
	Reason string
}

// Set is the merged commands of both layers, sorted by name, and the files
// that were left out.
type Set struct {
	Commands []Command
	Skipped  []Skipped
}

// Find returns the command of that name.
func (s Set) Find(name string) (Command, bool) {
	for _, c := range s.Commands {
		if c.Name == name {
			return c, true
		}
	}
	return Command{}, false
}

// Names lists the command names, sorted.
func (s Set) Names() []string {
	out := make([]string, len(s.Commands))
	for i, c := range s.Commands {
		out[i] = c.Name
	}
	return out
}

// Load reads both layers and merges them: a project command replaces a global
// one of the same name. An empty workdir reads the global layer alone. A
// directory that does not exist holds nothing.
func Load(workdir string) Set {
	var global, project []Command
	var skipped []Skipped
	if dir, err := config.GlobalCommandsDir(); err == nil {
		c, s := LoadDir(dir, config.ScopeGlobal)
		global, skipped = c, append(skipped, s...)
	}
	if workdir != "" {
		c, s := LoadDir(config.ProjectCommandsDir(workdir), config.ScopeProject)
		project, skipped = c, append(skipped, s...)
	}
	return Set{Commands: Merge(global, project), Skipped: skipped}
}

// LoadGlobal reads the global layer alone.
func LoadGlobal() Set { return Load("") }

// Merge overlays project commands on global ones by name; the result is sorted.
func Merge(global, project []Command) []Command {
	byName := map[string]Command{}
	for _, c := range global {
		byName[c.Name] = c
	}
	for _, c := range project {
		byName[c.Name] = c
	}
	out := make([]Command, 0, len(byName))
	for _, c := range byName {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// LoadDir reads one directory of <name>.md files. A missing directory, or one
// that is a symbolic link, holds nothing. Files that are not .md are ignored;
// an .md file that cannot be used is returned as skipped.
func LoadDir(dir string, scope config.Scope) ([]Command, []Skipped) {
	fi, err := os.Lstat(dir)
	if err != nil {
		return nil, nil
	}
	if !fi.IsDir() {
		return nil, []Skipped{{Name: filepath.Base(dir), Scope: scope, Reason: "the commands path is not a directory (a symbolic link is not followed)"}}
	}
	des, err := os.ReadDir(dir)
	if err != nil {
		return nil, []Skipped{{Name: filepath.Base(dir), Scope: scope, Reason: err.Error()}}
	}
	var out []Command
	var skipped []Skipped
	for _, de := range des {
		stem, ok := strings.CutSuffix(de.Name(), ".md")
		if !ok || strings.HasPrefix(de.Name(), ".") {
			continue
		}
		skip := func(why string) { skipped = append(skipped, Skipped{Name: stem, Scope: scope, Reason: why}) }
		if len(out) >= MaxPerLayer {
			skip(fmt.Sprintf("more than %d commands in one directory", MaxPerLayer))
			continue
		}
		path := filepath.Join(dir, de.Name())
		c, why := loadFile(path, stem, scope)
		if why != "" {
			skip(why)
			continue
		}
		out = append(out, c)
	}
	return out, skipped
}

func loadFile(path, stem string, scope config.Scope) (Command, string) {
	fi, err := os.Lstat(path)
	switch {
	case err != nil:
		return Command{}, err.Error()
	case !fi.Mode().IsRegular():
		return Command{}, "it is not a regular file"
	case fi.Size() > MaxFileBytes:
		return Command{}, fmt.Sprintf("the file is larger than %d bytes", MaxFileBytes)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Command{}, err.Error()
	}
	it, err := libitem.Validate(libitem.Command, data)
	if err != nil {
		return Command{}, err.Error()
	}
	if it.Name != stem {
		return Command{}, fmt.Sprintf("the file is named %s.md but the command is named %s", stem, it.Name)
	}
	if err := libstore.UntrustedText(it.Doc); err != nil {
		return Command{}, err.Error()
	}
	d, err := libitem.ParseCommand(it.Doc)
	if err != nil {
		return Command{}, err.Error()
	}
	return Command{
		Name: d.Name, Description: d.Description, ArgumentHint: d.ArgumentHint,
		AllowedTools: d.AllowedTools, Body: d.Body, Scope: scope, Path: path,
	}, ""
}

// ParseLine splits "/name args" (the slash is optional) into the command name
// and the raw argument text.
func ParseLine(line string) (name, args string) {
	line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "/"))
	name, args, _ = strings.Cut(line, " ")
	return strings.TrimSpace(name), strings.TrimSpace(args)
}

var placeholder = regexp.MustCompile(`\$(ARGUMENTS|[0-9]+)`)

// Expand turns a command and the raw argument text into the prompt it sends.
// $ARGUMENTS is replaced by the argument text as typed and $1, $2, ... by the
// whitespace-separated arguments (a double- or single-quoted run is one
// argument; a missing one is empty). Substitution is one pass, so an argument
// that itself reads "$1" is not expanded again. When the template uses no
// placeholder and there are arguments, they follow the template on a line of
// their own after "ARGUMENTS:".
func Expand(c Command, rawArgs string) (string, error) {
	rawArgs = strings.TrimSpace(rawArgs)
	if len(rawArgs) > MaxArgsBytes {
		return "", fmt.Errorf("the arguments are %d bytes; the most is %d", len(rawArgs), MaxArgsBytes)
	}
	pos := splitArgs(rawArgs)
	used := false
	out := placeholder.ReplaceAllStringFunc(c.Body, func(m string) string {
		used = true
		if m == "$ARGUMENTS" {
			return rawArgs
		}
		n, err := strconv.Atoi(m[1:])
		if err != nil || n < 1 || n > len(pos) {
			return ""
		}
		return pos[n-1]
	})
	out = strings.TrimSpace(out)
	if !used && rawArgs != "" {
		out += "\n\nARGUMENTS: " + rawArgs
	}
	return out, nil
}

// splitArgs splits on whitespace; a single- or double-quoted run is one
// argument with its quotes removed. There are no escapes.
func splitArgs(s string) []string {
	var out []string
	var cur strings.Builder
	var quote rune
	started := false
	flush := func() {
		if started {
			out = append(out, cur.String())
			cur.Reset()
			started = false
		}
	}
	for _, r := range s {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '"' || r == '\'':
			quote, started = r, true
		case r == ' ' || r == '\t' || r == '\n':
			flush()
		default:
			cur.WriteRune(r)
			started = true
		}
	}
	flush()
	return out
}
