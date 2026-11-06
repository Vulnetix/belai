package tui

import (
	"os"
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/belai/internal/prompt"
	"github.com/vulnetix/belai/internal/repomap"
	"github.com/vulnetix/belai/internal/sanitize"
)

// initPrompt is the fixed request /init sends, ahead of the harness facts. The
// model drafts prose; the file reaches disk only through the Write tool, so
// the ask gate, permission rules, hooks and sandbox apply as for any edit.
const initPrompt = `Draft an AGENTS.md for the root of this repository, for coding agents that will work in it.

Use Read, Grep and Glob to confirm what you state; do not guess and do not invent commands, paths or conventions. Keep it under about 60 lines. Cover only what an agent cannot learn quickly from the code: how to build, test, lint and format (use the detected commands below when they are right), the layout and where things live, conventions and rules you can verify in the repository, and anything that must not be changed. Do not copy secrets, tokens or personal data.

When you have the draft, write it with the Write tool to AGENTS.md at the repository root. The user approves that write.

Harness-detected facts (paths and counts, not repository prose):
`

// initCommand is /init. It refuses, with no turn, when the repository root already
// has an AGENTS.md or CLAUDE.md, so a file the user wrote is never a target for
// a rewrite; otherwise it sends one ordinary agent turn whose only content is
// a fixed request and the repository map's facts (sanitised). Plan mode cannot
// write, so it is refused there rather than leaving the mode.
func (a *App) initCommand() tea.Cmd {
	if a.working() || a.preSend {
		a.addSystem("/init: a turn is running; run it again when it ends")
		return nil
	}
	if a.mode == "plan" {
		a.addSystem("/init writes a file, which plan mode cannot do; switch with /mode agent first")
		return nil
	}
	if name := existingInstructionFile(a.initRoot()); name != "" {
		a.addSystem("/init: " + name + " already exists, so nothing was sent; edit it, or ask for specific changes in a prompt")
		return nil
	}
	facts := sanitize.Sanitize(prompt.RepoMapBlock(a.repoMap))
	if facts == "" {
		facts = "(no repository facts were detected; explore with Glob and Read)"
	}
	return a.submitSlashPrompt("/init", initPrompt+facts)
}

// existingInstructionFile returns the name of the first instruction file at
// the repository root, or "". A symbolic link counts: Lstat, so a dangling link
// is not overwritten through either.
func existingInstructionFile(dir string) string {
	for _, name := range repomap.InstructionFileNames {
		if _, err := os.Lstat(filepath.Join(dir, name)); err == nil {
			return name
		}
	}
	return ""
}

// initRoot is the directory /init checks and the file lands in: the
// repository root the map was scanned from, else the working directory.
func (a *App) initRoot() string {
	if a.repoMap.Module != "" {
		return a.repoMap.Module
	}
	return a.workdir
}
