package libitem

// Item is a validated document: its kind and name, its canonical bytes and the
// hash of them.
type Item struct {
	Kind   Kind
	Name   string
	Doc    []byte
	SHA256 string
}

// validator checks one kind's canonical document and returns the item's name. It
// applies the library's rules, which are the server's (vdb-site
// belai_items_validate*.go) and which the website mirrors for inline errors.
type validator func(canonical []byte) (name string, err error)

// hostCheck is a second pass for a kind whose document becomes part of Belai's
// own settings: it applies Belai's own validators (internal/config), so a document
// the library accepts is never written as a setting that Belai then refuses to
// load. It can only make Validate stricter than the library, and only where
// Belai itself would refuse the result.
type hostCheck func(canonical []byte) error

type kindImpl struct {
	validate validator
	host     hostCheck
}

var impls = map[Kind]kindImpl{}

func register(k Kind, v validator, h hostCheck) { impls[k] = kindImpl{validate: v, host: h} }

// Supported reports whether this build validates the kind.
func Supported(k Kind) bool { _, ok := impls[k]; return ok }

// Validate canonicalises raw and checks it against the kind's rules and limits:
// first the library's (the server's), then, for a kind that becomes a Belai
// setting, Belai's own. The first problem is returned as an *Error.
func Validate(kind Kind, raw []byte) (Item, error) {
	if !kind.Valid() {
		return Item{}, refuse("%q is not a library item kind", clip(string(kind), 40))
	}
	impl, ok := impls[kind]
	if !ok {
		return Item{}, refuse("this Belai does not take %s items", string(kind))
	}
	// A document far over the limit is refused before it is parsed.
	if len(raw) > kind.MaxBytes()*2+1024 {
		return Item{}, refuse("the document is over %d bytes", kind.MaxBytes())
	}
	doc, err := Canonical(kind, raw)
	if err != nil {
		return Item{}, err
	}
	if len(doc) > kind.MaxBytes() {
		return Item{}, refuse("the document is %d bytes; the most is %d", len(doc), kind.MaxBytes())
	}
	name, err := impl.validate(doc)
	if err != nil {
		return Item{}, err
	}
	if impl.host != nil {
		if err := impl.host(doc); err != nil {
			return Item{}, err
		}
	}
	return Item{Kind: kind, Name: name, Doc: doc, SHA256: Hash(doc)}, nil
}

// Name returns the validated name of raw, or the refusal.
func Name(kind Kind, raw []byte) (string, error) {
	it, err := Validate(kind, raw)
	return it.Name, err
}
