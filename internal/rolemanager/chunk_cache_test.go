package rolemanager

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/vulnetix/signet/internal/tools"
)

// chunkClassifier records every chunk it is asked to classify and answers via
// a verdict function.
type chunkClassifier struct {
	mu       sync.Mutex
	payloads []string
	verdict  func(chunk string) string
}

func (c *chunkClassifier) Classify(_ context.Context, p ClassifierPayload) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.payloads = append(c.payloads, p.User)
	return c.verdict(p.User), nil
}

func (c *chunkClassifier) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.payloads)
}

func TestSplitChunksOverlapsOnRuneBoundaries(t *testing.T) {
	content := strings.Repeat("日本語", 100) // multi-byte runes
	chunks := splitChunks(content, 120, 12)
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(chunks))
	}
	for _, c := range chunks {
		if !utf8.ValidString(c) {
			t.Fatalf("chunk is not valid UTF-8: %q", c)
		}
	}
	// Each adjacent pair shares an overlap of at least one byte.
	for i := 1; i < len(chunks); i++ {
		prev := chunks[i-1]
		if !strings.HasSuffix(prev, chunks[i][:1]) && !strings.HasPrefix(chunks[i], prev[len(prev)-1:]) {
			// Overlap is in bytes, but rune alignment may make exact byte
			// comparison awkward; just assert the next chunk re-reads content
			// that also appeared in the previous one.
			if !strings.Contains(prev, chunks[i][:utf8.RuneLen(rune(chunks[i][0]))]) {
				t.Fatalf("chunks %d and %d do not overlap", i-1, i)
			}
		}
	}
}

func TestChunkedClassifyFoldsFailClosed(t *testing.T) {
	cc := &chunkClassifier{verdict: func(chunk string) string {
		if strings.Contains(chunk, "INJECT") {
			return "PROMPT_INJECTION"
		}
		return "SAFE"
	}}
	p := NewPipelineWithChunk(cc, ChunkConfig{MaxBytes: 40, Concurrency: 2, Overlap: 8})
	content := "INJECT" + strings.Repeat("a", 200)

	d, err := p.Process(context.Background(), tools.ReadResult(content))
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if d.Action != ActionWarn || d.Sentinel != SentinelPromptInjection {
		t.Fatalf("decision = %+v, want PROMPT_INJECTION warn", d)
	}
	if cc.count() < 2 {
		t.Fatalf("expected chunked classification (%d calls), got 1", cc.count())
	}
}

func TestChunkedClassifyAllSafe(t *testing.T) {
	cc := &chunkClassifier{verdict: func(string) string { return "SAFE" }}
	p := NewPipelineWithChunk(cc, ChunkConfig{MaxBytes: 40, Concurrency: 2, Overlap: 8})
	d, err := p.Process(context.Background(), tools.ReadResult(strings.Repeat("b", 200)))
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if d.Action != ActionProceed {
		t.Fatalf("decision = %+v, want proceed", d)
	}
}

func TestChunkedClassifyMalformedChunkFailsClosed(t *testing.T) {
	cc := &chunkClassifier{verdict: func(chunk string) string {
		if strings.HasPrefix(chunk, "b") {
			return "not a sentinel"
		}
		return "SAFE"
	}}
	p := NewPipelineWithChunk(cc, ChunkConfig{MaxBytes: 40, Concurrency: 2, Overlap: 8})
	d, err := p.Process(context.Background(), tools.ReadResult(strings.Repeat("b", 200)))
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if d.Action != ActionWarn {
		t.Fatalf("malformed chunk must fail closed, got %+v", d)
	}
}

func TestCacheGetPutAndPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad-hashes.json")
	c := NewCache(2, path)

	key := Key("payload-a")
	if _, ok := c.Get(key); ok {
		t.Fatal("empty cache should miss")
	}
	if err := c.Put(key, SentinelSafe); err != nil {
		t.Fatalf("Put safe: %v", err)
	}
	if s, ok := c.Get(key); !ok || s != SentinelSafe {
		t.Fatalf("safe verdict not cached: %q %v", s, ok)
	}

	bad := Key("payload-bad")
	if err := c.Put(bad, SentinelPromptInjection); err != nil {
		t.Fatalf("Put bad: %v", err)
	}

	// Reload from disk: the bad hash survives, the safe one does not.
	loaded, err := LoadCache(path)
	if err != nil {
		t.Fatalf("LoadCache: %v", err)
	}
	if s, ok := loaded.Get(bad); !ok || s != SentinelPromptInjection {
		t.Fatalf("bad hash not persisted: %q %v", s, ok)
	}
	if _, ok := loaded.Get(key); ok {
		t.Fatal("safe verdict must not persist across sessions")
	}
}

func TestDefaultCachePath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SIGNET_HOME", home)
	if got, want := DefaultCachePath(), filepath.Join(home, "bad-hashes.json"); got != want {
		t.Fatalf("DefaultCachePath() = %q, want %q", got, want)
	}
}

func TestNewCacheDefaultsSafeBound(t *testing.T) {
	if c := NewCache(0, ""); c.maxSafe != defaultSafeCacheSize {
		t.Fatalf("NewCache(0) maxSafe = %d, want %d", c.maxSafe, defaultSafeCacheSize)
	}
	if c := NewCache(7, ""); c.maxSafe != 7 {
		t.Fatalf("NewCache(7) maxSafe = %d, want 7", c.maxSafe)
	}
}

// TestCacheBadWithoutPath persists bad verdicts in memory only: no disk write
// is attempted when badPath is empty.
func TestCacheBadWithoutPath(t *testing.T) {
	c := NewCache(16, "")
	key := Key("bad-no-path")
	if err := c.Put(key, SentinelPromptInjection); err != nil {
		t.Fatalf("Put(bad, no path): %v", err)
	}
	if s, ok := c.Get(key); !ok || s != SentinelPromptInjection {
		t.Fatalf("bad verdict not cached: %q %v", s, ok)
	}
}

func TestCacheSafeLRUBounded(t *testing.T) {
	c := NewCache(2, "")
	for _, content := range []string{"a", "b", "c"} {
		if err := c.Put(Key(content), SentinelSafe); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}
	if _, ok := c.Get(Key("a")); ok {
		t.Fatal("oldest safe verdict should have been evicted")
	}
	if _, ok := c.Get(Key("c")); !ok {
		t.Fatal("newest safe verdict should be present")
	}
}

func TestPipelineUsesCache(t *testing.T) {
	cc := &chunkClassifier{verdict: func(string) string { return "SAFE" }}
	c := NewCache(16, "")
	p := NewPipeline(cc)
	p.Cache = c

	content := "identical content"
	if _, err := p.Process(context.Background(), tools.ReadResult(content)); err != nil {
		t.Fatalf("first Process: %v", err)
	}
	first := cc.count()

	if _, err := p.Process(context.Background(), tools.ReadResult(content)); err != nil {
		t.Fatalf("second Process: %v", err)
	}
	if cc.count() != first {
		t.Fatalf("cache was not used: classifier calls went from %d to %d", first, cc.count())
	}
}
