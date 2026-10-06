package agentprofile

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"testing"
)

var updateIndex = flag.Bool("update-index", false, "rewrite internal/agentprofile/builtins-index.json")

// TestBuiltinsIndexIsCurrent keeps the committed index equal to the builtin
// files. The website reads it from the repository, so a stale index would show
// a console what Belai no longer ships. Regenerate with:
//
//	go test ./internal/agentprofile -run TestBuiltinsIndexIsCurrent -update-index
func TestBuiltinsIndexIsCurrent(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	idx, err := BuildIndex(root)
	if err != nil {
		t.Fatal(err)
	}
	want, err := MarshalIndex(idx)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, filepath.FromSlash(IndexPath))
	if *updateIndex {
		if err := os.WriteFile(file, want, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	got, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("%v; run the test with -update-index", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s is stale; run: go test ./internal/agentprofile -run TestBuiltinsIndexIsCurrent -update-index", IndexPath)
	}
}

// The index names every builtin the binary embeds, and nothing else.
func TestBuiltinsIndexMatchesTheEmbeddedSet(t *testing.T) {
	root, _ := filepath.Abs("../..")
	idx, err := BuildIndex(root)
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, p := range idx.Profiles {
		have[p.Name] = true
	}
	for name := range builtinProfiles {
		if !have[name] {
			t.Errorf("profile %s is embedded but not indexed", name)
		}
		delete(have, name)
	}
	for n := range have {
		t.Errorf("profile %s is indexed but not embedded", n)
	}
	if len(idx.Crews) != 2 || len(idx.Skills) != 6 {
		t.Errorf("crews = %d, skills = %d", len(idx.Crews), len(idx.Skills))
	}
}
