package headless

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/vulnetix/belai/internal/sanitize"
)

// Schema limits. A schema is the user's own file, but it is parsed with the
// same suspicion as any input: bounded size, depth and node count.
const (
	maxSchemaBytes = 64 << 10
	maxSchemaDepth = 16
	maxSchemaNodes = 1024
	// maxReplyDepth bounds the nesting Validate walks in a reply.
	maxReplyDepth = 64
	// maxDescription caps a description that rides into the model's directive.
	maxDescription = 300
)

// Schema is the closed subset of JSON Schema the -json-schema flag accepts:
// type, properties, required, enum, items, additionalProperties, plus a
// description that is shown to the model. Any other keyword is a load error,
// never ignored, so a schema never promises a check the harness does not run.
type Schema struct {
	Type                 string
	Description          string
	Properties           map[string]*Schema
	Required             []string
	Enum                 []json.RawMessage
	Items                *Schema
	AdditionalProperties *bool
}

// Fixed failure codes. The repair message names one of these and a JSON
// pointer, and nothing the reply said.
const (
	CodeNotJSON            = "not_json"
	CodeTrailingData       = "trailing_data"
	CodeTooDeep            = "too_deep"
	CodeType               = "wrong_type"
	CodeRequired           = "missing_required"
	CodeEnum               = "not_in_enum"
	CodeAdditionalProperty = "unexpected_property"
)

// SchemaError is why a reply did not satisfy the schema: a code from the
// fixed set and a JSON pointer built from the schema's own property names and
// array indexes. It never holds text from the reply.
type SchemaError struct {
	Pointer string `json:"pointer"`
	Code    string `json:"code"`
}

func (e *SchemaError) Error() string {
	return "schema: " + e.Code + " at " + e.Pointer
}

// allowedKeywords are the keys a schema object may hold.
var allowedKeywords = map[string]bool{
	"type": true, "properties": true, "required": true, "enum": true,
	"items": true, "additionalProperties": true, "description": true,
	// Annotations that carry no rule and never reach the model.
	"$schema": true, "title": true,
}

var schemaTypes = map[string]bool{
	"object": true, "array": true, "string": true, "number": true,
	"integer": true, "boolean": true, "null": true,
}

// ParseSchema reads the schema file at path. The path is checked lexically
// (sanitize.PathText) and the file must be a regular file within the size
// bound; an unsupported keyword, an unknown type or a malformed file is an
// error.
func ParseSchema(path string) (*Schema, error) {
	if err := sanitize.PathText(path); err != nil {
		return nil, fmt.Errorf("json-schema: the path has invalid characters")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("json-schema: %w", err)
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		return nil, errors.New("json-schema: the path is not a regular file")
	}
	b, err := io.ReadAll(io.LimitReader(f, maxSchemaBytes+1))
	if err != nil {
		return nil, fmt.Errorf("json-schema: %w", err)
	}
	if len(b) > maxSchemaBytes {
		return nil, fmt.Errorf("json-schema: the file is larger than %d bytes", maxSchemaBytes)
	}
	return ParseSchemaBytes(b)
}

// ParseSchemaBytes parses a schema document.
func ParseSchemaBytes(b []byte) (*Schema, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	var raw any
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("json-schema: not valid JSON: %w", err)
	}
	if dec.More() {
		return nil, errors.New("json-schema: more than one JSON value in the file")
	}
	n := 0
	s, err := buildSchema(raw, "", 0, &n)
	if err != nil {
		return nil, fmt.Errorf("json-schema: %w", err)
	}
	return s, nil
}

func pointerAt(base, seg string) string {
	return base + "/" + strings.NewReplacer("~", "~0", "/", "~1").Replace(seg)
}

func buildSchema(v any, at string, depth int, nodes *int) (*Schema, error) {
	where := at
	if where == "" {
		where = "/"
	}
	if depth > maxSchemaDepth {
		return nil, fmt.Errorf("%s: nested deeper than %d", where, maxSchemaDepth)
	}
	*nodes++
	if *nodes > maxSchemaNodes {
		return nil, fmt.Errorf("more than %d schema nodes", maxSchemaNodes)
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s: a schema must be an object", where)
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !allowedKeywords[k] {
			return nil, fmt.Errorf("%s: the keyword %q is not supported (supported: type, properties, required, enum, items, additionalProperties, description)", where, sanitize.Line(k, 40))
		}
	}
	s := &Schema{}
	if t, ok := m["type"]; ok {
		ts, isStr := t.(string)
		if !isStr || !schemaTypes[ts] {
			return nil, fmt.Errorf("%s: type must be one of object, array, string, number, integer, boolean or null", where)
		}
		s.Type = ts
	}
	if d, ok := m["description"]; ok {
		ds, isStr := d.(string)
		if !isStr {
			return nil, fmt.Errorf("%s: description must be a string", where)
		}
		s.Description = sanitize.Line(ds, maxDescription)
	}
	if p, ok := m["properties"]; ok {
		pm, isMap := p.(map[string]any)
		if !isMap {
			return nil, fmt.Errorf("%s: properties must be an object", where)
		}
		s.Properties = map[string]*Schema{}
		names := make([]string, 0, len(pm))
		for name := range pm {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			if err := checkName(name); err != nil {
				return nil, fmt.Errorf("%s: property name: %w", where, err)
			}
			sub, err := buildSchema(pm[name], pointerAt(at, name), depth+1, nodes)
			if err != nil {
				return nil, err
			}
			s.Properties[name] = sub
		}
	}
	if r, ok := m["required"]; ok {
		list, isList := r.([]any)
		if !isList {
			return nil, fmt.Errorf("%s: required must be an array of property names", where)
		}
		for _, item := range list {
			name, isStr := item.(string)
			if !isStr {
				return nil, fmt.Errorf("%s: required must be an array of property names", where)
			}
			if _, declared := s.Properties[name]; !declared {
				return nil, fmt.Errorf("%s: required names %q, which is not a declared property", where, sanitize.Line(name, 40))
			}
			s.Required = append(s.Required, name)
		}
	}
	if e, ok := m["enum"]; ok {
		list, isList := e.([]any)
		if !isList || len(list) == 0 {
			return nil, fmt.Errorf("%s: enum must be a non-empty array", where)
		}
		for _, item := range list {
			b, err := json.Marshal(item)
			if err != nil {
				return nil, fmt.Errorf("%s: enum: %w", where, err)
			}
			s.Enum = append(s.Enum, b)
		}
	}
	if it, ok := m["items"]; ok {
		sub, err := buildSchema(it, pointerAt(at, "items"), depth+1, nodes)
		if err != nil {
			return nil, err
		}
		s.Items = sub
	}
	if ap, ok := m["additionalProperties"]; ok {
		b, isBool := ap.(bool)
		if !isBool {
			return nil, fmt.Errorf("%s: additionalProperties must be true or false", where)
		}
		s.AdditionalProperties = &b
	}
	return s, nil
}

// checkName refuses a property name that could not be shown safely in a
// directive or a pointer.
func checkName(name string) error {
	if name == "" || len(name) > 128 || sanitize.Text(name) != name || strings.ContainsAny(name, "\n\t") {
		return errors.New("must be a plain name of at most 128 characters")
	}
	return nil
}

// canonical is the schema re-serialised from the parsed form, so the model
// sees exactly the rules Validate enforces and nothing else.
func (s *Schema) canonical() map[string]any {
	out := map[string]any{}
	if s.Type != "" {
		out["type"] = s.Type
	}
	if s.Description != "" {
		out["description"] = s.Description
	}
	if len(s.Properties) > 0 {
		props := map[string]any{}
		for name, sub := range s.Properties {
			props[name] = sub.canonical()
		}
		out["properties"] = props
	}
	if len(s.Required) > 0 {
		out["required"] = s.Required
	}
	if len(s.Enum) > 0 {
		out["enum"] = s.Enum
	}
	if s.Items != nil {
		out["items"] = s.Items.canonical()
	}
	if s.AdditionalProperties != nil {
		out["additionalProperties"] = *s.AdditionalProperties
	}
	return out
}

// Directive is the harness-composed instruction that tells the model the form
// of its final answer. It rides on the user turn as a sealed directive, and
// the schema text in it is the canonical form passed through sanitize.Text.
func (s *Schema) Directive() string {
	b, err := json.Marshal(s.canonical())
	if err != nil {
		return ""
	}
	return sanitize.Text("Your final answer must be a single JSON value that satisfies this JSON schema. " +
		"Reply with that JSON value alone: no code fence, no commentary before or after it.\nSchema: " + string(b))
}

// RepairMessage is the one follow-up turn sent when the reply did not satisfy
// the schema. It holds a fixed sentence, a code and a pointer built from the
// schema's own names, and never any text from the reply.
func (s *Schema) RepairMessage(e *SchemaError) string {
	return sanitize.Text(fmt.Sprintf("Your last answer did not satisfy the required JSON schema (%s at %s). "+
		"Reply again with only the corrected JSON value that satisfies the schema: no code fence, no commentary.\nSchema: %s",
		e.Code, e.Pointer, s.compactJSON()))
}

func (s *Schema) compactJSON() string {
	b, _ := json.Marshal(s.canonical())
	return string(b)
}

// extractJSON returns the JSON text of a reply: the reply itself, or the
// body of a single fenced block when the whole reply is one fence.
func extractJSON(reply string) string {
	t := strings.TrimSpace(reply)
	if strings.HasPrefix(t, "```") {
		if nl := strings.IndexByte(t, '\n'); nl >= 0 && strings.HasSuffix(t, "```") && len(t) >= nl+4 {
			return strings.TrimSpace(t[nl+1 : len(t)-3])
		}
	}
	return t
}

// Validate checks a reply against the schema in plain Go. On success it
// returns the value re-encoded compactly with every string cleaned
// (sanitize.Text); on failure it returns the first failure found, in a fixed
// order (declared properties by name, then array items by index). The model is
// never asked whether its output is valid.
func (s *Schema) Validate(reply string) (json.RawMessage, *SchemaError) {
	text := extractJSON(reply)
	dec := json.NewDecoder(strings.NewReader(text))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, &SchemaError{Pointer: "/", Code: CodeNotJSON}
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, &SchemaError{Pointer: "/", Code: CodeTrailingData}
	}
	if depthOf(v, 0) > maxReplyDepth {
		return nil, &SchemaError{Pointer: "/", Code: CodeTooDeep}
	}
	if e := s.check(v, "", 0); e != nil {
		return nil, e
	}
	out, err := json.Marshal(cleanValue(v))
	if err != nil {
		return nil, &SchemaError{Pointer: "/", Code: CodeNotJSON}
	}
	return out, nil
}

// depthOf is how deeply v nests, counting stops past the bound so a hostile
// reply costs no more than the bound.
func depthOf(v any, d int) int {
	if d > maxReplyDepth {
		return d
	}
	max := d
	switch x := v.(type) {
	case []any:
		for _, item := range x {
			if n := depthOf(item, d+1); n > max {
				max = n
			}
		}
	case map[string]any:
		for _, item := range x {
			if n := depthOf(item, d+1); n > max {
				max = n
			}
		}
	}
	return max
}

func ptr(p string) string {
	if p == "" {
		return "/"
	}
	return p
}

func (s *Schema) check(v any, at string, depth int) *SchemaError {
	if depth > maxReplyDepth {
		return &SchemaError{Pointer: ptr(at), Code: CodeTooDeep}
	}
	if s.Type != "" && !typeMatches(s.Type, v) {
		return &SchemaError{Pointer: ptr(at), Code: CodeType}
	}
	if len(s.Enum) > 0 {
		match := false
		for _, e := range s.Enum {
			if jsonEqual(e, v) {
				match = true
				break
			}
		}
		if !match {
			return &SchemaError{Pointer: ptr(at), Code: CodeEnum}
		}
	}
	switch x := v.(type) {
	case map[string]any:
		for _, name := range s.Required {
			if _, ok := x[name]; !ok {
				return &SchemaError{Pointer: pointerAt(at, name), Code: CodeRequired}
			}
		}
		names := make([]string, 0, len(s.Properties))
		for name := range s.Properties {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			if val, ok := x[name]; ok {
				if e := s.Properties[name].check(val, pointerAt(at, name), depth+1); e != nil {
					return e
				}
			}
		}
		if s.AdditionalProperties != nil && !*s.AdditionalProperties {
			for k := range x {
				if _, declared := s.Properties[k]; !declared {
					// The pointer is the object's own: a key the reply
					// invented is reply text and is not repeated.
					return &SchemaError{Pointer: ptr(at), Code: CodeAdditionalProperty}
				}
			}
		}
	case []any:
		if s.Items != nil {
			for i, item := range x {
				if e := s.Items.check(item, pointerAt(at, strconv.Itoa(i)), depth+1); e != nil {
					return e
				}
			}
		}
	}
	return nil
}

func typeMatches(t string, v any) bool {
	switch t {
	case "object":
		_, ok := v.(map[string]any)
		return ok
	case "array":
		_, ok := v.([]any)
		return ok
	case "string":
		_, ok := v.(string)
		return ok
	case "boolean":
		_, ok := v.(bool)
		return ok
	case "null":
		return v == nil
	case "number":
		_, ok := v.(json.Number)
		return ok
	case "integer":
		n, ok := v.(json.Number)
		if !ok {
			return false
		}
		if _, err := n.Int64(); err == nil {
			return true
		}
		f, err := n.Float64()
		return err == nil && !math.IsInf(f, 0) && f == math.Trunc(f)
	}
	return false
}

// jsonEqual compares an enum member with a decoded reply value by their
// canonical encodings.
func jsonEqual(enum json.RawMessage, v any) bool {
	var e any
	dec := json.NewDecoder(bytes.NewReader(enum))
	dec.UseNumber()
	if dec.Decode(&e) != nil {
		return false
	}
	a, err1 := json.Marshal(e)
	b, err2 := json.Marshal(v)
	return err1 == nil && err2 == nil && bytes.Equal(a, b)
}

// cleanValue passes every string and object key of a decoded value through
// sanitize.Text, so a structured result holds no control, bidi or delimiter
// text.
func cleanValue(v any) any {
	switch x := v.(type) {
	case string:
		return clean(x)
	case []any:
		out := make([]any, len(x))
		for i, item := range x {
			out[i] = cleanValue(item)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, item := range x {
			out[clean(k)] = cleanValue(item)
		}
		return out
	}
	return v
}
