// Package components holds the Bubble Tea building blocks for the Belai TUI.
package components

import (
	"fmt"
	"runtime"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

var versionStyle = lipgloss.NewStyle().Foreground(ColorMuted)

// updateStyle marks the "a newer release exists" note on the version row.
var updateStyle = lipgloss.NewStyle().Foreground(ColorAmber)

func isVersionSentinel(s string) bool {
	switch s {
	case "", "dev", "unknown":
		return true
	}
	return false
}

// PixGrid is a 12×12 pixel cartoon owl rendered with half-blocks (▀).
// Each text cell shows two pixels; 12 cols × 6 text rows.
var PixGrid = [12]string{
	". . # . . . . . . # . .",
	". # T # . . . . # T # .",
	". # T T T T T T T T # .",
	"# T O O O T T O O O T #",
	"# T O * @ T T @ * O T #",
	"# T O @ @ T T @ @ O T #",
	"# T O O O T T O O O T #",
	"# T T T o o o o T T T #",
	". # T T T o o T T T # .",
	". # T T T T T T T T # .",
	". . # T T T T T T # . .",
	". . . # # # # # # . . .",
}

var pixColors = map[rune]lipgloss.Color{
	'#': lipgloss.Color("#1C3431"),
	'T': lipgloss.Color("#3AC4B4"),
	't': lipgloss.Color("#76E0CD"),
	'O': lipgloss.Color("#F6EED6"),
	'@': lipgloss.Color("#1C3431"),
	'*': lipgloss.Color("#FFFDF7"),
	'o': lipgloss.Color("#E8912B"),
	'.': lipgloss.Color(""),
}

// Banner renders the Pix owl + wordmark.
type Banner struct {
	Width   int
	Version string
	Commit  string
	Built   string
	// Tip is the one-line shortcut hint on the banner's last row. It is
	// chosen once by the caller (stable across renders); empty falls back to
	// the default /help line so the six-row height invariant is never at risk.
	Tip string
	// Update, when set, is appended to the version line as a highlighted
	// note (e.g. "update v0.2.0 available"). It shares the version row so
	// the banner keeps its six-row height.
	Update string
	// Resumed, when set, replaces the subtitle row with a resumed-session
	// variant: "resumed <name> · N turns restored". RestoredTurns is the turn
	// count shown next to it.
	Resumed       string
	RestoredTurns int
	// Compact renders the one-line header instead of the owl: the wordmark,
	// the build facts and the resumed note. The owl is for a first run and
	// /welcome (docs/tui-design.md).
	Compact bool
	// Toolchain overrides the Go release and platform on the build line;
	// empty means the running binary's. Captures pin it so they do not
	// depend on the host.
	Toolchain string
}

// View returns the banner. In ASCII/NO_COLOR mode it falls back to plain text.
func (b Banner) View() string {
	if b.Compact {
		return b.compactView()
	}
	if termenv.NewOutput(nil).ColorProfile() == termenv.Ascii {
		return b.textView()
	}
	return b.pixView()
}

// Tagline is the brand line both banners carry: the owl's subtitle and the
// compact header.
const Tagline = "a safer LLM harness · vulnetix.com"

// compactView is the header past a first run: the wordmark and tagline (plus
// the resumed note) on one line, the full build facts on the next, each cut
// to the width.
func (b Banner) compactView() string {
	line := AccentStyle.Bold(true).Render("belai") + MutedStyle.Render(" · "+Tagline)
	if b.Resumed != "" {
		note := "resumed " + b.Resumed
		if b.RestoredTurns > 0 {
			note += fmt.Sprintf(" · %d turns restored", b.RestoredTurns)
		}
		line += LowStyle.Render("   " + note)
	}
	if b.Width > 0 {
		line = lipgloss.NewStyle().MaxWidth(b.Width).Render(line)
	}
	if v := b.versionLine(); v != "" {
		line += "\n" + v
	}
	return line
}

// versionLine renders one dim line with the build facts that are known,
// labelled: version, commit, build time, and the Go toolchain and platform
// the binary was built for. Empty fields and unstamped sentinels are dropped;
// if no stamp is known it returns "" so a bare `go build` shows no line at
// all.
func (b Banner) versionLine() string {
	var head, rest []string
	if !isVersionSentinel(b.Version) {
		head = append(head, versionStyle.Render("v"+strings.TrimPrefix(b.Version, "v")))
	}
	// The update note follows the version it supersedes, ahead of the
	// detail, so a narrow terminal cuts the detail and keeps the note.
	if b.Update != "" {
		head = append(head, updateStyle.Render(b.Update))
	}
	if !isVersionSentinel(b.Commit) {
		rest = append(rest, "commit "+b.Commit)
	}
	if !isVersionSentinel(b.Built) {
		rest = append(rest, "built "+b.Built)
	}
	if len(rest) > 0 || !isVersionSentinel(b.Version) {
		rest = append(rest, b.toolchain())
	}
	for _, r := range rest {
		head = append(head, versionStyle.Render(r))
	}
	if len(head) == 0 {
		return ""
	}
	line := strings.Join(head, versionStyle.Render(" · "))
	if b.Width > 0 {
		line = lipgloss.NewStyle().MaxWidth(b.Width).Render(line)
	}
	return line
}

// toolchain is the Go release and platform the binary was built for, or
// b.Toolchain when set.
func (b Banner) toolchain() string {
	if b.Toolchain != "" {
		return b.Toolchain
	}
	// A GOEXPERIMENT build reports e.g. "go1.27.1-X:nodwarf5"; the release
	// is the useful part.
	goVersion, _, _ := strings.Cut(runtime.Version(), "-X:")
	return goVersion + " " + runtime.GOOS + "/" + runtime.GOARCH
}

func (b Banner) pixView() string {
	var lines []string
	for row := 0; row < 6; row++ {
		var line strings.Builder
		for col := 0; col < 12; col++ {
			upper := rune(PixGrid[row*2][col*2])
			lower := rune(PixGrid[row*2+1][col*2])
			fg := pixColors[upper]
			bg := pixColors[lower]
			if upper == '.' && lower == '.' {
				line.WriteString(" ")
				continue
			}
			cell := "▀"
			style := lipgloss.NewStyle()
			if fg != "" {
				style = style.Foreground(fg)
			}
			if bg != "" {
				style = style.Background(bg)
			}
			line.WriteString(style.Render(cell))
		}
		lines = append(lines, line.String())
	}

	owl := strings.Join(lines, "\n")

	// The wordmark block sits beside the owl rather than under it: it keeps
	// the banner to six rows and leaves the transcript more of the screen.
	// Six rows, always: the owl is six rows tall, and an unstamped build must
	// not change the banner's height and reflow the transcript under it.
	subtitle := Tagline
	if b.Resumed != "" {
		subtitle = "resumed " + b.Resumed
		if b.RestoredTurns > 0 {
			subtitle += fmt.Sprintf(" · %d turns restored", b.RestoredTurns)
		}
	}
	tip := b.Tip
	if tip == "" {
		tip = "type " + KeyStyle.Render("/help") + " for commands and shortcuts"
	}
	right := []string{
		"",
		belayLine(b.ropeTail()),
		belayIndent + MutedStyle.Render(subtitle),
		belayIndent + b.versionLine(),
		"",
		belayIndent + MutedStyle.Render(tip),
	}

	block := lipgloss.NewStyle().PaddingLeft(1).Render(strings.Join(right, "\n"))
	return lipgloss.JoinHorizontal(lipgloss.Top, owl, block)
}

// Belai is pronounced "belay", so the wordmark is drawn as one: a rope leads
// out of the owl, through the name, and runs on to a carabiner. The rows
// beneath it are indented to line up with the name.
const (
	belayIndent  = "   "
	belayRopeMax = 33
	belayRopeMin = 3
	// belayLead is the owl, its one-cell gap and "── belai " before the rope.
	belayLead = 12 + 1 + 9
)

var (
	ropeStyle     = lipgloss.NewStyle().Foreground(ColorAmber)
	wordmarkStyle = lipgloss.NewStyle().Foreground(ColorCream).Bold(true)
)

// belayLine renders "── belai " followed by tail, which carries the line on.
func belayLine(tail string) string {
	return ropeStyle.Render("──") + " " + wordmarkStyle.Render("belai") + " " + tail
}

// ropeTail is the rope after the name, shortened to fit a narrow terminal so
// the wordmark row never wraps and the banner keeps its six rows.
func (b Banner) ropeTail() string {
	n := belayRopeMax
	if b.Width > 0 && b.Width-belayLead-1 < n {
		n = max(b.Width-belayLead-1, belayRopeMin)
	}
	return ropeStyle.Render(strings.Repeat("─", n) + "◉")
}

func (b Banner) textView() string {
	var out []string
	out = append(out, "BELAI · "+Tagline)
	if v := b.versionLine(); v != "" {
		out = append(out, v)
	}
	return strings.Join(out, "\n")
}
