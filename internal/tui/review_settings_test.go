package tui

import (
	"slices"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/config"
)

func reviewApp(t *testing.T, vulnetix *config.VulnetixSettings) *App {
	t.Helper()
	t.Setenv("BELAI_HOME", t.TempDir())
	workdir := t.TempDir()
	a := New(Options{Workdir: workdir})
	if vulnetix != nil {
		if err := config.Mutate(config.ScopeGlobal, workdir, func(s *config.Settings) error {
			s.Vulnetix = vulnetix
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if err := a.reloadSettings(); err != nil {
			t.Fatal(err)
		}
	}
	return a
}

// TestReviewRunsEveryScannerByDefault pins the defaults: no unattended fix, all
// nine scanners and the post-scan fix, and no time limit.
func TestReviewRunsEveryScannerByDefault(t *testing.T) {
	v := reviewApp(t, nil).reviewCommand(nil)
	if v.AutoFix || v.Subcommands != nil || v.Timeout != 0 {
		t.Fatalf("defaults = autofix %v, subcommands %v, timeout %v", v.AutoFix, v.Subcommands, v.Timeout)
	}
	names, err := v.ActivityNames()
	if err != nil || len(names) != 10 || names[len(names)-1] != "fix" {
		t.Fatalf("activities = %v (%v), want nine scanners and fix", names, err)
	}
}

// TestReviewHonoursTheVulnetixSettings is the wiring the docs promise: the
// user's autofix, subcommands and timeout shape the review the TUI starts.
func TestReviewHonoursTheVulnetixSettings(t *testing.T) {
	yes := true
	a := reviewApp(t, &config.VulnetixSettings{AutoFix: &yes, Subcommands: []string{"sca", "sast"}, Timeout: "10m"})
	v := a.reviewCommand(nil)
	if !v.AutoFix {
		t.Error("vulnetix.autofix did not reach the review")
	}
	if v.Timeout != 10*time.Minute {
		t.Errorf("review timeout = %v, want 10m", v.Timeout)
	}
	names, err := v.ActivityNames()
	if err != nil || !slices.Equal(names, []string{"sca", "sast", "fix"}) {
		t.Fatalf("activities = %v (%v), want only the configured scanners and fix", names, err)
	}
}
