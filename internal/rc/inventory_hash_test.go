package rc

import (
	"testing"

	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/config"
)

// The inventory carries the hash of each stored profile and crew as the library
// stores it, so the website can tell which library version a host holds without
// the host sending any text. The hash is the one the automatic sync computes
// (profileDocument and crewDocument are shared), and it follows sync.profiles.
func TestInventoryCarriesTheHashOfAStoredProfileAndCrew(t *testing.T) {
	home(t)
	p := worker("log", libID)
	if _, err := agentprofile.Save(p); err != nil {
		t.Fatal(err)
	}
	saveCrew(t, crewID, "aws-infra", "log")

	inv := LocalInventory()
	var gotP, builtinP string
	for _, x := range inv.Profiles {
		switch {
		case x.Name == "log":
			gotP = x.SHA256
		case x.Builtin && x.SHA256 != "":
			builtinP = x.Name
		}
	}
	stored, err := agentprofile.Load("log")
	if err != nil {
		t.Fatal(err)
	}
	md, _ := profileDocument(stored)
	if gotP == "" || gotP != hashOf(md) {
		t.Errorf("profile hash = %q, want %q", gotP, hashOf(md))
	}
	if builtinP != "" {
		t.Errorf("a built-in profile %q carries a hash", builtinP)
	}

	var gotC string
	for _, c := range inv.Crews {
		if c.ID == crewID {
			gotC = c.SHA256
		} else if c.Builtin && c.SHA256 != "" {
			t.Errorf("a built-in crew %q carries a hash", c.Name)
		}
	}
	crew, _ := agentprofile.CrewByID(crewID)
	js, _ := crewDocument(crew)
	if gotC == "" || gotC != hashOf(js) {
		t.Errorf("crew hash = %q, want %q", gotC, hashOf(js))
	}
}

// A changed profile or crew changes its hash and the catalogue hash, so the
// daemon advertises it again.
func TestInventoryHashFollowsTheStoredBytes(t *testing.T) {
	home(t)
	p := worker("log", libID)
	if _, err := agentprofile.Save(p); err != nil {
		t.Fatal(err)
	}
	before := LocalInventory()
	p.Description = "changed on the host"
	if _, err := agentprofile.Save(p); err != nil {
		t.Fatal(err)
	}
	after := LocalInventory()
	find := func(inv Inventory) string {
		for _, x := range inv.Profiles {
			if x.Name == "log" {
				return x.SHA256
			}
		}
		return ""
	}
	if find(before) == "" || find(before) == find(after) {
		t.Errorf("hash before %q, after %q: an edit must change it", find(before), find(after))
	}
	if before.catalogueHash() == after.catalogueHash() {
		t.Error("the catalogue hash ignores the profile hash, so the website would never hear of an edit")
	}
}

// With sync.profiles off nothing about a profile's or crew's bytes leaves the host.
func TestInventoryHashIsOmittedWithProfileSyncOff(t *testing.T) {
	home(t)
	if _, err := agentprofile.Save(worker("log", libID)); err != nil {
		t.Fatal(err)
	}
	saveCrew(t, crewID, "aws-infra", "log")
	off := false
	if err := config.Mutate(config.ScopeGlobal, "", func(s *config.Settings) error {
		s.Sync = &config.SyncSettings{Profiles: &off}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	inv := LocalInventory()
	for _, x := range inv.Profiles {
		if x.SHA256 != "" {
			t.Errorf("profile %q reports a hash with sync.profiles off", x.Name)
		}
	}
	for _, c := range inv.Crews {
		if c.SHA256 != "" {
			t.Errorf("crew %q reports a hash with sync.profiles off", c.Name)
		}
	}
}

// With sync.profiles off the heartbeat renders and hashes nothing: the flag is
// tested before the document is built, not after.
func TestInventoryRendersNoProfileWithProfileSyncOff(t *testing.T) {
	home(t)
	if _, err := agentprofile.Save(worker("log", libID)); err != nil {
		t.Fatal(err)
	}
	renders := 0
	orig := marshalProfile
	marshalProfile = func(p agentprofile.AgentProfile) ([]byte, error) {
		renders++
		return orig(p)
	}
	t.Cleanup(func() { marshalProfile = orig })

	LocalInventory()
	if renders == 0 {
		t.Fatal("with sync.profiles on, the inventory renders the profile it hashes")
	}

	off := false
	if err := config.Mutate(config.ScopeGlobal, "", func(s *config.Settings) error {
		s.Sync = &config.SyncSettings{Profiles: &off}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	renders = 0
	LocalInventory()
	if renders != 0 {
		t.Fatalf("the inventory rendered %d profile(s) with sync.profiles off", renders)
	}
}
