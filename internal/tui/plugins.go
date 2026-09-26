package tui

import (
	"fmt"
	"strings"

	"github.com/vulnetix/signet/internal/plugins"
)

// pluginCommand runs /plugin. Install and update need the full listing and
// an explicit confirmation, so they stay on the CLI (signet plugin install).
// A change applies to sessions built after it.
func pluginCommand(arg string) string {
	fields := strings.Fields(arg)
	sub := "list"
	if len(fields) > 0 {
		sub = fields[0]
	}
	switch sub {
	case "list":
		recs, err := plugins.List()
		if err != nil {
			return "plugins: " + err.Error()
		}
		if len(recs) == 0 {
			return "no plugins installed · install one with: signet plugin install <git-url|dir>"
		}
		var b strings.Builder
		fmt.Fprintf(&b, "%d plugin(s):", len(recs))
		for _, r := range recs {
			state := "enabled"
			if !r.Enabled {
				state = "disabled"
			}
			commit := r.Commit
			if len(commit) > 12 {
				commit = commit[:12]
			}
			fmt.Fprintf(&b, "\n  %s %s · %s · %s", r.Name, r.Version, state, commit)
		}
		return b.String()
	case "enable", "disable", "remove":
		if len(fields) != 2 {
			return "usage: /plugin " + sub + " <name>"
		}
		var err error
		if sub == "remove" {
			err = plugins.Remove(fields[1])
		} else {
			err = plugins.SetEnabled(fields[1], sub == "enable")
		}
		if err != nil {
			return "plugins: " + err.Error()
		}
		return fmt.Sprintf("plugin %s %sd · applies to the next session (/clear)", fields[1], strings.TrimSuffix(sub, "e"))
	case "install", "update":
		return "install and update need you to review the full listing: run `signet plugin " + sub + " …` in a terminal"
	}
	return "usage: /plugin [list | enable <name> | disable <name> | remove <name>]"
}
