package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/credentials"
)

// Each session toggle writes its own per-project preference, and a fresh App
// over the same workdir reopens with it.
func TestSessionTogglesPersistToProjectPrefsAndReopen(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	workdir := t.TempDir()

	a := New(Options{Workdir: workdir})

	a.Update(tea.KeyMsg{Type: tea.KeyF3}) // guardrails off
	a.Update(tea.KeyMsg{Type: tea.KeyF4}) // ask off
	a.Update(tea.KeyMsg{Type: tea.KeyF2}) // caveman on
	a.Update(tea.KeyMsg{Type: tea.KeyF5}) // mode agent -> plan

	prefs, err := config.LoadProjectPrefs(workdir)
	if err != nil {
		t.Fatalf("LoadProjectPrefs: %v", err)
	}
	if prefs.Guardrails == nil || *prefs.Guardrails {
		t.Fatalf("guardrails pref = %+v, want false", prefs.Guardrails)
	}
	if prefs.AskPermission == nil || *prefs.AskPermission {
		t.Fatalf("ask pref = %+v, want false", prefs.AskPermission)
	}
	if prefs.Caveman == nil || !*prefs.Caveman {
		t.Fatalf("caveman pref = %+v, want true", prefs.Caveman)
	}
	if prefs.Mode != "plan" {
		t.Fatalf("mode pref = %q, want plan", prefs.Mode)
	}

	// A fresh app over the same workdir opens with the persisted toggles.
	b := New(Options{Workdir: workdir})
	if b.settings.GuardrailsEnabled() {
		t.Fatal("fresh app should reopen with guardrails off")
	}
	if b.settings.AskPermissionEnabled() {
		t.Fatal("fresh app should reopen with ask off")
	}
	if !b.settings.CavemanEnabled() {
		t.Fatal("fresh app should reopen with caveman on")
	}
	if b.mode != "plan" || !b.modeSticky || !b.modeExplicit {
		t.Fatalf("fresh app mode = %q sticky=%v explicit=%v, want plan/true/true", b.mode, b.modeSticky, b.modeExplicit)
	}

	// A different workdir must not inherit any of it.
	c := New(Options{Workdir: t.TempDir()})
	if !c.settings.GuardrailsEnabled() || !c.settings.AskPermissionEnabled() || c.settings.CavemanEnabled() {
		t.Fatal("a different workdir must not inherit the persisted toggles")
	}
}

// /yolo on persists both relaxations; /yolo off clears the prefs so leaving
// yolo does not re-inherit a stale persisted relaxation.
func TestYoloOffClearsGuardrailsAndAskPrefs(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	workdir := t.TempDir()
	a := New(Options{Workdir: workdir})

	a.setYolo(true)
	prefs, err := config.LoadProjectPrefs(workdir)
	if err != nil {
		t.Fatalf("LoadProjectPrefs: %v", err)
	}
	if prefs.Guardrails == nil || *prefs.Guardrails || prefs.AskPermission == nil || *prefs.AskPermission {
		t.Fatalf("yolo on prefs = %+v, want both false", prefs)
	}

	a.setYolo(false)
	prefs, err = config.LoadProjectPrefs(workdir)
	if err != nil {
		t.Fatalf("LoadProjectPrefs: %v", err)
	}
	if prefs.Guardrails != nil || prefs.AskPermission != nil {
		t.Fatalf("yolo off must clear the guardrails/ask prefs, got %+v", prefs)
	}
}

// Turning the firewall off never needs availability, so it must write the pref
// even though turning it on is gated.
func TestToggleFirewallOffPersistsPref(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	workdir := t.TempDir()
	a := New(Options{Workdir: workdir})
	resolver, err := credentials.NewResolver(workdir)
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}
	a.resolver = resolver
	on := true
	a.firewallOverride = &on

	a.toggleFirewall()

	prefs, err := config.LoadProjectPrefs(workdir)
	if err != nil {
		t.Fatalf("LoadProjectPrefs: %v", err)
	}
	if prefs.FirewallEnabled == nil || *prefs.FirewallEnabled {
		t.Fatalf("firewall pref = %+v, want false", prefs.FirewallEnabled)
	}
}
