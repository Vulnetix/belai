package libitem

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
)

// decodeDoc decodes canonical JSON into out (a pointer to a struct) strictly:
// every key must be one the struct declares, spelled exactly, and every value
// must have the declared type.
func decodeDoc(canonical []byte, out any) error {
	obj, err := decodeObject(canonical)
	if err != nil {
		return err
	}
	if err := checkSchema(obj, reflect.TypeOf(out), ""); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(canonical))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		var te *json.UnmarshalTypeError
		if errors.As(err, &te) {
			field := te.Field
			if field == "" {
				field = "the document"
			}
			return refuse("%s must be %s, not %s", field, wantWord(te.Type), te.Value)
		}
		return refuse("the document does not match the schema: %s", jsonErrText(err))
	}
	return nil
}

func wantWord(t reflect.Type) string {
	switch t.Kind() {
	case reflect.String:
		return "a string"
	case reflect.Bool:
		return "true or false"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "an integer"
	case reflect.Float32, reflect.Float64:
		return "a number"
	case reflect.Slice, reflect.Array:
		return "a list"
	case reflect.Map, reflect.Struct:
		return "an object"
	}
	return t.String()
}

// present reports whether the canonical document has the top-level key, so a
// field the schema marks as required can be told from one left at its zero.
func present(canonical []byte, key string) bool {
	obj, err := decodeObject(canonical)
	if err != nil {
		return false
	}
	_, ok := obj[key]
	return ok
}
