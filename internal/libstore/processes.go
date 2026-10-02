package libstore

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/filelib"
	"github.com/vulnetix/belai/internal/libitem"
	"github.com/vulnetix/belai/internal/processlib"
)

func init() { register(libitem.Process, processStore{}) }

// processStore keeps a process where the process library does: a file
// <NNN>-<name>.json (structured) or <NNN>-<name>.sh (a legacy shell string) in
// the global processes directory. A structured file is the document itself, so an
// installed process hashes to what the library holds; a legacy file is offered to
// the library as the equivalent `sh -c` document. Only the global library is
// synced: a project process (.vulnetix/processes) is never listed, sent or
// written.
type processStore struct{}

func (processStore) list() ([]Local, []Skipped, error) {
	l, err := processlib.Load(config.ScopeGlobal, "")
	if err != nil {
		return nil, nil, err
	}
	var out []Local
	var skipped []Skipped
	for _, e := range l.Entries {
		doc, err := processDoc(e)
		if err != nil {
			skipped = append(skipped, Skipped{Kind: libitem.Process, Name: e.Name, Reason: err.Error()})
			continue
		}
		out = append(out, local(libitem.Process, e.Name, doc))
	}
	// A structured file that is not a valid document is a stray to the library;
	// say why, so it is not silently left out of the sync.
	dir, derr := processlib.Dir(config.ScopeGlobal, "")
	for _, name := range l.Strays {
		if derr != nil || !strings.HasSuffix(name, ".json") {
			continue
		}
		reason := "it is not a valid process document"
		if order, slug, enabled, _, ok := (filelib.Spec{Ext: ".sh", AltExts: []string{".json"}}).ParseFileNameExt(name); ok {
			if b, why := readRegular(filepath.Join(dir, name), 64<<10); why == "" {
				if _, err := libitem.NormalizeProcessFile(b, slug, order, enabled); err != nil {
					reason = err.Error()
				}
			}
		}
		skipped = append(skipped, Skipped{Kind: libitem.Process, Name: strings.TrimSuffix(name, ".json"), Reason: reason})
	}
	return out, skipped, nil
}

// processDoc is the canonical document for a process entry.
func processDoc(e processlib.Entry) ([]byte, error) {
	if e.Spec != nil {
		it, err := libitem.NormalizeProcessFile([]byte(e.Body), e.Name, e.Order, e.Enabled)
		if err != nil {
			return nil, err
		}
		return it.Doc, nil
	}
	d, err := libitem.LegacyProcess(e.Name, e.Command, e.Order, e.Enabled)
	if err != nil {
		return nil, errors.New("this shell command cannot be a library item: " + err.Error())
	}
	b, err := json.Marshal(d)
	if err != nil {
		return nil, err
	}
	it, err := libitem.Validate(libitem.Process, b)
	if err != nil {
		return nil, err
	}
	return it.Doc, nil
}

func (processStore) install(it libitem.Item, o InstallOptions) (Result, error) {
	d, err := libitem.ParseProcess(it.Doc)
	if err != nil {
		return Result{}, err
	}
	if slug, err := filelib.Slug(d.Name); err != nil || slug != d.Name {
		return Result{}, refusal("this host keeps a process in a file named after it, in lowercase letters, digits and hyphens; rename %q in the library to install it", d.Name)
	}
	l, err := processlib.Load(config.ScopeGlobal, "")
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
	e, err := processlib.PutDoc(config.ScopeGlobal, "", it.Doc)
	if err != nil {
		if errors.Is(err, processlib.ErrLibraryFull) {
			return Result{}, refusal("the process library is full (999 processes)")
		}
		return Result{}, err
	}
	return Result{Replaced: replaced, Where: e.Path}, nil
}
