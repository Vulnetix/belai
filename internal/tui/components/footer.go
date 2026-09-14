package components

import (
	"fmt"
	"strings"
)

// Footer shows session, token/cost, and model status.
type Footer struct {
	Session string
	Tokens  int
	Cost    string
	Model   string
}

// View renders the footer as a single line.
func (f Footer) View() string {
	parts := make([]string, 0, 4)
	if f.Session != "" {
		parts = append(parts, "session: "+f.Session)
	}
	parts = append(parts, fmt.Sprintf("tokens: %d", f.Tokens))
	if f.Cost != "" {
		parts = append(parts, "cost: "+f.Cost)
	}
	if f.Model != "" {
		parts = append(parts, "model: "+f.Model)
	}
	return strings.Join(parts, "  |  ")
}
