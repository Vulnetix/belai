package vulnetixcli

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fakeScript emits a shell stub that recognises the hardening flags Belai
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

func TestTimeoutOrDefaultNoTimeoutSkipsDeadline(t *testing.T) {
	if got := (CLI{Timeout: NoTimeout}).timeoutOrDefault(); got != 0 {
		t.Fatalf("NoTimeout timeoutOrDefault = %v, want 0", got)
	}
	if got := (CLI{Timeout: 0}).timeoutOrDefault(); got != DefaultTimeout {
		t.Fatalf("zero timeout should mean the default, got %v", got)
	}
}

func TestNoTimeoutIsCancelledByParentContext(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vulnetix")
	script := `#!/bin/sh
sleep 1
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake: %v", err)
	}
	setPathTo(t, dir)
	c, err := Detect()
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	c.Timeout = NoTimeout

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = c.ExecIn(ctx, "", "scan")
	if err == nil {
		t.Fatal("expected the parent deadline to cancel the scan")
	}
	if time.Since(start) > 500*time.Millisecond {
		t.Fatalf("parent cancellation was not honoured; took %s", time.Since(start))
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
