package builddetect

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/docparity"
)

// Every build check the table can name is in the Checks table of
// docs/merge-conflicts.md, and its command too.
func TestEveryBuildCheckIsDocumented(t *testing.T) {
	doc := docparity.Read(t, "docs/merge-conflicts.md")
	root := t.TempDir()
	for _, n := range []string{"go.mod", "Cargo.toml", "App.csproj", "gradlew", "settings.gradle", "mvnw", "pom.xml", "tsconfig.json",
		"node_modules/typescript/package.json", "node_modules/.bin/tsc"} {
		p := filepath.Join(root, n)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte("{}"), 0o755)
	}
	os.WriteFile(filepath.Join(root, "package.json"), []byte(`{"scripts":{"build":"x"}}`), 0o644)
	got := Detect(root)
	if len(got) != 8 {
		t.Fatalf("%d checks detected; the fixture misses one: %+v", len(got), got)
	}
	for _, b := range got {
		if !strings.Contains(doc, "`"+b.Name+"`") {
			t.Errorf("docs/merge-conflicts.md does not name %s", b.Name)
		}
		cmd := strings.Replace(b.Display(root), "./node_modules", "node_modules", 1)
		if !strings.Contains(doc, cmd) {
			t.Errorf("docs/merge-conflicts.md does not give %s's command %q", b.Name, cmd)
		}
	}
}
