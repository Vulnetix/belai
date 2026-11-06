package fuzzy

import (
	"strings"
	"testing"
)

func apply(t *testing.T, body, old, newS string, relaxed bool) (string, Result, error) {
	t.Helper()
	r := Locate(body, old, Options{Relaxed: relaxed})
	if r.Matches != 1 {
		return "", r, nil
	}
	out, err := Apply(body, newS, r)
	return out, r, err
}

func TestLocateExactAlwaysWins(t *testing.T) {
	body := "a\nfoo\nb\nfoo  \n"
	r := Locate(body, "foo", Options{Relaxed: true})
	if r.Tier != TierExact || r.Matches != 2 {
		t.Fatalf("got %+v, want exact with 2 matches", r)
	}
	if r.Start != strings.Index(body, "foo") {
		t.Fatalf("start = %d", r.Start)
	}
}

func TestLocateWithoutRelaxedIsSubstringOnly(t *testing.T) {
	r := Locate("a\r\nb\r\n", "a\nb\n", Options{})
	if r.Tier != TierNone || r.Matches != 0 {
		t.Fatalf("got %+v", r)
	}
}

func TestLocateLineEndingTier(t *testing.T) {
	body := "x\r\ny\r\nz\r\n"
	out, r, err := apply(t, body, "x\ny\n", "x\nY\nnew\n", true)
	if err != nil || r.Tier != TierLineEnding {
		t.Fatalf("tier %v err %v", r.Tier, err)
	}
	if want := "x\r\nY\r\nnew\r\nz\r\n"; out != want {
		t.Fatalf("got %q want %q", out, want)
	}
}

func TestLocateLoneCRIsALineEnding(t *testing.T) {
	out, r, err := apply(t, "a\rb\rc\r", "a\nb", "a\nB", true)
	if err != nil || r.Tier != TierLineEnding {
		t.Fatalf("tier %v err %v", r.Tier, err)
	}
	if out != "a\rB\rc\r" {
		t.Fatalf("got %q", out)
	}
}

func TestLocateTrailingWhitespaceTier(t *testing.T) {
	body := "if x {  \n\treturn\t\n}\n"
	out, r, err := apply(t, body, "if x {\n\treturn\n}", "if y {\n\treturn 1\n}", true)
	if err != nil || r.Tier != TierTrailing {
		t.Fatalf("tier %v err %v", r.Tier, err)
	}
	if want := "if y {\n\treturn 1\n}\n"; out != want {
		t.Fatalf("got %q want %q", out, want)
	}
}

func TestLocateIndentTierAddsDelta(t *testing.T) {
	body := "func f() {\n\tif ok {\n\t\tdo()\n\t}\n}\n"
	old := "if ok {\n\tdo()\n}"
	out, r, err := apply(t, body, old, "if ok {\n\tdo()\n\tmore()\n}", true)
	if err != nil || r.Tier != TierIndent {
		t.Fatalf("tier %v err %v", r.Tier, err)
	}
	if want := "func f() {\n\tif ok {\n\t\tdo()\n\t\tmore()\n\t}\n}\n"; out != want {
		t.Fatalf("got %q want %q", out, want)
	}
}

func TestLocateIndentTierRemovesDelta(t *testing.T) {
	body := "if ok {\n\tdo()\n}\n"
	old := "    if ok {\n    \tdo()\n    }"
	out, r, err := apply(t, body, old, "    if no {\n    \tdo()\n    }", true)
	if err != nil || r.Tier != TierIndent {
		t.Fatalf("tier %v err %v", r.Tier, err)
	}
	if want := "if no {\n\tdo()\n}\n"; out != want {
		t.Fatalf("got %q want %q", out, want)
	}
}

func TestLocateIndentDeltaMustBeConsistent(t *testing.T) {
	body := "  a\n    b\n"
	r := Locate(body, "a\n b", Options{Relaxed: true})
	if r.Matches != 0 {
		t.Fatalf("inconsistent delta matched: %+v", r)
	}
}

func TestLocateIndentBlankLinesNeedNotCarryIndent(t *testing.T) {
	body := "\tfoo\n\n\tbar\n"
	out, r, err := apply(t, body, "foo\n\nbar", "foo\n\nbaz", true)
	if err != nil || r.Tier != TierIndent {
		t.Fatalf("tier %v err %v", r.Tier, err)
	}
	if out != "\tfoo\n\n\tbaz\n" {
		t.Fatalf("got %q", out)
	}
}

func TestLocateMixedIndentDeltaRefuses(t *testing.T) {
	body := "\t  foo\n\t  bar\n"
	r := Locate(body, "foo\nbar", Options{Relaxed: true})
	if r.Matches != 1 || r.Problem != ProblemMixedIndent {
		t.Fatalf("got %+v", r)
	}
	if _, err := Refit("foo\nbaz", r); err == nil {
		t.Fatal("Refit accepted a mixed indent delta")
	}
}

func TestLocateMixedLineEndingsRefuses(t *testing.T) {
	body := "a\r\nb\nc\r\n"
	r := Locate(body, "a\nb\n", Options{Relaxed: true})
	if r.Matches != 1 || r.Problem != ProblemMixedLineEndings {
		t.Fatalf("got %+v", r)
	}
	if _, err := Apply(body, "a\nB\n", r); err == nil {
		t.Fatal("Apply accepted mixed line endings")
	}
}

func TestLocateAmbiguousRelaxedMatchIsCounted(t *testing.T) {
	body := "x  \nx\t\n"
	r := Locate(body, "x\n", Options{Relaxed: true})
	if r.Tier != TierTrailing || r.Matches != 2 {
		t.Fatalf("got %+v", r)
	}
	if _, err := Refit("y\n", r); err == nil {
		t.Fatal("Refit fitted an ambiguous match")
	}
}

func TestLocateRelaxedNeedsTextAndWholeLines(t *testing.T) {
	if r := Locate("a \n b\n", "  \n", Options{Relaxed: true}); r.Matches != 0 {
		t.Fatalf("blank old matched: %+v", r)
	}
	if r := Locate("foo bar\n", "oo ba ", Options{Relaxed: true}); r.Matches != 0 {
		t.Fatalf("part of a line matched: %+v", r)
	}
	if r := Locate("anything", "", Options{Relaxed: true}); r.Matches != 0 {
		t.Fatalf("empty old matched: %+v", r)
	}
}

func TestLocateTrailingNewlineInOldNeedsOneInBody(t *testing.T) {
	if r := Locate("a\nb", "a\nb\n", Options{Relaxed: true}); r.Matches != 0 {
		t.Fatalf("matched a last line with no terminator: %+v", r)
	}
	out, _, err := apply(t, "a \nb\n", "a\nb\n", "A\nB\n", true)
	if err != nil || out != "A\nB\n" {
		t.Fatalf("got %q err %v", out, err)
	}
}

func TestRefitRoundTripKeepsTheFilesStyle(t *testing.T) {
	for _, eol := range []string{"\n", "\r\n", "\r"} {
		body := strings.Join([]string{"\tone  ", "\ttwo", "\tthree"}, eol) + eol
		old := "one\ntwo\n"
		r := Locate(body, old, Options{Relaxed: true})
		if r.Matches != 1 {
			t.Fatalf("eol %q: %+v", eol, r)
		}
		out, err := Apply(body, "one\nTWO\nextra\n", r)
		if err != nil {
			t.Fatal(err)
		}
		want := strings.Join([]string{"\tone", "\tTWO", "\textra", "\tthree"}, eol) + eol
		if out != want {
			t.Fatalf("eol %q: got %q want %q", eol, out, want)
		}
	}
}

func TestRefitRefusesALineThatLacksTheRemovedIndent(t *testing.T) {
	body := "a\nb\n"
	r := Locate(body, "  a\n  b\n", Options{Relaxed: true})
	if r.Tier != TierIndent {
		t.Fatalf("got %+v", r)
	}
	if _, err := Refit("  a\nb\n", r); err == nil {
		t.Fatal("Refit dropped an indent the line did not have")
	}
}

func TestRefitExactIsUnchangedAndNoneErrors(t *testing.T) {
	if got, err := Refit("x\r\ny", Result{Tier: TierExact, Matches: 1}); err != nil || got != "x\r\ny" {
		t.Fatalf("got %q %v", got, err)
	}
	if _, err := Refit("x", Result{}); err == nil {
		t.Fatal("Refit fitted nothing")
	}
	if _, err := Apply("body", "x", Result{Tier: TierLineEnding, Matches: 1, Start: 3, End: 99}); err == nil {
		t.Fatal("Apply accepted a span outside the body")
	}
}

func TestNearestRanksAndClassifies(t *testing.T) {
	body := "package x\n\nfunc other() {}\n\nfunc target() {\n\treturn\n}\n"
	got := Nearest(body, "func target() {\n    return\n}\n", 3)
	if len(got) == 0 || got[0].Line != 5 {
		t.Fatalf("got %+v", got)
	}
	if got[0].Differs != DiffersIndent {
		t.Fatalf("differs = %q", got[0].Differs)
	}
	if got[0].Snippet != "func target() {" {
		t.Fatalf("snippet = %q", got[0].Snippet)
	}
}

func TestNearestDifferencesAreMeasuredNotRead(t *testing.T) {
	cases := []struct {
		body, old string
		want      Differs
	}{
		{"alpha  \nbeta\n", "alpha\nbeta", DiffersTrailing},
		{"alpha\nbetx\n", "alpha\nbeta", DiffersContent},
		{"  alpha\nbeta\n", "alpha\nbeta", DiffersIndent},
	}
	for _, c := range cases {
		got := Nearest(c.body, c.old, 1)
		if len(got) != 1 || got[0].Differs != c.want {
			t.Errorf("%q vs %q: got %+v want %s", c.body, c.old, got, c.want)
		}
	}
}

func TestNearestBounds(t *testing.T) {
	if Nearest("", "x", 3) != nil || Nearest("x", "", 3) != nil || Nearest("x", "  \n", 3) != nil || Nearest("x", "x", 0) != nil {
		t.Fatal("expected nil for empty input or n below 1")
	}
	body := strings.Repeat("handle(request)\n", 50)
	if got := Nearest(body, "handle(request)\n", 3); len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	// A window the size of old keeps two candidates from covering the same lines.
	got := Nearest(strings.Repeat("a\n", 10), "a\na\na\n", 10)
	for i := 1; i < len(got); i++ {
		for j := 0; j < i; j++ {
			if d := got[i].Line - got[j].Line; d < 3 && -d < 3 {
				t.Fatalf("candidates %d and %d overlap: %+v", j, i, got)
			}
		}
	}
}

func TestNearestSnippetIsCleanAndCapped(t *testing.T) {
	long := "func " + strings.Repeat("x", 300) + "\x1b[31m‮(a)"
	got := Nearest(long+"\n", "func xxx(a)\n", 1)
	if len(got) != 1 {
		t.Fatalf("got %+v", got)
	}
	s := got[0].Snippet
	if len([]rune(s)) > SnippetRunes {
		t.Fatalf("snippet has %d runes", len([]rune(s)))
	}
	if strings.ContainsAny(s, "\x1b‮") {
		t.Fatalf("snippet kept control or bidi runes: %q", s)
	}
}

func TestTierAndProblemNames(t *testing.T) {
	for _, tier := range []Tier{TierNone, TierExact, TierLineEnding, TierTrailing, TierIndent} {
		if tier.String() == "" {
			t.Errorf("tier %d has no name", tier)
		}
	}
	if ProblemNone.String() != "" || ProblemMixedIndent.String() == "" || ProblemMixedLineEndings.String() == "" {
		t.Fatal("problem names")
	}
}

func TestNearestRanksATypoedLine(t *testing.T) {
	body := "alpha\nsecret_token := 1\nomega\n"
	got := Nearest(body, "secret_token := 2\n", 2)
	if len(got) == 0 || got[0].Line != 2 || got[0].Differs != DiffersContent {
		t.Fatalf("got %+v", got)
	}
}

func TestLocateLinesExactIsWholeLine(t *testing.T) {
	body := "abc\nb\nxb\n"
	// Plain exact counts the substring wherever it sits.
	if r := Locate(body, "b\n", Options{}); r.Matches != 2 {
		t.Fatalf("plain exact = %d", r.Matches)
	}
	r := Locate(body, "b\n", Options{Lines: true})
	if r.Tier != TierExact || r.Matches != 1 || r.Start != 4 || r.End != 6 {
		t.Fatalf("lines exact = %+v", r)
	}
}

func TestLocateLinesCountsOverlappingStarts(t *testing.T) {
	r := Locate("x\nx\nx\n", "x\nx\n", Options{Lines: true})
	if r.Matches != 2 {
		t.Fatalf("matches = %d, want 2", r.Matches)
	}
}

func TestLocateLinesFallsThroughOnAMidLineHit(t *testing.T) {
	if r := Locate("abc\n", "b\n", Options{Lines: true}); r.Matches != 0 {
		t.Fatalf("a strict whole-line search matched mid-line: %+v", r)
	}
	r := Locate("abc\n  b\n", "b\n", Options{Lines: true, Relaxed: true})
	if r.Tier != TierIndent || r.Matches != 1 {
		t.Fatalf("relaxed = %+v", r)
	}
}

func TestLocateLinesWithoutTrailingNewlineEndsAtALineEnd(t *testing.T) {
	if r := Locate("a\nbc\n", "a\nb", Options{Lines: true}); r.Matches != 0 {
		t.Fatalf("matched a prefix of a line: %+v", r)
	}
	if r := Locate("a\nb", "a\nb", Options{Lines: true}); r.Matches != 1 {
		t.Fatalf("missed the last line: %+v", r)
	}
}
