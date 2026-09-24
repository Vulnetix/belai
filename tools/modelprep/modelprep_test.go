package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestTruncate pins the human-readable truncation used in golden-set output:
// short strings pass through, long strings collapse to an n-rune prefix plus a
// single ellipsis rune.
func TestTruncate(t *testing.T) {
	if got := truncate("short", 40); got != "short" {
		t.Fatalf("truncate(short) = %q", got)
	}
	if got := truncate("1234567890", 5); got != "12345…" {
		t.Fatalf("truncate(long) = %q", got)
	}
	if got := truncate("12345", 5); got != "12345" {
		t.Fatalf("truncate(boundary) = %q", got)
	}
	if got := truncate("", 5); got != "" {
		t.Fatalf("truncate(empty) = %q", got)
	}
}

// TestCountLines counts non-blank lines exactly as prepare's vocab-size check
// does, and reports zero for a missing file rather than erroring.
func TestCountLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vocab.txt")
	if got := countLines(path); got != 0 {
		t.Fatalf("countLines(missing) = %d, want 0", got)
	}
	data := "[PAD]\n[UNK]\n\nhello\nworld\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := countLines(path); got != 4 {
		t.Fatalf("countLines = %d, want 4", got)
	}
}

// TestFirstNonEmpty returns the first non-blank value and skips blank ones.
func TestFirstNonEmpty(t *testing.T) {
	if got := firstNonEmpty("", "  ", "tok", "other"); got != "tok" {
		t.Fatalf("firstNonEmpty = %q, want tok", got)
	}
	if got := firstNonEmpty("", "   "); got != "" {
		t.Fatalf("firstNonEmpty(blanks) = %q, want empty", got)
	}
	if got := firstNonEmpty(); got != "" {
		t.Fatalf("firstNonEmpty() = %q, want empty", got)
	}
}

// TestReadConfig pins the config JSON shape prepare consumes: vocab_size and
// id2label, plus the error paths for a missing file and malformed JSON.
func TestReadConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if _, err := readConfig(path); err == nil {
		t.Fatal("readConfig(missing) should error")
	}
	if err := os.WriteFile(path, []byte(`{"vocab_size":30522,"id2label":{"0":"safe","1":"unsafe"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := readConfig(path)
	if err != nil {
		t.Fatalf("readConfig: %v", err)
	}
	if cfg.VocabSize != 30522 || cfg.ID2Label["1"] != "unsafe" {
		t.Fatalf("cfg = %+v", cfg)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readConfig(path); err == nil {
		t.Fatal("readConfig(bad json) should error")
	}
}

// TestInjectID2Label pins that a config without id2label/label2id gains a
// two-class head so the cybertron converter sizes the classification output.
func TestInjectID2Label(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"vocab_size":30522}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := injectID2Label(path); err != nil {
		t.Fatalf("injectID2Label: %v", err)
	}
	cfg, err := readConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ID2Label["0"] != "LABEL_0" || cfg.ID2Label["1"] != "LABEL_1" {
		t.Fatalf("id2label after inject = %+v", cfg.ID2Label)
	}
}

// TestAssetsComplete pins the four-file presence check that lets runPhase skip
// an already-built asset directory.
func TestAssetsComplete(t *testing.T) {
	dir := t.TempDir()
	if assetsComplete(dir) {
		t.Fatal("empty dir must not be complete")
	}
	for _, f := range []string{"vocab.txt", "tokenizer_config.json", "config.json", "spago_model.bin"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if !assetsComplete(dir) {
		t.Fatal("four files must be complete")
	}
	// A directory in place of a file must fail.
	if err := os.Remove(filepath.Join(dir, "vocab.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "vocab.txt"), 0o700); err != nil {
		t.Fatal(err)
	}
	if assetsComplete(dir) {
		t.Fatal("directory must not count as a file")
	}
}

// TestEmitCopiesOnlyTheFourFiles pins the runtime-facing subset emit writes and
// that an out directory is created on demand.
func TestEmit(t *testing.T) {
	work := t.TempDir()
	out := filepath.Join(t.TempDir(), "nested", "assets")
	names := []string{"vocab.txt", "tokenizer_config.json", "config.json", "spago_model.bin", "unwanted.json"}
	for _, f := range names {
		if err := os.WriteFile(filepath.Join(work, f), []byte(f+"-body"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := emit(out, work); err != nil {
		t.Fatalf("emit: %v", err)
	}
	for _, f := range []string{"vocab.txt", "tokenizer_config.json", "config.json", "spago_model.bin"} {
		data, err := os.ReadFile(filepath.Join(out, f))
		if err != nil {
			t.Fatalf("missing emitted file %s: %v", f, err)
		}
		if string(data) != f+"-body" {
			t.Fatalf("%s = %q", f, data)
		}
	}
	if _, err := os.Stat(filepath.Join(out, "unwanted.json")); !os.IsNotExist(err) {
		t.Fatal("unwanted.json must not be emitted")
	}
}
