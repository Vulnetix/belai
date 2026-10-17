package fleet

import (
	"context"
	"net/url"
	"strconv"

	"github.com/vulnetix/belai/internal/audit"
	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/crewlog"
	"github.com/vulnetix/belai/internal/kanban"
	"github.com/vulnetix/belai/internal/notify"
	"github.com/vulnetix/belai/internal/otel"
)

// The fleet's audit facts (internal/audit, docs/audit.md). Every value is a
// harness fact: the worker's profile, crew and id, the card's id and finding,
// the branch and commit ids the harness read from git, an enum verdict. A
// model's words and a run's output never reach an event, and audit.Emit does
// nothing when sync is off.

// auditItem records a fact about a card this worker holds.
func (w *Worker) auditItem(kind audit.Kind, it kanban.Item, f audit.Fact) {
	if !audit.Enabled() && w.Record.Run == "" {
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
	w.emitFact(f)
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
	if !audit.Enabled() && w.Record.Run == "" {
		return
	}
	f := audit.Fact{Kind: kind, ActorKind: audit.ActorAgent, Actor: w.Profile.Name, Crew: w.Record.Crew,
		Repo: w.Record.Project, Outcome: state}
	f.Data = withData(nil, "worker", w.Record.ID)
	if state != "" {
		f.Data["state"] = state
	}
	w.emitFact(f)
}

// auditCommit records the tip of the item's work: the branch head after the
// harness committed what the model left, measured from the base the branch
// started at. It says nothing when the branch changed no file.
func (w *Worker) auditCommit(ctx context.Context, it kanban.Item, ws *Workspace, files int, outcome string) {
	if (!audit.Enabled() && w.Record.Run == "") || ws == nil || !ws.Worktree || files <= 0 {
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
	if !audit.Enabled() && w.Record.Run == "" {
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

// emitFact sends a fact to the audit (when sync records one) and, for a
// worker of a crew run, to the run's crew log, which is kept on this machine
// whether or not sync is on.
func (w *Worker) emitFact(f audit.Fact) {
	audit.Emit(f)
	if w.Record.Run != "" {
		appendEntry(w.Record.Run, f, w.Record.ID, w.Profile.Name)
	}
}

// appendEntry mirrors a fact into a run's crew log.
func appendEntry(run string, f audit.Fact, worker, profile string) {
	l := openCrewLog(run)
	if l == nil {
		return
	}
	defer l.Close()
	_ = l.AppendEntry(crewlog.Entry{Kind: string(f.Kind), Actor: f.Actor, ActorKind: f.ActorKind, Worker: worker, Profile: profile,
		Item: f.ItemID, Session: f.SessionID, Repo: f.Repo, Outcome: f.Outcome, Data: f.Data})
}

// openCrewLog opens a run's crew log, nil when it cannot.
func openCrewLog(run string) *crewlog.Log {
	gd, err := config.GlobalDir()
	if err != nil {
		return nil
	}
	l, err := crewlog.Open(crewlog.Dir(gd, run))
	if err != nil {
		return nil
	}
	return l
}

// auditPoolSwitch records a worker moving to another target of its pool
// (worker.fallback), or, with a zero target, finding the whole pool spent.
func (w *Worker) auditPoolSwitch(to Target, streak int) {
	if !audit.Enabled() && w.Record.Run == "" {
		return
	}
	outcome, data := "switch", map[string]string{"worker": w.Record.ID, "retries": strconv.Itoa(streak)}
	if to.Zero() {
		outcome = "pool_exhausted"
	} else {
		data["target"] = to.String()
	}
	w.emitFact(audit.Fact{Kind: audit.WorkerFallback, ActorKind: audit.ActorHarness, Actor: w.Profile.Name, Crew: w.Record.Crew,
		Repo: w.Record.Project, Outcome: outcome, Data: data})
}

// recordFault records a failed item: a worker.fault audit fact carrying only
// its code, the fatal-fault counter, the crew log's fault line with the
// harness's note as its one detail, and a notification for a fault that was
// not the provider's.
func (w *Worker) recordFault(it kanban.Item, o outcome) {
	code := o.fault.Code
	if code == "" {
		code = FaultIncomplete
		if o.blocked {
			code = FaultError
		}
	}
	state := "failed"
	switch {
	case o.transient:
		state = "transient"
	case o.blocked:
		state = "blocked"
	}
	data := map[string]string{"class": code}
	if o.fault.Status != 0 {
		data["status"] = strconv.Itoa(o.fault.Status)
	}
	w.auditItem(audit.WorkerFault, it, audit.Fact{SessionID: w.Record.Session, Outcome: state, Data: data})
	t := w.currentTarget()
	otel.Add(otel.MetricWorkerFatal, 1, otel.S(otel.AttrAgentProfile, w.Profile.Name), otel.S(otel.AttrProvider, t.Provider),
		otel.S(otel.AttrModel, t.Model), otel.S(otel.AttrErrorClass, code))
	if w.Record.Run != "" {
		if l := openCrewLog(w.Record.Run); l != nil {
			_ = l.AppendFault(crewlog.Fault{Run: w.Record.Run, Crew: w.Record.Crew, Worker: w.Record.ID, Profile: w.Profile.Name,
				Item: it.Short(), Session: w.Record.Session, Repo: w.Record.Project, Class: code, Provider: t.Provider, Model: t.Model,
				Status: o.fault.Status, Retries: w.outages, Outcome: state, Detail: o.note})
			l.Close()
		}
		if !o.transient {
			w.notify(notify.EventCrewFault)
		}
	}
}

// recordWorkerFault records a worker that stopped on an error of its own.
func (w *Worker) recordWorkerFault(err error) {
	if !audit.Enabled() && w.Record.Run == "" {
		return
	}
	code := classifyErr(err).Code
	if code == FaultError {
		code = FaultWorker
	}
	w.emitFact(audit.Fact{Kind: audit.WorkerFault, ActorKind: audit.ActorAgent, Actor: w.Profile.Name, Crew: w.Record.Crew,
		Repo: w.Record.Project, Outcome: "failed", Data: map[string]string{"worker": w.Record.ID, "class": code}})
	if w.Record.Run != "" {
		if l := openCrewLog(w.Record.Run); l != nil {
			_ = l.AppendFault(crewlog.Fault{Run: w.Record.Run, Crew: w.Record.Crew, Worker: w.Record.ID, Profile: w.Profile.Name,
				Repo: w.Record.Project, Class: code, Outcome: "failed", Detail: err.Error()})
			l.Close()
		}
	}
}

// recordPool records a change of the crew's pool phase, made by the
// supervisor (crew.control, harness): pool_sleep, final_retry, pool_recovered
// or pool_exhausted.
func recordPool(run CrewRun, action string) {
	f := audit.Fact{Kind: audit.CrewControl, ActorKind: audit.ActorHarness, Crew: run.Crew, Outcome: action,
		Data: map[string]string{"retries": strconv.Itoa(run.PoolEpoch)}}
	audit.Emit(f)
	appendEntry(run.ID, f, "", "")
}

// RecordControl records a change made to a running crew (crew.control): in
// the audit when sync records one, and in the run's crew log. action is a
// fixed word (pause, drain, model, scope, move, priority, …); data holds
// identifiers only.
func RecordControl(run, crew, action, itemID string, data map[string]string) {
	f := audit.Fact{Kind: audit.CrewControl, ActorKind: audit.ActorHuman, Crew: crew, ItemID: itemID, Outcome: action, Data: data}
	audit.Emit(f)
	if run != "" {
		appendEntry(run, f, data["worker"], "")
	}
}
