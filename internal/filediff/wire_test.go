package filediff

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestWireRoundTrip(t *testing.T) {
	ch := Preview("main.go", "a\nb\nc\n", "a\nB\nc\nd\n")
	w := ch.Wire(1 << 20)
	if len(w.Files) != 1 || w.Files[0].Adds == 0 || w.Files[0].Dels == 0 {
		t.Fatalf("wire = %+v", w)
	}
	raw, err := json.Marshal(w)
	if err != nil {
		t.Fatal(err)
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	back := WireFromMeta(decoded)
	if back == nil {
		t.Fatal("WireFromMeta returned nil")
	}
	restored := back.Change()
	want := ch.Files[0].Rows()
	got := restored.Files[0].Rows()
	if len(got) != len(want) {
		t.Fatalf("rows = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Op != want[i].Op || got[i].Text != want[i].Text || got[i].OldLine != want[i].OldLine ||
			got[i].NewLine != want[i].NewLine || len(got[i].Emph) != len(want[i].Emph) {
			t.Fatalf("row %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// Past the cap a file keeps its header and counts; its rows stop and the
// dropped count is reported, and later files still list their paths.
func TestWireCapKeepsEveryPath(t *testing.T) {
	big := strings.Repeat("line of text that is long enough\n", 400)
	ch := Change{Files: []FileChange{
		{Path: "big.txt", Old: "", New: big, Created: true},
		{Path: "small.txt", Old: "x\n", New: "y\n"},
	}}
	w := ch.Wire(2 << 10)
	if len(w.Files) != 2 || w.Files[1].Path != "small.txt" {
		t.Fatalf("files = %+v", w.Files)
	}
	if w.Files[0].Clipped == 0 || w.Files[0].Adds != 400 {
		t.Fatalf("big file = clipped %d adds %d", w.Files[0].Clipped, w.Files[0].Adds)
	}
	raw, _ := json.Marshal(w)
	if len(raw) > 4<<10 {
		t.Fatalf("encoded %d bytes, cap was 2 KiB", len(raw))
	}
	// The restored change marks the clip with an elision row.
	rows := w.Change().Files[0].Rows()
	if rows[len(rows)-1].Op != OpElide {
		t.Fatal("clipped file does not end in an elision")
	}
}

func TestWireFromMetaRejectsJunk(t *testing.T) {
	for _, v := range []any{nil, "x", 3, map[string]any{}, map[string]any{"files": []any{map[string]any{"path": ""}}}} {
		if WireFromMeta(v) != nil {
			t.Fatalf("WireFromMeta(%v) != nil", v)
		}
	}
}
