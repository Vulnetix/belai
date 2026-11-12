package review

import (
	"os"
	"testing"

	"github.com/vulnetix/belai/internal/gittest"
)

// TestMain drops inherited git redirection (a Belai sandbox's GIT_DIR,
// GIT_WORK_TREE and GIT_CONFIG_*) for the whole package: the repositories the
// collect fixtures build must resolve to their own .git, not the metadata the
// sandbox points its own git at.
func TestMain(m *testing.M) {
	gittest.IsolateProcess()
	os.Exit(m.Run())
}
