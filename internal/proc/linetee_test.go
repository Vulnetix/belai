package proc

import (
	"strings"
	"testing"
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
