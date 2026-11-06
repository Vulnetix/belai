package tui

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/belai/internal/checkpoint"
	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/permissions"
	"github.com/vulnetix/belai/internal/sanitize"
	"github.com/vulnetix/belai/internal/session"
	"github.com/vulnetix/belai/internal/tools"
)

// File checkpoints in the interactive session (docs/checkpoints.md). The
// tracker is the user's own: only these commands read it, nothing is sent to a
// model, and a restore is always confirmed here.

// rewindMode is what a restore puts back.
type rewindMode int

const (
	rewindCode rewindMode = iota
	rewindConversation
	rewindBoth
)

func (m rewindMode) word() string {
	switch m {
	case rewindConversation:
		return "conversation"
	case rewindBoth:
		return "both"
	}
	return "code"
}

// rewindStage is the step of the /rewind view.
type rewindStage int

const (
	rewindPick rewindStage = iota
	rewindChoose
	rewindConfirm
)

// rewindState is the /undo and /rewind view.
type rewindState struct {
	stage  rewindStage
	infos  []checkpoint.Info // newest first
	cursor int
	scroll int
	mode   rewindMode
	target checkpoint.Info
	plan   checkpoint.Plan
	err    string
	undo   bool
}

// checkpointTracker returns the session's tracker, opening it on first use and
// again when the session id changes. It is nil when checkpoints are off. A store
// that cannot be opened yields a tracker that refuses saves, so a mutating call
// is withheld rather than run unprotected.
func (a *App) checkpointTracker() *checkpoint.Tracker {
	if !a.settings.CheckpointsEnabled() || a.sessionID == "" || a.workdir == "" {
		return nil
	}
	if a.ckpt != nil && a.ckptSession == a.sessionID {
		return a.ckpt
	}
	a.ckptSession = a.sessionID
	dir, err := config.CheckpointsDir(a.workdir, a.sessionID)
	if err == nil {
		a.ckpt, err = checkpoint.Open(dir, a.checkpointLimits(), a.checkpointPolicy())
	}
	if err != nil {
		a.ckpt = checkpoint.Unavailable(err)
		a.addSystem("checkpoints: " + sanitize.Line(err.Error(), 160) + ". Edits are withheld until this is fixed; set checkpoints.enabled to false or start with -no-checkpoints to work without them")
		return a.ckpt
	}
	if !a.ckptSwept {
		a.ckptSwept = true
		if root, err := config.CheckpointsRoot(); err == nil {
			days, keep := a.settings.CheckpointsRetainDays(), a.sessionID
			go checkpoint.Sweep(root, days, time.Now(), keep)
		}
	}
	return a.ckpt
}

func (a *App) checkpointLimits() checkpoint.Limits {
	return checkpoint.Limits{MaxPerSession: a.settings.CheckpointsMaxPerSession(), MaxBytes: a.settings.CheckpointsMaxBytes()}
}

// checkpointPolicy is the policy used before an agent session has set its own:
// the working directory and added directories as roots, and the user's rules.
func (a *App) checkpointPolicy() checkpoint.Policy {
	workdir := a.workdir
	dirs := append([]string(nil), a.workspaceDirs...)
	contains := func(abs string) bool {
		c := tools.NewCwd(workdir)
		for _, d := range dirs {
			_ = c.AddRoot(d)
		}
		return c.Contains(abs)
	}
	p := a.settings.Permissions
	return checkpoint.PolicyFor(workdir, contains, permissions.From(p.Allow, p.Ask, p.Deny))
}

// beginCheckpointTurn marks the start of a user turn for the tracker, and drops
// a cached agent session built without it.
func (a *App) beginCheckpointTurn(prompt string) {
	t := a.checkpointTracker()
	if t != a.agentCkpt {
		if a.agent != nil {
			a.invalidateAgentSession()
		}
		a.agentCkpt = t
	}
	if t == nil {
		return
	}
	t.SetLimits(a.checkpointLimits())
	t.BeginTurn(a.turnUserEntry, prompt)
	a.turnUserEntry = ""
}

// undoCommand is /undo: restore the files the last prompt changed.
func (a *App) undoCommand(arg string) tea.Cmd {
	if strings.TrimSpace(arg) != "" {
		a.addSystem("undo: takes no argument; /rewind chooses a point and what to restore")
		return nil
	}
	t, ok := a.checkpointReady("undo")
	if !ok {
		return nil
	}
	info, found := t.Latest()
	if !found {
		a.addSystem("undo: nothing to undo; a checkpoint is made when a prompt first changes a file")
		return nil
	}
	plan, err := t.Plan(info.ID)
	if err != nil {
		a.addSystem("undo: " + sanitize.Line(err.Error(), 160))
		return nil
	}
	if len(plan.Changes) == 0 {
		a.addSystem("undo: the files already match what they held before that prompt")
		return nil
	}
	a.rewind = rewindState{stage: rewindConfirm, target: info, plan: plan, mode: rewindCode, undo: true}
	return a.push(viewRewind)
}

// rewindCommand is /rewind: pick a checkpoint, then what to restore.
func (a *App) rewindCommand(arg string) tea.Cmd {
	if strings.TrimSpace(arg) != "" {
		a.addSystem("rewind: takes no argument; pick a point in the list")
		return nil
	}
	t, ok := a.checkpointReady("rewind")
	if !ok {
		return nil
	}
	list := t.List()
	if len(list) == 0 {
		a.addSystem("rewind: no checkpoints yet; one is made when a prompt first changes a file")
		return nil
	}
	infos := make([]checkpoint.Info, 0, len(list))
	for i := len(list) - 1; i >= 0; i-- {
		infos = append(infos, list[i])
	}
	a.rewind = rewindState{stage: rewindPick, infos: infos}
	return a.push(viewRewind)
}

// checkpointReady applies the rules both commands share.
func (a *App) checkpointReady(cmd string) (*checkpoint.Tracker, bool) {
	if a.working() {
		a.addSystem(cmd + ": stop the running turn first")
		return nil, false
	}
	if !a.settings.CheckpointsEnabled() {
		a.addSystem(cmd + ": file checkpoints are off (checkpoints.enabled)")
		return nil, false
	}
	t := a.checkpointTracker()
	if t == nil {
		a.addSystem(cmd + ": file checkpoints are not available in this session")
		return nil, false
	}
	return t, true
}

// rewindAvailable reports whether the conversation can return to a checkpoint's
// prompt: the checkpoint names a user entry that has an entry before it.
func (a *App) rewindEntry(info checkpoint.Info) (string, bool) {
	if info.Entry == "" || a.store == nil || a.storeDisabled {
		return "", false
	}
	entries, err := a.treeEntries()
	if err != nil {
		return "", false
	}
	for _, e := range entries {
		if e.ID == info.Entry && e.Type == "user" && e.ParentID != "" {
			return e.ID, true
		}
	}
	return "", false
}

// rewindRel renders a path the way the user knows it: relative to the working
// directory when inside it, cleaned to one line.
func (a *App) rewindRel(p string) string {
	if r, err := filepath.Rel(a.workdir, p); err == nil && !strings.HasPrefix(r, "..") {
		p = r
	}
	return sanitize.Line(p, 100)
}

// rewindApply carries out the confirmed restore.
func (a *App) rewindApply() tea.Cmd {
	st := a.rewind
	if a.working() {
		a.rewind.err = "stop the running turn first"
		return nil
	}
	t := a.checkpointTracker()
	if t == nil {
		a.rewind.err = "file checkpoints are not available"
		return nil
	}
	var entry session.Entry
	if st.mode != rewindCode {
		id, ok := a.rewindEntry(st.target)
		if !ok {
			a.rewind.err = "the conversation cannot return to this point"
			return nil
		}
		entries, err := a.treeEntries()
		if err != nil {
			a.rewind.err = err.Error()
			return nil
		}
		for _, e := range entries {
			if e.ID == id {
				entry = e
			}
		}
	}
	if st.mode != rewindConversation {
		res, err := t.Apply(st.plan)
		if err != nil {
			a.rewind.err = sanitize.Line(err.Error(), 200)
			return nil
		}
		msg := fmt.Sprintf("restored %d, removed %d", res.Restored, res.Deleted)
		if res.Skipped > 0 {
			msg += fmt.Sprintf(", left %d", res.Skipped)
		}
		if res.Safety != "" {
			msg += "; what was overwritten is kept as checkpoint " + res.Safety + " (/rewind)"
		}
		a.addSystem("checkpoint: " + msg)
	}
	a.pop()
	if st.mode == rewindCode {
		return nil
	}
	return a.treeMove(entry, false, false)
}
