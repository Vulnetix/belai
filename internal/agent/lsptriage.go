package agent

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/kanban"
	"github.com/vulnetix/belai/internal/lsp"
	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/sanitize"
)

// LSP triage. After a Write or Edit the language server's errors ride back on
// the tool result and the model tries again. Some errors do not yield: the
// same ones return pass after pass, or the fix lies outside the file. From the
// second pass on with errors still present, a decision backend is asked how
// likely another pass is to clear them; when it is unlikely the harness files
// a bug on the board itself and tells the model, so the model can carry on
// with its goal, plan and todo steps instead of circling.
//
// The filing is deterministic and made by the harness: the model writes none of
// its text. A fleet worker, plan mode, a session with no board, or a job that
// is off never files.

const (
	// triageMinAttempt is the first pass that is judged (the second).
	triageMinAttempt = 2
	// defaultRepairAttempts is the pass count after which, with no answer from
	// the backend and no improvement, the bug is filed anyway.
	defaultRepairAttempts = 4
	// triageMaxRows is how many errors the backend and the board item show.
	triageMaxRows = 10
)

// fileDiag is what the session remembers about one file's diagnostics.
type fileDiag struct {
	attempts    int
	errors      int
	firstErrors int
	sig         string
	filed       string // short board id once filed
	filedSig    string
}

// diagTracker holds per-file history for the session.
type diagTracker struct {
	mu    sync.Mutex
	files map[string]*fileDiag
}

// errorRows returns the error-severity rows of a report.
func errorRows(rep lsp.Report) []lsp.Row {
	var out []lsp.Row
	for _, r := range rep.Rows {
		if r.Severity == lsp.SeverityError {
			out = append(out, r)
		}
	}
	return out
}

// errorSignature identifies a set of errors independent of where they sit in
// the file, so moving code does not read as new errors: the source, code and
// message of each, sorted.
func errorSignature(rows []lsp.Row) string {
	keys := make([]string, len(rows))
	for i, r := range rows {
		keys[i] = r.Source + "|" + r.Code + "|" + sanitize.Line(r.Message, 160)
	}
	sort.Strings(keys)
	return strings.Join(keys, "\n")
}

// triageDiagnostics is called with the report of a Write or Edit. It returns a
// harness note to append to the tool result, or "".
func (s *Session) triageDiagnostics(ctx context.Context, abs string, rep lsp.Report) string {
	if s.jev == nil || !s.jev.Enabled(config.JevLSPTriage) || s.kanban == nil || !s.kanban.on || s.kanban.worker ||
		s.planMode || s.kanban.base.Store == nil {
		return ""
	}
	if rep.Status != lsp.StatusReady && rep.Status != lsp.StatusFallback {
		return "" // no answer this time: nothing to learn from it
	}
	errs := errorRows(rep)
	t := &s.diagHist
	t.mu.Lock()
	if t.files == nil {
		t.files = map[string]*fileDiag{}
	}
	if len(errs) == 0 {
		delete(t.files, abs)
		t.mu.Unlock()
		return ""
	}
	h := t.files[abs]
	sig := errorSignature(errs)
	if h == nil || (h.filed != "" && h.filedSig != sig) {
		// A new problem, or the filed one changed shape: start counting again.
		h = &fileDiag{firstErrors: len(errs)}
		t.files[abs] = h
	}
	prev := h.errors
	prevSig := h.sig
	h.attempts++
	h.errors, h.sig = len(errs), sig
	attempts, first := h.attempts, h.firstErrors
	filed := h.filed
	t.mu.Unlock()

	rel := s.relPath(abs)
	if filed != "" {
		return fmt.Sprintf("[harness: the %d error(s) still reported in %s are filed on the board as %s; do not keep editing this file for them]", len(errs), rel, filed)
	}
	if attempts < triageMinAttempt {
		return ""
	}

	start := time.Now()
	trend := "the errors are the same as after the last pass"
	switch {
	case sig != prevSig && len(errs) < prev:
		trend = "there are fewer errors than after the last pass"
	case sig != prevSig && len(errs) > prev:
		trend = "there are more errors than after the last pass"
	case sig != prevSig:
		trend = "the errors differ from the last pass"
	}
	facts := fmt.Sprintf("Edit pass %d on this file. Errors now: %d. Errors after the first pass: %d. Errors after the previous pass: %d. %s.", attempts, len(errs), first, prev, capitalise(trend))
	p, res, answered := s.jev.RateRepair(ctx, facts, repairRows(errs))
	unlikely := false
	if answered {
		unlikely = p < config.ActiveJevThresholds().TriageAt
	} else {
		unlikely = attempts >= s.settings.LSPMaxRepairAttemptsOr(defaultRepairAttempts) && len(errs) >= prev
	}
	if !unlikely {
		rolemanager.RecordLSPTriage("retry", attempts, len(errs), pct(p, answered), res.Identity, time.Since(start))
		return ""
	}
	id, err := s.fileDiagnosticsBug(abs, errs, attempts)
	if err != nil {
		rolemanager.RecordLSPTriage("unfiled", attempts, len(errs), pct(p, answered), res.Identity, time.Since(start))
		return ""
	}
	t.mu.Lock()
	if cur := t.files[abs]; cur != nil {
		cur.filed, cur.filedSig = id, sig
	}
	t.mu.Unlock()
	rolemanager.RecordLSPTriage("filed", attempts, len(errs), pct(p, answered), res.Identity, time.Since(start))
	return fmt.Sprintf("[harness: another edit is unlikely to clear the %d error(s) still reported in %s after %d passes, so a bug was filed on the board as %s. Do not keep editing this file for them. Continue with your remaining goal, plan and todo steps, and say in your report that %s is open.]",
		len(errs), rel, attempts, id, id)
}

// fileDiagnosticsBug adds a board item for the file's errors and returns its
// short id. The title, body and labels are the harness's; the rows are cleaned
// and bounded. A live item with the same title (the same file) is reused.
func (s *Session) fileDiagnosticsBug(abs string, errs []lsp.Row, attempts int) (string, error) {
	rel := s.relPath(abs)
	body := fmt.Sprintf("The language server still reports %d error(s) in %s after %d edit passes; the harness judged another pass unlikely to clear them.\n\n%s",
		len(errs), rel, attempts, repairRows(errs))
	item, _, err := s.kanban.base.Store.Add(kanban.ItemInput{
		Title:  "LSP errors remain in " + rel,
		Body:   body,
		List:   kanban.Review,
		Labels: []string{"bug", "lsp"},
	}, s.kanban.base.Source.Get())
	if err != nil {
		return "", err
	}
	return item.Short(), nil
}

// repairRows lists errors one per line, at most triageMaxRows, cleaned.
func repairRows(errs []lsp.Row) string {
	var b strings.Builder
	for i, r := range errs {
		if i == triageMaxRows {
			fmt.Fprintf(&b, "… and %d more\n", len(errs)-triageMaxRows)
			break
		}
		code := ""
		if r.Code != "" {
			code = " " + r.Code
		}
		fmt.Fprintf(&b, "line %d %s%s: %s\n", r.Line, sanitize.Ident(r.Source, 24), code, sanitize.Line(r.Message, 200))
	}
	return strings.TrimRight(b.String(), "\n")
}

// relPath is abs relative to the working directory when it lies inside it,
// else the file name.
func (s *Session) relPath(abs string) string {
	if s.workdir != "" {
		if rel, err := filepath.Rel(s.workdir, abs); err == nil && !strings.HasPrefix(rel, "..") {
			return sanitize.Line(filepath.ToSlash(rel), 160)
		}
	}
	return sanitize.Line(filepath.Base(abs), 160)
}

func capitalise(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func pct(p float64, answered bool) int {
	if !answered {
		return -1
	}
	return int(p*100 + 0.5)
}
