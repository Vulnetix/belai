package inputhistory

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestLoadMissingFileIsEmpty(t *testing.T) {
	items, err := Load(filepath.Join(t.TempDir(), "missing.json"))
	if err != nil {
		t.Fatalf("missing file should not error, got %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("expected empty list, got %d", len(items))
	}
}

func TestRecordStoresAndLoads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.json")
	if err := Record(path, "!ls", time.Now().UnixMilli()); err != nil {
		t.Fatalf("record: %v", err)
	}
	items, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(items) != 1 || items[0].Text != "!ls" {
		t.Fatalf("expected one !ls item, got %+v", items)
	}
}

func TestRecordTrimsWhitespace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.json")
	if err := Record(path, "  \t", time.Now().UnixMilli()); err != nil {
		t.Fatalf("record blank: %v", err)
	}
	items, _ := Load(path)
	if len(items) != 0 {
		t.Fatalf("blank text must not be recorded, got %d", len(items))
	}
}

func TestRecordDedupesAndMovesToNewest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.json")
	_ = Record(path, "a", 1)
	_ = Record(path, "b", 2)
	_ = Record(path, "a", 3)

	items, _ := Load(path)
	if len(items) != 2 {
		t.Fatalf("expected 2 unique items, got %d", len(items))
	}
	if items[0].Text != "b" || items[1].Text != "a" {
		t.Fatalf("unexpected order: %+v", items)
	}
	if items[1].Timestamp != 3 {
		t.Fatalf("deduped item must keep newest timestamp, got %d", items[1].Timestamp)
	}
}

func TestRecordCapsAtMax(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.json")
	for i := 0; i < Max+5; i++ {
		_ = Record(path, fmt.Sprintf("line-%d", i), int64(i))
	}
	items, _ := Load(path)
	if len(items) != Max {
		t.Fatalf("expected Max (%d) items, got %d", Max, len(items))
	}
}

func TestNewestMergesNewestFirstAndDedupes(t *testing.T) {
	prompts := []Item{{Text: "hello", Timestamp: 2}, {Text: "world", Timestamp: 1}}
	local := []Item{{Text: "!pwd", Timestamp: 3}, {Text: "hello", Timestamp: 0}}
	got := Newest(prompts, local)
	want := []string{"!pwd", "hello", "world"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("index %d: got %q, want %q", i, got[i], want[i])
		}
	}
}

func TestNewestHandlesTiesAndEmpty(t *testing.T) {
	got := Newest([]Item{{Text: "a", Timestamp: 5}}, []Item{{Text: "b", Timestamp: 5}})
	if len(got) != 2 {
		t.Fatalf("expected 2 items for tie, got %v", got)
	}
	if got := Newest(nil, nil); len(got) != 0 {
		t.Fatalf("expected empty for nil lists, got %v", got)
	}
}

func TestPathUsesWorkdirKey(t *testing.T) {
	p1, err := Path("/foo")
	if err != nil {
		t.Fatalf("path: %v", err)
	}
	p2, err := Path("/bar")
	if err != nil {
		t.Fatalf("path: %v", err)
	}
	if p1 == p2 {
		t.Fatalf("different workdirs must have different paths: %q vs %q", p1, p2)
	}
	if !strings.Contains(p1, "inputhistory") {
		t.Fatalf("path should include inputhistory dir: %q", p1)
	}
}

func TestRecordTrimsText(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.json")
	if err := Record(path, "  /model \n", 1); err != nil {
		t.Fatal(err)
	}
	items, _ := Load(path)
	if want := []Item{{"/model", 1}}; !slices.Equal(items, want) {
		t.Fatalf("items = %v, want %v", items, want)
	}
}

func TestRecordReplacesUnreadableFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("Load of a corrupt file should error")
	}
	if err := Record(path, "!ls", 7); err != nil {
		t.Fatal(err)
	}
	items, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := []Item{{"!ls", 7}}; !slices.Equal(items, want) {
		t.Fatalf("items = %v, want %v", items, want)
	}
}

func TestRecordWritesPrivateFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.json")
	if err := Record(path, "!ls", 1); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600", fi.Mode().Perm())
	}
}

func TestPathIsPerProjectUnderGlobalDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("BELAI_HOME", home)
	a, err := Path("/work/a")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := Path("/work/b")
	if a == b {
		t.Fatalf("two workdirs share %s", a)
	}
	if filepath.Dir(a) != filepath.Join(home, "inputhistory") {
		t.Fatalf("path %s is not under %s/inputhistory", a, home)
	}
}
