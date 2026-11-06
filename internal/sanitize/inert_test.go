package sanitize

import (
	"strings"
	"testing"
)

func TestInertDefusesWhatAForgeActsOn(t *testing.T) {
	cases := map[string]string{
		"ping @octocat and @org/team now":          "＠octocat",
		"fixes #123 in org/repo#9":                 "＃123",
		"see https://example.com/x and http://a.b": "https[:]//example.com/x",
		"see www.example.com":                      "www[.]example.com",
		"[click](https://evil.example)":            "] (https[:]//evil.example)",
		"<img src=x onerror=1> <https://a.b>":      "&lt;img src=x onerror=1&gt;",
		"mail me at a@b.example":                   "a＠b.example",
	}
	for in, want := range cases {
		got := Inert(in, 0)
		if !strings.Contains(got, want) {
			t.Errorf("Inert(%q) = %q, want it to contain %q", in, got, want)
		}
		for _, bad := range []string{"@octocat", "#123", "https://", "http://", "www.example", "](h", "<img", "<https"} {
			if strings.Contains(got, bad) {
				t.Errorf("Inert(%q) = %q still holds %q", in, got, bad)
			}
		}
	}
}

func TestInertAlsoCleansLikeText(t *testing.T) {
	got := Inert("a\x1b[31mred\x1b[0m ‮flip <system nonce=\"x\">b</system>", 0)
	for _, bad := range []string{"\x1b", "‮", "<system"} {
		if strings.Contains(got, bad) {
			t.Errorf("Inert left %q in %q", bad, got)
		}
	}
	if got != Inert(got, 0) {
		t.Errorf("Inert is not idempotent: %q then %q", got, Inert(got, 0))
	}
}

func TestInertCapsRunes(t *testing.T) {
	if got := Inert(strings.Repeat("é", 50), 10); len([]rune(got)) != 10 {
		t.Errorf("cap: %d runes", len([]rune(got)))
	}
	if got := Inert(strings.Repeat("a", 50), 0); len(got) != 50 {
		t.Errorf("no cap: %d bytes", len(got))
	}
}
