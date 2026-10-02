package libitem

import (
	"fmt"
	"reflect"
	"strings"
)

// Item is a validated document: its kind and name, its canonical bytes and the
// hash of them.
type Item struct {
	Kind   Kind
	Name   string
	Doc    []byte
	SHA256 string
}

// validator checks one kind's canonical document and returns the item's name.
type validator func(canonical []byte) (name string, err error)

var validators = map[Kind]validator{}

func register(k Kind, v validator) { validators[k] = v }

// Supported reports whether this build validates the kind.
func Supported(k Kind) bool { _, ok := validators[k]; return ok }

// Validate canonicalises raw and checks it against the kind's schema and
// limits. The first problem is returned as an *Error.
func Validate(kind Kind, raw []byte) (Item, error) {
	if !kind.Valid() {
		return Item{}, refuse("%q is not a library item kind", clip(string(kind), 40))
	}
	v, ok := validators[kind]
	if !ok {
		return Item{}, refuse("this Belai does not take %s items", string(kind))
	}
	// A document is bounded before it is parsed, with room for the whitespace
	// canonicalisation removes.
	if len(raw) > kind.MaxBytes()*2 {
		return Item{}, refuse("the document is larger than %d bytes", kind.MaxBytes())
	}
	doc, err := Canonical(kind, raw)
	if err != nil {
		return Item{}, err
	}
	if len(doc) > kind.MaxBytes() {
		return Item{}, refuse("the document is larger than %d bytes", kind.MaxBytes())
	}
	name, err := v(doc)
	if err != nil {
		return Item{}, err
	}
	return Item{Kind: kind, Name: name, Doc: doc, SHA256: Hash(doc)}, nil
}

// Name returns the validated name of raw, or the refusal.
func Name(kind Kind, raw []byte) (string, error) {
	it, err := Validate(kind, raw)
	return it.Name, err
}

// checkSchema refuses any key of a decoded JSON value that the Go type t does
// not declare. Go's decoder matches field names without regard to case, so the
// strict check is made here, on the keys exactly as written.
func checkSchema(v any, t reflect.Type, path string) error {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Struct:
		obj, ok := v.(map[string]any)
		if !ok {
			return nil // a type error is reported by the decoder
		}
		fields := map[string]reflect.Type{}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			tag, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			if tag == "" || tag == "-" || !f.IsExported() {
				continue
			}
			fields[tag] = f.Type
		}
		for _, k := range sortedKeys(obj) {
			ft, ok := fields[k]
			if !ok {
				return refuse("unknown key %q%s", clip(k, 40), inPath(path))
			}
			if err := checkSchema(obj[k], ft, join(path, k)); err != nil {
				return err
			}
		}
	case reflect.Slice:
		arr, ok := v.([]any)
		if !ok {
			return nil
		}
		for i, e := range arr {
			if err := checkSchema(e, t.Elem(), fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
	case reflect.Map:
		obj, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		for _, k := range sortedKeys(obj) {
			if err := checkSchema(obj[k], t.Elem(), join(path, k)); err != nil {
				return err
			}
		}
	}
	return nil
}

func join(path, k string) string {
	if path == "" {
		return k
	}
	return path + "." + k
}

func inPath(path string) string {
	if path == "" {
		return ""
	}
	return " in " + path
}
