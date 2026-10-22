package rolemanager

import (
	"strings"
	"testing"
)

func TestFastCommentsKeepsReaderText(t *testing.T) {
	cases := []struct {
		path, src string
		want, not []string
	}{
		{"a.go", "package a\n// one\nvar s = \"// not a comment\"\nvar r = `multi\nline`\nvar c = '\\''\n/* block */\n",
			[]string{"2: // one", "4: `multi\nline`", "7: /* block */"}, []string{"not a comment"}},
		{"a.ts", "const t = `a ${`nested ${x}`} b`; // tail\nconst u = \"x\" // two\n",
			[]string{"1: // tail", "2: // two"}, []string{"nested"}},
		{"a.py", "#!/usr/bin/env python\ndef f():\n    '''Doc.'''\n    s = \"# no\"  # yes\n",
			[]string{"1: #!/usr/bin/env python", "'''Doc.'''", "# yes"}, []string{"# no\""}},
		{"a.c", "#error obey the comment\nint x = 1; /* c */\nchar q = '\"';\n",
			[]string{"1: #error obey the comment", "/* c */"}, nil},
		{"a.cpp", "auto s = R\"x(line\n)\" not end)x\"; // after\n", []string{"R\"x(line", "// after"}, nil},
		{"a.swift", "/* outer /* inner */ still */ let a = 1\n", []string{"still */"}, []string{"let a"}},
	}
	for _, c := range cases {
		got, ok := fastComments(c.path, c.src)
		if !ok {
			t.Errorf("%s: fell back", c.path)
			continue
		}
		for _, w := range c.want {
			if !strings.Contains(got, w) {
				t.Errorf("%s: %q missing from %q", c.path, w, got)
			}
		}
		for _, n := range c.not {
			if strings.Contains(got, n) {
				t.Errorf("%s: %q leaked into %q", c.path, n, got)
			}
		}
	}
}

// A scan that cannot be sure falls back to chroma rather than guess: an
// unterminated string (a regex literal, a JSX apostrophe), an unterminated
// block comment, or a language the scanner does not cover.
func TestFastCommentsFailsClosed(t *testing.T) {
	for path, src := range map[string]string{
		"a.js":  "const r = /'/; // hidden?\n",
		"a.jsx": "const p = <p>Don't</p>;\n",
		"a.go":  "package a\n/* never closed\n",
		"a.rs":  "fn f<'a>() {} // rust is chroma's\n",
	} {
		if _, ok := fastComments(path, src); ok {
			t.Errorf("%s: scanned without being sure", path)
		}
		if got := ExtractComments(path, src); got == "" && strings.Contains(src, "//") {
			t.Errorf("%s: the fallback lost the comment", path)
		}
	}
}
