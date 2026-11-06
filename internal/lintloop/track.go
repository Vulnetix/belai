package lintloop

import (
	"fmt"
	"strings"
	"sync"

	"github.com/vulnetix/belai/internal/lsp"
	"github.com/vulnetix/belai/internal/sanitize"
)

// maxTrackedFiles bounds the per-file history one session keeps.
const maxTrackedFiles = 256

// Tracker counts, for each file, how many passes in a row reported the same
// findings. It is a session's own memory and holds signatures only.
type Tracker struct {
	mu    sync.Mutex
	files map[string]trackEntry
	noted map[string]bool
}

type trackEntry struct {
	sig string
	n   int
}

// Observe records a pass that reported rows with signature sig for abs and
// returns how many passes in a row, this one included, reported exactly that.
func (t *Tracker) Observe(abs, sig string) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.files == nil || len(t.files) >= maxTrackedFiles {
		if _, known := t.files[abs]; !known {
			t.files = map[string]trackEntry{}
		}
	}
	e := t.files[abs]
	if e.sig == sig {
		e.n++
	} else {
		e = trackEntry{sig: sig, n: 1}
	}
	t.files[abs] = e
	return e.n
}

// NoteOnce reports true the first time it sees key and false after, so a
// harness note that would repeat on every edit is said once per session.
func (t *Tracker) NoteOnce(key string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.noted[key] {
		return false
	}
	if t.noted == nil {
		t.noted = map[string]bool{}
	}
	t.noted[key] = true
	return true
}

// Clear forgets abs, after a pass with nothing to report for it.
func (t *Tracker) Clear(abs string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.files, abs)
}

// Body renders the rows of a report as the diagnostics text a block carries:
// the one renderer in internal/lsp, capped at maxRows rows of one line each,
// then stripped of delimiter markup. It is "" when there are no rows. The
// caller classifies the result before it is sealed.
func Body(rep Report, maxRows int) string {
	if len(rep.Rows) == 0 {
		return ""
	}
	r := lsp.Report{
		Language: "lint (" + sanitize.Ident(rep.Tool, 24) + ")",
		Status:   lsp.StatusReady,
		Rows:     rep.Rows,
	}
	return sanitize.Sanitize(lsp.Render(r, maxRows, 200))
}

// CountsLine is the harness-composed line that stands in for the rows when they
// cannot be shown (the classifier withheld them or failed). It holds a tool
// name from the table, a count and the file's name, never linter text.
func CountsLine(rep Report, rel string) string {
	n := len(rep.Rows)
	return fmt.Sprintf("[harness: %s reported %d finding(s) in %s; the text was not shown]", sanitize.Ident(rep.Tool, 24), n, sanitize.Line(rel, 160))
}

// StuckLine is the harness-composed note for a file whose findings did not
// change over passes edits.
func StuckLine(rep Report, rel string, passes int) string {
	return fmt.Sprintf("[harness: %s still reports the same %d finding(s) in %s after %d passes. Do not keep editing this file for them; continue with your remaining steps and say in your report that they are open.]",
		sanitize.Ident(rep.Tool, 24), len(rep.Rows), sanitize.Line(rel, 160), passes)
}

// ApprovalLine is the one-line note for a linter an ask rule matches: the loop
// never prompts mid-edit, so it does not run.
func ApprovalLine(tool string) string {
	return fmt.Sprintf("[harness: the lint loop skipped %s because a permission ask rule matches it; add an allow rule to run it after edits]", strings.TrimSpace(sanitize.Ident(tool, 24)))
}
