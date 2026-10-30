package tools

import (
	"os"
	"testing"

	"github.com/vulnetix/belai/internal/gittest"
	"github.com/vulnetix/belai/internal/sandbox"
)

// TestMain lets this test binary act as the Landlock helper: a sandboxed
// command runs through os.Executable(), which under go test is this binary.
// It also drops inherited git redirection (a Belai sandbox's GIT_DIR and
// GIT_WORK_TREE, a hook's GIT_INDEX_FILE) so the repo-tool fixtures and the
// repoindex probes, which use the process environment, see only the
// repositories the tests build.
func TestMain(m *testing.M) {
	sandbox.HelperMain()
	gittest.IsolateProcess()
	os.Exit(m.Run())
}
