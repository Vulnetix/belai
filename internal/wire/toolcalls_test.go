package wire

import (
	"encoding/json"
	"testing"
)

func TestNewObjectToolCallArgsValid(t *testing.T) {
	got := string(NewObjectToolCallArgs(`{"x":1}`))
	want := `{"x":1}`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestNewObjectToolCallArgsMalformed(t *testing.T) {
	got := string(NewObjectToolCallArgs(`not json`))
	if got != "{}" {
		t.Fatalf("got %q, want {}", got)
	}
}

func TestNewObjectToolCallArgsArray(t *testing.T) {
	// array is not an object, should fallback
	got := string(NewObjectToolCallArgs(`[1,2]`))
	if got != "{}" {
		t.Fatalf("got %q, want {}", got)
	}
}

func TestNewStringToolCallArgs(t *testing.T) {
	got := string(NewStringToolCallArgs(`{"x":1}`))
	want := `"{\"x\":1}"`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestCanonicalToolArgsEmpty(t *testing.T) {
	for _, in := range []string{"", "null", "  null  "} {
		s, err := CanonicalToolArgs(json.RawMessage(in))
		if err != nil {
			t.Fatalf("CanonicalToolArgs(%q): %v", in, err)
		}
		if s != "" {
			t.Fatalf("CanonicalToolArgs(%q) = %q", in, s)
		}
	}
}

func TestCanonicalToolArgsString(t *testing.T) {
	s, err := CanonicalToolArgs(json.RawMessage(`"{\"x\":1}"`))
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if s != `{"x":1}` {
		t.Fatalf("got %q", s)
	}
}

func TestCanonicalToolArgsObject(t *testing.T) {
	s, err := CanonicalToolArgs(json.RawMessage(`{"x":1}`))
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if s != `{"x":1}` {
		t.Fatalf("got %q", s)
	}
}

func TestCanonicalToolArgsInvalid(t *testing.T) {
	_, err := CanonicalToolArgs(json.RawMessage(`not json`))
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestCanonicalToolArgsNeither(t *testing.T) {
	_, err := CanonicalToolArgs(json.RawMessage(`123`))
	if err == nil {
		t.Fatal("expected error for non-string non-object")
	}
}

func TestToolArgsFragmentString(t *testing.T) {
	got := ToolArgsFragment(json.RawMessage(`"hello"`))
	if got != "hello" {
		t.Fatalf("got %q", got)
	}
}

func TestToolArgsFragmentObject(t *testing.T) {
	got := ToolArgsFragment(json.RawMessage(`{"x":1}`))
	if got != `{"x":1}` {
		t.Fatalf("got %q", got)
	}
}

func TestToolArgsFragmentEmpty(t *testing.T) {
	for _, in := range []string{"", "null"} {
		if got := ToolArgsFragment(json.RawMessage(in)); got != "" {
			t.Fatalf("got %q", got)
		}
	}
}

func TestToolArgsFragmentInvalidString(t *testing.T) {
	got := ToolArgsFragment(json.RawMessage(`"\u`))
	if got != "" {
		t.Fatalf("got %q", got)
	}
}
