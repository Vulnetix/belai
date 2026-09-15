package components

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Footer shows session, token/cost, model, provider, and mode status.
type Footer struct {
	Session  string
	Tokens   int
	Cost     string
	Model    string
	Provider string
	Mode     string
	Width    int
	Cwd      string
	Branch   string
}

var (
	modeStyle = lipgloss.NewStyle().Bold(true).Padding(0, 1)
)

func modeColor(mode string) lipgloss.Color {
	switch mode {
	case "plan":
		return lipgloss.Color("4")
	case "goal":
		return lipgloss.Color("5")
	default:
		return lipgloss.Color("2")
	}
}

// View renders the footer as two lines.
func (f *Footer) View() string {
	if f.Width <= 0 {
		f.Width = 80
	}

	var line1Parts []string
	if f.Cwd != "" {
		line1Parts = append(line1Parts, f.Cwd)
	}
	if f.Branch != "" {
		line1Parts = append(line1Parts, "⎇ "+f.Branch)
	}
	line1 := strings.Join(line1Parts, "  ")

	modeChip := modeStyle.Background(modeColor(f.Mode)).Foreground(lipgloss.Color("0")).Render(f.Mode)
	parts := []string{}
	if f.Provider != "" {
		parts = append(parts, f.Provider)
	}
	if f.Model != "" {
		parts = append(parts, f.Model)
	}
	rightParts := []string{}
	if f.Session != "" {
		rightParts = append(rightParts, "session: "+f.Session)
	}
	rightParts = append(rightParts, fmt.Sprintf("tokens: %d", f.Tokens))
	if f.Cost != "" {
		rightParts = append(rightParts, "cost: "+f.Cost)
	}

	left := strings.Join(parts, " · ")
	right := strings.Join(rightParts, "  |  ")

	line2 := left
	if right != "" {
		pad := f.Width - lipgloss.Width(left) - lipgloss.Width(right) - lipgloss.Width(modeChip) - 2
		if pad < 1 {
			pad = 1
		}
		line2 = left + strings.Repeat(" ", pad) + modeChip
		extraPad := f.Width - lipgloss.Width(line2) - lipgloss.Width(right)
		if extraPad < 1 {
			extraPad = 1
		}
		line2 = line2 + strings.Repeat(" ", extraPad) + right
	} else {
		line2 = left + " " + modeChip
	}

	if line1 != "" {
		return line1 + "\n" + line2
	}
	return line2
}
