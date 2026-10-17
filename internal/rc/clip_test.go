package rc

import (
	"strings"
	"testing"
)

func TestClipKeepsTheCauseOfAGoCrashNotItsFrames(t *testing.T) {
	dump := "runtime: failed to create new OS thread (have 2 already; errno=11)\n" +
		"runtime: may need to increase max user processes (ulimit -u)\n" +
		"fatal error: newosproc\n\nruntime stack:\nruntime.throw({0x1ce3c40?, 0x7ffc9})\n" +
		strings.Repeat("\t/home/runner/_work/belai/belai/cmd/belai/main.go:69 +0x1b\n", 40) +
		"\ngoroutine 1 [runnable]:\nmain.main()\n"
	got := clip(dump)
	if !strings.Contains(got, "failed to create new OS thread") || !strings.Contains(got, "fatal error: newosproc") {
		t.Fatalf("the cause was dropped: %q", got)
	}
	if strings.Contains(got, "goroutine") || strings.Contains(got, "main.go") {
		t.Fatalf("stack frames were kept: %q", got)
	}
}

func TestClipStillKeepsTheEndOfALongPlainReport(t *testing.T) {
	got := clip(strings.Repeat("noise ", 200) + "needs a working OS sandbox backend")
	if !strings.HasPrefix(got, "…") || !strings.HasSuffix(got, "sandbox backend") {
		t.Fatalf("got %q", got)
	}
}
