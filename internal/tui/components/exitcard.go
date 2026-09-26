package components

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// ExitCard renders the branded session-end card printed to stdout after the
// alt screen tears down. It reuses the Pix owl and its colour table so the
// exit looks like the same tool that was running, and always carries the
// resume command that returns to the finished session.
type ExitCard struct {
	Name      string // display name; empty means fall back to the session id
	SessionID string
	ResumeArg string // the command argument printed after --resume
	Turns     int
	Duration  string
	Tokens    string
	Model     string
	Provider  string
	Path      string
	Width     int
}

// View renders the card. In ASCII/NO_COLOR mode it falls back to plain text,
// exactly as the banner does.
func (c ExitCard) View() string {
	if termenv.NewOutput(nil).ColorProfile() == termenv.Ascii {
		return c.textView()
	}
	return c.pixView()
}

// pixView draws the owl next to a six-row fact block, the same layout the
// banner uses so both bookends share one height and one brand.
func (c ExitCard) pixView() string {
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

	shortID := c.SessionID
	if len(shortID) > 8 {
		shortID = shortID[:8]
	}
	name := c.Name
	if name == "" {
		name = shortID
	}

	right := []string{
		belayLine(ropeStyle.Render("──") + " " + MutedStyle.Render("off belay · session ended")),
		belayIndent + MutedStyle.Render(name+" · "+shortID),
		belayIndent + MutedStyle.Render(c.factsLine()),
		"",
		belayIndent + MutedStyle.Render("belai --resume ") + KeyStyle.Render(c.ResumeArg),
		belayIndent + MutedStyle.Render(c.Path),
	}
	block := lipgloss.NewStyle().PaddingLeft(1).Render(strings.Join(right, "\n"))
	return lipgloss.JoinHorizontal(lipgloss.Top, owl, block)
}

func (c ExitCard) textView() string {
	shortID := c.SessionID
	if len(shortID) > 8 {
		shortID = shortID[:8]
	}
	name := c.Name
	if name == "" {
		name = shortID
	}
	out := []string{
		"BELAI · session ended",
		name + " · " + shortID,
	}
	if facts := c.factsLine(); facts != "" {
		out = append(out, facts)
	}
	if c.ResumeArg != "" {
		out = append(out, "belai --resume "+c.ResumeArg)
	}
	if c.Path != "" {
		out = append(out, c.Path)
	}
	return strings.Join(out, "\n")
}

// factsLine renders the turn/duration/token/model facts as one dim line.
func (c ExitCard) factsLine() string {
	var facts []string
	if c.Turns > 0 {
		if c.Turns == 1 {
			facts = append(facts, "1 turn")
		} else {
			facts = append(facts, strconv.Itoa(c.Turns)+" turns")
		}
	}
	if c.Duration != "" {
		facts = append(facts, c.Duration)
	}
	if c.Tokens != "" {
		facts = append(facts, c.Tokens)
	}
	model := strings.TrimSpace(c.Provider + "/" + c.Model)
	model = strings.TrimSuffix(model, "/")
	if model != "" {
		facts = append(facts, model)
	}
	return strings.Join(facts, " · ")
}
