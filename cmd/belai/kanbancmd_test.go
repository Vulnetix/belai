package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/sandbox"
)

// Inside Belai's OS sandbox the state directory is hidden, so the board would
// read as empty. The CLI must refuse and point at the kanban tools instead of
// reporting "no items".
func TestKanbanCLIRefusesInsideTheSandbox(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	t.Setenv(sandbox.EnvMarker, "1")
	var out, errOut bytes.Buffer
	code := runKanbanCLI([]string{"list"}, strings.NewReader(""), &out, &errOut)
	if code == 0 {
		t.Fatalf("exit 0 inside the sandbox; stdout %q", out.String())
	}
	if !strings.Contains(errOut.String(), "not reachable from inside Belai's sandbox") || !strings.Contains(errOut.String(), "KanbanSearch") {
		t.Fatalf("stderr %q", errOut.String())
	}
	if strings.Contains(out.String(), "no items") {
		t.Fatal("reported an empty board")
	}
}
