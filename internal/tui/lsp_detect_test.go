package tui

import "testing"

// TestInitProbesLSPImmediately verifies that App.Init starts the same LSP
// detection pass that /lsp runs, so language-server availability is cached
// before the user opens the LSP settings screen.
func TestInitProbesLSPImmediately(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	a := New(Options{Workdir: t.TempDir()})
	_ = a.Init()

	a.lspDetect.mu.Lock()
	defer a.lspDetect.mu.Unlock()
	if !a.lspDetect.inFlight {
		t.Fatal("Init must start the LSP probe immediately")
	}
	if a.lspDetect.langs == nil {
		t.Fatal("Init must seed the language list for the LSP probe")
	}
}
