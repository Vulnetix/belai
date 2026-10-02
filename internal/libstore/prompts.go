package libstore

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/filelib"
	"github.com/vulnetix/belai/internal/libitem"
	"github.com/vulnetix/belai/internal/promptlib"
)

func init() { register(libitem.Prompt, promptStore{}) }

// promptStore keeps a prompt where the prompt library does: a plain-text file
// <NNN>-<slug>.md in the global prompts directory. The file name carries the
// order and the enabled state, and the file holds the prompt text alone, so a
// prompt's description has no home in it.
//
// The description, and the exact document the library sent, are kept beside the
// library in <state>/library/prompts.json. While the file still matches what was
// installed, the host exports that document byte for byte, so an install never
// makes the next sync look like an edit. Once the text, the order or the enabled
// state changes here, the document is composed afresh from the file and keeps
// the description.
type promptStore struct{}

type promptMeta struct {
	// Doc is the canonical document as installed.
	Doc string `json:"doc"`
	// Order, Enabled and Body are the file as it was written, to tell an edit.
	Order   int    `json:"order"`
	Enabled bool   `json:"enabled"`
	Body    string `json:"body"`
}

func promptMetaPath() (string, error) {
	dir, err := config.GlobalDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "library", "prompts.json"), nil
}

func loadPromptMeta() map[string]promptMeta {
	path, err := promptMetaPath()
	if err != nil {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var m map[string]promptMeta
	if json.Unmarshal(data, &m) != nil {
		return nil
	}
	return m
}

func savePromptMeta(m map[string]promptMeta) error {
	path, err := promptMetaPath()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return config.WriteGlobalFileAtomic(path, data)
}

func trimBody(s string) string { return strings.TrimRightFunc(s, unicode.IsSpace) }

func (promptStore) list() ([]Local, []Skipped, error) {
	l, err := promptlib.Load(config.ScopeGlobal, "")
	if err != nil {
		return nil, nil, err
	}
	meta := loadPromptMeta()
	var out []Local
	var skipped []Skipped
	for _, e := range l.Entries {
		doc, err := promptDoc(e, meta[e.Name])
		if err != nil {
			skipped = append(skipped, Skipped{Kind: libitem.Prompt, Name: e.Name, Reason: err.Error()})
			continue
		}
		out = append(out, local(libitem.Prompt, e.Name, doc))
	}
	return out, skipped, nil
}

// promptDoc is the document for a prompt file: the installed one while the file
// is unchanged, else composed from the file.
func promptDoc(e promptlib.Entry, m promptMeta) ([]byte, error) {
	if m.Doc != "" && m.Order == e.Order && m.Enabled == e.Enabled && trimBody(m.Body) == trimBody(e.Prompt) {
		if _, err := libitem.ParsePrompt([]byte(m.Doc)); err == nil {
			return []byte(m.Doc), nil
		}
	}
	d := libitem.PromptDoc{Name: e.Name, Order: e.Order, Enabled: e.Enabled, Body: trimBody(e.Prompt)}
	if m.Doc != "" {
		if prev, err := libitem.ParsePrompt([]byte(m.Doc)); err == nil {
			d.Description = prev.Description
		}
	}
	return libitem.ComposePrompt(d)
}

func (promptStore) install(it libitem.Item, o InstallOptions) (Result, error) {
	d, err := libitem.ParsePrompt(it.Doc)
	if err != nil {
		return Result{}, err
	}
	if err := untrustedGate(it.Doc); err != nil {
		return Result{}, err
	}
	if slug, err := filelib.Slug(d.Name); err != nil || slug != d.Name {
		return Result{}, refusal("this host keeps a prompt in a file named after it, in lowercase letters, digits and hyphens; rename %q in the library to install it", d.Name)
	}
	l, err := promptlib.Load(config.ScopeGlobal, "")
	if err != nil {
		return Result{}, err
	}
	replaced := false
	for _, e := range l.Entries {
		if e.Name == d.Name {
			replaced = true
		}
	}
	if replaced && !o.Overwrite {
		return Result{}, ErrExists
	}
	body := trimBody(d.Body)
	e, err := promptlib.Put(config.ScopeGlobal, "", d.Name, body, d.Order, d.Enabled)
	if err != nil {
		if errors.Is(err, promptlib.ErrLibraryFull) {
			return Result{}, refusal("the prompt library is full (999 prompts)")
		}
		return Result{}, err
	}
	meta := loadPromptMeta()
	if meta == nil {
		meta = map[string]promptMeta{}
	}
	meta[d.Name] = promptMeta{Doc: string(it.Doc), Order: e.Order, Enabled: e.Enabled, Body: body}
	if err := savePromptMeta(meta); err != nil {
		return Result{Replaced: replaced, Where: e.Path}, err
	}
	return Result{Replaced: replaced, Where: e.Path}, nil
}
