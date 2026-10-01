package tui

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/posture"
)

func knowledgeTUI(t *testing.T) (*App, string) {
	t.Helper()
	t.Setenv("BELAI_HOME", t.TempDir())
	t.Setenv("VULNETIX_WEB_URL", "http://127.0.0.1:1")
	dir := t.TempDir()
	a := New(Options{Workdir: dir})
	// Guardrails off: ingestion sanitises and never calls a classifier here.
	a.live.Set(posture.AllIgnore(), true)
	return a, dir
}

func waitForHit(t *testing.T, a *App, query string, want bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		got := len(a.knowledgeStore().Set().Search(query, 0, nil)) > 0
		if got == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("search %q: found=%v, want %v", query, got, want)
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
