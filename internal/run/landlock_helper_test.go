package run

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/sandbox"
)

// TestBinaryAnswersTheLandlockProbe pins that a test binary linking the
// sandbox answers the Landlock helper's probe instead of rerunning its suite.
// Without a usable bubblewrap the sandbox probes `os.Executable()`, which is
// this test binary; before the sandbox registered its helper itself, each
// probe started the whole suite again, recursively, until a CI runner ran out
// of memory. This package has no TestMain on purpose.
func TestBinaryAnswersTheLandlockProbe(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, os.Args[0], sandbox.LandlockCommand, "-probe").CombinedOutput()
	if ctx.Err() != nil {
		t.Fatal("the probe did not return; the test binary is running its suite again")
	}
	text := string(out)
	if strings.Contains(text, "=== RUN") || strings.Contains(text, "PASS") || strings.Contains(text, "FAIL") {
		t.Fatalf("the probe ran the test suite:\n%s", text)
	}
	// 0 with an ABI where Landlock exists, 3 where it does not.
	var exit *exec.ExitError
	if err != nil && (!errors.As(err, &exit) || exit.ExitCode() != 3) {
		t.Fatalf("probe exit: %v\n%s", err, text)
	}
}
