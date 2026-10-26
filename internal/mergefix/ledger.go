package mergefix

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Fingerprint is the first eight hex characters of the sha256 of the sorted
// per-hunk choice vector. Two resolutions that make the same choice for every
// hunk share it; a mixed or other hunk carries the digest of its text, so two
// different mixes do not.
func Fingerprint(results []HunkResult) string {
	entries := make([]string, 0, len(results))
	for _, r := range results {
		entries = append(entries, r.entry())
	}
	sort.Strings(entries)
	sum := sha256.Sum256([]byte(strings.Join(entries, "\n")))
	return hex.EncodeToString(sum[:4])
}

// FileChoice is one file's summary in a note: every hunk the same choice, or
// mixed when they differ.
type FileChoice struct {
	File   string `json:"file"`
	Choice string `json:"choice"`
}

// Summarise reduces the hunk results to one choice per file, sorted by path.
func Summarise(results []HunkResult) []FileChoice {
	byFile := map[string]string{}
	var order []string
	for _, r := range results {
		c := r.Choice.NoteChoice()
		prev, seen := byFile[r.File]
		switch {
		case !seen:
			byFile[r.File] = c
			order = append(order, r.File)
		case prev != c:
			byFile[r.File] = string(Mixed)
		}
	}
	sort.Strings(order)
	out := make([]FileChoice, 0, len(order))
	for _, f := range order {
		out = append(out, FileChoice{File: f, Choice: byFile[f]})
	}
	return out
}

// Outcome is how an attempt ended.
type Outcome string

const (
	Passed                Outcome = "passed"
	BuildFailed           Outcome = "build failed"
	LintFailed            Outcome = "lint failed"
	TestsFailed           Outcome = "tests failed"
	MarkersFound          Outcome = "markers left"
	DuplicateImportsFound Outcome = "duplicate imports"
	Repeat                Outcome = "repeat"
)

// Result is an attempt's outcome with the identifier that names what failed:
// the build command, the linter and its finding count, the test suite, the
// file, or the earlier attempt a repeat matched.
type Result struct {
	Outcome  Outcome `json:"outcome"`
	Command  string  `json:"command,omitempty"`
	Linter   string  `json:"linter,omitempty"`
	Count    int     `json:"count,omitempty"`
	Suite    string  `json:"suite,omitempty"`
	File     string  `json:"file,omitempty"`
	RepeatOf int     `json:"repeat_of,omitempty"`
}

// Failed reports whether the attempt failed.
func (r Result) Failed() bool { return r.Outcome != Passed }

// String is the result as a note writes it.
func (r Result) String() string {
	switch r.Outcome {
	case Passed:
		return "passed"
	case BuildFailed:
		return "build failed (" + cleanIdent(r.Command) + ")"
	case LintFailed:
		return fmt.Sprintf("lint failed (%s: %d)", cleanIdent(r.Linter), r.Count)
	case TestsFailed:
		return "tests failed (" + cleanIdent(r.Suite) + ")"
	case MarkersFound:
		return "markers left"
	case DuplicateImportsFound:
		return "duplicate imports (" + NoteFile(r.File) + ")"
	case Repeat:
		return fmt.Sprintf("repeat of attempt %d", r.RepeatOf)
	}
	return "unknown"
}

// identRE is what a command, linter or suite name may hold in a note.
var identRE = regexp.MustCompile(`[^A-Za-z0-9 ./_:=+-]`)

func cleanIdent(s string) string {
	s = identRE.ReplaceAllString(s, "")
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 80 {
		s = s[:80]
	}
	if s == "" {
		return "unnamed"
	}
	return s
}

// noteFileRE is a path a note lists as written: the characters that cannot
// break the note's own separators.
var noteFileRE = regexp.MustCompile(`^[A-Za-z0-9._@+/-]{1,120}$`)

// NoteFile is a path as a note lists it. A path with any other character (a
// space, a comma, an equals sign, a non-ASCII rune), a dot-dot segment or more
// than 120 characters is named by the hash of its bytes instead, so the
// separators stay unambiguous and nothing the pull request's author chose as a
// name can shape the note.
func NoteFile(path string) string {
	if noteFileRE.MatchString(path) && !strings.Contains("/"+path+"/", "/../") {
		return path
	}
	sum := sha256.Sum256([]byte(path))
	return "file-" + hex.EncodeToString(sum[:4])
}

// MaxNoteFiles is how many files a note lists before "+K more".
const MaxNoteFiles = 12

// maxNoteLen keeps a note under the board's note limit.
const maxNoteLen = 960

// Attempt is one checkout of the loop.
type Attempt struct {
	N           int          `json:"n"`
	Max         int          `json:"max"`
	Fingerprint string       `json:"fingerprint"`
	Files       []FileChoice `json:"files"`
	Result      Result       `json:"result"`
	// Strategy is the harness's suggestion for the attempt, by name.
	Strategy string `json:"strategy,omitempty"`
	// Implicated are the conflicted files the failing check named.
	Implicated []string     `json:"implicated,omitempty"`
	Hunks      []HunkResult `json:"hunks,omitempty"`
}

// FormatNote is the attempt as the card's note:
//
//	conflict attempt N/M <fingerprint8>: <file>=<choice>[, ...] -> <result>
//
// At most twelve files are listed, then "+K more"; fewer when the note would
// pass the board's note limit.
func FormatNote(a Attempt) string {
	prefix := fmt.Sprintf("conflict attempt %d/%d %s: ", a.N, a.Max, a.Fingerprint)
	suffix := " -> " + a.Result.String()
	files := append([]FileChoice(nil), a.Files...)
	sort.Slice(files, func(i, j int) bool { return files[i].File < files[j].File })
	for n := min(MaxNoteFiles, len(files)); ; n-- {
		parts := make([]string, 0, n+1)
		for _, f := range files[:n] {
			parts = append(parts, NoteFile(f.File)+"="+f.Choice)
		}
		if rest := len(files) - n; rest > 0 {
			parts = append(parts, fmt.Sprintf("+%d more", rest))
		}
		note := prefix + strings.Join(parts, ", ") + suffix
		if len(note) <= maxNoteLen || n == 0 {
			return note
		}
	}
}

var (
	noteRE   = regexp.MustCompile(`^conflict attempt (\d{1,3})/(\d{1,3}) ([0-9a-f]{8}): (.+) -> (.+)$`)
	moreRE   = regexp.MustCompile(`^\+(\d+) more$`)
	buildRE  = regexp.MustCompile(`^build failed \((.+)\)$`)
	lintRE   = regexp.MustCompile(`^lint failed \((.+): (\d+)\)$`)
	testsRE  = regexp.MustCompile(`^tests failed \((.+)\)$`)
	dupRE    = regexp.MustCompile(`^duplicate imports \((.+)\)$`)
	repeatRE = regexp.MustCompile(`^repeat of attempt (\d{1,3})$`)
)

// ParseNote reads an attempt back from a note. Any other note is false.
func ParseNote(note string) (Attempt, bool) {
	m := noteRE.FindStringSubmatch(note)
	if m == nil {
		return Attempt{}, false
	}
	n, _ := strconv.Atoi(m[1])
	mx, _ := strconv.Atoi(m[2])
	a := Attempt{N: n, Max: mx, Fingerprint: m[3]}
	for _, part := range strings.Split(m[4], ", ") {
		if moreRE.MatchString(part) {
			continue
		}
		file, choice, ok := strings.Cut(part, "=")
		if !ok || !ValidChoice(Choice(choice)) || choice == string(BothIncomingFirst) {
			return Attempt{}, false
		}
		a.Files = append(a.Files, FileChoice{File: file, Choice: choice})
	}
	res := m[5]
	switch {
	case res == "passed":
		a.Result = Result{Outcome: Passed}
	case res == "markers left":
		a.Result = Result{Outcome: MarkersFound}
	case buildRE.MatchString(res):
		a.Result = Result{Outcome: BuildFailed, Command: buildRE.FindStringSubmatch(res)[1]}
	case lintRE.MatchString(res):
		lm := lintRE.FindStringSubmatch(res)
		c, _ := strconv.Atoi(lm[2])
		a.Result = Result{Outcome: LintFailed, Linter: lm[1], Count: c}
	case testsRE.MatchString(res):
		a.Result = Result{Outcome: TestsFailed, Suite: testsRE.FindStringSubmatch(res)[1]}
	case dupRE.MatchString(res):
		a.Result = Result{Outcome: DuplicateImportsFound, File: dupRE.FindStringSubmatch(res)[1]}
	case repeatRE.MatchString(res):
		r, _ := strconv.Atoi(repeatRE.FindStringSubmatch(res)[1])
		a.Result = Result{Outcome: Repeat, RepeatOf: r}
	default:
		return Attempt{}, false
	}
	return a, true
}

// FinalResolved is the note that ends a round with a pushed resolution.
func FinalResolved(n int, sha string) string {
	if len(sha) > 12 {
		sha = sha[:12]
	}
	return fmt.Sprintf("conflicts resolved on attempt %d, pushed %s", n, sha)
}

// FinalNotPushed is the note that ends a round whose resolution passed but
// was not pushed to the pull request's head; why is a harness phrase.
func FinalNotPushed(n int, why string) string {
	return fmt.Sprintf("conflicts resolved on attempt %d, not pushed: %s", n, why)
}

// FinalUnresolved is the note that ends a round with no resolution.
func FinalUnresolved(max int) string {
	return fmt.Sprintf("conflicts unresolved after %d attempts", max)
}

var finalRE = regexp.MustCompile(`^conflicts (resolved on attempt \d+|unresolved after \d+ attempts)`)

// Tried is a fingerprint an earlier attempt produced.
type Tried struct {
	Fingerprint string `json:"fingerprint"`
	N           int    `json:"n"`
}

// Baseline is what the build and the linters reported on the pull request's
// head before the merge: a build that already failed there, or a linter's
// findings there, are not the merge's doing.
type Baseline struct {
	Head        string         `json:"head"`
	BuildFailed []string       `json:"build_failed,omitempty"`
	Lint        map[string]int `json:"lint,omitempty"`
	LintSkip    []string       `json:"lint_skip,omitempty"`
}

// LedgerVersion is the ledger file's format.
const LedgerVersion = 1

// Ledger is an item's attempts on one pair of commits. Attempts is the
// current round; Tried holds every fingerprint produced on these commits,
// earlier rounds included, so no round repeats one.
type Ledger struct {
	Version  int       `json:"version"`
	Item     string    `json:"item"`
	Head     string    `json:"head"`
	Base     string    `json:"base"`
	Attempts []Attempt `json:"attempts,omitempty"`
	Tried    []Tried   `json:"tried,omitempty"`
	Baseline *Baseline `json:"baseline,omitempty"`
	// Round counts the rounds on these commits, from 1. A round ends when its
	// checkouts are spent (Closed) or one passes; a person sending the card back
	// starts the next one.
	Round  int  `json:"round,omitempty"`
	Closed bool `json:"closed,omitempty"`
}

// RoundOr is the round, 1 for a ledger that never counted one.
func (l Ledger) RoundOr() int { return max(l.Round, 1) }

// For returns the ledger to continue for these commits, and whether it starts
// a new round on a round that ended. It is l itself when its round is still
// open; a fresh first round when either side moved (the conflicts are new);
// and the next round, attempts numbered from 1 again, when the last one ended
// (its checkouts were spent and a person sent the card back, or one passed and
// the card came back). A new round still excludes every fingerprint any
// earlier round on these commits produced.
func (l Ledger) For(item, head, base string, max int) (Ledger, bool) {
	if l.Item != item || l.Head != head || l.Base != base {
		return Ledger{Version: LedgerVersion, Item: item, Head: head, Base: base, Round: 1}, false
	}
	l.Version = LedgerVersion
	ended := l.Closed || len(l.Attempts) >= max || (len(l.Attempts) > 0 && !l.Attempts[len(l.Attempts)-1].Result.Failed())
	if !ended {
		l.Round = l.RoundOr()
		return l, false
	}
	l.Round, l.Closed, l.Attempts = l.RoundOr()+1, false, nil
	return l, true
}

// RetryNote is the note that starts round r.
func RetryNote(r int) string { return fmt.Sprintf("conflict retry: round %d", r) }

var retryRE = regexp.MustCompile(`^conflict retry: round (\d{1,4})$`)

// LedgerFromNotes rebuilds a ledger from a card's notes, oldest first, for a
// host with no ledger file of its own: the current round's attempts, every
// fingerprint any round tried, the round number, and whether the last round
// ended. The notes carry no commits, so the caller states head and base.
func LedgerFromNotes(item, head, base string, notes []string) Ledger {
	l := Ledger{Version: LedgerVersion, Item: item, Head: head, Base: base, Round: 1}
	for _, n := range notes {
		switch {
		case retryRE.MatchString(n):
			r, _ := strconv.Atoi(retryRE.FindStringSubmatch(n)[1])
			l.Round, l.Closed, l.Attempts = max(r, l.Round), false, nil
		case finalRE.MatchString(n):
			l.Closed = true
		default:
			if a, ok := ParseNote(n); ok {
				if l.Closed {
					// An attempt after a final note with no retry note: a round
					// an older Belai started.
					l.Round, l.Closed, l.Attempts = l.Round+1, false, nil
				}
				l.Record(a)
			}
		}
	}
	return l
}

// Record adds an attempt to the round and its fingerprint to Tried.
func (l *Ledger) Record(a Attempt) {
	l.Attempts = append(l.Attempts, a)
	if a.Fingerprint == "" {
		return
	}
	for _, t := range l.Tried {
		if t.Fingerprint == a.Fingerprint {
			return
		}
	}
	l.Tried = append(l.Tried, Tried{Fingerprint: a.Fingerprint, N: a.N})
}

// TriedBy returns the attempt that produced fingerprint fp, or 0.
func (l Ledger) TriedBy(fp string) int {
	for _, a := range l.Attempts {
		if a.Fingerprint == fp {
			return a.N
		}
	}
	for _, t := range l.Tried {
		if t.Fingerprint == fp {
			return t.N
		}
	}
	return 0
}

// Fingerprints are every fingerprint the ledger holds.
func (l Ledger) Fingerprints() map[string]bool {
	out := map[string]bool{}
	for _, a := range l.Attempts {
		if a.Fingerprint != "" {
			out[a.Fingerprint] = true
		}
	}
	for _, t := range l.Tried {
		out[t.Fingerprint] = true
	}
	return out
}

// LastImplicated is the files the most recent failed attempt's check named.
func (l Ledger) LastImplicated() []string {
	for i := len(l.Attempts) - 1; i >= 0; i-- {
		if a := l.Attempts[i]; a.Result.Failed() && len(a.Implicated) > 0 {
			return a.Implicated
		}
	}
	return nil
}

var itemIDRE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// ledgerPath is the item's ledger file in dir. The item id is a UUID, so it is
// a plain file name.
func ledgerPath(dir, item string) (string, error) {
	if !itemIDRE.MatchString(item) {
		return "", errors.New("mergefix: not an item id")
	}
	return filepath.Join(dir, item+".json"), nil
}

// MaxLedgerBytes bounds a ledger file.
const MaxLedgerBytes = 1 << 20

// ReadLedger reads an item's ledger from dir. A missing, symlinked, oversized
// or unreadable file is no ledger.
func ReadLedger(dir, item string) (Ledger, bool) {
	p, err := ledgerPath(dir, item)
	if err != nil {
		return Ledger{}, false
	}
	if fi, err := os.Lstat(dir); err != nil || !fi.IsDir() {
		return Ledger{}, false
	}
	fi, err := os.Lstat(p)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > MaxLedgerBytes {
		return Ledger{}, false
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return Ledger{}, false
	}
	var l Ledger
	if json.Unmarshal(data, &l) != nil || l.Version != LedgerVersion || l.Item != item {
		return Ledger{}, false
	}
	return l, true
}

// WriteLedger writes the ledger to dir, mode 0600, by an atomic rename. A
// symlinked directory or file is refused.
func WriteLedger(dir string, l Ledger) error {
	p, err := ledgerPath(dir, l.Item)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if fi, err := os.Lstat(dir); err != nil || !fi.IsDir() {
		return errors.New("mergefix: the ledger directory is not a directory")
	}
	if fi, err := os.Lstat(p); err == nil && !fi.Mode().IsRegular() {
		return errors.New("mergefix: the ledger file is not a regular file")
	}
	l.Version = LedgerVersion
	data, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return err
	}
	if len(data) > MaxLedgerBytes {
		return errors.New("mergefix: the ledger is too large")
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}
