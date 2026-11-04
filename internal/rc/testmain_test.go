package rc

import (
	"fmt"
	"os"
	"testing"

	"github.com/vulnetix/belai/internal/gittest"
)

// TestMain points the whole package at a scratch home. A test's t.Setenv is
// undone when the test returns, but a daemon goroutine it started (a
// session's wait, which rewrites rc.json) can still run after that, and it
// then resolved the developer's real ~/.vulnetix/belai/rc/rc.json. With the
// package's own BELAI_HOME and HOME underneath, a late write lands here.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "belai-rc-test-home-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "rc tests: scratch home:", err)
		os.Exit(1)
	}
	for k, v := range map[string]string{"BELAI_HOME": home + "/belai", "HOME": home} {
		if err := os.Setenv(k, v); err != nil {
			fmt.Fprintln(os.Stderr, "rc tests:", err)
			os.Exit(1)
		}
	}
	// Drop inherited git redirection so the workspace fixtures the tests
	// build resolve to their own repositories, not the wrapper's.
	gittest.IsolateProcess()
	code := m.Run()
	_ = os.RemoveAll(home)
	os.Exit(code)
}
