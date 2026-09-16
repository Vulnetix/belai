// Package filediff works out what a command changed on disk and turns it into
// rows a UI can draw.
//
// Write and Edit report their own targets through tools.Targeter, so their
// diffs are snapshotted around the call by path. Bash's changes are still
// observed, because inside a git repository that observation is authoritative:
// `git status` sees whatever happened regardless of how it happened — sed, a
// heredoc, a formatter, `make`, a test that rewrites its own fixtures.
// Outside a repository Bash falls back to inferring targets from the command,
// which is honest but lossy.
//
// The package is deliberately free of any rendering dependency. It produces
// data; internal/tui/components decides what it looks like.
package filediff

// Op is what one row of a rendered diff represents.
type Op uint8

const (
	OpContext Op = iota // unchanged line, shown for orientation
	OpDel               // line present before the change
	OpAdd               // line present after it
	OpElide             // stands for a run of unchanged lines that was skipped
)

// Span is a half-open range of cells within a Row's text.
type Span struct{ From, To int }

// Row is one line of a rendered diff.
//
// OldLine and NewLine are 1-based file line numbers, and are zero where they
// do not apply: an added line has no line number in the old file, a removed
// line has none in the new one.
type Row struct {
	Op               Op
	OldLine, NewLine int
	Text             string

	// Emph marks the parts of the line that actually changed, when that can be
	// determined unambiguously. See the gate in rows.go.
	Emph []Span
}

// FileChange is one file's before and after.
type FileChange struct {
	Path     string // relative to the workdir, for display
	Old, New string // "" when the file did not exist on that side

	Created   bool
	Deleted   bool
	Binary    bool // no text diff is possible
	Truncated bool // too large to diff
}

// Change is everything one command did.
//
// Unavailable is set when no diff could be produced, and says why in a form
// fit to show the user. It is not an error: outside a git repository, "I could
// not tell what that command changed" is the expected outcome for most
// commands, and the UI treats it as an absence rather than a failure.
type Change struct {
	Files       []FileChange
	Unavailable string
}

// Empty reports whether there is nothing to show.
func (c Change) Empty() bool { return len(c.Files) == 0 && c.Unavailable == "" }

// Preview builds a Change describing the effect of writing new content to a
// single path, without touching disk. It feeds the tool-permission gate so the
// user approves a concrete diff rather than a bare path. path is display-only.
func Preview(path, old, new string) Change {
	fc := FileChange{Path: path, Old: old, New: new}
	if fc.Old == "" && fc.New != "" {
		fc.Created = true
	}
	if fc.Old != "" && fc.New == "" {
		fc.Deleted = true
	}
	if isBinary(fc.Old) || isBinary(fc.New) {
		fc.Binary = true
		fc.Old, fc.New = "", ""
	}
	if fc.Old == fc.New && !fc.Created && !fc.Deleted && !fc.Binary {
		return Change{}
	}
	return Change{Files: []FileChange{fc}}
}
