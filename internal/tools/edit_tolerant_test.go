package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func editFixture(t *testing.T, body string, tolerant bool) (*Edit, string) {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "f.txt")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return &Edit{Root: root, Cwd: NewCwd(root), Tolerant: tolerant}, path
}

func editArgs(oldS, newS string) map[string]any {
	return map[string]any{"file_path": "f.txt", "old_string": oldS, "new_string": newS}
}

func TestEditTiersAndPreviewAgree(t *testing.T) {
	cases := []struct {
		name, body, old, new, want, tier string
	}{
		{"exact", "a\nb\n", "a\n", "A\n", "A\nb\n", ""},
		{"line endings", "a\r\nb\r\nc\r\n", "a\nb\n", "x\ny\n", "x\r\ny\r\nc\r\n", "line endings folded"},
		{"trailing", "a  \nb\n", "a\nb", "x\ny", "x\ny\n", "trailing whitespace ignored"},
		{"indent", "\tif x {\n\t\tdo()\n\t}\n", "if x {\n\tdo()\n}", "if x {\n\tdo()\n\tmore()\n}", "\tif x {\n\t\tdo()\n\t\tmore()\n\t}\n", "indentation shifted"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e, path := editFixture(t, c.body, true)
			args := editArgs(c.old, c.new)
			_, before, preview, ok := e.Preview(args)
			if !ok || before != c.body {
				t.Fatalf("preview ok=%v before=%q", ok, before)
			}
			res, err := e.Execute(context.Background(), args)
			if err != nil {
				t.Fatal(err)
			}
			got, _ := os.ReadFile(path)
			if string(got) != c.want || preview != c.want {
				t.Fatalf("written %q preview %q want %q", got, preview, c.want)
			}
			if c.tier != "" && !strings.Contains(res.Content, "matched with "+c.tier) {
				t.Fatalf("result does not name the tier: %q", res.Content)
			}
			if c.tier == "" && strings.Contains(res.Content, "matched with") {
				t.Fatalf("exact result names a tier: %q", res.Content)
			}
		})
	}
}

func TestEditStrictKeepsTheOldErrors(t *testing.T) {
	e, path := editFixture(t, "a  \nb\n", false)
	_, err := e.Execute(context.Background(), editArgs("a\nb", "x"))
	if err == nil || err.Error() != "old_string not found in f.txt" {
		t.Fatalf("err = %v", err)
	}
	if got, _ := os.ReadFile(path); string(got) != "a  \nb\n" {
		t.Fatalf("file changed: %q", got)
	}
	e2, _ := editFixture(t, "x\nx\n", false)
	if _, err := e2.Execute(context.Background(), editArgs("x", "y")); err == nil || !strings.Contains(err.Error(), "appears 2 times") {
		t.Fatalf("err = %v", err)
	}
}

func TestEditRelaxedAmbiguityFailsAndLeavesFileAlone(t *testing.T) {
	body := "x  \nx\t\n"
	e, path := editFixture(t, body, true)
	_, err := e.Execute(context.Background(), editArgs("x\n", "y\n"))
	if err == nil || !strings.Contains(err.Error(), "matches 2 places") {
		t.Fatalf("err = %v", err)
	}
	if got, _ := os.ReadFile(path); string(got) != body {
		t.Fatalf("file changed: %q", got)
	}
}

func TestEditReplaceAllNeverRelaxes(t *testing.T) {
	body := "x  \nx\t\n"
	e, path := editFixture(t, body, true)
	args := editArgs("x\n", "y\n")
	args["replace_all"] = true
	if _, err := e.Execute(context.Background(), args); err == nil {
		t.Fatal("replace_all applied a relaxed match")
	}
	if got, _ := os.ReadFile(path); string(got) != body {
		t.Fatalf("file changed: %q", got)
	}
	// Exact replace_all still replaces every occurrence.
	e2, path2 := editFixture(t, "x\nx\n", true)
	args = editArgs("x", "y")
	args["replace_all"] = true
	if _, err := e2.Execute(context.Background(), args); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path2); string(got) != "y\ny\n" {
		t.Fatalf("got %q", got)
	}
}

func TestEditMixedDeltaAndMixedEndingsRefuse(t *testing.T) {
	for name, c := range map[string]struct{ body, old string }{
		"mixed indent":  {"\t  foo\n\t  bar\n", "foo\nbar"},
		"mixed endings": {"a\r\nb\nc\r\n", "a\nb\n"},
	} {
		t.Run(name, func(t *testing.T) {
			e, path := editFixture(t, c.body, true)
			_, err := e.Execute(context.Background(), editArgs(c.old, "new"))
			if err == nil || !strings.Contains(err.Error(), "nothing was changed") {
				t.Fatalf("err = %v", err)
			}
			if got, _ := os.ReadFile(path); string(got) != c.body {
				t.Fatalf("file changed: %q", got)
			}
		})
	}
}

func TestEditRefitFailureRefuses(t *testing.T) {
	e, path := editFixture(t, "a\nb\n", true)
	_, err := e.Execute(context.Background(), editArgs("  a\n  b\n", "  a\nb\n"))
	if err == nil || !strings.Contains(err.Error(), "cannot be fitted") {
		t.Fatalf("err = %v", err)
	}
	if got, _ := os.ReadFile(path); string(got) != "a\nb\n" {
		t.Fatalf("file changed: %q", got)
	}
}

func TestEditNearMissDiagnostic(t *testing.T) {
	body := "package x\n\nfunc target() {\n\treturn nil\n}\n"
	e, path := editFixture(t, body, true)
	_, err := e.Execute(context.Background(), editArgs("func target() {\n\treturn 1\n}\n", "z"))
	if err == nil {
		t.Fatal("expected an error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "closest lines") || !strings.Contains(msg, "line 3 (content differs): func target() {") {
		t.Fatalf("err = %q", msg)
	}
	if got, _ := os.ReadFile(path); string(got) != body {
		t.Fatalf("file changed: %q", got)
	}
}

func TestEditNearMissStripsControlRunesAndCaps(t *testing.T) {
	body := "func target(" + strings.Repeat("a", 300) + "\x1b]0;x\x07‮)\n"
	e, _ := editFixture(t, body, true)
	_, err := e.Execute(context.Background(), editArgs("func target(\n", "z"))
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.ContainsAny(err.Error(), "\x1b\x07‮") {
		t.Fatalf("diagnostic kept control or bidi runes: %q", err)
	}
	for _, line := range strings.Split(err.Error(), "\n") {
		if len([]rune(line)) > 260 {
			t.Fatalf("diagnostic line has %d runes", len([]rune(line)))
		}
	}
}

func TestEditNearMissOmitsTextOfAWithheldFile(t *testing.T) {
	body := "secret_token := 1\n"
	e, _ := editFixture(t, body, true)
	ctx := WithEditWithheld(context.Background(), func(abs string) bool { return strings.HasSuffix(abs, "f.txt") })
	_, err := e.Execute(ctx, editArgs("secret_token := 2\n", "z"))
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), "secret_token") {
		t.Fatalf("withheld file text echoed: %q", err)
	}
	if !strings.Contains(err.Error(), "line 1 (content differs)") || !strings.Contains(err.Error(), "withheld") {
		t.Fatalf("err = %q", err)
	}
	// Without the check the snippet is present.
	if _, err := e.Execute(context.Background(), editArgs("secret_token := 2\n", "z")); err == nil || !strings.Contains(err.Error(), "secret_token") {
		t.Fatalf("err = %v", err)
	}
}

func TestEditNoSimilarLineAndGutterHints(t *testing.T) {
	e, _ := editFixture(t, "alpha\n", true)
	if _, err := e.Execute(context.Background(), editArgs("zzz\n", "q")); err == nil || !strings.Contains(err.Error(), "no line resembles") {
		t.Fatalf("err = %v", err)
	}
	if _, err := e.Execute(context.Background(), editArgs("     1\talpha\n", "q")); err == nil || !strings.Contains(err.Error(), "line-number prefix") {
		t.Fatalf("err = %v", err)
	}
}

func TestEditBinaryAndOversizeStillRefuseBeforeMatching(t *testing.T) {
	e, _ := editFixture(t, "a\x00b\n", true)
	if _, err := e.Execute(context.Background(), editArgs("a", "b")); err == nil || !strings.Contains(err.Error(), "binary") {
		t.Fatalf("err = %v", err)
	}
	e2, _ := editFixture(t, "aaaa\n", true)
	e2.MaxBytes = 2
	if _, err := e2.Execute(context.Background(), editArgs("a", "b")); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("err = %v", err)
	}
}

func TestEditDefinitionStatesTheLadder(t *testing.T) {
	d := (&Edit{}).Definition().Description
	for _, want := range []string{"exact match always wins", "line endings folded", "replace_all=true is always exact"} {
		if !strings.Contains(d, want) {
			t.Errorf("description lacks %q", want)
		}
	}
	if strings.Contains(d, "byte for byte, including") && strings.Contains(d, "No whitespace or line-ending normalisation") {
		t.Error("description still promises byte-exact only")
	}
}
