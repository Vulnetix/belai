package tts

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func clip(n int, b byte) []byte { return bytes.Repeat([]byte{b}, n) }

func TestKeyCoversEverythingThatChangesTheAudio(t *testing.T) {
	a := Key("edge", "v1", "hello")
	if a == Key("edge", "v2", "hello") || a == Key("other", "v1", "hello") || a == Key("edge", "v1", "hello!") {
		t.Fatal("a differing input produced the same key")
	}
	if Key("ab", "c", "d") == Key("a", "bc", "d") {
		t.Fatal("parts run together")
	}
	if !keyRE.MatchString(a) {
		t.Fatalf("key %q is not a SHA-256 hex string", a)
	}
}

func TestCacheRoundTripAndPrivateDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "tts")
	c := NewCache(dir, 1)
	k := Key("e", "v", "t")
	if _, ok := c.Get(k); ok {
		t.Fatal("hit on an empty cache")
	}
	if err := c.Put(k, clip(2000, 7)); err != nil {
		t.Fatal(err)
	}
	got, ok := c.Get(k)
	if !ok || !bytes.Equal(got, clip(2000, 7)) {
		t.Fatalf("round trip = %d bytes, %v", len(got), ok)
	}
	info, err := os.Stat(dir)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("dir = %v, %v; want 0700", info, err)
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 1 || filepath.Ext(ents[0].Name()) != ".pcm" {
		t.Fatalf("leftover files: %v", ents)
	}
}

func TestCacheZeroMeansOff(t *testing.T) {
	dir := t.TempDir()
	c := NewCache(dir, 0)
	k := Key("e", "v", "t")
	if err := c.Put(k, clip(100, 1)); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.Get(k); ok {
		t.Fatal("a disabled cache returned a hit")
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 0 {
		t.Fatalf("a disabled cache wrote %v", ents)
	}
}

func TestCacheEvictsLeastRecentlyUsedAtTheCap(t *testing.T) {
	dir := t.TempDir()
	c := &Cache{Dir: dir, MaxBytes: 5000}
	k1, k2, k3 := Key("e", "v", "1"), Key("e", "v", "2"), Key("e", "v", "3")
	for i, k := range []string{k1, k2} {
		if err := c.Put(k, clip(2000, byte(i))); err != nil {
			t.Fatal(err)
		}
		old := time.Now().Add(time.Duration(i-10) * time.Minute)
		_ = os.Chtimes(filepath.Join(dir, k+".pcm"), old, old)
	}
	if _, ok := c.Get(k1); !ok { // k1 becomes the most recently used
		t.Fatal("k1 missing")
	}
	if err := c.Put(k3, clip(2000, 3)); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.Get(k2); ok {
		t.Fatal("the least recently used clip survived")
	}
	for _, k := range []string{k1, k3} {
		if _, ok := c.Get(k); !ok {
			t.Fatalf("%s was evicted", k[:6])
		}
	}
	if c.Size() > c.MaxBytes {
		t.Fatalf("size %d over the cap %d", c.Size(), c.MaxBytes)
	}
}

func TestCacheRefusesAClipLargerThanTheCapAndBadKeys(t *testing.T) {
	c := &Cache{Dir: t.TempDir(), MaxBytes: 1000}
	if err := c.Put(Key("e", "v", "big"), clip(2000, 1)); err != nil || c.Size() != 0 {
		t.Fatalf("stored an oversized clip: %v size %d", err, c.Size())
	}
	for _, bad := range []string{"", "../../etc/passwd", "abc", Key("e", "v", "t") + "/x"} {
		if err := c.Put(bad, clip(10, 1)); err != nil {
			t.Fatal(err)
		}
		if _, ok := c.Get(bad); ok {
			t.Fatalf("key %q was accepted", bad)
		}
	}
	if c.Size() != 0 {
		t.Fatal("a bad key wrote a file")
	}
}

func TestCacheClearOnlyRemovesClips(t *testing.T) {
	dir := t.TempDir()
	c := NewCache(dir, 1)
	_ = c.Put(Key("e", "v", "t"), clip(100, 1))
	other := filepath.Join(dir, "notes.txt")
	_ = os.WriteFile(other, []byte("keep"), 0o600)
	if err := c.Clear(); err != nil {
		t.Fatal(err)
	}
	if c.Size() != 0 {
		t.Fatal("clips remain")
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatal("Clear removed a file that is not a clip")
	}
}

func TestCacheIgnoresACorruptClip(t *testing.T) {
	dir := t.TempDir()
	c := NewCache(dir, 1)
	k := Key("e", "v", "t")
	_ = os.WriteFile(filepath.Join(dir, k+".pcm"), []byte{1, 2, 3}, 0o600) // odd length
	if _, ok := c.Get(k); ok {
		t.Fatal("an odd-length clip was returned")
	}
}
