package repomap

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func nestedTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for rel, body := range map[string]string{
		"AGENTS.md":                  "root",
		"a/AGENTS.md":                "a",
		"a/CLAUDE.md":                "a-claude",
		"a/b/c/AGENTS.md":            "abc",
		"a/b/other.txt":              "x",
		"node_modules/pkg/AGENTS.md": "dep",
		"a/b/c/d/e.go":               "package e",
		"empty/AGENTS.md":            "",
		".github/AGENTS.md":          "hidden dir is allowed",
		"vendor/lib/CLAUDE.md":       "vendored",
	} {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func names(fs []AgentsFile) string {
	var out []string
	for _, f := range fs {
		out = append(out, f.Name)
	}
	return strings.Join(out, ",")
}

func TestNestedAgentsFilesWalkOutermostFirstExcludingRoot(t *testing.T) {
	root := nestedTree(t)
	got := NestedAgentsFiles(root, "a/b/c/d")
	if want := "a/AGENTS.md,a/CLAUDE.md,a/b/c/AGENTS.md"; names(got) != want {
		t.Fatalf("got %s, want %s", names(got), want)
	}
	if got[0].Dir != "a" || got[2].Dir != "a/b/c" || got[0].Size != 1 {
		t.Errorf("Dir/Size wrong: %+v", got)
	}
	if got := NestedAgentsFiles(root, ".github"); names(got) != ".github/AGENTS.md" {
		t.Errorf(".github = %s", names(got))
	}
}

func TestNestedAgentsFilesNeverLeaveTheRoot(t *testing.T) {
	root := nestedTree(t)
	for _, rel := range []string{"", ".", "..", "../x", "a/../../x", filepath.Join(root, "a"), "a/\x00"} {
		if got := NestedAgentsFiles(root, rel); len(got) != 0 {
			t.Errorf("rel %q listed %s", rel, names(got))
		}
	}
	// a/../a is cleaned, not rejected.
	if got := NestedAgentsFiles(root, "a/../a"); names(got) != "a/AGENTS.md,a/CLAUDE.md" {
		t.Errorf("cleaned path = %s", names(got))
	}
}

func TestNestedAgentsFilesHonourSkipDirs(t *testing.T) {
	root := nestedTree(t)
	for _, rel := range []string{"node_modules/pkg", "vendor/lib"} {
		if got := NestedAgentsFiles(root, rel); len(got) != 0 {
			t.Errorf("%s listed %s", rel, names(got))
		}
	}
}

func TestNestedAgentsFilesRefuseSymlinks(t *testing.T) {
	root := nestedTree(t)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "AGENTS.md"), []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Skip("symlinks unavailable")
	}
	if got := NestedAgentsFiles(root, "link"); len(got) != 0 {
		t.Errorf("a linked directory listed %s", names(got))
	}
	// A directory link inside the tree stops the walk too, even to a target
	// inside the root.
	if err := os.Symlink(filepath.Join(root, "a"), filepath.Join(root, "alias")); err != nil {
		t.Fatal(err)
	}
	if got := NestedAgentsFiles(root, "alias/b"); len(got) != 0 {
		t.Errorf("a linked directory inside the tree listed %s", names(got))
	}
	// A linked instruction file is not a regular file.
	if err := os.Symlink(filepath.Join(outside, "AGENTS.md"), filepath.Join(root, "a/b/AGENTS.md")); err != nil {
		t.Fatal(err)
	}
	if got := NestedAgentsFiles(root, "a/b/c"); names(got) != "a/AGENTS.md,a/CLAUDE.md,a/b/c/AGENTS.md" {
		t.Errorf("a linked file was listed: %s", names(got))
	}
}

func TestEmptyInstructionFileIsListedWithSizeZero(t *testing.T) {
	root := nestedTree(t)
	got := NestedAgentsFiles(root, "empty")
	if len(got) != 1 || got[0].Size != 0 {
		t.Errorf("got %+v; presence is reported, the agent drops empties", got)
	}
}
