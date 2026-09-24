package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/vulnetix/signet/internal/session"
)

func TestInteractiveDecision(t *testing.T) {
	cases := []struct {
		name      string
		stdoutTTY bool
		stdinTTY  bool
		ci        string
		noTUI     string
		want      bool
	}{
		{"both tty", true, true, "", "", true},
		{"stdout only", true, false, "", "", false},
		{"stdin only", false, true, "", "", false},
		{"neither tty", false, false, "", "", false},
		{"ci set", true, true, "true", "", false},
		{"signet_no_tui set", true, true, "", "1", false},
		{"both tty despite empty ci", true, true, "", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CI", tc.ci)
			t.Setenv("SIGNET_NO_TUI", tc.noTUI)
			got := interactive(tc.stdoutTTY, tc.stdinTTY, os.Getenv)
			if got != tc.want {
				t.Fatalf("interactive = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestIsCharDevice(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "notty")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	defer f.Close()
	if isCharDevice(f) {
		t.Fatal("isCharDevice(regular file) = true, want false")
	}
}

func TestContinueLatest(t *testing.T) {
	st := session.NewStoreAt(t.TempDir())
	cur, err := session.KeyFor(t.TempDir())
	if err != nil {
		t.Fatalf("KeyFor: %v", err)
	}

	// Two sessions in the current project; the newer one (by mtime) wins.
	for _, id := range []string{"older", "newer"} {
		if err := st.AppendTo(cur, id, session.Entry{ID: "x", Type: "user", Content: "hi"}); err != nil {
			t.Fatalf("append %s: %v", id, err)
		}
	}
	_ = os.Chtimes(filepath.Join(st.Root, string(cur), "older.jsonl"), time.UnixMilli(100), time.UnixMilli(100))
	_ = os.Chtimes(filepath.Join(st.Root, string(cur), "newer.jsonl"), time.UnixMilli(200), time.UnixMilli(200))

	key, id, err := continueLatest(st, cur)
	if err != nil {
		t.Fatalf("continueLatest: %v", err)
	}
	if key != cur {
		t.Fatalf("key = %q, want %q", key, cur)
	}
	if id != "newer" {
		t.Fatalf("id = %q, want %q", id, "newer")
	}
}

func TestContinueLatestNoSessions(t *testing.T) {
	st := session.NewStoreAt(t.TempDir())
	cur, err := session.KeyFor(t.TempDir())
	if err != nil {
		t.Fatalf("KeyFor: %v", err)
	}
	if _, _, err := continueLatest(st, cur); err == nil {
		t.Fatal("continueLatest = nil error, want no-sessions error")
	}
}
