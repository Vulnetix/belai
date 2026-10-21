package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/tools"
)

type warmCounter struct{ n atomic.Int32 }

func (c *warmCounter) Classify(context.Context, rolemanager.ClassifierPayload) (string, error) {
	c.n.Add(1)
	return string(rolemanager.SentinelSafe), nil
}

func warmRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	root := t.TempDir()
	files := map[string]string{
		"README.md":               "# Project\n",
		"docs/guide.md":           "A guide.\n",
		"main.go":                 "package main\n",
		"node_modules/pkg/doc.md": "dependency doc\n",
		".github/notes.md":        "hidden\n",
		"docs/huge.md":            strings.Repeat("x", WarmDocsMaxBytes+1),
		"untracked-not-added.txt": "",
	}
	for rel, body := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "-f", "README.md", "docs", "main.go", "node_modules", ".github"}} {
		if out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	return root
}

func TestWarmDocsClassifiesTrackedProseOnce(t *testing.T) {
	root := warmRepo(t)
	text := &warmCounter{}
	p := rolemanager.NewPipeline(text)
	p.Cache = rolemanager.NewCache(0, "")
	p.SetText(text, "text/test", true)
	if n := WarmDocs(context.Background(), p, root); n != 2 {
		t.Fatalf("classified %d, want README.md and docs/guide.md", n)
	}
	// A Read of a warmed file is answered from its file verdict.
	read := &tools.Read{Root: root}
	res, err := read.Execute(context.Background(), map[string]any{"file_path": "docs/guide.md"})
	if err != nil {
		t.Fatal(err)
	}
	before := text.n.Load()
	if dec, err := p.ProcessRead(context.Background(), res, false); err != nil || dec.Action != rolemanager.ActionProceed {
		t.Fatalf("dec = %+v err = %v", dec, err)
	}
	if text.n.Load() != before {
		t.Fatal("a warmed doc was classified again on Read")
	}
}

func TestWarmDocsNeedsSystemOne(t *testing.T) {
	root := warmRepo(t)
	text := &warmCounter{}
	p := rolemanager.NewPipeline(text)
	p.SetText(text, "text/test", false)
	if n := WarmDocs(context.Background(), p, root); n != 0 || text.n.Load() != 0 {
		t.Fatalf("warmed %d docs without a SystemOne backend", n)
	}
}
