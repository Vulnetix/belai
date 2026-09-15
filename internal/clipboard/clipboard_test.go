package clipboard

import (
	"runtime"
	"testing"
)

func TestCopy(t *testing.T) {
	// clipboard.WriteAll may fail in headless CI; the fallback is OSC 52.
	method, err := Copy("hello clipboard")
	if runtime.GOOS == "linux" && err != nil {
		// headless CI may not have a clipboard
		return
	}
	if method != "native" && method != "osc52" {
		t.Fatalf("unexpected method: %s", method)
	}
}
