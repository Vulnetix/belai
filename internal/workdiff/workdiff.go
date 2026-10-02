// Package workdiff collects the working tree's git changes for the /diff pane:
// staged, unstaged and untracked files, each as a [filediff.FileChange] the
// existing diff renderer draws.
//
// The result is for the user's own display. It is never handed to a model, the
// transcript, telemetry, audit or sync, so this package has no classifier and
// no tool kind; it does sanitise every string it returns, because file text
// reaches the user's terminal and must not carry escape sequences.
//
// Every git call is read-only and hardened like the fleet's: repository hooks
// and fsmonitor off, no file transport, no credential prompt, no optional
// locks (status never rewrites the index), the scrubbed environment, its own
// process group and no stdin. The working-tree side of a file is read by this
// package, never through a symlink, and is bounded.
//
// A path the caller's hide function reports (a Read deny rule) is listed by
// name and status only: its content is not read from disk or from git.
package workdiff

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/vulnetix/belai/internal/filediff"
	"github.com/vulnetix/belai/internal/proc"
	"github.com/vulnetix/belai/internal/sanitize"
)

const (
	// MaxFiles bounds how many changed files are expanded into diffs.
	MaxFiles = 300
	// MaxBytes bounds one side of one file; a larger file is listed as too
	// large to diff.
	MaxBytes = 1 << 20
	// maxGitOutput bounds the status listing.
	maxGitOutput = 8 << 20
)

// File is one changed path.
type File struct {
	Path   string // relative to the repository root, cleaned for display
	Status string // two-letter porcelain status, "??" for untracked

	Staged    bool
	Unstaged  bool
	Untracked bool
	Conflict  bool

	// Hidden: a Read deny rule covers the path, so no content was read.
	Hidden bool
	// Note explains an absent diff ("symbolic link", "too large to diff").
	Note string

	Change         filediff.FileChange
	Added, Removed int
}

// Snapshot is everything the pane shows.
type Snapshot struct {
	Root  string
	Files []File

	Staged, Unstaged, Untracked int
	Added, Removed              int

	// More counts changed files beyond MaxFiles that are not listed.
	More int
	// Unavailable says, in words for the user, why there is nothing to show
	// (not a git repository, git missing).
	Unavailable string
}

// Collect reads the working tree's changes under workdir. hide, when non-nil,
// is asked for each repository-relative slash path and returns true to list it
// by name only.
func Collect(ctx context.Context, workdir string, hide func(rel string) bool) Snapshot {
	top, err := git(ctx, workdir, "rev-parse", "--show-toplevel")
	if err != nil {
		return Snapshot{Unavailable: "not a git repository (or git is not available)"}
	}
	snap := Snapshot{Root: strings.TrimSpace(string(top))}
	_, headErr := git(ctx, snap.Root, "rev-parse", "--verify", "--quiet", "HEAD^{commit}")
	hasHead := headErr == nil

	out, err := git(ctx, snap.Root, "status", "--porcelain=v1", "-z", "--untracked-files=all", "--no-renames")
	if err != nil {
		snap.Unavailable = "git status failed"
		return snap
	}
	entries := parseStatus(out)
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	for _, e := range entries {
		switch {
		case e.Untracked:
			snap.Untracked++
		default:
			if e.Staged {
				snap.Staged++
			}
			if e.Unstaged {
				snap.Unstaged++
			}
		}
	}
	for i, e := range entries {
		if i >= MaxFiles {
			snap.More = len(entries) - MaxFiles
			break
		}
		f := build(ctx, snap.Root, hasHead, e, hide)
		snap.Added += f.Added
		snap.Removed += f.Removed
		snap.Files = append(snap.Files, f)
	}
	return snap
}

// parseStatus reads `git status --porcelain=v1 -z --no-renames` output.
func parseStatus(out []byte) []File {
	var files []File
	for _, rec := range bytes.Split(out, []byte{0}) {
		if len(rec) < 4 || rec[2] != ' ' {
			continue
		}
		x, y := rec[0], rec[1]
		f := File{Path: string(rec[3:]), Status: string(rec[:2])}
		switch {
		case x == '?' && y == '?':
			f.Untracked = true
		case x == '!' && y == '!':
			continue
		default:
			f.Staged = x != ' ' && x != 'U'
			f.Unstaged = y != ' ' && y != 'U'
			f.Conflict = x == 'U' || y == 'U' || (x == 'A' && y == 'A') || (x == 'D' && y == 'D')
		}
		files = append(files, f)
	}
	return files
}

func build(ctx context.Context, root string, hasHead bool, f File, hide func(string) bool) File {
	raw := f.Path
	f.Path = sanitize.Line(raw, 400)
	if hide != nil && hide(filepath.ToSlash(raw)) {
		f.Hidden = true
		f.Note = "hidden: a Read deny rule covers this path"
		return f
	}
	abs := filepath.Join(root, filepath.FromSlash(raw))

	var old, cur string
	// An untracked or newly added file has no committed side.
	if hasHead && !f.Untracked && f.Status[0] != 'A' {
		o, note := readHead(ctx, root, raw)
		if note != "" {
			f.Note = note
			return f
		}
		old = o
	}
	st, err := os.Lstat(abs)
	switch {
	case err == nil:
		switch {
		case st.Mode()&os.ModeSymlink != 0:
			f.Note = "symbolic link"
			return f
		case !st.Mode().IsRegular():
			f.Note = "not a regular file"
			return f
		case st.Size() > MaxBytes:
			f.Note = "too large to diff"
			return f
		}
		b, err := os.ReadFile(abs)
		if err != nil {
			f.Note = "unreadable"
			return f
		}
		cur = string(b)
	case !errors.Is(err, os.ErrNotExist):
		f.Note = "unreadable"
		return f
	}

	ch := filediff.Preview(f.Path, old, cur)
	if len(ch.Files) == 0 {
		f.Note = "mode or metadata change only"
		return f
	}
	fc := ch.Files[0]
	if !fc.Binary {
		fc.Old, fc.New = sanitize.Text(fc.Old), sanitize.Text(fc.New)
	}
	f.Change = fc
	f.Added, f.Removed = filediff.Stat(fc.Rows())
	return f
}

// readHead returns the committed content of path, or a note when it cannot be
// shown. A path absent from HEAD is a new file: empty content, no note.
func readHead(ctx context.Context, root, path string) (string, string) {
	spec := "HEAD:" + path
	sz, err := git(ctx, root, "cat-file", "-s", spec)
	if err != nil {
		return "", ""
	}
	n, err := strconv.ParseInt(strings.TrimSpace(string(sz)), 10, 64)
	if err != nil || n > MaxBytes {
		return "", "too large to diff"
	}
	b, err := git(ctx, root, "show", spec)
	if err != nil {
		return "", ""
	}
	return string(b), ""
}

// git runs one hardened, read-only git command in dir.
func git(ctx context.Context, dir string, args ...string) ([]byte, error) {
	argv := append([]string{
		"--no-optional-locks",
		"-c", "core.hooksPath=" + os.DevNull,
		"-c", "core.fsmonitor=false",
		"-c", "core.quotepath=false",
		"-c", "protocol.file.allow=never",
		"-c", "credential.interactive=never",
	}, args...)
	cmd := exec.CommandContext(ctx, "git", argv...)
	cmd.Dir = dir
	cmd.Env = append(proc.ScrubbedEnv(), "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0", "GIT_PAGER=cat")
	proc.SetProcessGroup(cmd)
	var stdout bytes.Buffer
	cmd.Stdout = &limitWriter{w: &stdout, left: maxGitOutput + MaxBytes}
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	return stdout.Bytes(), nil
}

// limitWriter fails once more than left bytes arrive, so a hostile repository
// cannot make the pane buffer without bound.
type limitWriter struct {
	w    *bytes.Buffer
	left int
}

func (l *limitWriter) Write(p []byte) (int, error) {
	if len(p) > l.left {
		return 0, errors.New("workdiff: git output too large")
	}
	l.left -= len(p)
	return l.w.Write(p)
}
