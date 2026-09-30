package components

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// PlayerRole is the transcript role of the read-aloud player card. It is
// render-only: the card is drawn from Message.Player, which the TUI refreshes
// as playback moves, it never produces a session entry, and buildTurns never
// promotes it, so no model sees it. Audio never enters a Message.
const PlayerRole = "player"

// Click actions a player card's hit regions carry.
const (
	PlayerToggle = "toggle"
	PlayerStop   = "stop"
	PlayerBack   = "back"
	PlayerFwd    = "fwd"
	PlayerSeek   = "seek"
	PlayerSpeed  = "speed:" // followed by the speed, such as "speed:1.5"
)

// PlayerSpeeds are the speed chips, in order.
var PlayerSpeeds = []float64{0.75, 1, 1.25, 1.5, 2}

// PlayerSkip is how far the back and forward buttons move.
const PlayerSkip = 10 * time.Second

// PlayerCard is everything the card draws. The TUI fills it from the player's
// snapshot on every animation tick.
type PlayerCard struct {
	Title  string // the first words of what is being read
	State  string // buffering, playing, paused, stopped, ended, failed
	Pos    time.Duration
	Dur    time.Duration
	Final  bool // Dur will not grow
	Speed  float64
	Levels []float64 // 0 to 1, oldest first
	Status string    // synthesising 2 of 5, from cache, an error
	Frame  int       // animation frame, for the buffering spinner
}

// Key is what makes two cards draw differently. Levels are quantised to the
// eight bar heights so a tick that changes nothing visible keeps the cache.
func (c *PlayerCard) Key() string {
	if c == nil {
		return ""
	}
	var lv strings.Builder
	for _, l := range c.Levels {
		lv.WriteByte(byte('0' + levelStep(l)))
	}
	return fmt.Sprintf("%s|%d|%d|%t|%.2f|%s|%s|%d|%s", c.State, int(c.Pos/time.Second/1), c.Dur/time.Second, c.Final, c.Speed, lv.String(), c.Status, c.Frame%10, c.Title)
}

func levelStep(l float64) int { return max(0, min(8, int(l*8+0.5))) }

var (
	barGlyphs = []rune(" ▁▂▃▄▅▆▇█")
	spinner   = []rune("⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏")
)

// Clock formats a duration as m:ss, or h:mm:ss from an hour up.
func Clock(d time.Duration) string {
	s := int(d.Round(time.Second) / time.Second)
	if s < 0 {
		s = 0
	}
	if s >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", s/3600, s/60%60, s%60)
	}
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

// SpeedLabel formats a speed as 1.25×, 1×.
func SpeedLabel(f float64) string {
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.2f", f), "0"), ".") + "×"
}

// rowBuilder lays out one card row and records where its clickable stretches
// land.
type rowBuilder struct {
	segs []Seg
	hits []Hit
	col  int
}

func (b *rowBuilder) text(s string, fg lipgloss.TerminalColor) {
	b.segs = append(b.segs, Seg{Text: s, FG: fg})
	b.col += lipgloss.Width(s)
}

// chip adds clickable text and records its span.
func (b *rowBuilder) chip(s, action string, fg lipgloss.TerminalColor) {
	b.hits = append(b.hits, Hit{Col: b.col, Width: lipgloss.Width(s), Action: action})
	b.text(s, fg)
}

func (b *rowBuilder) row() Row {
	// The whole row is interface, not text: nothing in it is copied.
	return Row{Segs: b.segs, Hits: b.hits, Gutter: b.col}
}

// playerPanel renders the card: a transport row, the scrub bar with the
// position, and the level bars with the status.
func playerPanel(msg Message, width int) (string, LineMap) {
	c := msg.Player
	if c == nil {
		c = &PlayerCard{State: "idle", Speed: 1}
	}
	inner := max(width-4, 24)
	accent := lipgloss.TerminalColor(ColorVoice)
	if c.State == "failed" {
		accent = ColorDanger
	}
	active := c.State == "playing" || c.State == "buffering"

	// Transport.
	var t rowBuilder
	compact := inner < 62
	label := func(glyph, word string) string {
		if compact {
			return glyph
		}
		return glyph + " " + word
	}
	t.chip(label("◂◂", "10s"), PlayerBack, ColorMuted)
	t.text("  ", nil)
	switch {
	case c.State == "buffering":
		t.chip(string(spinner[c.Frame%len(spinner)])+" wait", PlayerToggle, ColorVoice)
	case active:
		t.chip(label("❚❚", "pause"), PlayerToggle, ColorVoice)
	case c.State == "ended" || c.State == "stopped":
		t.chip(label("↻", "replay"), PlayerToggle, ColorVoice)
	default:
		t.chip(label("▶", "play"), PlayerToggle, ColorVoice)
	}
	t.text("  ", nil)
	t.chip(label("■", "stop"), PlayerStop, ColorMuted)
	t.text("  ", nil)
	t.chip(label("10s", "▸▸"), PlayerFwd, ColorMuted)
	t.text("   ", nil)
	if !compact {
		t.text("speed ", ColorLow)
	}
	for i, sp := range PlayerSpeeds {
		if i > 0 {
			t.text(" ", nil)
		}
		fg := lipgloss.TerminalColor(ColorMuted)
		if sp == c.Speed {
			fg = ColorCream
		}
		s := SpeedLabel(sp)
		if sp == c.Speed {
			s = "[" + s + "]"
		}
		t.chip(s, PlayerSpeed+fmt.Sprintf("%g", sp), fg)
	}

	// Scrub bar.
	var s rowBuilder
	pos := Clock(c.Pos)
	end := Clock(c.Dur)
	if !c.Final {
		end += "+"
	}
	s.text(pos+" ", ColorMuted)
	barW := max(inner-lipgloss.Width(pos)-lipgloss.Width(end)-4, 8)
	frac := 0.0
	if c.Dur > 0 {
		frac = float64(c.Pos) / float64(c.Dur)
	}
	frac = max(0, min(1, frac))
	knob := int(frac * float64(barW-1))
	s.hits = append(s.hits, Hit{Col: s.col, Width: barW, Action: PlayerSeek})
	s.text(strings.Repeat("━", knob), ColorVoice)
	s.text("●", ColorCream)
	s.text(strings.Repeat("─", max(barW-knob-1, 0)), ColorLow)
	s.text(" "+end, ColorMuted)

	// Levels and status.
	var l rowBuilder
	bars := make([]rune, 0, len(c.Levels))
	for _, lv := range c.Levels {
		bars = append(bars, barGlyphs[levelStep(lv)])
	}
	if len(bars) == 0 {
		bars = []rune(strings.Repeat(string(barGlyphs[1]), 24))
	}
	lvFG := lipgloss.TerminalColor(ColorTeal)
	if !active {
		lvFG = ColorLow
	}
	l.text(string(bars), lvFG)
	status := c.Status
	if status == "" {
		status = c.State
	}
	l.text("  "+status, ColorMuted)

	title := "read aloud"
	detail := ""
	if c.Title != "" {
		detail = c.Title
	}
	p := Panel{
		Title: title, Detail: detail, Meta: c.State + " · " + SpeedLabel(max(c.Speed, 0.5)),
		Width: width, Accent: accent, Open: true,
		BodyRows: []Row{t.row(), s.row(), l.row()},
	}
	return p.Render()
}
