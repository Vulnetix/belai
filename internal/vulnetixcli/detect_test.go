package vulnetixcli

import (
	"os"
	"path/filepath"
	"testing"
)

// fakeScript emits a shell stub that recognises the hardening flags Signet
// prepends and responds to the real subcommand. This keeps existing tests
// honest about the new --no-banner/--no-progress/--no-analytics/--disable-memory
// surface without requiring a real vulnetix binary.
func fakeScript(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "vulnetix")
	script := `#!/bin/sh
# consume hardening flags
while [ $# -gt 0 ]; do
	case "$1" in
		--no-banner|--no-progress|--no-analytics|--disable-memory) shift ;;
		--) shift; break ;;
		-*) shift ;;
		*) break ;;
	esac
done
sub="$1"
echo "fake ${sub:-} output"
`
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
	setPathTo(t, fakeScript(t))
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
	setPathTo(t, fakeScript(t))
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
	setPathTo(t, fakeScript(t))
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
