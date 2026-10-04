package components

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func vulnCard() *VulnCard {
	return &VulnCard{
		ID:      "CVE-2021-44228",
		URL:     "https://www.vulnetix.com/vuln/CVE-2021-44228",
		Command: "vulnetix vdb vuln CVE-2021-44228",
	}
}

func TestVulnCardShowsLinkHintAndLaunch(t *testing.T) {
	out, lm := vulnPanel(Message{Role: VulnRole, Vuln: vulnCard()}, 110)
	plain := ansi.Strip(out)
	for _, want := range []string{"CVE-2021-44228", "https://www.vulnetix.com/vuln/CVE-2021-44228", "vulnetix vdb vuln CVE-2021-44228", "launch belai:triage"} {
		if !strings.Contains(plain, want) {
			t.Errorf("no %q in\n%s", want, plain)
		}
	}
	lines := strings.Split(out, "\n")
	if len(lines) != len(lm) {
		t.Fatalf("%d lines, %d line-map rows", len(lines), len(lm))
	}
	var actions []string
	for i, sl := range lm {
		line := ansi.Strip(lines[i])
		for _, h := range sl.Hits {
			if h.Col < 0 || h.Col+h.Width > ansi.StringWidth(line)+1 {
				t.Errorf("hit %+v outside %q", h, line)
			}
			actions = append(actions, h.Action)
		}
	}
	for _, want := range []string{VulnOpen, VulnCopyURL, VulnCopyCmd, VulnRemediate, VulnTriage} {
		if !contains(actions, want) {
			t.Errorf("no %s hit in %v", want, actions)
		}
	}
}

// The link and the command are text a drag copies, the labels, the note and
// the buttons are not: a selection over the whole card yields the two values.
func TestVulnCardCopiesTheLinkAndTheCommandOnly(t *testing.T) {
	out, lm := vulnPanel(Message{Role: VulnRole, Vuln: vulnCard()}, 110)
	got := lm.Text(Pos{Line: 0, Col: 0}, Pos{Line: len(lm) - 1, Col: 200})
	for _, want := range []string{"https://www.vulnetix.com/vuln/CVE-2021-44228", "vulnetix vdb vuln CVE-2021-44228"} {
		if !strings.Contains(got, want) {
			t.Errorf("a selection lost %q: %q", want, got)
		}
	}
	for _, leak := range []string{"console", "vdb      ", "remediate", "launch", "copy link", "open link", "lists the remediation"} {
		if strings.Contains(got, leak) {
			t.Errorf("a selection copied the interface text %q: %q", leak, got)
		}
	}
	// The link is plain text, not a button: a press on it starts a selection.
	for i, sl := range lm {
		for _, h := range sl.Hits {
			line := ansi.Strip(strings.Split(out, "\n")[i])
			if strings.Contains(line[h.Col:min(len(line), h.Col+h.Width)], "https://") {
				t.Errorf("the link is a button again: %q", line)
			}
		}
	}
}

func TestVulnCardAfterLaunchOffersNoSecondLaunch(t *testing.T) {
	c := vulnCard()
	c.Launched = true
	out, lm := vulnPanel(Message{Role: VulnRole, Vuln: c}, 110)
	if !strings.Contains(ansi.Strip(out), "started") {
		t.Errorf("no started note:\n%s", ansi.Strip(out))
	}
	for _, sl := range lm {
		for _, h := range sl.Hits {
			if h.Action == VulnTriage {
				t.Errorf("launch still offered")
			}
		}
	}
}

func TestVulnCardAfterRemediateOffersNoSecondPrompt(t *testing.T) {
	c := vulnCard()
	c.Remediating = true
	out, lm := vulnPanel(Message{Role: VulnRole, Vuln: c}, 110)
	if !strings.Contains(ansi.Strip(out), "remediation started") {
		t.Errorf("no started note:\n%s", ansi.Strip(out))
	}
	for _, sl := range lm {
		for _, h := range sl.Hits {
			if h.Action == VulnRemediate {
				t.Errorf("remediate still offered")
			}
		}
	}
}

func TestVulnCardKeyTracksLaunch(t *testing.T) {
	a, b, c := vulnCard(), vulnCard(), vulnCard()
	b.Launched = true
	c.Remediating = true
	if a.Key() == b.Key() || a.Key() == c.Key() || (*VulnCard)(nil).Key() != "" {
		t.Error("key does not follow the card")
	}
}

func TestVulnCardIsCleanAtNarrowWidth(t *testing.T) {
	out, lm := vulnPanel(Message{Role: VulnRole, Vuln: vulnCard()}, 30)
	if len(strings.Split(out, "\n")) != len(lm) {
		t.Fatal("line map out of step")
	}
	for _, l := range strings.Split(out, "\n") {
		if ansi.StringWidth(l) > 30 {
			t.Errorf("line wider than 30: %q", ansi.Strip(l))
		}
	}
}
