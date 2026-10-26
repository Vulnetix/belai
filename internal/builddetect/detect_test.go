package builddetect

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func touch(t *testing.T, root string, names ...string) {
	t.Helper()
	for _, n := range names {
		p := filepath.Join(root, n)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("{}"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

func names(bs []Build) []string {
	var out []string
	for _, b := range bs {
		out = append(out, b.Name)
	}
	return out
}

func TestDetectFromMarkerFiles(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files []string
		want  []string
	}{
		{"go", []string{"go.mod"}, []string{"build-go", "build-go-vet"}},
		{"rust", []string{"Cargo.toml"}, []string{"build-cargo"}},
		{"dotnet", []string{"App.csproj"}, []string{"build-dotnet"}},
		{"gradle", []string{"gradlew", "build.gradle.kts"}, []string{"build-gradle"}},
		{"gradle wrapper alone", []string{"gradlew"}, nil},
		{"maven", []string{"mvnw", "pom.xml"}, []string{"build-maven"}},
		{"tsc without node_modules", []string{"tsconfig.json"}, nil},
		{"tsc installed", []string{"tsconfig.json", "node_modules/typescript/package.json", "node_modules/.bin/tsc"}, []string{"build-tsc"}},
		{"nothing", []string{"README.md"}, nil},
	} {
		root := t.TempDir()
		touch(t, root, tc.files...)
		if got := names(Detect(root)); !slices.Equal(got, tc.want) {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestNodeBuildScriptNeedsInstalledDependencies(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "package.json"), []byte(`{"scripts":{"build":"vite build"}}`), 0o644)
	if got := Detect(root); len(got) != 0 {
		t.Fatalf("a build script with no node_modules: %v", got)
	}
	os.Mkdir(filepath.Join(root, "node_modules"), 0o755)
	os.WriteFile(filepath.Join(root, "yarn.lock"), nil, 0o644)
	got := Detect(root)
	if len(got) != 1 || !slices.Equal(got[0].Command, []string{"yarn", "run", "build"}) {
		t.Fatalf("%+v", got)
	}
	os.WriteFile(filepath.Join(root, "package.json"), []byte(`{"scripts":{"test":"x"}}`), 0o644)
	if got := Detect(root); len(got) != 0 {
		t.Fatalf("no build script: %v", got)
	}
}

func TestDisplayHidesTheCheckoutPath(t *testing.T) {
	root := t.TempDir()
	touch(t, root, "gradlew", "settings.gradle")
	b := Detect(root)[0]
	if got := b.Display(root); got != "./gradlew --no-daemon -q classes" {
		t.Fatalf("%q", got)
	}
}

func TestASymlinkedWrapperIsNotRun(t *testing.T) {
	root := t.TempDir()
	touch(t, root, "pom.xml")
	if err := os.Symlink("/bin/sh", filepath.Join(root, "mvnw")); err != nil {
		t.Skip(err)
	}
	if got := Detect(root); len(got) != 0 {
		t.Fatalf("%v", got)
	}
}
