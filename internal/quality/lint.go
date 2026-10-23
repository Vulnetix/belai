package quality

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/vulnetix/belai/internal/lintdetect"
	"github.com/vulnetix/belai/internal/testrun"
)

// The lint sweep's facts. A linter run is raw process text, so, like a test
// run, none of it is stored or shown to a model: a record keeps the tool, its
// fixed argv, an exit code and a finding count read from the tool's own
// summary or location lines. The count is a number; no path, rule name or
// message is kept. A record is the run's facts for one commit, and its
// existence for a commit is what tells a later launch not to run the linters
// again.

// LintDir is where lint records live, relative to the repository root.
const LintDir = ".vulnetix/belai/lint"

// LintRecordVersion is the lint record format.
const LintRecordVersion = 1

// PrefixLint starts the finding id of every lint seed card.
const PrefixLint = "lint:"

// maxLintFindings caps a count, so a hostile output cannot make a number that
// is not a count.
const maxLintFindings = 99999

// LintResult is one linter's outcome.
type LintResult struct {
	Name       string   `json:"name"`
	Tool       string   `json:"tool"`
	Command    []string `json:"command"`
	Marker     string   `json:"marker,omitempty"`
	Status     string   `json:"status"`
	ExitCode   int      `json:"exit_code"`
	DurationMS int64    `json:"duration_ms"`
	// Findings is the count the tool reported, zero when it reported none or
	// its output could not be counted.
	Findings int `json:"findings"`
	// Drift says the repository does not meet the rule file: findings were
	// counted, or the tool exited with the code it uses for findings.
	Drift bool `json:"drift"`
}

// LintRecord is the facts of one lint run at one commit.
type LintRecord struct {
	Version int          `json:"version"`
	Commit  string       `json:"commit"`
	At      string       `json:"at"`
	Tools   []LintResult `json:"tools"`
}

// Ran reports whether any linter actually ran. A run in which nothing ran
// (every linter denied, or no binary) is not a result and is not recorded.
func (r LintRecord) Ran() bool {
	for _, t := range r.Tools {
		if t.ran() {
			return true
		}
	}
	return false
}

func (t LintResult) ran() bool {
	switch t.Status {
	case string(testrun.Passed), string(testrun.Failed), string(testrun.TimedOut):
		return true
	}
	return false
}

// Per-tool counts. Each reads one fixed shape of the tool's own output.
var (
	// golangci-lint, text format: path:line:col: message (linter).
	golangciRE = regexp.MustCompile(`(?m)^\S+\.go:\d+(?::\d+)?: `)
	// ruff: the closing "Found 12 errors." line, in every output format.
	ruffRE = regexp.MustCompile(`(?m)^Found (\d+) errors?\.`)
	// rubocop: the closing "3 files inspected, 12 offenses detected" line.
	rubocopRE = regexp.MustCompile(`(?m)(\d+) offenses? detected`)
	// cargo fmt --check: one "Diff in path:line:" header per hunk.
	rustfmtRE = regexp.MustCompile(`(?m)^Diff in \S+`)
	// clippy: a warning or error diagnostic header, "warning: ..." or
	// "error[E0432]: ...", minus the per-crate summaries below.
	clippyRE      = regexp.MustCompile(`(?m)^(?:warning|error)(?:\[\w+\])?: .+$`)
	clippySummary = regexp.MustCompile("generated \\d+ warning|could not compile|aborting due to|^warning: build failed")
	// eslint, stylish format: "✖ 12 problems (10 errors, 2 warnings)".
	eslintRE = regexp.MustCompile(`(?m)^\s*✖ (\d+) problems?`)
)

// CountFindings returns how many findings a linter's output reports, by the
// tool's fixed output shape, or zero when the shape is not found. The text is
// raw process output: only the count leaves this function.
func CountFindings(tool, output string) int {
	n := 0
	switch tool {
	case "golangci-lint":
		n = len(golangciRE.FindAllStringIndex(output, -1))
	case "ruff":
		n = lastNumber(ruffRE, output)
	case "rubocop":
		n = lastNumber(rubocopRE, output)
	case "rustfmt":
		n = len(rustfmtRE.FindAllStringIndex(output, -1))
	case "clippy":
		for _, l := range clippyRE.FindAllString(output, -1) {
			if !clippySummary.MatchString(l) {
				n++
			}
		}
	case "eslint":
		n = lastNumber(eslintRE, output)
	}
	if n > maxLintFindings {
		n = maxLintFindings
	}
	return n
}

// lastNumber reads the number of the last match: a summary line closes the
// output, so the last one is the total.
func lastNumber(re *regexp.Regexp, s string) int {
	all := re.FindAllStringSubmatch(s, -1)
	if len(all) == 0 {
		return 0
	}
	n, err := strconv.Atoi(all[len(all)-1][1])
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// BuildLint turns a plan's results into a record for commit. results are
// matched to linters by name; a linter with no result is left out.
func BuildLint(commit string, now time.Time, linters []lintdetect.Linter, results []testrun.Result) LintRecord {
	rec := LintRecord{Version: LintRecordVersion, Commit: strings.ToLower(commit), At: now.UTC().Format(time.RFC3339)}
	byName := map[string]lintdetect.Linter{}
	for _, l := range linters {
		byName[l.Name] = l
	}
	for _, r := range results {
		l, ok := byName[r.Suite]
		if !ok {
			continue
		}
		t := LintResult{
			Name: ident(l.Name), Tool: ident(l.Tool), Command: append([]string(nil), l.Command...), Marker: ident(l.Marker),
			Status: string(r.Status), ExitCode: r.ExitCode, DurationMS: r.Duration.Milliseconds(),
		}
		if r.Status == testrun.Passed || r.Status == testrun.Failed {
			t.Findings = CountFindings(l.Tool, r.Output)
			t.Drift = t.Findings > 0 || (r.Status == testrun.Failed && l.FindingsExit != 0 && r.ExitCode == l.FindingsExit)
		}
		rec.Tools = append(rec.Tools, t)
	}
	return rec
}

// ReadLintRecord returns the lint record for commit under root, if one exists
// and parses. A record for another commit is never returned.
func ReadLintRecord(root, commit string) (LintRecord, bool) {
	commit = strings.ToLower(strings.TrimSpace(commit))
	if !validCommit(commit) {
		return LintRecord{}, false
	}
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(LintDir), commit+".json"))
	if err != nil || len(data) > maxRecordLen {
		return LintRecord{}, false
	}
	var rec LintRecord
	if json.Unmarshal(data, &rec) != nil || rec.Version != LintRecordVersion || rec.Commit != commit {
		return LintRecord{}, false
	}
	return rec, true
}

// WriteLintRecord writes rec under root, whole or not at all, and drops the
// oldest records beyond the last few. It refuses a symlinked directory.
func WriteLintRecord(root string, rec LintRecord) error {
	if !validCommit(rec.Commit) {
		return fmt.Errorf("quality: %q is not a commit id", rec.Commit)
	}
	dir := filepath.Join(root, filepath.FromSlash(LintDir))
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

// LintSeeds composes the cards a lint record calls for, one for each linter
// that does not meet its rule file. Every line of every body is a harness fact
// or a harness constant. The finding id carries a hash of the tool and the
// count, so a card finished for one count does not hide the drift that comes
// back with another, and the same count on the same tool is filed once.
func LintSeeds(rec LintRecord) []Seed {
	short := rec.Commit
	if len(short) > 12 {
		short = short[:12]
	}
	var out []Seed
	for _, t := range rec.Tools {
		if !t.Drift {
			continue
		}
		title := "[lint] " + t.Tool + " reports findings"
		if t.Findings > 0 {
			title = fmt.Sprintf("[lint] %s reports %d findings", t.Tool, t.Findings)
		}
		var b strings.Builder
		fmt.Fprintf(&b, "focus: the code does not meet the lint rules checked in, seen at %s\n", short)
		fmt.Fprintf(&b, "tool: %s\ncommand: %s\n", t.Tool, strings.Join(t.Command, " "))
		if t.Marker != "" {
			fmt.Fprintf(&b, "rule file: %s\n", t.Marker)
		}
		if t.Findings > 0 {
			fmt.Fprintf(&b, "findings: %d\n", t.Findings)
		}
		b.WriteString(instruction("Run the linter or formatter in its autofix mode over the files it reports, fix what is left by hand, and change as little as possible. Never edit the rule file, add an ignore or disable a rule."))
		out = append(out, Seed{
			Finding:  PrefixLint + trunc(t.Name, 24) + ":" + hash8(t.Name, strconv.Itoa(t.Findings)),
			Title:    title,
			Body:     b.String(),
			Priority: 1,
		})
	}
	return out
}

// LintPresent is the set of finding ids the record still reports.
func LintPresent(rec LintRecord) map[string]bool {
	out := map[string]bool{}
	for _, s := range LintSeeds(rec) {
		out[s.Finding] = true
	}
	return out
}

// LintCovered says which lint finding ids the record looked for: those of a
// linter that ran this time. A linter that did not run (denied, not
// installed) or is no longer detected leaves its card alone.
func LintCovered(rec LintRecord) func(finding string) bool {
	ran := map[string]bool{}
	for _, t := range rec.Tools {
		if t.ran() && t.Status != string(testrun.TimedOut) {
			ran[trunc(t.Name, 24)] = true
		}
	}
	return func(f string) bool {
		name, _, ok := strings.Cut(strings.TrimPrefix(f, PrefixLint), ":")
		return ok && strings.HasPrefix(f, PrefixLint) && ran[name]
	}
}
