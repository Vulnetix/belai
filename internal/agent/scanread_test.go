package agent

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/tools"
)

type routeCounter struct{ n atomic.Int32 }

func (c *routeCounter) Classify(context.Context, rolemanager.ClassifierPayload) (string, error) {
	c.n.Add(1)
	return string(rolemanager.SentinelSafe), nil
}

// readFile runs the real Read tool over a file written to a temp root.
func readFile(t *testing.T, name, body string) tools.Result {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := (&tools.Read{Root: root}).Execute(context.Background(), map[string]any{"file_path": name})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// The agent's Read path sends a doc to the SystemOne text classifier when one
// answers and to the usual classifier when not; code goes to the text
// classifier as comments only; classifier.code_scan "full" reads code whole
// through the usual classifier.
func TestAgentReadRoutesByContentClass(t *testing.T) {
	cases := []struct {
		name, file, body string
		systemOne, full  bool
		wantText, wantML int32
	}{
		{"doc with SystemOne", "README.md", "# Title\n", true, false, 1, 0},
		{"doc without SystemOne", "README.md", "# Title\n", false, false, 0, 1},
		{"code comments", "a.go", "package a\n// note\n", false, false, 1, 0},
		{"code without comments", "a.go", "package a\nfunc f() {}\n", true, false, 0, 0},
		{"code_scan full", "a.go", "package a\n// note\n", true, true, 0, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			text, ml := &routeCounter{}, &routeCounter{}
			pipe := rolemanager.NewPipeline(text)
			pipe.Security = ml
			pipe.SetMLSecurity(true)
			pipe.SetText(text, "text/test", c.systemOne)
			s := &Session{cfg: run.Config{Classifier: run.ClassifierConfig{CodeScanFull: c.full}}}
			dec, err := s.classifyRead(context.Background(), pipe, readFile(t, c.file, c.body))
			if err != nil || dec.Action != rolemanager.ActionProceed {
				t.Fatalf("dec=%+v err=%v", dec, err)
			}
			if text.n.Load() != c.wantText || ml.n.Load() != c.wantML {
				t.Fatalf("text %d ml %d, want %d %d", text.n.Load(), ml.n.Load(), c.wantText, c.wantML)
			}
		})
	}
}
