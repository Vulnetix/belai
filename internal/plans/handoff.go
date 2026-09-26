package plans

import (
	"path/filepath"
	"regexp"
	"strings"
)

// taskCountThreshold is the minimum number of task items that make a Markdown
// file look like a plan when the path/basename hints are absent.
const taskCountThreshold = 3

var (
	// checkboxRe matches GitHub-style task checkboxes.
	checkboxRe = regexp.MustCompile(`^\s*[-*]\s+\[[ xX]\]\s+(.*)$`)
	// stepHeadingRe matches Markdown headings used as step markers.
	stepHeadingRe = regexp.MustCompile(`^#{3,4}\s+(?:[Ss]tep\s+)?(\d+)[.):\s]+(.*)$`)
	// backtickPathRe matches backticked tokens and bare slash-shaped paths.
	backtickPathRe = regexp.MustCompile("`([^`]+)`|\\b([a-zA-Z0-9_.-]+/+[a-zA-Z0-9_./-]+)\\b")
	// looksLikePathRe filters referenced tokens to path-shaped ones.
	looksLikePathRe = regexp.MustCompile(`[/.]`)
)

// HandoffFacts holds harness-computed metadata about a plan-file attachment.
// It deliberately lives in the plans package so rolemanager can remain the
// higher-level consumer without creating an import cycle.
type HandoffFacts struct {
	Label string
	Tasks int
	Paths []string
}

// DetectHandoff decides whether an attached file should be treated as a plan
// for the belai:plan-handoff profile. A file is a plan when it is Markdown
// and at least one of the path/basename hints is present or it contains enough
// task markers.
func DetectHandoff(label, body, plansDir string) (HandoffFacts, bool) {
	if !isMarkdown(label) {
		return HandoffFacts{}, false
	}
	hasHint := isUnderPlansDir(label, plansDir) ||
		strings.Contains(strings.ToLower(filepath.ToSlash(filepath.Dir(label))), "/plans/") ||
		strings.Contains(strings.ToLower(filepath.Base(label)), "plan")
	count := CountTasks(body)
	if !hasHint && count < taskCountThreshold {
		return HandoffFacts{}, false
	}
	return HandoffFacts{
		Label: label,
		Tasks: count,
		Paths: ReferencedPaths(body),
	}, true
}

// isMarkdown reports whether label names a Markdown file by extension.
func isMarkdown(label string) bool {
	switch strings.ToLower(filepath.Ext(label)) {
	case ".md", ".markdown":
		return true
	}
	return false
}

// isUnderPlansDir reports whether label resolves inside plansDir.
func isUnderPlansDir(label, plansDir string) bool {
	if plansDir == "" {
		return false
	}
	absPlans, err := filepath.Abs(plansDir)
	if err != nil {
		return false
	}
	absLabel, err := filepath.Abs(label)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(absPlans, absLabel)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// CountTasks counts the task items in a Markdown body: numbered lists,
// checkboxes, and step headings. It reuses ExtractSteps' stepRe for numbered
// items so the handoff detector agrees with the plan extractor on what counts
// as a step.
func CountTasks(md string) int {
	count := 0
	for _, line := range strings.Split(md, "\n") {
		t := strings.TrimSpace(line)
		if t == "" {
			continue
		}
		if stepRe.MatchString(t) {
			count++
			continue
		}
		if checkboxRe.MatchString(t) {
			count++
			continue
		}
		if stepHeadingRe.MatchString(t) {
			count++
		}
	}
	return count
}

// ReferencedPaths returns the path-shaped tokens referenced in a Markdown
// body. It collects backticked tokens and slash-containing words. The caller
// filters these against the session confinement roots and filesystem.
func ReferencedPaths(md string) []string {
	seen := map[string]bool{}
	var out []string
	for _, line := range strings.Split(md, "\n") {
		for _, m := range backtickPathRe.FindAllStringSubmatch(line, -1) {
			token := m[1]
			if token == "" {
				token = m[2]
			}
			token = strings.TrimSpace(token)
			if token == "" || !looksLikePath(token) || seen[token] {
				continue
			}
			seen[token] = true
			out = append(out, token)
		}
	}
	return out
}

// looksLikePath is a cheap filter that keeps tokens with a slash or dot,
// dropping things like variable names that happen to be backticked.
func looksLikePath(s string) bool {
	return looksLikePathRe.MatchString(s)
}
