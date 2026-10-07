package libitem

import (
	"encoding/json"
	"strings"
	"testing"
)

func hookDoc(t *testing.T, edit func(m map[string]any)) []byte {
	t.Helper()
	m := map[string]any{"name": "pix", "hooks": map[string]any{
		"PreToolUse": []any{map[string]any{"matcher": "Bash|Edit", "hooks": []any{map[string]any{"type": "command", "command": "vulnetix agent hook", "timeout": 30}}}},
	}}
	if edit != nil {
		edit(m)
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func hookWith(event string, groups ...any) func(map[string]any) {
	return func(m map[string]any) { m["hooks"] = map[string]any{event: groups} }
}

func group(handlers ...any) map[string]any { return map[string]any{"hooks": handlers} }
func cmd(c string) map[string]any          { return map[string]any{"type": "command", "command": c} }

// The rules are vdb-site's belaiValidateHook, case for case.
func TestValidateHook(t *testing.T) {
	tooMany := make([]any, MaxHookHandlers+1)
	for i := range tooMany {
		tooMany[i] = cmd("a")
	}
	groups := make([]any, MaxHookGroups+1)
	for i := range groups {
		groups[i] = group(cmd("a"))
	}
	cases := []struct {
		name string
		edit func(map[string]any)
		err  string
	}{
		{"the base", nil, ""},
		{"a description", func(m map[string]any) { m["description"] = "Guards installs" }, ""},
		{"an unknown key", func(m map[string]any) { m["run"] = "x" }, "unknown field"},
		{"no hooks", func(m map[string]any) { delete(m, "hooks") }, "at least one event"},
		{"empty hooks", func(m map[string]any) { m["hooks"] = map[string]any{} }, "at least one event"},
		{"hooks not an object", func(m map[string]any) { m["hooks"] = []any{} }, "must be an object"},
		{"an unknown event", hookWith("OnSave", group(cmd("a"))), "not an event"},
		{"an event with no groups", hookWith("Stop"), "at least one group"},
		{"too many groups", hookWith("PreToolUse", groups...), "the most is 8"},
		{"a stray group key", hookWith("PreToolUse", map[string]any{"hooks": []any{cmd("a")}, "if": "x"}), "unknown field"},
		{"a group with no handlers", hookWith("PreToolUse", map[string]any{"matcher": "Bash"}), "at least one handler"},
		{"too many handlers", hookWith("PreToolUse", group(tooMany...)), "the most is 8"},
		{"a matcher on a prompt event", hookWith("UserPromptSubmit", map[string]any{"matcher": "x", "hooks": []any{cmd("a")}}), "nothing to match"},
		{"an empty matcher on a prompt event", hookWith("UserPromptSubmit", map[string]any{"matcher": "", "hooks": []any{cmd("a")}}), ""},
		{"a matcher too long", hookWith("PreToolUse", map[string]any{"matcher": strings.Repeat("a", 129), "hooks": []any{cmd("a")}}), "the most is 128"},
		{"a handler that is not a command", hookWith("Stop", group(map[string]any{"type": "prompt", "command": "a"})), "must be command"},
		{"no type", hookWith("Stop", group(map[string]any{"command": "a"})), "must be command"},
		{"no command", hookWith("Stop", group(map[string]any{"type": "command"})), "command is required"},
		{"a command over two lines", hookWith("Stop", group(cmd("a\nb"))), "control character"},
		{"a command with a trailing space", hookWith("Stop", group(cmd("a "))), "must not start or end with a space"},
		{"a command too long", hookWith("Stop", group(cmd(strings.Repeat("a", 513)))), "the most is 512"},
		{"a timeout of 600", hookWith("Stop", group(map[string]any{"type": "command", "command": "a", "timeout": 600})), ""},
		{"a timeout of 0", hookWith("Stop", group(map[string]any{"type": "command", "command": "a", "timeout": 0})), "from 1 to 600"},
		{"a timeout over 600", hookWith("Stop", group(map[string]any{"type": "command", "command": "a", "timeout": 601})), "from 1 to 600"},
		{"a timeout with a fraction", hookWith("Stop", group(map[string]any{"type": "command", "command": "a", "timeout": 1.5})), "whole number"},
		{"a stray handler key", hookWith("Stop", group(map[string]any{"type": "command", "command": "a", "async": true})), "unknown field"},
		{"a capital in the name", func(m map[string]any) { m["name"] = "Pix" }, "is not valid"},
		{"no name", func(m map[string]any) { delete(m, "name") }, "name is required"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			it, err := Validate(Hook, hookDoc(t, c.edit))
			if c.err == "" {
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				if it.Kind != Hook || it.Name == "" || it.SHA256 != Hash(it.Doc) {
					t.Fatalf("item = %+v", it)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Fatalf("err = %v, want %q", err, c.err)
			}
		})
	}
}

func TestHookIsInstallOnly(t *testing.T) {
	if !Hook.InstallOnly() || Skill.InstallOnly() || Command.InstallOnly() || MCP.InstallOnly() {
		t.Error("only a hook is install-only")
	}
}

// The bundle hash is the contract between the host and the library: vdb-site's
// belaiBundleHash pins the same values (belai_golden_hash_test.go).
func TestHashBundleGolden(t *testing.T) {
	doc := []byte(`{"hooks":{"Stop":[{"hooks":[{"command":"a.sh","type":"command"}]}]},"name":"x"}` + "\n")
	if got, want := HashBundle(doc, nil), Hash(doc); got != want {
		t.Errorf("no files = %s, want the document's own hash %s", got, want)
	}
	files := []BundleFile{{Path: "scripts/b.sh", SHA256: strings.Repeat("b", 64)}, {Path: "a.sh", SHA256: strings.Repeat("a", 64)}}
	const want = "341011b2221be4c90e63b86c5f7078e03daf4ed74230f576a9e8251840e57202"
	got := HashBundle(doc, files)
	if got != want {
		t.Errorf("golden = %s, want %s", got, want)
	}
	if got == HashBundle(doc, nil) {
		t.Error("files do not change the hash")
	}
	// Order of the list does not matter; a different file does.
	if got != HashBundle(doc, []BundleFile{files[1], files[0]}) {
		t.Error("the hash depends on the order given")
	}
	changed := []BundleFile{{Path: "scripts/b.sh", SHA256: strings.Repeat("c", 64)}, files[1]}
	if got == HashBundle(doc, changed) {
		t.Error("a changed script did not change the hash")
	}
}
