package main

import (
	"os"
	"testing"

	"github.com/vulnetix/belai/internal/gittest"
	"github.com/vulnetix/belai/internal/sandbox"
	"github.com/vulnetix/belai/internal/testenv"
)

// TestMain keeps every test in this package off the developer's live
// account. `belai kanban …` pushes what it changed on exit, with the real
// Vulnetix CLI credential from $HOME whatever BELAI_HOME says, and sync is on
// by default: without this, each go test run added its fixture items
// ("Check the branch", "Survey tests") to the developer's own board. A closed
// loopback port is an allowed origin that never answers. sessionsync also
// refuses any non-loopback origin from a test binary, as a second line.
func TestMain(m *testing.M) {
	// The binary is also the Landlock helper for the commands its tests sandbox.
	sandbox.HelperMain()
	os.Setenv("VULNETIX_WEB_URL", testenv.OfflineWebURL)
	// These tests exercise the CLI's outside-the-sandbox path; the marker
	// leaks in when `go test` itself runs inside a Belai sandbox.
	os.Unsetenv(sandbox.EnvMarker)
	// A Belai sandbox exports GIT_DIR, GIT_WORK_TREE and GIT_CONFIG_* aimed at
	// its own git metadata (read-only to these tests); the fixture
	// repositories the CLI tests build must resolve to their own .git.
	gittest.IsolateProcess()
	os.Exit(m.Run())
}
