package keys

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestWriterPushesAfterAltScreenEnter(t *testing.T) {
	f := tempFile(t)
	w := &Writer{File: f}
	n, err := w.Write([]byte("\x1b[?1049h"))
	if err != nil {
		t.Fatal(err)
	}
	if n != len("\x1b[?1049h") {
		t.Fatalf("n = %d", n)
	}
	got, _ := os.ReadFile(f.Name())
	if !bytes.HasPrefix(got, []byte("\x1b[?1049h")) || !bytes.Contains(got, []byte(Enter)) {
		t.Fatalf("expected enter followed by push, got %q", got)
	}
}

func TestWriterPopsBeforeAltScreenExit(t *testing.T) {
	f := tempFile(t)
	w := &Writer{File: f}
	w.Write([]byte("\x1b[?1049l"))
	got, _ := os.ReadFile(f.Name())
	if !bytes.HasPrefix(got, []byte(Pop)) || !bytes.HasSuffix(got, []byte("\x1b[?1049l")) {
		t.Fatalf("expected pop before exit, got %q", got)
	}
}

func TestWriterPassesUnrelatedWrites(t *testing.T) {
	f := tempFile(t)
	w := &Writer{File: f}
	in := []byte("hello")
	w.Write(in)
	got, _ := os.ReadFile(f.Name())
	if !bytes.Equal(got, in) {
		t.Fatalf("got %q want %q", got, in)
	}
}

func tempFile(t *testing.T) *os.File {
	t.Helper()
	path := filepath.Join(t.TempDir(), "out")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}
