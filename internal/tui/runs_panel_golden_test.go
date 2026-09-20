package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vulnetix/signet/internal/activity"
)

func TestRunsPanelGolden(t *testing.T) {
	a := New(Options{})
	a.width = 80
	a.height = 24
	a.activity = activity.NewRegistry()
	for _, label := range []string{"vulnetix scan", "vulnetix bom", "!go test ./..."} {
		a.activity.Add(activity.Activity{Kind: activity.KindShell, Label: label, State: activity.StateDone}, func() {})
	}
	a.runsOpen = true
	a.runsFocus = true

	got := a.renderRunsPanel()
	path := filepath.Join("testdata", "runs_panel.golden")
	if os.Getenv("UPDATE_GOLDEN") != "" {
		_ = os.MkdirAll("testdata", 0o755)
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Log("golden updated")
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden (run UPDATE_GOLDEN=1 to create): %v", err)
	}
	if strings.TrimSpace(got) != strings.TrimSpace(string(want)) {
		t.Fatalf("rendered panel does not match golden:\n%s", diffLines(string(want), got))
	}
}

func diffLines(a, b string) string {
	as := strings.Split(a, "\n")
	bs := strings.Split(b, "\n")
	var out strings.Builder
	for i := 0; i < len(as) || i < len(bs); i++ {
		al, bl := "", ""
		if i < len(as) {
			al = as[i]
		}
		if i < len(bs) {
			bl = bs[i]
		}
		if al != bl {
			out.WriteString("-\t" + al + "\n+\t" + bl + "\n")
		}
	}
	return out.String()
}
