package tools

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/vulnetix/belai/internal/fuzzy"
)

// Edit is the file-edit tool. It replaces a byte string. An exact match always
// wins; with Tolerant set, a whitespace-only relaxed match (see
// internal/fuzzy) is applied when it is the only one (docs/edit.md).
type Edit struct {
	Cwd      *Cwd
	Root     string
	MaxBytes int64
	// Reads, when set, refuses an edit to a file not read this session or
	// changed since; see ReadState.
	Reads *ReadState
	// Tolerant turns on the relaxed matching ladder and the near-miss
	// diagnostic. The zero value is strict: a hand-built Edit matches byte for
	// byte and returns the plain errors. Sessions set it from edit.tolerant.
	Tolerant bool
}

// Definition returns the static tool metadata.
func (e *Edit) Definition() Definition {
	return Definition{
		Name: "Edit",
		Description: "Edit an existing file under the working directory by replacing a string. " +
			"old_string should match the file byte for byte, including indentation: an exact match always wins. Read the file first, and copy only the text after the line-number prefix and tab that Read puts on each line. " +
			"When there is no exact match, Edit retries on whole lines in a fixed order: line endings folded, then trailing spaces and tabs ignored, then one consistent indentation shift, and applies the first that finds exactly one place, fitting new_string to the file's line endings and indentation. More than one such place, a mix of tabs and spaces, or mixed line endings fails instead of guessing. replace_all=true is always exact. When nothing matches, the error names the closest lines and how they differ. " +
			"The call fails, leaving the file byte-identical, when the file has not been Read in this session or changed on disk since it was, when it does not exist, is binary, is over 1 MiB, when old_string equals new_string, when old_string is not found, or when it matches more than once without replace_all=true. " +
			"The write is atomic. Mutating, so it asks for approval unless an explicit allow rule matches, and it is unavailable in plan mode.",
		Properties: map[string]Property{
			"file_path":   {Type: "string", Format: FormatPath, Description: "Path to the file to edit: an absolute filesystem path under one of the session roots, or relative to the working directory; the file must already exist"},
			"old_string":  {Type: "string", Description: "The exact bytes to replace; include surrounding context to make it unique"},
			"new_string":  {Type: "string", Description: "The replacement bytes; must differ from old_string"},
			"replace_all": {Type: "boolean", Description: "Replace every occurrence instead of requiring a unique match (default false)"},
		},
		Required: []string{"file_path", "old_string", "new_string"},
	}
}

// Kind returns the tool kind.
func (e *Edit) Kind() Kind { return KindEdit }

// Mutates reports that Edit always mutates the workspace.
func (e *Edit) Mutates() bool { return true }

// Subject returns the permission-rule subject (the lexical cleaned path).
func (e *Edit) Subject(args map[string]any) string {
	path, _ := argString(args, "file_path")
	return subjectPath(e.Root, e.Cwd, path)
}

// Targets returns the sanitised target path for the diff recorder.
func (e *Edit) Targets(args map[string]any) []string {
	path, _ := argString(args, "file_path")
	res, err := resolvePath(e.Root, e.Cwd, path)
	if err != nil {
		return nil
	}
	return []string{res.Rel}
}

// Preview returns the before/after contents for the approval diff without
// touching disk. It is best-effort: a preview is produced for the success
// shape even when Execute would later reject the call (e.g. a non-unique
// match), because the render-only diff must not be the thing that leaks or
// writes.
func (e *Edit) Preview(args map[string]any) (path, old, new string, ok bool) {
	pathArg, ok := argString(args, "file_path")
	if !ok || strings.TrimSpace(pathArg) == "" {
		return "", "", "", false
	}
	oldS, ok1 := argString(args, "old_string")
	newS, ok2 := argString(args, "new_string")
	if !ok1 || !ok2 {
		return "", "", "", false
	}
	res, err := resolvePath(e.Root, e.Cwd, pathArg)
	if err != nil {
		return "", "", "", false
	}
	body, err := os.ReadFile(res.Abs())
	if err != nil {
		return "", "", "", false
	}
	replaceAll, _ := argBool(args, "replace_all")
	// The same plan Execute runs, so the approval diff equals the written
	// bytes. When Execute would refuse, the preview falls back to the exact
	// replacement (the unchanged file when nothing matches).
	if p := e.plan(string(body), oldS, newS, replaceAll); p.err == nil {
		return res.Rel, string(body), p.out, true
	}
	return res.Rel, string(body), replaceEdit(string(body), oldS, newS, replaceAll), true
}

// Execute edits the file, failing closed in order: missing file, over
// MaxBytes, NUL (binary), old_string == new_string, zero matches, then
// multiple matches without replace_all. The size and NUL checks run before any
// matching, relaxed or not.
func (e *Edit) Execute(ctx context.Context, args map[string]any) (Result, error) {
	pathArg, ok := argString(args, "file_path")
	if !ok || strings.TrimSpace(pathArg) == "" {
		return Result{}, fmt.Errorf("missing path argument")
	}
	oldS, ok := argString(args, "old_string")
	if !ok {
		return Result{}, fmt.Errorf("missing old_string argument")
	}
	newS, ok := argString(args, "new_string")
	if !ok {
		return Result{}, fmt.Errorf("missing new_string argument")
	}
	replaceAll, _ := argBool(args, "replace_all")

	res, err := resolvePath(e.Root, e.Cwd, pathArg)
	if err != nil {
		return Result{}, err
	}
	full := res.Abs()
	if err := e.Reads.Check(full, res.Rel); err != nil {
		return Result{}, err
	}
	body, err := os.ReadFile(full)
	if err != nil {
		return Result{}, err
	}
	max := e.maxBytes()
	if int64(len(body)) > max {
		return Result{}, fmt.Errorf("file exceeds %d bytes", max)
	}
	if bytes.IndexByte(body, 0) >= 0 || strings.IndexByte(oldS, 0) >= 0 || strings.IndexByte(newS, 0) >= 0 {
		return Result{}, fmt.Errorf("binary file (contains NUL bytes)")
	}
	if oldS == newS {
		return Result{}, fmt.Errorf("old_string and new_string are identical")
	}

	p := e.plan(string(body), oldS, newS, replaceAll)
	if p.err != nil {
		return Result{}, e.explain(ctx, p, string(body), oldS, res.Rel, full)
	}
	replacements := p.replacements
	if err := writeFileAtomic(full, []byte(p.out)); err != nil {
		return Result{}, err
	}
	// The model wrote these bytes, so it knows them: a follow-up edit to the
	// same file needs no re-read.
	e.Reads.Note(full)
	plural := "s"
	if replacements == 1 {
		plural = ""
	}
	how := ""
	if p.tier > fuzzy.TierExact {
		how = ", matched with " + p.tier.String()
	}
	return EditResultMeta(
		fmt.Sprintf("edited %s (%d replacement%s%s)", res.Rel, replacements, plural, how),
		map[string]any{"abs_path": full, "rel_path": res.Rel},
	), nil
}

// editFailure says why a plan found nothing to apply.
type editFailure int

const (
	failNotFound editFailure = iota + 1
	failAmbiguous
	failRelaxedAmbiguous
	failProblem
	failRefit
)

// editPlan is the outcome of matching old_string against a body: the new body
// and how many replacements it holds, or the reason there is none. Execute and
// Preview build the same plan, so the approval diff is the written file.
type editPlan struct {
	out          string
	replacements int
	tier         fuzzy.Tier
	err          error
	fail         editFailure
	matches      int
	problem      fuzzy.Problem
}

// plan matches oldS in body. replace_all is always exact: a relaxed match is
// applied to one place only, so it never rewrites several lines the model did
// not spell out.
func (e *Edit) plan(body, oldS, newS string, replaceAll bool) editPlan {
	r := fuzzy.Locate(body, oldS, fuzzy.Options{Relaxed: e.Tolerant && !replaceAll})
	p := editPlan{tier: r.Tier, matches: r.Matches, problem: r.Problem}
	switch {
	case r.Matches == 0:
		p.fail, p.err = failNotFound, fmt.Errorf("old_string not found")
	case r.Tier == fuzzy.TierExact:
		if r.Matches > 1 && !replaceAll {
			p.fail, p.err = failAmbiguous, fmt.Errorf("old_string matches more than once")
			return p
		}
		p.replacements = r.Matches
		if !replaceAll {
			p.replacements = 1
		}
		p.out = replaceEdit(body, oldS, newS, replaceAll)
	case r.Matches > 1:
		p.fail, p.err = failRelaxedAmbiguous, fmt.Errorf("old_string has several relaxed matches")
	case r.Problem != fuzzy.ProblemNone:
		p.fail, p.err = failProblem, fmt.Errorf("%s", r.Problem)
	default:
		out, err := fuzzy.Apply(body, newS, r)
		if err != nil {
			p.fail, p.err = failRefit, err
			return p
		}
		p.out, p.replacements = out, 1
	}
	return p
}

// explain turns a failed plan into the error the model sees. Off Tolerant it is
// the plain error of a byte-exact edit. On, a miss names the nearest lines by
// number and measured kind of difference, with a cleaned snippet unless the
// classifier withheld an earlier Read of this file: a file it did not clear
// never has its text echoed through a diagnostic.
func (e *Edit) explain(ctx context.Context, p editPlan, body, oldS, rel, abs string) error {
	switch p.fail {
	case failAmbiguous:
		return fmt.Errorf("old_string appears %d times in %s; pass replace_all=true or include more surrounding context to make it unique", p.matches, rel)
	case failRelaxedAmbiguous:
		return fmt.Errorf("old_string has no exact match in %s and matches %d places once whitespace is relaxed (%s); include more surrounding context to make it unique", rel, p.matches, p.tier)
	case failProblem:
		return fmt.Errorf("old_string matches %s only after whitespace differences (%s), and %s, so nothing was changed; copy the lines exactly as Read shows them", rel, p.tier, p.problem)
	case failRefit:
		return fmt.Errorf("old_string matches %s only after whitespace differences (%s), but new_string cannot be fitted to the file's indentation or line endings; give new_string the same indentation as old_string", rel, p.tier)
	}
	if hasReadGutter(oldS) {
		return fmt.Errorf("old_string not found in %s; it carries Read's line-number prefix (the number and tab before each line), which is not part of the file — remove it and match the text after the tab", rel)
	}
	if !e.Tolerant {
		return fmt.Errorf("old_string not found in %s", rel)
	}
	near := fuzzy.Nearest(body, oldS, nearMisses)
	if len(near) == 0 {
		return fmt.Errorf("old_string not found in %s and no line resembles it; Read the file again", rel)
	}
	withheld := editWithheld(ctx)(abs)
	var b strings.Builder
	fmt.Fprintf(&b, "old_string not found in %s; closest lines:", rel)
	for _, n := range near {
		fmt.Fprintf(&b, "\n  line %d (%s differs)", n.Line, n.Differs)
		if !withheld {
			fmt.Fprintf(&b, ": %s", n.Snippet)
		}
	}
	if withheld {
		b.WriteString("\n(line text omitted: an earlier read of this file was withheld)")
	}
	return fmt.Errorf("%s", b.String())
}

// nearMisses is how many candidate lines a not-found error names.
const nearMisses = 3

type editWithheldKey struct{}

// WithEditWithheld returns ctx carrying the session's test for a file whose
// Read the classifier withheld. Edit omits line text from its near-miss
// diagnostic for such a file. The agent sets it on a model's own call.
func WithEditWithheld(ctx context.Context, withheld func(abs string) bool) context.Context {
	return context.WithValue(ctx, editWithheldKey{}, withheld)
}

func editWithheld(ctx context.Context) func(string) bool {
	if f, ok := ctx.Value(editWithheldKey{}).(func(string) bool); ok && f != nil {
		return f
	}
	return func(string) bool { return false }
}

func (e *Edit) maxBytes() int64 {
	if e.MaxBytes <= 0 {
		return MaxWriteBytes
	}
	return e.MaxBytes
}

// hasReadGutter reports whether every line of s starts with Read's `cat -n`
// prefix — digits, optionally left-padded with spaces, then a tab — which is
// the shape of an old_string copied from a Read result along with its
// numbers. It only shapes an error message; matching stays byte-exact.
func hasReadGutter(s string) bool {
	if s == "" {
		return false
	}
	for _, line := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		tab := strings.IndexByte(line, '\t')
		if tab < 1 {
			return false
		}
		digits := strings.TrimLeft(line[:tab], " ")
		if digits == "" || strings.Trim(digits, "0123456789") != "" {
			return false
		}
	}
	return true
}

// replaceEdit applies the replacement without any normalisation.
func replaceEdit(body, oldS, newS string, replaceAll bool) string {
	if replaceAll {
		return strings.ReplaceAll(body, oldS, newS)
	}
	return strings.Replace(body, oldS, newS, 1)
}
