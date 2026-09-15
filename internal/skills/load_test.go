package skills

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/vulnetix/signet/internal/posture"
)

func TestLoadDirFindsAndValidatesSkills(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "review", "---\nname: review\ndescription: review diffs\n---\nbody")
	writeSkill(t, dir, "bad", "---\nname: bad\n---\nbody") // missing description
	writeSkill(t, dir, "notaskill", "not a skill")         // no front-matter

	manifests, err := LoadDir(dir, posture.Defaults())
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	if len(manifests) != 1 || manifests[0].Name != "review" {
		t.Fatalf("manifests = %+v, want only the valid review skill", manifests)
	}
}

func TestLoadDirMissingReturnsEmpty(t *testing.T) {
	manifests, err := LoadDir(filepath.Join(t.TempDir(), "nope"), posture.Defaults())
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	if len(manifests) != 0 {
		t.Fatalf("expected empty, got %+v", manifests)
	}
}

func writeSkill(t *testing.T, root, name, doc string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(doc), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
}
