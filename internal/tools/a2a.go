package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// The A2A tools (docs/a2a.md). A2AMessage sends a direct message to another
// agent whose broadcast this session received; it is always asynchronous: the
// other agent answers in a side-turn and the reply arrives later as a
// classified attachment. A2AAck records the session's decision on a broadcast
// or message. Both return a confirmation the harness composes, never touch the
// workspace, and are offered in plan mode (Mutates false, KindA2A read-only).
// They are added only to a session with an A2A endpoint (WithA2A).

// A2A tool names.
const (
	A2AMessageName = "A2AMessage"
	A2AAckName     = "A2AAck"
)

// A2A argument caps.
const (
	MaxA2AText   = 4 << 10
	MaxA2AReason = 140
)

// A2AHub is the session's A2A endpoint. The harness implements it over
// internal/a2a; no tool argument names a path, a host or a file.
type A2AHub interface {
	// A2AMessage sends text to the agent to names (an Agent Card id or a
	// display id) and returns the message id and the agent's display id.
	A2AMessage(ctx context.Context, to, text string) (messageID, display string, err error)
	// A2AAck records a decision on a received broadcast or message, sending
	// it to the sender unless the decision is ignore.
	A2AAck(ctx context.Context, messageID, decision, reason string) (sent bool, err error)
}

// A2ADecisions are A2AAck's decisions.
var A2ADecisions = []string{"adjust", "todo", "wait", "ignore"}

// A2AMessage is the direct-message tool.
type A2AMessage struct{ Hub A2AHub }

// Definition returns the static tool metadata.
func (t A2AMessage) Definition() Definition {
	return Definition{
		Name: A2AMessageName,
		Description: "Send a question to another agent over A2A: an agent whose A2A broadcast reached this session (its id is in the broadcast, e.g. @belai·1a2b3c). " +
			"It is always asynchronous: this returns at once, the other agent answers in a side-turn without stopping its work, and its reply arrives in a later message tied to your question. " +
			"Keep it to one short, specific question about the overlapping work (files, branch, PR). Do not wait on it; carry on.",
		Properties: map[string]Property{
			"to":   {Type: "string", Description: "The agent's id from its broadcast: @profile·hex or the full host:session id"},
			"text": {Type: "string", Description: "The message, at most 4 KB"},
		},
		Required: []string{"to", "text"},
	}
}

// Kind returns KindA2A.
func (t A2AMessage) Kind() Kind { return KindA2A }

// Subject returns the recipient.
func (t A2AMessage) Subject(args map[string]any) string {
	v, _ := argString(args, "to")
	return v
}

// Mutates reports false: a message never touches the workspace.
func (t A2AMessage) Mutates() bool { return false }

// Execute sends the message.
func (t A2AMessage) Execute(ctx context.Context, args map[string]any) (Result, error) {
	if t.Hub == nil {
		return Result{}, errors.New("A2A messaging is not available in this session")
	}
	to, _ := argString(args, "to")
	text, _ := argString(args, "text")
	to = strings.TrimSpace(to)
	if to == "" {
		return Result{}, errors.New("missing to argument")
	}
	if strings.TrimSpace(text) == "" {
		return Result{}, errors.New("missing text argument")
	}
	if len(text) > MaxA2AText {
		return Result{}, fmt.Errorf("text is %d bytes; the limit is %d", len(text), MaxA2AText)
	}
	id, display, err := t.Hub.A2AMessage(ctx, to, text)
	if err != nil {
		return Result{}, err
	}
	return Result{Kind: KindA2A, Content: fmt.Sprintf("Sent %s to %s. The reply will arrive in a later message.", id, display),
		Meta: map[string]any{"messageId": id}}, nil
}

// A2AAck is the acknowledgement tool.
type A2AAck struct{ Hub A2AHub }

// Definition returns the static tool metadata.
func (t A2AAck) Definition() Definition {
	return Definition{
		Name: A2AAckName,
		Description: "Record your decision on an A2A broadcast or message another agent sent: adjust (you changed your tool calls to avoid the overlap), todo (you added a todo), wait (you will wait for it), or ignore. " +
			"Give one short sentence of reason. adjust, todo and wait are also sent to the other agent; ignore is only recorded. " +
			`Example: A2AAck{messageId:"a2a-…", decision:"ignore", reason:"I only edit docs/; no overlap."}`,
		Properties: map[string]Property{
			"messageId": {Type: "string", Description: "The broadcast's or message's id (a2a-…)"},
			"decision":  {Type: "string", Description: "adjust, todo, wait or ignore", Enum: A2ADecisions},
			"reason":    {Type: "string", Description: "One short sentence, at most 140 characters"},
		},
		Required: []string{"messageId", "decision", "reason"},
	}
}

// Kind returns KindA2A.
func (t A2AAck) Kind() Kind { return KindA2A }

// Subject returns the message id.
func (t A2AAck) Subject(args map[string]any) string {
	v, _ := argString(args, "messageId")
	return v
}

// Mutates reports false: an acknowledgement never touches the workspace.
func (t A2AAck) Mutates() bool { return false }

// Execute records the decision.
func (t A2AAck) Execute(ctx context.Context, args map[string]any) (Result, error) {
	if t.Hub == nil {
		return Result{}, errors.New("A2A messaging is not available in this session")
	}
	id, _ := argString(args, "messageId")
	decision, _ := argString(args, "decision")
	reason, _ := argString(args, "reason")
	id, decision, reason = strings.TrimSpace(id), strings.TrimSpace(decision), strings.TrimSpace(reason)
	valid := false
	for _, d := range A2ADecisions {
		valid = valid || d == decision
	}
	if !valid {
		return Result{}, fmt.Errorf("decision must be one of %s", strings.Join(A2ADecisions, ", "))
	}
	if reason == "" {
		return Result{}, errors.New("missing reason argument: one short sentence")
	}
	if utf8.RuneCountInString(reason) > MaxA2AReason {
		return Result{}, fmt.Errorf("reason is %d characters; keep it to one short sentence of at most %d", utf8.RuneCountInString(reason), MaxA2AReason)
	}
	sent, err := t.Hub.A2AAck(ctx, id, decision, reason)
	if err != nil {
		return Result{}, err
	}
	msg := fmt.Sprintf("Recorded %s on %s.", decision, id)
	if sent {
		msg += " The other agent was told."
	}
	return Result{Kind: KindA2A, Content: msg}, nil
}

// WithA2A returns r plus A2AMessage and A2AAck over hub. A nil hub returns r
// unchanged (A2A messaging is off).
func (r *Registry) WithA2A(hub A2AHub) *Registry {
	if hub == nil {
		return r
	}
	return r.With(A2AMessage{Hub: hub}, A2AAck{Hub: hub})
}

var (
	_ Tool    = A2AMessage{}
	_ Tool    = A2AAck{}
	_ Mutator = A2AMessage{}
	_ Mutator = A2AAck{}
)
