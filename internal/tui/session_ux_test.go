package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/tui/components"
)

// TestPrependBannerLineMapAlignment pins the regression that would otherwise
// ship silently: the banner now lives inside the scrollable transcript, so the
// line map must grow by exactly as many Chrome rows as the body grows lines.
func TestPrependBannerLineMapAlignment(t *testing.T) {
	a := New(Options{Workdir: t.TempDir()})
	a.width = 120
	body, lm := components.MessageList{
		Messages:  []components.Message{{Role: "user", Content: "hello"}},
		Width:     a.contentWidth(),
		ExpandAll: false,
		ShowTools: true,
	}.Render()

	gotBody, gotLM := a.prependBanner(body, lm)
	if len(gotLM) != strings.Count(gotBody, "\n")+1 {
		t.Fatalf("line map len %d != body lines %d", len(gotLM), strings.Count(gotBody, "\n")+1)
	}
	// The banner prefix is Chrome, ownerless, and never highlighted.
	nBanner := a.bannerHeight() + 1
	if len(gotLM) != len(lm)+nBanner {
		t.Fatalf("line map grew by %d, want %d", len(gotLM)-len(lm), nBanner)
	}
	for i := 0; i < nBanner; i++ {
		if !gotLM[i].Chrome || gotLM[i].Owner != -1 {
			t.Fatalf("prefix line %d not chrome: %+v", i, gotLM[i])
		}
	}
	// The transcript rows keep their original provenance after the shift.
	if gotLM[nBanner].Owner != lm[0].Owner {
		t.Fatalf("transcript provenance shifted: %+v", gotLM[nBanner])
	}
}

// TestBannerPresentAfterManyMessages pins the new contract: the banner is never
// hidden by message count; it scrolls away as the first transcript entry.
func TestBannerPresentAfterManyMessages(t *testing.T) {
	a := New(Options{Workdir: t.TempDir()})
	a.width = 120
	a.height = 40
	for i := 0; i < 6; i++ {
		a.messages = append(a.messages, components.Message{Role: "user", Content: "message"})
	}
	if !a.bannerVisible() {
		t.Fatal("banner must stay visible after more than two messages")
	}
	// The banner is now the first entry of the transcript content (it scrolls
	// out of the visible viewport as the thread grows), so it must be present
	// in the prepared body, not hidden by the message count.
	body, _ := a.prependBanner("transcript", components.LineMap{})
	if !strings.Contains(body, "BELAI") {
		t.Fatal("transcript body should still contain the banner")
	}
}

// TestBannerSettingFalseHidesBanner pins the explicit ui.banner:false switch
// still works in both directions.
func TestBannerSettingFalseHidesBanner(t *testing.T) {
	off := false
	a := New(Options{Workdir: t.TempDir()})
	a.settings.UI = &config.UISettings{Banner: &off}
	if a.bannerVisible() {
		t.Fatal("ui.banner:false must hide the banner")
	}
	body, lm := components.MessageList{Messages: nil, Width: 80}.Render()
	gotBody, gotLM := a.prependBanner(body, lm)
	if gotBody != body || len(gotLM) != len(lm) {
		t.Fatal("hidden banner must leave the body and line map untouched")
	}
}

// TestBannerTipStableAcrossViews pins the one-choice-per-session rule: the tip
// is chosen in New and must not strobe across repeated renders.
func TestBannerTipStableAcrossViews(t *testing.T) {
	a := New(Options{Workdir: t.TempDir()})
	a.width = 120
	first := a.bannerView()
	for i := 0; i < 5; i++ {
		if a.bannerView() != first {
			t.Fatal("banner tip must be stable across renders")
		}
	}
	if a.bannerTip == "" {
		t.Fatal("a session id must produce a tip")
	}
}

// TestEscEscClearsComposer pins the two-press composer clear: first esc arms,
// second clears.
func TestEscEscClearsComposer(t *testing.T) {
	a := New(Options{Workdir: t.TempDir()})
	a.editor.SetValue("a draft")

	a.handleChatKey(tea.KeyMsg{Type: tea.KeyEsc})
	if a.editor.Value() != "a draft" {
		t.Fatal("first esc must not clear the composer")
	}
	if !a.isArmed(armClear) {
		t.Fatal("first esc over a draft must arm the composer clear")
	}

	a.handleChatKey(tea.KeyMsg{Type: tea.KeyEsc})
	if a.editor.Value() != "" {
		t.Fatal("second esc must clear the composer")
	}
	if a.isArmed(armClear) {
		t.Fatal("composer clear arm must be disarmed after firing")
	}
}

// TestEscCancelsRequestBeforeClearingComposer pins the cascade precedence: a
// running turn is cancelled before a draft is ever touched.
func TestEscCancelsRequestBeforeClearingComposer(t *testing.T) {
	a := New(Options{Workdir: t.TempDir()})
	a.editor.SetValue("a draft")
	cancelled := false
	a.cancel = func() { cancelled = true }

	a.handleChatKey(tea.KeyMsg{Type: tea.KeyEsc})
	if !cancelled || a.cancel != nil {
		t.Fatal("first esc must cancel the in-flight request")
	}
	if a.editor.Value() != "a draft" {
		t.Fatal("cancelling must leave the draft alone")
	}
	if a.isArmed(armClear) {
		t.Fatal("the cancel esc must not arm the composer clear")
	}

	// A second esc pair then clears the draft.
	a.handleChatKey(tea.KeyMsg{Type: tea.KeyEsc})
	if !a.isArmed(armClear) {
		t.Fatal("second esc should arm the composer clear")
	}
	a.handleChatKey(tea.KeyMsg{Type: tea.KeyEsc})
	if a.editor.Value() != "" {
		t.Fatal("third esc should clear the composer")
	}
}

// TestHoverResolvesAfterBannerPrepend drives the real chatView frame and pins
// that a file panel still hover-resolves to the right message now that the
// banner occupies the first transcript rows.
func TestHoverResolvesAfterBannerPrepend(t *testing.T) {
	a := New(Options{Workdir: t.TempDir()})
	a.width = 120
	a.height = 40
	a.messages = []components.Message{
		{Role: "tool", ToolName: "Read", ToolArgs: `{"path":"main.go"}`,
			Meta: map[string]any{"path": "main.go"}, Content: "package main\n\nfunc main() {}\n"},
	}
	// Build the real frame the way chatView does (banner + viewport + footer).
	a.View()
	line := hoverLine(t, a, 0, true, false)
	if a.lastFrame.lines[line].Owner != 0 {
		t.Fatalf("hover line owner = %d, want 0", a.lastFrame.lines[line].Owner)
	}
	pointAt(a, line)
	a.recomputeHover()
	if !a.hover.file || a.hover.msg != 0 {
		t.Fatalf("hover = %+v, want file panel 0", a.hover)
	}
}

// A turn that opens with tool calls fills the bubble the turn started with.
// That bubble must carry the agent provider/model, or ctrl+o titles the panel
// with the generic word "model".
func TestToolOnlyAssistantBubbleTitlesWithTheModel(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	a := New(Options{Workdir: t.TempDir()})
	a.cfg.Provider, a.cfg.Model = "cloudflare-ai-gateway", "@cf/deepseek-ai/deepseek-v4-pro-0813"
	a.messages = nil

	i := a.currentAssistantBubble()
	a.messages[i].ToolCalls = []components.AgentToolCall{{ID: "c1", Name: "Bash", Args: `{"command":"go test ./..."}`}}
	if got := a.messages[i]; got.Provider != a.cfg.Provider || got.Model != a.cfg.Model {
		t.Fatalf("bubble = %s/%s, want the agent model", got.Provider, got.Model)
	}

	out := ansi.Strip(components.MessageList{Width: 120, ExpandAll: true, ShowTools: true, Messages: a.messages}.View())
	if !strings.Contains(out, "cloudflare-ai-gateway/@cf/deepseek-ai/deepseek-v4-pro-0813") {
		t.Fatalf("expanded tool-only panel is not titled with the model:\n%s", out)
	}
}
