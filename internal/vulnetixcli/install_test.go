package vulnetixcli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDetectInstallDirect(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	binDir := filepath.Join(dir, ".local", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(binDir, "vulnetix")
	if err := os.WriteFile(bin, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	method, prefix := DetectInstall(bin, os.Getenv)
	if method != InstallDirect {
		t.Fatalf("method = %q, want direct", method)
	}
	if prefix == "" {
		t.Fatal("expected non-empty prefix")
	}
}

func TestDetectInstallBrewSymlinkChain(t *testing.T) {
	root := t.TempDir()
	cellar := filepath.Join(root, "Cellar", "vulnetix", "3.107.2", "bin")
	if err := os.MkdirAll(cellar, 0o755); err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(cellar, "vulnetix")
	if err := os.WriteFile(real, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	prefix := filepath.Join(root, "bin")
	if err := os.MkdirAll(prefix, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(prefix, "vulnetix")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}

	method, pfx := DetectInstall(link, os.Getenv)
	if method != InstallBrew {
		t.Fatalf("method = %q, want brew", method)
	}
	if pfx == "" {
		t.Fatalf("expected brew prefix")
	}
}
