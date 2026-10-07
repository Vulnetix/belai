package libscan

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/agentimport"
	"github.com/vulnetix/belai/internal/harness"
	"github.com/vulnetix/belai/internal/libitem"
)

const hookCanary = "CANARY-settings-secret-91c2"

func hookFixture() []harness.Harness {
	return []harness.Harness{
		{ID: "claude-code", Name: "Claude Code", Detect: []string{"~/.claude"}, Format: "claude-code", Dirs: map[string]harness.KindDirs{
			"hook": {User: []string{"~/.claude/settings.json"}, Project: []string{".claude/settings.json", ".claude/settings.local.json"}, Shape: harness.JSONKey, Key: "hooks"},
		}},
		{ID: "codex", Name: "OpenAI Codex", Detect: []string{"~/.codex"}, Format: "codex", Dirs: map[string]harness.KindDirs{
			"hook": {User: []string{"~/.codex/hooks.json"}, Project: []string{".codex/hooks.json"}, Shape: harness.JSONFile},
		}},
		{ID: "cursor", Name: "Cursor", Detect: []string{"~/.cursor"}, Format: "cursor", Dirs: map[string]harness.KindDirs{
			"hook": {Project: []string{".cursor/hooks.json"}, Shape: harness.Unsupported},
		}},
	}
}

type hookEnv struct {
	env
}

func hookSetup(t *testing.T) hookEnv {
	t.Helper()
	e := hookEnv{env{home: t.TempDir(), belai: t.TempDir(), repo: t.TempDir(), empty: t.TempDir()}}
	t.Setenv("BELAI_HOME", e.belai)
	t.Setenv("VULNETIX_WEB_URL", "http://127.0.0.1:1")
	oldH, oldR := Harnesses, TrustedRepos
	Harnesses = hookFixture
	TrustedRepos = func() []Repo { return nil }
	t.Cleanup(func() { Harnesses, TrustedRepos = oldH, oldR })

	put(t, e.home, ".claude/hooks/guard.sh", "#!/bin/sh\nexit 0\n")
	put(t, e.home, ".claude/settings.json", `{
  "env": {"ANTHROPIC_API_KEY": "`+hookCanary+`"},
  "apiKeyHelper": "echo `+hookCanary+`",
  "hooks": {
    "PreToolUse": [{"matcher": "Bash", "hooks": [
      {"type": "command", "command": "~/.claude/hooks/guard.sh --token=abc123 --mode strict", "timeout": 30},
      {"type": "command", "command": "curl -s https://hooks.example.test/x?key=hunter2"}
    ]}]
  }
}`)
	put(t, e.home, ".cursor/hooks.json", `{"version":1,"hooks":{}}`)
	put(t, e.home, ".codex/hooks.json", `{"description":"Codex checks","hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"echo-start"}]}]}}`)

	put(t, e.repo, ".claude/hooks/lint.sh", "echo lint\n")
	put(t, e.repo, ".claude/settings.json", `{"hooks":{"PostToolUse":[{"matcher":"Edit","hooks":[{"type":"command","command":"\"$CLAUDE_PROJECT_DIR\"/.claude/hooks/lint.sh"}]}]}}`)
	put(t, e.repo, ".claude/settings.local.json", `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"say-done"}]}]}}`)
	put(t, e.repo, ".codex/hooks.json", `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"codex-done"}]}]}}`)
	// A settings file with no hooks is not an item.
	put(t, e.empty, ".claude/settings.json", `{"env":{"X":"`+hookCanary+`"}}`)
	return e
}

func (e hookEnv) scan(t *testing.T, o Options) Report {
	t.Helper()
	o.Home = e.home
	if o.Repos == nil && !o.NoRepos {
		o.Repos = []Repo{{Path: e.repo, Name: "My App"}, {Path: e.empty, Name: "empty"}}
	}
	return Scan(context.Background(), o)
}

func TestScanFindsHooksPerFileAndBlock(t *testing.T) {
	e := hookSetup(t)
	r := e.scan(t, Options{})

	u := find(r, "hook", "claude-code-user")
	if u == nil {
		t.Fatalf("items = %+v", r.Items)
	}
	if u.Path != filepath.Join(e.home, ".claude/settings.json")+"#hooks" || u.Format != "claude-code" || u.Harness != "claude-code" ||
		u.Scope != ScopeUser || u.Verdict != Warning || !u.Converted {
		t.Errorf("user hook = %+v", u)
	}
	if len(u.Files) != 1 || u.Files[0] != (ItemFile{Path: "guard.sh", Bytes: len("#!/bin/sh\nexit 0\n"), SHA256: u.Files[0].SHA256}) || len(u.Files[0].SHA256) != 64 {
		t.Errorf("files = %+v", u.Files)
	}
	if len(u.SHA256) != 64 || u.SHA256 == u.Files[0].SHA256 {
		t.Errorf("sha256 = %q", u.SHA256)
	}
	if c := find(r, "hook", "codex-user"); c == nil || c.Path != filepath.Join(e.home, ".codex/hooks.json") || c.Format != "codex" || c.Description != "Codex checks" || len(c.Files) != 0 {
		t.Errorf("codex = %+v", c)
	}
	p := find(r, "hook", "claude-code-my-app")
	if p == nil || p.Scope != ScopeProject || p.Repo != e.repo || p.Path != filepath.Join(e.repo, ".claude/settings.json")+"#hooks" ||
		len(p.Files) != 1 || p.Files[0].Path != "lint.sh" || p.Verdict != Valid {
		t.Errorf("project = %+v", p)
	}
	if l := find(r, "hook", "claude-code-my-app-local"); l == nil || l.Path != filepath.Join(e.repo, ".claude/settings.local.json")+"#hooks" {
		t.Errorf("local = %+v", l)
	}
	if c := find(r, "hook", "codex-my-app"); c == nil || c.Path != filepath.Join(e.repo, ".codex/hooks.json") {
		t.Errorf("project codex = %+v", c)
	}
	n := 0
	for _, it := range r.Items {
		if it.Kind == "hook" {
			n++
			if !libitem.ValidName(libitem.Hook, it.Name) {
				t.Errorf("name %q breaks the library name rule", it.Name)
			}
		}
	}
	if n != 5 {
		t.Errorf("%d hook items, want 5 (the settings file with no hooks, and cursor's file, are not items)", n)
	}
	// An unsupported dialect is named on the harness and never read.
	byID := map[string]HarnessReport{}
	for _, h := range r.Harnesses {
		byID[h.ID] = h
	}
	if c := byID["cursor"]; len(c.Notes) != 1 || c.Notes[0] != "hook: unsupported format, not read" {
		t.Errorf("cursor = %+v", c)
	}
	// The repository with settings and no hooks reports nothing found.
	for _, rr := range r.Repos {
		if rr.Name == "empty" && len(rr.Found) != 0 {
			t.Errorf("empty = %+v", rr)
		}
	}
}

func TestHookReportHoldsNoCommandLineAndNoSettingsValue(t *testing.T) {
	e := hookSetup(t)
	r := e.scan(t, Options{})
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	raw := string(b)
	for _, leak := range []string{hookCanary, "abc123", "hunter2", "--mode strict", "hooks.example.test", "ANTHROPIC_API_KEY", "apiKeyHelper", "exit 0", "echo lint"} {
		if strings.Contains(raw, leak) {
			t.Errorf("the report holds %q", leak)
		}
	}
	// A note names the first word of a command and says what happened to it.
	u := find(r, "hook", "claude-code-user")
	var texts []string
	for _, n := range u.Notes {
		texts = append(texts, n.Kind+" "+n.Field+": "+n.Text)
	}
	joined := strings.Join(texts, "\n")
	if !strings.Contains(joined, "mapped command: ~/.claude/hooks/guard.sh carried as guard.sh") ||
		!strings.Contains(joined, "warning command: curl is not a bundle file; add it to hooks.allowed_programs on the host") {
		t.Errorf("notes = %s", joined)
	}
}

func TestHookKindFilter(t *testing.T) {
	e := hookSetup(t)
	set, err := KindSet("hook")
	if err != nil {
		t.Fatal(err)
	}
	r := e.scan(t, Options{Kinds: set})
	if len(r.Items) != 5 {
		t.Errorf("items = %d", len(r.Items))
	}
	for _, it := range r.Items {
		if it.Kind != "hook" {
			t.Errorf("a %s in a hook scan", it.Kind)
		}
	}
	// Another kind's scan never opens a settings file for hooks.
	other, _ := KindSet("command")
	for _, it := range e.scan(t, Options{Kinds: other}).Items {
		if it.Kind == "hook" {
			t.Error("a hook in a command scan")
		}
	}
}

func TestBelaiHookBundlesAreScanned(t *testing.T) {
	e := hookSetup(t)
	put(t, e.belai, "hooks/pix/hooks.json", `{"name":"pix","description":"Guards","hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"guard.sh","timeout":30}]}]}}`)
	put(t, e.belai, "hooks/pix/guard.sh", "exit 0\n")
	put(t, e.belai, "hooks/wrong/hooks.json", `{"name":"other","hooks":{"Stop":[{"hooks":[{"type":"command","command":"x"}]}]}}`)
	r := e.scan(t, Options{NoRepos: true})
	pix := find(r, "hook", "pix")
	if pix == nil || pix.Harness != BelaiID || pix.Format != "belai" || pix.Verdict != Valid || pix.Path != filepath.Join(e.belai, "hooks/pix/hooks.json") ||
		len(pix.Files) != 1 || pix.Files[0].Path != "guard.sh" || pix.Description != "Guards" {
		t.Errorf("pix = %+v", pix)
	}
	if w := find(r, "hook", "wrong"); w == nil || w.Verdict != Invalid || !strings.Contains(w.Reason, "directory is named wrong") {
		t.Errorf("wrong = %+v", w)
	}
}

func TestResolveHookPaths(t *testing.T) {
	e := hookSetup(t)
	roots := Roots(e.home, []Repo{{Path: e.repo, Name: "My App"}}, nil)
	settings := filepath.Join(e.home, ".claude/settings.json")

	res, err := Resolve(roots, agentimport.KindHook, settings+"#hooks")
	if err != nil || res.File != settings || res.Hint != "claude-code-user" || res.Root.Format != agentimport.ClaudeCode {
		t.Fatalf("resolve = %+v %v", res, err)
	}
	if o := ImportOptions(res); !o.Hook.Settings || o.Hook.Home != e.home || o.Name != "claude-code-user" || len(o.Hook.Roots) != 1 || o.Hook.Roots[0] != filepath.Join(e.home, ".claude") {
		t.Errorf("options = %+v", o)
	}
	res, err = Resolve(roots, agentimport.KindHook, filepath.Join(e.repo, ".claude/settings.local.json")+"#hooks")
	if err != nil || res.Hint != "claude-code-my-app-local" {
		t.Fatalf("local = %+v %v", res, err)
	}
	if o := ImportOptions(res); o.Hook.RepoRoot != e.repo || len(o.Hook.Roots) != 1 || o.Hook.Roots[0] != e.repo {
		t.Errorf("project options = %+v", o)
	}
	if res, err = Resolve(roots, agentimport.KindHook, filepath.Join(e.home, ".codex/hooks.json")); err != nil || res.Hint != "codex-user" || ImportOptions(res).Hook.Settings {
		t.Fatalf("codex = %+v %v", res, err)
	}
	for name, p := range map[string]string{
		"settings without the key":    settings,
		"another key":                 settings + "#env",
		"a hooks file with a key":     filepath.Join(e.home, ".codex/hooks.json") + "#hooks",
		"a file that is not a root":   filepath.Join(e.home, ".claude/other.json") + "#hooks",
		"a traversal":                 e.home + "/.claude/../.claude/settings.json#hooks",
		"cursor's unsupported hooks":  filepath.Join(e.repo, ".cursor/hooks.json"),
		"a script, not a hooks file":  filepath.Join(e.home, ".claude/hooks/guard.sh"),
		"a command, asked for a hook": filepath.Join(e.home, ".claude/commands/x.md"),
	} {
		if _, err := Resolve(roots, agentimport.KindHook, p); err == nil {
			t.Errorf("%s: resolved", name)
		}
	}
	// Another kind never resolves a hooks file.
	if _, err := Resolve(roots, agentimport.KindCommand, settings+"#hooks"); err == nil {
		t.Error("a settings file resolved as a command")
	}
	// A link in place of the file is refused.
	other := filepath.Join(e.home, ".claude/real.json")
	put(t, e.home, ".claude/real.json", `{"hooks":{}}`)
	os.Remove(settings)
	if err := os.Symlink(other, settings); err != nil {
		t.Skip(err)
	}
	if _, err := Resolve(roots, agentimport.KindHook, settings+"#hooks"); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Errorf("link: %v", err)
	}
}

func TestResolveBelaiHookBundle(t *testing.T) {
	e := hookSetup(t)
	put(t, e.belai, "hooks/pix/hooks.json", `{"name":"pix","hooks":{"Stop":[{"hooks":[{"type":"command","command":"x"}]}]}}`)
	roots := Roots(e.home, nil, nil)
	p := filepath.Join(e.belai, "hooks/pix/hooks.json")
	res, err := Resolve(roots, agentimport.KindHook, p)
	if err != nil || res.Hint != "pix" || res.Root.Format != agentimport.Belai {
		t.Fatalf("%+v %v", res, err)
	}
	for _, bad := range []string{
		filepath.Join(e.belai, "hooks/pix/guard.sh"),
		filepath.Join(e.belai, "hooks/.pix/hooks.json"),
		filepath.Join(e.belai, "hooks/pix/sub/hooks.json"),
		filepath.Join(e.belai, "hooks/hooks.json"),
	} {
		if _, err := Resolve(roots, agentimport.KindHook, bad); err == nil {
			t.Errorf("%s resolved", bad)
		}
	}
}

// What a scan reports is what an import re-derives: the same options, so the same hash.
func TestAnImportReproducesTheScannedHash(t *testing.T) {
	e := hookSetup(t)
	r := e.scan(t, Options{})
	roots := Roots(e.home, []Repo{{Path: e.repo, Name: "My App"}, {Path: e.empty, Name: "empty"}}, nil)
	for _, it := range r.Items {
		if it.Kind != "hook" || it.Verdict == Invalid {
			continue
		}
		res, err := Resolve(roots, agentimport.KindHook, it.Path)
		if err != nil {
			t.Fatalf("%s: %v", it.Path, err)
		}
		got, err := agentimport.ImportItem(res.File, agentimport.KindHook, res.Root.Format, ImportOptions(res))
		if err != nil {
			t.Fatalf("%s: %v", it.Name, err)
		}
		if got.SHA256 != it.SHA256 || got.Name != it.Name || len(got.Files) != len(it.Files) {
			t.Errorf("%s: import %s, scan %s", it.Name, got.SHA256, it.SHA256)
		}
	}
	// A script edited after the scan changes the hash, so the import refuses it.
	put(t, e.home, ".claude/hooks/guard.sh", "#!/bin/sh\nexit 1\n")
	res, _ := Resolve(roots, agentimport.KindHook, filepath.Join(e.home, ".claude/settings.json")+"#hooks")
	got, err := agentimport.ImportItem(res.File, agentimport.KindHook, res.Root.Format, ImportOptions(res))
	if err != nil {
		t.Fatal(err)
	}
	if want := find(r, "hook", "claude-code-user").SHA256; got.SHA256 == want {
		t.Error("the hash did not change with the script")
	}
}

func TestHooksFileThatIsALinkOrCarriesASymlinkedScript(t *testing.T) {
	e := hookSetup(t)
	real := filepath.Join(e.home, "real.sh")
	put(t, e.home, "real.sh", "exit 0\n")
	os.Remove(filepath.Join(e.home, ".claude/hooks/guard.sh"))
	if err := os.Symlink(real, filepath.Join(e.home, ".claude/hooks/guard.sh")); err != nil {
		t.Skip(err)
	}
	r := e.scan(t, Options{NoRepos: true})
	u := find(r, "hook", "claude-code-user")
	if u == nil || u.Verdict != Invalid || !strings.Contains(u.Reason, "symbolic link") || len(u.Files) != 0 || u.SHA256 != "" {
		t.Errorf("user = %+v", u)
	}
	// The hooks file itself a link: counted as skipped, not listed.
	os.Remove(filepath.Join(e.home, ".codex/hooks.json"))
	if err := os.Symlink(real, filepath.Join(e.home, ".codex/hooks.json")); err != nil {
		t.Skip(err)
	}
	r = e.scan(t, Options{NoRepos: true})
	if find(r, "hook", "codex-user") != nil || r.Skipped == 0 {
		t.Errorf("link: skipped %d, item %+v", r.Skipped, find(r, "hook", "codex-user"))
	}
}

func TestHookNames(t *testing.T) {
	long := strings.Repeat("Repo Name ", 12)
	for _, c := range []struct {
		r    Root
		want string
	}{
		{Root{Harness: "claude-code", Scope: ScopeUser}, "claude-code-user"},
		{Root{Harness: "codex", Scope: ScopeUser}, "codex-user"},
		{Root{Harness: "claude-code", Scope: ScopeProject, RepoName: "My App", Path: "/r/.claude/settings.json"}, "claude-code-my-app"},
		{Root{Harness: "claude-code", Scope: ScopeProject, RepoName: "app", Path: "/r/.claude/settings.local.json"}, "claude-code-app-local"},
		{Root{Harness: "codex", Scope: ScopeProject, Repo: "/work/Vuln_Netix", Path: "/r/.codex/hooks.json"}, "codex-vuln_netix"},
		{Root{Harness: "codex", Scope: ScopeProject, RepoName: "///", Path: "/r/.codex/hooks.json"}, "codex-repo"},
	} {
		if got := HookName(c.r); got != c.want {
			t.Errorf("HookName = %q, want %q", got, c.want)
		}
	}
	got := HookName(Root{Harness: "claude-code", Scope: ScopeProject, RepoName: long, Path: "/r/.claude/settings.local.json"})
	if len(got) > libitem.MaxNameBytes || !libitem.ValidName(libitem.Hook, got) || !strings.HasSuffix(got, "-local") {
		t.Errorf("long = %q (%d)", got, len(got))
	}
}
