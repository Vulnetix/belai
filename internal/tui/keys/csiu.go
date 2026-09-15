// Package keys translates kitty keyboard-protocol CSI-u sequences into the
// legacy bubbletea v1 KeyMsg values used by Signet. It is implemented by hand
// because bubbletea v1.3 has no native keyboard-enhancement option.
package keys

import (
	"reflect"
	"regexp"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// Enter pushes progressive-enhancement flag 1 on the kitty keyboard stack.
const Enter = "\x1b[>1u"

// Pop removes the pushed enhancement flags.
const Pop = "\x1b[<u"

var csiuRe = regexp.MustCompile(`^\x1b\[(.*)u$`)

// DecodeParams parses a CSI-u parameter string into key code and modifier
// set. The optional modifiers field defaults to 1 (no modifiers) when absent.
func DecodeParams(seq []byte) (code int, mods int, ok bool) {
	m := csiuRe.FindSubmatch(seq)
	if m == nil {
		return 0, 0, false
	}
	parts := strings.Split(string(m[1]), ";")
	code, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, false
	}
	mods = 1
	if len(parts) > 1 {
		v, err := strconv.Atoi(parts[1])
		if err == nil {
			mods = v
		}
	}
	return code, mods, true
}

// Translate converts one kitty CSI-u byte sequence to a bubbletea KeyMsg.
// It returns false when the sequence is not a CSI-u event it can translate.
func Translate(seq []byte) (tea.KeyMsg, bool) {
	code, mods, ok := DecodeParams(seq)
	if !ok {
		return tea.KeyMsg{}, false
	}
	// Kitty encodes modifiers as a bitset whose lowest set bit means
	// "no modifiers", so true modifier state is shifted by one.
	m := mods - 1
	shift := m&1 != 0
	alt := m&2 != 0
	ctrl := m&4 != 0

	switch code {
	case 13:
		// Any modified enter is treated as newline; plain enter falls through
		// as well so the key binding sees the same submission signal.
		if ctrl || alt || shift {
			return tea.KeyMsg{Type: tea.KeyCtrlJ}, true
		}
		return tea.KeyMsg{Type: tea.KeyEnter}, true
	case 27:
		return tea.KeyMsg{Type: tea.KeyEscape}, true
	case 9:
		if shift {
			return tea.KeyMsg{Type: tea.KeyShiftTab}, true
		}
		return tea.KeyMsg{Type: tea.KeyTab}, true
	case 99:
		if ctrl {
			return tea.KeyMsg{Type: tea.KeyCtrlC}, true
		}
	case 100:
		if ctrl {
			return tea.KeyMsg{Type: tea.KeyCtrlD}, true
		}
	case 108:
		if ctrl {
			return tea.KeyMsg{Type: tea.KeyCtrlL}, true
		}
	case 106:
		if ctrl {
			return tea.KeyMsg{Type: tea.KeyCtrlJ}, true
		}
	case 127, 8:
		return tea.KeyMsg{Type: tea.KeyBackspace}, true
	}

	if code >= 'a' && code <= 'z' {
		r := rune(code)
		if ctrl && !shift {
			return tea.KeyMsg{Type: ctrlLetter(r)}, true
		}
		if shift {
			r = rune(code - 'a' + 'A')
		}
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}, Alt: alt}, true
	}

	// Pass through any other code as a plain rune.
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{rune(code)}, Alt: alt}, true
}

// ctrlLetter maps 'a'..'z' to tea's KeyCtrlA..KeyCtrlZ constants.
func ctrlLetter(r rune) tea.KeyType {
	return tea.KeyCtrlA + tea.KeyType(r-'a')
}

// Filter is a tea.WithFilter function that promotes CSI-u byte slices into
// KeyMsg values before the model sees them. It intentionally matches only
// on the []byte shape because unknownCSISequenceMsg is unexported in
// bubbletea v1; if that type ever changes this translator degrades safely by
// doing nothing.
func Filter(_ tea.Model, msg tea.Msg) tea.Msg {
	t := reflect.TypeOf(msg)
	if t == nil || t.Kind() != reflect.Slice || t.Elem().Kind() != reflect.Uint8 {
		return msg
	}
	if keyMsg, ok := Translate(msg.([]byte)); ok {
		return keyMsg
	}
	return msg
}
