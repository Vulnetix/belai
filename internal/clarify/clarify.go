// Package clarify models the explore→questionnaire→answer loop used when a
// plan-mode prompt needs user clarification before planning. It is pure:
// no I/O, no model calls, and no TUI code.
package clarify

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/vulnetix/signet/internal/sanitize"
)

// Limits enforced by Validate. They match the schema contract described in
// docs/role-manager.md.
const (
	maxGroups           = 6
	minOptionsPerGroup  = 2
	maxOptionsPerGroup  = 4
	maxContextRunes     = 200
	maxLabelRunes       = 80
	maxDescriptionRunes = 160
)

// Option is one answerable choice inside a group.
type Option struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// Group is one context sentence and its options.
type Group struct {
	Context string   `json:"context"`
	Multi   bool     `json:"multi,omitempty"`
	Options []Option `json:"options"`
}

// Questionnaire is the classifier's reply to the clarifier prompt.
type Questionnaire struct {
	Groups []Group `json:"groups"`
}

// Empty reports whether the classifier explicitly signalled the end of
// questions with {"groups": []}.
func (q Questionnaire) Empty() bool { return len(q.Groups) == 0 }

// Answer is the user's response to one group.
type Answer struct {
	GroupIndex int
	Chosen     []int
	Note       string
	Skipped    bool
}

// Answers collects the user's response to a whole questionnaire.
type Answers struct {
	Items []Answer
}

// Parse strips Markdown fences and <thinking> blocks, then decodes the raw
// classifier reply into a Questionnaire. Unknown JSON fields are rejected.
func Parse(raw string) (Questionnaire, error) {
	clean := strings.TrimSpace(raw)

	for {
		start := strings.Index(clean, "<thinking>")
		if start == -1 {
			break
		}
		end := strings.Index(clean[start:], "</thinking>")
		if end == -1 {
			clean = clean[:start]
			break
		}
		clean = clean[:start] + clean[start+end+len("</thinking>"):]
		clean = strings.TrimSpace(clean)
	}
	clean = strings.TrimSpace(clean)

	clean = strings.TrimPrefix(clean, "```json")
	clean = strings.TrimPrefix(clean, "```")
	clean = strings.TrimSuffix(clean, "```")
	clean = strings.TrimSpace(clean)

	d := json.NewDecoder(strings.NewReader(clean))
	d.DisallowUnknownFields()
	var q Questionnaire
	if err := d.Decode(&q); err != nil {
		return Questionnaire{}, fmt.Errorf("invalid JSON: %w", err)
	}
	return sanitizeQuestionnaire(q), nil
}

// Validate checks the schema limits and returns all violations as a single
// joined string suitable for model feedback. A nil return means the
// questionnaire is usable.
func (q Questionnaire) Validate() error {
	if len(q.Groups) == 0 {
		return nil
	}
	if len(q.Groups) > maxGroups {
		return fmt.Errorf("too many groups: %d (max %d)", len(q.Groups), maxGroups)
	}

	var errs []string
	seenContexts := map[string]bool{}

	for gi, g := range q.Groups {
		ctx := strings.TrimSpace(g.Context)
		if ctx == "" {
			errs = append(errs, fmt.Sprintf("group %d: context is empty", gi+1))
		} else {
			if strings.Contains(ctx, "\n") {
				errs = append(errs, fmt.Sprintf("group %d: context must be a single line", gi+1))
			}
			if utf8.RuneCountInString(ctx) > maxContextRunes {
				errs = append(errs, fmt.Sprintf("group %d: context exceeds %d runes", gi+1, maxContextRunes))
			}
			last := ' '
			if len(ctx) > 0 {
				last = rune(ctx[len(ctx)-1])
			}
			if last != '.' && last != '?' {
				errs = append(errs, fmt.Sprintf("group %d: context must end with '.' or '?'", gi+1))
			}
		}
		if seenContexts[ctx] {
			errs = append(errs, fmt.Sprintf("group %d: duplicate context", gi+1))
		} else if ctx != "" {
			seenContexts[ctx] = true
		}

		if len(g.Options) < minOptionsPerGroup {
			errs = append(errs, fmt.Sprintf("group %d: need at least %d options", gi+1, minOptionsPerGroup))
		}
		if len(g.Options) > maxOptionsPerGroup {
			errs = append(errs, fmt.Sprintf("group %d: at most %d options", gi+1, maxOptionsPerGroup))
		}

		seenLabels := map[string]bool{}
		for oi, o := range g.Options {
			label := strings.TrimSpace(o.Label)
			if label == "" {
				errs = append(errs, fmt.Sprintf("group %d option %d: label is empty", gi+1, oi+1))
			} else {
				if strings.Contains(label, "\n") {
					errs = append(errs, fmt.Sprintf("group %d option %d: label must be a single line", gi+1, oi+1))
				}
				if utf8.RuneCountInString(label) > maxLabelRunes {
					errs = append(errs, fmt.Sprintf("group %d option %d: label exceeds %d runes", gi+1, oi+1, maxLabelRunes))
				}
				if seenLabels[label] {
					errs = append(errs, fmt.Sprintf("group %d: duplicate label %q", gi+1, label))
				}
				seenLabels[label] = true
			}
			desc := strings.TrimSpace(o.Description)
			if desc != "" {
				if strings.Contains(desc, "\n") {
					errs = append(errs, fmt.Sprintf("group %d option %d: description must be a single line", gi+1, oi+1))
				}
				if utf8.RuneCountInString(desc) > maxDescriptionRunes {
					errs = append(errs, fmt.Sprintf("group %d option %d: description exceeds %d runes", gi+1, oi+1, maxDescriptionRunes))
				}
			}
		}
	}

	if len(errs) == 0 {
		return nil
	}
	return errors.New(strings.Join(errs, "; "))
}

// Render turns a set of answers into the harness-authored user turn that
// re-enters the agent conversation.
func (a Answers) Render(q Questionnaire) string {
	if len(a.Items) == 0 {
		return "Clarifications from the user: (none)"
	}
	var b strings.Builder
	b.WriteString("Clarifications from the user:\n")
	for i, ans := range a.Items {
		if ans.GroupIndex < 0 || ans.GroupIndex >= len(q.Groups) {
			continue
		}
		g := q.Groups[ans.GroupIndex]
		fmt.Fprintf(&b, "%d. %s\n", i+1, g.Context)
		if ans.Skipped {
			b.WriteString("   skipped\n")
			continue
		}
		var labels []string
		for _, idx := range ans.Chosen {
			if idx >= 0 && idx < len(g.Options) {
				labels = append(labels, g.Options[idx].Label)
			}
		}
		if len(labels) == 0 {
			b.WriteString("   chose: (none)\n")
		} else {
			b.WriteString("   chose: " + strings.Join(labels, ", ") + "\n")
		}
		if ans.Note != "" {
			b.WriteString("   note: " + ans.Note + "\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// sanitizeQuestionnaire applies sanitize.Sanitize to every human-readable
// string in the parsed questionnaire.
func sanitizeQuestionnaire(q Questionnaire) Questionnaire {
	for gi := range q.Groups {
		q.Groups[gi].Context = sanitize.Sanitize(q.Groups[gi].Context)
		for oi := range q.Groups[gi].Options {
			q.Groups[gi].Options[oi].Label = sanitize.Sanitize(q.Groups[gi].Options[oi].Label)
			q.Groups[gi].Options[oi].Description = sanitize.Sanitize(q.Groups[gi].Options[oi].Description)
		}
	}
	return q
}
