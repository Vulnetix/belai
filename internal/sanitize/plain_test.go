package sanitize

import (
	"math/rand"
	"strings"
	"testing"
)

// The plain-ASCII path must cut exactly what the general rune-by-rune pass
// cuts. This drives both over random text built from the fragments that
// matter (brackets, kinds, attributes, quotes, entities, case) and compares.
func TestPlainPassMatchesUnitPass(t *testing.T) {
	frags := []string{
		"<", ">", "</", "/", " ", "\n", "\t", "\r", "=", `"`, "'", "&&", "&", ";",
		"system", "SYSTEM", "agent", "tool", "tools", "attachment", "directive",
		"nonce", "integrity", "nonce=\"x\"", "integrity='y'", "a", "b", "x1", "-", "_",
		"<system>", "</system>", "<Agent nonce=\"n\">", "<b>", "</div>", "Vec<T>", "a < b && c > d",
	}
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < 20000; i++ {
		var b strings.Builder
		for n := rng.Intn(14); n >= 0; n-- {
			b.WriteString(frags[rng.Intn(len(frags))])
		}
		s := b.String()
		if !plainScan(s) {
			t.Fatalf("generator produced non-plain text %q", s)
		}
		got, gc := stripPlain(s)
		want, wc := stripUnits(s)
		if got != want || gc != wc {
			t.Fatalf("stripPlain(%q) = %q,%v; stripUnits = %q,%v", s, got, gc, want, wc)
		}
	}
}

func TestPlainScanRejectsWhatNeedsFolding(t *testing.T) {
	for _, s := range []string{"a\x00b", "a\x1bb", "café", "a&lt;b", "a&#60;b", "a\x7fb", "​<x>"} {
		if plainScan(s) {
			t.Errorf("plainScan(%q) = true, want false", s)
		}
	}
	for _, s := range []string{"a && b", "x < y", "a&b", "&&&&&&&&&&&&&&&&&&&", "Vec<T>\n\t\r"} {
		if !plainScan(s) {
			t.Errorf("plainScan(%q) = false, want true", s)
		}
	}
}

func TestTextPlainFastPathAgrees(t *testing.T) {
	for _, s := range []string{"", "hello\nworld\t!", "a <system> b", "x &lt;agent&gt; y", "cr\rlf", "a\x00b", "é<agent>"} {
		slow := Sanitize(strings.Map(textRune, s))
		if got := Text(s); got != slow && plainText(s) {
			t.Errorf("Text(%q) = %q, want %q", s, got, slow)
		}
	}
}
