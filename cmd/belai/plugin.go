package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/hooks"
	"github.com/vulnetix/belai/internal/plugins"
	"github.com/vulnetix/belai/internal/promptlib"
	"github.com/vulnetix/belai/internal/tools"
)

// activatePlugins points every component loader at the enabled plugins. It
// runs once, before any session is built.
func activatePlugins() {
	tools.ExtraSkillRoots = plugins.SkillRoots
	hooks.Extra = plugins.Hooks
	agentprofile.ExtraProfiles = plugins.Profiles
	promptlib.ExtraEntries = func() []promptlib.Entry {
		var out []promptlib.Entry
		for _, p := range plugins.Prompts() {
			out = append(out, promptlib.Entry{Name: p.Name, Prompt: p.Prompt, Enabled: true})
		}
		return out
	}
}

const pluginUsage = `usage: belai plugin <command>

  list                          installed plugins
  install [-yes] <git-url|dir>  install after confirming the listing (git-url may end #<ref>)
  update [-yes] <name> [source] re-fetch, show the new listing, confirm, move the pin
  enable <name> | disable <name>
  remove <name>
`

// runPluginCLI implements `belai plugin …` and returns the exit code.
func runPluginCLI(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, isTTY bool) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, pluginUsage)
		return 2
	}
	cmd, rest := args[0], args[1:]
	fs := flag.NewFlagSet("plugin "+cmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	yes := fs.Bool("yes", false, "install without the interactive confirmation (the listing is still printed)")
	if err := fs.Parse(rest); err != nil {
		return 2
	}
	rest = fs.Args()
	confirm := func(s plugins.Summary, source, commit string, prev *plugins.Summary) bool {
		fmt.Fprint(stdout, plugins.Describe(s, source, commit, prev))
		if *yes {
			return true
		}
		if !isTTY {
			fmt.Fprintln(stderr, "belai: no terminal to confirm on; pass -yes after reviewing the listing")
			return false
		}
		fmt.Fprint(stdout, "Install this plugin? [y/N] ")
		line, _ := bufio.NewReader(stdin).ReadString('\n')
		return strings.EqualFold(strings.TrimSpace(line), "y") || strings.EqualFold(strings.TrimSpace(line), "yes")
	}
	fail := func(err error) int {
		if errors.Is(err, plugins.ErrDeclined) {
			fmt.Fprintln(stderr, "belai: not installed")
			return 1
		}
		fmt.Fprintln(stderr, "belai:", err)
		return 1
	}
	switch cmd {
	case "list":
		recs, err := plugins.List()
		if err != nil {
			return fail(err)
		}
		if len(recs) == 0 {
			fmt.Fprintln(stdout, "no plugins installed")
		}
		for _, r := range recs {
			state := "enabled"
			if !r.Enabled {
				state = "disabled"
			}
			fmt.Fprintf(stdout, "%s\t%s\t%s\t%s\t%s\n", r.Name, r.Version, state, shortCommit(r.Commit), r.Source)
		}
		return 0
	case "install":
		if len(rest) != 1 {
			fmt.Fprint(stderr, pluginUsage)
			return 2
		}
		rec, err := plugins.Install(ctx, rest[0], confirm)
		if err != nil {
			return fail(err)
		}
		fmt.Fprintf(stdout, "installed %s at %s\n", rec.Name, shortCommit(rec.Commit))
		return 0
	case "update":
		if len(rest) < 1 || len(rest) > 2 {
			fmt.Fprint(stderr, pluginUsage)
			return 2
		}
		source := ""
		if len(rest) == 2 {
			source = rest[1]
		}
		rec, err := plugins.Update(ctx, rest[0], source, confirm)
		if err != nil {
			return fail(err)
		}
		fmt.Fprintf(stdout, "updated %s to %s\n", rec.Name, shortCommit(rec.Commit))
		return 0
	case "enable", "disable":
		if len(rest) != 1 {
			fmt.Fprint(stderr, pluginUsage)
			return 2
		}
		if err := plugins.SetEnabled(rest[0], cmd == "enable"); err != nil {
			return fail(err)
		}
		fmt.Fprintf(stdout, "%sd %s\n", cmd, rest[0])
		return 0
	case "remove":
		if len(rest) != 1 {
			fmt.Fprint(stderr, pluginUsage)
			return 2
		}
		if err := plugins.Remove(rest[0]); err != nil {
			return fail(err)
		}
		fmt.Fprintf(stdout, "removed %s\n", rest[0])
		return 0
	}
	fmt.Fprint(stderr, pluginUsage)
	return 2
}

func shortCommit(c string) string {
	if len(c) > 12 {
		return c[:12]
	}
	return c
}
