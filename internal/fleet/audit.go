package fleet

import (
	"context"
	"net/url"
	"strconv"

	"github.com/vulnetix/belai/internal/audit"
	"github.com/vulnetix/belai/internal/kanban"
)

// The fleet's audit facts (internal/audit, docs/audit.md). Every value is a
// harness fact: the worker's profile, crew and id, the card's id and finding,
// the branch and commit ids the harness read from git, an enum verdict. A
// model's words and a run's output never reach an event, and audit.Emit does
// nothing when sync is off.

// auditItem records a fact about a card this worker holds.
func (w *Worker) auditItem(kind audit.Kind, it kanban.Item, f audit.Fact) {
	if !audit.Enabled() {
		return
	}
	f.Kind, f.ActorKind = kind, audit.ActorAgent
	f.Actor, f.Crew = w.Profile.Name, w.Record.Crew
	// SessionID is the caller's: Record.Session names the item being worked,
	// which is not yet the case when a card is claimed.
	f.ItemID, f.Repo = it.ID, w.Record.Project
	// Only a security card links to a vulnerability: a quality or gate card
	// also carries a finding id, but it is not vulnerability work.
	if f.VulnID == "" {
		f.VulnID = it.VulnID()
	}
	if f.VulnID != "" && f.SeenRef == "" {
		f.SeenRef = it.SeenRef
	}
	f.Data = withData(f.Data, "worker", w.Record.ID)
	audit.Emit(f)
}

// auditClaimed records that this worker claimed the card. The card's list is
// where it came from and returns to; attempts is how many claims ended badly.
func (w *Worker) auditClaimed(it kanban.Item) {
	w.auditItem(audit.CardClaimed, it, audit.Fact{Outcome: "claimed",
		Data: map[string]string{"list": string(it.ClaimFrom), "attempt": strconv.Itoa(it.Attempts)}})
}

// auditWorker records a fact about the worker itself. state is the word the
// worker's record holds (paused, running, stopped, failed): never a reason.
func (w *Worker) auditWorker(kind audit.Kind, state string) {
	if !audit.Enabled() {
		return
	}
	f := audit.Fact{Kind: kind, ActorKind: audit.ActorAgent, Actor: w.Profile.Name, Crew: w.Record.Crew,
		Repo: w.Record.Project, Outcome: state}
	f.Data = withData(nil, "worker", w.Record.ID)
	if state != "" {
		f.Data["state"] = state
	}
	audit.Emit(f)
}

// auditCommit records the tip of the item's work: the branch head after the
// harness committed what the model left, measured from the base the branch
// started at. It says nothing when the branch changed no file.
func (w *Worker) auditCommit(ctx context.Context, it kanban.Item, ws *Workspace, files int, outcome string) {
	if !audit.Enabled() || ws == nil || !ws.Worktree || files <= 0 {
		return
	}
	head, err := ws.Head(ctx)
	if err != nil {
		return
	}
	w.auditItem(audit.RepoCommit, it, audit.Fact{
		SessionID: w.Record.Session, Branch: ws.Branch, Commit: head, BaseCommit: ws.Base, Outcome: outcome,
		Data: map[string]string{"files": strconv.Itoa(files)},
	})
}

// auditPublish records a draft pull request the harness opened for the item's
// branch. The address is kept only as an https URL; anything else is dropped.
func (w *Worker) auditPublish(it kanban.Item, ws *Workspace, pr string) {
	if !audit.Enabled() {
		return
	}
	data := map[string]string{}
	if u := httpsURL(pr); u != "" {
		data["pr"] = u
		if p, err := url.Parse(u); err == nil {
			data["forge"] = p.Hostname()
		}
	}
	w.auditItem(audit.RepoPublish, it, audit.Fact{SessionID: w.Record.Session, Branch: ws.Branch, Outcome: "draft", Data: data})
}

// httpsURL returns s when it is a plain https URL (no credentials, query or
// fragment), else "".
func httpsURL(s string) string {
	u, err := url.Parse(s)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return ""
	}
	return u.String()
}

func withData(m map[string]string, k, v string) map[string]string {
	if m == nil {
		m = map[string]string{}
	}
	m[k] = v
	return m
}
