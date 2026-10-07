package agentimport

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/vulnetix/belai/internal/libitem"
)

// Kind is what an item is. It is the nine library kinds (libitem.Kind) plus the
// three the library keeps in other ways: an agent profile, a crew and a
// document (an instruction file such as AGENTS.md).
type Kind string

// The kinds ImportItem reads.
const (
	KindCommand  Kind = "command"
	KindSkill    Kind = "skill"
	KindPrompt   Kind = "prompt"
	KindAgent    Kind = "agent"
	KindCrew     Kind = "crew"
	KindProcess  Kind = "process"
	KindBudget   Kind = "budget"
	KindRewrite  Kind = "rewrite"
	KindProvider Kind = "provider"
	KindRepo     Kind = "repo"
	KindHook     Kind = "hook"
	KindDocument Kind = "document"
)

// ItemKinds lists every kind, in the order a scan reports them.
func ItemKinds() []Kind {
	return []Kind{KindCommand, KindSkill, KindPrompt, KindAgent, KindCrew, KindProcess, KindBudget, KindRewrite, KindProvider, KindRepo, KindHook, KindDocument}
}

// ParseKind reads a kind word.
func ParseKind(s string) (Kind, bool) {
	for _, k := range ItemKinds() {
		if string(k) == s {
			return k, true
		}
	}
	return "", false
}

// Markdown reports whether the kind's canonical document is Markdown text (the
// library carries it as a string); the rest are JSON objects, except a
// document, which is text.
func (k Kind) Markdown() bool {
	switch k {
	case KindCommand, KindSkill, KindPrompt, KindAgent, KindDocument:
		return true
	}
	return false
}

// Library returns the library item kind for the nine kinds that are one.
func (k Kind) Library() (libitem.Kind, bool) {
	switch k {
	case KindCommand, KindSkill, KindPrompt, KindProcess, KindBudget, KindRewrite, KindProvider, KindRepo, KindHook:
		return libitem.Kind(k), true
	}
	return "", false
}

// ImportItem reads the item of the given kind at path and converts it to the
// canonical document the library stores, without writing anything. path is a
// file (a directory holding SKILL.md for a skill). An empty format means the
// kind's default: Belai's own layout for crews, processes and the settings
// kinds, a generic Markdown reader for the others (an agent also accepts the
// four formats Import reads, detected from the files).
//
// A path that is a symbolic link, a credential file or over the size limit is
// refused with an error that matches ErrSkipped. Every other refusal is the
// reason the item is invalid.
func ImportItem(path string, kind Kind, f Format, o Options) (Result, error) {
	if _, ok := ParseKind(string(kind)); !ok {
		return Result{}, fmt.Errorf("%q is not an item kind", clipText(string(kind), 40))
	}
	if err := checkFormat(kind, f); err != nil {
		return Result{}, err
	}
	t, err := loadItem(path, kind)
	if err != nil {
		return Result{}, err
	}
	var res Result
	switch kind {
	case KindCommand, KindSkill, KindPrompt:
		res, err = importMarkdown(t, kind, f, o)
	case KindAgent:
		res, err = importAgentItem(t, f, o)
	case KindCrew:
		res, err = importCrew(t, o)
	case KindProcess:
		res, err = importProcess(t, o)
	case KindBudget, KindRewrite, KindProvider, KindRepo:
		res, err = importSettings(t, kind, o)
	case KindHook:
		res, err = importHook(path, t, f, o)
	case KindDocument:
		res, err = importDocument(t, f, o)
	}
	if err != nil {
		return Result{}, err
	}
	res.Kind = kind
	if res.Format == "" {
		res.Format = f
	}
	if res.SHA256 == "" && len(res.Doc) > 0 {
		res.SHA256 = libitem.Hash(res.Doc)
	}
	return res, nil
}

// checkFormat refuses a format that does not read the kind.
func checkFormat(kind Kind, f Format) error {
	switch kind {
	case KindCrew, KindProcess, KindBudget, KindRewrite, KindProvider, KindRepo:
		if f != "" && f != Belai {
			return fmt.Errorf("a %s is read only from Belai's own files, not as %s", kind, f)
		}
	case KindAgent:
		if f == "" || isLegacy(f) || isHarness(f) || f == Belai {
			return nil
		}
		return fmt.Errorf("unknown format %q", string(f))
	case KindHook:
		// Only the two dialects whose files are verified are read.
		if f != "" && f != ClaudeCode && f != Codex && f != Belai {
			return fmt.Errorf("hooks are not read as %s (only claude-code, codex and belai)", f)
		}
	default:
		if f != "" && !isHarness(f) && f != Belai {
			return fmt.Errorf("%s is not a format a %s is read in", f, kind)
		}
	}
	return nil
}

func isLegacy(f Format) bool {
	for _, l := range Formats() {
		if l == f {
			return true
		}
	}
	return false
}

// loadItem reads the one file an item is, or for a skill its SKILL.md.
func loadItem(path string, kind Kind) (*tree, error) {
	if kind == KindSkill {
		return loadSkill(path)
	}
	if kind != KindAgent {
		fi, err := os.Lstat(path)
		if err != nil {
			return nil, err
		}
		if fi.Mode()&fs.ModeSymlink != 0 {
			return nil, skipped("the path is a symbolic link; name the file itself")
		}
		if !fi.Mode().IsRegular() {
			return nil, fmt.Errorf("a %s is a file, and %s is not one", kind, filepath.Base(path))
		}
	}
	return load(path)
}

// loadSkill reads a skill from a directory holding SKILL.md or from the SKILL.md
// itself. Only that file is read: the files beside it are counted and left alone.
func loadSkill(p string) (*tree, error) {
	fi, err := os.Lstat(p)
	if err != nil {
		return nil, err
	}
	if fi.Mode()&fs.ModeSymlink != 0 {
		return nil, skipped("the path is a symbolic link; name the file or directory itself")
	}
	t := &tree{files: map[string][]byte{}}
	file := p
	if fi.IsDir() {
		abs, err := filepath.Abs(p)
		if err != nil {
			return nil, err
		}
		t.base = filepath.Base(abs)
		file = filepath.Join(p, "SKILL.md")
		if des, err := os.ReadDir(p); err == nil {
			for _, de := range des {
				if de.Name() != "SKILL.md" {
					t.skipped++
				}
			}
		}
	} else {
		abs, err := filepath.Abs(p)
		if err != nil {
			return nil, err
		}
		if strings.EqualFold(filepath.Base(abs), "SKILL.md") {
			t.base = filepath.Base(filepath.Dir(abs))
		} else {
			t.base = strings.TrimSuffix(filepath.Base(abs), filepath.Ext(abs))
		}
	}
	fi, err = os.Lstat(file)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, errors.New("the folder holds no SKILL.md")
	case err != nil:
		return nil, err
	case fi.Mode()&fs.ModeSymlink != 0:
		return nil, skipped("SKILL.md is a symbolic link")
	case !fi.Mode().IsRegular():
		return nil, errors.New("SKILL.md is not a regular file")
	case fi.Size() > maxFileBytes:
		return nil, skipped(fmt.Sprintf("SKILL.md is over %d bytes", maxFileBytes))
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	t.files["SKILL.md"] = data
	return t, nil
}

// only returns the single file of a tree: the item's own file.
func (t *tree) only() (string, []byte, error) {
	if len(t.files) != 1 {
		return "", nil, fmt.Errorf("expected one file, found %d", len(t.files))
	}
	for n, d := range t.files {
		return n, d, nil
	}
	return "", nil, errors.New("no file")
}

// noter collects the report of one item.
type noter struct{ list []Note }

func (n *noter) add(k NoteKind, field, format string, args ...any) {
	n.list = append(n.list, Note{Kind: k, Field: line(field, 120), Text: line(fmt.Sprintf(format, args...), 400)})
}
