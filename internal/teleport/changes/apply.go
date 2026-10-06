package changes

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/vulnetix/belai/internal/forge"
)

// Failure is one file whose part of the patch did not apply, with the part
// itself, so the model that finishes the job is shown exactly what is left.
type Failure struct {
	Path string
	// Status is added, modified or deleted.
	Status string
	// Reason is git's explanation, cleaned and capped. It is third-party text
	// about a third-party patch, so it reaches a model only as a classified
	// attachment (agent.TurnInput.TeleportReplay).
	Reason string
	// Section is the file's part of the patch, exactly as it arrived.
	Section string
}

// Result is what applying a patch did.
type Result struct {
	Applied []string
	Failed  []Failure
}

// Apply applies each file's part of a patch in dir, a worktree root the caller
// made for this teleport. Each part is checked (`git apply --check`) and, when
// it passes, applied to the working tree only (the index is left alone) by git,
// which refuses a path through a symbolic link or outside the repository. A part
// that does not pass is reported, never forced: the next step (a model repairs
// it) sees exactly what is left. The sections must have come from ParsePatch.
func Apply(ctx context.Context, mk Runners, dir string, secs []Section) (Result, error) {
	if mk == nil {
		mk = Hardened
	}
	g := mk()
	root, err := run(ctx, g, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return Result{}, fmt.Errorf("not a git repository: %s", forge.CleanErr(err))
	}
	if !sameDir(root, dir) {
		return Result{}, errors.New("the directory is not a worktree root, so nothing was applied")
	}
	tmp, err := os.MkdirTemp("", "belai-teleport-patch-")
	if err != nil {
		return Result{}, err
	}
	defer os.RemoveAll(tmp)
	var res Result
	for i, s := range secs {
		if !SafePath(s.Path) {
			res.Failed = append(res.Failed, Failure{Path: displayPath(s.Path), Status: s.Status, Reason: "not a path a patch may change", Section: s.Text})
			continue
		}
		file := filepath.Join(tmp, fmt.Sprintf("%04d.patch", i))
		if err := os.WriteFile(file, []byte(s.Text), 0o600); err != nil {
			return res, err
		}
		if _, err := run(ctx, g, root, "apply", "--check", "--whitespace=nowarn", "--", file); err != nil {
			res.Failed = append(res.Failed, Failure{Path: s.Path, Status: s.Status, Reason: reasonOf(err), Section: s.Text})
			continue
		}
		if _, err := run(ctx, g, root, "apply", "--whitespace=nowarn", "--", file); err != nil {
			res.Failed = append(res.Failed, Failure{Path: s.Path, Status: s.Status, Reason: reasonOf(err), Section: s.Text})
			continue
		}
		res.Applied = append(res.Applied, s.Path)
	}
	return res, nil
}

func reasonOf(err error) string {
	r := forge.CleanErr(err)
	// git names the patch file in its message; the path is a temporary one.
	if i := strings.Index(r, "error: "); i >= 0 {
		r = r[i+len("error: "):]
	}
	return r
}

func sameDir(a, b string) bool {
	ra, err1 := filepath.EvalSymlinks(a)
	rb, err2 := filepath.EvalSymlinks(b)
	return err1 == nil && err2 == nil && ra == rb
}

// Matches reports whether dir's working tree, built the way Collect builds the
// origin's, is the tree the origin sent. An exact match is a verification that
// needs no model; a mismatch is not an error (a model finishes the job and the
// check runs again).
func Matches(ctx context.Context, mk Runners, dir, wantTree string) (bool, string, error) {
	if mk == nil {
		mk = Hardened
	}
	if !shaShape.MatchString(wantTree) {
		return false, "", errors.New("the origin's tree is not a tree id")
	}
	tree, _, _, err := WorkTree(ctx, mk, dir)
	if err != nil {
		return false, "", err
	}
	return tree == wantTree, tree, nil
}

// Changed lists the paths dir's working tree changes against HEAD, built the way
// Collect builds the origin's, so a verifier is shown which files the replay left
// different from the base. Paths only, never content.
func Changed(ctx context.Context, mk Runners, dir string) ([]string, error) {
	if mk == nil {
		mk = Hardened
	}
	tree, _, _, err := WorkTree(ctx, mk, dir)
	if err != nil {
		return nil, err
	}
	raw, err := runRaw(ctx, mk("GIT_OPTIONAL_LOCKS=0"), dir, "diff", "--name-only", "-z", "--no-renames", "HEAD", tree)
	if err != nil {
		return nil, fmt.Errorf("diff: %s", forge.CleanErr(err))
	}
	var out []string
	for _, p := range strings.Split(string(raw), "\x00") {
		if p != "" && SafePath(p) {
			out = append(out, p)
		}
	}
	return out, nil
}

// FileCheck is one file of the patch compared with the checkout: whether it is
// now exactly as the origin left it.
type FileCheck struct {
	Path   string
	Status string
	Match  bool
}

// Check compares each file the patch changes with dir's working tree: a deleted
// file must be gone, any other must hash to the object id the origin's patch
// names. Where the patch names none (a mode-only change) the file must exist.
// It reads files only through git and never writes. The result is harness fact:
// paths and a yes or no, which is what the verifier is shown.
func Check(ctx context.Context, mk Runners, dir string, secs []Section) []FileCheck {
	if mk == nil {
		mk = Hardened
	}
	g := mk()
	out := make([]FileCheck, 0, len(secs))
	for _, s := range secs {
		c := FileCheck{Path: s.Path, Status: s.Status}
		full := filepath.Join(dir, filepath.FromSlash(s.Path))
		_, err := os.Lstat(full)
		switch {
		case !SafePath(s.Path):
		case s.Status == "deleted":
			c.Match = os.IsNotExist(err)
		case err != nil:
		case s.Blob == "":
			c.Match = true
		default:
			if got, herr := run(ctx, g, dir, "hash-object", "--path="+s.Path, "--", s.Path); herr == nil {
				c.Match = got == s.Blob
			}
		}
		out = append(out, c)
	}
	return out
}
