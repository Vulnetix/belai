// Package testenv keeps a test's Belai state, home directory and Vulnetix
// account apart from the developer's own: Belai's home and model directory
// and $HOME move to temp dirs, and the Vulnetix web URL points at a closed
// loopback port so no request can reach a real account.
//
// Only test files import this package.
package testenv

import "testing"

// OfflineWebURL is a loopback address nothing listens on. A Vulnetix client
// aimed at it fails fast with a refused connection.
const OfflineWebURL = "http://127.0.0.1:1"

// Env is the set of directories Isolate created for one test.
type Env struct {
	// Home is $HOME.
	Home string
	// BelaiHome is $BELAI_HOME.
	BelaiHome string
	// ModelsDir is $BELAI_MODELS_DIR.
	ModelsDir string
	// WorkDir is a further empty directory for the test to use as its
	// working tree; no variable points at it.
	WorkDir string
}

// Offline points VULNETIX_WEB_URL at OfflineWebURL for the rest of the test.
func Offline(t testing.TB) {
	t.Helper()
	t.Setenv("VULNETIX_WEB_URL", OfflineWebURL)
}

// Isolate gives BELAI_HOME, BELAI_MODELS_DIR and HOME their own fresh temp
// dirs, makes the test Offline, and returns the directories. Like t.Setenv it
// cannot be used in a parallel test.
func Isolate(t testing.TB) Env {
	t.Helper()
	e := Env{Home: t.TempDir(), BelaiHome: t.TempDir(), ModelsDir: t.TempDir(), WorkDir: t.TempDir()}
	t.Setenv("BELAI_HOME", e.BelaiHome)
	t.Setenv("BELAI_MODELS_DIR", e.ModelsDir)
	t.Setenv("HOME", e.Home)
	Offline(t)
	return e
}
