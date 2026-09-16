package tools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestNativeToolsAreReadOnly pins the catalogue invariant: every native tool
// (local and cloud) is read-only by construction, so the read-only master
// switch can never strip one and the concurrent read-only fan-out can run it.
func TestNativeToolsAreReadOnly(t *testing.T) {
	var commands []nativeCommand
	commands = append(commands, localCatalog()...)
	commands = append(commands, cloudCatalog()...)
	if len(commands) == 0 {
		t.Fatal("catalogue is empty")
	}
	for _, c := range commands {
		n := &Native{Root: t.TempDir(), cmd: c}
		if n.Kind() != KindNative {
			t.Fatalf("%s kind = %q, want %q", c.name, n.Kind(), KindNative)
		}
		if !n.Kind().ReadOnly() {
			t.Fatalf("%s must be read-only", c.name)
		}
		if Mutates(n) {
			t.Fatalf("%s must not mutate", c.name)
		}
		if n.Definition().Name != c.name {
			t.Fatalf("%s definition name = %q", c.name, n.Definition().Name)
		}
	}
}

// TestNativePathEscapesRootRejected pins the confinement rule: a path that
// resolves outside Root must be rejected before the command runs.
func TestNativePathEscapesRootRejected(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	_ = os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0o600)

	cat := &Native{Root: root, cmd: nativeCommand{
		name: "Cat",
		build: func(root string, args map[string]any) ([]string, string, error) {
			p, err := nativePath(root, argPath(args))
			return []string{p}, "", err
		},
	}}
	if _, err := cat.Execute(context.Background(), map[string]any{"path": "../" + filepath.Base(outside) + "/secret.txt"}); err == nil {
		t.Fatal("expected path escape to be rejected")
	}
}

// TestNativeOutputCapped pins the 64 KiB (configurable) output cap.
func TestNativeOutputCapped(t *testing.T) {
	if _, err := exec.LookPath("cat"); err != nil {
		t.Skip("cat not available")
	}
	root := t.TempDir()
	big := strings.Repeat("x", 2000)
	_ = os.WriteFile(filepath.Join(root, "big.txt"), []byte(big), 0o600)

	cat := &Native{Root: root, MaxBytes: 32, cmd: nativeCommand{
		name: "Cat",
		build: func(root string, args map[string]any) ([]string, string, error) {
			p, err := nativePath(root, argPath(args))
			return []string{p}, "", err
		},
	}}
	res, err := cat.Execute(context.Background(), map[string]any{"path": "big.txt"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(res.Content, "truncated at 32 bytes") {
		t.Fatalf("expected truncation notice, got %q", res.Content)
	}
}

// TestNativeRejectsMutation pins the fixed-shape rule: a Git tool refuses a
// mutating subcommand and a cloud tool refuses anything outside its read-only
// prefix allowlist.
func TestNativeRejectsMutation(t *testing.T) {
	git := &Native{Root: t.TempDir(), cmd: gitTool()}
	for _, cmd := range []string{"commit -m x", "push", "checkout -b x", "merge x"} {
		if _, err := git.Execute(context.Background(), map[string]any{"command": cmd}); err == nil {
			t.Fatalf("Git(%q) should be rejected", cmd)
		}
	}
	if _, err := git.Execute(context.Background(), map[string]any{"command": "status"}); err != nil {
		// "status" is allowed; it can only fail at exec when git is absent or
		// the working tree is unusual — never with a read-only rejection.
		if strings.Contains(err.Error(), "not read-only") {
			t.Fatalf("Git(status) rejected as non-read-only: %v", err)
		}
	}

	gh := &Native{Root: t.TempDir(), cmd: cloudNative(cloudSpecs[0])}
	if _, err := gh.Execute(context.Background(), map[string]any{"command": "pr merge"}); err == nil {
		t.Fatal("GH(pr merge) should be rejected")
	}
	if _, err := gh.Execute(context.Background(), map[string]any{"command": "pr list"}); err != nil && !strings.Contains(err.Error(), "not in the read-only") {
		t.Fatalf("GH(pr list) should only fail at exec, got %v", err)
	}
}

// TestJQTransformsStdin pins the stdin-backed transform path end to end.
func TestJQTransformsStdin(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not available")
	}
	jq := &Native{Root: t.TempDir(), cmd: nativeCommand{
		name: "JQ",
		build: func(root string, args map[string]any) ([]string, string, error) {
			f, _ := argString(args, "filter")
			return []string{"-r", f}, `{"a": 1, "b": 2}`, nil
		},
	}}
	res, err := jq.Execute(context.Background(), map[string]any{"filter": ".a"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if strings.TrimSpace(res.Content) != "1" {
		t.Fatalf("jq output = %q, want 1", res.Content)
	}
}

// TestNativeToolsBuiltFromCaps pins the wiring: only detected tools appear,
// and the read-only switch keeps native tools while stripping mutating ones.
func TestNativeToolsBuiltFromCaps(t *testing.T) {
	caps := Capabilities{local: map[string]bool{"Cat": true, "Head": true}, cloud: map[string]bool{"GH": true}}
	tools := NativeTools(t.TempDir(), caps)
	names := map[string]bool{}
	for _, tool := range tools {
		names[tool.Definition().Name] = true
	}
	for _, want := range []string{"Cat", "Head", "GH"} {
		if !names[want] {
			t.Fatalf("missing %s in %v", want, names)
		}
	}
	if names["JQ"] {
		t.Fatal("undetected JQ must be absent")
	}

	full := DefaultWithCaps(t.TempDir(), false, caps)
	ro := DefaultWithCaps(t.TempDir(), true, caps)
	if _, ok := ro.Find("Cat"); !ok {
		t.Fatal("read-only registry must keep native Cat")
	}
	if _, ok := ro.Find("Write"); ok {
		t.Fatal("read-only registry must strip Write")
	}
	if _, ok := full.Find("Write"); !ok {
		t.Fatal("full registry must keep Write")
	}
}

// TestCatalogueNamesIsHermetic pins that the catalogue name list never
// depends on what is installed.
func TestCatalogueNamesIsHermetic(t *testing.T) {
	a := CatalogueNames()
	b := CatalogueNames()
	if len(a) == 0 {
		t.Fatal("catalogue names empty")
	}
	if strings.Join(a, ",") != strings.Join(b, ",") {
		t.Fatal("catalogue names drifted between calls")
	}
	for _, name := range []string{"Cat", "Head", "Tail", "LS", "Find", "Git", "JQ", "YQ", "Sed", "Awk", "Sort", "GH", "AWS", "Kubectl"} {
		if !contains(a, name) {
			t.Fatalf("catalogue missing %q", name)
		}
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// TestNativeSubjectExtraction pins that permission subjects survive the
// native path (so a Deny rule on a tool name can still be written).
func TestNativeSubjectExtraction(t *testing.T) {
	cat := &Native{cmd: nativeCommand{name: "Cat", subject: pathSubject}}
	if cat.Subject(map[string]any{"path": "a/b.txt"}) != "a/b.txt" {
		t.Fatal("Cat subject should be its path")
	}
	jq := &Native{cmd: nativeCommand{name: "JQ", subject: func(args map[string]any) string { s, _ := argString(args, "filter"); return s }}}
	if jq.Subject(map[string]any{"filter": ".x"}) != ".x" {
		t.Fatal("JQ subject should be its filter")
	}
}
