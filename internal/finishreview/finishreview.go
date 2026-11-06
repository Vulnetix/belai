// Package finishreview is the harness's own check of the working tree before a
// goal is accepted as complete (docs/finish-review.md). It answers four
// questions a model's "done" does not: did the goal leave scratch files
// behind, did it weaken a test to get green, did anything change after the last
// passing check, and is there a part of the tree this review could not see.
//
// Nothing here asks a model. The review reads the working tree through
// [workdiff.Collect], whose git calls are read-only and hardened, and decides
// from file names, line counts and fixed marker tables. File text is matched
// locally and never leaves the package: a [Finding] holds a closed kind, a path
// drawn from a plain alphabet and a harness-composed detail, so a finding can be
// shown to the user and put in a directive without a classifier.
//
// The review approves and deletes nothing. It names what it found, and any
// remedy runs through the model's ordinary tool calls under the usual
// permissions, hooks and sandbox.
package finishreview

import (
	"context"
	"fmt"
	"path"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/vulnetix/belai/internal/teleport/changes"
	"github.com/vulnetix/belai/internal/workdiff"
)

// Kind is the closed set of things the review reports.
type Kind string

const (
	// KindScratch is an untracked file, new since the goal started, whose name
	// matches the scratch table.
	KindScratch Kind = "scratch"
	// KindTestRemoved is a test file that lost more lines than it gained while
	// source code changed.
	KindTestRemoved Kind = "test_removed"
	// KindTestSkipped is a test file that gained a skip or focus marker while
	// source code changed.
	KindTestSkipped Kind = "test_skipped"
	// KindUnverified is changed source code with no passing check after the
	// last change.
	KindUnverified Kind = "unverified"
	// KindUnreviewed is part of the tree the review could not read.
	KindUnreviewed Kind = "unreviewed"
)

// Kinds lists every kind in the order findings are reported, most serious first.
var Kinds = []Kind{KindUnreviewed, KindUnverified, KindTestRemoved, KindTestSkipped, KindScratch}

// MaxFindings bounds one report.
const MaxFindings = 20

// collectBudget bounds one review's git work.
const collectBudget = 45 * time.Second

// Finding is one thing the review noticed. Path is empty when the finding is
// about the tree as a whole, or when the file's name is outside the plain
// alphabet and so is not named. Detail is composed here from fixed text and
// counts.
type Finding struct {
	Kind   Kind
	Path   string
	Detail string
}

// Line renders a finding as one line of harness text.
func (f Finding) Line() string {
	var head string
	switch f.Kind {
	case KindScratch:
		head = "scratch file left behind"
	case KindTestRemoved:
		head = "test lost more lines than it gained"
	case KindTestSkipped:
		head = "test gained a skip or focus marker"
	case KindUnverified:
		head = "source changed with no passing check after it"
	case KindUnreviewed:
		head = "part of the tree was not reviewed"
	default:
		head = "unknown finding"
	}
	s := head
	if f.Path != "" {
		s += ": " + f.Path
	}
	if f.Detail != "" {
		s += " (" + f.Detail + ")"
	}
	return s
}

// Report is the outcome of one review.
type Report struct {
	// Findings are at most [MaxFindings], most serious first, then by path.
	Findings []Finding
	// More counts findings past the cap that are not listed.
	More int
}

// Empty reports whether the review found nothing.
func (r Report) Empty() bool { return len(r.Findings) == 0 && r.More == 0 }

// OnlyUnreviewed reports whether the one finding is that the tree could not be
// reviewed: there is nothing for the model to fix, only to state.
func (r Report) OnlyUnreviewed() bool {
	return r.More == 0 && len(r.Findings) == 1 && r.Findings[0].Kind == KindUnreviewed
}

// Options is everything a review takes.
type Options struct {
	// Workdir is the directory under review (the repository is found from it).
	Workdir string
	// Hide reports a repository-relative path whose content must not be read
	// (a Read deny rule). Such a path counts as unreviewed. May be nil.
	Hide func(rel string) bool
	// Baseline is the set of files that were already changed when the goal
	// started. The zero value knows nothing, so every untracked file is new.
	Baseline Baseline
	// Verified says a passing check ran after the last change the goal made.
	// A goal that changed nothing is verified.
	Verified bool
	// Suites names the detected test suites, for the unverified finding.
	Suites []string
	// ScratchPatterns are extra scratch patterns, added to the built-in table.
	ScratchPatterns []string
}

// Review collects the working tree's changes and checks them. It never changes
// the tree, the index or any ref.
func Review(ctx context.Context, o Options) Report {
	ctx, cancel := context.WithTimeout(ctx, collectBudget)
	defer cancel()
	return analyse(workdiff.Collect(ctx, o.Workdir, o.Hide), o)
}

// ScratchTable is the built-in set of base-name patterns that mark an untracked
// file as scratch. It is fixed: a project can add patterns and never remove one.
var ScratchTable = []string{
	"*.orig", "*.rej", "*.bak", "*.tmp", "*.log", "nohup.out", "scratch*",
	"debug*.go", "debug*.py", "debug*.js",
}

// analyse is the pure half of [Review], so each rule is tested against a
// snapshot built by hand.
func analyse(snap workdiff.Snapshot, o Options) Report {
	if snap.Unavailable != "" {
		return Report{Findings: []Finding{{Kind: KindUnreviewed, Detail: "the working tree could not be read: not a git repository, or git is unavailable"}}}
	}
	var (
		found                  []Finding
		tests                  []workdiff.File
		blind                  int
		patterns               = slices.Concat(ScratchTable, o.ScratchPatterns)
		unnamedScratch, srcAny int
	)
	for _, f := range snap.Files {
		if harnessPath(f.Path) {
			continue
		}
		if f.Hidden || f.Note == "too large to diff" || f.Note == "unreadable" {
			blind++
		}
		switch {
		case IsTestFile(f.Path):
			tests = append(tests, f)
		case IsSourceFile(f.Path):
			srcAny++
		}
		if f.Untracked && !o.Baseline.Has(f.Path) && matchesAny(f.Path, patterns) {
			if changes.SafePath(f.Path) {
				found = append(found, Finding{Kind: KindScratch, Path: f.Path, Detail: "new since the goal started"})
			} else {
				unnamedScratch++
			}
		}
	}
	if unnamedScratch > 0 {
		found = append(found, Finding{Kind: KindScratch, Detail: fmt.Sprintf("%d new file(s) with unusual names", unnamedScratch)})
	}

	if srcAny > 0 {
		for _, f := range tests {
			if f.Hidden || f.Note != "" || f.Change.Binary || f.Change.Truncated {
				continue
			}
			p := f.Path
			if !changes.SafePath(p) {
				p = ""
			}
			if !f.Untracked && f.Removed > f.Added {
				found = append(found, Finding{Kind: KindTestRemoved, Path: p, Detail: fmt.Sprintf("%d added, %d removed", f.Added, f.Removed)})
			}
			if n := countSkips(f.Change.New) - countSkips(f.Change.Old); n > 0 {
				found = append(found, Finding{Kind: KindTestSkipped, Path: p, Detail: fmt.Sprintf("%d new marker(s)", n)})
			}
		}
		if !o.Verified {
			d := "run the project's checks after the last change"
			if names := suiteNames(o.Suites); names != "" {
				d += ": " + names
			}
			found = append(found, Finding{Kind: KindUnverified, Detail: d})
		}
	}

	var gaps []string
	if snap.More > 0 {
		gaps = append(gaps, fmt.Sprintf("%d changed file(s) lie beyond the review limit of %d", snap.More, workdiff.MaxFiles))
	}
	if blind > 0 {
		gaps = append(gaps, fmt.Sprintf("%d changed file(s) could not be read (hidden by a Read rule, too large or unreadable)", blind))
	}
	if len(gaps) > 0 {
		found = append(found, Finding{Kind: KindUnreviewed, Detail: strings.Join(gaps, "; ")})
	}

	order := map[Kind]int{}
	for i, k := range Kinds {
		order[k] = i
	}
	sort.SliceStable(found, func(i, j int) bool {
		if order[found[i].Kind] != order[found[j].Kind] {
			return order[found[i].Kind] < order[found[j].Kind]
		}
		return found[i].Path < found[j].Path
	})
	r := Report{Findings: found}
	if len(found) > MaxFindings {
		r.More = len(found) - MaxFindings
		r.Findings = found[:MaxFindings]
	}
	return r
}

// harnessPath reports whether p is Belai's or git's own state, which is neither
// the goal's work nor reviewable.
func harnessPath(p string) bool {
	return p == ".git" || p == ".vulnetix" || strings.HasPrefix(p, ".git/") || strings.HasPrefix(p, ".vulnetix/")
}

// matchesAny reports whether p matches a pattern. A pattern with no slash is
// matched against the base name, lowercased; one with a slash against the whole
// relative path.
func matchesAny(p string, patterns []string) bool {
	base := strings.ToLower(path.Base(p))
	for _, pat := range patterns {
		var ok bool
		if strings.Contains(pat, "/") {
			ok, _ = path.Match(pat, p)
		} else {
			ok, _ = path.Match(strings.ToLower(pat), base)
		}
		if ok {
			return true
		}
	}
	return false
}

func suiteNames(suites []string) string {
	var out []string
	for _, s := range suites {
		if safeName(s) && len(out) < 4 {
			out = append(out, s)
		}
	}
	return strings.Join(out, ", ")
}

// safeName accepts a suite name from the harness's fixed table.
func safeName(s string) bool {
	if s == "" || len(s) > 32 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
		default:
			return false
		}
	}
	return true
}
