package quality

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/kanban"
	"github.com/vulnetix/belai/internal/testdetect"
	"github.com/vulnetix/belai/internal/testrun"
)

// The tests in this file each pin a boundary or branch that a gremlins
// survivor showed no other test observed. The comment on each names the
// mutant it kills.

// uniq stops at exactly max entries (quality.go uniq: len(out) >= max).
func TestUniqStopsAtExactlyMax(t *testing.T) {
	if got := uniq([]string{"a", "b", "c", "d"}, 2); strings.Join(got, ",") != "a,b" {
		t.Fatalf("uniq(_, 2) = %v, want [a b]", got)
	}
	var out strings.Builder
	for i := range maxTests + 5 {
		fmt.Fprintf(&out, "--- FAIL: Test%d (0.00s)\n", i)
	}
	if got := FailedTests(out.String()); len(got) != maxTests {
		t.Fatalf("FailedTests kept %d names, want the cap of %d", len(got), maxTests)
	}
}

// Every match is read, not only the first (FindAllStringSubmatch(_, -1) in
// FailedPackages and NoTestFiles).
func TestParsersReadEveryMatch(t *testing.T) {
	out := "FAIL\texample.com/m/a\t0.01s\nFAIL\texample.com/m/b\t0.01s\n" +
		"?   \texample.com/m/x\t[no test files]\n?   \texample.com/m/y\t[no test files]\n"
	if got := FailedPackages(out); strings.Join(got, ",") != "example.com/m/a,example.com/m/b" {
		t.Fatalf("FailedPackages = %v", got)
	}
	if got := NoTestFiles(out); strings.Join(got, ",") != "example.com/m/x,example.com/m/y" {
		t.Fatalf("NoTestFiles = %v", got)
	}
}

// 0% and 100% are both valid coverages (pct < 0 || pct > 100), and the list
// stops at exactly 500 packages (len(out) >= 500).
func TestCoveragesBounds(t *testing.T) {
	out := "ok  \texample.com/m/none\t0.01s\tcoverage: 0.0% of statements\n" +
		"ok  \texample.com/m/full\t0.01s\tcoverage: 100.0% of statements\n" +
		"ok  \texample.com/m/over\t0.01s\tcoverage: 100.1% of statements\n"
	got := Coverages(out)
	if len(got) != 2 || got[0] != (Cover{Package: "example.com/m/full", Percent: 100}) || got[1] != (Cover{Package: "example.com/m/none", Percent: 0}) {
		t.Fatalf("Coverages = %+v, want full at 100 and none at 0, over dropped", got)
	}

	var many strings.Builder
	for i := range 510 {
		fmt.Fprintf(&many, "ok  \texample.com/m/p%03d\t0.01s\tcoverage: 50.0%% of statements\n", i)
	}
	if got := Coverages(many.String()); len(got) != 500 {
		t.Fatalf("Coverages kept %d packages, want the cap of 500", len(got))
	}
}

// Only suites that name an ecosystem or framework add one (s.Ecosystem != "",
// s.Framework != "").
func TestDetectToolingCollectsNamedEcosystemsAndFrameworks(t *testing.T) {
	suites := []testdetect.Suite{
		{Name: "go", Ecosystem: "go", Framework: "gotest"},
		{Name: "bare"},
		{Name: "js", Ecosystem: "node"},
	}
	got := DetectTooling(t.TempDir(), suites)
	if strings.Join(got.Ecosystems, ",") != "go,node" || strings.Join(got.Frameworks, ",") != "gotest" {
		t.Fatalf("tooling = %+v", got)
	}
}

// A bare `go test` (exactly two argv words) is a Go suite (len(r.Command) >= 2
// in both of Build's loops), and only a failed or timed-out Go suite records a
// failure (r.Status == Failed || r.Status == TimedOut).
func TestBuildReadsABareGoTestAndOnlyFailedGoSuites(t *testing.T) {
	out := "ok  \texample.com/m/a\t0.01s\tcoverage: 80.0% of statements\n"
	bare := testrun.Result{Suite: "go", Command: []string{"go", "test"}, Status: testrun.Passed, Output: out}
	rec := Build(commitA, time.Unix(0, 0), []testrun.Result{bare}, nil, t.TempDir())
	if !rec.CoverageMeasured || len(rec.Coverage) != 1 {
		t.Fatalf("a bare go test run was not parsed: %+v", rec)
	}
	if len(rec.Failing) != 0 {
		t.Fatalf("a passing go suite recorded a failure: %+v", rec.Failing)
	}

	timedOut := testrun.Result{Suite: "go", Command: []string{"go", "test"}, Status: testrun.TimedOut, Output: "--- FAIL: TestSlow (9.00s)\n"}
	rec = Build(commitA, time.Unix(0, 0), []testrun.Result{timedOut}, nil, t.TempDir())
	if len(rec.Failing) != 1 || strings.Join(rec.Failing[0].Tests, ",") != "TestSlow" {
		t.Fatalf("a timed-out go suite = %+v, want one failure naming TestSlow", rec.Failing)
	}
}

func seedByPrefix(seeds []Seed, prefix string) (Seed, bool) {
	for _, s := range seeds {
		if strings.HasPrefix(s.Finding, prefix) {
			return s, true
		}
	}
	return Seed{}, false
}

// A failure card names tests and packages only when there are some
// (len(f.Tests) > 0, len(f.Packages) > 0).
func TestFailureSeedListsOnlyWhatItHas(t *testing.T) {
	rec := Record{Commit: commitA, Suites: []SuiteResult{{Name: "go"}}, Failing: []Failure{{Suite: "go"}}}
	s, ok := seedByPrefix(Seeds(rec), PrefixFail)
	if !ok {
		t.Fatal("no failure seed")
	}
	if strings.Contains(s.Body, "failing tests:") || strings.Contains(s.Body, "failing packages:") {
		t.Fatalf("a failure with no detail lists empty detail:\n%s", s.Body)
	}

	rec.Failing = []Failure{{Suite: "go", Tests: []string{"TestA"}, Packages: []string{"example.com/m/a"}}}
	s, _ = seedByPrefix(Seeds(rec), PrefixFail)
	if !strings.Contains(s.Body, "failing tests: TestA\n") || !strings.Contains(s.Body, "failing packages: example.com/m/a\n") {
		t.Fatalf("a failure with detail does not list it:\n%s", s.Body)
	}
}

// A package exactly at LowCoverage is not low (c.Percent < LowCoverage); the
// low list is ordered by percent and then by name; and no coverage or
// untested card is filed when there is nothing to list (len(low) > 0,
// len(rec.Untested) > 0).
func TestCoverageSeeds(t *testing.T) {
	rec := Record{Commit: commitA, Suites: []SuiteResult{{Name: "go"}}, CoverageMeasured: true, Coverage: []Cover{
		{Package: "example.com/m/edge", Percent: LowCoverage},
		{Package: "example.com/m/high", Percent: 95},
	}}
	seeds := Seeds(rec)
	if _, ok := seedByPrefix(seeds, PrefixCover); ok {
		t.Fatal("a coverage card was filed with no package under the threshold")
	}
	if _, ok := seedByPrefix(seeds, PrefixUntested); ok {
		t.Fatal("an untested card was filed with no untested package")
	}

	rec.Coverage = []Cover{
		{Package: "example.com/m/c", Percent: 30},
		{Package: "example.com/m/b", Percent: 10},
		{Package: "example.com/m/a", Percent: 30},
		{Package: "example.com/m/edge", Percent: LowCoverage},
	}
	s, ok := seedByPrefix(Seeds(rec), PrefixCover)
	if !ok {
		t.Fatal("no coverage card for packages under the threshold")
	}
	want := "example.com/m/b: 10.0%\nexample.com/m/a: 30.0%\nexample.com/m/c: 30.0%\n"
	if !strings.Contains(s.Body, want) {
		t.Fatalf("low packages not ordered by percent then name:\n%s", s.Body)
	}
	if strings.Contains(s.Body, "example.com/m/edge") {
		t.Fatalf("a package at exactly the threshold was listed as low:\n%s", s.Body)
	}
}

// Only the coverage category card is priority 1 (c.name == "coverage").
func TestCategoryPriorities(t *testing.T) {
	rec := Record{Commit: commitA, Suites: []SuiteResult{{Name: "go"}}}
	var sawCoverage bool
	for _, s := range Seeds(rec) {
		if !strings.HasPrefix(s.Finding, PrefixCategory) {
			continue
		}
		want := 0
		if strings.HasPrefix(s.Finding, PrefixCategory+"coverage:") {
			want, sawCoverage = 1, true
		}
		if s.Priority != want {
			t.Errorf("%s priority = %d, want %d", s.Finding, s.Priority, want)
		}
	}
	if !sawCoverage {
		t.Fatal("an unmeasured repository filed no coverage card")
	}
}

// The mutation card's task and the facts lines follow what is configured
// (len(rec.Tooling.Mutation) > 0, and each len(...) > 0 / != "" in
// toolingFacts).
func TestToolingFactsAndMutationTask(t *testing.T) {
	if got := mutationTask(Record{}); !strings.HasPrefix(got, "No mutation testing tool is configured.") {
		t.Fatalf("mutationTask with no markers = %q", got)
	}
	if got := mutationTask(Record{Tooling: Tooling{Mutation: []string{".gremlins.yaml"}}}); !strings.HasPrefix(got, "A mutation testing tool is already configured") {
		t.Fatalf("mutationTask with a marker = %q", got)
	}

	if got := toolingFacts(Record{}); got != "" {
		t.Fatalf("toolingFacts of an empty record = %q, want nothing", got)
	}
	rec := Record{
		Suites:  []SuiteResult{{Name: "go"}, {Name: "e2e"}},
		Tooling: Tooling{Ecosystems: []string{"go"}, Frameworks: []string{"gotest"}, Mutation: []string{".gremlins.yaml"}},
	}
	want := "ecosystems: go\nframeworks: gotest\nsuites: e2e, go\nmutation tool markers: .gremlins.yaml\n"
	if got := toolingFacts(rec); got != want {
		t.Fatalf("toolingFacts = %q, want %q", got, want)
	}
}

// lastNumber reads the last summary, not the first (lint.go
// FindAllStringSubmatch(_, -1)).
func TestLastNumberReadsTheLastSummary(t *testing.T) {
	if got := CountFindings("ruff", "Found 3 errors.\nFound 12 errors.\n"); got != 12 {
		t.Fatalf("CountFindings = %d, want the last summary's 12", got)
	}
}

// A failed Go gate with no test names notes only the exit code
// (gateverify.go len(tests) > 0).
func TestJudgeGateFailedWithoutTestNames(t *testing.T) {
	r := testrun.Result{Command: []string{"go", "test", "./x"}, Status: testrun.Failed, ExitCode: 2, Output: "FAIL\texample.com/m/x [build failed]\n"}
	if _, note, _ := JudgeGate(kanban.Gate{}, r); note != "exit 2" {
		t.Fatalf("note = %q, want %q", note, "exit 2")
	}
}

// Cards name the commit by its first 12 characters (len(short) > 12 in Seeds
// and LintSeeds).
func TestSeedsNameTheShortCommit(t *testing.T) {
	short := commitA[:12]
	for _, s := range Seeds(Record{Commit: commitA, Suites: []SuiteResult{{Name: "go"}}}) {
		if !strings.Contains(s.Body, short+"\n") || strings.Contains(s.Body, commitA) {
			t.Fatalf("%s does not name the commit as %s:\n%s", s.Finding, short, s.Body)
		}
	}
	lint := LintSeeds(LintRecord{Commit: commitA, Tools: []LintResult{{Tool: "ruff", Drift: true}}})
	if len(lint) != 1 || !strings.Contains(lint[0].Body, "seen at "+short+"\n") {
		t.Fatalf("lint seed does not name the commit as %s: %+v", short, lint)
	}
}

// Build lists coverage by package name whatever order the suites reported it
// in (the sort after the covers map in Build).
func TestBuildOrdersCoverageByPackage(t *testing.T) {
	results := []testrun.Result{
		{Suite: "one", Command: []string{"go", "test"}, Status: testrun.Passed, Output: "ok  \texample.com/m/c\t0.01s\tcoverage: 70.0% of statements\n"},
		{Suite: "two", Command: []string{"go", "test"}, Status: testrun.Passed, Output: "ok  \texample.com/m/a\t0.01s\tcoverage: 70.0% of statements\n"},
		{Suite: "three", Command: []string{"go", "test"}, Status: testrun.Passed, Output: "ok  \texample.com/m/b\t0.01s\tcoverage: 70.0% of statements\n"},
	}
	rec := Build(commitA, time.Unix(0, 0), results, nil, t.TempDir())
	var got []string
	for _, c := range rec.Coverage {
		got = append(got, c.Package)
	}
	if strings.Join(got, ",") != "example.com/m/a,example.com/m/b,example.com/m/c" {
		t.Fatalf("coverage order = %v", got)
	}
}
