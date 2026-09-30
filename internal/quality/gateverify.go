package quality

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/vulnetix/belai/internal/kanban"
	"github.com/vulnetix/belai/internal/testdetect"
	"github.com/vulnetix/belai/internal/testrun"
)

// Verification of a card's acceptance gates. The harness runs each runnable
// gate's suite (an argv built from the detected-suite table, never from a
// model) and decides the gate by the exit code, with one rule added: a run in
// which nothing ran is not a pass. Only identifiers and numbers are kept from
// the output.

// VerifyDir is where verification records live, under Dir.
const VerifyDir = Dir + "/verify"

// VerificationVersion is the verification record format.
const VerificationVersion = 1

const keepVerifications = 50

var (
	noTestsToRunRE = regexp.MustCompile(`(?m)^ok\s+\S+\s+\S+\s+\[no tests to run\]`)
	okLineRE       = regexp.MustCompile(`(?m)^ok\s+\S+`)
	itemShortRE    = regexp.MustCompile(`^K-[0-9a-f]{6}$`)
)

// GateArgv builds the argv that decides a runnable gate. A Go suite narrows
// to the gate's package directory and test name; every other suite, and a Go
// gate that names neither, runs the suite's own command. dir and test were
// validated as plain identifiers when the gate was filed, and dir gets a ./
// prefix, so neither can be read as an option. It returns nil for a gate that
// is not runnable.
func GateArgv(s testdetect.Suite, g kanban.Gate) []string {
	if g.Kind != kanban.GateRunnable {
		return nil
	}
	if s.Ecosystem != "go" || (g.Dir == "" && g.Test == "") {
		return append([]string(nil), s.Command...)
	}
	argv := []string{"go", "test", "-count=1"}
	if g.Test != "" {
		argv = append(argv, "-run", "^"+g.Test+"$")
	}
	if g.Dir != "" {
		argv = append(argv, "./"+strings.TrimPrefix(g.Dir, "./"))
	} else {
		argv = append(argv, "./...")
	}
	return argv
}

func isGoTest(argv []string) bool {
	return len(argv) >= 2 && argv[0] == "go" && argv[1] == "test"
}

// JudgeGate decides one runnable gate from its run. ran is false when the
// harness could not run it at all (a deny or ask rule, no binary, no sandbox),
// which is not a verdict on the work. A zero exit is met unless the run shows
// that nothing was tested: a Go test name that selected no tests, or a
// package with no test files, is unmet, so an absence check can never pass
// vacuously. The note holds harness constants and cleaned identifiers only.
func JudgeGate(g kanban.Gate, r testrun.Result) (state kanban.GateState, note string, ran bool) {
	switch r.Status {
	case testrun.Passed:
		if isGoTest(r.Command) {
			if g.Test != "" && noTestsToRunRE.MatchString(r.Output) {
				return kanban.GateUnmet, "the named test selected no tests", true
			}
			if g.Dir != "" && len(NoTestFiles(r.Output)) > 0 && !okLineRE.MatchString(r.Output) {
				return kanban.GateUnmet, "the package has no test files", true
			}
		}
		return kanban.GateMet, "exit 0", true
	case testrun.Failed:
		note = fmt.Sprintf("exit %d", r.ExitCode)
		if isGoTest(r.Command) {
			if tests := FailedTests(r.Output); len(tests) > 0 {
				note += ": " + strings.Join(head(tests, 5), ", ")
			}
		}
		return kanban.GateUnmet, note, true
	case testrun.TimedOut:
		return kanban.GateUnmet, "timed out", true
	case testrun.Denied:
		return kanban.GateUnmet, "a permission deny rule refused the run", false
	case testrun.NeedsApproval:
		return kanban.GateUnmet, "an ask rule matches the run and nobody can be asked", false
	}
	return kanban.GateUnmet, "could not run: " + strings.TrimSpace(r.Note), false
}

// Regressions names what the run shows failing that the base record did not.
// A suite that passed at the base and fails now is a regression, and so is a
// test that fails now, named suite:test, in a suite that also failed at the base but did not fail
// there. A suite that failed at the base with no test names to compare is
// treated as failing already. Only suites present in both are compared.
func Regressions(base Record, results []testrun.Result) []string {
	baseStatus := map[string]string{}
	for _, s := range base.Suites {
		baseStatus[s.Name] = s.Status
	}
	baseTests := map[string]map[string]bool{}
	for _, f := range base.Failing {
		set := baseTests[f.Suite]
		if set == nil {
			set = map[string]bool{}
			baseTests[f.Suite] = set
		}
		for _, t := range f.Tests {
			set[t] = true
		}
	}
	var out []string
	for _, r := range results {
		if r.Status != testrun.Failed && r.Status != testrun.TimedOut {
			continue
		}
		name := ident(r.Suite)
		was, known := baseStatus[name]
		if !known {
			continue
		}
		switch was {
		case string(testrun.Passed):
			out = append(out, name)
		case string(testrun.Failed), string(testrun.TimedOut):
			if !isGoTest(r.Command) || len(baseTests[name]) == 0 {
				continue
			}
			for _, t := range FailedTests(r.Output) {
				if !baseTests[name][t] {
					out = append(out, name+":"+t)
				}
			}
		}
	}
	sort.Strings(out)
	return uniq(out, 20)
}

// GateResult is one gate's outcome in a verification.
type GateResult struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	Suite string `json:"suite,omitempty"`
	State string `json:"state"`
	Note  string `json:"note,omitempty"`
}

// Verification is the facts of one card's gate run on one commit.
type Verification struct {
	Version     int           `json:"version"`
	Item        string        `json:"item"`
	Commit      string        `json:"commit"`
	Base        string        `json:"base,omitempty"`
	At          string        `json:"at"`
	Gates       []GateResult  `json:"gates"`
	Suites      []SuiteResult `json:"suites,omitempty"`
	Regressed   []string      `json:"regressed,omitempty"`
	Compared    bool          `json:"compared"`
	AllRunnable bool          `json:"all_runnable_met"`
}

// NewVerification stamps a verification for item (its K-xxxxxx short id) at
// commit.
func NewVerification(item, commit, base string, now time.Time) Verification {
	return Verification{Version: VerificationVersion, Item: item, Commit: strings.ToLower(commit), Base: strings.ToLower(base), At: now.UTC().Format(time.RFC3339)}
}

// SuiteResults converts a run's results into record rows.
func SuiteResults(results []testrun.Result) []SuiteResult {
	var out []SuiteResult
	for _, r := range results {
		out = append(out, SuiteResult{Name: ident(r.Suite), Command: append([]string(nil), r.Command...), Status: string(r.Status), ExitCode: r.ExitCode, DurationMS: r.Duration.Milliseconds()})
	}
	return out
}

// WriteVerification writes v under root, whole or not at all, and drops the
// oldest beyond the last few. It refuses a symlinked directory and a name that
// is not a short item id plus a commit id.
func WriteVerification(root string, v Verification) error {
	if !validCommit(v.Commit) || !itemShortRE.MatchString(v.Item) {
		return fmt.Errorf("quality: %q at %q is not a card and a commit", v.Item, v.Commit)
	}
	dir := filepath.Join(root, filepath.FromSlash(VerifyDir))
	for _, d := range []string{filepath.Join(root, ".vulnetix"), filepath.Join(root, ".vulnetix", "belai"), filepath.Join(root, filepath.FromSlash(Dir)), dir} {
		if fi, err := os.Lstat(d); err == nil && fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("quality: %s is a symlink", d)
		}
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(dir, v.Item+"-"+v.Commit+".json")
	tmp := path + ".part"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	pruneDir(dir, keepVerifications)
	return nil
}

// ReadVerification returns the verification of item at commit, if one exists
// and parses. A record for another commit or card is never returned.
func ReadVerification(root, item, commit string) (Verification, bool) {
	commit = strings.ToLower(strings.TrimSpace(commit))
	if !validCommit(commit) || !itemShortRE.MatchString(item) {
		return Verification{}, false
	}
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(VerifyDir), item+"-"+commit+".json"))
	if err != nil || len(data) > maxRecordLen {
		return Verification{}, false
	}
	var v Verification
	if json.Unmarshal(data, &v) != nil || v.Version != VerificationVersion || v.Commit != commit || v.Item != item {
		return Verification{}, false
	}
	return v, true
}

func pruneDir(dir string, keep int) {
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
	if len(recs) <= keep {
		return
	}
	sort.Slice(recs, func(i, j int) bool { return recs[i].mod.After(recs[j].mod) })
	for _, r := range recs[keep:] {
		_ = os.Remove(filepath.Join(dir, r.name))
	}
}
