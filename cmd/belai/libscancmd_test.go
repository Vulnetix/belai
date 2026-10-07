package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/harness"
	"github.com/vulnetix/belai/internal/libscan"
)

func TestLibraryScanKindHook(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("BELAI_HOME", t.TempDir())
	oldH, oldR := libscan.Harnesses, libscan.TrustedRepos
	libscan.Harnesses = func() []harness.Harness {
		return []harness.Harness{{ID: "claude-code", Name: "Claude Code", Detect: []string{"~/.claude"}, Format: "claude-code", Dirs: map[string]harness.KindDirs{
			"hook":    {User: []string{"~/.claude/settings.json"}, Shape: harness.JSONKey, Key: "hooks"},
			"command": {User: []string{"~/.claude/commands"}, Shape: harness.MDFile},
		}}}
	}
	libscan.TrustedRepos = func() []libscan.Repo { return nil }
	t.Cleanup(func() { libscan.Harnesses, libscan.TrustedRepos = oldH, oldR })

	write := func(rel, body string) {
		p := filepath.Join(home, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(".claude/hooks/guard.sh", "exit 0\n")
	write(".claude/settings.json", `{"env":{"K":"top-secret-value"},"hooks":{"Stop":[{"hooks":[{"type":"command","command":"~/.claude/hooks/guard.sh --flag"}]}]}}`)
	write(".claude/commands/ship.md", "Ship it.\n")

	var out, errb bytes.Buffer
	if code := runLibraryScanCLI(context.Background(), []string{"scan", "-kind", "hook", "-json"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	var rep libscan.Report
	if err := json.Unmarshal(out.Bytes(), &rep); err != nil {
		t.Fatal(err)
	}
	if len(rep.Items) != 1 || rep.Items[0].Kind != "hook" || rep.Items[0].Name != "claude-code-user" || len(rep.Items[0].Files) != 1 {
		t.Fatalf("items = %+v", rep.Items)
	}
	for _, leak := range []string{"top-secret-value", "--flag"} {
		if strings.Contains(out.String(), leak) {
			t.Errorf("the report holds %q", leak)
		}
	}
	if !strings.Contains(out.String(), `"files"`) || !strings.Contains(out.String(), `"path": "guard.sh"`) {
		t.Errorf("no files in the JSON: %s", out.String())
	}

	out.Reset()
	if code := runLibraryScanCLI(context.Background(), []string{"scan", "-kind", "hook"}, &out, &errb); code != 0 || !strings.Contains(out.String(), "claude-code-user") {
		t.Fatalf("text: %d %s", code, out.String())
	}
	errb.Reset()
	if code := runLibraryScanCLI(context.Background(), []string{"scan", "-kind", "hooks"}, &out, &errb); code != 2 || !strings.Contains(errb.String(), "hook") {
		t.Errorf("a wrong kind lists the kinds: %d %s", code, errb.String())
	}
}
