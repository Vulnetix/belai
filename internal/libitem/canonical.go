package libitem

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"
)

// The canonical bytes of a document are what the library stores and what the
// SHA-256 in a sync is taken over, so the host and the server must produce the
// same bytes from the same document.
//
//   - A Markdown document: CRLF becomes LF, every trailing whitespace rune
//     (unicode.IsSpace) of the whole document is dropped, and exactly one "\n"
//     follows. It must be valid UTF-8 without a NUL.
//   - A JSON document: decoded with UseNumber (so a number keeps its spelling)
//     and written again compactly with HTML escaping off, map keys sorted, and
//     one trailing "\n". The top level must be an object.

// CanonicalMarkdown returns the canonical bytes of a Markdown document.
func CanonicalMarkdown(raw []byte) ([]byte, error) {
	if !utf8.Valid(raw) {
		return nil, refuse("the document is not valid UTF-8")
	}
	if bytes.IndexByte(raw, 0) >= 0 {
		return nil, refuse("the document contains a NUL byte")
	}
	s := strings.ReplaceAll(string(raw), "\r\n", "\n")
	s = strings.TrimRightFunc(s, unicode.IsSpace)
	if s == "" {
		return nil, refuse("the document is empty")
	}
	return []byte(s + "\n"), nil
}

// CanonicalJSON returns the canonical bytes of a JSON object document.
func CanonicalJSON(raw []byte) ([]byte, error) {
	if !utf8.Valid(raw) {
		return nil, refuse("the document is not valid UTF-8")
	}
	obj, err := decodeObject(raw)
	if err != nil {
		return nil, err
	}
	return encodeCanonical(obj)
}

func decodeObject(raw []byte) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, refuse("the document is not valid JSON: %s", jsonErrText(err))
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, refuse("the document has data after its JSON object")
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return nil, refuse("the document must be a JSON object")
	}
	return obj, nil
}

func encodeCanonical(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, refuse("the document cannot be written as JSON: %s", jsonErrText(err))
	}
	return buf.Bytes(), nil
}

// jsonErrText keeps a decoder's message short and free of the document's text.
func jsonErrText(err error) string {
	if se, ok := err.(*json.SyntaxError); ok {
		return "syntax error at byte " + itoa(se.Offset)
	}
	if err == io.EOF || err == io.ErrUnexpectedEOF {
		return "it ends too soon"
	}
	return clip(err.Error(), 120)
}

func itoa(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}

// Canonical returns the canonical bytes of raw for the kind, with no
// per-kind validation and no size limit. Validate does both.
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
