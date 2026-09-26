package keys

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestTranslateCSIu(t *testing.T) {
	tests := []struct {
		seq  []byte
		want tea.KeyMsg
		ok   bool
	}{
		{[]byte("\x1b[13u"), tea.KeyMsg{Type: tea.KeyEnter}, true},
		{[]byte("\x1b[13;1u"), tea.KeyMsg{Type: tea.KeyEnter}, true},
		{[]byte("\x1b[13;2u"), tea.KeyMsg{Type: tea.KeyCtrlJ}, true},
		{[]byte("\x1b[13;3u"), tea.KeyMsg{Type: tea.KeyCtrlJ}, true},
		{[]byte("\x1b[13;5u"), tea.KeyMsg{Type: tea.KeyCtrlJ}, true},
		{[]byte("\x1b[13;6u"), tea.KeyMsg{Type: tea.KeyCtrlJ}, true},
		{[]byte("\x1b[27u"), tea.KeyMsg{Type: tea.KeyEscape}, true},
		{[]byte("\x1b[9u"), tea.KeyMsg{Type: tea.KeyTab}, true},
		{[]byte("\x1b[9;2u"), tea.KeyMsg{Type: tea.KeyShiftTab}, true},
		{[]byte("\x1b[99;5u"), tea.KeyMsg{Type: tea.KeyCtrlC}, true},
		{[]byte("\x1b[100;5u"), tea.KeyMsg{Type: tea.KeyCtrlD}, true},
		{[]byte("\x1b[108;5u"), tea.KeyMsg{Type: tea.KeyCtrlL}, true},
		{[]byte("\x1b[106;5u"), tea.KeyMsg{Type: tea.KeyCtrlJ}, true},
		{[]byte("\x1b[127u"), tea.KeyMsg{Type: tea.KeyBackspace}, true},
		{[]byte("\x1b[97u"), tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}}, true},
		{[]byte("\x1b[65;2u"), tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'A'}}, true},
		{[]byte("\x1b[97;3u"), tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}, Alt: true}, true},
		{[]byte("\x1b[97;5u"), tea.KeyMsg{Type: tea.KeyCtrlA}, true},
		{[]byte("\x1b[Z"), tea.KeyMsg{}, false},
		{[]byte("plain"), tea.KeyMsg{}, false},
	}
	for _, tc := range tests {
		got, ok := Translate(tc.seq)
		if ok != tc.ok {
			t.Fatalf("Translate(%q) ok=%v want %v", tc.seq, ok, tc.ok)
		}
		if !tc.ok {
			continue
		}
		if got.Type != tc.want.Type {
			t.Fatalf("Translate(%q) Type=%v want %v", tc.seq, got.Type, tc.want.Type)
		}
		if len(got.Runes) != len(tc.want.Runes) {
			t.Fatalf("Translate(%q) Runes=%v want %v", tc.seq, got.Runes, tc.want.Runes)
		}
		for i := range got.Runes {
			if got.Runes[i] != tc.want.Runes[i] {
				t.Fatalf("Translate(%q) Runes=%v want %v", tc.seq, got.Runes, tc.want.Runes)
			}
		}
		if got.Alt != tc.want.Alt {
			t.Fatalf("Translate(%q) Alt=%v want %v", tc.seq, got.Alt, tc.want.Alt)
		}
	}
}

// TestCtrlAltCollapsesToCtrl pins the reason Belai binds no ctrl+alt chord.
// A ctrl-modified letter is translated onto the legacy tea.KeyCtrlA…KeyCtrlZ
// constants, and those constants have no alt bit to carry: modifier set 7
// (ctrl+alt) and modifier set 5 (ctrl) produce the identical KeyMsg, so
// `case "ctrl+alt+c"` in an Update switch can never match. Binding the four
// session toggles to f2…f5 instead is what makes them reachable.
func TestCtrlAltCollapsesToCtrl(t *testing.T) {
	// 99 is 'c'; ;5u is ctrl, ;7u is ctrl+alt.
	ctrlOnly, ok := Translate([]byte("\x1b[99;5u"))
	if !ok {
		t.Fatal("ctrl+c must translate")
	}
	ctrlAlt, ok := Translate([]byte("\x1b[99;7u"))
	if !ok {
		t.Fatal("ctrl+alt+c must translate")
	}
	if ctrlAlt.String() != ctrlOnly.String() {
		t.Fatalf("ctrl+alt+c = %q, ctrl+c = %q — if these ever differ, revisit the no-alt rule in docs/architecture.md", ctrlAlt.String(), ctrlOnly.String())
	}
	if ctrlAlt.String() != "ctrl+c" {
		t.Fatalf("ctrl+alt+c rendered as %q, want ctrl+c", ctrlAlt.String())
	}
}

// Function keys keep their legacy encodings under the enhancement flag Belai
// pushes (flag 1 only disambiguates keys that lack one), so they never reach
// Translate and are parsed by bubbletea itself. A CSI-u sequence that is not a
// key this translator knows must be refused rather than guessed at.
func TestTranslateRefusesNonCSIu(t *testing.T) {
	for _, seq := range [][]byte{
		[]byte("\x1bOQ"),    // F2, SS3 form
		[]byte("\x1b[15~"),  // F5, CSI ~ form
		[]byte("\x1b[1;2P"), // shift+F1
	} {
		if _, ok := Translate(seq); ok {
			t.Fatalf("Translate(%q) must refuse a non-CSI-u sequence", seq)
		}
	}
}

func TestFilterConvertsByteSlice(t *testing.T) {
	fake := []byte("\x1b[13;2u")
	got := Filter(nil, fake)
	k, ok := got.(tea.KeyMsg)
	if !ok || k.Type != tea.KeyCtrlJ {
		t.Fatalf("Filter returned %#v", got)
	}
}

func TestFilterPassesThroughKeyMsg(t *testing.T) {
	in := tea.KeyMsg{Type: tea.KeyEnter}
	got := Filter(nil, in)
	k, ok := got.(tea.KeyMsg)
	if !ok || k.Type != in.Type {
		t.Fatalf("Filter altered a KeyMsg")
	}
}
