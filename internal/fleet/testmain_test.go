package fleet

import (
	"os"
	"testing"

	"github.com/vulnetix/belai/internal/proc"
	"github.com/vulnetix/belai/internal/sandbox"
)

// TestMain lets this test binary act as the Landlock helper: a sandboxed
// command runs through os.Executable(), which under go test is this binary.
func TestMain(m *testing.M) {
	sandbox.HelperMain()
	// Ambient git locator variables (GIT_DIR, GIT_WORK_TREE, …) exported by a
	// sandbox or worktree helper would point every git subprocess at a foreign
	// repository instead of the test's own fixture directory.
	proc.UnsetGitEnv()
	os.Exit(m.Run())
}
