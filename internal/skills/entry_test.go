package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/posture"
)

func putSkill(t *testing.T, root, dir, doc string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, dir), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, dir, "SKILL.md"), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestDiscoverNamespacesAndShadows(t *testing.T) {
	user, plug := t.TempDir(), t.TempDir()
	putSkill(t, user, "release", "---\nname: release\ndescription: mine\n---\nbody")
	putSkill(t, plug, "release", "---\nname: release\ndescription: theirs\n---\nbody")
	putSkill(t, plug, "lint", "---\nname: lint\ndescription: lint it\n---\nbody")
	putSkill(t, plug, "bad", "no front matter")
	got := Discover([]Root{{Dir: user}, {Dir: plug, Namespace: "team"}}, posture.Defaults())
	var names []string
	for _, e := range got {
		names = append(names, e.Name+"="+e.Source)
	}
	if strings.Join(names, ",") != "release=user,team:lint=team,team:release=team" {
		t.Fatalf("names = %v", names)
	}
}

func TestReadBodyRevalidates(t *testing.T) {
	root := t.TempDir()
	putSkill(t, root, "x", "---\nname: x\ndescription: d\n---\n\nStep one.\n")
	e := Discover([]Root{{Dir: root}}, posture.Defaults())[0]
	body, err := ReadBody(e)
	if err != nil || body != "Step one.\n" {
		t.Fatalf("body = %q, err = %v", body, err)
	}
	putSkill(t, root, "x", "---\nname: x\nevil: yes\n---\nboo")
	if _, err := ReadBody(e); err == nil {
		t.Fatal("a skill that stopped validating still returned a body")
	}
}

func TestCompose(t *testing.T) {
	doc, err := Compose("cut-release", "Cut a release\nname: injected", "1. tag\n2. push")
	if err != nil {
		t.Fatal(err)
	}
	m, err := ValidateSkill(doc)
	if err != nil || m.Name != "cut-release" || m.Description != "Cut a release name: injected" {
		t.Fatalf("manifest = %+v, err = %v", m, err)
	}
	for _, bad := range []string{"", "Upper", "../x", "a b", strings.Repeat("a", 65)} {
		if _, err := Compose(bad, "d", "b"); err == nil {
			t.Errorf("name %q accepted", bad)
		}
	}
	if _, err := Compose("x", "d", "  "); err == nil {
		t.Error("empty body accepted")
	}
}
