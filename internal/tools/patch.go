package tools

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/vulnetix/belai/internal/patchfmt"
	"github.com/vulnetix/belai/internal/permissions"
	"github.com/vulnetix/belai/internal/sanitize"
)

// ApplyPatchName is the apply_patch tool's name.
const ApplyPatchName = "apply_patch"

// ApplyPatch changes several files in one call, all or none. The harness parses
// the envelope (internal/patchfmt), anchors every hunk in exactly one place,
// builds every file in memory, checks each against the session's read record
// and then writes them; one failure at any step leaves every file as it was
// (docs/patch-tool.md).
type ApplyPatch struct {
	Cwd      *Cwd
	Root     string
	MaxBytes int64
	// Reads, when set, refuses a change to a file not read this session or
	// changed since; see ReadState.
	Reads *ReadState
	// Tolerance is how far a hunk's anchor may stray from the file. The zero
	// value is strict; sessions set it from patch_tolerance.
	Tolerance patchfmt.Tolerance

	// write is the seam the tests use to fail a write part way. Nil means
	// writeFileAtomic. Rollback never uses it.
	write func(path string, content []byte) error
}

// Definition returns the static tool metadata.
func (p *ApplyPatch) Definition() Definition {
	return Definition{
		Name: ApplyPatchName,
		Description: "Change one or more files in a single atomic call: every file changes or none does. `input` is the whole patch:\n" +
			"*** Begin Patch\n*** Update File: path\n@@ optional line of the file that the hunk follows\n context line\n-line to remove\n+line to add\n*** Add File: path\n+every line of the new file\n*** Delete File: path\n*** End Patch\n" +
			"An Update has one or more hunks. Each hunk line starts with a space (context), `-` (remove) or `+` (add). Put enough context around a change that its old lines occur in one place in the file; a hunk whose old lines occur more than once, or not at all, fails the whole patch. Start a further hunk with `@@`. An `@@ line` must be one whole line that occurs once in the file, and the hunk is searched for after it. End a hunk at the end of the file with `*** End of File`. " +
			"Hunk anchoring is by whole lines: exact first, then, as the session's patch_tolerance allows, line endings folded, trailing spaces and tabs ignored, one consistent indentation shift, and last of all typographic punctuation folded to ASCII. The first step that finds exactly one place decides, the new lines are fitted to the file's line endings and indentation, and the result says which step matched. Hunks are located in the file as it is now, so their order does not matter and two may not overlap. Rename and move are not supported. Paths are relative to the working directory or absolute under a session root. At most 64 files, 256 hunks and 1 MiB. " +
			"Every file you Update or Delete must have been Read in this session and be unchanged since; an Add path must not exist. An error names the hunk and a path or line number, never file text: Read the file again and correct the patch. " +
			"The argument is `input`, a single string, as other patch tools take it. Mutating, so it asks for approval unless an explicit allow rule matches, and it is unavailable in plan mode.",
		Properties: map[string]Property{
			"input": {Type: "string", Description: "The whole patch, from `*** Begin Patch` to `*** End Patch`"},
		},
		Required: []string{"input"},
	}
}

// Kind returns the tool kind.
func (p *ApplyPatch) Kind() Kind { return KindEdit }

// Mutates reports that apply_patch always mutates the workspace.
func (p *ApplyPatch) Mutates() bool { return true }

// parse reads the patch argument.
func (p *ApplyPatch) parse(args map[string]any) (*patchfmt.Patch, error) {
	in, ok := argString(args, "input")
	if !ok || strings.TrimSpace(in) == "" {
		return nil, errors.New("missing input argument: pass the whole patch as `input`")
	}
	return patchfmt.Parse(in)
}

// Subject returns the first file of the patch as the permission subject. The
// full set is PermissionSubjects.
func (p *ApplyPatch) Subject(args map[string]any) string {
	pt, err := p.parse(args)
	if err != nil || len(pt.Files) == 0 {
		return ""
	}
	return subjectPath(p.Root, p.Cwd, pt.Files[0].Path)
}

// PermissionSubjects names every file the patch touches, as the single-file
// tool that would make the same change: an Add is a Write, an Update or Delete
// is an Edit. A rule written for Edit(...) or Write(...) therefore covers a
// patch, a deny on any file denies the whole call and an allow must cover them
// all. It is nil when the patch does not parse; the call then fails in Execute
// before touching anything.
func (p *ApplyPatch) PermissionSubjects(args map[string]any) []permissions.Subject {
	pt, err := p.parse(args)
	if err != nil {
		return nil
	}
	out := make([]permissions.Subject, 0, len(pt.Files))
	for _, f := range pt.Files {
		tool := "Edit"
		if f.Op == patchfmt.OpAdd {
			tool = "Write"
		}
		out = append(out, permissions.Subject{Tool: tool, Path: subjectPath(p.Root, p.Cwd, f.Path)})
	}
	return out
}

// Targets returns the resolved relative paths of every file the patch names,
// for the diff recorder and the read index. A path that does not resolve is
// left out: Execute refuses it.
func (p *ApplyPatch) Targets(args map[string]any) []string {
	pt, err := p.parse(args)
	if err != nil {
		return nil
	}
	var out []string
	for _, f := range pt.Files {
		res, err := p.resolve(f)
		if err != nil {
			continue
		}
		out = append(out, res.Rel)
	}
	return out
}

func (p *ApplyPatch) resolve(f patchfmt.File) (resolved, error) {
	if f.Op == patchfmt.OpAdd {
		return resolveNewPath(p.Root, p.Cwd, f.Path)
	}
	return resolvePath(p.Root, p.Cwd, f.Path)
}

// PreviewFile is one file's before and after, for the approval diff.
type PreviewFile struct {
	Path     string
	Old, New string
}

// PreviewFiles returns every file's before and after without touching disk,
// from the same plan Execute runs. It is empty when the patch would not apply.
func (p *ApplyPatch) PreviewFiles(args map[string]any) []PreviewFile {
	pt, err := p.parse(args)
	if err != nil {
		return nil
	}
	chs, err := p.prepare(pt, false)
	if err != nil {
		return nil
	}
	out := make([]PreviewFile, 0, len(chs))
	for _, c := range chs {
		out = append(out, PreviewFile{Path: c.rel, Old: string(c.old), New: string(c.new)})
	}
	return out
}

// change is one file of a patch, resolved and built in memory.
type change struct {
	op       patchfmt.Op
	rel, abs string
	old, new []byte
	mode     os.FileMode
	hunks    int
	notes    []patchfmt.Note
	// madeDirs are the directories an Add would create, deepest last.
	madeDirs []string
}

func (p *ApplyPatch) maxBytes() int64 {
	if p.MaxBytes <= 0 {
		return MaxWriteBytes
	}
	return p.MaxBytes
}

// prepare resolves every path, checks it and builds every file's new contents
// in memory. It changes nothing. enforceReads is false for the approval
// preview, so a file not read yet still shows its diff.
func (p *ApplyPatch) prepare(pt *patchfmt.Patch, enforceReads bool) ([]change, error) {
	max := p.maxBytes()
	tol := p.Tolerance
	seen := map[string]bool{}
	var out []change
	for _, f := range pt.Files {
		res, err := p.resolve(f)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", sanitize.Line(f.Path, 120), err)
		}
		abs := res.Abs()
		if seen[abs] {
			return nil, fmt.Errorf("%s appears twice once symbolic links are resolved; put every change to a file in one section", res.Rel)
		}
		seen[abs] = true
		c := change{op: f.Op, rel: res.Rel, abs: abs, hunks: len(f.Hunks)}
		fi, statErr := os.Lstat(abs)
		switch f.Op {
		case patchfmt.OpAdd:
			if statErr == nil {
				return nil, fmt.Errorf("%s already exists; use an Update section to change it", res.Rel)
			}
			if !os.IsNotExist(statErr) {
				return nil, statErr
			}
			if int64(len(f.Content)) > max {
				return nil, fmt.Errorf("%s: content exceeds %d bytes", res.Rel, max)
			}
			if strings.ContainsRune(f.Content, 0) {
				return nil, fmt.Errorf("%s: content contains NUL bytes", res.Rel)
			}
			c.new = []byte(f.Content)
			c.madeDirs = missingDirs(filepath.Dir(abs))
		default:
			if statErr != nil {
				if os.IsNotExist(statErr) {
					return nil, fmt.Errorf("%s does not exist", res.Rel)
				}
				return nil, statErr
			}
			if !fi.Mode().IsRegular() {
				return nil, fmt.Errorf("%s is not a regular file", res.Rel)
			}
			if enforceReads {
				if err := p.Reads.Check(abs, res.Rel); err != nil {
					return nil, err
				}
			}
			body, err := os.ReadFile(abs)
			if err != nil {
				return nil, err
			}
			if int64(len(body)) > max {
				return nil, fmt.Errorf("%s exceeds %d bytes", res.Rel, max)
			}
			if bytes.IndexByte(body, 0) >= 0 {
				return nil, fmt.Errorf("%s is binary (contains NUL bytes)", res.Rel)
			}
			c.old, c.mode = body, fi.Mode().Perm()
			if f.Op == patchfmt.OpUpdate {
				next, notes, err := patchfmt.Plan(res.Rel, string(body), f.Hunks, tol)
				if err != nil {
					return nil, err
				}
				if int64(len(next)) > max {
					return nil, fmt.Errorf("%s would exceed %d bytes", res.Rel, max)
				}
				if strings.ContainsRune(next, 0) {
					return nil, fmt.Errorf("%s: the result contains NUL bytes", res.Rel)
				}
				c.new, c.notes = []byte(next), notes
			}
		}
		out = append(out, c)
	}
	return out, nil
}

// missingDirs lists the directories at and above dir that do not exist yet,
// shallowest first.
func missingDirs(dir string) []string {
	var out []string
	for d := dir; ; d = filepath.Dir(d) {
		if _, err := os.Lstat(d); err == nil || !os.IsNotExist(err) {
			break
		}
		out = append([]string{d}, out...)
		if filepath.Dir(d) == d {
			break
		}
	}
	return out
}

// Execute applies the patch, failing closed in order: parse, resolve and check
// every path, anchor every hunk, then write. Any failure before the write
// leaves the tree untouched; a failure during it restores every file.
func (p *ApplyPatch) Execute(ctx context.Context, args map[string]any) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	pt, err := p.parse(args)
	if err != nil {
		return Result{}, err
	}
	chs, err := p.prepare(pt, true)
	if err != nil {
		return Result{}, err
	}
	if err := p.commit(chs); err != nil {
		return Result{}, err
	}
	var abs, rel []string
	for _, c := range chs {
		if c.op == patchfmt.OpDelete {
			continue
		}
		// The model wrote these bytes, so it knows them: a follow-up edit to
		// the same file needs no re-read.
		p.Reads.Note(c.abs)
		abs = append(abs, c.abs)
		rel = append(rel, c.rel)
	}
	return EditResultMeta(summarize(chs), map[string]any{"abs_paths": abs, "rel_paths": rel}), nil
}

// commit writes every change, or none. Updates and adds go first, deletes
// last, so a failed delete still has every other file to restore from memory.
func (p *ApplyPatch) commit(chs []change) error {
	write := p.write
	if write == nil {
		write = writeFileAtomic
	}
	order := make([]int, 0, len(chs))
	for i, c := range chs {
		if c.op != patchfmt.OpDelete {
			order = append(order, i)
		}
	}
	for i, c := range chs {
		if c.op == patchfmt.OpDelete {
			order = append(order, i)
		}
	}
	var done []int
	for _, i := range order {
		c := chs[i]
		var err error
		if c.op == patchfmt.OpDelete {
			err = os.Remove(c.abs)
		} else {
			err = write(c.abs, c.new)
		}
		if err != nil {
			stuck := p.rollback(chs, done, order[len(done):])
			if len(stuck) > 0 {
				return fmt.Errorf("the patch failed on %s (%v) and these files could not be restored: %s; check them with Read",
					c.rel, err, strings.Join(stuck, ", "))
			}
			return fmt.Errorf("the patch failed on %s (%v); every file was restored", c.rel, err)
		}
		done = append(done, i)
	}
	return nil
}

// rollback restores the files in done from memory, newest first, and removes
// the directories an Add created. pending is the rest of the order, whose
// directories may exist if a failed write made them. It returns the paths it
// could not restore.
func (p *ApplyPatch) rollback(chs []change, done, pending []int) []string {
	var stuck []string
	for k := len(done) - 1; k >= 0; k-- {
		c := chs[done[k]]
		var err error
		switch c.op {
		case patchfmt.OpAdd:
			err = os.Remove(c.abs)
		case patchfmt.OpUpdate:
			err = writeFileAtomic(c.abs, c.old)
		case patchfmt.OpDelete:
			if err = writeFileAtomic(c.abs, c.old); err == nil {
				err = os.Chmod(c.abs, c.mode)
			}
		}
		if err != nil {
			stuck = append(stuck, c.rel)
		}
	}
	// Directories last, deepest first; os.Remove refuses a non-empty one.
	var dirs []string
	for _, i := range append(append([]int{}, done...), pending...) {
		dirs = append(dirs, chs[i].madeDirs...)
	}
	sort.Slice(dirs, func(i, j int) bool { return len(dirs[i]) > len(dirs[j]) })
	for _, d := range dirs {
		_ = os.Remove(d)
	}
	return stuck
}

// summarize is the result the model sees, composed from paths, counts and the
// matching tier of each relaxed hunk.
func summarize(chs []change) string {
	var upd, add, del int
	for _, c := range chs {
		switch c.op {
		case patchfmt.OpUpdate:
			upd++
		case patchfmt.OpAdd:
			add++
		case patchfmt.OpDelete:
			del++
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "applied the patch to %d file%s (%d updated, %d added, %d deleted)", len(chs), plural(len(chs)), upd, add, del)
	for _, c := range chs {
		fmt.Fprintf(&b, "\n  %s %s", c.op, c.rel)
		if c.op == patchfmt.OpUpdate {
			fmt.Fprintf(&b, " (%d hunk%s", c.hunks, plural(c.hunks))
			for _, n := range c.notes {
				fmt.Fprintf(&b, "; hunk %d matched with %s", n.Hunk, n.Tier)
			}
			b.WriteString(")")
		}
	}
	return b.String()
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
