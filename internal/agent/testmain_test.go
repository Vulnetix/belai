package agent

import (
	"os"
	"testing"

	"github.com/vulnetix/belai/internal/gittest"
)

// TestMain drops inherited git redirection (a Belai sandbox's GIT_DIR and
// GIT_WORK_TREE, a hook's GIT_INDEX_FILE) for the whole package: the prefetch
// and warm-docs fixtures, and the product's own git probes (repoindex,
// warmDocFiles) that run with the process environment, must see only the
// repositories the tests build.
func TestMain(m *testing.M) {
	gittest.IsolateProcess()
	os.Exit(m.Run())
}
