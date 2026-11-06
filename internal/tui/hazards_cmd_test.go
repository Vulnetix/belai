package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/belai/internal/agent"
	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/shellsafe"
	"github.com/vulnetix/belai/internal/tools"
)

func hazardAskRequest() *agent.AskRequest {
	return &agent.AskRequest{Name: "Bash", Subject: "git push --force", Hazard: []string{"exec.git.destructive"}}
}

func TestHazardAskOffersNoAllowAlways(t *testing.T) {
	st := newPermissionAskState(hazardAskRequest(), nil)
	if got := st.options(); len(got) != 2 || got[0] != "allow once" || got[1] != "deny" {
		t.Fatalf("hazard options = %v", got)
	}
	if st.decisionAt(1) != askDeny || st.decisionAt(0) != askAllowOnce || st.decisionAt(9) != askDeny {
		t.Fatal("decisionAt maps the wrong rows")
	}
	plain := newPermissionAskState(sampleAskRequest(), nil)
	if len(plain.options()) != 3 {
		t.Fatalf("an ordinary ask keeps allow always: %v", plain.options())
	}
}

func TestHazardAskAnsweredAlwaysIsOnceAndWritesNoRule(t *testing.T) {
	workdir := t.TempDir()
	t.Setenv("BELAI_HOME", t.TempDir())
	a := New(Options{Workdir: workdir})
	a.push(viewPermissionAsk)
	reply := make(chan agent.PermissionAskReply, 1)
	a.permAskState = newPermissionAskState(hazardAskRequest(), reply)

	// A web answer or a spoken keyword can still say allow always.
	a.settlePermissionAsk(askAllowAlways, answerFromHost, "")

	select {
	case ans := <-reply:
		if !ans.Allow {
			t.Fatal("the once answer must still allow this call")
		}
	case <-time.After(time.Second):
		t.Fatal("no reply sent")
	}
	got, err := config.LoadProject(workdir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Permissions.Allow) != 0 {
		t.Fatalf("a hazard ask wrote a rule: %v", got.Permissions.Allow)
	}
}

func TestHazardAskKeysSelectOnlyOnceOrDeny(t *testing.T) {
	a := New(Options{})
	a.push(viewPermissionAsk)
	reply := make(chan agent.PermissionAskReply, 1)
	a.permAskState = newPermissionAskState(hazardAskRequest(), reply)
	_, _ = a.Update(tea.KeyMsg{Type: tea.KeyDown})
	_, _ = a.Update(tea.KeyMsg{Type: tea.KeyDown})
	if a.permAskState.selected != 1 {
		t.Fatalf("selected = %d, want the last row", a.permAskState.selected)
	}
	ch := make(chan agent.Event)
	a.events = ch
	close(ch)
	_, _ = a.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if ans := <-reply; ans.Allow {
		t.Fatal("the second row of a hazard ask is deny")
	}
}

func TestHazardAskViewNamesTheRail(t *testing.T) {
	a := New(Options{})
	a.push(viewPermissionAsk)
	a.permAskState = newPermissionAskState(hazardAskRequest(), nil)
	out := a.permissionAskView()
	if !strings.Contains(out, "hazard rail") || !strings.Contains(out, "exec.git.destructive") {
		t.Fatalf("the view does not name the rail:\n%s", out)
	}
	if strings.Contains(out, "allow always") {
		t.Fatalf("the view offers allow always:\n%s", out)
	}
}

func TestAskOffDoesNotAnswerAHazardAsk(t *testing.T) {
	a := New(Options{})
	a.push(viewPermissionAsk)
	reply := make(chan agent.PermissionAskReply, 1)
	a.permAskState = newPermissionAskState(hazardAskRequest(), reply)
	a.resolvePendingAsk(true)
	select {
	case <-reply:
		t.Fatal("turning ask off allowed a hazard ask")
	case <-time.After(50 * time.Millisecond):
	}
	if a.permAskState.reply == nil {
		t.Fatal("the hazard ask was dismissed")
	}
}

func TestHazardsReport(t *testing.T) {
	out := hazardsReport(true, false, []string{"docs/private", "bad\x1b[31m"})
	for _, id := range shellsafe.HazardIDs {
		if !strings.Contains(out, id) {
			t.Errorf("/hazards does not list %s", id)
		}
	}
	for _, c := range tools.HazardPathClasses {
		if !strings.Contains(out, c) {
			t.Errorf("/hazards does not list %s", c)
		}
	}
	if !strings.Contains(out, "hazard rails: on") || !strings.Contains(out, "do not waive") || !strings.Contains(out, "docs/private") {
		t.Fatalf("report = %s", out)
	}
	if strings.Contains(out, "\x1b") {
		t.Fatalf("a user path reached the screen uncleaned: %q", out)
	}
	off := hazardsReport(false, true, nil)
	if !strings.Contains(off, "hazard rails: off") || !strings.Contains(off, "waives them") || !strings.Contains(off, "none") {
		t.Fatalf("report = %s", off)
	}
}
