package tools

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
)

// Edit is the file-edit tool. It replaces an exact byte string, with no
// whitespace or line-ending normalisation: the old_string must match exactly.
type Edit struct {
	Cwd      *Cwd
	Root     string
	MaxBytes int64
}

// Definition returns the static tool metadata.
func (e *Edit) Definition() Definition {
	return Definition{
		Name: "Edit",
		Description: "Edit an existing file under the working directory by replacing an exact byte string. " +
			"No whitespace or line-ending normalisation is performed: old_string must match the file byte for byte, including indentation. Read the file first, and copy only the text after the line-number prefix and tab that Read puts on each line. " +
			"The call fails, leaving the file byte-identical, when the file does not exist, is binary, is over 1 MiB, when old_string equals new_string, when old_string is not found, or when it appears more than once without replace_all=true. " +
			"The write is atomic. Mutating, so it asks for approval unless an explicit allow rule matches, and it is unavailable in plan mode.",
		Properties: map[string]Property{
			"file_path":   {Type: "string", Description: "Path to the file to edit: an absolute filesystem path under one of the session roots, or relative to the working directory; the file must already exist"},
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
	return res.Rel, string(body), replaceEdit(string(body), oldS, newS, replaceAll), true
}

// Execute edits the file, failing closed in order: missing file, over
// MaxBytes, NUL (binary), old_string == new_string, zero matches, then
// multiple matches without replace_all.
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

	count := strings.Count(string(body), oldS)
	if count == 0 {
		if hasReadGutter(oldS) {
			return Result{}, fmt.Errorf("old_string not found in %s; it carries Read's line-number prefix (the number and tab before each line), which is not part of the file — remove it and match the text after the tab", res.Rel)
		}
		return Result{}, fmt.Errorf("old_string not found in %s", res.Rel)
	}
	if count > 1 && !replaceAll {
		return Result{}, fmt.Errorf("old_string appears %d times in %s; pass replace_all=true or include more surrounding context to make it unique", count, res.Rel)
	}

	replacements := count
	if !replaceAll {
		replacements = 1
	}
	if err := writeFileAtomic(full, []byte(replaceEdit(string(body), oldS, newS, replaceAll))); err != nil {
		return Result{}, err
	}
	plural := "s"
	if replacements == 1 {
		plural = ""
	}
	return EditResultMeta(
		fmt.Sprintf("edited %s (%d replacement%s)", res.Rel, replacements, plural),
		map[string]any{"abs_path": full, "rel_path": res.Rel},
	), nil
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
