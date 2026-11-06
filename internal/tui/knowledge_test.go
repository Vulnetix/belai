package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/posture"
)

func knowledgeTUI(t *testing.T) (*App, string) {
	t.Helper()
	t.Setenv("BELAI_HOME", t.TempDir())
	t.Setenv("VULNETIX_WEB_URL", "http://127.0.0.1:1")
	dir := t.TempDir()
	a := New(Options{Workdir: dir})
	// Guardrails off: ingestion sanitises and never calls a classifier here.
	// The session override, not only the live holder: any settings reload
	// (git sync, a settings write) recomputes the live posture through
	// syncPosture, and on the CI runners one landed mid-test and put the
	// default posture back, so the gate failed the document closed.
	off := false
	a.guardrailsOverride = &off
	a.live.Set(posture.AllIgnore(), true)
	return a, dir
}

func waitForHit(t *testing.T, a *App, query string, want bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		got := len(a.knowledgeStore().Set().Search(query, 0, nil)) > 0
		if got == want {
			return
		}
		if time.Now().After(deadline) {
			// Say why: the background refresh swallows its report, so run one
			// here and show what it skipped, dropped or failed.
			rep, err := a.knowledgeStore().Refresh(context.Background())
			t.Fatalf("search %q: found=%v, want %v\nrefresh: err=%v profile=%+v project=%+v warnings=%q store warnings=%q",
				query, got, want, err, rep.Profile, rep.Project, rep.Warnings, a.knowledgeStore().Warnings())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestEngagedProfileDocumentsFollowTheEngagedAgentInEveryMode(t *testing.T) {
	a, _ := knowledgeTUI(t)
	docs := t.TempDir()
	if err := os.WriteFile(filepath.Join(docs, "auth.md"), []byte("Rotate the signing key every ninety days and revoke tokens on logout."), 0o600); err != nil {
		t.Fatal(err)
	}
	p := agentprofile.AgentProfile{
		Name: "reviewer", Description: "d", SystemPrompt: "s", Mode: agentprofile.ModeSingle,
		Knowledge: &agentprofile.KnowledgeSpec{Paths: []string{docs}},
	}
	if _, err := agentprofile.Save(p); err != nil {
		t.Fatal(err)
	}
	store := a.knowledgeStore()
	set := store.Set()
	if !set.Empty() {
		t.Fatal("nothing is searchable before an agent is engaged")
	}

	a.mode = "agent"
	a.setNamedAgent("reviewer")
	waitForHit(t, a, "signing key rotation", true)

	// Plan and goal mode keep the engaged agent dormant, and its documents
	// stay searchable: the session's Set is the one taken before.
	for _, mode := range []string{"plan", "goal"} {
		a.mode = mode
		if a.engagedAgent() != "" {
			t.Fatalf("%s mode: the agent is dormant for tools and carrier", mode)
		}
		a.syncKnowledge()
		if len(set.Search("signing key rotation", 0, nil)) == 0 {
			t.Fatalf("%s mode lost the engaged agent's documents", mode)
		}
	}

	a.clearEngagedAgent()
	if len(set.Search("signing key rotation", 0, nil)) != 0 {
		t.Fatal("clearing the agent must stop its documents being searchable")
	}
}

func TestAttachedFilesAreIndexedForTheSessionOnly(t *testing.T) {
	a, dir := knowledgeTUI(t)
	att := &attachment{raw: "notes/retries.md", text: "@notes/retries.md", root: dir, body: "The retry budget is three attempts with exponential backoff."}
	a.indexAttachment(att)
	hits := a.knowledgeStore().Set().Search("retry budget attempts backoff", 0, nil)
	if len(hits) == 0 || hits[0].Address != "kb+session/notes/retries.md" {
		t.Fatalf("hits = %+v", hits)
	}
	// A directory listing, an image and an empty body are not indexed.
	a.indexAttachment(&attachment{raw: "dir", isDir: true, body: "listing of entries and files here"})
	a.indexAttachment(&attachment{raw: "p.png", img: &attachedImage{}, body: "x"})
	a.indexAttachment(&attachment{raw: "empty.md"})
	if n := len(a.knowledgeStore().Docs()["session"]); n != 1 {
		t.Fatalf("session docs = %d, want 1", n)
	}
	// Nothing was written: a new App in the same project starts with none.
	b := New(Options{Workdir: dir})
	b.live.Set(posture.AllIgnore(), true)
	if h := b.knowledgeStore().Set().Search("retry budget attempts backoff", 0, nil); len(h) != 0 {
		t.Fatalf("session files must not persist: %+v", h)
	}
}

func TestSessionBuildCarriesTheKnowledgeStore(t *testing.T) {
	a, _ := knowledgeTUI(t)
	p := a.sessionBuildParams()
	if p.knowledge == nil || p.knowledge != a.knowStore {
		t.Fatal("the build params must carry the App's store")
	}
	if _, err := buildAgentSession(p); err != nil {
		t.Fatal(err)
	}
}

// K17: the project index is refreshed at session start and each turn, at most
// every twenty seconds.
func TestKnowledgeRefreshIntervalIsTwentySeconds(t *testing.T) {
	if knowledgeRefreshEvery != 20*time.Second {
		t.Fatalf("refresh interval = %v, docs/knowledge.md says 20 seconds", knowledgeRefreshEvery)
	}
}

func TestSourceIndexFollowsTheSettingAndReindexExplainsWhenOff(t *testing.T) {
	a, dir := knowledgeTUI(t)
	if err := os.WriteFile(filepath.Join(dir, "auth.go"), []byte("package auth\n\n// RotateSigningKey replaces the oldest key.\nfunc RotateSigningKey() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if a.knowledgeStore().SourceOn() {
		t.Fatal("the source index is off by default")
	}
	a.reindexSource()
	last := a.messages[len(a.messages)-1].Content
	if !strings.Contains(last, "knowledge.source_index") {
		t.Fatalf("reindex while off must say how to turn it on: %q", last)
	}
	on := true
	a.settings.Knowledge = &config.KnowledgeSettings{SourceIndex: &on}
	a.syncKnowledge()
	if !a.knowledgeStore().SourceOn() {
		t.Fatal("turning the setting on must turn the index on")
	}
	waitForHit(t, a, "RotateSigningKey", true)
	hits := a.knowledgeStore().Set().Search("RotateSigningKey", 0, nil)
	if len(hits) == 0 || hits[0].Address != "kb+src/auth.go" {
		t.Fatalf("hits = %+v", hits)
	}
	off := false
	a.settings.Knowledge = &config.KnowledgeSettings{SourceIndex: &off}
	a.syncKnowledge()
	waitForHit(t, a, "RotateSigningKey", false)
}
