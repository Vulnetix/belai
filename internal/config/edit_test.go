package config

import (
	"encoding/json"
	"testing"
)

func TestEditToleranceDefaultsOn(t *testing.T) {
	if !(Settings{}).EditTolerant() || !(Settings{Edit: &EditSettings{}}).EditTolerant() {
		t.Fatal("edit.tolerant must default to on")
	}
	if (Settings{Edit: &EditSettings{Tolerant: ptr(false)}}).EditTolerant() {
		t.Fatal("edit.tolerant false must switch it off")
	}
}

func TestEditSettingsParse(t *testing.T) {
	var s Settings
	if err := json.Unmarshal([]byte(`{"edit":{"tolerant":false}}`), &s); err != nil {
		t.Fatal(err)
	}
	if s.EditTolerant() {
		t.Fatal("parsed edit.tolerant false was not honoured")
	}
}

func TestMergeEditProjectOnlyNarrows(t *testing.T) {
	if got := mergeEdit(nil, &EditSettings{Tolerant: ptr(true)}, true); got != nil {
		t.Fatalf("a project true alone must change nothing, got %+v", got)
	}
	user := &EditSettings{Tolerant: ptr(false)}
	if got := mergeEdit(user, &EditSettings{Tolerant: ptr(true)}, true); got != user || *got.Tolerant {
		t.Fatal("a project layer turned tolerance back on")
	}
	if got := mergeEdit(nil, &EditSettings{Tolerant: ptr(false)}, true); got == nil || *got.Tolerant {
		t.Fatal("a project layer could not turn tolerance off")
	}
	if got := mergeEdit(user, &EditSettings{Tolerant: ptr(true)}, false); got == nil || !*got.Tolerant {
		t.Fatal("a user layer must be able to turn tolerance on")
	}
	if got := mergeEdit(user, nil, true); got != user {
		t.Fatal("a nil layer changed the settings")
	}
}

func TestOverrideProjectCannotEnableEditTolerance(t *testing.T) {
	global := Settings{Edit: &EditSettings{Tolerant: ptr(false)}}
	if global.Override(Settings{Edit: &EditSettings{Tolerant: ptr(true)}}).EditTolerant() {
		t.Fatal("a project layer enabled tolerant edits")
	}
	if (Settings{}).Override(Settings{Edit: &EditSettings{Tolerant: ptr(false)}}).EditTolerant() {
		t.Fatal("a project layer could not disable tolerant edits")
	}
}

func TestResolveEditProjectOffOnly(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	workdir := t.TempDir()
	off, on := false, true
	if err := SaveGlobal(Settings{Edit: &EditSettings{Tolerant: &off}}); err != nil {
		t.Fatal(err)
	}
	if err := SaveProject(workdir, Settings{Edit: &EditSettings{Tolerant: &on}}); err != nil {
		t.Fatal(err)
	}
	eff, err := Resolve(workdir, func(string) string { return "" }, Settings{})
	if err != nil {
		t.Fatal(err)
	}
	if eff.Settings.EditTolerant() {
		t.Fatal("a project layer must not turn tolerant edits on")
	}
	if eff.Origin["edit"] != SourceGlobal {
		t.Fatalf("origin = %v", eff.Origin["edit"])
	}

	if err := SaveGlobal(Settings{}); err != nil {
		t.Fatal(err)
	}
	if err := SaveProject(workdir, Settings{Edit: &EditSettings{Tolerant: &off}}); err != nil {
		t.Fatal(err)
	}
	eff, err = Resolve(workdir, func(string) string { return "" }, Settings{})
	if err != nil {
		t.Fatal(err)
	}
	if eff.Settings.EditTolerant() || eff.Origin["edit"] != SourceProject {
		t.Fatalf("project off not honoured: %v", eff.Origin["edit"])
	}

	if err := SaveGlobal(Settings{Edit: &EditSettings{Tolerant: &on}}); err != nil {
		t.Fatal(err)
	}
	if err := SaveProject(workdir, Settings{}); err != nil {
		t.Fatal(err)
	}
	eff, err = Resolve(workdir, func(string) string { return "" }, Settings{})
	if err != nil {
		t.Fatal(err)
	}
	if !eff.Settings.EditTolerant() {
		t.Fatal("a user layer must be able to set it on")
	}
}
