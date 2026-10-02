package components

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func vulnCard() *VulnCard {
	return &VulnCard{
		ID:   "CVE-2021-44228",
		URL:  "https://www.vulnetix.com/vuln/CVE-2021-44228",
		Hint: "remediation: vulnetix vdb vuln CVE-2021-44228",
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
	if !contains(actions, VulnOpen) || !contains(actions, VulnTriage) {
		t.Errorf("hits = %v", actions)
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

func TestVulnCardKeyTracksLaunch(t *testing.T) {
	a, b := vulnCard(), vulnCard()
	b.Launched = true
	if a.Key() == b.Key() || (*VulnCard)(nil).Key() != "" {
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
