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

func TestKanbanImportFilesGatesAndRefusesUnsafeOnes(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	t.Setenv("VULNETIX_WEB_URL", "http://127.0.0.1:1")
	file := func(lines ...string) *strings.Reader { return strings.NewReader(strings.Join(lines, "\n") + "\n") }
	var out, errOut bytes.Buffer
	code := runKanbanCLI([]string{"import", "-"}, file(
		`{"title":"gated","labels":["build"],"gates":[{"title":"add works","kind":"runnable","suite":"go","dir":"p","test":"TestAdd"},{"title":"wording","kind":"manual"}]}`,
	), &out, &errOut)
	if code != 0 || !strings.Contains(out.String(), "1 items filed") {
		t.Fatalf("import: %d %s %s", code, out.String(), errOut.String())
	}
	for name, line := range map[string]string{
		"dir traversal": `{"title":"t1","gates":[{"title":"x","kind":"runnable","suite":"go","dir":"../etc"}]}`,
		"test regex":    `{"title":"t2","gates":[{"title":"x","kind":"runnable","suite":"go","test":"Test.*"}]}`,
		"unknown kind":  `{"title":"t3","gates":[{"title":"x","kind":"shell"}]}`,
		"unknown field": `{"title":"t4","gates":[{"title":"x","kind":"manual","command":"rm -rf /"}]}`,
	} {
		var o, e bytes.Buffer
		if code := runKanbanCLI([]string{"import", "-"}, file(line), &o, &e); code == 0 {
			t.Errorf("%s: an unsafe gate was accepted: %s", name, o.String())
		}
	}
}
