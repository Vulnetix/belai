package tui

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/signet/internal/filediff"
	"github.com/vulnetix/signet/internal/forge"
	"github.com/vulnetix/signet/internal/rolemanager"
	"github.com/vulnetix/signet/internal/run"
)

// maxTaskPaths caps the number of distinct paths one goal turn collects for
// auto-commit, so a pathological turn cannot build an unbounded argv.
const maxTaskPaths = 1000

// autoCommitMsg carries the result of an auto-commit back to the UI loop.
type autoCommitMsg struct {
	sha string
	msg string
	err error
}

// observeTaskDiff feeds one tool's observed file changes to the auto-commit
// collector. It is deterministic and render-only: the diff itself never
// reaches a model. Paths are deduplicated and capped at maxTaskPaths.
func (a *App) observeTaskDiff(ch *filediff.Change) {
	if ch == nil || len(ch.Files) == 0 {
		return
	}
	if a.taskPathSet == nil {
		a.taskPathSet = map[string]bool{}
	}
	for _, f := range ch.Files {
		p := filepath.ToSlash(f.Path)
		if p == "" || a.taskPathSet[p] {
			continue
		}
		if len(a.taskPaths) >= maxTaskPaths {
			return
		}
		a.taskPathSet[p] = true
		a.taskPaths = append(a.taskPaths, p)
	}
}

// resetTaskPaths drops the collected path set. It runs at the end of every
// turn — committed, partial, stopped, errored or plain agent — so the next
// commit covers exactly the files written by the goal that just completed.
func (a *App) resetTaskPaths() {
	a.taskPaths = nil
	a.taskPathSet = nil
}

// flushAutoCommit starts one auto-commit when the finished turn completed a
// goal with at least one changed file. It snapshots every input on the UI loop
// and runs the commit off the UI loop as a tea.Cmd. The path set is reset
// before the commit is returned, so a commit failure still cannot leak one
// goal's files into the next.
func (a *App) flushAutoCommit(res run.Result) tea.Cmd {
	paths := append([]string(nil), a.taskPaths...)
	a.resetTaskPaths()

	if !a.settings.AutoCommitPerTaskEnabled() {
		return nil
	}
	if res.GoalSentinel != rolemanager.GoalComplete || len(paths) == 0 {
		return nil
	}
	if a.lastGoal == nil || strings.TrimSpace(a.lastGoal.Objective) == "" {
		return nil
	}

	workdir := a.workdir
	objective := a.lastGoal.Objective
	runner := a.forgeRun()
	msg := forge.TaskCommitMessage(objective, paths)

	return func() tea.Msg {
		sha, err := forge.CommitPaths(context.Background(), runner, workdir, paths, msg)
		return autoCommitMsg{sha: sha, msg: msg, err: err}
	}
}

// handleAutoCommit reports the commit (or the failure) and refreshes the git
// tab so its HEAD subject reflects the new commit.
func (a *App) handleAutoCommit(m autoCommitMsg) tea.Cmd {
	switch {
	case m.err != nil:
		a.addSystem("auto-commit: " + forge.CleanErr(m.err))
	case m.sha != "":
		a.addSystem(fmt.Sprintf("auto-commit %s %s", m.sha, m.msg))
	}
	return a.refreshForge(true)
}
