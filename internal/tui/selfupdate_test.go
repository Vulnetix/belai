package tui

import (
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/selfupdate"
	"github.com/vulnetix/belai/internal/vulnetixcli"
)

func availableStatus() selfupdate.Status {
	cur, _ := vulnetixcli.ParseVersion("v0.1.1")
	latest, _ := vulnetixcli.ParseVersion("v0.9.0")
	return selfupdate.Status{
		Checked:   true,
		Available: true,
		Current:   cur,
		Latest:    latest,
		Tag:       "v0.9.0",
		URL:       "https://github.com/Vulnetix/belai/releases/tag/v0.9.0",
		Method:    vulnetixcli.InstallBrew,
		Command:   "brew upgrade --formula vulnetix/tap/belai",
	}
}

func TestBelaiUpdateNoticeReachesPanelAndBanner(t *testing.T) {
	a := New(Options{})
	a.width = 100
	before := len(a.messages)

	a.handleBelaiUpdate(belaiUpdateMsg{status: availableStatus()})

	if len(a.messages) != before+1 {
		t.Fatalf("messages = %d, want %d", len(a.messages), before+1)
	}
	last := a.messages[len(a.messages)-1]
	if last.Role != "system" {
		t.Fatalf("notice role = %q, want system", last.Role)
	}
	for _, want := range []string{"v0.9.0", "v0.1.1", "brew upgrade --formula vulnetix/tap/belai"} {
		if !strings.Contains(last.Text(), want) {
			t.Fatalf("notice = %q, missing %q", last.Text(), want)
		}
	}
	if !strings.Contains(a.bannerView(), "update v0.9.0 available") {
		t.Fatalf("banner missing the update note:\n%s", a.bannerView())
	}
}

func TestBelaiUpdateSilentWhenCurrent(t *testing.T) {
	a := New(Options{})
	a.width = 100
	before := len(a.messages)

	a.handleBelaiUpdate(belaiUpdateMsg{status: selfupdate.Status{Checked: true, Error: "github returned 403"}})

	if len(a.messages) != before {
		t.Fatalf("a failed check added %d message(s)", len(a.messages)-before)
	}
	if strings.Contains(a.bannerView(), "update") {
		t.Fatalf("banner shows an update note after a failed check:\n%s", a.bannerView())
	}
}

func TestCheckBelaiUpdateCmdRespectsOptOut(t *testing.T) {
	a := New(Options{})
	if a.checkBelaiUpdateCmd() == nil {
		t.Fatal("the check should be on by default")
	}
	off := false
	a.settings.UpdateCheck = &off
	if a.checkBelaiUpdateCmd() != nil {
		t.Fatal("update_check=false must skip the check entirely")
	}
	a.settings.UpdateCheck = nil
	t.Setenv("BELAI_NO_UPDATE_CHECK", "1")
	if a.checkBelaiUpdateCmd() != nil {
		t.Fatal("BELAI_NO_UPDATE_CHECK=1 must skip the check entirely")
	}
}
