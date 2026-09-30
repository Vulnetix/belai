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

// The composer's mark is a microphone drawn as a bitmap and printed in Braille.
// A terminal cell is about twice as tall as it is wide, and a Braille cell is
// two dots wide by four tall, so its dots sit on a square grid. Nine cells by
// three rows is therefore an 18 by 12 pixel picture, enough for a capsule, a
// holder, a pole, a base and two pairs of sound arcs that block glyphs cannot
// draw without falling apart. Every state is the same bitmap put through a
// small transform, so the shapes always agree.
const (
	voiceArtRows = 3
	voiceArtCols = 9
	voicePxW     = voiceArtCols * 2
	voicePxH     = voiceArtRows * 4
	voiceMicX    = 5 // the microphone is 8 pixels wide, centred in the 18
)

// voiceMicBitmap is the microphone, 8 by 12 pixels: a capsule with a rounded
// top and bottom, a U-shaped holder around it, a pole and a base.
var voiceMicBitmap = [voicePxH]string{
	"..####..",
	".######.",
	".######.",
	".######.",
	".######.",
	".######.",
	"#.####.#",
	"#.####.#",
	".#....#.",
	"..####..",
	"...##...",
	"..####..",
}

// voiceArcBitmaps are the sound arcs on the left, inner then outer, each two
// pixels wide. The right side is the mirror image.
var voiceArcBitmaps = [2]struct {
	x    int
	rows []string // starting at row y0
	y0   int
}{
	{x: 2, y0: 2, rows: []string{".#", "#.", "#.", "#.", "#.", "#.", "#.", ".#"}},
	{x: 0, y0: 1, rows: []string{".#", ".#", "#.", "#.", "#.", "#.", "#.", "#.", ".#", ".#"}},
}

// voicePixels is a picture of pixels, [row][column].
type voicePixels [voicePxH][voicePxW]bool

// voiceMicPixels draws the microphone into an empty picture: whole, or as an
// outline (only the pixels that touch an empty one), or as a soft dither
// (every other pixel).
func voiceMicPixels(outline, soft bool) voicePixels {
	var p voicePixels
	on := func(x, y int) bool {
		return y >= 0 && y < voicePxH && x >= 0 && x < len(voiceMicBitmap[y]) && voiceMicBitmap[y][x] == '#'
	}
	for y := 0; y < voicePxH; y++ {
		for x := 0; x < len(voiceMicBitmap[y]); x++ {
			if !on(x, y) {
				continue
			}
			if outline && on(x-1, y) && on(x+1, y) && on(x, y-1) && on(x, y+1) {
				continue // an interior pixel
			}
			if soft && (x+y)%2 != 0 {
				continue
			}
			p[y][voiceMicX+x] = true
		}
	}
	return p
}

// addArcs draws n sound arcs (0, 1 or 2) on each side of the microphone.
func (p *voicePixels) addArcs(n int) {
	for i := 0; i < n && i < len(voiceArcBitmaps); i++ {
		a := voiceArcBitmaps[i]
		for dy, row := range a.rows {
			for dx, c := range row {
				if c != '#' {
					continue
				}
				p[a.y0+dy][a.x+dx] = true
				p[a.y0+dy][voicePxW-1-(a.x+dx)] = true
			}
		}
	}
}

// addSlash draws a diagonal from the capsule's top left to the base's right.
func (p *voicePixels) addSlash() {
	x0, y0, x1, y1 := voiceMicX, 0, voiceMicX+7, voicePxH-1
	steps := y1 - y0
	for i := 0; i <= steps; i++ {
		x := x0 + (x1-x0)*i/steps
		p[y0+i][x] = true
		if x+1 < voicePxW {
			p[y0+i][x+1] = true
		}
	}
}

// voiceBraille prints a picture as Braille rows. An empty cell is a plain
// space, so a font that draws the blank Braille cell wider or narrower cannot
// disturb the layout.
func voiceBraille(p voicePixels) [voiceArtRows]string {
	// Dot bits by (column in cell, row in cell).
	bit := [2][4]rune{{0x01, 0x02, 0x04, 0x40}, {0x08, 0x10, 0x20, 0x80}}
	var out [voiceArtRows]string
	for cy := 0; cy < voiceArtRows; cy++ {
		row := make([]rune, voiceArtCols)
		for cx := 0; cx < voiceArtCols; cx++ {
			var bits rune
			for dx := 0; dx < 2; dx++ {
				for dy := 0; dy < 4; dy++ {
					if p[cy*4+dy][cx*2+dx] {
						bits |= bit[dx][dy]
					}
				}
			}
			if bits == 0 {
				row[cx] = ' '
			} else {
				row[cx] = 0x2800 + bits
			}
		}
		out[cy] = string(row)
	}
	return out
}

// voiceLookPixels is the picture for a look at a frame, with the colour to draw
// it in. Listening beats between a whole and a soft microphone, hearing adds one
// and then two sound arcs to each side, working is purple and beats the same way
// as listening, armed and paused are a still outline, and muted is that outline
// with a slash through it. The states differ in shape as well as colour, so they
// show on a terminal without colour.
func voiceLookPixels(look voiceLook, frame int) (p voicePixels, color lipgloss.TerminalColor, bold, ok bool) {
	switch look {
	case lookListening:
		soft := frame/4%2 == 1
		color, bold = components.ColorTeal, !soft
		if soft {
			color = components.ColorTealSoft
		}
		return voiceMicPixels(false, soft), color, bold, true
	case lookHearing:
		p = voiceMicPixels(false, false)
		p.addArcs(1 + frame%2)
		return p, components.ColorTeal, true, true
	case lookWorking:
		soft := frame/2%2 == 1
		return voiceMicPixels(false, soft), components.ColorVoice, !soft, true
	case lookMuted:
		p = voiceMicPixels(true, false)
		p.addSlash()
		return p, components.ColorLow, false, true
	case lookIdle, lookPaused:
		return voiceMicPixels(true, false), components.ColorLow, false, true
	}
	return p, nil, false, false
}

// voiceArtFor draws the microphone for a look at a frame.
func voiceArtFor(look voiceLook, frame int) [voiceArtRows]string {
	p, color, bold, ok := voiceLookPixels(look, frame)
	if !ok {
		return [voiceArtRows]string{}
	}
	st := lipgloss.NewStyle().Foreground(color).Bold(bold)
	rows := voiceBraille(p)
	var out [voiceArtRows]string
	for i, r := range rows {
		out[i] = st.Render(r)
	}
	return out
}

// overlayVoiceArt draws the mark flush right in the composer body, across its
// first three rows, level with the first line of text. It reports false,
// leaving the body alone, when the body is too small or when typed text
// reaches the mark's columns, so the mark never covers what you wrote; the
// caller then draws the small icon at the edge. The composer reserves the
// mark's width (voiceIconReserve), so text wraps before it.
func overlayVoiceArt(body string, inner int, art [voiceArtRows]string) (string, bool) {
	lines := strings.Split(body, "\n")
	if art[0] == "" || len(lines) < voiceArtRows || inner < voiceArtCols+4 {
		return body, false
	}
	left := inner - voiceArtCols
	for r := 0; r < voiceArtRows; r++ {
		if lipgloss.Width(strings.TrimRight(ansi.Strip(lines[r]), " ")) > left-1 {
			return body, false
		}
	}
	for r := 0; r < voiceArtRows; r++ {
		base := ansi.Truncate(lines[r], left, "")
		lines[r] = base + strings.Repeat(" ", left-lipgloss.Width(base)) + art[r]
	}
	return strings.Join(lines, "\n"), true
}
