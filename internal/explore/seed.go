package explore

import (
	"fmt"
	"strings"
)

// Seeding hands explore subagents the files a ranking put first, so they start
// where the code is instead of searching for it. The seed is a list of paths
// and line numbers the harness computed; it holds no file text.

// SeedFile is one ranked file.
type SeedFile struct {
	Path string
	Line int
	// Lead marks a weaker match.
	Lead bool
}

// MaxSeed is the most files put in a task's prompt.
const MaxSeed = 8

// safePath reports whether every rune of p is in a conservative set. A path
// with a space, quote, control or markup character is left out of a prompt:
// file names are the repository's text, and a prompt is not the place for it.
func safePath(p string) bool {
	if p == "" || len(p) > 200 {
		return false
	}
	for _, r := range p {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.', r == '_', r == '-', r == '/', r == '@', r == '+':
		default:
			return false
		}
	}
	return !strings.Contains(p, "..")
}

// SeedLine renders the seed for a prompt, strong matches first, or "" when no
// file is safe to name.
func SeedLine(files []SeedFile) string {
	var strong, weak []string
	for _, f := range files {
		if !safePath(f.Path) {
			continue
		}
		ref := f.Path
		if f.Line > 0 {
			ref = fmt.Sprintf("%s:%d", f.Path, f.Line)
		}
		if f.Lead {
			weak = append(weak, ref)
		} else {
			strong = append(strong, ref)
		}
	}
	list := append(strong, weak...)
	if len(list) > MaxSeed {
		list = list[:MaxSeed]
	}
	if len(list) == 0 {
		return ""
	}
	return "Files ranked most likely for this goal, best first (computed before you started; start with these instead of searching): " + strings.Join(list, ", ") + "."
}

// WithSeed puts the seed line in front of the report contract of a task's
// prompt and, when a strong match was found, takes one round off its budget:
// the search it replaces. A task with no seed line is returned unchanged.
func WithSeed(t Task, line string, strong bool) Task {
	if line == "" {
		return t
	}
	if i := strings.LastIndex(t.Prompt, reportContract); i >= 0 {
		t.Prompt = t.Prompt[:i] + "\n\n" + line + t.Prompt[i:]
	} else {
		t.Prompt += "\n\n" + line
	}
	if strong && t.Budget > 2 {
		t.Budget--
	}
	return t
}

// Survey references whose value depends on what the ranking found.
const (
	SurveyTests = "tests"
	SurveyDocs  = "docs and config"
)

// DropUnsupported removes the tests and docs survey tasks when a ranking that
// found files found none of that kind. Any other task is kept. Reindexing
// keeps Index equal to position.
func DropUnsupported(tasks []Task, files []SeedFile) []Task {
	var hasTest, hasDoc bool
	for _, f := range files {
		p := strings.ToLower(f.Path)
		if isTestPath(p) {
			hasTest = true
		}
		if isDocPath(p) {
			hasDoc = true
		}
	}
	out := tasks[:0:0]
	for _, t := range tasks {
		if (t.Reference == SurveyTests && !hasTest) || (t.Reference == SurveyDocs && !hasDoc) {
			continue
		}
		t.Index = len(out)
		out = append(out, t)
	}
	return out
}

func isTestPath(p string) bool {
	return strings.Contains(p, "_test.") || strings.Contains(p, ".test.") || strings.Contains(p, ".spec.") ||
		strings.Contains(p, "/test/") || strings.Contains(p, "/tests/") || strings.HasPrefix(p, "test/") ||
		strings.HasPrefix(p, "tests/") || strings.Contains(p, "/test_") || strings.HasPrefix(p, "test_")
}

func isDocPath(p string) bool {
	for _, ext := range []string{".md", ".mdx", ".rst", ".toml", ".yaml", ".yml", ".json", ".ini", ".cfg"} {
		if strings.HasSuffix(p, ext) {
			return true
		}
	}
	return strings.HasPrefix(p, "docs/") || strings.Contains(p, "/docs/") || strings.Contains(p, "config")
}
