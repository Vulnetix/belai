package tui

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The help body has to list every command the registry exposes, or /help
// silently hides a feature.
func TestHelpTextListsEveryCommand(t *testing.T) {
	r := NewRegistry(t.TempDir())
	got := helpText(r)
	for _, n := range r.Names() {
		if !strings.Contains(got, "/"+n+" ") {
			t.Errorf("helpText is missing /%s", n)
		}
	}
	if !strings.Contains(got, "— alias of /clear") {
		t.Errorf("helpText does not describe the /new alias:\n%s", got)
	}
}

// Every section carries a title and at least one binding, and the descriptions
// are prose rather than a repeat of the keycap.
func TestKeySectionsWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for _, s := range keySections() {
		if s.Title == "" {
			t.Errorf("section with no title")
		}
		if seen[s.Title] {
			t.Errorf("duplicate section %q", s.Title)
		}
		seen[s.Title] = true
		if len(s.Bindings) == 0 {
			t.Errorf("section %q has no bindings", s.Title)
		}
		for _, k := range s.Bindings {
			if k.Keys == "" || k.Desc == "" {
				t.Errorf("section %q has an incomplete binding %+v", s.Title, k)
			}
			if k.Keys == k.Desc {
				t.Errorf("section %q binding %q has no description", s.Title, k.Keys)
			}
		}
	}
}

// The globals are the ones with no on-screen help bar anywhere, so they are the
// ones /help must carry.
func TestHelpTextListsGlobalAndChatKeys(t *testing.T) {
	got := helpText(NewRegistry(t.TempDir()))
	for _, want := range []string{
		"ctrl+c", "ctrl+d", "ctrl+r", "ctrl+t", "ctrl+alt+c", "ctrl+alt+p",
		"shift+tab", "ctrl+l", "ctrl+o", "alt+s", "ctrl+j",
		"pgup", "ctrl+home", "ctrl+end", "shift+up", "shift+down",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("helpText is missing the %s binding", want)
		}
	}
}

// keyCase matches the key literals the Update switches dispatch on. Anything a
// handler reacts to should be documented, so this is the drift guard: add a
// binding to a switch without adding it to keySections and this fails.
var keyCase = regexp.MustCompile(`case "([a-z+ ,"]+)":`)

// isKeyName reports whether a switch case is a key name rather than one of the
// other short strings the package switches on, such as a settings key or a
// message role.
func isKeyName(s string) bool {
	if strings.Contains(s, "+") {
		return strings.HasPrefix(s, "ctrl+") || strings.HasPrefix(s, "alt+") || strings.HasPrefix(s, "shift+")
	}
	switch s {
	case "esc", "enter", "tab", "space", "up", "down", "left", "right",
		"pgup", "pgdown", "home", "end", "backspace", "delete", "insert":
		return true
	}
	return len(s) == 1 && s[0] >= 'a' && s[0] <= 'z'
}

func TestHelpTextCoversEveryHandledKey(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	body := helpText(NewRegistry(t.TempDir()))
	missing := map[string][]string{}
	scanned := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		for _, m := range keyCase.FindAllStringSubmatch(string(src), -1) {
			for _, k := range strings.Split(m[1], `", "`) {
				k = strings.Trim(k, `" `)
				if !isKeyName(k) {
					continue
				}
				scanned++
				if !strings.Contains(body, k) {
					missing[k] = append(missing[k], f)
				}
			}
		}
	}
	// A regex that stops matching would make this test vacuously green.
	if scanned < 50 {
		t.Fatalf("only %d key cases scanned — the key-case regex has stopped matching", scanned)
	}
	if len(missing) > 0 {
		keys := make([]string, 0, len(missing))
		for k := range missing {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			t.Errorf("key %q is handled in %s but absent from /help", k, strings.Join(missing[k], ", "))
		}
	}
}

// /help writes one system message holding the whole body.
func TestHelpCommandAddsOneSystemMessage(t *testing.T) {
	a := New(Options{})
	before := len(a.messages)
	a.handleCommand("/help")
	if len(a.messages) != before+1 {
		t.Fatalf("messages = %d, want %d", len(a.messages), before+1)
	}
	last := a.messages[len(a.messages)-1]
	if last.Role != "system" {
		t.Fatalf("role = %q, want system", last.Role)
	}
	if !strings.Contains(last.Text(), "commands:") || !strings.Contains(last.Text(), "shift+tab") {
		t.Fatalf("help message lacks commands or keys:\n%s", last.Text())
	}
}
