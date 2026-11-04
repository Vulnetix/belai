package changes

import (
	"os"
	"testing"

	"github.com/vulnetix/belai/internal/gittest"
)

// TestMain drops inherited git redirection (GIT_DIR, GIT_WORK_TREE, the
// index and object overrides a sandbox or hook exports) so the git fixtures
// resolve to their own repositories, not a foreign one.
func TestMain(m *testing.M) {
	gittest.IsolateProcess()
	os.Exit(m.Run())
}
