package proc

import (
	"strings"
	"sync"
	"testing"
	"time"
)

func TestLineTeeReportsWholeLinesAndFlushesPartial(t *testing.T) {
	var lines []string
	tee := NewLineTee(64*1024, func(s string) { lines = append(lines, s) })

	_, _ = tee.Write([]byte("one\ntwo\nthree"))
	tee.Flush()

	var got []string
	for _, batch := range lines {
		got = append(got, strings.Split(batch, "\n")...)
	}
	if len(got) != 3 || got[0] != "one" || got[1] != "two" || got[2] != "three" {
		t.Fatalf("lines = %v, want [one two three]", got)
	}
}

func TestLineTeeCapsOutput(t *testing.T) {
	tee := NewLineTee(8, nil)
	_, _ = tee.Write([]byte("1234567890"))
	if got := tee.Content(); !strings.Contains(got, "12345678") || !strings.Contains(got, "truncated at 8 bytes") {
		t.Fatalf("content = %q", got)
	}
}

func TestLineTeeNilSinkStaysByteIdentical(t *testing.T) {
	tee := NewLineTee(0, nil)
	_, _ = tee.Write([]byte("hello\nworld"))
	tee.Flush()
	if got := tee.Content(); got != "hello\nworld" {
		t.Fatalf("content = %q", got)
	}
}

// KeepTail keeps the start and the end of an over-cap stream, the end on a
// whole line, and says how much fell between them.
func TestLineTeeKeepTail(t *testing.T) {
	tee := NewLineTee(40, nil).KeepTail()
	for i := 0; i < 200; i++ {
		_, _ = tee.Write([]byte("line ok\n"))
	}
	_, _ = tee.Write([]byte("FAIL: boom\n"))
	got := tee.Content()
	if !strings.HasPrefix(got, "line ok\nline ok\n") {
		t.Fatalf("head lost: %q", got)
	}
	if !strings.HasSuffix(got, "FAIL: boom\n") {
		t.Fatalf("tail lost: %q", got)
	}
	if !strings.Contains(got, "bytes elided between the first 20 and the last") {
		t.Fatalf("no elision notice: %q", got)
	}
	tail := got[strings.Index(got, "…\n")+len("…\n"):]
	if !strings.HasPrefix(tail, "line ok\n") && !strings.HasPrefix(tail, "FAIL") {
		t.Fatalf("tail does not start on a whole line: %q", tail)
	}
}

// Under the cap, KeepTail output is byte-identical to the input.
func TestLineTeeKeepTailUnderCap(t *testing.T) {
	tee := NewLineTee(40, nil).KeepTail()
	_, _ = tee.Write([]byte("0123456789"))
	_, _ = tee.Write([]byte("abcdefghijklmnopqrst"))
	if got := tee.Content(); got != "0123456789abcdefghijklmnopqrst" {
		t.Fatalf("content = %q", got)
	}
}

// A command that prints a burst of lines and then waits (gh auth login prints
// its one-time code and polls) must still have those lines reported while it
// waits, not when it exits.
func TestLineTeeFlushesHeldBackLinesWhenOutputGoesQuiet(t *testing.T) {
	var mu sync.Mutex
	var got []string
	tee := NewLineTee(64*1024, func(s string) {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, strings.Split(s, "\n")...)
	})

	_, _ = tee.Write([]byte("first\n"))
	_, _ = tee.Write([]byte("code: F00E-9114\n"))
	_, _ = tee.Write([]byte("open https://github.com/login/device\n"))

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n == 3 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	t.Fatalf("lines before Flush = %v, want all 3", got)
}
