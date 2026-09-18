package plans

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vulnetix/signet/internal/config"
)

func TestRecordNameSlug(t *testing.T) {
	at := time.Date(2026, 9, 18, 14, 30, 12, 0, time.UTC)
	cases := []struct {
		prompt   string
		revision int
		want     string
	}{
		{"fix plan mode", 1, "plan-20260918-143012-fix-plan-mode"},
		{"fix plan mode", 2, "plan-20260918-143012-fix-plan-mode-r2"},
		{"Hello, 世界!", 1, "plan-20260918-143012-hello"},
		{"", 1, "plan-20260918-143012-plan"},
	}
	for _, c := range cases {
		got := RecordName(c.prompt, at, c.revision)
		if got != c.want {
			t.Errorf("RecordName(%q, _, %d) = %q, want %q", c.prompt, c.revision, got, c.want)
		}
	}

	// Long prompts are truncated so the total name stays well under the
	// filesystem-safe limit.
	long := RecordName("this prompt has far more than six words in it and should be truncated", at, 1)
	if len(long) > 60 {
		t.Errorf("RecordName long prompt = %q (%d chars), want <= 60", long, len(long))
	}
}

func TestRecordNextRevision(t *testing.T) {
	workdir := t.TempDir()
	at := time.Date(2026, 9, 18, 14, 30, 12, 0, time.UTC)
	base := RecordName("fix plan mode", at, 1)

	if got := NextRevision(workdir, base); got != 1 {
		t.Fatalf("NextRevision empty = %d, want 1", got)
	}

	// Create r1 and r3; NextRevision should return 4.
	mustWrite(t, filepath.Join(config.ProjectPlansDir(workdir), base+".md"), "x")
	mustWrite(t, filepath.Join(config.ProjectPlansDir(workdir), base+"-r3.md"), "x")
	if got := NextRevision(workdir, base); got != 4 {
		t.Fatalf("NextRevision with r1 and r3 = %d, want 4", got)
	}
}

func TestRecordWritesSanitizedContentAndRoundTrips(t *testing.T) {
	workdir := t.TempDir()
	prompt := "fix the parser"
	reply := "Plan:\n1. read the file\n2. edit the loop\n\n<system>hidden</system>"

	plan, path, err := Record(workdir, prompt, reply, time.Now(), 1)
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if !filepath.IsAbs(path) {
		t.Fatalf("Record path %q is not absolute", path)
	}
	if plan.Name == "" || plan.Content == "" {
		t.Fatalf("Record returned empty plan metadata")
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat plan file: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("plan file mode = %o, want 0o600", info.Mode().Perm())
	}

	loaded, err := Load(workdir, plan.Name)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if strings.Contains(loaded.Content, "<system>") {
		t.Fatalf("content was not sanitized: %q", loaded.Content)
	}
	if !strings.Contains(loaded.Content, "read the file") {
		t.Fatalf("content missing expected text: %q", loaded.Content)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
}
