// Package audit is Belai's harness-stamped audit log: facts about what this
// host and its agents did, never what anyone said.
//
// A Fact names an event kind and a handful of identifiers (a worker profile, a
// card id, a repository key, a branch, a commit, an advisory id, an enum
// verdict). Nothing else can be recorded: every string is reduced to a fixed
// charset and capped, a hash is accepted only as lowercase hex, an advisory id
// only in its identifier shape, and the free-form Data map only under keys from
// a closed allowlist. A prompt, a reply, a command, tool output, a file's
// contents or a commit message has nowhere to go. TestEventKeysClosed and
// TestDataKeysAllowlist pin that surface.
//
// The log is append-only and tamper evident. Each process run (a stream)
// appends to its own file, so there is no lock between the TUI, a worker and the
// rc daemon. Every event carries its line index (Seq) and a SHA-256 chain: Hash
// covers the event and names the previous event's Hash, so editing, dropping or
// reordering a line breaks every hash after it. internal/sessionsync uploads the
// file by line index, like a session transcript, and the server recomputes the
// chain. Canonical is the exact text the hash covers and is pinned to the
// server's by a shared golden vector.
//
// The log exists only while sync is on: with no Recorder installed, Emit does
// nothing. It is best effort and never fails or slows the work it records.
package audit

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Kind names one recorded fact.
type Kind string

// The kinds. A host.* kind is host scope; every other kind is agent scope.
const (
	HostFirstSeen     Kind = "host.first_seen"
	HostVersionChange Kind = "host.version_changed"
	HostRCOnline      Kind = "host.rc_online"
	HostRCOffline     Kind = "host.rc_offline"
	HostDispatch      Kind = "host.dispatch"
	HostSchedule      Kind = "host.schedule"
	HostTeleport      Kind = "host.teleport"

	WorkerStarted Kind = "worker.started"
	WorkerState   Kind = "worker.state"
	WorkerStopped Kind = "worker.stopped"

	CardClaimed    Kind = "card.claimed"
	CardReleased   Kind = "card.released"
	CardLeaseLapse Kind = "card.lease_lapsed"

	RepoCommit  Kind = "repo.commit"
	RepoPublish Kind = "repo.publish"

	GateVerified Kind = "gate.verified"
	VEXWritten   Kind = "vex.written"

	FindingCarded     Kind = "finding.carded"
	FindingReconciled Kind = "finding.reconciled"
)

// Scope is where an event shows on the website: a host's audit or an agent's.
const (
	ScopeHost  = "host"
	ScopeAgent = "agent"
)

var kindScope = map[Kind]string{
	HostFirstSeen: ScopeHost, HostVersionChange: ScopeHost, HostRCOnline: ScopeHost,
	HostRCOffline: ScopeHost, HostDispatch: ScopeHost, HostSchedule: ScopeHost, HostTeleport: ScopeHost,
	WorkerStarted: ScopeAgent, WorkerState: ScopeAgent, WorkerStopped: ScopeAgent,
	CardClaimed: ScopeAgent, CardReleased: ScopeAgent, CardLeaseLapse: ScopeAgent,
	RepoCommit: ScopeAgent, RepoPublish: ScopeAgent,
	GateVerified: ScopeAgent, VEXWritten: ScopeAgent,
	FindingCarded: ScopeAgent, FindingReconciled: ScopeAgent,
}

// Kinds lists every kind, sorted, for tests and docs.
func Kinds() []Kind {
	out := make([]Kind, 0, len(kindScope))
	for k := range kindScope {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Valid reports whether k is a known kind.
func (k Kind) Valid() bool { _, ok := kindScope[k]; return ok }

// Scope returns the kind's scope, "" for an unknown kind.
func (k Kind) Scope() string { return kindScope[k] }

// Who did it. The website draws a harness actor as the system, an agent as a
// fleet worker, a human as a person at a keyboard.
const (
	ActorAgent   = "agent"
	ActorHuman   = "human"
	ActorWeb     = "web"
	ActorHarness = "harness"
)

var actorKinds = map[string]bool{ActorAgent: true, ActorHuman: true, ActorWeb: true, ActorHarness: true}

// Verdicts are kanban.Verdict's values; the log keeps only the enum.
var verdicts = map[string]bool{"fixed": true, "false_positive": true, "no_fix": true, "needs_human": true, "rejected": true}

// Caps on what one event may carry.
const (
	maxValue = 256
	maxData  = 16
	maxLine  = 1 << 20
)

// dataKeys is the closed set of keys the free-form Data map may use. Adding a
// key here is adding a thing that can leave the machine: it must name an
// identifier, a count, an enum or a hash, never text, and it needs a line in
// docs/audit.md.
var dataKeys = map[string]bool{
	"version": true, "previous": true, "os": true, // host
	"dispatch": true, "status": true, "schedule": true, // host.dispatch: kind and ack status; host.schedule: the id
	"state": true, "reason": true, "worker": true, "profile": true, // worker
	"list": true, "from": true, "to": true, "attempt": true, "hops": true, // card
	"files": true, "pr": true, "forge": true, // repo
	"gate": true, "suite": true, "exit": true, "regressed": true, "commit": true, // gate
	"justification": true, "path": true, // vex
	"rule": true, "change": true, "severity": true, "ecosystem": true, "package": true, // finding
	"passes": true,
}

// DataKeys lists the allowed Data keys, sorted.
func DataKeys() []string {
	out := make([]string, 0, len(dataKeys))
	for k := range dataKeys {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Fact is what an emitter supplies. Seq, time, hostname, chain and scope are the
// Recorder's; an emitter cannot set them.
type Fact struct {
	Kind      Kind
	ActorKind string
	// Actor is a worker profile name or a worker id, never a person's name.
	Actor      string
	Crew       string
	SessionID  string
	ItemID     string
	Repo       string
	Branch     string
	Commit     string
	BaseCommit string
	// VulnID is the advisory or rule id when the work was vulnerability related.
	VulnID  string
	SeenRef string
	Verdict string
	Outcome string
	Data    map[string]string
}

// Event is one line of the stream file and one element of an upload. Its JSON
// names are the server's (vdb-site handler.belaiAuditEvent).
type Event struct {
	Seq        int64             `json:"seq"`
	At         int64             `json:"at"`
	Scope      string            `json:"scope"`
	Kind       string            `json:"kind"`
	Hostname   string            `json:"hostname,omitempty"`
	ActorKind  string            `json:"actorKind,omitempty"`
	Actor      string            `json:"actor,omitempty"`
	Crew       string            `json:"crew,omitempty"`
	SessionID  string            `json:"sessionId,omitempty"`
	ItemID     string            `json:"itemId,omitempty"`
	Repo       string            `json:"repo,omitempty"`
	Branch     string            `json:"branch,omitempty"`
	Commit     string            `json:"commit,omitempty"`
	BaseCommit string            `json:"baseCommit,omitempty"`
	VulnID     string            `json:"vulnId,omitempty"`
	SeenRef    string            `json:"seenRef,omitempty"`
	Verdict    string            `json:"verdict,omitempty"`
	Outcome    string            `json:"outcome,omitempty"`
	Data       map[string]string `json:"data,omitempty"`
	Prev       string            `json:"prev"`
	Hash       string            `json:"hash"`
}

const canonicalVersion = "belai-audit-v1"

// Canonical is the exact text Hash covers: the version, the previous hash, seq
// and time, every field in a fixed order as name=value lines, then the data
// facts sorted by key. No value can hold a newline (Clean), so two different
// events never share a canonical form. It is byte-for-byte the server's.
func Canonical(e Event) string {
	var b strings.Builder
	line := func(k, v string) {
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(v)
		b.WriteByte('\n')
	}
	b.WriteString(canonicalVersion)
	b.WriteByte('\n')
	line("prev", e.Prev)
	line("seq", strconv.FormatInt(e.Seq, 10))
	line("at", strconv.FormatInt(e.At, 10))
	line("scope", e.Scope)
	line("kind", e.Kind)
	line("hostname", e.Hostname)
	line("actorKind", e.ActorKind)
	line("actor", e.Actor)
	line("crew", e.Crew)
	line("sessionId", e.SessionID)
	line("itemId", e.ItemID)
	line("repo", e.Repo)
	line("branch", e.Branch)
	line("commit", e.Commit)
	line("baseCommit", e.BaseCommit)
	line("vulnId", e.VulnID)
	line("seenRef", e.SeenRef)
	line("verdict", e.Verdict)
	line("outcome", e.Outcome)
	keys := make([]string, 0, len(e.Data))
	for k := range e.Data {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		line("d."+k, e.Data[k])
	}
	return b.String()
}

// HashOf is the chain hash of e (its Hash field is ignored).
func HashOf(e Event) string {
	sum := sha256.Sum256([]byte(Canonical(e)))
	return hex.EncodeToString(sum[:])
}

// Verify walks events in order and returns the seq of the first one that does
// not continue the chain (wrong seq, wrong prev or a hash that does not
// recompute), or -1 when the whole stream is intact. A stream starts at seq 0
// with an empty prev.
func Verify(events []Event) int64 {
	prev, want := "", int64(0)
	for _, e := range events {
		if e.Seq != want || e.Prev != prev || HashOf(e) != e.Hash {
			return e.Seq
		}
		prev, want = e.Hash, e.Seq+1
	}
	return -1
}

var (
	shaPattern  = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)
	vulnPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,64}$`)
)

// Clean reduces a fact to the audit charset [A-Za-z0-9._:/@+-], replacing every
// other rune (a space, a quote, a newline, any non-ASCII rune) with '-' and
// capping it. It is the one place a string becomes an identifier.
func Clean(s string) string {
	s = strings.TrimSpace(s)
	var b strings.Builder
	for _, r := range s {
		if b.Len() >= maxValue {
			break
		}
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '_', r == ':', r == '/', r == '@', r == '+', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return b.String()
}

// hash returns s lowercased when it is a full commit id, else "".
func hash(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if shaPattern.MatchString(s) {
		return s
	}
	return ""
}

// vuln returns s when it has the advisory id shape, else "".
func vuln(s string) string {
	s = strings.TrimSpace(s)
	if vulnPattern.MatchString(s) {
		return s
	}
	return ""
}

// normalize turns a Fact into the cleaned fields of an Event. It reports false
// for an unknown kind, which is dropped.
func normalize(f Fact) (Event, bool) {
	if !f.Kind.Valid() {
		return Event{}, false
	}
	e := Event{
		Scope: f.Kind.Scope(), Kind: string(f.Kind),
		Actor: Clean(f.Actor), Crew: Clean(f.Crew), SessionID: Clean(f.SessionID),
		ItemID: Clean(f.ItemID), Repo: Clean(f.Repo), Branch: Clean(f.Branch),
		Commit: hash(f.Commit), BaseCommit: hash(f.BaseCommit), VulnID: vuln(f.VulnID), SeenRef: hash(f.SeenRef),
		Outcome: Clean(f.Outcome),
	}
	if actorKinds[f.ActorKind] {
		e.ActorKind = f.ActorKind
	}
	if verdicts[f.Verdict] {
		e.Verdict = f.Verdict
	}
	if len(f.Data) > 0 {
		keys := make([]string, 0, len(f.Data))
		for k := range f.Data {
			if dataKeys[k] {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		if len(keys) > maxData {
			keys = keys[:maxData]
		}
		if len(keys) > 0 {
			e.Data = make(map[string]string, len(keys))
			for _, k := range keys {
				e.Data[k] = Clean(f.Data[k])
			}
		}
	}
	return e, true
}
