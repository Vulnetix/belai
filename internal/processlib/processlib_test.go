package processlib

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vulnetix/signet/internal/config"
)

func TestNameForDerivesFromArgvZero(t *testing.T) {
	cases := []struct {
		cmd  string
		want string
	}{
		{"llama-server -hf foo --port 18080", "llama-server"},
		{"/usr/local/bin/python script.py", "python"},
		{"sleep 30", "sleep"},
	}
	for _, c := range cases {
		got, err := NameFor(c.cmd)
		if err != nil {
			t.Fatalf("NameFor(%q): %v", c.cmd, err)
		}
		if got != c.want {
			t.Fatalf("NameFor(%q) = %q, want %q", c.cmd, got, c.want)
		}
	}
}

func TestCreateUniqueReusesIdenticalBody(t *testing.T) {
	dir := t.TempDir()
	cmd := "llama-server --port 18080"
	e1, err := CreateUnique(config.ScopeProject, dir, cmd)
	if err != nil {
		t.Fatalf("first create: %v", err)
	}
	e2, err := CreateUnique(config.ScopeProject, dir, cmd)
	if err != nil {
		t.Fatalf("second create: %v", err)
	}
	if e1.Path != e2.Path || e1.Name != e2.Name {
		t.Fatalf("expected reuse, got %q / %q and %q / %q", e1.Path, e1.Name, e2.Path, e2.Name)
	}
	listing, _ := Load(config.ScopeProject, dir)
	if len(listing.Entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(listing.Entries))
	}
}

func TestCreateUniqueSuffixesDifferentBody(t *testing.T) {
	dir := t.TempDir()
	cmdA := "llama-server --port 18080"
	cmdB := "llama-server --port 18081"
	eA, err := CreateUnique(config.ScopeProject, dir, cmdA)
	if err != nil {
		t.Fatalf("create A: %v", err)
	}
	eB, err := CreateUnique(config.ScopeProject, dir, cmdB)
	if err != nil {
		t.Fatalf("create B: %v", err)
	}
	if eA.Name == eB.Name {
		t.Fatalf("expected different names, got %q and %q", eA.Name, eB.Name)
	}
	if !strings.HasSuffix(eB.Name, "-2") && eB.Name != "llama-server-2" {
		t.Fatalf("expected suffixed name, got %q", eB.Name)
	}
	listing, _ := Load(config.ScopeProject, dir)
	if len(listing.Entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(listing.Entries))
	}
}

func TestCommandStoredVerbatim(t *testing.T) {
	dir := t.TempDir()
	cmd := "llama-server -hf yuxinlu1/model --port 18080"
	e, err := CreateUnique(config.ScopeProject, dir, cmd)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	data, err := os.ReadFile(e.Path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	// The generic library stores the body with a trailing newline.
	if string(data) != cmd+"\n" {
		t.Fatalf("file = %q, want %q", string(data), cmd+"\n")
	}
}

func TestLoadAndMerge(t *testing.T) {
	dir := t.TempDir()
	if _, err := CreateUnique(config.ScopeProject, dir, "sleep 1"); err != nil {
		t.Fatal(err)
	}
	if _, err := CreateUnique(config.ScopeProject, dir, "sleep 2"); err != nil {
		t.Fatal(err)
	}
	listing, err := Load(config.ScopeProject, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(listing.Entries) != 2 {
		t.Fatalf("got %d entries", len(listing.Entries))
	}
	got := Filter(listing.Entries, "sleep 2")
	if len(got) != 1 {
		t.Fatalf("filter returned %d", len(got))
	}
}

func TestProcessDirCreated(t *testing.T) {
	dir := t.TempDir()
	if _, err := CreateUnique(config.ScopeProject, dir, "sleep 1"); err != nil {
		t.Fatal(err)
	}
	pd := filepath.Join(dir, ".vulnetix", "processes")
	if _, err := os.Stat(pd); err != nil {
		t.Fatalf("process dir not created: %v", err)
	}
}
