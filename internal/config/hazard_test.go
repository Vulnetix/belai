package config

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestHazardDefaultsFailClosed(t *testing.T) {
	var s Settings
	if !s.HazardAskOn() {
		t.Fatal("the floor must be on when nothing is set")
	}
	if s.HazardYoloWaived() {
		t.Fatal("the waive must be off when nothing is set")
	}
	if len(s.ProtectedPathList()) != 0 {
		t.Fatal("no extra paths by default")
	}
}

func TestPermissionsDecodeHazardKeys(t *testing.T) {
	var s Settings
	in := `{"permissions":{"allow":["Read"],"hazard_ask":false,"hazard_yolo_waive":true,"protected_paths":["docs/private","~/notes","docs/private"]}}`
	if err := json.Unmarshal([]byte(in), &s); err != nil {
		t.Fatal(err)
	}
	if s.HazardAskOn() || !s.HazardYoloWaived() {
		t.Fatalf("switches not read: %+v", s.Permissions)
	}
	if !reflect.DeepEqual(s.Permissions.Allow, []string{"Read"}) {
		t.Fatalf("rules lost: %v", s.Permissions.Allow)
	}
	if !reflect.DeepEqual(s.ProtectedPathList(), []string{"docs/private", "~/notes"}) {
		t.Fatalf("paths = %v", s.ProtectedPathList())
	}
	// Only hazard keys: no rule bucket at all.
	var only Settings
	if err := json.Unmarshal([]byte(`{"permissions":{"hazard_ask":true}}`), &only); err != nil {
		t.Fatal(err)
	}
	if only.Permissions.HazardAsk == nil || !*only.Permissions.HazardAsk {
		t.Fatalf("lone key lost: %+v", only.Permissions)
	}
	// Round trip.
	b, err := json.Marshal(s.Permissions)
	if err != nil {
		t.Fatal(err)
	}
	var back PermissionRules
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(back, s.Permissions) {
		t.Fatalf("round trip: %+v != %+v", back, s.Permissions)
	}
}

func TestPermissionsMalformedHazardKeysFailTheLoad(t *testing.T) {
	for _, in := range []string{
		`{"permissions":{"hazard_ask":"no"}}`,
		`{"permissions":{"hazard_ask":0}}`,
		`{"permissions":{"hazard_yolo_waive":"yes"}}`,
		`{"permissions":{"protected_paths":"docs"}}`,
		`{"permissions":{"protected_paths":[1]}}`,
		`{"permissions":{"allow":["Read"],"hazard_unknown":true}}`,
	} {
		var s Settings
		if err := json.Unmarshal([]byte(in), &s); err == nil {
			t.Errorf("%s was accepted", in)
		}
	}
}

func TestProjectLayerCannotTurnTheFloorOffOrWaiveIt(t *testing.T) {
	f, tr := false, true
	user := PermissionRules{HazardAsk: &tr}
	proj := PermissionRules{HazardAsk: &f, HazardYoloWaive: &tr, ProtectedPaths: []string{"vendor/secret"}}
	got := user.mergeLayer(proj, true)
	if got.HazardAsk == nil || !*got.HazardAsk {
		t.Fatalf("a project layer turned the floor off: %+v", got)
	}
	if got.HazardYoloWaive != nil {
		t.Fatalf("a project layer set the waive: %+v", got)
	}
	if !reflect.DeepEqual(got.ProtectedPaths, []string{"vendor/secret"}) {
		t.Fatalf("a project layer could not add a path: %v", got.ProtectedPaths)
	}
	// Merge is the project form.
	if m := (PermissionRules{}).Merge(proj); m.HazardAsk != nil || m.HazardYoloWaive != nil {
		t.Fatalf("Merge applied project switches: %+v", m)
	}
	// A user layer sets both ways and keeps earlier paths.
	u2 := got.mergeLayer(PermissionRules{HazardAsk: &f, HazardYoloWaive: &tr, ProtectedPaths: []string{"a"}}, false)
	if u2.HazardAsk == nil || *u2.HazardAsk || u2.HazardYoloWaive == nil || !*u2.HazardYoloWaive {
		t.Fatalf("a user layer could not set the switches: %+v", u2)
	}
	if !reflect.DeepEqual(u2.ProtectedPaths, []string{"vendor/secret", "a"}) {
		t.Fatalf("paths = %v", u2.ProtectedPaths)
	}
}

func TestOverrideProjectCannotLoosenTheFloor(t *testing.T) {
	tr, f := true, false
	global := Settings{Permissions: PermissionRules{HazardYoloWaive: &f}}
	got := global.Override(Settings{Permissions: PermissionRules{HazardAsk: &f, HazardYoloWaive: &tr}})
	if !got.HazardAskOn() || got.HazardYoloWaived() {
		t.Fatalf("project override loosened the floor: %+v", got.Permissions)
	}
}

func TestResolveHazardLayers(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	workdir := t.TempDir()
	f, tr := false, true
	if err := SaveProject(workdir, Settings{Permissions: PermissionRules{HazardAsk: &f, HazardYoloWaive: &tr, ProtectedPaths: []string{"build/keys"}}}); err != nil {
		t.Fatal(err)
	}
	eff, err := Resolve(workdir, func(string) string { return "" }, Settings{})
	if err != nil {
		t.Fatal(err)
	}
	if !eff.Settings.HazardAskOn() || eff.Settings.HazardYoloWaived() {
		t.Fatalf("project switches took effect: %+v", eff.Settings.Permissions)
	}
	if !reflect.DeepEqual(eff.Settings.ProtectedPathList(), []string{"build/keys"}) {
		t.Fatalf("project path not added: %v", eff.Settings.ProtectedPathList())
	}
	noted := false
	for _, n := range eff.Notes {
		if strings.Contains(n, "hazard_ask") {
			noted = true
		}
	}
	if !noted {
		t.Fatalf("no note for the ignored project switches: %v", eff.Notes)
	}
	// The user's own layer sets them.
	if err := SaveProject(workdir, Settings{}); err != nil {
		t.Fatal(err)
	}
	if err := SaveGlobal(Settings{Permissions: PermissionRules{HazardYoloWaive: &tr}}); err != nil {
		t.Fatal(err)
	}
	eff, err = Resolve(workdir, func(string) string { return "" }, Settings{})
	if err != nil {
		t.Fatal(err)
	}
	if !eff.Settings.HazardYoloWaived() || !eff.Settings.HazardAskOn() {
		t.Fatalf("user waive lost: %+v", eff.Settings.Permissions)
	}
}

func TestValidateHazard(t *testing.T) {
	ok := Settings{Permissions: PermissionRules{ProtectedPaths: []string{"docs/private", "~/notes", "*.tfstate"}}}
	if err := ValidateHazard(ok); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", " x", "a/../b", "a\nb", "a‮b", strings.Repeat("a", MaxProtectedPathBytes+1)} {
		s := Settings{Permissions: PermissionRules{ProtectedPaths: []string{bad}}}
		if err := ValidateHazard(s); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
	many := make([]string, MaxProtectedPaths+1)
	for i := range many {
		many[i] = "p" + strings.Repeat("x", i)
	}
	if err := ValidateHazard(Settings{Permissions: PermissionRules{ProtectedPaths: many}}); err == nil {
		t.Fatal("too many entries were accepted")
	}
}
