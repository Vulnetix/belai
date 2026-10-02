package libstore

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/libitem"
)

// DefaultSetName is what the host calls its budget set or provider set until the
// library names it: a host has one configuration of each, and the library keeps
// sets by name.
const DefaultSetName = "default"

// The host holds one token-budget configuration and one provider configuration,
// not a list of them, so the library's name for each is remembered in
// <state>/library/names.json: the name of the set the host last installed or
// exported. It is a label for the host's whole configuration of that kind, and a
// backup request names it.

func namesPath() (string, error) {
	dir, err := config.GlobalDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "library", "names.json"), nil
}

func loadNames() map[string]string {
	p, err := namesPath()
	if err != nil {
		return map[string]string{}
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return map[string]string{}
	}
	var m map[string]string
	if json.Unmarshal(data, &m) != nil || m == nil {
		return map[string]string{}
	}
	return m
}

// LocalName is the name the host's configuration of a set kind (budget, provider)
// goes by in the library. A kind that is not a set has the name of each item.
func LocalName(kind libitem.Kind) string {
	if kind == libitem.Rewrite {
		return libitem.RewriteName
	}
	if n := loadNames()[string(kind)]; libitem.ValidName(kind, n) {
		return n
	}
	return DefaultSetName
}

func setLocalName(kind libitem.Kind, name string) error {
	p, err := namesPath()
	if err != nil {
		return err
	}
	m := loadNames()
	m[string(kind)] = name
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return config.WriteGlobalFileAtomic(p, data)
}

// ExportAs is Get for the singleton kinds, under a name of the caller's choosing:
// the document for the host's whole configuration of the kind, named name. It is
// for the commands, where a person names the file they export; a website backup
// names the host's own set and goes through Get. A rewrite is always bash_rewrite.
func ExportAs(kind libitem.Kind, name string) (Local, error) {
	if !kind.Singleton() {
		return Get(kind, name)
	}
	items, _, err := List(kind)
	if err != nil {
		return Local{}, err
	}
	if len(items) == 0 {
		return Local{}, ErrNotFound
	}
	if kind == libitem.Rewrite {
		if name != libitem.RewriteName {
			return Local{}, refusal("the rewrite table is always named %s", libitem.RewriteName)
		}
		return items[0], nil
	}
	it, err := rename(items[0], kind, name)
	if err != nil {
		return Local{}, err
	}
	return it, nil
}

// rename re-validates a set document under another name.
func rename(it Local, kind libitem.Kind, name string) (Local, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(it.Doc, &m); err != nil {
		return Local{}, err
	}
	b, err := json.Marshal(name)
	if err != nil {
		return Local{}, err
	}
	m["name"] = b
	raw, err := json.Marshal(m)
	if err != nil {
		return Local{}, err
	}
	out, err := libitem.Validate(kind, raw)
	if err != nil {
		return Local{}, err
	}
	return local(kind, out.Name, out.Doc), nil
}
