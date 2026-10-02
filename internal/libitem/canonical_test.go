package libitem

import (
	"strings"
	"testing"
)

func TestCanonicalMarkdown(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
		err  string
	}{
		{"already canonical", "a\nb\n", "a\nb\n", ""},
		{"crlf becomes lf", "a\r\nb\r\n", "a\nb\n", ""},
		{"trailing blank lines and spaces", "a\n\n  \t\n\n", "a\n", ""},
		{"no trailing newline gains one", "a", "a\n", ""},
		{"inner whitespace is kept", "a  \n\n  b", "a  \n\n  b\n", ""},
		{"leading whitespace is kept", "  a", "  a\n", ""},
		{"a lone CR is kept", "a\rb", "a\rb\n", ""},
		{"non-breaking space is trailing whitespace", "a  ", "a\n", ""},
		{"empty", "", "", "empty"},
		{"only whitespace", " \r\n\t\n", "", "empty"},
		{"NUL", "a\x00b", "", "NUL"},
		{"invalid UTF-8", "a\xffb", "", "UTF-8"},
		{"multibyte text is kept", "café ✓\n", "café ✓\n", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := CanonicalMarkdown([]byte(c.in))
			if c.err != "" {
				if err == nil || !strings.Contains(err.Error(), c.err) {
					t.Fatalf("err = %v, want one naming %q", err, c.err)
				}
				if !IsRefusal(err) {
					t.Errorf("%v is not a refusal", err)
				}
				return
			}
			if err != nil || string(got) != c.want {
				t.Fatalf("got %q, %v; want %q", got, err, c.want)
			}
		})
	}
}

// Canonicalisation is idempotent and every spelling of a document lands on the
// same bytes and the same hash: that is what lets the host and the server compare.
func TestCanonicalMarkdownIsDeterministic(t *testing.T) {
	spellings := []string{"x: 1\n\nbody", "x: 1\r\n\r\nbody\r\n", "x: 1\n\nbody\n\n\n", "x: 1\n\nbody \t\n"}
	var first []byte
	for _, s := range spellings {
		got, err := CanonicalMarkdown([]byte(s))
		if err != nil {
			t.Fatal(err)
		}
		if first == nil {
			first = got
		}
		if string(got) != string(first) || Hash(got) != Hash(first) {
			t.Errorf("%q canonicalises to %q, want %q", s, got, first)
		}
		again, err := CanonicalMarkdown(got)
		if err != nil || string(again) != string(got) {
			t.Errorf("not idempotent: %q then %q (%v)", got, again, err)
		}
	}
}

func TestCanonicalJSON(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
		err  string
	}{
		{"keys sort and whitespace goes", "{ \"b\": 1,\n \"a\": [1, 2] }", `{"a":[1,2],"b":1}` + "\n", ""},
		{"nested keys sort", `{"z":{"b":1,"a":2},"a":{}}`, `{"a":{},"z":{"a":2,"b":1}}` + "\n", ""},
		{"HTML is not escaped", `{"a":"<b>&</b>"}`, `{"a":"<b>&</b>"}` + "\n", ""},
		{"a number keeps its spelling", `{"a":1.50,"b":1e3,"c":12345678901234567890}`, `{"a":1.50,"b":1e3,"c":12345678901234567890}` + "\n", ""},
		{"unicode is not escaped", `{"a":"café"}`, `{"a":"caf` + "é" + `"}` + "\n", ""},
		{"null and bool survive", `{"a":null,"b":true}`, `{"a":null,"b":true}` + "\n", ""},
		{"array of objects keeps its order", `{"a":[{"y":1,"x":2},{"x":3}]}`, `{"a":[{"x":2,"y":1},{"x":3}]}` + "\n", ""},
		{"empty object", `{}`, "{}\n", ""},
		{"top-level array", `[1]`, "", "object"},
		{"top-level string", `"x"`, "", "object"},
		{"top-level number", `1`, "", "object"},
		{"trailing data", `{} {}`, "", "after"},
		{"trailing garbage", `{}x`, "", "after"},
		{"truncated", `{"a":`, "", "JSON"},
		{"empty", ``, "", "JSON"},
		{"invalid UTF-8", "{\"a\":\"\xff\"}", "", "UTF-8"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := CanonicalJSON([]byte(c.in))
			if c.err != "" {
				if err == nil || !strings.Contains(err.Error(), c.err) {
					t.Fatalf("err = %v, want one naming %q", err, c.err)
				}
				return
			}
			if err != nil || string(got) != c.want {
				t.Fatalf("got %q, %v; want %q", got, err, c.want)
			}
			again, err := CanonicalJSON(got)
			if err != nil || string(again) != string(got) {
				t.Errorf("not idempotent: %q then %q (%v)", got, again, err)
			}
		})
	}
}

func TestCanonicalJSONHashIgnoresFormatting(t *testing.T) {
	a, _ := CanonicalJSON([]byte(`{"name":"x","args":["a","b"]}`))
	b, _ := CanonicalJSON([]byte("{\n  \"args\": [\"a\", \"b\"],\n  \"name\": \"x\"\n}\n"))
	if Hash(a) != Hash(b) {
		t.Fatalf("the same document hashed two ways: %s %s", Hash(a), Hash(b))
	}
}

// Hash is pinned to a known SHA-256 so a change of algorithm or encoding is loud.
func TestHashVectors(t *testing.T) {
	if got := Hash([]byte("abc\n")); got != "edeaaff3f1774ad2888673770c6d64097e391bc362d7d6fb34982ddf0efd18cb" {
		t.Errorf("Hash(abc\\n) = %s", got)
	}
	got, err := CanonicalJSON([]byte(`{"b":2,"a":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if h := Hash(got); h != "e8d38819d39f705646bfb643368eca78f7db476c16471dbc33b941b27326410d" {
		t.Errorf("Hash of the canonical {a:1,b:2} = %s", h)
	}
}

func TestCanonicalRefusesAnUnknownKind(t *testing.T) {
	if _, err := Canonical("nope", []byte("x")); err == nil || !IsRefusal(err) {
		t.Fatalf("err = %v", err)
	}
}
