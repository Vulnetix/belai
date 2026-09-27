package filediff

import "strings"

// WireRow is one diff row as it is written to the session JSONL: the op as a
// one-character sign ("+", "-", " ", or "…" for skipped context), the 1-based
// line numbers (zero where they do not apply), the text, and the intra-line
// emphasis spans as [from, to) cell pairs.
type WireRow struct {
	Op   string   `json:"op"`
	Old  int      `json:"o,omitempty"`
	New  int      `json:"n,omitempty"`
	Text string   `json:"t,omitempty"`
	Emph [][2]int `json:"e,omitempty"`
}

// WireFile is one file of a WireChange.
type WireFile struct {
	Path      string    `json:"path"`
	Created   bool      `json:"created,omitempty"`
	Deleted   bool      `json:"deleted,omitempty"`
	Binary    bool      `json:"binary,omitempty"`
	Truncated bool      `json:"truncated,omitempty"` // too large to diff at all
	Adds      int       `json:"adds"`
	Dels      int       `json:"dels"`
	Rows      []WireRow `json:"rows,omitempty"`
	// Clipped counts rows dropped to keep the whole change under its cap.
	Clipped int `json:"clipped,omitempty"`
}

// WireChange is a Change reduced to its rendered rows: what a reader needs to
// draw the diff (the website, a resumed session), without the two full file
// bodies a Change carries. It is harness-computed from the files on disk.
type WireChange struct {
	Files       []WireFile `json:"files,omitempty"`
	Unavailable string     `json:"unavailable,omitempty"`
}

// Wire reduces c to rows, keeping the encoded text under roughly capBytes.
// Past the cap a file keeps its header and counts and drops the rest of its
// rows (Clipped says how many); later files keep their headers too, so the
// reader always sees every path that changed.
func (c Change) Wire(capBytes int) WireChange {
	out := WireChange{Unavailable: c.Unavailable}
	budget := capBytes
	for _, fc := range c.Files {
		rows := fc.Rows()
		adds, dels := Stat(rows)
		wf := WireFile{
			Path: fc.Path, Created: fc.Created, Deleted: fc.Deleted,
			Binary: fc.Binary, Truncated: fc.Truncated, Adds: adds, Dels: dels,
		}
		budget -= len(fc.Path) + 64
		for i, r := range rows {
			cost := len(r.Text) + 24 + 12*len(r.Emph)
			if budget-cost < 0 {
				wf.Clipped = len(rows) - i
				budget = 0
				break
			}
			budget -= cost
			wf.Rows = append(wf.Rows, wireRow(r))
		}
		out.Files = append(out.Files, wf)
	}
	return out
}

func wireRow(r Row) WireRow {
	w := WireRow{Old: r.OldLine, New: r.NewLine, Text: r.Text}
	switch r.Op {
	case OpAdd:
		w.Op = "+"
	case OpDel:
		w.Op = "-"
	case OpElide:
		w.Op = "…"
		w.Text = ""
	default:
		w.Op = " "
	}
	for _, s := range r.Emph {
		w.Emph = append(w.Emph, [2]int{s.From, s.To})
	}
	return w
}

// Change rebuilds a renderable Change from its wire form. The file bodies are
// gone, so each FileChange carries its rows in Stored instead; Rows returns
// them as they were and nothing re-diffs.
func (w WireChange) Change() *Change {
	c := &Change{Unavailable: w.Unavailable}
	for _, wf := range w.Files {
		fc := FileChange{
			Path: wf.Path, Created: wf.Created, Deleted: wf.Deleted,
			Binary: wf.Binary, Truncated: wf.Truncated,
		}
		rows := make([]Row, 0, len(wf.Rows)+1)
		for _, wr := range wf.Rows {
			r := Row{OldLine: wr.Old, NewLine: wr.New, Text: wr.Text}
			switch wr.Op {
			case "+":
				r.Op = OpAdd
			case "-":
				r.Op = OpDel
			case "…":
				r.Op = OpElide
			default:
				r.Op = OpContext
			}
			for _, e := range wr.Emph {
				r.Emph = append(r.Emph, Span{From: e[0], To: e[1]})
			}
			rows = append(rows, r)
		}
		if wf.Clipped > 0 {
			rows = append(rows, Row{Op: OpElide})
		}
		fc.Stored = rows
		c.Files = append(c.Files, fc)
	}
	return c
}

// WireFromMeta decodes a WireChange from a session entry's meta value (the
// map a JSON round trip produces). It returns nil for anything malformed.
func WireFromMeta(v any) *WireChange {
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	w := &WireChange{}
	w.Unavailable, _ = m["unavailable"].(string)
	files, _ := m["files"].([]any)
	for _, f := range files {
		fm, ok := f.(map[string]any)
		if !ok {
			continue
		}
		wf := WireFile{}
		wf.Path, _ = fm["path"].(string)
		if strings.TrimSpace(wf.Path) == "" {
			continue
		}
		wf.Created, _ = fm["created"].(bool)
		wf.Deleted, _ = fm["deleted"].(bool)
		wf.Binary, _ = fm["binary"].(bool)
		wf.Truncated, _ = fm["truncated"].(bool)
		wf.Adds = wireInt(fm["adds"])
		wf.Dels = wireInt(fm["dels"])
		wf.Clipped = wireInt(fm["clipped"])
		rows, _ := fm["rows"].([]any)
		for _, r := range rows {
			rm, ok := r.(map[string]any)
			if !ok {
				continue
			}
			wr := WireRow{Old: wireInt(rm["o"]), New: wireInt(rm["n"])}
			wr.Op, _ = rm["op"].(string)
			wr.Text, _ = rm["t"].(string)
			emph, _ := rm["e"].([]any)
			for _, e := range emph {
				pair, ok := e.([]any)
				if ok && len(pair) == 2 {
					wr.Emph = append(wr.Emph, [2]int{wireInt(pair[0]), wireInt(pair[1])})
				}
			}
			wf.Rows = append(wf.Rows, wr)
		}
		w.Files = append(w.Files, wf)
	}
	if len(w.Files) == 0 && w.Unavailable == "" {
		return nil
	}
	return w
}

func wireInt(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	}
	return 0
}
