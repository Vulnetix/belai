package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/belai/internal/plans"
	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/run"
)

// handoffDefaultPrompt is the prompt /handoff sends when the user adds no
// direction of their own.
const handoffDefaultPrompt = "Execute the attached plan."

// splitHandoffArg splits "/handoff" arguments into the plan path and any
// extra direction. The path may be quoted to hold spaces, and a leading "@"
// (as the file chooser spells it) is accepted.
func splitHandoffArg(arg string) (path, rest string) {
	arg = strings.TrimSpace(arg)
	arg = strings.TrimPrefix(arg, "@")
	if strings.HasPrefix(arg, `"`) {
		if end := strings.Index(arg[1:], `"`); end >= 0 {
			return arg[1 : 1+end], strings.TrimSpace(arg[2+end:])
		}
		return strings.Trim(arg, `"`), ""
	}
	path, rest, _ = strings.Cut(arg, " ")
	return path, strings.TrimSpace(rest)
}

// startHandoff is /handoff: the named plan file is attached through the
// composer's own @path admission (root-confined, sanitised, classified) and
// the turn runs as a plan handoff with no intent detection. Prompt admission
// and every permission gate still apply; only the detection and routing
// checks are skipped.
func (a *App) startHandoff(arg string) tea.Cmd {
	path, rest := splitHandoffArg(arg)
	if path == "" {
		a.addSystem("usage: /handoff <plan file path> [extra direction]")
		return nil
	}
	if rest == "" {
		rest = handoffDefaultPrompt
	}
	token := "@" + path
	if strings.ContainsAny(path, " \t") {
		token = `@"` + path + `"`
	}
	if a.mode == "plan" {
		// A handoff edits files; naming it is the user leaving plan mode.
		a.mode = "agent"
		a.modeExplicit = true
		a.modeSticky = true
		a.modeAuto = false
		a.syncPlanMode()
		a.saveMode()
		a.addSystem("plan mode off (handoff)")
	}
	a.pendingHandoff = true
	input := token + " " + rest
	a.editor.SetValue(input)
	cmd := a.syncAttachments()
	if a.hasPendingAttachments() {
		a.pendingInput = input
		return tea.Batch(cmd, a.attachSpin.Tick)
	}
	return tea.Batch(cmd, a.submitInput(input))
}

// handoffFactsFrom returns the handoff facts of the first Markdown file
// attachment, or nil when there is none. It reads harness facts (label, task
// count, referenced paths) only.
func handoffFactsFrom(atts []run.Attachment) *rolemanager.HandoffFacts {
	for _, at := range atts {
		if at.Kind != "file" {
			continue
		}
		if f, ok := plans.HandoffFactsFor(strings.Trim(at.Label, `@"`), at.Body); ok {
			return &rolemanager.HandoffFacts{Label: f.Label, Tasks: f.Tasks, Paths: f.Paths}
		}
	}
	return nil
}
