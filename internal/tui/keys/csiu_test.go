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
