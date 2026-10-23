package a2a

import (
	"github.com/vulnetix/belai/internal/audit"
	"github.com/vulnetix/belai/internal/crewlog"
)

// Local audit outcomes.
const (
	OutcomeLocal    = "local"    // sent on this host's bus only
	OutcomeRelayed  = "relayed"  // also handed to the relay
	OutcomeMatched  = "matched"  // received and shown to this session
	OutcomeFiltered = "filtered" // received and dropped by the filter
	OutcomeWithheld = "withheld" // held back: blackout, or the classifier
	OutcomeOn       = "on"
	OutcomeOff      = "off"
)

// Audit is the local A2A audit: a crewlog-style hash-chained audit.jsonl in
// the A2A directory, written with or without a login and during a blackout,
// because internal/audit's recorder exists only while sync is on. It holds
// facts only (the kinds and data keys of internal/audit), never text parts.
// Each fact also goes to internal/audit, which uploads it when sync is on.
type Audit struct {
	log *crewlog.Log
}

// OpenAudit opens the audit in dir.
func OpenAudit(dir string) (*Audit, error) {
	l, err := crewlog.Open(dir)
	if err != nil {
		return nil, err
	}
	return &Audit{log: l}, nil
}

// Record writes one fact about env. kind is one of audit.A2A*; outcome one of
// the Outcome* words. session is this host's session the fact is about.
// Failures are ignored: the audit never fails what it records.
func (a *Audit) Record(kind audit.Kind, outcome, session string, env Envelope, upload bool) {
	data := map[string]string{"message_id": env.MessageID, "kind": env.Kind}
	if env.Decision != "" {
		data["decision"] = env.Decision
	}
	peer := env.Agent.ID
	if env.Agent.SessionID == session && env.To != "" {
		peer = env.To
	}
	if peer != "" {
		data["peer"] = peer
	}
	if a != nil && a.log != nil {
		_ = a.log.AppendEntry(crewlog.Entry{Kind: string(kind), ActorKind: audit.ActorHarness, Session: session,
			Repo: env.Work.Repo, Outcome: outcome, Profile: env.Agent.Profile, Data: data})
	}
	if upload {
		audit.Emit(audit.Fact{Kind: kind, ActorKind: audit.ActorHarness, SessionID: session, Repo: env.Work.Repo,
			Outcome: outcome, Data: data})
	}
}

// RecordBlackout writes the A2A blackout switching on or off.
func (a *Audit) RecordBlackout(on bool, session string) {
	outcome := OutcomeOff
	if on {
		outcome = OutcomeOn
	}
	if a != nil && a.log != nil {
		_ = a.log.AppendEntry(crewlog.Entry{Kind: string(audit.A2ABlackout), ActorKind: audit.ActorHarness, Session: session, Outcome: outcome})
	}
	audit.Emit(audit.Fact{Kind: audit.A2ABlackout, ActorKind: audit.ActorHarness, SessionID: session, Outcome: outcome})
}

// Dir is the directory the audit writes.
func (a *Audit) Dir() string {
	if a == nil || a.log == nil {
		return ""
	}
	return a.log.Dir()
}
