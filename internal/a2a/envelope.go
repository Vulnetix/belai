// Package a2a is A2A (agent-to-agent) messaging between Belai sessions
// (docs/a2a.md): harness broadcasts of what each session is working on, direct
// messages a model sends another agent, the replies, and acknowledgements.
//
// The vocabulary follows the Linux Foundation A2A protocol and the
// teammates/mailbox/broadcast vocabulary of multi-agent coding tools: an
// Envelope carries an AgentCard (the source agent's identity), the Work the
// agent is doing, and for a message or reply the A2A Message fields
// (messageId, contextId, referenceMessageId, text parts). The wire format is
// A2A-shaped but not A2A JSON-RPC.
//
// Same-host delivery is a file bus under the global directory (bus.go), so it
// works with no Vulnetix login. Cross-host delivery is the Relay (relay.go)
// through the Vulnetix API, stopped by an A2A blackout. Every envelope sent,
// received, withheld or filtered is written to a local hash-chained audit
// (audit.go) of facts only, never text parts.
//
// Every identifier field is cleaned to a fixed shape by Clean before it is
// stored, sent or shown. The text parts are the only free text: they are
// model-written, capped, sanitised, and classified (tools.KindPeer) by the
// receiving harness before any model reads them.
package a2a

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/vulnetix/belai/internal/sanitize"
)

// Envelope kinds.
const (
	KindBroadcast    = "broadcast"
	KindBroadcastEnd = "broadcast_end"
	KindMessage      = "message"
	KindReply        = "reply"
	KindAck          = "ack"
)

// Kinds lists every envelope kind.
func Kinds() []string {
	return []string{KindBroadcast, KindBroadcastEnd, KindMessage, KindReply, KindAck}
}

// CapMessage is the one capability an Agent Card advertises: the agent takes
// direct messages (the A2AMessage tool) and answers them in a side-turn.
const CapMessage = "a2a.message"

// Ack decisions (the A2AAck tool).
const (
	DecisionAdjust = "adjust"
	DecisionTodo   = "todo"
	DecisionWait   = "wait"
	DecisionIgnore = "ignore"
)

// Decisions lists the acknowledgement decisions.
func Decisions() []string {
	return []string{DecisionAdjust, DecisionTodo, DecisionWait, DecisionIgnore}
}

// ValidDecision reports whether d is an acknowledgement decision.
func ValidDecision(d string) bool {
	switch d {
	case DecisionAdjust, DecisionTodo, DecisionWait, DecisionIgnore:
		return true
	}
	return false
}

// Caps.
const (
	// MaxFiles bounds a broadcast's files.
	MaxFiles = 50
	// MaxTextBytes bounds a message or reply's text parts, together.
	MaxTextBytes = 4 << 10
	// MaxReasonRunes bounds an acknowledgement's reason.
	MaxReasonRunes = 140
	maxPathBytes   = 200
	maxBranchBytes = 200
	maxRepoBytes   = 128
	maxIdentBytes  = 64
	maxParts       = 4
	maxPR          = 1 << 30
)

// AgentCard is the source agent's identity, after the A2A Agent Card: id is
// "<hostId>:<sessionId>", displayed as "@<profile|belai>·<6 hex>".
type AgentCard struct {
	ID           string   `json:"id"`
	HostID       string   `json:"hostId"`
	SessionID    string   `json:"sessionId"`
	Profile      string   `json:"profile,omitempty"`
	Hostname     string   `json:"hostname,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
}

// Work is what an agent is working on: the repository (normalised owner/name,
// or "path:<hash>" for one with no remote), branch, repo-relative files, the
// pull request number and a ticket (a kanban id or a reference in the branch).
type Work struct {
	Repo   string   `json:"repo,omitempty"`
	Branch string   `json:"branch,omitempty"`
	Files  []string `json:"files,omitempty"`
	PR     int      `json:"pr,omitempty"`
	Ticket string   `json:"ticket,omitempty"`
}

// Part is one text part of a message or reply, after the A2A Message part.
type Part struct {
	Text string `json:"text"`
}

// Envelope is one A2A item. MessageID is "a2a-<ulid>"; ContextID is the
// thread: the first message's id for a message/reply chain, or the broadcast's
// own id. To, ReferenceMessageID and Parts are a message's, reply's or ack's.
// Decision is an ack's.
type Envelope struct {
	MessageID          string    `json:"messageId"`
	Kind               string    `json:"kind"`
	TS                 int64     `json:"ts"`
	ContextID          string    `json:"contextId,omitempty"`
	Agent              AgentCard `json:"agent"`
	Work               Work      `json:"work"`
	To                 string    `json:"to,omitempty"`
	ReferenceMessageID string    `json:"referenceMessageId,omitempty"`
	Parts              []Part    `json:"parts,omitempty"`
	Decision           string    `json:"decision,omitempty"`
}

// Text is the envelope's text parts joined.
func (e Envelope) Text() string {
	var parts []string
	for _, p := range e.Parts {
		if p.Text != "" {
			parts = append(parts, p.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// Directed reports whether the kind is addressed to one agent.
func (e Envelope) Directed() bool {
	return e.Kind == KindMessage || e.Kind == KindReply || e.Kind == KindAck
}

var (
	messageIDShape = regexp.MustCompile(`^a2a-[0-9a-z]{26}$`)
	uuidShape      = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	repoShape      = regexp.MustCompile(`^([A-Za-z0-9._-]{1,100}(/[A-Za-z0-9._-]{1,100}){1,3}|path:[0-9a-f]{16})$`)
	branchShape    = regexp.MustCompile(`^[A-Za-z0-9._/+@-]{1,200}$`)
	pathShape      = regexp.MustCompile(`^[A-Za-z0-9._/@+-]{1,200}$`)
	ticketShape    = regexp.MustCompile(`^#?[A-Za-z0-9._-]{1,64}$`)
)

// ValidMessageID reports whether id has the a2a-<ulid> shape.
func ValidMessageID(id string) bool { return messageIDShape.MatchString(id) }

// AgentID is the Agent Card id of a session on a host.
func AgentID(hostID, sessionID string) string { return hostID + ":" + sessionID }

// ValidAgentID reports whether id is "<uuid>:<uuid>".
func ValidAgentID(id string) bool {
	h, s, ok := strings.Cut(id, ":")
	return ok && uuidShape.MatchString(h) && uuidShape.MatchString(s)
}

// Display is the card as people and models read it: "@<profile>·<6 hex>".
func (c AgentCard) Display() string {
	return DisplayID(c.Profile, c.SessionID)
}

// DisplayID is "@<profile|belai>·<the session id's first 6 hex>".
func DisplayID(profile, sessionID string) string {
	name := sanitize.Ident(profile, maxIdentBytes)
	if name == "" {
		name = "belai"
	}
	hex := strings.ReplaceAll(sessionID, "-", "")
	if len(hex) > 6 {
		hex = hex[:6]
	}
	return "@" + name + "·" + hex
}

// ErrInvalid is returned by Clean for an envelope that cannot be kept.
var ErrInvalid = errors.New("a2a: invalid envelope")

func invalid(what string) error { return fmt.Errorf("%w: %s", ErrInvalid, what) }

// Clean returns the envelope with every identifier reduced to its shape and
// the free text capped and sanitised. An envelope whose kind, ids or Agent
// Card do not fit is refused; an identifier field that does not fit its shape
// is dropped. It is idempotent.
func (e Envelope) Clean() (Envelope, error) {
	out := Envelope{Kind: e.Kind, TS: e.TS}
	switch e.Kind {
	case KindBroadcast, KindBroadcastEnd, KindMessage, KindReply, KindAck:
	default:
		return Envelope{}, invalid("kind")
	}
	if !ValidMessageID(e.MessageID) {
		return Envelope{}, invalid("messageId")
	}
	out.MessageID = e.MessageID
	if e.ContextID != "" {
		if !ValidMessageID(e.ContextID) {
			return Envelope{}, invalid("contextId")
		}
		out.ContextID = e.ContextID
	}
	if out.TS < 0 {
		out.TS = 0
	}
	card, err := e.Agent.clean()
	if err != nil {
		return Envelope{}, err
	}
	out.Agent = card
	out.Work = e.Work.Clean()
	if e.Directed() {
		if !ValidAgentID(e.To) {
			return Envelope{}, invalid("to")
		}
		out.To = e.To
		if e.ReferenceMessageID != "" {
			if !ValidMessageID(e.ReferenceMessageID) {
				return Envelope{}, invalid("referenceMessageId")
			}
			out.ReferenceMessageID = e.ReferenceMessageID
		}
		if e.Kind == KindReply && out.ReferenceMessageID == "" {
			return Envelope{}, invalid("a reply names no message")
		}
		out.Parts = cleanParts(e.Parts)
		if e.Kind != KindAck && len(out.Parts) == 0 {
			return Envelope{}, invalid("no text")
		}
		if e.Kind == KindAck {
			if !ValidDecision(e.Decision) {
				return Envelope{}, invalid("decision")
			}
			out.Decision = e.Decision
		}
	}
	return out, nil
}

func (c AgentCard) clean() (AgentCard, error) {
	if !uuidShape.MatchString(c.HostID) || !uuidShape.MatchString(c.SessionID) {
		return AgentCard{}, invalid("agent")
	}
	out := AgentCard{
		ID:        AgentID(c.HostID, c.SessionID),
		HostID:    c.HostID,
		SessionID: c.SessionID,
		Profile:   sanitize.Ident(c.Profile, maxIdentBytes),
		Hostname:  sanitize.Ident(c.Hostname, maxIdentBytes),
	}
	if c.ID != "" && c.ID != out.ID {
		return AgentCard{}, invalid("agent id")
	}
	for _, cp := range c.Capabilities {
		if cp == CapMessage && len(out.Capabilities) == 0 {
			out.Capabilities = append(out.Capabilities, cp)
		}
	}
	return out, nil
}

// Clean reduces every field to its shape: a field that does not fit is
// dropped, files are kept in order up to MaxFiles.
func (w Work) Clean() Work {
	var out Work
	if len(w.Repo) <= maxRepoBytes && repoShape.MatchString(w.Repo) {
		out.Repo = w.Repo
	}
	if len(w.Branch) <= maxBranchBytes && branchShape.MatchString(w.Branch) && !strings.Contains(w.Branch, "..") {
		out.Branch = w.Branch
	}
	seen := map[string]bool{}
	for _, f := range w.Files {
		if len(out.Files) >= MaxFiles {
			break
		}
		if f = CleanPath(f); f != "" && !seen[f] {
			seen[f] = true
			out.Files = append(out.Files, f)
		}
	}
	if w.PR > 0 && w.PR <= maxPR {
		out.PR = w.PR
	}
	if ticketShape.MatchString(w.Ticket) {
		out.Ticket = w.Ticket
	}
	return out
}

// CleanPath returns a repo-relative path when it fits the shape (no spaces,
// no "..", not absolute), or "".
func CleanPath(p string) string {
	p = strings.TrimPrefix(p, "./")
	if len(p) > maxPathBytes || !pathShape.MatchString(p) || strings.HasPrefix(p, "/") {
		return ""
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." || seg == "." || seg == "" {
			return ""
		}
	}
	return p
}

// cleanParts sanitises the text parts and caps them at MaxTextBytes together.
func cleanParts(in []Part) []Part {
	var out []Part
	left := MaxTextBytes
	for _, p := range in {
		if len(out) >= maxParts || left <= 0 {
			break
		}
		t := strings.TrimSpace(sanitize.Text(p.Text))
		if t == "" {
			continue
		}
		t = capBytes(t, left)
		left -= len(t)
		out = append(out, Part{Text: t})
	}
	return out
}

// capBytes cuts s to at most max bytes on a rune boundary.
func capBytes(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && (s[cut]&0xC0) == 0x80 {
		cut--
	}
	return s[:cut]
}

// Marshal is the envelope's JSON line.
func (e Envelope) Marshal() ([]byte, error) { return json.Marshal(e) }

// crockford is the ULID alphabet, lower-cased.
const crockford = "0123456789abcdefghjkmnpqrstvwxyz"

// NewMessageID returns "a2a-<ulid>": 48 bits of milliseconds then 80 random
// bits, in lower-case Crockford base32.
func NewMessageID(now time.Time) string {
	var b [16]byte
	ms := uint64(now.UnixMilli())
	for i := 5; i >= 0; i-- {
		b[i] = byte(ms)
		ms >>= 8
	}
	_, _ = rand.Read(b[6:])
	// 128 bits → 26 characters of 5 bits, the first holding 3.
	var out [26]byte
	var hi, lo uint64
	for i := 0; i < 8; i++ {
		hi = hi<<8 | uint64(b[i])
		lo = lo<<8 | uint64(b[8+i])
	}
	for i := 25; i >= 0; i-- {
		out[i] = crockford[lo&31]
		lo = lo>>5 | (hi&31)<<59
		hi >>= 5
	}
	return "a2a-" + string(out[:])
}
