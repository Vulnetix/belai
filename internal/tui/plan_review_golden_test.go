package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/viewport"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/vulnetix/belai/internal/plans"
)

// goldenPlanReviewDoc is the fixed structured plan the golden fixture renders,
// covering every section the pane presents: title, summary, steps with files
// and verify, tests, assumptions, risks, and a diff against a previous
// revision.
func goldenPlanReviewDoc() plans.Doc {
	return plans.Doc{
		Title:   "Refactor the parser",
		Summary: "Split the parser into a lexer and a grammar.",
		Steps: []plans.Step{
			{N: 1, Text: "Extract a tokeniser module", Files: []string{"parser/lex.go", "parser/lex_test.go"}, Verify: "go test ./parser"},
			{N: 2, Text: "Rewrite the grammar in terms of tokens", Files: []string{"parser/grammar.go"}, Verify: "go test ./parser"},
		},
		Tests:       []string{"go test ./...", "go vet ./..."},
		Assumptions: []string{"The token vocabulary is stable."},
		Risks:       []string{"Error messages may change."},
	}
}

// planReviewGolden renders the review pane under the Ascii profile so the
// golden file is reviewable without escape bytes, exactly like the component
// row golden. Refresh with: UPDATE_GOLDEN=1 go test ./internal/tui/
func TestPlanReviewGolden(t *testing.T) {
	path := filepath.Join("testdata", "plan_review.golden")

	var got strings.Builder
	old := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.Ascii)
	defer lipgloss.SetColorProfile(old)

	for _, tc := range []struct {
		name   string
		prev   *plans.Doc
		show   bool
		edited bool
	}{
		{"plain", nil, false, false},
		{"edited", nil, false, true},
	} {
		_ = tc.edited
		a := New(Options{Workdir: t.TempDir()})
		a.width, a.height = 100, 40
		a.planReview = newPlanReviewState("golden-plan", "/tmp/golden-plan.md")
		a.planReview.doc = goldenPlanReviewDoc()
		if tc.prev != nil {
			a.planReview.prev = tc.prev
			a.planReview.showDiff = tc.show
		}
		if tc.edited {
			a.planReview.edited = true
		}
		w := a.planReviewWidth()
		h := a.planReviewHeight()
		a.planReview.vp = viewport.New(w, h)
		a.planReview.setContent()
		fmt.Fprintf(&got, "=== %s\n%s\n", tc.name, a.planReviewView())
	}

	// Diff view over a previous revision.
	a := New(Options{Workdir: t.TempDir()})
	a.width, a.height = 100, 40
	a.planReview = newPlanReviewState("golden-plan", "/tmp/golden-plan.md")
	a.planReview.doc = goldenPlanReviewDoc()
	prev := goldenPlanReviewDoc()
	prev.Steps[0].Text = "Extract a tokeniser package"
	a.planReview.prev = &prev
	a.planReview.showDiff = true
	w := a.planReviewWidth()
	h := a.planReviewHeight()
	a.planReview.vp = viewport.New(w, h)
	a.planReview.setContent()
	fmt.Fprintf(&got, "=== diff\n%s\n", a.planReviewView())

	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got.String()), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Log("golden updated")
		return
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden (run with UPDATE_GOLDEN=1 to create): %v", err)
	}
	if got.String() != string(want) {
		gotLines := strings.Split(got.String(), "\n")
		wantLines := strings.Split(string(want), "\n")
		for i := 0; i < len(gotLines) || i < len(wantLines); i++ {
			g, w := "", ""
			if i < len(gotLines) {
				g = gotLines[i]
			}
			if i < len(wantLines) {
				w = wantLines[i]
			}
			if g != w {
				t.Errorf("line %d:\n  got  %q\n  want %q", i, g, w)
			}
		}
	}
}

// TestPlanReviewViewShowsStructuredSections pins the A4 contract: the pane
// presents the parsed sections (title, summary, steps, tests, assumptions),
// not the raw model message.
func TestPlanReviewViewShowsStructuredSections(t *testing.T) {
	a := New(Options{Workdir: t.TempDir()})
	a.width, a.height = 100, 40
	a.planReview = newPlanReviewState("plan", "/tmp/plan.md")
	a.planReview.doc = goldenPlanReviewDoc()
	w := a.planReviewWidth()
	h := a.planReviewHeight()
	a.planReview.vp = viewport.New(w, h)
	a.planReview.setContent()

	v := a.planReviewView()
	for _, want := range []string{"# Refactor the parser", "## Summary", "## Steps", "1. Extract a tokeniser module", "- Files: parser/lex.go", "- Verify: go test ./parser", "## Test Plan", "## Assumptions", "## Risks"} {
		if !strings.Contains(v, want) {
			t.Fatalf("review pane missing %q:\n%s", want, v)
		}
	}
}
