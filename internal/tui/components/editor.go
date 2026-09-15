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
	ta.KeyMap.InsertNewline = key.NewBinding(
		key.WithKeys("ctrl+j", "alt+enter"),
		key.WithHelp("ctrl+j", "newline"),
	)
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

// Update forwards a message to the textarea.
func (e *Editor) Update(msg tea.Msg) tea.Cmd {
	m, cmd := e.textarea.Update(msg)
	e.textarea = m
	return cmd
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
