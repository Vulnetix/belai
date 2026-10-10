package plans

import (
	"errors"
	"regexp"
	"strings"
)

var stepRe = regexp.MustCompile(`^\d+[.)]\s+(.+)$`)

// planStartRe matches the line a plan document starts at: its title or its
// first section heading.
var planStartRe = regexp.MustCompile(`^(#\s+\S|##\s+(Summary|Overview|Context|Key Changes|Steps|Implementation)\b)`)

// ExtractDoc returns the plan document inside a model reply, and whether the
// reply holds one. A document is a summary plus at least one numbered step in a
// steps section (ParseDoc), the shape ExitPlanMode requires. The text before
// the document's first heading is narration ("I have everything I need.") and
// is dropped, so a plan written as a reply records as cleanly as one passed to
// ExitPlanMode. A reply that is only a "Plan:" list is not a document.
func ExtractDoc(reply string) (string, bool) {
	lines := strings.Split(reply, "\n")
	start := -1
	inFence := false
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~") {
			inFence = !inFence
			continue
		}
		if !inFence && planStartRe.MatchString(t) {
			start = i
			break
		}
	}
	if start < 0 {
		return "", false
	}
	doc := strings.TrimSpace(strings.Join(lines[start:], "\n"))
	d, err := ParseDoc(doc)
	if err != nil || strings.TrimSpace(d.Summary) == "" {
		return "", false
	}
	return doc, true
}

// ExtractSteps parses a numbered plan out of agent output. Steps are the lines
// following a "Plan:" header that look like "1. do this" / "2) do that".
// Collection stops at the first non-empty, non-step line after steps begin.
func ExtractSteps(text string) ([]string, error) {
	lines := strings.Split(text, "\n")
	inPlan := false
	started := false
	var steps []string
	for _, line := range lines {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "Plan:") {
			inPlan = true
			continue
		}
		if !inPlan {
			continue
		}
		if t == "" {
			continue
		}
		if m := stepRe.FindStringSubmatch(t); m != nil {
			steps = append(steps, strings.TrimSpace(m[1]))
			started = true
			continue
		}
		if started {
			break
		}
	}
	if len(steps) == 0 {
		return nil, errors.New("no numbered steps found under Plan:")
	}
	return steps, nil
}
