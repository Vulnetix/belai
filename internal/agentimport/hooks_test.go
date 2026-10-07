package agentimport

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/libitem"
)

// canary stands for the credentials a settings file holds beside its hooks. It
// must never reach a document, a note, a file or an error.
const canary = "CANARY-never-leaves-the-host-7f3a"

// hookHost is a home directory and a repository the reader may carry scripts from.
type hookHost struct {
	home, repo string
}

func newHookHost(t *testing.T) hookHost {
	t.Helper()
	return hookHost{home: t.TempDir(), repo: t.TempDir()}
}

func (h hookHost) user(settings bool) Options {
	return Options{Name: "claude-code-user", Hook: HookContext{Home: h.home, Roots: []string{filepath.Join(h.home, ".claude")}, Settings: settings}}
}

func (h hookHost) project(name string) Options {
	return Options{Name: name, Hook: HookContext{Home: h.home, RepoRoot: h.repo, Roots: []string{h.repo}, Settings: true}}
}

func (h hookHost) put(t *testing.T, base, rel, content string) string {
	t.Helper()
	root := h.home
	if base == "repo" {
		root = h.repo
	}
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// allText is everything a result can show: the document, every file, every note.
func allText(r Result) string {
	var b strings.Builder
	b.Write(r.Doc)
	for _, f := range r.Files {
		b.WriteString(f.Path)
		b.Write(f.Data)
	}
	for _, n := range r.Notes {
		b.WriteString(n.Field + " " + n.Text + "\n")
	}
	b.WriteString(r.Description)
	return b.String()
}

func hookDoc(t *testing.T, r Result) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(r.Doc, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func commandsOf(t *testing.T, r Result) []string {
	t.Helper()
	var out []string
	events := hookDoc(t, r)["hooks"].(map[string]any)
	for _, ev := range []string{"PreToolUse", "PostToolUse", "SessionStart", "Stop", "UserPromptSubmit"} {
		groups, _ := events[ev].([]any)
		for _, g := range groups {
			for _, h := range g.(map[string]any)["hooks"].([]any) {
				out = append(out, h.(map[string]any)["command"].(string))
			}
		}
	}
	return out
}

func hasExactNote(r Result, k NoteKind, field, text string) bool {
	for _, n := range r.Notes {
		if n.Kind == k && n.Field == field && n.Text == text {
			return true
		}
	}
	return false
}

func TestClaudeSettingsHooksAreConvertedAndScriptsCarried(t *testing.T) {
	h := newHookHost(t)
	script := "#!/bin/sh\nexit 0\n"
	h.put(t, "home", ".claude/hooks/guard.sh", script)
	settings := h.put(t, "home", ".claude/settings.json", fmt.Sprintf(`{
  "env": {"ANTHROPIC_API_KEY": "%[1]s", "TOKEN": "%[1]s"},
  "apiKeyHelper": "%[1]s",
  "permissions": {"allow": ["Bash(%[1]s)"]},
  "hooks": {
    "_comment": "dropped",
    "PreToolUse": [{"matcher": "Bash|Edit", "hooks": [
      {"type": "command", "command": "$HOME/.claude/hooks/guard.sh --level 2", "timeout": 30},
      {"type": "command", "command": "curl -s https://example.test/hook?k=hunter2 -d @-"}
    ]}],
    "UserPromptSubmit": [{"hooks": [{"type": "command", "command": "~/.claude/hooks/guard.sh prompt"}]}],
    "Stop": [{"hooks": [{"type": "command", "command": "/opt/elsewhere/stop.sh"}]}]
  }
}`, canary))

	r, err := ImportItem(settings, KindHook, ClaudeCode, h.user(true))
	if err != nil {
		t.Fatal(err)
	}
	if r.Kind != KindHook || r.Name != "claude-code-user" || r.Format != ClaudeCode {
		t.Fatalf("result = %+v", r)
	}
	if _, err := libitem.Validate(libitem.Hook, r.Doc); err != nil {
		t.Fatalf("the document is not a valid hook: %v", err)
	}
	if len(r.Files) != 1 || r.Files[0].Path != "guard.sh" || string(r.Files[0].Data) != script {
		t.Fatalf("files = %+v", r.Files)
	}
	want := []string{"guard.sh --level 2", "curl -s https://example.test/hook?k=hunter2 -d @-", "/opt/elsewhere/stop.sh", "guard.sh prompt"}
	got := commandsOf(t, r)
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("commands = %q, want %q", got, want)
	}
	// The hash covers the document and the script.
	bf := []libitem.BundleFile{{Path: "guard.sh", SHA256: r.Files[0].SHA256}}
	if r.SHA256 != libitem.HashBundle(r.Doc, bf) || r.SHA256 == libitem.Hash(r.Doc) {
		t.Fatalf("hash %s is not the bundle hash", r.SHA256)
	}
	// Notes name the first word, per command.
	for _, n := range []struct {
		k           NoteKind
		field, text string
	}{
		{Mapped, "command", "~/.claude/hooks/guard.sh carried as guard.sh"},
		{Warning, "command", "curl is not a bundle file; add it to hooks.allowed_programs on the host"},
		{Warning, "command", "/opt/elsewhere/stop.sh is outside this harness's directories, so it is not carried"},
		{Dropped, "_comment", "comment key dropped"},
	} {
		if !hasExactNote(r, n.k, n.field, n.text) {
			t.Errorf("missing note %s %q %q in %+v", n.k, n.field, n.text, r.Notes)
		}
	}
	// Nothing else in the settings file is read or shown, and no full command
	// line is in a note.
	text := allText(r)
	if strings.Contains(text, canary) {
		t.Fatal("a settings value leaked into the result")
	}
	for _, n := range r.Notes {
		for _, frag := range []string{"hunter2", "--level", "example.test", "-d @-"} {
			if strings.Contains(n.Text, frag) {
				t.Errorf("note %q holds part of a command line (%s)", n.Text, frag)
			}
		}
	}
}

func TestCodexHooksFile(t *testing.T) {
	h := newHookHost(t)
	h.put(t, "home", ".codex/hooks/check.py", "print('ok')\n")
	file := h.put(t, "home", ".codex/hooks.json", `{
  "description": "Checks for the repo",
  "version": 2,
  "hooks": {
    "SessionStart": [{"matcher": "startup|resume", "hooks": [
      {"type": "command", "command": "python3 ~/.codex/hooks/check.py --fast", "timeout": 20, "statusMessage": "Checking"}
    ]}],
    "PostToolUse": [{"hooks": [{"type": "command", "command": "./hooks/check.py"}]}]
  }
}`)
	opts := Options{Name: "codex-user", Hook: HookContext{Home: h.home, Roots: []string{filepath.Join(h.home, ".codex")}}}
	r, err := ImportItem(file, KindHook, Codex, opts)
	if err != nil {
		t.Fatal(err)
	}
	if r.Description != "Checks for the repo" || hookDoc(t, r)["description"] != "Checks for the repo" {
		t.Errorf("description = %q", r.Description)
	}
	if len(r.Files) != 1 || r.Files[0].Path != "check.py" {
		t.Fatalf("files = %+v", r.Files)
	}
	cmds := commandsOf(t, r)
	// python3 is a program, so its script argument stays an argument; the other
	// command's first word is a script relative to the hooks file.
	if strings.Join(cmds, "|") != "check.py|python3 ~/.codex/hooks/check.py --fast" {
		t.Errorf("commands = %q", cmds)
	}
	if !hasExactNote(r, Dropped, "statusMessage", "not imported: a key Belai has no field for") {
		t.Errorf("notes = %+v", r.Notes)
	}
	if !hasExactNote(r, Warning, "command", "python3 is not a bundle file; add it to hooks.allowed_programs on the host") {
		t.Errorf("notes = %+v", r.Notes)
	}
	if !hasExactNote(r, Dropped, "keys", "1 other top-level key(s) have no place in a hook and were not imported") {
		t.Errorf("notes = %+v", r.Notes)
	}
}

func TestPluginHooksFileDropsCommentKeys(t *testing.T) {
	h := newHookHost(t)
	file := h.put(t, "home", ".claude/hooks.json", `{
  "_comment": "Hooks for the plugin. Not a secret.",
  "hooks": {
    "_note": "also a comment",
    "PreToolUse": [{"matcher": "Bash", "hooks": [{"type": "command", "command": "vulnetix agent hook", "timeout": 30}]}]
  }
}`)
	r, err := ImportItem(file, KindHook, ClaudeCode, h.user(false))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(r.Doc), "_comment") || strings.Contains(string(r.Doc), "Not a secret") {
		t.Errorf("a comment reached the document: %s", r.Doc)
	}
	if !hasExactNote(r, Dropped, "_comment", "comment key dropped") || !hasExactNote(r, Dropped, "_note", "comment key dropped") {
		t.Errorf("notes = %+v", r.Notes)
	}
	if !hasExactNote(r, Warning, "command", "vulnetix is not a bundle file; add it to hooks.allowed_programs on the host") {
		t.Errorf("notes = %+v", r.Notes)
	}
	if len(r.Files) != 0 || r.SHA256 != libitem.Hash(r.Doc) {
		t.Errorf("a bundle with no files hashes as its document: %+v", r)
	}
}

func TestProjectScriptsResolveAgainstTheRepository(t *testing.T) {
	h := newHookHost(t)
	h.put(t, "repo", ".claude/hooks/check.sh", "echo a\n")
	h.put(t, "repo", "scripts/check.sh", "echo b\n")
	h.put(t, "repo", ".claude/hooks/lint.sh", "echo c\n")
	file := h.put(t, "repo", ".claude/settings.json", `{"hooks": {"PreToolUse": [{"matcher": "Edit", "hooks": [
      {"type": "command", "command": "\"$CLAUDE_PROJECT_DIR\"/.claude/hooks/check.sh a"},
      {"type": "command", "command": "${CLAUDE_PROJECT_DIR}/scripts/check.sh b"},
      {"type": "command", "command": ".claude/hooks/lint.sh"},
      {"type": "command", "command": "./.claude/hooks/lint.sh again"}
    ]}]}}`)
	r, err := ImportItem(file, KindHook, ClaudeCode, h.project("claude-code-app"))
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(commandsOf(t, r), "|")
	if got != "check.sh a|check-2.sh b|lint.sh|lint.sh again" {
		t.Fatalf("commands = %s", got)
	}
	var names []string
	for _, f := range r.Files {
		names = append(names, f.Path)
	}
	if strings.Join(names, ",") != "check-2.sh,check.sh,lint.sh" {
		t.Errorf("files = %v (sorted by path, the same file carried once)", names)
	}
}

func TestAScriptOutsideTheRootsIsLeftAlone(t *testing.T) {
	h := newHookHost(t)
	outside := filepath.Join(t.TempDir(), "guard.sh")
	if err := os.WriteFile(outside, []byte("exit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	h.put(t, "home", "bin/mine.sh", "exit 0\n") // under home but not under ~/.claude
	file := h.put(t, "home", ".claude/settings.json", fmt.Sprintf(`{"hooks": {"Stop": [{"hooks": [
      {"type": "command", "command": "%s"},
      {"type": "command", "command": "~/bin/mine.sh"},
      {"type": "command", "command": "../../../../etc/passwd"},
      {"type": "command", "command": "~/.claude/hooks/missing.sh"},
      {"type": "command", "command": "$UNKNOWN/x.sh"}
    ]}]}}`, outside))
	r, err := ImportItem(file, KindHook, ClaudeCode, h.user(true))
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Files) != 0 {
		t.Fatalf("a script outside the roots was carried: %+v", r.Files)
	}
	cmds := commandsOf(t, r)
	if cmds[0] != outside || cmds[1] != "~/bin/mine.sh" {
		t.Errorf("commands = %q", cmds)
	}
	if !hasExactNote(r, Warning, "command", "~/bin/mine.sh is outside this harness's directories, so it is not carried") ||
		!hasExactNote(r, Warning, "command", "~/.claude/hooks/missing.sh was not found, so it is not carried") {
		t.Errorf("notes = %+v", r.Notes)
	}
	if !hasExactNote(r, Warning, "command", "../../../../etc/passwd is outside this harness's directories, so it is not carried") ||
		!hasExactNote(r, Warning, "command", "$UNKNOWN/x.sh is outside this harness's directories, so it is not carried") {
		t.Errorf("notes = %+v", r.Notes)
	}
}

func TestAScriptThatCannotBeCarriedMakesTheItemInvalid(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, h hookHost)
		want  string
	}{
		{"symlink", func(t *testing.T, h hookHost) {
			real := h.put(t, "home", "real.sh", "exit 0\n")
			if err := os.MkdirAll(filepath.Join(h.home, ".claude/hooks"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(real, filepath.Join(h.home, ".claude/hooks/guard.sh")); err != nil {
				t.Skip(err)
			}
		}, "symbolic link"},
		{"directory in the path is a symlink", func(t *testing.T, h hookHost) {
			h.put(t, "home", "elsewhere/guard.sh", "exit 0\n")
			if err := os.MkdirAll(filepath.Join(h.home, ".claude"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(h.home, "elsewhere"), filepath.Join(h.home, ".claude/hooks")); err != nil {
				t.Skip(err)
			}
		}, "symbolic link"},
		{"oversized", func(t *testing.T, h hookHost) {
			h.put(t, "home", ".claude/hooks/guard.sh", strings.Repeat("a", 256<<10+1))
		}, "over 262144 bytes"},
		{"empty", func(t *testing.T, h hookHost) { h.put(t, "home", ".claude/hooks/guard.sh", "") }, "empty"},
		{"binary", func(t *testing.T, h hookHost) { h.put(t, "home", ".claude/hooks/guard.sh", "ab\x00cd") }, "not bounded text"},
		{"a secret", func(t *testing.T, h hookHost) {
			h.put(t, "home", ".claude/hooks/guard.sh", "#!/bin/sh\nexport K=AKIAIOSFODNN7EXAMPLE\n")
		}, "looks like it holds a secret"},
		{"a control character", func(t *testing.T, h hookHost) {
			h.put(t, "home", ".claude/hooks/guard.sh", "echo \x1b[31mred\n")
		}, "control"},
		{"a directory", func(t *testing.T, h hookHost) {
			if err := os.MkdirAll(filepath.Join(h.home, ".claude/hooks/guard.sh"), 0o755); err != nil {
				t.Fatal(err)
			}
		}, "not a regular file"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHookHost(t)
			c.setup(t, h)
			file := h.put(t, "home", ".claude/settings.json", `{"hooks": {"Stop": [{"hooks": [{"type": "command", "command": "~/.claude/hooks/guard.sh"}]}]}}`)
			_, err := ImportItem(file, KindHook, ClaudeCode, h.user(true))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want one naming %q", err, c.want)
			}
			if errors.Is(err, ErrSkipped) {
				t.Error("an invalid hook is not a skipped path")
			}
			if strings.Contains(err.Error(), "AKIAIOSFODNN7EXAMPLE") {
				t.Error("the reason carries the secret")
			}
		})
	}
}

func TestMoreThanThirtyTwoScriptsIsInvalid(t *testing.T) {
	h := newHookHost(t)
	var groups []string
	for g := 0; g < 5; g++ {
		var hs []string
		for i := 0; i < 7; i++ {
			n := g*7 + i
			if n >= 33 {
				break
			}
			h.put(t, "home", fmt.Sprintf(".claude/hooks/s%02d.sh", n), fmt.Sprintf("echo %d\n", n))
			hs = append(hs, fmt.Sprintf(`{"type":"command","command":"~/.claude/hooks/s%02d.sh"}`, n))
		}
		groups = append(groups, fmt.Sprintf(`{"hooks":[%s]}`, strings.Join(hs, ",")))
	}
	body := func(groups []string) string {
		return `{"hooks":{"PreToolUse":[` + strings.Join(groups, ",") + `]}}`
	}
	file := h.put(t, "home", ".claude/settings.json", body(groups))
	_, err := ImportItem(file, KindHook, ClaudeCode, h.user(true))
	if err == nil || !strings.Contains(err.Error(), "more than 32 scripts") {
		t.Fatalf("33 scripts: %v", err)
	}
	// Thirty-two is the limit and is accepted.
	h.put(t, "home", ".claude/settings.json", body(groups[:4])) // 28 scripts
	if r, err := ImportItem(file, KindHook, ClaudeCode, h.user(true)); err != nil || len(r.Files) != 28 {
		t.Fatalf("28 scripts: %v %d", err, len(r.Files))
	}
}

func TestScriptsOverTwoMiBInAllAreInvalid(t *testing.T) {
	h := newHookHost(t)
	var hs []string
	for i := 0; i < 9; i++ {
		h.put(t, "home", fmt.Sprintf(".claude/hooks/big%d.sh", i), strings.Repeat("a", 250<<10)+"\n")
		hs = append(hs, fmt.Sprintf(`{"type":"command","command":"~/.claude/hooks/big%d.sh"}`, i))
	}
	file := h.put(t, "home", ".claude/settings.json", `{"hooks":{"Stop":[{"hooks":[`+strings.Join(hs[:8], ",")+`]}],"PostToolUse":[{"hooks":[`+hs[8]+`]}]}}`)
	_, err := ImportItem(file, KindHook, ClaudeCode, h.user(true))
	if err == nil || !strings.Contains(err.Error(), "over 2097152 bytes in all") {
		t.Fatalf("err = %v", err)
	}
}

func TestInvalidHookStructureIsRefusedWithAReason(t *testing.T) {
	long := strings.Repeat("a", 513)
	groups9 := strings.TrimSuffix(strings.Repeat(`{"hooks":[{"type":"command","command":"x"}]},`, 9), ",")
	handlers9 := strings.TrimSuffix(strings.Repeat(`{"type":"command","command":"x"},`, 9), ",")
	cases := []struct{ name, hooks, want string }{
		{"unknown event", `{"PermissionRequest":[{"hooks":[{"type":"command","command":"x"}]}]}`, "not an event a hook can name"},
		{"http handler", `{"Stop":[{"hooks":[{"type":"http","url":"https://x.test"}]}]}`, `"http" handler`},
		{"prompt handler", `{"Stop":[{"hooks":[{"type":"prompt","prompt":"x"}]}]}`, `"prompt" handler`},
		{"no type", `{"Stop":[{"hooks":[{"command":"x"}]}]}`, "type must be a string"},
		{"long command", `{"Stop":[{"hooks":[{"type":"command","command":"` + long + `"}]}]}`, "the most is 512"},
		{"multi-line command", `{"Stop":[{"hooks":[{"type":"command","command":"a\nb"}]}]}`, "more than one line"},
		{"matcher on Stop", `{"Stop":[{"matcher":"Bash","hooks":[{"type":"command","command":"x"}]}]}`, "takes no matcher"},
		{"matcher on UserPromptSubmit", `{"UserPromptSubmit":[{"matcher":"x","hooks":[{"type":"command","command":"x"}]}]}`, "takes no matcher"},
		{"matcher on SubagentStop", `{"SubagentStop":[{"matcher":"x","hooks":[{"type":"command","command":"x"}]}]}`, "takes no matcher"},
		{"nine groups", `{"PreToolUse":[` + groups9 + `]}`, "has 9 groups"},
		{"nine handlers", `{"PreToolUse":[{"hooks":[` + handlers9 + `]}]}`, "has 9 handlers"},
		{"empty command", `{"Stop":[{"hooks":[{"type":"command","command":"  "}]}]}`, "command is empty"},
		{"no events", `{}`, "names no event"},
		{"bad timeout", `{"Stop":[{"hooks":[{"type":"command","command":"x","timeout":900}]}]}`, "timeout"},
		{"events not an object", `[]`, "must be an object of events"},
		{"event not a list", `{"Stop":{}}`, "must be a list of at least one group"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHookHost(t)
			file := h.put(t, "home", ".claude/settings.json", fmt.Sprintf(`{"env":{"K":%q},"hooks":%s}`, canary, c.hooks))
			_, err := ImportItem(file, KindHook, ClaudeCode, h.user(true))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want one naming %q", err, c.want)
			}
			if errors.Is(err, ErrSkipped) || strings.Contains(err.Error(), canary) {
				t.Errorf("err = %v", err)
			}
		})
	}
	// A file with no hooks at all.
	h := newHookHost(t)
	file := h.put(t, "home", ".claude/settings.json", `{"env":{}}`)
	if _, err := ImportItem(file, KindHook, ClaudeCode, h.user(true)); err == nil || !strings.Contains(err.Error(), "no hooks key") {
		t.Errorf("no hooks: %v", err)
	}
	file = h.put(t, "home", ".claude/other.json", `not json`)
	if _, err := ImportItem(file, KindHook, ClaudeCode, h.user(false)); err == nil || !strings.Contains(err.Error(), "not a JSON object") {
		t.Errorf("not json: %v", err)
	}
}

func TestASecretInACommandMakesTheItemInvalidWithoutShowingIt(t *testing.T) {
	h := newHookHost(t)
	token := "ghp_" + strings.Repeat("a1B2c3", 7)
	file := h.put(t, "home", ".claude/settings.json", fmt.Sprintf(`{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"notify --token %s"}]}]}}`, token))
	_, err := ImportItem(file, KindHook, ClaudeCode, h.user(true))
	if err == nil || !strings.Contains(err.Error(), "looks like it holds a secret") || strings.Contains(err.Error(), token) {
		t.Fatalf("err = %v", err)
	}
}

func TestOnlyTheHooksKeyIsEverRead(t *testing.T) {
	h := newHookHost(t)
	// Every other key is malformed, huge or secret. None may matter or show.
	file := h.put(t, "home", ".claude/settings.json", fmt.Sprintf(`{
  "env": {"GITHUB_TOKEN": "ghp_%s"},
  "mcpServers": {"x": {"headers": {"Authorization": "Bearer %s"}}},
  "apiKeyHelper": "echo %s",
  "hooks": {"Stop": [{"hooks": [{"type": "command", "command": "done-script"}]}]}
}`, strings.Repeat("z", 40), canary, canary))
	r, err := ImportItem(file, KindHook, ClaudeCode, h.user(true))
	if err != nil {
		t.Fatalf("a secret elsewhere in settings must not matter: %v", err)
	}
	if text := allText(r); strings.Contains(text, canary) || strings.Contains(text, "ghp_") || strings.Contains(text, "mcpServers") || strings.Contains(text, "env") {
		t.Errorf("another key reached the result: %s", text)
	}
	for _, n := range r.Notes {
		if n.Kind == Dropped {
			t.Errorf("a settings key was reported: %+v", n)
		}
	}
}

func TestHookFormatsAndPaths(t *testing.T) {
	h := newHookHost(t)
	file := h.put(t, "home", ".claude/settings.json", `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"x"}]}]}}`)
	for _, f := range []Format{Cursor, Windsurf, GenericMD, Cline} {
		if _, err := ImportItem(file, KindHook, f, h.user(true)); err == nil || !strings.Contains(err.Error(), "not read as") {
			t.Errorf("%s: %v", f, err)
		}
	}
	// A link is skipped, never read.
	link := filepath.Join(h.home, ".claude", "linked.json")
	if err := os.Symlink(file, link); err != nil {
		t.Skip(err)
	}
	if _, err := ImportItem(link, KindHook, ClaudeCode, h.user(false)); !errors.Is(err, ErrSkipped) {
		t.Errorf("link: %v", err)
	}
	// With no roots no script is carried and the item is still a valid hook.
	h.put(t, "home", ".claude/hooks/guard.sh", "exit 0\n")
	file = h.put(t, "home", ".claude/settings.json", `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"~/.claude/hooks/guard.sh"}]}]}}`)
	r, err := ImportItem(file, KindHook, ClaudeCode, Options{Name: "x"})
	if err != nil || len(r.Files) != 0 {
		t.Fatalf("%v %+v", err, r.Files)
	}
	k, ok := ParseKind("hook")
	if !ok || k != KindHook {
		t.Error("hook is not an item kind")
	}
	if lk, ok := KindHook.Library(); !ok || lk != libitem.Hook || KindHook.Markdown() {
		t.Error("hook maps to the library hook kind and is JSON")
	}
}

func TestArgumentsAreKeptAndShellSyntaxIsWarnedOnce(t *testing.T) {
	h := newHookHost(t)
	h.put(t, "home", ".claude/hooks/guard.sh", "exit 0\n")
	file := h.put(t, "home", ".claude/settings.json", `{"hooks":{"Stop":[{"hooks":[
      {"type":"command","command":"~/.claude/hooks/guard.sh --out \"$HOME/x\" 'a b'"},
      {"type":"command","command":"FOO=bar ./run.sh"},
      {"type":"command","command":"cd /tmp && ./run.sh"}
    ]}]}}`)
	r, err := ImportItem(file, KindHook, ClaudeCode, h.user(true))
	if err != nil {
		t.Fatal(err)
	}
	cmds := commandsOf(t, r)
	if cmds[0] != `guard.sh --out "$HOME/x" 'a b'` || cmds[1] != "FOO=bar ./run.sh" || cmds[2] != "cd /tmp && ./run.sh" {
		t.Errorf("commands = %q", cmds)
	}
	if !hasExactNote(r, Warning, "command", "~/.claude/hooks/guard.sh: the arguments use shell syntax, which a bundle cannot run") ||
		!hasExactNote(r, Warning, "command", "a command starts with an environment assignment, which a bundle cannot run") ||
		!hasExactNote(r, Warning, "command", "cd is not a bundle file; add it to hooks.allowed_programs on the host") {
		t.Errorf("notes = %+v", r.Notes)
	}
	for _, n := range r.Notes {
		if strings.Contains(n.Text, "FOO") || strings.Contains(n.Text, "bar") {
			t.Errorf("an assignment is shown: %q", n.Text)
		}
	}
}

func TestBelaiHookBundle(t *testing.T) {
	dir := t.TempDir()
	bundle := filepath.Join(dir, "pix")
	write(t, bundle, map[string]string{
		"hooks.json": `{"name":"pix","description":"Guards","hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"guard.sh","timeout":30}]}]}}`,
		"guard.sh":   "exit 0\n",
		"lib/x.sh":   "exit 1\n",
	})
	r, err := ImportItem(filepath.Join(bundle, "hooks.json"), KindHook, Belai, Options{Name: "pix"})
	if err != nil {
		t.Fatal(err)
	}
	if r.Name != "pix" || r.Description != "Guards" || len(r.Files) != 2 || r.Files[0].Path != "guard.sh" || r.Files[1].Path != "lib/x.sh" {
		t.Fatalf("%+v", r)
	}
	bf := []libitem.BundleFile{{Path: "guard.sh", SHA256: r.Files[0].SHA256}, {Path: "lib/x.sh", SHA256: r.Files[1].SHA256}}
	if r.SHA256 != libitem.HashBundle(r.Doc, bf) {
		t.Error("hash is not the bundle hash")
	}
	// The name must be the directory's.
	wrong := filepath.Join(dir, "other")
	write(t, wrong, map[string]string{"hooks.json": `{"name":"pix","hooks":{"Stop":[{"hooks":[{"type":"command","command":"x"}]}]}}`})
	if _, err := ImportItem(filepath.Join(wrong, "hooks.json"), KindHook, Belai, Options{}); err == nil || !strings.Contains(err.Error(), "directory is named other") {
		t.Errorf("name mismatch: %v", err)
	}
	// A secret in a bundle file is refused.
	write(t, bundle, map[string]string{"guard.sh": "K=AKIAIOSFODNN7EXAMPLE\n"})
	if _, err := ImportItem(filepath.Join(bundle, "hooks.json"), KindHook, Belai, Options{}); err == nil || !strings.Contains(err.Error(), "looks like it holds a secret") {
		t.Errorf("secret: %v", err)
	}
}

func TestListHooks(t *testing.T) {
	h := newHookHost(t)
	cases := []struct {
		name, body string
		settings   bool
		want       bool
	}{
		{"hooks present", `{"hooks":{"Stop":[]}}`, true, true},
		{"no hooks", `{"env":{}}`, true, false},
		{"empty hooks", `{"hooks":{}}`, true, false},
		{"settings not json", `nope`, true, false},
		{"hooks file not json", `nope`, false, true},
	}
	for _, c := range cases {
		p := h.put(t, "home", ".claude/"+strings.ReplaceAll(c.name, " ", "-")+".json", c.body)
		got, err := ListHooks(p, c.settings)
		if err != nil || got != c.want {
			t.Errorf("%s: %v %v", c.name, got, err)
		}
	}
}
