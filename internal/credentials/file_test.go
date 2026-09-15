package credentials

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFileMissingIsNotError(t *testing.T) {
	fs := newFileStore(filepath.Join(t.TempDir(), "missing", "credentials.json"), false)
	_, ok, _ := fs.read("openai", "api_key", Spec("openai"))
	if ok {
		t.Fatalf("expected missing file to yield no value")
	}
}

func TestFileInlineSecretRejectedAtLooseMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "credentials.json")
	data := `{"version":1,"providers":{"openai":{"api_key":{"source":"inline","value":"secret"}}}}`
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	fs := newFileStore(path, false)
	v, ok, note := fs.read("openai", "api_key", Spec("openai"))
	if ok {
		t.Fatalf("expected rejection at mode 0644")
	}
	if note == "" {
		t.Fatalf("expected a note about insecure permissions")
	}
	if v.value != "" {
		t.Fatalf("value should be empty")
	}
}

func TestFileInlineSecretRejectedInProjectFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "credentials.json")
	data := `{"version":1,"providers":{"openai":{"api_key":{"source":"inline","value":"secret"}}}}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	fs := newFileStore(path, true)
	v, ok, note := fs.read("openai", "api_key", Spec("openai"))
	if ok {
		t.Fatalf("expected rejection in project file")
	}
	if note == "" {
		t.Fatalf("expected a note about project file")
	}
	if v.value != "" {
		t.Fatalf("value should be empty")
	}
}

func TestFileProjectReferenceAccepted(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "credentials.json")
	data := `{"version":1,"providers":{"openai":{"api_key":{"source":"env","name":"OPENAI_API_KEY"}}}}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	t.Setenv("OPENAI_API_KEY", "from-env")
	fs := newFileStore(path, true)
	v, ok, _ := fs.read("openai", "api_key", Spec("openai"))
	if !ok {
		t.Fatalf("expected reference to be accepted")
	}
	if v.Reveal() != "from-env" {
		t.Fatalf("unexpected value: %q", v.Reveal())
	}
	if v.Source != SourceEnv {
		t.Fatalf("expected source env, got %q", v.Source)
	}
}

func TestFileStoreWritesMode0600(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "credentials.json")
	fs := newFileStore(path, false)
	if err := fs.write("openai", "api_key", "secret"); err != nil {
		t.Fatalf("write: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("expected mode 0600, got %04o", info.Mode().Perm())
	}
}

func TestFileAllowInsecurePermsOptIn(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "credentials.json")
	data := `{"version":1,"allow_insecure_perms":true,"providers":{"openai":{"api_key":{"source":"inline","value":"secret"}}}}`
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	fs := newFileStore(path, false)
	v, ok, _ := fs.read("openai", "api_key", Spec("openai"))
	if !ok {
		t.Fatalf("expected opt-in to honour insecure perms")
	}
	if v.Reveal() != "secret" {
		t.Fatalf("unexpected value: %q", v.Reveal())
	}
}

func TestFilePlainFileForm(t *testing.T) {
	// If the parent path is a regular file (not a directory), read it directly.
	dir := t.TempDir()
	path := filepath.Join(dir, "signet") // no /credentials.json suffix; signet is a file
	data := `{"version":1,"providers":{"openai":{"api_key":{"source":"inline","value":"plain-secret"}}}}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	fs := newFileStore(path+"/credentials.json", false) // expects a dir, but parent is a file
	v, ok, _ := fs.read("openai", "api_key", Spec("openai"))
	if !ok {
		t.Fatalf("expected plain-file form to load")
	}
	if v.Reveal() != "plain-secret" {
		t.Fatalf("unexpected value: %q", v.Reveal())
	}
}
