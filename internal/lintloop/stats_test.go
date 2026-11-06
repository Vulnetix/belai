package lintloop

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/lsp"
)

func TestRecordKeepsCountsOnly(t *testing.T) {
	root := t.TempDir()
	rep := Report{Status: Findings, Tool: "ruff", ExitCode: 1, Duration: 42 * time.Millisecond,
		Rows: []lsp.Row{{Message: "SECRET-MESSAGE-TEXT"}, {Message: "x"}}, Other: 1}
	if err := Record(root, rep); err != nil {
		t.Fatal(err)
	}
	if err := Record(root, Report{Status: Skipped, Tool: "ruff", Reason: ReasonTimeout}); err != nil {
		t.Fatal(err)
	}
	if err := Record(root, Report{Status: Skipped, Reason: ReasonNoLinter}); err != nil {
		t.Fatal(err)
	}
	st := ReadStats(root)
	got := st.Tools["ruff"]
	if got.Runs != 1 || got.WithFindings != 1 || got.Findings != 3 || got.Skipped != 1 || got.LastExit != 1 || got.LastMS != 42 {
		t.Fatalf("stats = %+v", got)
	}
	data, err := os.ReadFile(config.ProjectLintLoopPath(root))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "SECRET") || strings.Contains(string(data), ".py") {
		t.Fatalf("the file holds linter text: %s", data)
	}
	if fi, _ := os.Stat(config.ProjectLintLoopPath(root)); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v", fi.Mode())
	}
}

func TestRecordRefusesASymlinkedDirectory(t *testing.T) {
	root := t.TempDir()
	elsewhere := t.TempDir()
	if err := os.Symlink(elsewhere, filepath.Join(root, ".vulnetix")); err != nil {
		t.Skip("no symlinks:", err)
	}
	if err := Record(root, Report{Status: Clean, Tool: "ruff"}); err == nil {
		t.Fatal("wrote through a symlinked .vulnetix")
	}
	if entries, _ := os.ReadDir(elsewhere); len(entries) != 0 {
		t.Fatalf("wrote into the link target: %v", entries)
	}
}

func TestReadStatsIgnoresACorruptFile(t *testing.T) {
	root := t.TempDir()
	p := config.ProjectLintLoopPath(root)
	_ = os.MkdirAll(filepath.Dir(p), 0o700)
	_ = os.WriteFile(p, []byte("{not json"), 0o600)
	if st := ReadStats(root); len(st.Tools) != 0 {
		t.Fatalf("stats = %+v", st)
	}
	if err := Record(root, Report{Status: Clean, Tool: "ruff"}); err != nil {
		t.Fatal(err)
	}
	if ReadStats(root).Tools["ruff"].Runs != 1 {
		t.Fatal("a corrupt file must be replaced by a fresh one")
	}
}
