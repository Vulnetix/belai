package tui

import (
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/vulnetix/belai/internal/tui/components"
	"github.com/vulnetix/belai/internal/voice"
)

// How voice shows on the chat composer: a circle icon at the right edge of
// the first row, and the frame itself. The icon pulses while the microphone
// is open and is a still, dim mark when voice is muted, paused or waiting for
// the key. The frame's rules ripple slowly while speech is heard and turn a
// pastel purple while the speech model or the fast model is working.

// voiceAnimInterval is one animation frame. Everything below counts in frames.
const voiceAnimInterval = 140 * time.Millisecond

// voiceIconWidth is the room the icon takes: a space and one cell.
const voiceIconWidth = 2

type voiceAnimMsg struct{}

type voiceLook int

const (
	lookNone      voiceLook = iota // voice is off: nothing is drawn
	lookIdle                       // push to talk, waiting for the key
	lookMuted                      // listen mode, silenced with the key
	lookPaused                     // the composer cannot take text, or the model is loading
	lookListening                  // the microphone is open, no speech yet
	lookHearing                    // speech is being heard
	lookWorking                    // recognising or tidying what was said
)

// voiceLook classifies what voice is doing for drawing purposes.
func (a *App) voiceLook() voiceLook {
	v := &a.voice
	if v.eng == nil {
		return lookNone
	}
	if v.muted {
		return lookMuted
	}
	if v.busy {
		return lookWorking
	}
	switch v.eng.State() {
	case voice.StateLoading, voice.StatePaused:
		return lookPaused
	case voice.StateIdle:
		return lookIdle
	case voice.StateListening:
		return lookListening
	case voice.StateHearing:
		return lookHearing
	case voice.StateTranscribing:
		return lookWorking
	}
	return lookNone
}

// voiceAnimating reports whether something on screen moves right now.
func (a *App) voiceAnimating() bool {
	switch a.voiceLook() {
	case lookListening, lookHearing, lookWorking:
		return true
	}
	return false
}

func voiceAnimTick() tea.Cmd {
	return tea.Tick(voiceAnimInterval, func(time.Time) tea.Msg { return voiceAnimMsg{} })
}

// voiceIconFor draws the icon for a look at a frame. The same look and frame
// always draw the same icon.
func voiceIconFor(look voiceLook, frame int) string {
	switch look {
	case lookListening:
		// A slow pulse: about a second a breath.
		if frame/4%2 == 0 {
			return lipgloss.NewStyle().Foreground(components.ColorTeal).Bold(true).Render("◉")
		}
		return lipgloss.NewStyle().Foreground(components.ColorTealSoft).Render("◎")
	case lookHearing:
		if frame%2 == 0 {
			return lipgloss.NewStyle().Foreground(components.ColorTeal).Bold(true).Render("◉")
		}
		return lipgloss.NewStyle().Foreground(components.ColorTealSoft).Render("●")
	case lookWorking:
		if frame/2%2 == 0 {
			return lipgloss.NewStyle().Foreground(components.ColorVoice).Bold(true).Render("◉")
		}
		return lipgloss.NewStyle().Foreground(components.ColorVoice).Render("◌")
	case lookPaused:
		return components.LowStyle.Render("◌")
	case lookIdle, lookMuted:
		return components.LowStyle.Render("⊘")
	}
	return ""
}

// voiceFrame is the composer frame's look: its colour and its ripple.
func (a *App) voiceFrame(accent lipgloss.TerminalColor) (lipgloss.TerminalColor, bool, int) {
	switch a.voiceLook() {
	case lookHearing:
		// Two frames per step keeps the ripple slow.
		return accent, true, a.voice.frame / 2
	case lookWorking:
		return lipgloss.TerminalColor(components.ColorVoice), false, 0
	}
	return accent, false, 0
}

// voiceIconReserve is how many columns the composer's text gives up so the
// icon never sits on typed text. It is fixed while voice runs, so the text
// does not rewrap as the icon changes.
func (a *App) voiceIconReserve() int {
	if a.voice.eng == nil {
		return 0
	}
	return voiceIconWidth
}

// overlayVoiceIcon puts the icon at the right edge of the composer body's
// first row. inner is the body width.
func overlayVoiceIcon(body string, inner int, icon string) string {
	if icon == "" || inner <= voiceIconWidth {
		return body
	}
	lines := strings.Split(body, "\n")
	first := lines[0]
	room := inner - voiceIconWidth
	if lipgloss.Width(first) > room {
		first = ansi.Truncate(first, room, "")
	}
	pad := room - lipgloss.Width(first)
	lines[0] = first + strings.Repeat(" ", pad) + " " + icon
	return strings.Join(lines, "\n")
}
