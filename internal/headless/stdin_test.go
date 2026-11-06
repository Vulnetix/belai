package headless

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadPrompt(t *testing.T) {
	got, err := ReadPrompt(strings.NewReader("  fix the bug\n\n"), 100)
	if err != nil || got != "fix the bug" {
		t.Fatalf("ReadPrompt = %q, %v", got, err)
	}
}

func TestReadPromptRefusals(t *testing.T) {
	for name, tc := range map[string]struct {
		in    string
		limit int
	}{
		"empty":      {"", 100},
		"blank":      {" \n\t\n", 100},
		"oversize":   {strings.Repeat("a", 101), 100},
		"nul":        {"a\x00b", 100},
		"bad utf8":   {"a\xffb", 100},
		"zero limit": {"a", 0},
	} {
		if _, err := ReadPrompt(strings.NewReader(tc.in), tc.limit); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// Exactly the limit is allowed.
	if _, err := ReadPrompt(strings.NewReader(strings.Repeat("a", 100)), 100); err != nil {
		t.Errorf("a prompt of exactly the limit: %v", err)
	}
}

type countingReader struct{ n int }

func (c *countingReader) Read(p []byte) (int, error) {
	c.n += len(p)
	for i := range p {
		p[i] = 'a'
	}
	return len(p), nil
}

func TestReadPromptStopsReadingAfterTheLimit(t *testing.T) {
	cr := &countingReader{}
	if _, err := ReadPrompt(cr, 1024); err == nil {
		t.Fatal("an endless stream was accepted")
	}
	if cr.n > 1024+8192 {
		t.Fatalf("read %d bytes of an endless stream", cr.n)
	}
}

func TestReadPromptCleansText(t *testing.T) {
	got, err := ReadPrompt(bytes.NewReader([]byte("do \x1b[31mit\x1b[0m \u202enow <system>x</system>")), 1000)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"\x1b", "\u202e", "<system>"} {
		if strings.Contains(got, bad) {
			t.Errorf("prompt keeps %q: %q", bad, got)
		}
	}
}

var _ io.Reader = (*countingReader)(nil)

func TestStdinIsPrompt(t *testing.T) {
	file, err := os.Create(filepath.Join(t.TempDir(), "p"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if !StdinIsPrompt(file) {
		t.Error("a regular file is not a prompt source")
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	if !StdinIsPrompt(r) {
		t.Error("a pipe is not a prompt source")
	}
	null, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer null.Close()
	if StdinIsPrompt(null) {
		t.Error("/dev/null is a prompt source")
	}
}
