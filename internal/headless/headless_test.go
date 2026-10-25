package headless

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/kanban"
	"github.com/vulnetix/belai/internal/testpass"
)

func boolPtr(b bool) *bool { return &b }

// syncSettings returns settings with kanban and sync at the given values.
func syncSettings(kanbanOn, syncOn bool) config.Settings {
	return config.Settings{Kanban: boolPtr(kanbanOn), Sync: &config.SyncSettings{Enabled: boolPtr(syncOn)}}
}

// tokenCredential writes the Vulnetix CLI's token credential into workdir's
// .vulnetix/credentials.json: a bearer token shaped like a JWT (two dots)
// is a usable credential for sessionsync.UsableCredential.
func tokenCredential(t *testing.T, workdir string) {
	t.Helper()
	dir := filepath.Join(workdir, ".vulnetix")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	cred := `{"org_id":"o","method":"token","token":"header.payload.signature"}`
	if err := os.WriteFile(filepath.Join(dir, "credentials.json"), []byte(cred), 0o600); err != nil {
		t.Fatal(err)
	}
}

// emptyHome points the credential lookup at a directory with no credentials.
func emptyHome(t *testing.T) {
	t.Helper()
	t.Setenv("VULNETIX_API_TOKEN", "")
	t.Setenv("VULNETIX_API_KEY", "")
	t.Setenv("VULNETIX_ORG_ID", "")
	t.Setenv("VVD_ORG", "")
	t.Setenv("VVD_SECRET", "")
	t.Setenv("VULNETIX_CREDENTIALS_DIR", t.TempDir())
}

func TestHostID(t *testing.T) {
	t.Run("mints and persists", func(t *testing.T) {
		t.Setenv("BELAI_HOME", t.TempDir())
		id := HostID()
		if id == "" {
			t.Fatal("HostID() = \"\", want a minted id")
		}
		if again := HostID(); again != id {
			t.Fatalf("HostID() = %q on the second call, want the persisted %q", again, id)
		}
	})

	t.Run("no home returns empty", func(t *testing.T) {
		t.Setenv("BELAI_HOME", "")
		t.Setenv("HOME", "")
		if id := HostID(); id != "" {
			t.Fatalf("HostID() = %q, want \"\" when the global dir cannot be located", id)
		}
	})
}

func TestSyncClient(t *testing.T) {
	t.Setenv("VULNETIX_WEB_URL", "http://127.0.0.1:1")
	t.Run("kanban off returns nil", func(t *testing.T) {
		if c := SyncClient(syncSettings(false, true), t.TempDir()); c != nil {
			t.Fatalf("SyncClient() = %+v, want nil with kanban off", c)
		}
	})
	t.Run("sync off returns nil", func(t *testing.T) {
		if c := SyncClient(syncSettings(true, false), t.TempDir()); c != nil {
			t.Fatalf("SyncClient() = %+v, want nil with sync off", c)
		}
	})
	t.Run("missing credential returns nil", func(t *testing.T) {
		emptyHome(t)
		if c := SyncClient(syncSettings(true, true), t.TempDir()); c != nil {
			t.Fatalf("SyncClient() = %+v, want nil with no credential", c)
		}
	})
	t.Run("usable credential builds a client", func(t *testing.T) {
		emptyHome(t)
		workdir := t.TempDir()
		tokenCredential(t, workdir)
		c := SyncClient(syncSettings(true, true), workdir)
		if c == nil {
			t.Fatal("SyncClient() = nil, want a client with a usable credential")
		}
		if c.Base != "http://127.0.0.1:1/api/site/v1/belai" {
			t.Fatalf("client.Base = %q, want the loopback VULNETIX_WEB_URL api path", c.Base)
		}
	})
}

func TestA2ASyncClient(t *testing.T) {
	t.Setenv("VULNETIX_WEB_URL", "http://127.0.0.1:1")
	t.Run("sync off returns nil", func(t *testing.T) {
		if c := a2aSyncClient(config.Settings{}, t.TempDir()); c != nil {
			t.Fatalf("a2aSyncClient() = %+v, want nil with sync off", c)
		}
	})
	t.Run("missing credential returns nil", func(t *testing.T) {
		emptyHome(t)
		if c := a2aSyncClient(config.Settings{}, t.TempDir()); c != nil {
			t.Fatalf("a2aSyncClient() = %+v, want nil with no credential", c)
		}
	})
	t.Run("usable credential builds a client without the board setting", func(t *testing.T) {
		emptyHome(t)
		workdir := t.TempDir()
		tokenCredential(t, workdir)
		// a2a relays whatever the board setting says: kanban off.
		if c := a2aSyncClient(syncSettings(false, true), workdir); c == nil {
			t.Fatal("a2aSyncClient() = nil, want a client with sync on and kanban off")
		}
	})
}

func TestKanban(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	t.Run("disabled returns nils", func(t *testing.T) {
		if store, src := Kanban(t.TempDir(), "s", syncSettings(false, true)); store != nil || src != nil {
			t.Fatalf("Kanban() = %v, %v, want nils with kanban off", store, src)
		}
	})
	t.Run("error opening the default board returns nils", func(t *testing.T) {
		// An unlocatable global dir makes OpenDefault fail.
		t.Setenv("BELAI_HOME", "")
		t.Setenv("HOME", "")
		if store, src := Kanban(t.TempDir(), "s", syncSettings(true, true)); store != nil || src != nil {
			t.Fatalf("Kanban() = %v, %v, want nils when the board cannot open", store, src)
		}
	})
	t.Run("empty session id is minted", func(t *testing.T) {
		store, src := Kanban(t.TempDir(), "", syncSettings(true, true))
		if store == nil {
			t.Fatal("Kanban() store = nil, want the default board")
		}
		if src == nil {
			t.Fatal("Kanban() source = nil, want a provenance source")
		}
		if id := src.Get().SessionID; id == "" {
			t.Fatal("source.SessionID = \"\", want a minted id")
		}
	})
	t.Run("provenance uses the host id", func(t *testing.T) {
		host := HostID()
		workdir := t.TempDir()
		_, src := Kanban(workdir, "s1", syncSettings(true, true))
		if src == nil {
			t.Fatal("Kanban() source = nil")
		}
		prov := src.Get()
		if prov.HostID != host {
			t.Fatalf("provenance.HostID = %q, want HostID() %q", prov.HostID, host)
		}
		if prov.SessionID != "s1" {
			t.Fatalf("provenance.SessionID = %q, want s1", prov.SessionID)
		}
		if prov.Dir == "" {
			t.Fatal("provenance.Dir is empty, want the workdir")
		}
	})
}

func TestFlushKanban(t *testing.T) {
	t.Setenv("VULNETIX_WEB_URL", "http://127.0.0.1:1")
	t.Run("disabled settings return early", func(t *testing.T) {
		FlushKanban(syncSettings(false, true), t.TempDir())
		FlushKanban(syncSettings(true, false), t.TempDir())
	})
	t.Run("empty outbox returns early", func(t *testing.T) {
		t.Setenv("BELAI_HOME", t.TempDir())
		emptyHome(t)
		FlushKanban(syncSettings(true, true), t.TempDir())
	})
	t.Run("non-empty outbox without a client returns early", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("BELAI_HOME", home)
		emptyHome(t)
		store, err := kanban.OpenDefault()
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.Add(kanban.ItemInput{Title: "flush me"}, kanban.Provenance{}); err != nil {
			t.Fatal(err)
		}
		FlushKanban(syncSettings(true, true), t.TempDir())
	})
	t.Run("best-effort flush of a dirty outbox", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("BELAI_HOME", home)
		emptyHome(t)
		store, err := kanban.OpenDefault()
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.Add(kanban.ItemInput{Title: "flush me"}, kanban.Provenance{}); err != nil {
			t.Fatal(err)
		}
		workdir := t.TempDir()
		tokenCredential(t, workdir)
		// The loopback client has no server behind it: the flush must be
		// swallowed, not panic or hang.
		FlushKanban(syncSettings(true, true), workdir)
	})
}

// A non-session trigger never reaches the dirty-tree scan, so it passes
// through even where git cannot run (the inherited harness GIT_* env).
func TestShouldPostEndNonSessionTriggersSkipTheTreeScan(t *testing.T) {
	ctx := context.Background()
	if !ShouldPostEnd(ctx, testsSettings("goal"), t.TempDir(), testpass.TriggerGoal) {
		t.Fatal("the goal trigger must pass through without a changed tree")
	}
	if ShouldPostEnd(ctx, testsSettings("off"), t.TempDir(), testpass.TriggerGoal) {
		t.Fatal("the off level must never run")
	}
}
