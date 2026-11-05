package gittest

import (
	"os"
	"testing"
)

// TestMain drops inherited git redirection (GIT_DIR, GIT_WORK_TREE, the
// index and object overrides a sandbox or hook exports) so the fixtures
// built with a bare exec.Command resolve to their own repositories.
func TestMain(m *testing.M) {
	IsolateProcess()
	os.Exit(m.Run())
}
