package config

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/docparity"
)

func sp(s string) *string { return &s }

func TestReviewDefaults(t *testing.T) {
	for _, s := range []Settings{{}, {Review: &ReviewSettings{}}} {
		if !s.ReviewEnabled() || s.ReviewMaxDiffBytes() != 400<<10 || s.ReviewPostMode() != ReviewPostAsk || s.ReviewRubricFile() != "" {
			t.Fatalf("defaults wrong for %+v", s.Review)
		}
	}
}

func TestReviewDiffCapIsClamped(t *testing.T) {
	for in, want := range map[int]int{-5: 16, 0: 16, 15: 16, 16: 16, 400: 400, 2048: 2048, 9000: 2048} {
		s := Settings{Review: &ReviewSettings{MaxDiffKB: ip(in)}}
		if got := s.ReviewMaxDiffBytes(); got != want<<10 {
			t.Errorf("max_diff_kb %d -> %d bytes, want %d KiB", in, got, want)
		}
	}
}

func TestReviewUnknownPostIsDenyAndFailsValidation(t *testing.T) {
	s := Settings{Review: &ReviewSettings{Post: sp("yes please")}}
	if s.ReviewPostMode() != ReviewPostDeny {
		t.Fatalf("an unknown post value is %q, want deny", s.ReviewPostMode())
	}
	if err := ValidateSettings(s); err == nil || !strings.Contains(err.Error(), "review.post") {
		t.Fatalf("validation = %v", err)
	}
	for _, v := range ReviewPostValues {
		if err := ValidateSettings(Settings{Review: &ReviewSettings{Post: sp(strings.ToUpper(v))}}); err != nil {
			t.Errorf("post %q refused: %v", v, err)
		}
	}
	if err := ValidateSettings(Settings{Review: &ReviewSettings{RubricFile: sp("a\nb")}}); err == nil {
		t.Fatal("a rubric path with a newline was accepted")
	}
}

func TestReviewUserLayerSetsAnything(t *testing.T) {
	got := mergeReview(&ReviewSettings{Enabled: bp(false), MaxDiffKB: ip(32), Post: sp("deny")},
		&ReviewSettings{Enabled: bp(true), MaxDiffKB: ip(2048), Post: sp("allow"), RubricFile: sp("/home/u/rubric.txt")}, false)
	s := Settings{Review: got}
	if !s.ReviewEnabled() || s.ReviewMaxDiffBytes() != 2048<<10 || s.ReviewPostMode() != ReviewPostAllow || s.ReviewRubricFile() != "/home/u/rubric.txt" {
		t.Fatalf("user layer not honoured: %+v", got)
	}
}

func TestReviewProjectCanOnlyNarrow(t *testing.T) {
	user := &ReviewSettings{Enabled: bp(false), MaxDiffKB: ip(100), Post: sp("allow"), RubricFile: sp("/home/u/r.txt")}
	got := mergeReview(user, &ReviewSettings{Enabled: bp(true), MaxDiffKB: ip(2048), Post: sp("allow"), RubricFile: sp("/etc/passwd")}, true)
	s := Settings{Review: got}
	if s.ReviewEnabled() || s.ReviewMaxDiffBytes() != 100<<10 || s.ReviewPostMode() != ReviewPostAllow || s.ReviewRubricFile() != "/home/u/r.txt" {
		t.Fatalf("a project widened: %+v", got)
	}
	// Against the defaults a project still stores nothing that is not narrower.
	got = mergeReview(nil, &ReviewSettings{Enabled: bp(true), MaxDiffKB: ip(400), Post: sp("allow"), RubricFile: sp("x")}, true)
	if got.Enabled != nil || got.MaxDiffKB != nil || got.Post != nil || got.RubricFile != nil {
		t.Fatalf("a no-op or widening project value was stored: %+v", got)
	}
	got = mergeReview(nil, &ReviewSettings{Post: sp("ask")}, true)
	if got.Post != nil {
		t.Fatal("a project stored post=ask")
	}
	// Turning off, lowering and denying apply.
	got = mergeReview(user, &ReviewSettings{Enabled: bp(false), MaxDiffKB: ip(1), Post: sp("DENY")}, true)
	s = Settings{Review: got}
	if s.ReviewEnabled() || s.ReviewMaxDiffBytes() != 16<<10 || s.ReviewPostMode() != ReviewPostDeny {
		t.Fatalf("a project could not narrow: %+v", got)
	}
}

func TestReviewOverrideNarrowsOnly(t *testing.T) {
	user := Settings{Review: &ReviewSettings{Post: sp("allow"), MaxDiffKB: ip(1000)}}
	out := user.Override(Settings{Review: &ReviewSettings{Post: sp("allow"), MaxDiffKB: ip(2000), RubricFile: sp("x")}})
	if out.ReviewPostMode() != ReviewPostAllow || out.ReviewMaxDiffBytes() != 1000<<10 || out.ReviewRubricFile() != "" {
		t.Fatalf("Override: %+v", out.Review)
	}
	out = user.Override(Settings{Review: &ReviewSettings{Post: sp("deny"), Enabled: bp(false)}})
	if out.ReviewPostMode() != ReviewPostDeny || out.ReviewEnabled() {
		t.Fatalf("Override did not narrow: %+v", out.Review)
	}
}

func TestReviewJSONRoundTrip(t *testing.T) {
	var s Settings
	if err := json.Unmarshal([]byte(`{"review":{"enabled":false,"max_diff_kb":64,"post":"deny","rubric_file":"/r"}}`), &s); err != nil {
		t.Fatal(err)
	}
	if s.ReviewEnabled() || s.ReviewMaxDiffBytes() != 64<<10 || s.ReviewPostMode() != ReviewPostDeny || s.ReviewRubricFile() != "/r" {
		t.Fatalf("%+v", s.Review)
	}
}

func TestResolveReviewProjectLayer(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	t.Setenv("VULNETIX_WEB_URL", "http://127.0.0.1:1")
	workdir := t.TempDir()
	resolve := func() Effective {
		t.Helper()
		eff, err := Resolve(workdir, func(string) string { return "" }, Settings{})
		if err != nil {
			t.Fatal(err)
		}
		return eff
	}
	if err := SaveGlobal(Settings{Review: &ReviewSettings{Enabled: bp(false), MaxDiffKB: ip(100), Post: sp("allow")}}); err != nil {
		t.Fatal(err)
	}
	if err := SaveProject(workdir, Settings{Review: &ReviewSettings{Enabled: bp(true), MaxDiffKB: ip(2048), Post: sp("allow"), RubricFile: sp("/etc/shadow")}}); err != nil {
		t.Fatal(err)
	}
	eff := resolve()
	if eff.Settings.ReviewEnabled() || eff.Settings.ReviewMaxDiffBytes() != 100<<10 || eff.Settings.ReviewPostMode() != ReviewPostAllow || eff.Settings.ReviewRubricFile() != "" {
		t.Fatalf("project widened: %+v", eff.Settings.Review)
	}
	noted := false
	for _, n := range eff.Notes {
		if strings.Contains(n, "review.enabled") && strings.Contains(n, "review.max_diff_kb") && strings.Contains(n, "review.rubric_file") {
			noted = true
		}
	}
	if !noted {
		t.Fatalf("no note for the ignored project keys: %v", eff.Notes)
	}
	// A project denies posting even over a user's allow.
	if err := SaveProject(workdir, Settings{Review: &ReviewSettings{Post: sp("deny")}}); err != nil {
		t.Fatal(err)
	}
	if got := resolve(); got.Settings.ReviewPostMode() != ReviewPostDeny || got.Origin["review"] != SourceProject {
		t.Fatalf("a project could not deny posting: %+v", got.Settings.Review)
	}
}

// TestReviewPageStatesEachDefaultAndRange keeps docs/review.md equal to the
// bounds the code clamps to.
func TestReviewPageStatesEachDefaultAndRange(t *testing.T) {
	doc := docparity.Read(t, "docs/review.md")
	for _, want := range []string{
		"| `review.enabled` | on |",
		fmt.Sprintf("| `review.max_diff_kb` | %d | %d to %d |", DefaultReviewMaxDiffKB, MinReviewMaxDiffKB, MaxReviewMaxDiffKB),
		"| `review.post` | ask | ask, deny or allow |",
		"| `review.rubric_file` | none | a file path |",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("docs/review.md does not state %q", want)
		}
	}
}
