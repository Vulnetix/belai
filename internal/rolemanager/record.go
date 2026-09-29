package rolemanager

import (
	"strings"
)

// RecordType is the session-entry type and role of a role-manager record.
const RecordType = "rolemanager"

// Record is the persisted form of one activity: the session entry that says a
// role-manager decision happened. Every activity has one, whether or not the
// feed shows it, so the transcript is a complete account of what the harness
// decided. It is built from the bounded Activity only:
//
//   - the event name, verdict token and its label, the subject (a tool or use
//     case name), the pass, the model identity, the wall-clock time and the
//     sequence number;
//   - for an event the feed describes, the plain-English summary and outcome
//     with their tone and level, so a resumed session shows the same row;
//   - never Detail, a prompt, a command, a path or classified text.
type Record struct {
	// Content is the row text ("summary - outcome"), or the event name for an
	// event the feed does not show.
	Content string
	// Meta is the entry metadata.
	Meta map[string]any
	// Timestamp is when the decision was recorded, in Unix milliseconds.
	Timestamp int64
}

// Record builds the persisted form of the activity.
func (a Activity) Record() Record {
	meta := Facts(a)
	meta["activity"] = string(a.Event)
	if a.Seq > 0 {
		meta["seq"] = a.Seq
	}
	if a.Model != "" {
		meta["model"] = a.Model
		if p, m, ok := strings.Cut(a.Model, "/"); ok {
			meta["provider"], meta["model"] = p, m
		}
	}
	if a.Duration > 0 {
		meta["duration_ms"] = a.Duration.Milliseconds()
	}
	content := string(a.Event)
	if d, shown := Describe(a); shown {
		meta["summary"] = d.Summary
		meta["outcome"] = d.Outcome
		meta["tone"] = int(d.Tone)
		meta["level"] = int(d.Levels)
		content = d.Summary + " — " + d.Outcome
	} else {
		// The feed does not print this event (its own dedicated line does);
		// hidden keeps a resumed TUI from rendering it twice.
		meta["hidden"] = true
	}
	r := Record{Content: content, Meta: meta}
	if !a.At.IsZero() {
		r.Timestamp = a.At.UnixMilli()
	}
	return r
}

// Facts is the bounded metadata a decision persists. Detail is never carried:
// only the verdict token, its label, the subject, the pass and the model
// reach the record, exactly as the feed's own rule.
func Facts(act Activity) map[string]any {
	f := map[string]any{}
	if act.Verdict != "" {
		f["verdict"] = act.Verdict
		if l := VerdictLabel(act.Verdict); l != "" {
			f["verdict_label"] = l
		}
	}
	if act.Subject != "" {
		f["subject"] = act.Subject
	}
	if act.Pass > 0 {
		f["pass"] = act.Pass
	}
	return f
}

// VerdictLabel finds the human label for a sentinel of any role, or "".
func VerdictLabel(v string) string {
	if l, ok := SentinelLabels[Sentinel(v)]; ok {
		return l
	}
	if l, ok := PlanSentinelLabels[PlanSentinel(v)]; ok {
		return l
	}
	if l, ok := GoalSentinelLabels[GoalSentinel(v)]; ok {
		return l
	}
	if l, ok := DepSentinelLabels[DepSentinel(v)]; ok {
		return l
	}
	if l, ok := BashReplanSentinelLabels[BashReplanSentinel(v)]; ok {
		return l
	}
	if l, ok := IntentLabels[Intent(v)]; ok {
		return l
	}
	return ""
}
