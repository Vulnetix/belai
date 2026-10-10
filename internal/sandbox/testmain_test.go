package sandbox

import (
	"os"
	"testing"
)

// TestMain lets this test binary act as the Landlock helper: Wrap runs the
// command through os.Executable(), which under go test is this binary.
func TestMain(m *testing.M) {
	HelperMain()
	os.Exit(m.Run())
}
