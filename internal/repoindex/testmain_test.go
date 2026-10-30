package repoindex

import (
	"os"
	"testing"

	"github.com/vulnetix/belai/internal/gittest"
)

// TestMain drops inherited git redirection (a Belai sandbox's GIT_DIR and
// GIT_WORK_TREE, a hook's GIT_INDEX_FILE) for the whole package: both the
// fixtures and Scan's own probes run git with the process environment.
func TestMain(m *testing.M) {
	gittest.IsolateProcess()
	os.Exit(m.Run())
}
