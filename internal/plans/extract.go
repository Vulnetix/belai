package plans

import (
	"errors"
	"regexp"
	"strings"
)

var stepRe = regexp.MustCompile(`^\d+[.)]\s+(.+)$`)

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
