package tools

import (
	"os"
	"testing"

	"github.com/vulnetix/belai/internal/sandbox"
)

// TestMain lets this test binary act as the Landlock helper: a sandboxed
// command runs through os.Executable(), which under go test is this binary.
func TestMain(m *testing.M) {
	sandbox.HelperMain()
	os.Exit(m.Run())
}
