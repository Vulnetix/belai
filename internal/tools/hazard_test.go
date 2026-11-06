package tools

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestProtectedClasses(t *testing.T) {
	home := "/home/u"
	cases := []struct {
		abs   string
		extra []string
		want  []string
	}{
		{"/repo/.git/config", nil, []string{HazardPathVCS}},
		{"/repo/.git/hooks/pre-commit", nil, []string{HazardPathVCS}},
		{"/repo/sub/.gitmodules", nil, []string{HazardPathVCS}},
		{"/repo/.gitignore", nil, nil},
		{"/repo/.github/workflows/ci.yml", nil, []string{HazardPathCI}},
		{"/repo/.github/CODEOWNERS", nil, nil},
		{"/repo/.gitlab-ci.yml", nil, []string{HazardPathCI}},
		{"/repo/package-lock.json", nil, []string{HazardPathLockfile}},
		{"/repo/web/go.sum", nil, []string{HazardPathLockfile}},
		{"/repo/go.mod", nil, nil},
		{"/repo/.npmrc", nil, []string{HazardPathCredential}},
		{"/repo/.env", nil, []string{HazardPathCredential}},
		{"/repo/.env.production", nil, []string{HazardPathCredential}},
		{"/repo/.env.example", nil, nil},
		{"/repo/deploy/server.pem", nil, []string{HazardPathCredential}},
		{"/repo/internal/credentials.go", nil, nil},
		{"/repo/.belai/settings.json", nil, []string{HazardPathState}},
		{"/repo/.vulnetix/belai/kanban/x", nil, []string{HazardPathState}},
		{"/repo/.vulnetix/memory.yaml", nil, nil},
		{"/home/u/.ssh/authorized_keys", nil, []string{HazardPathCredential}},
		{"/home/u/.aws/credentials", nil, []string{HazardPathCredential}},
		{"/home/u/.zshrc", nil, []string{HazardPathShell}},
		{"/home/u/proj/.zshrc", nil, nil},
		{"/home/u/.config/fish/config.fish", nil, []string{HazardPathShell}},
		{"/repo/docs/private/a.md", []string{"docs/private"}, []string{HazardPathUser}},
		{"/repo/docs/public/a.md", []string{"docs/private"}, nil},
		{"/repo/secrets/a.txt", []string{"secrets"}, []string{HazardPathUser}},
		{"/repo/a/b.tfstate", []string{"*.tfstate"}, []string{HazardPathUser}},
		{"/data/keep/x", []string{"/data/keep"}, []string{HazardPathUser}},
		{"/data/keeper/x", []string{"/data/keep"}, nil},
		{"/home/u/notes/x", []string{"~/notes"}, []string{HazardPathUser}},
		{"/repo/.git/config", []string{".git"}, []string{HazardPathVCS, HazardPathUser}},
	}
	for _, tc := range cases {
		got := ProtectedClasses(tc.abs, tc.extra, home)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("ProtectedClasses(%q, %v) = %v, want %v", tc.abs, tc.extra, got, tc.want)
		}
	}
}

func TestProtectedRulesUseOnlyKnownClasses(t *testing.T) {
	known := map[string]bool{}
	for _, c := range HazardPathClasses {
		known[c] = true
	}
	for _, r := range ProtectedRules {
		if !known[r.Class] {
			t.Errorf("rule %+v names a class outside HazardPathClasses", r)
		}
		if r.Class == HazardPathUser {
			t.Errorf("rule %+v: the user class has no fixed rows", r)
		}
		switch r.Kind {
		case "segment", "name", "dir", "home", "credential":
		default:
			t.Errorf("rule %+v has an unknown kind", r)
		}
	}
}

func hazardRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func TestHazardCallBash(t *testing.T) {
	b := &Bash{Root: hazardRoot(t)}
	if got := HazardCall(b, map[string]any{"command": "rm -rf /"}, nil); !reflect.DeepEqual(got, []string{"exec.delete.catastrophic"}) {
		t.Fatalf("rm -rf / = %v", got)
	}
	if got := HazardCall(b, map[string]any{"command": "go test ./..."}, nil); got != nil {
		t.Fatalf("go test = %v", got)
	}
	if got := HazardCall(b, map[string]any{}, nil); got != nil {
		t.Fatalf("no command = %v", got)
	}
	ro := &Bash{Root: b.Root, ReadOnly: true}
	if got := HazardCall(ro, map[string]any{"command": "rm -rf /"}, nil); got != nil {
		t.Fatalf("read-only Bash cannot run it, so it has no hazard: %v", got)
	}
}

func TestHazardCallWriteEditPatch(t *testing.T) {
	root := hazardRoot(t)
	for _, d := range []string{".git/hooks", ".github/workflows", "src"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for f, c := range map[string]string{".git/hooks/pre-commit": "x\n", "src/a.go": "package a\n", "package-lock.json": "{}\n"} {
		if err := os.WriteFile(filepath.Join(root, f), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	w := &Write{Root: root}
	e := &Edit{Root: root}
	p := &ApplyPatch{Root: root}

	if got := HazardCall(w, map[string]any{"file_path": ".git/hooks/pre-commit", "content": "x"}, nil); !reflect.DeepEqual(got, []string{HazardPathVCS}) {
		t.Fatalf("write hook = %v", got)
	}
	if got := HazardCall(w, map[string]any{"file_path": ".github/workflows/new.yml", "content": "x"}, nil); !reflect.DeepEqual(got, []string{HazardPathCI}) {
		t.Fatalf("write workflow = %v", got)
	}
	if got := HazardCall(e, map[string]any{"file_path": "package-lock.json", "old_string": "{", "new_string": "{ "}, nil); !reflect.DeepEqual(got, []string{HazardPathLockfile}) {
		t.Fatalf("edit lockfile = %v", got)
	}
	if got := HazardCall(e, map[string]any{"file_path": "src/a.go", "old_string": "a", "new_string": "b"}, nil); got != nil {
		t.Fatalf("edit source = %v", got)
	}
	if got := HazardCall(w, map[string]any{"file_path": "src/a.go", "content": "x"}, []string{"src"}); !reflect.DeepEqual(got, []string{HazardPathUser}) {
		t.Fatalf("user path = %v", got)
	}
	// ../ and an absolute path outside the root do not resolve, so there is no
	// target and Execute refuses the call.
	if got := HazardCall(w, map[string]any{"file_path": "../outside/.git/config", "content": "x"}, nil); got != nil {
		t.Fatalf("escaping path = %v", got)
	}
	patch := "*** Begin Patch\n*** Update File: src/a.go\n@@\n-package a\n+package b\n*** Add File: .github/workflows/x.yml\n+on: push\n*** End Patch"
	if got := HazardCall(p, map[string]any{"input": patch}, nil); !reflect.DeepEqual(got, []string{HazardPathCI}) {
		t.Fatalf("patch = %v", got)
	}
	if got := HazardCall(p, map[string]any{"input": "not a patch"}, nil); got != nil {
		t.Fatalf("unparseable patch = %v", got)
	}
}

func TestHazardCallFollowsSymlinks(t *testing.T) {
	root := hazardRoot(t)
	if err := os.MkdirAll(filepath.Join(root, ".git", "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, ".git", "hooks"), filepath.Join(root, "innocent")); err != nil {
		t.Fatal(err)
	}
	w := &Write{Root: root}
	got := HazardCall(w, map[string]any{"file_path": "innocent/pre-commit", "content": "x"}, nil)
	if !reflect.DeepEqual(got, []string{HazardPathVCS}) {
		t.Fatalf("write through a symlink into .git = %v", got)
	}
}

func TestHazardCallOtherToolsHaveNone(t *testing.T) {
	if got := HazardCall(&Read{Root: hazardRoot(t)}, map[string]any{"file_path": ".git/config"}, nil); got != nil {
		t.Fatalf("Read = %v", got)
	}
}
