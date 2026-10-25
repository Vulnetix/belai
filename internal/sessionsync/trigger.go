package sessionsync

import (
	"encoding/json"
	"strings"

	"github.com/vulnetix/belai/internal/sanitize"
)

// Trigger says what started a dispatched session or worker: a Pix Call, the
// console, a finding, a pull request, the fleet, a workflow or the Pix
// assistant, with the workflow run and the GitHub delivery behind it when
// there is one. The website writes it, so every field is untrusted text: it
// is decoded leniently, cleaned by Clean, and reaches a model only as the
// provenance sentence in a classified attachment (never a system block or a
// directive).
type Trigger struct {
	Source       string `json:"source"`
	Via          string `json:"via,omitempty"`
	RunID        string `json:"runId,omitempty"`
	WorkflowID   string `json:"workflowId,omitempty"`
	WorkflowName string `json:"workflowName,omitempty"`
	Event        string `json:"event,omitempty"`
	Action       string `json:"action,omitempty"`
	Delivery     string `json:"delivery,omitempty"`
	Subject      string `json:"subject,omitempty"`
	Repo         string `json:"repo,omitempty"`
	Note         string `json:"note,omitempty"`
}

// Trigger sources and the ways a workflow is fired.
var (
	triggerSources = map[string]bool{"pix_call": true, "console": true, "finding": true, "pull_request": true,
		"fleet": true, "workflow": true, "pix_assistant": true}
	triggerVias = map[string]bool{"webhook": true, "cron": true, "manual": true, "kanban": true, "finding": true, "label": true}
)

// Caps on the cleaned fields, in runes.
const (
	triggerIDMax      = 64
	triggerWordMax    = 40
	triggerNameMax    = 80
	triggerSubjectMax = 120
	triggerRepoMax    = 100
	triggerNoteMax    = 200
	// TriggerMaxBytes bounds the raw JSON a child accepts on -trigger.
	TriggerMaxBytes = 4 << 10
)

// UnmarshalJSON takes the string fields it knows and ignores the rest. A
// trigger that is not an object, or a field that is not a string, never fails
// the dispatch that carries it: the field is dropped instead.
func (t *Trigger) UnmarshalJSON(b []byte) error {
	*t = Trigger{}
	var raw map[string]json.RawMessage
	if json.Unmarshal(b, &raw) != nil {
		return nil
	}
	str := func(k string) string {
		var s string
		if v, ok := raw[k]; ok && json.Unmarshal(v, &s) == nil {
			return s
		}
		return ""
	}
	*t = Trigger{
		Source: str("source"), Via: str("via"), RunID: str("runId"), WorkflowID: str("workflowId"),
		WorkflowName: str("workflowName"), Event: str("event"), Action: str("action"), Delivery: str("delivery"),
		Subject: str("subject"), Repo: str("repo"), Note: str("note"),
	}
	return nil
}

// Clean returns the trigger with every field reduced to one short line, or
// nil when there is none or its source is not one Belai knows. Identifiers
// keep only identifier characters, names and notes lose control characters,
// newlines and delimiter markup, and a via is kept only for a workflow.
func (t *Trigger) Clean() *Trigger {
	if t == nil {
		return nil
	}
	src := strings.ToLower(sanitize.Ident(t.Source, triggerWordMax))
	if !triggerSources[src] {
		return nil
	}
	c := &Trigger{
		Source:       src,
		RunID:        sanitize.Ident(t.RunID, triggerIDMax),
		WorkflowID:   sanitize.Ident(t.WorkflowID, triggerIDMax),
		WorkflowName: triggerLine(strings.ReplaceAll(t.WorkflowName, `"`, "'"), triggerNameMax),
		Event:        sanitize.Ident(t.Event, triggerWordMax),
		Action:       sanitize.Ident(t.Action, triggerWordMax),
		Delivery:     sanitize.Ident(t.Delivery, triggerIDMax),
		Subject:      triggerLine(t.Subject, triggerSubjectMax),
		Repo:         triggerLine(t.Repo, triggerRepoMax),
		Note:         triggerLine(t.Note, triggerNoteMax),
	}
	if via := strings.ToLower(sanitize.Ident(t.Via, triggerWordMax)); src == "workflow" && triggerVias[via] {
		c.Via = via
	}
	return c
}

// triggerLine is sanitize.Line: control and bidi runes, newlines and delimiter
// markup removed, whitespace folded, capped at max runes.
func triggerLine(s string, max int) string { return sanitize.Line(s, max) }

// ParseTrigger decodes and cleans a trigger a parent process passed on
// (-trigger or BELAI_SESSION_TRIGGER). Empty, oversized or unknown input is
// nil: a trigger only ever adds a provenance line, so a bad one is dropped.
func ParseTrigger(raw string) *Trigger {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > TriggerMaxBytes {
		return nil
	}
	var t Trigger
	if json.Unmarshal([]byte(raw), &t) != nil {
		return nil
	}
	return t.Clean()
}

// Encode is the trigger as compact JSON, for -trigger and the
// BELAI_SESSION_TRIGGER environment variable. Callers encode a cleaned
// trigger.
func (t Trigger) Encode() string {
	b, err := json.Marshal(t)
	if err != nil {
		return ""
	}
	return string(b)
}

// Sentence is the one plain line the coding agent reads about how its session
// started, built from a cleaned trigger. It states provenance only; the
// caller labels it as data and it rides in a classified attachment.
func (t Trigger) Sentence() string {
	target := t.Subject
	if target == "" {
		target = t.Repo
	}
	var b strings.Builder
	switch t.Source {
	case "pix_call":
		b.WriteString("Started from a Pix Call at the user's request")
		if t.Note != "" {
			b.WriteString(": " + t.Note)
		}
		return endSentence(b.String())
	case "console":
		b.WriteString("Started from the Vulnetix console")
	case "pix_assistant":
		b.WriteString("Started from the Pix assistant")
	case "finding":
		b.WriteString("Started from a finding")
		if t.Subject != "" {
			b.WriteString(" (" + t.Subject + ")")
		}
		if t.Repo != "" {
			b.WriteString(" in " + t.Repo)
		}
	case "pull_request":
		if t.Subject != "" {
			b.WriteString("Started from pull request " + t.Subject)
		} else {
			b.WriteString("Started from a pull request")
			if t.Repo != "" {
				b.WriteString(" in " + t.Repo)
			}
		}
	case "fleet":
		b.WriteString("Started by a fleet dispatch")
		if t.Repo != "" {
			b.WriteString(" in " + t.Repo)
		}
	case "workflow":
		name := "a workflow"
		if t.WorkflowName != "" {
			name = `workflow "` + t.WorkflowName + `"`
		}
		switch t.Via {
		case "cron":
			b.WriteString("Started by a scheduled run of " + name)
		case "manual":
			b.WriteString("Started by a manual run of " + name)
		default:
			b.WriteString("Started by " + name)
		}
		if t.RunID != "" {
			b.WriteString(" (run " + t.RunID + ")")
		}
		switch t.Via {
		case "webhook":
			b.WriteString(", triggered by a GitHub")
			for _, w := range []string{t.Event, t.Action} {
				if w != "" {
					b.WriteString(" " + w)
				}
			}
			b.WriteString(" webhook")
			if target != "" {
				b.WriteString(" on " + target)
			}
		case "label":
			b.WriteString(", triggered by a label")
			if target != "" {
				b.WriteString(" on " + target)
			}
		case "kanban":
			b.WriteString(", triggered by a kanban card")
			if t.Subject != "" {
				b.WriteString(" (" + t.Subject + ")")
			}
		case "finding":
			b.WriteString(", triggered by a finding")
			if t.Subject != "" {
				b.WriteString(" (" + t.Subject + ")")
			}
		}
	default:
		return ""
	}
	s := endSentence(b.String())
	if t.Note != "" {
		s += " Note: " + endSentence(t.Note)
	}
	return s
}

// endSentence closes s with a full stop unless it already ends a sentence.
func endSentence(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || strings.HasSuffix(s, ".") || strings.HasSuffix(s, "!") || strings.HasSuffix(s, "?") {
		return s
	}
	return s + "."
}
