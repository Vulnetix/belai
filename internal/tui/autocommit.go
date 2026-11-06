package tui

import (
	"path/filepath"

	"github.com/vulnetix/belai/internal/filediff"
	"github.com/vulnetix/belai/internal/forge"
	"github.com/vulnetix/belai/internal/sessionmeasure"
)

// maxTaskPaths caps the number of distinct paths one goal turn collects for
// auto-commit, so a pathological turn cannot build an unbounded argv.
const maxTaskPaths = 1000

// autoCommitMsg carries the result of a commit decision back to the UI loop:
// a commit, a refusal, a note or a question for the person (commit_flow.go).
type autoCommitMsg struct {
	sha string
	// full is the whole commit id, read off the UI loop for the audit log.
	full string
	msg  string
	err  error
	// commit is the commit as the session records it (docs/session-measures.md);
	// nil when git could not read it back.
	commit *sessionmeasure.Commit

	// mode is "auto", "ask" or "manual".
	mode string
	// split is what the harness decided about every dirty path.
	split forge.Split
	// refused is a harness-worded reason nothing was committed.
	refused string
	// note is a harness-worded outcome that is neither a commit nor a refusal.
	note string
	// draftNote says why a requested model draft was not used.
	draftNote string
	// ask is set when a person must answer before the commit is made.
	ask  *commitPlan
	plan *commitPlan
	head string
	// now is the dirty set the decision read, valid when nowKnown.
	now      []string
	nowKnown bool
}

// observeTaskDiff feeds one tool's observed file changes to the auto-commit
// collector. It is deterministic and render-only: the diff itself never
// reaches a model. Paths are deduplicated and capped at maxTaskPaths.
func (a *App) observeTaskDiff(ch *filediff.Change) {
	if ch == nil || len(ch.Files) == 0 {
		return
	}
	a.observeRelayPaths(ch)
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
// turn (committed, partial, stopped, errored or plain agent) so the next
// commit covers exactly the files written by the goal that just completed.
func (a *App) resetTaskPaths() {
	a.taskPaths = nil
	a.taskPathSet = nil
}
