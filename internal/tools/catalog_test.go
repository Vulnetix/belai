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
	tools := NativeTools(t.TempDir(), caps, nil)
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

// catalogueCommand returns the named native command from the local catalogue,
// failing the test when it is absent.
func catalogueCommand(t *testing.T, name string) nativeCommand {
	t.Helper()
	for _, c := range localCatalog() {
		if c.name == name {
			return c
		}
	}
	t.Fatalf("no native command %q", name)
	return nativeCommand{}
}

// TestArgumentlessNativeToolsExecute pins that an empty argv is legal: Pwd,
// Env, and a bare Paste/Sort/Uniq invoke their binary with no arguments.
func TestArgumentlessNativeToolsExecute(t *testing.T) {
	cases := []struct {
		name   string
		binary string
		input  string
	}{
		{"Pwd", "pwd", ""},
		{"Env", "env", ""},
		{"Paste", "paste", "a\nb\n"},
		{"Sort", "sort", "c\na\nb\n"},
		{"Uniq", "uniq", "a\na\nb\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := exec.LookPath(tc.binary); err != nil {
				t.Skipf("%s not available", tc.binary)
			}
			n := &Native{Root: t.TempDir(), cmd: catalogueCommand(t, tc.name)}
			args := map[string]any{}
			if tc.input != "" {
				args["input"] = tc.input
			}
			res, err := n.Execute(context.Background(), args)
			if err != nil {
				t.Fatalf("%s Execute: %v", tc.name, err)
			}
			if strings.TrimSpace(res.Content) == "" {
				t.Fatalf("%s returned empty output", tc.name)
			}
		})
	}
}

// TestInputSourcePrefersPathWhenInputBlank pins the input:"" fix.
func TestInputSourcePrefersPathWhenInputBlank(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "d.json"), []byte(`{"a":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := inputSource(root, map[string]any{"input": "", "path": "d.json"})
	if err != nil {
		t.Fatalf("inputSource: %v", err)
	}
	if !strings.Contains(got, `{"a":1}`) {
		t.Fatalf("blank input shadowed path; got %q", got)
	}
	got, err = inputSource(root, map[string]any{"input": "direct", "path": "d.json"})
	if err != nil || got != "direct" {
		t.Fatalf("non-blank input must win; got %q, %v", got, err)
	}
}

// TestJQRejectsNonJSONInput pins early JSON validation before jq runs.
func TestJQRejectsNonJSONInput(t *testing.T) {
	n := &Native{Root: t.TempDir(), cmd: catalogueCommand(t, "JQ")}
	if _, err := n.Execute(context.Background(), map[string]any{"filter": ".a", "input": "not json"}); err == nil || !strings.Contains(err.Error(), "not valid JSON") {
		t.Fatalf("non-JSON input: got %v, want not-valid-JSON error", err)
	}
	if _, err := n.Execute(context.Background(), map[string]any{"filter": ".a"}); err == nil || !strings.Contains(err.Error(), "no input") {
		t.Fatalf("empty input: got %v, want no-input error", err)
	}
}
