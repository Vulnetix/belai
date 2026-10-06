package config

import "testing"

func TestGitSyncDefaultsOnAndTheProjectMayOnlyTurnItOff(t *testing.T) {
	on, off := true, false

	if !(Settings{}).GitSyncEnabled() || !(Settings{Git: &GitSettings{}}).GitSyncEnabled() {
		t.Fatal("git sync must default on")
	}
	if (Settings{Git: &GitSettings{Sync: &off}}).GitSyncEnabled() {
		t.Fatal("git.sync false must turn it off")
	}

	// The user's global layer turns it off; a repository cannot turn it back on.
	e := Effective{Settings: Settings{}, Origin: map[string]Source{}}
	e.apply(Settings{Git: &GitSettings{Sync: &off}}, SourceGlobal)
	e.apply(Settings{Git: &GitSettings{Sync: &on}}, SourceProject)
	if e.Settings.GitSyncEnabled() {
		t.Fatal("a project layer switched the git sync back on over the user's choice")
	}

	// A repository may opt out of it for itself.
	e = Effective{Settings: Settings{}, Origin: map[string]Source{}}
	e.apply(Settings{Git: &GitSettings{Sync: &on}}, SourceGlobal)
	e.apply(Settings{Git: &GitSettings{Sync: &off}}, SourceProject)
	if e.Settings.GitSyncEnabled() {
		t.Fatal("a project layer could not opt out of the git sync")
	}
}
