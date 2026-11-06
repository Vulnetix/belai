package patchfmt

import (
	"strings"
	"testing"
)

func plan(t *testing.T, body string, tol Tolerance, hunks ...Hunk) (string, []Note, error) {
	t.Helper()
	return Plan("f.go", body, hunks, tol)
}

func hk(old, nw string) Hunk { return Hunk{Old: old, New: nw} }

func TestPlanExact(t *testing.T) {
	out, notes, err := plan(t, "a\nb\nc\n", Strict, hk("b\n", "B\n"))
	if err != nil || out != "a\nB\nc\n" || len(notes) != 0 {
		t.Fatalf("%q %v %v", out, notes, err)
	}
}

func TestPlanExactIsWholeLine(t *testing.T) {
	// "b" is inside "abc": a hunk of whole lines does not land there.
	_, _, err := plan(t, "abc\nx\n", Strict, hk("b\n", "B\n"))
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("a mid-line match applied: %v", err)
	}
	_, _, err = plan(t, "abc\nx\n", Whitespace, hk("bc\n", "BC\n"))
	if err == nil {
		t.Fatal("a mid-line match applied under whitespace tolerance")
	}
}

func TestPlanAmbiguityFailsTheWholeLadder(t *testing.T) {
	_, _, err := plan(t, "x\ny\nx\ny\n", Unicode, hk("x\ny\n", "z\n"))
	if err == nil || !strings.Contains(err.Error(), "2 places") {
		t.Fatalf("%v", err)
	}
	// Overlapping starts count: the same line twice in a row is not unique.
	_, _, err = plan(t, "x\nx\nx\n", Strict, hk("x\nx\n", "y\n"))
	if err == nil || !strings.Contains(err.Error(), "2 places") {
		t.Fatalf("overlapping occurrences were not counted: %v", err)
	}
}

func TestPlanTrailingWhitespace(t *testing.T) {
	out, notes, err := plan(t, "a\nb  \nc\n", Whitespace, hk("b\n", "B\n"))
	if err != nil || out != "a\nB\nc\n" {
		t.Fatalf("%q %v", out, err)
	}
	if len(notes) != 1 || notes[0].Hunk != 1 || !strings.Contains(notes[0].Tier, "trailing") {
		t.Fatalf("notes = %+v", notes)
	}
	if _, _, err := plan(t, "a\nb  \nc\n", Strict, hk("b\n", "B\n")); err == nil {
		t.Fatal("strict matched through trailing whitespace")
	}
}

func TestPlanCRLFIsPreserved(t *testing.T) {
	out, _, err := plan(t, "a\r\nb\r\nc\r\n", Whitespace, hk("b\n", "B\nB2\n"))
	if err != nil || out != "a\r\nB\r\nB2\r\nc\r\n" {
		t.Fatalf("%q %v", out, err)
	}
}

func TestPlanIndentationDeltaIsPreserved(t *testing.T) {
	body := "func f() {\n\tif x {\n\t\treturn 1\n\t}\n}\n"
	out, notes, err := plan(t, body, Whitespace, hk("if x {\n\treturn 1\n}\n", "if x {\n\treturn 2\n}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if out != "func f() {\n\tif x {\n\t\treturn 2\n\t}\n}\n" {
		t.Fatalf("%q", out)
	}
	if len(notes) != 1 || !strings.Contains(notes[0].Tier, "indentation") {
		t.Fatalf("notes = %+v", notes)
	}
}

func TestPlanUnicodeIsAnExplicitOptIn(t *testing.T) {
	body := "say “hi” — now…\nnext\n"
	h := hk("say \"hi\" - now...\n", "say \"bye\"\n")
	for _, tol := range []Tolerance{Strict, Whitespace} {
		if _, _, err := plan(t, body, tol, h); err == nil {
			t.Fatalf("%s matched typographic punctuation", tol)
		}
	}
	out, notes, err := plan(t, body, Unicode, h)
	if err != nil || out != "say \"bye\"\nnext\n" {
		t.Fatalf("%q %v", out, err)
	}
	if len(notes) != 1 || !strings.Contains(notes[0].Tier, "Unicode") {
		t.Fatalf("notes = %+v", notes)
	}
}

func TestPlanUnicodeKeepsOffsetsAfterMultiByteFolds(t *testing.T) {
	body := "‘a’\nkeep…\nb\n"
	out, _, err := plan(t, body, Unicode, hk("'a'\n", "A\n"))
	if err != nil || out != "A\nkeep…\nb\n" {
		t.Fatalf("%q %v", out, err)
	}
	out, _, err = plan(t, body, Unicode, hk("keep...\n", "kept\n"))
	if err != nil || out != "‘a’\nkept\nb\n" {
		t.Fatalf("%q %v", out, err)
	}
}

func TestPlanUnicodeNeverFoldsLettersOrInvisibles(t *testing.T) {
	// A zero-width space is not folded away: the file's hidden character stays
	// visible to the match and the hunk is not found.
	if _, _, err := plan(t, "a​b\n", Unicode, hk("ab\n", "x\n")); err == nil {
		t.Fatal("an invisible rune was matched away")
	}
}

func TestPlanZeroMatchHintCarriesNoFileText(t *testing.T) {
	secret := "SECRET_PASSWORD_VALUE"
	body := "line one\nconst token = \"" + secret + "\"\nline three\n"
	_, _, err := plan(t, body, Whitespace, hk("const token = \"other\"\n", "y\n"))
	if err == nil {
		t.Fatal("expected a miss")
	}
	msg := err.Error()
	if strings.Contains(msg, secret) || strings.Contains(msg, "const") {
		t.Fatalf("hint quotes file or hunk text: %s", msg)
	}
	if !strings.Contains(msg, "hunk 1 of f.go") || !strings.Contains(msg, "closest line is 2") {
		t.Fatalf("hint lacks the hunk, path or line number: %s", msg)
	}
}

func TestPlanNoLineResemblesHint(t *testing.T) {
	_, _, err := plan(t, "alpha\nbeta\n", Strict, hk("zzzzzz qqqqqq\n", "x\n"))
	if err == nil || !strings.Contains(err.Error(), "no line resembles") {
		t.Fatalf("%v", err)
	}
}

func TestPlanHeaderScopesTheSearch(t *testing.T) {
	body := "func a() {\n\treturn 1\n}\nfunc b() {\n\treturn 1\n}\n"
	// Without a header the hunk matches twice.
	if _, _, err := plan(t, body, Strict, hk("\treturn 1\n", "\treturn 2\n")); err == nil {
		t.Fatal("an ambiguous hunk applied")
	}
	// With one the hunk's scope starts after the named line; the first
	// occurrence after it must still be the only one in that scope... which
	// it is not, so the header does not rescue a hunk that repeats later.
	_, _, err := plan(t, body, Strict, Hunk{Header: "func b() {", Old: "\treturn 1\n", New: "\treturn 2\n"})
	if err != nil {
		t.Fatalf("scoped hunk: %v", err)
	}
	out, _, _ := plan(t, body, Strict, Hunk{Header: "func b() {", Old: "\treturn 1\n", New: "\treturn 2\n"})
	if out != "func a() {\n\treturn 1\n}\nfunc b() {\n\treturn 2\n}\n" {
		t.Fatalf("%q", out)
	}
}

func TestPlanHeaderMustBeOneLine(t *testing.T) {
	if _, _, err := plan(t, "x\nx\ny\n", Strict, Hunk{Header: "x", Old: "y\n", New: "z\n"}); err == nil || !strings.Contains(err.Error(), "matches 2 lines") {
		t.Fatalf("%v", err)
	}
	if _, _, err := plan(t, "x\ny\n", Strict, Hunk{Header: "nope", Old: "y\n", New: "z\n"}); err == nil || !strings.Contains(err.Error(), "not in the file") {
		t.Fatalf("%v", err)
	}
	if _, _, err := plan(t, "  x\ny\n", Strict, Hunk{Header: "x", Old: "y\n", New: "z\n"}); err == nil {
		t.Fatal("strict ignored the header's indentation")
	}
	if _, _, err := plan(t, "  x\ny\n", Whitespace, Hunk{Header: "x", Old: "y\n", New: "z\n"}); err != nil {
		t.Fatalf("%v", err)
	}
}

func TestPlanSeveralHunksAreLocatedInTheOriginal(t *testing.T) {
	body := "a\nb\nc\nd\n"
	out, _, err := plan(t, body, Strict, hk("d\n", "D\n"), hk("a\n", "A\n"))
	if err != nil || out != "A\nb\nc\nD\n" {
		t.Fatalf("%q %v", out, err)
	}
}

func TestPlanOverlappingHunksFail(t *testing.T) {
	_, _, err := plan(t, "a\nb\nc\n", Strict, hk("a\nb\n", "x\n"), hk("b\nc\n", "y\n"))
	if err == nil || !strings.Contains(err.Error(), "overlap") {
		t.Fatalf("%v", err)
	}
}

func TestPlanFailureNamesTheHunk(t *testing.T) {
	_, _, err := plan(t, "a\nb\n", Strict, hk("a\n", "A\n"), hk("zzz\n", "y\n"))
	if err == nil || !strings.Contains(err.Error(), "hunk 2 of f.go") {
		t.Fatalf("%v", err)
	}
}

func TestPlanEndOfFile(t *testing.T) {
	body := "x\ny\nx\ny\n"
	out, _, err := plan(t, body, Strict, Hunk{Old: "x\ny\n", New: "x\nY\n", AtEnd: true})
	if err != nil || out != "x\ny\nx\nY\n" {
		t.Fatalf("%q %v", out, err)
	}
	if _, _, err := plan(t, "x\ny\nz\n", Strict, Hunk{Old: "x\ny\n", New: "q\n", AtEnd: true}); err == nil {
		t.Fatal("an end-of-file hunk matched away from the end")
	}
}

func TestPlanLastLineWithoutNewline(t *testing.T) {
	out, _, err := plan(t, "a\nb", Strict, hk("b\n", "B\n"))
	if err != nil || out != "a\nB" {
		t.Fatalf("%q %v", out, err)
	}
	out, _, err = plan(t, "a\nb", Strict, hk("b\n", "B\nC\n"))
	if err != nil || out != "a\nB\nC" {
		t.Fatalf("%q %v", out, err)
	}
}

func TestPlanAppendAtEnd(t *testing.T) {
	out, _, err := plan(t, "a\nb", Strict, Hunk{New: "c\n", AtEnd: true})
	if err != nil || out != "a\nb\nc\n" {
		t.Fatalf("%q %v", out, err)
	}
	out, _, err = plan(t, "a\r\nb\r\n", Strict, Hunk{New: "c\n", AtEnd: true})
	if err != nil || out != "a\r\nb\r\nc\r\n" {
		t.Fatalf("%q %v", out, err)
	}
}

func TestPlanDeletingLines(t *testing.T) {
	out, _, err := plan(t, "a\nb\nc\n", Strict, hk("b\n", ""))
	if err != nil || out != "a\nc\n" {
		t.Fatalf("%q %v", out, err)
	}
}

func TestPlanMixedLineEndingsFailClosed(t *testing.T) {
	_, _, err := plan(t, "a\r\nb\nc\r\n", Whitespace, hk("a\nb\n", "x\n"))
	if err == nil || !strings.Contains(err.Error(), "mix line endings") {
		t.Fatalf("%v", err)
	}
}

func TestParseTolerance(t *testing.T) {
	for in, want := range map[string]Tolerance{"strict": Strict, "whitespace": Whitespace, "unicode": Unicode} {
		got, ok := ParseTolerance(in)
		if !ok || got != want || got.String() != in {
			t.Fatalf("%s: %v %v", in, got, ok)
		}
	}
	if _, ok := ParseTolerance("loose"); ok {
		t.Fatal("an unknown tolerance parsed")
	}
	if Narrower(Unicode, Whitespace) != Whitespace || Narrower(Strict, Unicode) != Strict {
		t.Fatal("Narrower picks the looser one")
	}
}

func FuzzPlan(f *testing.F) {
	f.Add("a\nb\nc\n", "b\n", "B\n", 1, false)
	f.Add("a\r\n\tb\r\n", "b\n", "x\n", 2, false)
	f.Add("‘q’\n", "'q'\n", "z\n", 2, true)
	f.Fuzz(func(t *testing.T, body, old, nw string, tol int, eof bool) {
		if old == "" || strings.ContainsRune(body+old+nw, 0) {
			return
		}
		tolerance := Tolerance(((tol % 3) + 3) % 3)
		out, _, err := Plan("f", body, []Hunk{{Old: old, New: nw, AtEnd: eof}}, tolerance)
		if err == nil && out == body {
			t.Fatal("a plan that changes nothing succeeded")
		}
	})
}
