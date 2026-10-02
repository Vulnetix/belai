package libitem

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"
)

// The canonical bytes of a document are what the library stores and what the
// SHA-256 in a sync is taken over, so the host and the server must produce the
// same bytes from the same document (vdb-site belaiCanonicalMarkdown and
// belaiCanonicalJSON are the reference).
//
//   - A Markdown document: not empty, valid UTF-8, and no control character other
//     than tab and line feed (a NUL, a lone carriage return and a terminal escape
//     are refused). CRLF becomes LF, every trailing whitespace character
//     (unicode.IsSpace) of the whole document is dropped, and exactly one "\n"
//     follows.
//   - A JSON document: valid UTF-8, with no key repeated inside one object and no
//     nesting past MaxJSONDepth. It is decoded with numbers kept as written (so
//     1.50 stays 1.50) and written again compactly with HTML escaping off, object
//     keys sorted, and one "\n" after it. The top level must be an object.

// MaxJSONDepth is how deeply a JSON document may nest.
const MaxJSONDepth = 8

// CanonicalMarkdown returns the canonical bytes of a Markdown document.
func CanonicalMarkdown(raw []byte) ([]byte, error) {
	if len(raw) == 0 {
		return nil, refuse("the document is empty")
	}
	if !utf8.Valid(raw) {
		return nil, refuse("the document is not valid UTF-8")
	}
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")
	for i, r := range text {
		if r == '\n' || r == '\t' {
			continue
		}
		if r == 0 {
			return nil, refuse("the document holds a NUL byte")
		}
		if unicode.IsControl(r) {
			return nil, refuse("the document holds a control character at byte %d", i)
		}
	}
	text = strings.TrimRightFunc(text, unicode.IsSpace)
	return []byte(text + "\n"), nil
}

// CanonicalJSON returns the canonical bytes of a JSON object document.
func CanonicalJSON(raw []byte) ([]byte, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, refuse("the document is empty")
	}
	if !utf8.Valid(raw) {
		return nil, refuse("the document is not valid UTF-8")
	}
	if err := scanJSON(raw); err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, refuse("the document is not valid JSON")
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, refuse("the document has data after the JSON object")
	}
	if _, ok := v.(map[string]any); !ok {
		return nil, refuse("the document must be a JSON object")
	}
	return encodeCanonical(v)
}

// decodeObject decodes raw into its top-level object, with numbers kept as
// written. It reports why a document is not a JSON object.
func decodeObject(raw []byte) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, refuse("the document is not valid JSON")
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, refuse("the document has data after the JSON object")
	}
	o, ok := v.(map[string]any)
	if !ok {
		return nil, refuse("the document must be a JSON object")
	}
	return o, nil
}

func encodeCanonical(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, refuse("the document cannot be encoded")
	}
	return buf.Bytes(), nil
}

// scanJSON walks the tokens of a JSON document, refusing a key repeated in one
// object (a decoder would silently keep the last) and nesting past MaxJSONDepth.
// It reports a syntax error as an invalid document.
func scanJSON(raw []byte) error {
	type frame struct {
		obj     bool
		keys    map[string]bool
		wantKey bool
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var stack []frame
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return refuse("the document is not valid JSON")
		}
		switch t := tok.(type) {
		case json.Delim:
			switch t {
			case '{', '[':
				if len(stack) >= MaxJSONDepth {
					return refuse("the document nests more than %d levels", MaxJSONDepth)
				}
				f := frame{obj: t == '{', wantKey: t == '{'}
				if f.obj {
					f.keys = map[string]bool{}
				}
				stack = append(stack, f)
			default:
				stack = stack[:len(stack)-1]
				if n := len(stack); n > 0 && stack[n-1].obj {
					stack[n-1].wantKey = true
				}
			}
		default:
			n := len(stack)
			if n == 0 || !stack[n-1].obj {
				continue
			}
			if stack[n-1].wantKey {
				key, _ := tok.(string)
				if stack[n-1].keys[key] {
					return refuse("the key %q appears twice in one object", cleanForMessage(key))
				}
				stack[n-1].keys[key] = true
				stack[n-1].wantKey = false
			} else {
				stack[n-1].wantKey = true
			}
		}
	}
}

// Canonical returns the canonical bytes of raw for the kind, with no per-kind
// validation and no size limit. Validate does both.
func Canonical(kind Kind, raw []byte) ([]byte, error) {
	if !kind.Valid() {
		return nil, refuse("%q is not a library item kind", string(kind))
	}
	if kind.IsMarkdown() {
		return CanonicalMarkdown(raw)
	}
	return CanonicalJSON(raw)
}

// Hash is the lower-case hex SHA-256 of canonical bytes.
func Hash(canonical []byte) string {
	h := sha256.Sum256(canonical)
	return hex.EncodeToString(h[:])
}
