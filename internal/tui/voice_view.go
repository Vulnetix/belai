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

// composerPlaceholder is what the empty composer shows. While voice runs it is
// shorter, so the round icon centred in the composer has room on an empty row.
func (a *App) composerPlaceholder() string {
	if a.voice.eng == nil {
		return components.DefaultPlaceholder
	}
	return "Type, or hold " + a.settings.Voice.VoiceKeyOr() + " and speak…"
}

// The composer's mark is a microphone: a capsule on a U-shaped holder with a
// pole and a base, three rows by nine cells, drawn with block and box glyphs (no
// emoji, per docs/tui-design.md). Cells are about twice as tall as they are
// wide, so the silhouette keeps its proportions. The two cells either side of
// the microphone are for sound arcs, which animate while speech is heard and
// are blank otherwise, so the width never changes.
const (
	voiceArtRows = 3
	voiceArtCols = 9
)

// The microphone itself is five cells wide, in the middle of the nine.
var (
	voiceMic     = [voiceArtRows]string{" ███ ", "█ █ █", " ▀█▀ "}
	voiceMicSoft = [voiceArtRows]string{" ▒▒▒ ", "▒ ▒ ▒", " ▀▒▀ "}
	voiceMicIdle = [voiceArtRows]string{" ░░░ ", "░ ░ ░", " ▀░▀ "}
)

// voiceArcs returns the left and right sound-arc columns for n arcs (0, 1 or
// 2): each arc is a bracket of box-drawing corners and a bar, three rows tall,
// so it reads as a curve open toward the microphone.
func voiceArcs(n int) (left, right [voiceArtRows]string) {
	l := [voiceArtRows]string{"╭", "│", "╰"}
	r := [voiceArtRows]string{"╮", "│", "╯"}
	for i := 0; i < voiceArtRows; i++ {
		switch n {
		case 0:
			left[i], right[i] = "  ", "  "
		case 1:
			left[i], right[i] = " "+l[i], r[i]+" "
		default:
			left[i], right[i] = l[i]+l[i], r[i]+r[i]
		}
	}
	return left, right
}

// voiceArtFor draws the microphone for a look at a frame. Listening pulses
// between a solid and a soft mic; hearing adds one and then two sound arcs to
// each side; working is a purple mic alternating solid and soft; muted is a
// light mic with a slash through it; waiting for the key or paused is a still
// light mic. The pulse changes shape as well as colour, so it shows on a
// terminal without colour.
func voiceArtFor(look voiceLook, frame int) [voiceArtRows]string {
	if look == lookNone {
		return [voiceArtRows]string{}
	}
	var (
		mic   = voiceMic
		color lipgloss.TerminalColor
		bold  bool
		arcs  int
		slash bool
	)
	switch look {
	case lookListening:
		color, bold = components.ColorTeal, true
		if frame/4%2 == 1 {
			mic, color, bold = voiceMicSoft, components.ColorTealSoft, false
		}
	case lookHearing:
		color, bold = components.ColorTeal, true
		arcs = 1 + frame%2
	case lookWorking:
		color, bold = components.ColorVoice, true
		if frame/2%2 == 1 {
			mic, bold = voiceMicSoft, false
		}
	case lookMuted:
		mic, color, slash = voiceMicIdle, components.ColorLow, true
	default: // idle, paused
		mic = voiceMicIdle
		color = components.ColorLow
	}
	left, right := voiceArcs(arcs)
	st := lipgloss.NewStyle().Foreground(color).Bold(bold)
	var out [voiceArtRows]string
	for i := 0; i < voiceArtRows; i++ {
		body := mic[i]
		if slash {
			// A slash from the capsule's top left to the base's right.
			b := []rune(body)
			b[i*2] = '╲'
			body = string(b)
		}
		out[i] = st.Render(left[i] + body + right[i])
	}
	return out
}

// overlayVoiceArt draws the mark centred in the composer body, across its
// first three rows. It reports false, leaving the body alone, when the body is
// too small or when typed text reaches the mark's columns, so the mark never
// covers what you wrote; the caller then draws the small icon at the edge.
func overlayVoiceArt(body string, inner int, art [voiceArtRows]string) (string, bool) {
	lines := strings.Split(body, "\n")
	if art[0] == "" || len(lines) < voiceArtRows || inner < voiceArtCols+4 {
		return body, false
	}
	left := (inner - voiceArtCols) / 2
	for r := 0; r < voiceArtRows; r++ {
		if lipgloss.Width(strings.TrimRight(ansi.Strip(lines[r]), " ")) > left-1 {
			return body, false
		}
	}
	right := inner - left - voiceArtCols
	for r := 0; r < voiceArtRows; r++ {
		base := ansi.Truncate(lines[r], left, "")
		lines[r] = base + strings.Repeat(" ", left-lipgloss.Width(base)) + art[r] + strings.Repeat(" ", right)
	}
	return strings.Join(lines, "\n"), true
}
