package lsp

import "testing"

func TestParseGenericGofmtErrors(t *testing.T) {
	output := "foo.go:12:9: undefined: bar\nfoo.go:20:1: syntax error\n"
	rows := parseGeneric(output, "foo.go")
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(rows))
	}
	if rows[0].Line != 12 || rows[0].Col != 9 {
		t.Fatalf("first row = %+v", rows[0])
	}
	if rows[0].Source != "fallback" {
		t.Fatalf("source = %q", rows[0].Source)
	}
}

func TestParseGenericIgnoresOtherFiles(t *testing.T) {
	output := "other.go:1:1: error\nfoo.go:2:2: error"
	rows := parseGeneric(output, "foo.go")
	if len(rows) != 1 || rows[0].Line != 2 {
		t.Fatalf("got %d rows, want 1 at line 2", len(rows))
	}
}

func TestParseBashErrors(t *testing.T) {
	output := "foo.sh: line 5: syntax error near unexpected token `fi'\n"
	rows := parseBash(output, "foo.sh")
	if len(rows) != 1 || rows[0].Line != 5 {
		t.Fatalf("got %+v", rows)
	}
}

func TestParsePythonErrors(t *testing.T) {
	output := "Sorry: IndentationError: unexpected indent (foo.py, line 7)\n"
	rows := parsePython(output, "foo.py")
	if len(rows) != 1 || rows[0].Line != 7 {
		t.Fatalf("got %+v", rows)
	}
}

func TestCanFallbackTSOnlyJS(t *testing.T) {
	ts := LanguageFor("a.ts")
	js := LanguageFor("a.js")
	if canFallback(ts, "a.ts", "") {
		t.Fatal("TypeScript must not use node --check fallback")
	}
	if !canFallback(js, "a.js", "") {
		t.Fatal("JavaScript may use node --check fallback")
	}
}
