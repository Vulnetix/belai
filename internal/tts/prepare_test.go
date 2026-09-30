package tts

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func joined(p []string) string { return strings.Join(p, " ") }

func TestPrepareDropsCodeAndSaysSo(t *testing.T) {
	got := joined(Prepare("Run this:\n\n```go\nfmt.Println(\"x\")\n```\n\nThen check."))
	if strings.Contains(got, "Println") || !strings.Contains(got, "Code omitted.") {
		t.Fatalf("code was read or not announced: %q", got)
	}
	if !strings.Contains(got, "Then check.") {
		t.Fatalf("text after the block lost: %q", got)
	}
}

func TestPrepareAnUnclosedFenceIsStillOmitted(t *testing.T) {
	got := joined(Prepare("Before.\n\n```\nsecret := 1\nmore"))
	if strings.Contains(got, "secret") {
		t.Fatalf("unterminated fence was read: %q", got)
	}
}

func TestPrepareStripsMarkdownAndUrls(t *testing.T) {
	in := "# Title\n\n- **bold** item with `code` and a [link text](https://example.com/x)\n- see https://example.com/very/long/path?x=1\n\n> quoted\n\n---\n"
	got := joined(Prepare(in))
	for _, bad := range []string{"#", "**", "`", "](", "https://", "example.com", ">"} {
		if strings.Contains(got, bad) {
			t.Errorf("%q survived in %q", bad, got)
		}
	}
	for _, want := range []string{"Title.", "bold item with code and a link text", "see link", "quoted"} {
		if !strings.Contains(got, want) {
			t.Errorf("%q missing from %q", want, got)
		}
	}
}

func TestPrepareTablesReadAsRows(t *testing.T) {
	got := joined(Prepare("| Name | Count |\n| --- | --- |\n| a | 2 |\n"))
	if strings.Contains(got, "---") || strings.Contains(got, "|") || !strings.Contains(got, "a, 2") {
		t.Fatalf("table = %q", got)
	}
}

func TestPrepareGroupsByLengthAndKeepsOrder(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 200; i++ {
		b.WriteString("This is sentence number one. ")
	}
	groups := Prepare(b.String())
	if len(groups) < 3 {
		t.Fatalf("%d groups for a long message", len(groups))
	}
	for i, g := range groups {
		if n := utf8.RuneCountInString(g); n > GroupChars {
			t.Errorf("group %d has %d runes", i, n)
		}
	}
}

func TestPrepareSplitsAnOverlongSentenceAtAWord(t *testing.T) {
	groups := Prepare(strings.Repeat("word ", 500) + "end.")
	if len(groups) < 2 {
		t.Fatalf("groups = %d", len(groups))
	}
	for _, g := range groups {
		if strings.HasPrefix(g, "ord") || strings.HasSuffix(g, " wor") {
			t.Fatalf("split inside a word: %q", g)
		}
	}
}

func TestPrepareIsCappedAndEmptyIsNil(t *testing.T) {
	if Prepare("") != nil || Prepare("```\nonly code\n```") == nil {
		t.Fatal("empty input is nil; a code-only message still says so")
	}
	total := 0
	for _, g := range Prepare(strings.Repeat("A sentence here. ", 2000)) {
		total += utf8.RuneCountInString(g)
	}
	if total > MaxChars+GroupChars {
		t.Fatalf("read %d runes, cap is %d", total, MaxChars)
	}
}

func TestPrepareKeepsAbbreviationsTogether(t *testing.T) {
	got := Prepare("Use a flag, e.g. the verbose one. Done.")
	if len(got) != 1 || !strings.Contains(got[0], "e.g. the verbose one.") {
		t.Fatalf("got %q", got)
	}
}
