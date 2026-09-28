package offload

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
)

func testLog(n int) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "line %04d ok\n", i)
	}
	b.WriteString("FAIL TestLast (0.01s)\n")
	return b.String()
}

// A result under the threshold is left alone and nothing is stored.
func TestOffloadBelowThresholdIsUnchanged(t *testing.T) {
	s := NewStore()
	got, ok := s.Offload("Bash", "short", 100, 50)
	if ok || got != "short" {
		t.Fatalf("got %q ok=%v", got, ok)
	}
	if _, found := s.Get("r1"); found {
		t.Fatal("stored a result that was not offloaded")
	}
}

// The preview keeps whole head and tail lines — so a test failure printed
// last survives — names the reference, and is deterministic.
func TestOffloadPreviewKeepsHeadAndTail(t *testing.T) {
	content := testLog(2000)
	s1, s2 := NewStore(), NewStore()
	p1, ok := s1.Offload("Bash", content, 1000, 300)
	if !ok {
		t.Fatal("not offloaded")
	}
	p2, _ := s2.Offload("Bash", content, 1000, 300)
	if p1 != p2 {
		t.Fatal("preview is not deterministic")
	}
	if !strings.HasPrefix(p1, "line 0001 ok\n") {
		t.Fatalf("head lost: %q", p1[:40])
	}
	if !strings.Contains(p1, "FAIL TestLast (0.01s)\n") {
		t.Fatal("tail (the failure) lost")
	}
	if !strings.Contains(p1, `ReadResult(ref="r1")`) || !strings.Contains(p1, "lines elided") {
		t.Fatalf("trailer missing: %s", p1[len(p1)-300:])
	}
	for _, l := range strings.Split(p1, "\n") {
		if strings.HasPrefix(l, "line ") && !strings.HasSuffix(l, " ok") {
			t.Fatalf("partial line in preview: %q", l)
		}
	}
	if Tokens(p1) > 300+120 {
		t.Fatalf("preview too large: ~%d tokens", Tokens(p1))
	}
	full, found := s1.Get("r1")
	if !found || full != content {
		t.Fatal("stored content differs from the original")
	}
}

// A single enormous line is still cut, inside the line.
func TestOffloadSingleLine(t *testing.T) {
	s := NewStore()
	p, ok := s.Offload("WebFetch", strings.Repeat("x", 100000), 1000, 200)
	if !ok || Tokens(p) > 400 {
		t.Fatalf("ok=%v size=%d", ok, Tokens(p))
	}
}

// The store evicts oldest first past its cap, and an evicted or unknown
// reference answers not-found rather than another result.
func TestStoreEvictsOldest(t *testing.T) {
	s := NewStore()
	big := strings.Repeat("a\n", maxPutBytes/2) // stored at the per-result cap
	n := MaxStoreBytes/maxPutBytes + 1
	for range n {
		s.Offload("Bash", big, 10, 5)
	}
	if _, ok := s.Get("r1"); ok {
		t.Fatal("oldest result not evicted")
	}
	if _, ok := s.Get(fmt.Sprintf("r%d", n)); !ok {
		t.Fatal("newest result evicted")
	}
	if _, ok := s.Get("../r3"); ok {
		t.Fatal("unknown reference resolved")
	}
}

func TestSliceWindowAndPattern(t *testing.T) {
	content := testLog(100)
	w := Slice(content, nil, 10, 3, 0, 1<<20)
	if !strings.HasPrefix(w, "10\tline 0010 ok\n11\t") || !strings.Contains(w, "continue with offset=13") {
		t.Fatalf("window: %q", w)
	}
	end := Slice(content, nil, 100, 0, 0, 1<<20)
	if !strings.Contains(end, "101\tFAIL TestLast") || !strings.Contains(end, "end of result") {
		t.Fatalf("end window: %q", end)
	}
	m := Slice(content, regexp.MustCompile(`FAIL`), 0, 0, 1, 1<<20)
	if !strings.Contains(m, "100\tline 0100 ok\n101\tFAIL") || !strings.Contains(m, "1 matching lines of 101") {
		t.Fatalf("pattern: %q", m)
	}
	none := Slice(content, regexp.MustCompile(`nomatch`), 0, 0, 1, 1<<20)
	if !strings.Contains(none, "no lines match") {
		t.Fatalf("no match: %q", none)
	}
	capped := Slice(content, nil, 1, 0, 0, 100)
	if !strings.Contains(capped, "continue with offset=") {
		t.Fatalf("cap: %q", capped)
	}
	past := Slice(content, nil, 500, 0, 0, 100)
	if !strings.Contains(past, "past the end") {
		t.Fatalf("past: %q", past)
	}
}

// A single huge line keeps both of its ends and counts characters, not lines.
func TestOffloadSingleLineKeepsBothEnds(t *testing.T) {
	s := NewStore()
	p, ok := s.Offload("WebFetch", "START "+strings.Repeat("x", 100000)+" END", 1000, 200)
	if !ok || !strings.HasPrefix(p, "START ") || !strings.Contains(p, " END\n") || !strings.Contains(p, "characters elided") {
		t.Fatalf("ok=%v preview=%q", ok, p)
	}
	if strings.Contains(p, "lines elided") {
		t.Fatal("a single line was reported as elided lines")
	}
}

// Slice clips a huge line, so reading back a one-line result stays bounded.
func TestSliceClipsLongLines(t *testing.T) {
	got := Slice(strings.Repeat("y", 50000), nil, 1, 10, 0, 1<<20)
	if len(got) > maxSliceLine+200 || !strings.Contains(got, "[line clipped]") {
		t.Fatalf("len=%d", len(got))
	}
}
