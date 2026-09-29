package kiroauth

import (
	"os"
	"testing"
)

// TestMain keeps every import test off the developer's own kiro-cli database,
// which XDG_DATA_HOME would otherwise point at.
func TestMain(m *testing.M) {
	os.Unsetenv("XDG_DATA_HOME")
	os.Exit(m.Run())
}
