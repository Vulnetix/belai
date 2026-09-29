package agentscan

import (
	"os"
	"testing"
)

// TestMain keeps the scans off the developer's own agent state: kiro-cli's
// database is found through XDG_DATA_HOME before the test's home, so a set
// XDG_DATA_HOME would import the machine's real Kiro sign-in.
func TestMain(m *testing.M) {
	os.Unsetenv("XDG_DATA_HOME")
	os.Exit(m.Run())
}
