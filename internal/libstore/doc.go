// Package libstore is where library items meet the host: it reads what this
// machine holds of each kind (a skill directory, a prompt file, ...) as the
// canonical document the library would store, and installs a document the
// library holds as the files or settings the kind lives in.
//
// The same package serves the rc daemon (a website backup or install request,
// the automatic sync) and the `belai <kind> import|export` commands, so a
// document means one thing however it arrives. Every install validates the
// whole document with internal/libitem first, never replaces an item unless it
// is told to, and writes atomically.
//
// Only the user's own layers are read or written. A project-layer file is never
// listed and never touched.
package libstore

import (
	"errors"
	"fmt"
	"sort"

	"github.com/vulnetix/belai/internal/libitem"
	"github.com/vulnetix/belai/internal/sanitize"
)

// Local is one item this host holds: its canonical document and the hash of it.
type Local struct {
	Kind libitem.Kind
	Name string
	Doc  []byte
	// SHA256 is the hash the library compares: of Doc alone, or for a hook of Doc
	// and its Files (libitem.HashBundle).
	SHA256 string
	// Files are the script files of a hook bundle, in path order.
	Files []LocalFile
}

// Skipped is a local item that cannot travel to the library, and why.
type Skipped struct {
	Kind   libitem.Kind
	Name   string
	Reason string
}

// InstallOptions say how an install may treat an item the host already has.
type InstallOptions struct {
	// Overwrite lets the install replace the host's item of the same name.
	Overwrite bool
	// Name, when set, is the name the document must carry: the library item the
	// request named.
	Name string
}

// Result is what an install did.
type Result struct {
	// Replaced is true when an existing item was overwritten.
	Replaced bool
	// Where names the file or setting written, for a message to the user.
	Where string
}

// ErrNotFound is returned when the host has no item of that name.
var ErrNotFound = errors.New("not found")

// ErrExists is returned when an install would replace an item and was not told
// it may.
var ErrExists = errors.New("already exists")

// Refusal is an install that was refused for a reason the user can act on.
type Refusal struct{ Reason string }

func (r *Refusal) Error() string { return r.Reason }

func refusal(format string, args ...any) error {
	return &Refusal{Reason: fmt.Sprintf(format, args...)}
}

// IsRefusal reports whether err is a Refusal or a validation refusal.
func IsRefusal(err error) bool {
	var r *Refusal
	return errors.As(err, &r) || libitem.IsRefusal(err)
}

type adapter interface {
	// list returns every item of the kind on the host, sorted by name, and the
	// ones that exist but cannot be synced.
	list() ([]Local, []Skipped, error)
	// install writes a validated item.
	install(it libitem.Item, o InstallOptions) (Result, error)
}

// bundleInstaller is an adapter whose item carries files (a hook).
type bundleInstaller interface {
	installBundle(it libitem.Item, files []BundleFile, o InstallOptions) (Result, error)
}

var adapters = map[libitem.Kind]adapter{}

func register(k libitem.Kind, a adapter) { adapters[k] = a }

// Supported reports whether this build can store the kind.
func Supported(k libitem.Kind) bool {
	_, ok := adapters[k]
	return ok && libitem.Supported(k)
}

// Kinds lists the kinds this build can store, in the contract's order.
func Kinds() []libitem.Kind {
	var out []libitem.Kind
	for _, k := range libitem.Kinds() {
		if Supported(k) {
			out = append(out, k)
		}
	}
	return out
}

// List returns what the host holds of one kind.
func List(kind libitem.Kind) ([]Local, []Skipped, error) {
	a, ok := adapters[kind]
	if !ok {
		return nil, nil, fmt.Errorf("this Belai does not store %s items", kind)
	}
	items, skipped, err := a.list()
	if err != nil {
		return nil, nil, err
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	return items, skipped, nil
}

// Get returns the host's item of that name.
func Get(kind libitem.Kind, name string) (Local, error) {
	items, _, err := List(kind)
	if err != nil {
		return Local{}, err
	}
	for _, it := range items {
		if it.Name == name {
			return it, nil
		}
	}
	return Local{}, ErrNotFound
}

// Install validates raw as an item of the kind and writes it. It returns
// ErrExists when the host already has the item and Overwrite is not set, a
// *Refusal or a validation refusal when the document or the host will not take
// it, and any other error for a failure to write.
func Install(kind libitem.Kind, raw []byte, o InstallOptions) (Result, error) {
	a, ok := adapters[kind]
	if !ok || !libitem.Supported(kind) {
		return Result{}, refusal("this Belai does not store %s items", kind)
	}
	it, err := libitem.Validate(kind, raw)
	if err != nil {
		return Result{}, err
	}
	if o.Name != "" && it.Name != o.Name {
		return Result{}, refusal("the document is named %q, not %q", it.Name, o.Name)
	}
	return a.install(it, o)
}

func local(kind libitem.Kind, name string, doc []byte) Local {
	return Local{Kind: kind, Name: name, Doc: doc, SHA256: libitem.Hash(doc)}
}

// untrustedGate refuses a Markdown document that carries text the harness never
// lets near a model: delimiter markup, terminal escapes, control characters,
// bidirectional overrides and the invisible runes that hide text. The document
// is refused whole rather than repaired, so what is stored is what the library
// holds and the hashes keep agreeing.
func untrustedGate(doc []byte) error {
	s := string(doc)
	if sanitize.Sanitize(s) != s {
		return refusal("the text contains harness delimiter markup, so it was not written")
	}
	for _, r := range s {
		if unsafeRune(r) {
			return refusal("the text contains a control, escape, bidirectional or invisible character (U+%04X), so it was not written", r)
		}
	}
	return nil
}

func unsafeRune(r rune) bool {
	switch {
	case r == '\n' || r == '\t':
		return false
	case r < ' ' || r == 0x7f || (r >= 0x80 && r <= 0x9f):
		return true
	case r == 0x200B || r == 0x200E || r == 0x200F || r == 0x2060 || r == 0xFEFF || r == 0x2028 || r == 0x2029:
		return true
	case r >= 0x202A && r <= 0x202E, r >= 0x2066 && r <= 0x2069, r >= 0xE0000 && r <= 0xE007F:
		return true
	}
	return false
}

// UntrustedText applies the gate an install applies to a Markdown document: it
// refuses text that carries delimiter markup, a control or escape character, a
// bidirectional override or an invisible rune. A reader of a file the host did
// not install itself (a project's commands directory) calls it before the text
// can reach a model.
func UntrustedText(doc []byte) error { return untrustedGate(doc) }
