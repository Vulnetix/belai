// Package review runs a code review turn (docs/review.md): the harness gathers
// the change, a read-only model turn judges it, and the harness validates what
// came back, stores it and, only when it is allowed to, posts it.
//
// The split is the point. The model emits findings and a verdict as plain
// lines and nothing else: it has no write tool, cannot approve or merge, and
// is never asked whether anything is safe. The harness decides the target, the
// caps, which files are reviewable, whether a finding sits in the diff, the
// order, whether a comment is posted and whether it was posted before. The
// verdict is advisory and moves nothing.
//
// Untrusted text (the diff, a pull request's title and description, check
// output, a repository's own rubric lines) reaches the model only through the
// tools it calls, whose results are sanitised and classified like any other,
// or as a path or number the harness has shape-checked. What comes back is
// parsed against a closed grammar; a line that does not fit is dropped and
// counted, never repaired.
package review

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

// Kind names what is being reviewed.
type Kind string

// The review targets.
const (
	// KindWorkdir is the working tree's staged, unstaged and untracked changes.
	KindWorkdir Kind = "workdir"
	// KindBranch is a branch against its merge base with another ref.
	KindBranch Kind = "branch"
	// KindPR is a pull request (a merge request on GitLab).
	KindPR Kind = "pr"
)

// Target says what to review. Ref is a branch name for KindBranch (empty is
// HEAD) and a pull request number for KindPR (empty or "current" is the pull
// request of the current branch). Base is the ref a branch is compared with
// (empty is the remote's default branch).
type Target struct {
	Kind Kind   `json:"kind"`
	Ref  string `json:"ref,omitempty"`
	Base string `json:"base,omitempty"`
}

// Verdict is the reviewer's recommendation. It is advisory: nothing in Belai
// approves, requests changes on or merges a change because of it.
type Verdict string

// The verdicts, as the model writes them after "verdict:".
const (
	VerdictApprove        Verdict = "approve"
	VerdictComment        Verdict = "comment"
	VerdictRequestChanges Verdict = "request_changes"
)

// Verdicts is the closed set of verdict words.
var Verdicts = []Verdict{VerdictApprove, VerdictComment, VerdictRequestChanges}

// Severity ranks a finding.
type Severity string

// The severities, most serious first.
const (
	SeverityBlocker Severity = "blocker"
	SeverityMajor   Severity = "major"
	SeverityMinor   Severity = "minor"
	SeverityNit     Severity = "nit"
)

// Severities is the closed set of severity words, most serious first.
var Severities = []Severity{SeverityBlocker, SeverityMajor, SeverityMinor, SeverityNit}

func (s Severity) rank() int { return slices.Index(Severities, s) }

// serious reports whether a finding of this severity can back a
// request_changes verdict.
func (s Severity) serious() bool { return s == SeverityBlocker || s == SeverityMajor }

// Criterion is one item of the fixed rubric.
type Criterion struct {
	ID   string
	Text string
}

// Criteria is the closed rubric. A finding names one of these ids. A
// repository or the user can add criteria for the model to weigh, never remove
// one of these or add an id.
var Criteria = []Criterion{
	{"correctness", "the change does what it says: logic, edge cases, off-by-one errors, nil and error paths"},
	{"security", "input handling, injection, secrets, permissions, unsafe defaults, a new trust boundary"},
	{"tests", "new behaviour is tested; no test was weakened, skipped or deleted to make it pass"},
	{"errors", "errors are handled and surfaced, and none is swallowed"},
	{"concurrency", "races, locking, the lifetime of goroutines and resources"},
	{"performance", "work in a hot path, unbounded growth, needless I/O"},
	{"compatibility", "public API, file formats, settings and migrations are not broken"},
	{"maintainability", "clarity, duplication, naming, dead code"},
	{"docs", "docs and comments match the change; new settings and flags are documented"},
}

// RubricIDs lists the ids of Criteria.
func RubricIDs() []string {
	ids := make([]string, len(Criteria))
	for i, c := range Criteria {
		ids[i] = c.ID
	}
	return ids
}

func validRubric(id string) bool {
	for _, c := range Criteria {
		if c.ID == id {
			return true
		}
	}
	return false
}

// Bounds on a review.
const (
	// MaxFindings is how many finding lines are read. The rest are counted as
	// dropped.
	MaxFindings = 20
	// MaxBodyBytes is the longest finding body kept; a longer one is dropped.
	MaxBodyBytes = 2048
	// MaxFixBytes is the longest suggested fix kept; a longer one is dropped.
	MaxFixBytes = 1024
	// MaxReplyBytes is how much of the model's reply is read.
	MaxReplyBytes = 64 << 10
	// MaxDiffFiles is how many reviewable files one review covers.
	MaxDiffFiles = 200
	// Keep is how many finished reviews the store keeps.
	Keep = 50
	// MaxCommentBytes bounds a posted comment, under the 65536 a forge allows.
	MaxCommentBytes = 60000
)

// Finding is one validated observation about a line of the diff.
type Finding struct {
	File     string   `json:"file"`
	Line     int      `json:"line"`
	Severity Severity `json:"severity"`
	Rubric   string   `json:"rubric"`
	Body     string   `json:"body"`
	Fix      string   `json:"fix,omitempty"`
}

// Posted records that a review was posted to a pull request.
type Posted struct {
	PR int       `json:"pr"`
	At time.Time `json:"at"`
}

// Result is a finished review.
type Result struct {
	Version int    `json:"version"`
	Target  Target `json:"target"`
	// Repo is the repository key the review belongs to (a hash, not a name).
	Repo string `json:"repo"`
	// HeadSHA is the full commit id the review covers: the head of the branch
	// or pull request, or HEAD for a working tree. Empty when there is none.
	HeadSHA string `json:"head_sha,omitempty"`
	// Digest tells two states of one working tree apart; empty otherwise.
	Digest string `json:"digest,omitempty"`
	// PR is the pull request number for KindPR.
	PR       int       `json:"pr,omitempty"`
	Verdict  Verdict   `json:"verdict"`
	Findings []Finding `json:"findings"`
	// Files is how many files the review covered, Skipped how many changed
	// files it could not (binary, deleted, read-denied, over a cap).
	Files   int `json:"files"`
	Skipped int `json:"skipped"`
	// Dropped counts finding lines that failed validation or passed the limit.
	Dropped int `json:"dropped"`
	// Truncated reports a diff cut at a cap: some changed files were not seen.
	Truncated bool `json:"truncated"`
	// Incomplete reports that the model gave no readable verdict line.
	Incomplete bool      `json:"incomplete"`
	Created    time.Time `json:"created"`
	Posted     []Posted  `json:"posted,omitempty"`
	// Cached is set on a result read back from the store; it is not stored.
	Cached bool `json:"-"`
}

// ResultVersion is the layout version of a stored Result.
const ResultVersion = 1

// ExitCode is the process exit code `belai review` returns for a result:
// 0 for approve or comment, 1 for request_changes.
func (r Result) ExitCode() int {
	if r.Verdict == VerdictRequestChanges {
		return 1
	}
	return 0
}

// finalVerdict is the harness's verdict, derived from the model's word and
// what survived validation. The model cannot approve what the harness did not
// let it see in full, nor ask for changes it cannot point to.
func finalVerdict(said Verdict, ok bool, findings []Finding, truncated bool) Verdict {
	if !ok {
		return VerdictComment
	}
	serious := false
	for _, f := range findings {
		serious = serious || f.Severity.serious()
	}
	switch said {
	case VerdictRequestChanges:
		if !serious {
			return VerdictComment
		}
		return VerdictRequestChanges
	case VerdictApprove:
		if serious || truncated || len(findings) > 0 && anyAbove(findings, SeverityNit) {
			return VerdictComment
		}
		return VerdictApprove
	}
	return VerdictComment
}

// anyAbove reports whether a finding is more serious than sev.
func anyAbove(fs []Finding, sev Severity) bool {
	for _, f := range fs {
		if f.Severity.rank() < sev.rank() {
			return true
		}
	}
	return false
}

// Format renders r as plain text for a terminal: the verdict (advisory), each
// finding with its place, severity and criterion, and what the harness left
// out. Every string in r is already clean, so the text is safe to print.
func Format(r Result) string {
	var b strings.Builder
	where := "working tree"
	switch r.Target.Kind {
	case KindBranch:
		where = "branch " + shortID(r.HeadSHA)
	case KindPR:
		where = fmt.Sprintf("pull request #%d", r.PR)
	}
	fmt.Fprintf(&b, "review of %s: %d file(s)", where, r.Files)
	if r.Cached {
		b.WriteString(" (stored review of this head; run it again to replace it)")
	}
	fmt.Fprintf(&b, "\nverdict: %s (advisory)\n", r.Verdict)
	for i, f := range r.Findings {
		fmt.Fprintf(&b, "%d. %s:%d %s/%s: %s\n", i+1, f.File, f.Line, f.Severity, f.Rubric, f.Body)
		if f.Fix != "" {
			fmt.Fprintf(&b, "   fix: %s\n", f.Fix)
		}
	}
	if len(r.Findings) == 0 {
		b.WriteString("no findings\n")
	}
	if r.Dropped > 0 {
		fmt.Fprintf(&b, "%d finding line(s) did not match the diff or the format and were dropped\n", r.Dropped)
	}
	if r.Truncated || r.Skipped > 0 {
		fmt.Fprintf(&b, "%d changed file(s) were not reviewed (binary, deleted, read-denied or past a cap)\n", r.Skipped)
	}
	if r.Incomplete {
		b.WriteString("the model gave no readable verdict line, so the verdict is comment\n")
	}
	return b.String()
}

func shortID(s string) string {
	switch {
	case s == "":
		return "(no commit)"
	case len(s) > 12:
		return s[:12]
	}
	return s
}
