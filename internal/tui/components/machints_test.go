package components

import (
	"strings"
	"testing"
)

func withMacHints(t *testing.T, on bool) {
	t.Helper()
	prev := MacKeyHintsOn()
	SetMacKeyHints(on)
	t.Cleanup(func() { SetMacKeyHints(prev) })
}

func TestMacKeyTextIsIdentityWhenOff(t *testing.T) {
	withMacHints(t, false)
	for _, s := range []string{"f9 opens the runs panel", "ctrl+x copies", ""} {
		if got := MacKeyText(s); got != s {
			t.Errorf("%q changed to %q", s, got)
		}
	}
	if got := MacFKeyHint("f9"); got != "f9" {
		t.Errorf("MacFKeyHint off = %q", got)
	}
}

func TestMacKeyTextAnnotatesEachKeyOnce(t *testing.T) {
	withMacHints(t, true)
	got := MacKeyText("f9 then tab; f9 again, and f3 toggles guardrails")
	want := "f9 (Fn+⏭) then tab; f9 again, and f3 (Fn+Mission Control) toggles guardrails"
	if got != want {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
	if again := MacKeyText(got); again != got {
		t.Fatalf("not idempotent: %q", again)
	}
}

func TestMacKeyTextLeavesNonKeysAlone(t *testing.T) {
	withMacHints(t, true)
	for _, s := range []string{"read f1.txt", "path/f2/x", "the f13 key", "self", "leaf9", "shift+tab"} {
		if got := MacKeyText(s); got != s {
			t.Errorf("%q changed to %q", s, got)
		}
	}
}

func TestMacFKeyHintCoversEveryFKey(t *testing.T) {
	withMacHints(t, true)
	for n := 1; n <= 12; n++ {
		key := "f" + string(rune('0'+n%10))
		if n >= 10 {
			key = "f1" + string(rune('0'+n-10))
		}
		if got := MacFKeyHint(key); !strings.HasPrefix(got, key+" (Fn+") || strings.HasSuffix(got, "(Fn+)") {
			t.Errorf("%s: %q", key, got)
		}
	}
	if got := MacFKeyHint("ctrl+x"); got != "ctrl+x" {
		t.Errorf("a non F key changed: %q", got)
	}
}

func TestMacHintsNameNoAltChord(t *testing.T) {
	for k, v := range macFKeys {
		if strings.Contains(strings.ToLower(v), "alt+") {
			t.Errorf("%s hint names an alt chord: %q", k, v)
		}
	}
}
