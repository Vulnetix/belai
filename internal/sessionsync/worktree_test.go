package sessionsync

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"testing"
)

// The branch, workspace, worktree and checkout fields carry the exact names
// the website reads, and an empty one is left out.
func TestWorktreeAndCheckoutFieldsRoundTrip(t *testing.T) {
	d := Dispatch{ID: "d1", Kind: "start", Cwd: "/w", Prompt: "p", Branch: "feat/x", Workspace: "acme/app/feat-x"}
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if m["branch"] != "feat/x" || m["workspace"] != "acme/app/feat-x" {
		t.Fatalf("dispatch = %s", b)
	}
	var back Dispatch
	if err := json.Unmarshal(b, &back); err != nil || !reflect.DeepEqual(back, d) {
		t.Fatalf("round trip = %+v, %v", back, err)
	}
	empty, _ := json.Marshal(Dispatch{Kind: "start"})
	var em map[string]any
	if err := json.Unmarshal(empty, &em); err != nil {
		t.Fatal(err)
	}
	if _, ok := em["branch"]; ok {
		t.Fatalf("an empty branch was sent: %s", empty)
	}
	if _, ok := em["workspace"]; ok {
		t.Fatalf("an empty workspace was sent: %s", empty)
	}

	info := RCInfo{Features: []string{FeatureWorktree, FeatureCheckout}, Dirs: []RCDir{{
		Path: "/w", Name: "w", Source: "trusted",
		Worktrees: []RCWorktree{{Path: "/home/u/.vulnetix/worktrees/acme/app/feat-x", Branch: "feat/x", Workspace: "acme/app/feat-x"}},
	}}}
	b, err = json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	var raw struct {
		Features []string `json:"features"`
		Dirs     []struct {
			Worktrees []map[string]string `json:"worktrees"`
		} `json:"dirs"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(raw.Features, []string{"worktree", "checkout"}) || len(raw.Dirs) != 1 ||
		!reflect.DeepEqual(raw.Dirs[0].Worktrees, []map[string]string{{"path": "/home/u/.vulnetix/worktrees/acme/app/feat-x", "branch": "feat/x", "workspace": "acme/app/feat-x"}}) {
		t.Fatalf("rc info = %s", b)
	}

	var c RemoteCommand
	if err := json.Unmarshal([]byte(`{"id":"c1","sessionId":"s","checkout":"main","interrupt":true}`), &c); err != nil {
		t.Fatal(err)
	}
	if c.Checkout != "main" || !c.Interrupt {
		t.Fatalf("command = %+v", c)
	}
}

// A host started without session controls refuses a branch switch too.
func TestCheckoutRefusedWithoutControls(t *testing.T) {
	fake := newFake()
	fake.commands = []RemoteCommand{{ID: "c3", SessionID: testSess, Checkout: "main"}}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	path := filepath.Join(t.TempDir(), testSess+".jsonl")
	appendLines(t, path, line("a", "user", "1"))
	s := startSyncer(t, srv, true)
	s.Activate(SessionInfo{ID: testSess, Path: path})
	eventually(t, "refusal", func() bool { fake.mu.Lock(); defer fake.mu.Unlock(); return fake.acks["command:c3"] == AckRefused })
}
