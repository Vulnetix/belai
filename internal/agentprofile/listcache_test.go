package agentprofile

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// A cached List is the same as a fresh one, shares nothing with it, and sees
// a profile edited in place.
func TestListCacheMatchesAFreshParse(t *testing.T) {
	resetDir(t)
	p := AgentProfile{Name: "cache-bot", Description: "d", SystemPrompt: "sp", Mode: ModeSingle, Tools: []string{"Read"}, Metadata: map[string]string{"k": "v"}}
	if _, err := Save(p); err != nil {
		t.Fatal(err)
	}
	first, err := List()
	if err != nil {
		t.Fatal(err)
	}
	second, _ := List()
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("cached list differs:\n%+v\n%+v", first, second)
	}
	second[0].Tools[0] = "Write"
	second[0].Metadata["k"] = "changed"
	if third, _ := List(); third[0].Tools[0] != "Read" || third[0].Metadata["k"] != "v" {
		t.Fatal("a caller's change reached the cache")
	}

	dir, _ := Dir()
	file := filepath.Join(dir, first[0].File)
	p.Description = "e" // the same size: only the time tells
	if _, err := Save(p); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(2 * time.Second)
	_ = os.Chtimes(file, later, later)
	if got, _ := List(); got[0].Description != "e" {
		t.Fatalf("an edit in place was not seen: %q", got[0].Description)
	}
}
