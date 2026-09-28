package tui

import (
	"regexp"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/vulnetix/belai/internal/tui/components"
	"github.com/vulnetix/belai/internal/vulnetixcli"
)

// linkPattern finds https links in rendered text. Only https is ever opened
// (vulnetixcli.OpenBrowser refuses anything else), so only https is offered.
var linkPattern = regexp.MustCompile("https://[^\\s<>\"'`()\\[\\]{}]+")

// trimLink drops sentence punctuation a link picks up at the end of a line.
func trimLink(s string) string { return strings.TrimRight(s, ".,;:!?…") }

// findLinks returns every https link in s, in order.
func findLinks(s string) []string {
	var out []string
	for _, m := range linkPattern.FindAllString(s, -1) {
		if l := trimLink(m); len(l) > len("https://") {
			out = append(out, l)
		}
	}
	return out
}

// linkAt returns the https link under content column col on a rendered line,
// or "" when the pointer is not on one.
func linkAt(line components.SourceLine, col int) string {
	for _, loc := range linkPattern.FindAllStringIndex(line.Text, -1) {
		link := trimLink(line.Text[loc[0]:loc[1]])
		start := line.Col + ansi.StringWidth(line.Text[:loc[0]])
		end := start + ansi.StringWidth(link)
		if col >= start && col < end {
			return link
		}
	}
	return ""
}

// hoverLink picks the link for a hovered line: the one under the pointer,
// else the panel's only link. A panel with several links and the pointer
// on none offers none, so ctrl+y never guesses.
func (a *App) hoverLink(line components.SourceLine, col int) string {
	if l := linkAt(line, col); l != "" {
		return l
	}
	if line.Owner < 0 || line.Owner >= len(a.messages) {
		return ""
	}
	links := findLinks(a.messages[line.Owner].Text())
	if len(links) == 1 {
		return links[0]
	}
	return ""
}

// openLink opens an https link in the default browser.
func openLink(link string) tea.Cmd {
	if link == "" {
		return nil
	}
	return func() tea.Msg {
		if err := vulnetixcli.OpenBrowser(link); err != nil {
			return copiedMsg{text: "could not open " + link + ": " + err.Error()}
		}
		return copiedMsg{text: "opened " + link + " in your browser"}
	}
}
