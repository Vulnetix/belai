package vulnetixcli

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFakeVulnetix(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "vulnetix")
	script := "#!/bin/sh\nsub=\"$1\"\necho \"fake $sub output\"\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake vulnetix: %v", err)
	}
	return dir
}

func setPathTo(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestDetectFindsVulnetix(t *testing.T) {
	setPathTo(t, writeFakeVulnetix(t))
	c, err := Detect()
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if filepath.Base(c.Path) != "vulnetix" {
		t.Fatalf("Path = %q", c.Path)
	}
}

func TestDetectNotFound(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if _, err := Detect(); err == nil {
		t.Fatalf("expected detection error")
	}
}

func TestRun(t *testing.T) {
	setPathTo(t, writeFakeVulnetix(t))
	c, err := Detect()
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	out, err := c.Run("scan")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out != "fake scan output\n" {
		t.Fatalf("Run output = %q", out)
	}
}

func TestInstallAgentAssets(t *testing.T) {
	setPathTo(t, writeFakeVulnetix(t))
	c, err := Detect()
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	out, err := c.InstallAgentAssets()
	if err != nil {
		t.Fatalf("InstallAgentAssets: %v", err)
	}
	if out != "fake agent output\n" {
		t.Fatalf("InstallAgentAssets output = %q", out)
	}
}
