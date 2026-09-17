package components

import (
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Editor wraps a textarea for slash-command and message input.
type Editor struct {
	textarea textarea.Model
	Masked   bool
}

// NewEditor returns a focused input editor.
func NewEditor() Editor {
	ta := textarea.New()
	ta.Placeholder = "Type / for commands, or ask Signet anything…"
	ta.ShowLineNumbers = false
	ta.SetHeight(3)
	ta.SetWidth(80)
	// The composer frame supplies the chrome: drop the textarea's own gutter
	// prompt and cursor-line fill so the input reads as one clean field.
	ta.Prompt = ""
	ta.FocusedStyle.CursorLine = lipgloss.NewStyle()
	ta.FocusedStyle.Base = lipgloss.NewStyle()
	ta.FocusedStyle.Placeholder = MutedStyle
	ta.FocusedStyle.Text = lipgloss.NewStyle().Foreground(ColorCream)
	ta.BlurredStyle.CursorLine = lipgloss.NewStyle()
	ta.BlurredStyle.Base = lipgloss.NewStyle()
	ta.BlurredStyle.Placeholder = MutedStyle
	ta.BlurredStyle.Text = MutedStyle
	// Newline is ctrl+j. shift+enter reaches it two ways, both handled in
	// Update: under the kitty protocol the CSI-u translator folds every
	// modified enter onto ctrl+j, and without it the terminal sends ESC+CR,
	// which decodes as enter carrying the alt flag. Neither is an alt chord a
	// user types, so no alt keycap is bound here.
	ta.KeyMap.InsertNewline = key.NewBinding(
		key.WithKeys("ctrl+j"),
		key.WithHelp("ctrl+j", "newline"),
	)
	// The textarea's own word motion is bound to alt chords that never arrive,
	// and its boundaries are whitespace-only — coarser than the motion Editor
	// implements for ctrl+left/ctrl+right. Clear both so there is exactly one
	// word-motion behaviour rather than two that could disagree.
	ta.KeyMap.WordForward = key.NewBinding()
	ta.KeyMap.WordBackward = key.NewBinding()
	return Editor{textarea: ta}
}

// Focus focuses the editor.
func (e *Editor) Focus() tea.Cmd { return e.textarea.Focus() }

// Blur blurs the editor.
func (e *Editor) Blur() { e.textarea.Blur() }

// SetValue replaces the editor contents.
func (e *Editor) SetValue(s string) { e.textarea.SetValue(s) }

// CursorEnd moves the cursor to the end of the text.
func (e *Editor) CursorEnd() { e.textarea.CursorEnd() }

// Reset clears the editor.
func (e *Editor) Reset() { e.textarea.Reset() }

// SetWidth sets the editor width.
func (e *Editor) SetWidth(w int) { e.textarea.SetWidth(w) }

// Update forwards a message to the textarea, after handling the motions the
// textarea does not bind itself. Word motion is intercepted here rather than
// in the App key switch so that every text-entry context gets it — the
// composer, the credential fields, the agent editor — instead of only chat.
func (e *Editor) Update(msg tea.Msg) tea.Cmd {
	// A blurred textarea ignores every key, so word motion must ignore them
	// too — otherwise these two keys would move a cursor nothing else can.
	//
	// The match is on the key *type*, not on String(): several terminals send
	// ctrl+arrow in a form bubbletea decodes with the alt flag set (urxvt's
	// \x1b[Od, xterm's \x1b[1;7D), which String() would render as an alt
	// chord and no case would catch. The type is the same either way, and
	// Signet treats a stray alt bit on these two keys as noise.
	if k, ok := msg.(tea.KeyMsg); ok && e.textarea.Focused() {
		switch k.Type {
		case tea.KeyCtrlLeft:
			e.WordLeft()
			return nil
		case tea.KeyCtrlRight:
			e.WordRight()
			return nil
		case tea.KeyEnter:
			// bubbletea has no shift+enter key type. A terminal without the
			// kitty protocol sends shift+enter as ESC+CR, which decodes as
			// enter with the alt flag set — a terminal encoding, not a chord
			// anyone presses, so it is accepted here rather than bound as an
			// alt keycap. Plain enter is left alone: it is the submit key, and
			// the App handles it before the composer ever sees it.
			if k.Alt {
				msg = tea.KeyMsg{Type: tea.KeyCtrlJ}
			}
		}
	}
	m, cmd := e.textarea.Update(msg)
	e.textarea = m
	return cmd
}

// cursor returns the editor's logical lines plus the cursor's logical row and
// column. The textarea tracks the column against the soft-wrapped grid, so the
// logical column has to be rebuilt from the line info: StartColumn is where the
// current visual row begins and ColumnOffset is the distance into it.
func (e *Editor) cursor() (lines [][]rune, row, col int) {
	for _, l := range strings.Split(e.textarea.Value(), "\n") {
		lines = append(lines, []rune(l))
	}
	row = e.textarea.Line()
	if row < 0 || row >= len(lines) {
		return lines, 0, 0
	}
	li := e.textarea.LineInfo()
	col = li.StartColumn + li.ColumnOffset
	if col > len(lines[row]) {
		col = len(lines[row])
	}
	return lines, row, col
}

// WordLeft moves the cursor back one word. At the start of a line it steps to
// the end of the previous line, so holding the key walks the whole prompt
// rather than stalling at a line break; on the first line it stays put.
func (e *Editor) WordLeft() {
	lines, row, col := e.cursor()
	if col > 0 {
		e.textarea.SetCursor(wordLeft(lines[row], col))
		return
	}
	if row > 0 {
		e.textarea.CursorUp()
		e.textarea.CursorEnd()
	}
}

// WordRight moves the cursor forward one word, stepping to the start of the
// next line at the end of a line and staying put on the last one.
func (e *Editor) WordRight() {
	lines, row, col := e.cursor()
	if col < len(lines[row]) {
		e.textarea.SetCursor(wordRight(lines[row], col))
		return
	}
	if row < len(lines)-1 {
		e.textarea.CursorDown()
		e.textarea.CursorStart()
	}
}

// SetHeight sets the editor height.
func (e *Editor) SetHeight(h int) { e.textarea.SetHeight(h) }

// Height returns the editor height.
func (e Editor) Height() int { return e.textarea.Height() }

// LineCount returns the number of logical lines in the editor value.
func (e Editor) LineCount() int { return e.textarea.LineCount() }

// Value returns the current editor contents.
func (e Editor) Value() string { return e.textarea.Value() }

// View renders the editor.
func (e Editor) View() string {
	if e.Masked {
		v := e.Value()
		if v != "" {
			return strings.Repeat("•", len(v))
		}
	}
	return e.textarea.View()
}

// Width returns the editor width.
func (e Editor) Width() int { return e.textarea.Width() }
