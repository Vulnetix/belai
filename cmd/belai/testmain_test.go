package main

import (
	"os"
	"testing"
)

// TestMain keeps every test in this package off the developer's live
// account. `belai kanban …` pushes what it changed on exit, with the real
// Vulnetix CLI credential from $HOME whatever BELAI_HOME says, and sync is on
// by default: without this, each go test run added its fixture items
// ("Check the branch", "Survey tests") to the developer's own board. A closed
// loopback port is an allowed origin that never answers. sessionsync also
// refuses any non-loopback origin from a test binary, as a second line.
func TestMain(m *testing.M) {
	os.Setenv("VULNETIX_WEB_URL", "http://127.0.0.1:1")
	os.Exit(m.Run())
}
