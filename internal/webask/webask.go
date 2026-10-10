// Package webask is the wire form of the questions a host asks and the web
// answers it takes (docs/session-sync.md): the ask kinds and decisions the
// website knows, the metadata a permission ask records, and the validation of
// a web clarify answer against the questionnaire actually open. The TUI and a
// `belai rc` session both use it, so an ask looks and validates the same
// wherever it was asked.
//
// A web answer is untrusted input. ParseClarify checks every group and option
// index against the open questionnaire and cleans and caps every note; a
// permission decision is one of three fixed words.
package webask

import (
	"encoding/json"
	"fmt"
	"unicode/utf8"

	"github.com/vulnetix/belai/internal/agent"
	"github.com/vulnetix/belai/internal/clarify"
	"github.com/vulnetix/belai/internal/sessionsync"
)

// Ask kinds, shared with the website.
const (
	KindPermission = "permission"
	KindClarify    = "clarify"
	KindModeChoice = "mode_choice"
	KindPlanReview = "plan_review"
)

// Permission decisions.
const (
	AllowOnce   = "allow_once"
	AllowAlways = "allow_always"
	Deny        = "deny"
)

// Answer sources.
const (
	FromHost = "host"
	FromWeb  = "web"
)

const (
	// MaxArgsBytes caps the tool arguments an ask records.
	MaxArgsBytes = 32 << 10
	// MaxNoteBytes caps a web answer's per-group note.
	MaxNoteBytes = 2 << 10
	// maxDiffWireBytes caps the diff a permission ask carries.
	maxDiffWireBytes = 256 << 10
)

// AnswerEntryID is the ask_answer entry id for an ask.
func AnswerEntryID(askID string) string { return "answer-" + askID }

// PermissionAsk is the content and metadata of a permission ask entry: the
// tool, its subject and arguments, the rule allow-always would add, and the
// diff the call would make.
func PermissionAsk(ask *agent.AskRequest) (string, map[string]any) {
	args := ""
	if len(ask.Args) > 0 {
		b, _ := json.Marshal(ask.Args)
		args = string(b)
	}
	if len(args) > MaxArgsBytes {
		args = Truncate(args, MaxArgsBytes)
	}
	meta := map[string]any{
		"tool":    ask.Name,
		"subject": ask.Subject,
		"args":    args,
		"rule":    Rule(ask),
		"options": []string{AllowOnce, AllowAlways, Deny},
	}
	if ask.Preview != nil && !ask.Preview.Empty() {
		meta["diff"] = ask.Preview.Wire(maxDiffWireBytes)
	}
	content := "permission: " + ask.Name
	if ask.Subject != "" {
		content += " " + ask.Subject
	}
	return content, meta
}

// Rule is the permission rule allow-always adds for an ask.
func Rule(ask *agent.AskRequest) string {
	if ask.Subject != "" {
		return ask.Name + "(" + ask.Subject + ")"
	}
	return ask.Name
}

// ParseDecision reads a web permission answer's decision.
func ParseDecision(raw json.RawMessage) (string, error) {
	var p struct {
		Decision string `json:"decision"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return "", fmt.Errorf("the answer could not be read")
	}
	switch p.Decision {
	case AllowOnce, AllowAlways, Deny:
		return p.Decision, nil
	}
	return "", fmt.Errorf("unknown decision; expected allow_once, allow_always or deny")
}

// WireAnswer is one clarify answer as the website sends it and the record
// stores it.
type WireAnswer struct {
	Group   int    `json:"group"`
	Chosen  []int  `json:"chosen,omitempty"`
	Note    string `json:"note,omitempty"`
	Skipped bool   `json:"skipped,omitempty"`
}

// WireAnswers is the record form of answers.
func WireAnswers(a clarify.Answers) []WireAnswer {
	out := make([]WireAnswer, 0, len(a.Items))
	for _, it := range a.Items {
		out = append(out, WireAnswer{Group: it.GroupIndex, Chosen: it.Chosen, Note: it.Note, Skipped: it.Skipped})
	}
	return out
}

// ParseClarify validates a web clarify payload against the open
// questionnaire: every group index in range and answered at most once, every
// chosen option in range and unique, a single-choice group given at most one,
// notes cleaned and capped. A group the web left out is skipped. A decline
// answers nothing, exactly as a host that dismisses the questions.
func ParseClarify(q clarify.Questionnaire, raw json.RawMessage) (clarify.Answers, bool, error) {
	var p struct {
		Answers []WireAnswer `json:"answers"`
		Decline bool         `json:"decline"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return clarify.Answers{}, false, fmt.Errorf("the answer could not be read")
	}
	if p.Decline {
		return clarify.Answers{}, true, nil
	}
	seen := map[int]bool{}
	byGroup := map[int]clarify.Answer{}
	for _, w := range p.Answers {
		if w.Group < 0 || w.Group >= len(q.Groups) {
			return clarify.Answers{}, false, fmt.Errorf("answer for question %d, which was not asked", w.Group+1)
		}
		if seen[w.Group] {
			return clarify.Answers{}, false, fmt.Errorf("question %d answered twice", w.Group+1)
		}
		seen[w.Group] = true
		g := q.Groups[w.Group]
		ans := clarify.Answer{GroupIndex: w.Group, Skipped: w.Skipped}
		if !w.Skipped {
			picked := map[int]bool{}
			for _, c := range w.Chosen {
				if c < 0 || c >= len(g.Options) {
					return clarify.Answers{}, false, fmt.Errorf("question %d has no option %d", w.Group+1, c+1)
				}
				if picked[c] {
					continue
				}
				picked[c] = true
				ans.Chosen = append(ans.Chosen, c)
			}
			if !g.Multi && len(ans.Chosen) > 1 {
				return clarify.Answers{}, false, fmt.Errorf("question %d takes one choice", w.Group+1)
			}
		}
		if note := sessionsync.CleanPrompt(w.Note); note != "" {
			if len(note) > MaxNoteBytes {
				note = Truncate(note, MaxNoteBytes)
			}
			ans.Note = note
		}
		byGroup[w.Group] = ans
	}
	out := clarify.Answers{}
	for gi := range q.Groups {
		if ans, ok := byGroup[gi]; ok {
			out.Items = append(out.Items, ans)
		} else {
			out.Items = append(out.Items, clarify.Answer{GroupIndex: gi, Skipped: true})
		}
	}
	return out, false, nil
}

// Truncate cuts s to at most max bytes without splitting a character.
func Truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// Plan-review choices, shared with the website. The TUI offers all four; a
// `belai rc` session has no second session to fork into, so it offers
// PlanApproveHere, PlanRefine and PlanStay.
const (
	PlanApproveHere = "approve_here"
	PlanApproveNew  = "approve_new"
	PlanRefine      = "refine"
	PlanStay        = "stay"
)

// MaxPlanBytes caps the plan text a plan-review ask carries.
const MaxPlanBytes = 64 << 10

// PlanReviewAsk is the content and metadata of a plan-review ask entry: the
// plan's name and path, the choices on offer and the plan text itself, so the
// website can show what it is approving. body is the recorded plan file.
func PlanReviewAsk(name, path, body string, options []string) (string, map[string]any) {
	meta := map[string]any{
		"plan_name": name,
		"plan_path": path,
		"options":   options,
	}
	if body != "" {
		if len(body) > MaxPlanBytes {
			body = Truncate(body, MaxPlanBytes)
			meta["plan_truncated"] = true
		}
		meta["plan"] = body
	}
	return "plan written: " + path, meta
}

// ParsePlanChoice reads a web plan-review answer: one of the fixed choices
// and, for a refinement, the notes. The notes are untrusted web text, so they
// are cleaned like a web prompt and capped; a refinement with no notes left
// is refused.
func ParsePlanChoice(raw json.RawMessage) (choice, notes string, err error) {
	var p struct {
		Choice string `json:"choice"`
		Notes  string `json:"notes"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return "", "", fmt.Errorf("the answer could not be read")
	}
	switch p.Choice {
	case PlanApproveHere, PlanApproveNew, PlanStay:
		return p.Choice, "", nil
	case PlanRefine:
		notes = sessionsync.CleanPrompt(p.Notes)
		if notes == "" {
			return "", "", fmt.Errorf("refinement notes were empty after cleaning")
		}
		if len(notes) > MaxNoteBytes*4 {
			notes = Truncate(notes, MaxNoteBytes*4)
		}
		return p.Choice, notes, nil
	}
	return "", "", fmt.Errorf("unknown plan choice; expected approve_here, refine or stay")
}
