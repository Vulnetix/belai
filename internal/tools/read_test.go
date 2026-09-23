package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// readBody strips Read's `cat -n` gutter and any paging trailer, leaving the
// file bytes the result carries. Tests about path resolution use it so they
// assert on the file, not on the presentation.
func readBody(content string) string {
	var lines []string
	for _, l := range strings.Split(content, "\n") {
		if strings.HasPrefix(l, "[Read: ") {
			continue
		}
		if i := strings.IndexByte(l, '\t'); i >= 0 && strings.TrimSpace(l[:i]) != "" {
			l = l[i+1:]
		}
		lines = append(lines, l)
	}
	return strings.Join(lines, "\n")
}

func writeLines(t *testing.T, root, name string, n int) {
	t.Helper()
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "line%d\n", i)
	}
	if err := os.WriteFile(filepath.Join(root, name), []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestReadDefinitionAdvertisesFilePath(t *testing.T) {
	d := (&Read{}).Definition()
	if _, ok := d.Properties["file_path"]; !ok {
		t.Fatalf("Read schema does not advertise file_path: %+v", d.Properties)
	}
	if len(d.Required) != 1 || d.Required[0] != "file_path" {
		t.Fatalf("Read required = %v, want [file_path]", d.Required)
	}
}

// The schema is the trained one, and so are its units: offset and limit are
// lines. A description that still said "byte" would teach the model the
// opposite of what the tool does.
func TestReadDefinitionSaysLines(t *testing.T) {
	d := (&Read{}).Definition()
	for _, k := range []string{"offset", "limit"} {
		desc := d.Properties[k].Description
		if !strings.Contains(desc, "line") || strings.Contains(desc, "byte (not line)") {
			t.Fatalf("%s description = %q, want line units", k, desc)
		}
	}
	if !strings.Contains(d.Description, "cat -n") || !strings.Contains(d.Description, "old_string") {
		t.Fatalf("description does not explain the gutter: %q", d.Description)
	}
}

func TestReadAcceptsPathAlias(t *testing.T) {
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "hello.txt"), []byte("world"), 0o600)

	r := &Read{Root: root, MaxBytes: 1024}
	res, err := r.Execute(context.Background(), map[string]any{"path": "hello.txt"})
	if err != nil {
		t.Fatalf("Read with path alias: %v", err)
	}
	if res.Content != "     1\tworld" {
		t.Fatalf("content = %q", res.Content)
	}
}

func TestReadFileIsCatN(t *testing.T) {
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "a.txt"), []byte("one\ntwo\r\n\tthree\n"), 0o600)

	r := &Read{Root: root, MaxBytes: 1024}
	res, err := r.Execute(context.Background(), map[string]any{"file_path": "a.txt"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Line bytes are untouched after the tab — CR and indentation included —
	// and a whole-file read carries no trailer.
	if want := "     1\tone\n     2\ttwo\r\n     3\t\tthree"; res.Content != want {
		t.Fatalf("got %q, want %q", res.Content, want)
	}
	if res.Meta["start_line"] != 1 || res.Meta["numbered"] != true {
		t.Fatalf("meta = %v", res.Meta)
	}
}

func TestReadOffsetLimitAreLines(t *testing.T) {
	root := t.TempDir()
	writeLines(t, root, "data.txt", 10)

	r := &Read{Root: root, MaxBytes: 1024}
	res, err := r.Execute(context.Background(), map[string]any{"path": "data.txt", "offset": 4, "limit": 2})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "     4\tline4\n     5\tline5\n[Read: lines 4–5 of 10; truncated — continue with offset=6]"
	if res.Content != want {
		t.Fatalf("got %q, want %q", res.Content, want)
	}
	if res.Meta["start_line"] != 4 {
		t.Fatalf("start_line = %v, want 4", res.Meta["start_line"])
	}
}

// Models on the string-args path send numbers as strings.
func TestReadOffsetAsString(t *testing.T) {
	root := t.TempDir()
	writeLines(t, root, "data.txt", 3)
	r := &Read{Root: root, MaxBytes: 1024}
	res, err := r.Execute(context.Background(), map[string]any{"path": "data.txt", "offset": "3"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := "     3\tline3\n[Read: lines 3–3 of 3; end of file]"; res.Content != want {
		t.Fatalf("got %q, want %q", res.Content, want)
	}
}

// Offset 0 means "from the start", as it did when it was a byte count.
func TestReadOffsetZeroIsLineOne(t *testing.T) {
	root := t.TempDir()
	writeLines(t, root, "data.txt", 2)
	r := &Read{Root: root, MaxBytes: 1024}
	res, err := r.Execute(context.Background(), map[string]any{"path": "data.txt", "offset": 0})
	if err != nil || res.Content != "     1\tline1\n     2\tline2" {
		t.Fatalf("got %q, %v", res.Content, err)
	}
}

func TestReadNoTrailingNewline(t *testing.T) {
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "a.txt"), []byte("a\nb"), 0o600)
	r := &Read{Root: root, MaxBytes: 1024}
	res, err := r.Execute(context.Background(), map[string]any{"path": "a.txt", "offset": 2})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := "     2\tb\n[Read: lines 2–2 of 2; end of file]"; res.Content != want {
		t.Fatalf("got %q, want %q", res.Content, want)
	}
}

func TestReadEmptyFile(t *testing.T) {
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "empty.txt"), nil, 0o600)
	r := &Read{Root: root, MaxBytes: 1024}
	res, err := r.Execute(context.Background(), map[string]any{"path": "empty.txt"})
	if err != nil || res.Content != "" {
		t.Fatalf("got %q, %v", res.Content, err)
	}
}

// The byte cap stops on a whole line and says where to continue.
func TestReadByteCapStopsOnLineBoundary(t *testing.T) {
	root := t.TempDir()
	writeLines(t, root, "data.txt", 100)
	// Each row is "     N\tlineN\n" — 13 bytes here; 30 bytes fits two rows.
	r := &Read{Root: root, MaxBytes: 30}
	res, err := r.Execute(context.Background(), map[string]any{"path": "data.txt"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "     1\tline1\n     2\tline2\n[Read: lines 1–2 of 100; truncated — continue with offset=3]"
	if res.Content != want {
		t.Fatalf("got %q, want %q", res.Content, want)
	}
}

func TestReadDefaultLimitIs2000Lines(t *testing.T) {
	root := t.TempDir()
	writeLines(t, root, "data.txt", 2500)
	r := &Read{Root: root, MaxBytes: 1 << 20}
	res, err := r.Execute(context.Background(), map[string]any{"path": "data.txt"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasSuffix(res.Content, "[Read: lines 1–2000 of 2500; truncated — continue with offset=2001]") {
		t.Fatalf("tail = %q", res.Content[len(res.Content)-120:])
	}
}

// One enormous line is clipped on a rune boundary rather than spending the
// whole budget, and the lines after it still come back.
func TestReadClipsLongLine(t *testing.T) {
	root := t.TempDir()
	long := strings.Repeat("x", readMaxLineBytes-1) + "—" + strings.Repeat("y", 5000)
	_ = os.WriteFile(filepath.Join(root, "min.js"), []byte(long+"\nnext\n"), 0o600)
	r := &Read{Root: root, MaxBytes: 64 * 1024}
	res, err := r.Execute(context.Background(), map[string]any{"path": "min.js"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "     1\t" + strings.Repeat("x", readMaxLineBytes-1) + "… [line truncated]\n     2\tnext"
	if res.Content != want {
		t.Fatalf("got %d bytes, tail %q", len(res.Content), res.Content[len(res.Content)-40:])
	}
}

func TestReadBinaryRejection(t *testing.T) {
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "bin.dat"), []byte{0x00, 0x01, 0x02}, 0o600)

	r := &Read{Root: root, MaxBytes: 1024}
	_, err := r.Execute(context.Background(), map[string]any{"path": "bin.dat"})
	if err == nil || !strings.Contains(err.Error(), "binary") {
		t.Fatalf("expected binary rejection, got %v", err)
	}
}

func TestReadEscapeRejection(t *testing.T) {
	root := t.TempDir()
	r := &Read{Root: root, MaxBytes: 1024}
	_, err := r.Execute(context.Background(), map[string]any{"path": "../escape.txt"})
	if err == nil {
		t.Fatal("expected escape rejection")
	}
}

func TestReadMissingPath(t *testing.T) {
	root := t.TempDir()
	r := &Read{Root: root, MaxBytes: 1024}
	_, err := r.Execute(context.Background(), map[string]any{})
	if err == nil {
		t.Fatal("expected missing path error")
	}
}

func TestReadDirectoryRejection(t *testing.T) {
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "subdir"), 0o755)
	r := &Read{Root: root, MaxBytes: 1024}
	_, err := r.Execute(context.Background(), map[string]any{"path": "subdir"})
	if err == nil {
		t.Fatal("expected directory rejection")
	}
}

func TestReadMetaContainsPathAndLang(t *testing.T) {
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n"), 0o600)

	r := &Read{Root: root, MaxBytes: 1024}
	res, err := r.Execute(context.Background(), map[string]any{"path": "main.go"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Meta["path"] != "main.go" {
		t.Fatalf("path meta = %q", res.Meta["path"])
	}
	if res.Meta["lang"] != "Go" {
		t.Fatalf("lang meta = %q", res.Meta["lang"])
	}
}

// start_line is exact however deep the read starts: it is the line the
// caller asked for, not a newline count over a bounded prefix.
func TestReadStartLineDeepInLargeFile(t *testing.T) {
	root := t.TempDir()
	writeLines(t, root, "big.txt", 200_000)
	r := &Read{Root: root, MaxBytes: 64 * 1024}
	res, err := r.Execute(context.Background(), map[string]any{"path": "big.txt", "offset": 150_000, "limit": 1})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Meta["start_line"] != 150_000 {
		t.Fatalf("start_line = %v", res.Meta["start_line"])
	}
	if want := "150000\tline150000\n[Read: lines 150000–150000 of 200000; truncated — continue with offset=150001]"; res.Content != want {
		t.Fatalf("got %q, want %q", res.Content, want)
	}
}

func TestReadPastEOFIsNotAnError(t *testing.T) {
	root := t.TempDir()
	writeLines(t, root, "data.txt", 3)
	r := &Read{Root: root, MaxBytes: 1024}
	res, err := r.Execute(context.Background(), map[string]any{"path": "data.txt", "offset": 10000, "limit": 9000})
	if err != nil {
		t.Fatalf("offset past EOF should answer, got error %v", err)
	}
	if !strings.Contains(res.Content, "past the end of the file (3 lines)") {
		t.Fatalf("content = %q", res.Content)
	}
}

func TestReadRejectsNegativeArgs(t *testing.T) {
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "data.txt"), []byte("abcdef"), 0o600)
	r := &Read{Root: root, MaxBytes: 1024}
	for _, args := range []map[string]any{
		{"path": "data.txt", "limit": -1},
		{"path": "data.txt", "offset": -1},
	} {
		if _, err := r.Execute(context.Background(), args); err == nil {
			t.Fatalf("args %v: expected an error", args)
		}
	}
}

// Verbatim is the harness path (@file attachments): exact bytes, no gutter,
// no trailer, and a truncated read still ends on a whole character.
func TestReadVerbatim(t *testing.T) {
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "a.txt"), []byte("one\ntwo\n"), 0o600)
	r := &Read{Root: root, MaxBytes: 1024, Verbatim: true}
	res, err := r.Execute(context.Background(), map[string]any{"path": "a.txt", "offset": 2})
	if err != nil || res.Content != "one\ntwo\n" {
		t.Fatalf("got %q, %v", res.Content, err)
	}
	if _, ok := res.Meta["numbered"]; ok {
		t.Fatal("verbatim result must not claim a gutter")
	}

	// "a—b": the em-dash is three bytes (1..3); a 2-byte cap would split it.
	_ = os.WriteFile(filepath.Join(root, "dash.txt"), []byte("a—b"), 0o600)
	r = &Read{Root: root, MaxBytes: 2, Verbatim: true}
	res, err = r.Execute(context.Background(), map[string]any{"path": "dash.txt"})
	if err != nil || res.Content != "a" {
		t.Fatalf("got %q, %v", res.Content, err)
	}
}
