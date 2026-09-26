package plans

import (
	"path/filepath"
	"testing"

	"github.com/vulnetix/belai/internal/config"
)

func TestDetectHandoffMarkdownOnly(t *testing.T) {
	_, ok := DetectHandoff("notes.txt", "1. a\n2. b\n3. c", "")
	if ok {
		t.Fatal("non-markdown file must not be a plan")
	}
}

func TestDetectHandoffByPath(t *testing.T) {
	plansDir := config.ProjectPlansDir(t.TempDir())
	label := filepath.Join(plansDir, "rollout.md")
	facts, ok := DetectHandoff(label, "## Plan\n1. step one\n", plansDir)
	if !ok {
		t.Fatal("markdown under plans dir must be a plan")
	}
	if facts.Label != label {
		t.Errorf("label = %q, want %q", facts.Label, label)
	}
}

func TestDetectHandoffByBasename(t *testing.T) {
	facts, ok := DetectHandoff("rollout-plan.md", "1. a\n2. b\n", "")
	if !ok {
		t.Fatal("*plan*.md basename must be a plan")
	}
	if facts.Tasks != 2 {
		t.Errorf("tasks = %d, want 2", facts.Tasks)
	}
}

func TestDetectHandoffByTaskCount(t *testing.T) {
	body := "1. a\n2. b\n3. c\n"
	_, ok := DetectHandoff("roadmap.md", body, "")
	if !ok {
		t.Fatal("three numbered tasks must make a plan")
	}
}

func TestDetectHandoffBelowThreshold(t *testing.T) {
	body := "1. a\n2. b\n"
	_, ok := DetectHandoff("roadmap.md", body, "")
	if ok {
		t.Fatal("two tasks with no path/basename hint must not be a plan")
	}
}

func TestCountTasks(t *testing.T) {
	body := `## Plan
1. numbered one
2) numbered two
- [x] done task
- [ ] todo task
### Step 3: heading step
not a step
`
	got := CountTasks(body)
	if got != 5 {
		t.Errorf("CountTasks = %d, want 5", got)
	}
}

func TestReferencedPaths(t *testing.T) {
	body := "Read `internal/foo.go` and `README.md`. Then check docs/bar.md. Also `VAR_NAME` is not a path."
	got := ReferencedPaths(body)
	want := []string{"internal/foo.go", "README.md", "docs/bar.md"}
	if len(got) != len(want) {
		t.Fatalf("paths = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("path[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestReferencedPathsDedupes(t *testing.T) {
	body := "`a.go` and `a.go`"
	got := ReferencedPaths(body)
	if len(got) != 1 || got[0] != "a.go" {
		t.Fatalf("got %v, want [a.go]", got)
	}
}
