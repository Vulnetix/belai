package tools

import (
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/docparity"
)

func valuesDef() Definition {
	return Definition{
		Name: "Probe",
		Properties: map[string]Property{
			"file_path": {Type: "string", Format: FormatPath},
			"url":       {Type: "string", Format: FormatURL},
			"command":   {Type: "string", Format: FormatCommand},
			"name":      {Type: "string", Format: FormatIdent},
			"title":     {Type: "string", Format: FormatLine},
			"mode":      {Type: "string", Enum: []string{"content", "count"}},
			"limit":     {Type: "integer"},
			"ratio":     {Type: "number"},
			"flag":      {Type: "boolean"},
			"items":     {Type: "array"},
			"opts":      {Type: "object"},
			"glob":      {Type: "string", Format: FormatGlob},
			"pattern":   {Type: "string", Format: FormatRegex},
		},
	}
}

func TestCheckArgsValuesAccept(t *testing.T) {
	ok := []map[string]any{
		{},
		{"file_path": "a/b.go"},
		{"path": "a/b.go"}, // the file_path/path alias pair
		{"file_path": ""},  // empty is the tool's concern
		{"url": "https://example.com/x"},
		{"command": "go test ./...\nls"},
		{"name": "abc-1.2_x"},
		{"title": "one line"},
		{"mode": "count"},
		{"mode": ""},
		{"limit": float64(5)},
		{"limit": "5"},
		{"limit": 5},
		{"ratio": 1.5},
		{"ratio": "1.5"},
		{"flag": true},
		{"flag": "true"},
		{"items": []any{"a"}},
		{"opts": map[string]any{"a": 1}},
		{"glob": "**/*.go"},
		{"pattern": "a|b"},
		{"limit": nil},
	}
	for _, args := range ok {
		if err := CheckArgs(valuesDef(), args); err != nil {
			t.Errorf("CheckArgs(%v) = %v", args, err)
		}
	}
}

func TestCheckArgsValuesRefuse(t *testing.T) {
	bad := []struct {
		args map[string]any
		want string
	}{
		{map[string]any{"file_path": "a\x00b"}, "file_path"},
		{map[string]any{"file_path": "a\nb"}, "valid path"},
		{map[string]any{"path": "a\x1bb"}, "valid path"},
		{map[string]any{"url": "http://127.0.0.1/"}, "valid url"},
		{map[string]any{"url": "file:///etc/passwd"}, "valid url"},
		{map[string]any{"command": "ls\x00"}, "valid command"},
		{map[string]any{"name": "a b"}, "valid ident"},
		{map[string]any{"name": "a/b"}, "valid ident"},
		{map[string]any{"title": "a\nb"}, "valid line"},
		{map[string]any{"mode": "files"}, "one of content, count"},
		{map[string]any{"limit": 1.5}, "whole number"},
		{map[string]any{"limit": "five"}, "whole number"},
		{map[string]any{"limit": []any{1}}, "whole number"},
		{map[string]any{"ratio": "x"}, "number"},
		{map[string]any{"flag": "maybe"}, "true or false"},
		{map[string]any{"flag": 1.0}, "true or false"},
		{map[string]any{"items": "a"}, "array"},
		{map[string]any{"opts": []any{}}, "object"},
		{map[string]any{"file_path": 12.0}, "string"},
		{map[string]any{"glob": "a\x00"}, "valid glob"},
		{map[string]any{"pattern": strings.Repeat("a", maxPatternBytes+1)}, "valid regex"},
	}
	for _, c := range bad {
		err := CheckArgs(valuesDef(), c.args)
		if err == nil {
			t.Errorf("CheckArgs(%v) = nil, want an error", c.args)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("CheckArgs(%v) = %q, want it to mention %q", c.args, err, c.want)
		}
	}
}

func TestCheckArgsUnknownKeyStillReported(t *testing.T) {
	err := CheckArgs(valuesDef(), map[string]any{"bogus": 1, "mode": "content"})
	if err == nil || !strings.Contains(err.Error(), `"bogus"`) {
		t.Fatalf("err = %v", err)
	}
}

func TestCheckFormatUnknownFormatFailsClosed(t *testing.T) {
	if err := CheckFormat(Format("nope"), "x"); err == nil {
		t.Fatal("an undeclared format must not pass")
	}
	if err := CheckFormat("", "anything\x00"); err != nil {
		t.Fatalf("no format means no check: %v", err)
	}
}

// TestCoreToolPathsDeclareTheirFormat pins that the path arguments of the
// file tools are checked before the tools run.
func TestCoreToolPathsDeclareTheirFormat(t *testing.T) {
	for _, tool := range []Tool{&Read{}, &Write{}, &Edit{}, &Glob{}, &Grep{}} {
		def := tool.Definition()
		found := false
		for k, p := range def.Properties {
			if (k == "file_path" || k == "path") && p.Format == FormatPath {
				found = true
			}
		}
		if !found {
			t.Errorf("%s declares no path format", def.Name)
		}
	}
	if p := (&WebFetch{}).Definition().Properties["url"]; p.Format != FormatURL {
		t.Errorf("WebFetch url format = %q", p.Format)
	}
	if p := (&Bash{}).Definition().Properties["command"]; p.Format != FormatCommand {
		t.Errorf("Bash command format = %q", p.Format)
	}
}

// The page says a glob or regex is at most 4096 bytes of valid UTF-8 without
// NUL: that exact size passes, one more byte does not.
func TestGlobAndRegexBoundaryIs4096Bytes(t *testing.T) {
	doc := strings.Join(strings.Fields(docparity.Read(t, "docs/sanitization.md")), " ")
	if !strings.Contains(doc, "Valid UTF-8, no NUL, at most 4096 bytes") || maxPatternBytes != 4096 {
		t.Fatalf("the page says 4096 bytes; maxPatternBytes is %d", maxPatternBytes)
	}
	for _, f := range []Format{FormatGlob, FormatRegex} {
		if err := CheckFormat(f, strings.Repeat("a", 4096)); err != nil {
			t.Errorf("%s of exactly 4096 bytes refused: %v", f, err)
		}
		for name, v := range map[string]string{"too long": strings.Repeat("a", 4097), "NUL": "a\x00b", "invalid UTF-8": "a\xffb"} {
			if err := CheckFormat(f, v); err == nil {
				t.Errorf("%s accepted a pattern that is %s", f, name)
			}
		}
	}
}
