package tui

import "testing"

// Init must start the language-server probe and, with a resolver, the
// startup model tests must be armed for the first resolved credentials.
func TestInitArmsStartupChecks(t *testing.T) {
	a := New(Options{Workdir: t.TempDir()})
	if cmd := a.Init(); cmd == nil {
		t.Fatal("Init returned no command")
	}
	if !a.startupPending {
		t.Fatal("startup model tests were not armed")
	}
	if !a.lspDetect.inFlight {
		t.Fatal("language-server probe did not start at Init")
	}
}
