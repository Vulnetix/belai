// Package lintloop is the per-edit lint loop (docs/lint.md): after a Write,
// Edit or apply_patch the harness runs the repository's detected linter on the
// edited file and hands the findings back on the same tool result.
//
// Everything here is the harness's. The argv is the fixed per-file form from
// internal/lintdetect and its one varying operand is a path the harness
// validated against the repository root, so no model, profile or settings file
// names a command. The run goes through internal/testrun: no shell, the user's
// permission rules, the call's OS sandbox policy, the scrubbed environment and
// a process group. A deny rule skips the run, and an ask rule skips it too,
// because an edit never stops to prompt.
//
// The linter's output is arbitrary text a hostile file can shape. This package
// keeps only rows of the form path:line:col: message for the edited file,
// bounded in number and length, and the caller must sanitise the rendered
// block, classify it as tools.KindLint and seal it before a model reads it.
// Raw output is never returned, logged or persisted. When nothing can be said
// with confidence (no linter, an unsupported file, a timeout, a missing tool, a
// configuration error, a clean run) the result carries no rows, and the loop
// never claims the file is clean.
package lintloop

import (
	"context"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/vulnetix/belai/internal/lintdetect"
	"github.com/vulnetix/belai/internal/lsp"
	"github.com/vulnetix/belai/internal/permissions"
	"github.com/vulnetix/belai/internal/sanitize"
	"github.com/vulnetix/belai/internal/testrun"
)

// Status is how one lint run ended.
type Status string

const (
	// Findings means the linter reported something. Rows may still be empty
	// when every finding was in another file.
	Findings Status = "findings"
	// Clean means the linter exited 0 and reported no row for the file.
	Clean Status = "clean"
	// Skipped means nothing ran or nothing could be read; Reason says why.
	Skipped Status = "skipped"
)

// Reason is why a run was skipped. It is a closed set of harness words.
type Reason string

const (
	ReasonNoLinter      Reason = "no_linter"
	ReasonUnsupported   Reason = "unsupported_file"
	ReasonOutsideRoot   Reason = "outside_root"
	ReasonDenied        Reason = "denied"
	ReasonNeedsApproval Reason = "needs_approval"
	ReasonTimeout       Reason = "timeout"
	ReasonToolMissing   Reason = "tool_missing"
	ReasonStartFailed   Reason = "start_failed"
	ReasonToolError     Reason = "tool_error"
)

// Bounds on what one run keeps. The output cap bounds the process text held;
// the row and length caps bound what is parsed from it.
const (
	maxOutputBytes = 32 * 1024
	maxScanLines   = 2000
	maxParsedRows  = 200
	maxMessageLen  = 400
	detectTTL      = 30 * time.Second
)

// Options configures one run.
type Options struct {
	// Root is the trusted repository root: the linter's working directory and
	// the base the edited file's path is validated against.
	Root string
	// Timeout bounds the linter. Zero means 10 seconds.
	Timeout time.Duration
	// Perms is the user's permission configuration.
	Perms permissions.Settings
}

// Report is the outcome of one run. It holds parsed rows and counts only.
type Report struct {
	Status Status
	Reason Reason
	// Linter is the table name ("lint-ruff") and Tool the program ("ruff").
	Linter, Tool string
	// Rows are the findings in the edited file, in the order the linter gave
	// them, at most 200.
	Rows []lsp.Row
	// Other counts the findings the same run reported in other files.
	Other    int
	ExitCode int
	Duration time.Duration
}

// detectCache holds lintdetect.Detect results briefly, so a burst of edits
// reads the marker files once. A newly added rule file is seen within the TTL.
var detectCache struct {
	mu sync.Mutex
	m  map[string]detected
}

type detected struct {
	at time.Time
	ls []lintdetect.Linter
}

func linters(root string) []lintdetect.Linter {
	detectCache.mu.Lock()
	defer detectCache.mu.Unlock()
	if d, ok := detectCache.m[root]; ok && time.Since(d.at) < detectTTL {
		return d.ls
	}
	if detectCache.m == nil || len(detectCache.m) > 32 {
		detectCache.m = map[string]detected{}
	}
	ls := lintdetect.Detect(root)
	detectCache.m[root] = detected{at: time.Now(), ls: ls}
	return ls
}

func skipped(r Reason) Report { return Report{Status: Skipped, Reason: r} }

// Run lints the file abs. It never returns an error: every way the run can
// fail is a Skipped report.
func Run(ctx context.Context, opts Options, abs string) (rep Report) {
	defer func() {
		if recover() != nil {
			rep = skipped(ReasonToolError)
		}
	}()
	rel, ok := relInRoot(opts.Root, abs)
	if !ok {
		return skipped(ReasonOutsideRoot)
	}
	all := linters(opts.Root)
	if len(all) == 0 {
		return skipped(ReasonNoLinter)
	}
	var l lintdetect.Linter
	var argv []string
	for _, c := range all {
		if a, ok := lintdetect.FileArgv(c, rel); ok {
			l, argv = c, a
			break
		}
	}
	if argv == nil {
		return skipped(ReasonUnsupported)
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	res := testrun.Run(ctx, testrun.Options{Dir: opts.Root, Timeout: timeout, Perms: opts.Perms, MaxBytes: maxOutputBytes}, l.Name, argv)
	out := Report{Linter: l.Name, Tool: l.Tool, ExitCode: res.ExitCode, Duration: res.Duration}
	switch res.Status {
	case testrun.Denied:
		out.Status, out.Reason = Skipped, ReasonDenied
	case testrun.NeedsApproval:
		out.Status, out.Reason = Skipped, ReasonNeedsApproval
	case testrun.TimedOut:
		out.Status, out.Reason = Skipped, ReasonTimeout
	case testrun.Errored:
		out.Status, out.Reason = Skipped, ReasonStartFailed
		if strings.Contains(res.Note, "command not found") {
			out.Reason = ReasonToolMissing
		}
	case testrun.Passed, testrun.Failed:
		// A failing exit that is not the tool's findings code is a
		// configuration or crash error: its text is not findings.
		if res.Status == testrun.Failed && (l.FindingsExit == 0 || res.ExitCode != l.FindingsExit) {
			out.Status, out.Reason = Skipped, ReasonToolError
			break
		}
		out.Rows, out.Other = parseRows(l.Tool, res.Output, rel, opts.Root)
		out.Status = Clean
		if res.Status == testrun.Failed || len(out.Rows) > 0 || out.Other > 0 {
			out.Status = Findings
		}
	default:
		out.Status, out.Reason = Skipped, ReasonStartFailed
	}
	return out
}

// relInRoot returns abs as a slash path relative to root, after resolving
// symlinks on both, and whether it lies inside root. A file reached through a
// link that leaves the root is outside.
func relInRoot(root, abs string) (string, bool) {
	if root == "" || abs == "" {
		return "", false
	}
	r, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", false
	}
	a, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", false
	}
	rel, err := filepath.Rel(r, a)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

var (
	rowRe      = regexp.MustCompile(`^(\S[^:\n]*?):(\d{1,7}):(?:(\d{1,7}):)?\s*(\S.*)$`)
	ruffCodeRe = regexp.MustCompile(`^([A-Z]{1,4}[0-9]{1,5})\s+(.*)$`)
	rubocopRe  = regexp.MustCompile(`^([CWEFR]): ([A-Za-z0-9_/]+): (.*)$`)
	golangciRe = regexp.MustCompile(`^(.*\S)\s+\(([A-Za-z0-9_-]{1,40})\)$`)
)

// parseRows reads path:line:col: message rows from a linter's output. Rows for
// the edited file (rel) become lsp.Rows; rows for any other path are only
// counted. Anything that is not a row is ignored, which is what keeps prose a
// file wrote into a message from becoming anything but one row's text.
func parseRows(tool, output, rel, root string) (rows []lsp.Row, other int) {
	lines := strings.Split(output, "\n")
	if len(lines) > maxScanLines {
		lines = lines[:maxScanLines]
	}
	for _, line := range lines {
		line = strings.TrimRight(line, "\r")
		m := rowRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		if !sameFile(m[1], rel, root) {
			other++
			continue
		}
		if len(rows) >= maxParsedRows {
			continue
		}
		n, _ := strconv.Atoi(m[2])
		c, _ := strconv.Atoi(m[3])
		rows = append(rows, buildRow(tool, n, c, m[4]))
	}
	return rows, other
}

// sameFile reports whether the path a linter printed names rel. Linters print
// the path as given, relative to the working directory, or absolute.
func sameFile(printed, rel, root string) bool {
	p := strings.TrimSpace(printed)
	if filepath.IsAbs(p) {
		for _, base := range []string{root, realOr(root)} {
			if r, err := filepath.Rel(base, p); err == nil && r == filepath.FromSlash(rel) {
				return true
			}
		}
		return false
	}
	return path.Clean(filepath.ToSlash(p)) == rel
}

func realOr(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

// buildRow shapes one row. The source is the table's tool name, so a message
// can never choose it. The code, where the tool prints one, is cleaned to
// identifier characters.
func buildRow(tool string, line, col int, msg string) lsp.Row {
	sev := lsp.SeverityWarning
	code := ""
	msg = sanitize.Line(msg, maxMessageLen)
	switch tool {
	case "ruff":
		if m := ruffCodeRe.FindStringSubmatch(msg); m != nil {
			code, msg = m[1], m[2]
		}
	case "rubocop":
		if m := rubocopRe.FindStringSubmatch(msg); m != nil {
			if m[1] == "E" || m[1] == "F" {
				sev = lsp.SeverityError
			}
			code, msg = m[2], m[3]
		}
	case "golangci-lint":
		if m := golangciRe.FindStringSubmatch(msg); m != nil {
			msg, code = m[1], m[2]
		}
	}
	code = cleanCode(code)
	text := msg
	if code != "" {
		text = code + " " + msg
	}
	return lsp.Row{Severity: sev, Line: line, Col: col, Source: tool, Code: code, Message: text}
}

// cleanCode keeps the characters a rule code uses (letters, digits and . _ - /,
// as in "Style/Foo") and caps it at 40, so a code is never free text.
func cleanCode(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-', r == '/':
			b.WriteRune(r)
		}
		if b.Len() >= 40 {
			break
		}
	}
	return b.String()
}

// Signature identifies a set of rows independent of where they sit in the
// file, so moving code does not read as new findings: the source, code and
// message of each, sorted.
func Signature(rows []lsp.Row) string {
	keys := make([]string, len(rows))
	for i, r := range rows {
		keys[i] = r.Source + "|" + r.Code + "|" + sanitize.Line(r.Message, 160)
	}
	sort.Strings(keys)
	return strings.Join(keys, "\n")
}
