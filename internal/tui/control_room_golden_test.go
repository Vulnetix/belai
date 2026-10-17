package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/fleet"
	"github.com/vulnetix/belai/internal/kanban"
)

// TestControlRoomGolden renders every tab of the control room at a narrow,
// a standard and a wide terminal, so a layout change that breaks one width
// shows up in review, and no line runs past the screen. The repository path
// and the clock are fixed, and the random run and card ids are replaced.
// UPDATE_GOLDEN=1 rewrites the files.
func TestControlRoomGolden(t *testing.T) {
	for _, width := range []int{40, 80, 120} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			a, reg, run := controlRoomAppIn(t, "/srv/acme")
			now := time.UnixMilli(run.Started).Add(90 * time.Second)
			a.crew.now = func() time.Time { return now }
			rec, _ := reg.Get("belai-builder-aaaaaa")
			rec.Started, rec.Beat, rec.OutageUntil = run.Started, now.Add(-2*time.Second).UnixMilli(), now.Add(time.Minute).UnixMilli()
			if err := reg.Save(rec); err != nil {
				t.Fatal(err)
			}
			if _, err := reg.UpdateRun(run.ID, "human", func(r *fleet.CrewRun) error {
				r.Pool = []fleet.Target{{Provider: "openai", Model: "gpt-5"}, {Provider: "anthropic", Model: "claude-sonnet"}}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			one := crewCard(t, a, "wire the login form", kanban.Backlog)
			two := crewCard(t, a, "fix the flaky test", kanban.Blocked)
			a.width, a.height = width, 40
			a.push(viewControlRoom)
			var b strings.Builder
			for tab := 1; tab <= 6; tab++ {
				a.handleControlRoomKey(kkey(fmt.Sprint(tab)))
				fmt.Fprintf(&b, "=== tab %d\n%s\n", tab, roomText(a))
			}
			got := strings.NewReplacer(run.ID, "RUN-ID", one.Short(), "K-card01", two.Short(), "K-card02").Replace(b.String())
			got = trimLines(got)

			path := filepath.Join("testdata", fmt.Sprintf("control_room_%d.golden", width))
			if os.Getenv("UPDATE_GOLDEN") != "" {
				if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read golden (run UPDATE_GOLDEN=1 to create): %v", err)
			}
			if got != string(want) {
				t.Fatalf("the control room at %d columns does not match its golden:\n%s", width, diffLines(string(want), got))
			}
			for i, line := range strings.Split(got, "\n") {
				if n := len([]rune(line)); n > width {
					t.Errorf("line %d is %d columns wide at %d: %q", i+1, n, width, line)
				}
			}
		})
	}
}

// trimLines drops trailing spaces, which padding adds and a diff hides.
func trimLines(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " ")
	}
	return strings.Join(lines, "\n")
}
