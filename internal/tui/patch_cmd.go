package tui

import (
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/belai/internal/config"
)

// patchToleranceCmd is /patch-tolerance: with no argument it names the level
// apply_patch anchors hunks at, with strict, whitespace or unicode it sets that
// level for this session. The choice is the user's own, typed here, so it may
// be looser than a project file allowed; it is held in memory, re-applied over
// every settings reload and never written to a settings file
// (docs/patch-tool.md).
func (a *App) patchToleranceCmd(arg string) tea.Cmd {
	arg = strings.ToLower(strings.TrimSpace(arg))
	if arg == "" {
		a.addSystem("patch tolerance: " + a.settings.PatchToleranceValue() + " (strict, whitespace or unicode; /patch-tolerance <level> sets it for this session)")
		return nil
	}
	if !slices.Contains(config.PatchTolerances, arg) {
		a.addSystem("patch tolerance: " + arg + " is not a level; use strict, whitespace or unicode")
		return nil
	}
	a.patchToleranceOverride = arg
	a.settings.PatchTolerance = arg
	a.invalidateAgentSession()
	msg := "patch tolerance: " + arg + " for this session"
	if !a.settings.PatchToolEnabled() {
		msg += " (apply_patch is switched off by patch_tool)"
	}
	a.addSystem(msg)
	return nil
}
