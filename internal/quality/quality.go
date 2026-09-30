// Package quality turns a test suite run into facts, ties them to a commit
// and composes the seed cards a delivery scout works from.
//
// Everything here is harness-computed. A suite's output is raw process text
// and is never stored or shown to a model: the parsers keep only identifiers
// (test names, package paths) and numbers (a coverage percentage, an exit
// code), cleaned and capped. A record is the run's facts for one commit, and
// its existence for a commit is what tells a later launch not to run the
// suites again.
package quality

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/vulnetix/belai/internal/testdetect"
	"github.com/vulnetix/belai/internal/testrun"
)

// Dir is where records live, relative to the repository root.
const Dir = ".vulnetix/belai/quality"

// RecordVersion is the record format.
const RecordVersion = 1

// Bounds on what a record keeps.
const (
	maxTests     = 40
	maxPackages  = 60
	maxIdentLen  = 100
	keepRecords  = 20
	maxRecordLen = 1 << 20
)

// SuiteResult is one suite's outcome.
type SuiteResult struct {
	Name       string   `json:"name"`
	Command    []string `json:"command"`
	Status     string   `json:"status"`
	ExitCode   int      `json:"exit_code"`
	DurationMS int64    `json:"duration_ms"`
}

// Failure is one failing suite with the tests and packages it named.
type Failure struct {
	Suite    string   `json:"suite"`
	Tests    []string `json:"tests,omitempty"`
	Packages []string `json:"packages,omitempty"`
}

// Cover is one package's statement coverage.
type Cover struct {
	Package string  `json:"package"`
	Percent float64 `json:"percent"`
}

// Tooling is what marker files say the repository already has.
type Tooling struct {
	Ecosystems []string `json:"ecosystems,omitempty"`
	Frameworks []string `json:"frameworks,omitempty"`
	Mutation   []string `json:"mutation,omitempty"` // marker files of a mutation tool
}

// Record is the facts of one quality run at one commit.
type Record struct {
	Version  int           `json:"version"`
	Commit   string        `json:"commit"`
	At       string        `json:"at"`
	Suites   []SuiteResult `json:"suites"`
	Failing  []Failure     `json:"failing,omitempty"`
	Coverage []Cover       `json:"coverage,omitempty"`
	// CoverageMeasured is true when a suite reported coverage, so a package
	// missing from Coverage really has none. Untested lists packages a suite
	// reported as having no test files; it is only meaningful when
	// CoverageMeasured is true.
	CoverageMeasured bool     `json:"coverage_measured"`
	Untested         []string `json:"untested,omitempty"`
	Tooling          Tooling  `json:"tooling"`
}

// Ran reports whether any suite actually ran. A run in which nothing ran
// (every suite denied, or no binary) is not a result and is not recorded.
func (r Record) Ran() bool {
	for _, s := range r.Suites {
		switch s.Status {
		case string(testrun.Passed), string(testrun.Failed), string(testrun.TimedOut):
			return true
		}
	}
	return false
}

var (
	failTestRE = regexp.MustCompile(`(?m)^\s*--- FAIL: (\S+)`)
	failPkgRE  = regexp.MustCompile(`(?m)^FAIL\s+(\S+)`)
	coverRE    = regexp.MustCompile(`(?m)^ok\s+(\S+)\s+\S+\s+coverage: ([0-9]+(?:\.[0-9]+)?)% of statements`)
	noTestsRE  = regexp.MustCompile(`(?m)^\?\s+(\S+)\s+\[no test files\]`)
	identBad   = regexp.MustCompile(`[^A-Za-z0-9._/#@:+-]+`)
)

func ident(s string) string {
	s = identBad.ReplaceAllString(strings.TrimSpace(s), "_")
	if len(s) > maxIdentLen {
		s = s[:maxIdentLen]
	}
	return strings.Trim(s, "_")
}

func uniq(in []string, max int) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if s = ident(s); s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
			if len(out) >= max {
				break
			}
		}
	}
	return out
}

// FailedTests returns the names of failing tests in Go test output.
func FailedTests(output string) []string {
	var all []string
	for _, m := range failTestRE.FindAllStringSubmatch(output, -1) {
		all = append(all, m[1])
	}
	return uniq(all, maxTests)
}

// FailedPackages returns the packages Go test output reports as FAIL.
func FailedPackages(output string) []string {
	var all []string
	for _, m := range failPkgRE.FindAllStringSubmatch(output, -1) {
		all = append(all, m[1])
	}
	return uniq(all, maxPackages)
}

// Coverages returns each package's statement coverage from Go test output.
func Coverages(output string) []Cover {
	var out []Cover
	seen := map[string]bool{}
	for _, m := range coverRE.FindAllStringSubmatch(output, -1) {
		p := ident(m[1])
		pct, err := strconv.ParseFloat(m[2], 64)
		if p == "" || err != nil || pct < 0 || pct > 100 || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, Cover{Package: p, Percent: pct})
		if len(out) >= 500 {
			break
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Package < out[j].Package })
	return out
}

// NoTestFiles returns the packages Go test output says have no test files.
func NoTestFiles(output string) []string {
	var all []string
	for _, m := range noTestsRE.FindAllStringSubmatch(output, -1) {
		all = append(all, m[1])
	}
	sort.Strings(all)
	return uniq(all, 500)
}

// mutationMarkers are files whose presence says a mutation testing tool is
// set up. Presence only; no file is read.
var mutationMarkers = []string{
	"stryker.conf.js", "stryker.conf.json", "stryker.conf.mjs", "stryker.config.json", "stryker.config.mjs",
	"mutants.toml", ".cargo/mutants.toml", "mutmut_config.py", ".mutmut-cache", "cosmic-ray.toml", "pitest.xml",
}

// DetectTooling reads marker-file presence for the suites at root.
func DetectTooling(root string, suites []testdetect.Suite) Tooling {
	var t Tooling
	eco, fw := map[string]bool{}, map[string]bool{}
	for _, s := range suites {
		if s.Ecosystem != "" {
			eco[s.Ecosystem] = true
		}
		if s.Framework != "" {
			fw[s.Framework] = true
		}
	}
	t.Ecosystems, t.Frameworks = keys(eco), keys(fw)
	for _, m := range mutationMarkers {
		if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(m))); err == nil {
			t.Mutation = append(t.Mutation, m)
		}
	}
	return t
}

func keys(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Build turns a plan's results into a record for commit. Only Go output is
// parsed for test, package and coverage facts; every other suite contributes
// its status and exit code.
func Build(commit string, now time.Time, results []testrun.Result, suites []testdetect.Suite, root string) Record {
	rec := Record{Version: RecordVersion, Commit: strings.ToLower(commit), At: now.UTC().Format(time.RFC3339), Tooling: DetectTooling(root, suites)}
	covers := map[string]Cover{}
	untested := map[string]bool{}
	for _, r := range results {
		rec.Suites = append(rec.Suites, SuiteResult{
			Name: ident(r.Suite), Command: append([]string(nil), r.Command...), Status: string(r.Status),
			ExitCode: r.ExitCode, DurationMS: r.Duration.Milliseconds(),
		})
		isGo := len(r.Command) >= 2 && r.Command[0] == "go" && r.Command[1] == "test"
		if !isGo {
			continue
		}
		if r.Status == testrun.Failed || r.Status == testrun.TimedOut {
			f := Failure{Suite: ident(r.Suite), Tests: FailedTests(r.Output), Packages: FailedPackages(r.Output)}
			rec.Failing = append(rec.Failing, f)
		}
		for _, c := range Coverages(r.Output) {
			covers[c.Package] = c
			rec.CoverageMeasured = true
		}
		for _, p := range NoTestFiles(r.Output) {
			untested[p] = true
			rec.CoverageMeasured = true
		}
	}
	// A non-Go suite that failed still names a failure, without detail.
	for _, r := range results {
		isGo := len(r.Command) >= 2 && r.Command[0] == "go" && r.Command[1] == "test"
		if !isGo && (r.Status == testrun.Failed || r.Status == testrun.TimedOut) {
			rec.Failing = append(rec.Failing, Failure{Suite: ident(r.Suite)})
		}
	}
	for _, c := range covers {
		rec.Coverage = append(rec.Coverage, c)
	}
	sort.Slice(rec.Coverage, func(i, j int) bool { return rec.Coverage[i].Package < rec.Coverage[j].Package })
	rec.Untested = keys(untested)
	return rec
}

// ReadRecord returns the record for commit under root, if one exists and
// parses. A record for another commit is never returned.
func ReadRecord(root, commit string) (Record, bool) {
	commit = strings.ToLower(strings.TrimSpace(commit))
	if !validCommit(commit) {
		return Record{}, false
	}
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(Dir), commit+".json"))
	if err != nil || len(data) > maxRecordLen {
		return Record{}, false
	}
	var rec Record
	if json.Unmarshal(data, &rec) != nil || rec.Version != RecordVersion || rec.Commit != commit {
		return Record{}, false
	}
	return rec, true
}

// WriteRecord writes rec under root, whole or not at all, and drops the
// oldest records beyond the last few. It refuses a symlinked directory.
func WriteRecord(root string, rec Record) error {
	if !validCommit(rec.Commit) {
		return fmt.Errorf("quality: %q is not a commit id", rec.Commit)
	}
	dir := filepath.Join(root, filepath.FromSlash(Dir))
	for _, d := range []string{filepath.Join(root, ".vulnetix"), filepath.Join(root, ".vulnetix", "belai"), dir} {
		if fi, err := os.Lstat(d); err == nil && fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("quality: %s is a symlink", d)
		}
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
	}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(dir, rec.Commit+".json")
	tmp := path + ".part"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	prune(dir)
	return nil
}

func prune(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	type rec struct {
		name string
		mod  time.Time
	}
	var recs []rec
	for _, e := range entries {
		if !e.Type().IsRegular() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		if fi, err := e.Info(); err == nil {
			recs = append(recs, rec{e.Name(), fi.ModTime()})
		}
	}
	if len(recs) <= keepRecords {
		return
	}
	sort.Slice(recs, func(i, j int) bool { return recs[i].mod.After(recs[j].mod) })
	for _, r := range recs[keepRecords:] {
		_ = os.Remove(filepath.Join(dir, r.name))
	}
}

var commitRE = regexp.MustCompile(`^[0-9a-f]{40}$`)

func validCommit(s string) bool { return commitRE.MatchString(s) }

// Seed is one card the harness files for the delivery scout.
type Seed struct {
	// Finding is the stable id: kind and a hash of what it is about.
	Finding  string
	Title    string
	Body     string
	Priority int
}

// Finding id prefixes. Failure, coverage and untested seeds are closed when
// their subject goes away; category seeds are filed once per repository shape.
const (
	PrefixFail     = "quality:fail:"
	PrefixCover    = "quality:cover:"
	PrefixUntested = "quality:untested:"
	PrefixCategory = "quality:cat:"
	Prefix         = "quality:"
)

func hash8(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:4])
}

// lowCoverage is the percentage under which a package is worth a card.
const lowCoverage = 60.0

// Seeds composes the cards a record calls for, most urgent first. Every line
// of every body is a harness fact or a harness constant.
func Seeds(rec Record) []Seed {
	var out []Seed
	short := rec.Commit
	if len(short) > 12 {
		short = short[:12]
	}
	for _, f := range rec.Failing {
		var b strings.Builder
		fmt.Fprintf(&b, "focus: a failing suite, seen at %s\nsuite: %s\n", short, f.Suite)
		if len(f.Tests) > 0 {
			fmt.Fprintf(&b, "failing tests: %s\n", strings.Join(head(f.Tests, 15), ", "))
		}
		if len(f.Packages) > 0 {
			fmt.Fprintf(&b, "failing packages: %s\n", strings.Join(head(f.Packages, 10), ", "))
		}
		b.WriteString(instruction("Decide whether the code or the test is wrong for each failure, with the output as evidence, and hand off one build task per cause."))
		out = append(out, Seed{
			Finding:  PrefixFail + trunc(f.Suite, 16) + ":" + hash8(f.Suite, strings.Join(f.Tests, ","), strings.Join(f.Packages, ",")),
			Title:    "[quality] " + f.Suite + " suite is failing",
			Body:     b.String(),
			Priority: 3,
		})
	}
	if rec.CoverageMeasured {
		var low []Cover
		for _, c := range rec.Coverage {
			if c.Percent < lowCoverage {
				low = append(low, c)
			}
		}
		sort.Slice(low, func(i, j int) bool {
			if low[i].Percent != low[j].Percent {
				return low[i].Percent < low[j].Percent
			}
			return low[i].Package < low[j].Package
		})
		if len(low) > 0 {
			var b strings.Builder
			fmt.Fprintf(&b, "focus: packages under %.0f%% statement coverage, measured at %s\n", lowCoverage, short)
			var names []string
			for _, c := range head2(low, 10) {
				fmt.Fprintf(&b, "%s: %.1f%%\n", c.Package, c.Percent)
				names = append(names, c.Package)
			}
			b.WriteString(instruction("Read the least covered packages, find the business rules and edge cases no test exercises, and hand off one build task per package with the cases to cover."))
			out = append(out, Seed{
				Finding: PrefixCover + hash8(names...), Title: "[quality] raise coverage of the least covered packages",
				Body: b.String(), Priority: 2,
			})
		}
		if len(rec.Untested) > 0 {
			var b strings.Builder
			fmt.Fprintf(&b, "focus: packages with no test files, seen at %s\n", short)
			for _, p := range head(rec.Untested, 15) {
				fmt.Fprintf(&b, "%s\n", p)
			}
			b.WriteString(instruction("Pick the packages that hold real logic (skip generated code and thin mains), and hand off one build task each that adds a first test file."))
			out = append(out, Seed{
				Finding: PrefixUntested + hash8(rec.Untested...), Title: "[quality] add tests to packages that have none",
				Body: b.String(), Priority: 1,
			})
		}
	}
	shape := hash8(strings.Join(rec.Tooling.Ecosystems, ","), strings.Join(rec.Tooling.Frameworks, ","), suiteNames(rec), strings.Join(rec.Tooling.Mutation, ","))
	facts := toolingFacts(rec)
	cats := []struct{ name, title, task string }{
		{"properties", "[quality] find where property-based tests fit", "Look for parsers, encoders, sorters, state machines and validators tested only by examples. Hand off one build task per function with the invariant to check (round trip, idempotence, ordering, no panic on any input)."},
		{"fixtures", "[quality] improve mocks, stubs and fixtures", "Look for tests that hit the network, the clock, the file system or a real service, copy the same setup, or hard-code data. Hand off tasks that add fakes, shared fixtures or builders where they remove duplication or flakiness."},
		{"contracts", "[quality] add contract tests at the seams", "Look for seams whose shape other code or users depend on: command-line flags and output, HTTP or wire formats, file formats, plugin and provider interfaces. Hand off one build task per seam with a test that pins its contract, so a breaking change fails a test."},
		{"mutation", "[quality] check the tests with mutation testing", mutationTask(rec)},
		{"docs", "[quality] check the docs, specs and site against the code", "Compare specs, PRDs, design notes, README, docs/ and any static site prose with the code they describe. Flags, defaults, limits, commands, paths and messages that no longer match are candidates; say which side is out of date, then hand off one build task per discrepancy."},
	}
	if len(rec.Suites) == 0 {
		// Nothing to test with: the useful cards are getting a suite and the docs.
		cats = []struct{ name, title, task string }{
			{"suite", "[quality] set up a test suite", "The harness found no test suite to run. Work out the project's language and usual test tooling, and hand off tasks that add a runnable suite, a first test for the most important behaviour, and the command that runs it."},
			cats[len(cats)-1],
		}
	} else if !rec.CoverageMeasured {
		cats = append(cats, struct{ name, title, task string }{"coverage", "[quality] measure test coverage", "The harness could not measure coverage for this repository. Find the project's coverage tool and command, run it if it is cheap, and hand off tasks for the weakest areas, or a task that adds coverage reporting."})
	}
	for _, c := range cats {
		var b strings.Builder
		fmt.Fprintf(&b, "focus: %s\nseen at: %s\n%s", c.name, short, facts)
		b.WriteString(instruction(c.task))
		pri := 0
		if c.name == "coverage" {
			pri = 1
		}
		out = append(out, Seed{Finding: PrefixCategory + c.name + ":" + shape, Title: c.title, Body: b.String(), Priority: pri})
	}
	return out
}

func mutationTask(rec Record) string {
	if len(rec.Tooling.Mutation) > 0 {
		return "A mutation testing tool is already configured (see the marker files above). Run it on the package with the most logic, list the surviving mutants, and hand off one build task per cluster of survivors with the test that would kill them."
	}
	return "No mutation testing tool is configured. Pick the ecosystem's usual one, try it on one well-tested package, and hand off tasks that add it to the project and kill the first surviving mutants."
}

func instruction(s string) string { return "task: " + s + "\n" }

func suiteNames(rec Record) string {
	var n []string
	for _, s := range rec.Suites {
		n = append(n, s.Name)
	}
	sort.Strings(n)
	return strings.Join(n, ",")
}

func toolingFacts(rec Record) string {
	var b strings.Builder
	if len(rec.Tooling.Ecosystems) > 0 {
		fmt.Fprintf(&b, "ecosystems: %s\n", strings.Join(rec.Tooling.Ecosystems, ", "))
	}
	if len(rec.Tooling.Frameworks) > 0 {
		fmt.Fprintf(&b, "frameworks: %s\n", strings.Join(rec.Tooling.Frameworks, ", "))
	}
	if n := suiteNames(rec); n != "" {
		fmt.Fprintf(&b, "suites: %s\n", strings.ReplaceAll(n, ",", ", "))
	}
	if len(rec.Tooling.Mutation) > 0 {
		fmt.Fprintf(&b, "mutation tool markers: %s\n", strings.Join(rec.Tooling.Mutation, ", "))
	}
	if rec.CoverageMeasured {
		fmt.Fprintf(&b, "packages with coverage: %d, without test files: %d\n", len(rec.Coverage), len(rec.Untested))
	}
	return b.String()
}

func head(s []string, n int) []string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func head2(s []Cover, n int) []Cover {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func trunc(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// Present is the set of failure, coverage and untested finding ids the record
// still reports, for closing the cards whose subject has gone.
func Present(rec Record) map[string]bool {
	out := map[string]bool{}
	for _, s := range Seeds(rec) {
		out[s.Finding] = true
	}
	return out
}

// Covered says which closable finding ids the record looked for: failures
// whenever a suite ran, coverage and untested packages only when coverage was
// measured. Category cards are never closed by a record.
func Covered(rec Record) func(finding string) bool {
	return func(f string) bool {
		switch {
		case strings.HasPrefix(f, PrefixFail):
			return rec.Ran()
		case strings.HasPrefix(f, PrefixCover), strings.HasPrefix(f, PrefixUntested):
			return rec.CoverageMeasured
		}
		return false
	}
}
