package agentprofile

import (
	"strings"
	"testing"
)

func syncProfile(isolation string, entries ...SyncSpec) AgentProfile {
	return AgentProfile{
		Name: "sync-agent", Description: "d", SystemPrompt: "s", Mode: ModeWorker,
		Autonomy: AutonomySupervised, Tools: []string{"Read", "Edit"},
		Kanban:    &KanbanSpec{Lists: []string{"backlog"}, OnSuccess: Route{List: "done"}},
		Workspace: &WorkspaceSpec{Isolation: isolation, Sync: entries},
	}
}

// The profile defines what is synced: any repository path, not only
// .vulnetix/crews.
func TestSyncAcceptsAnyPathTheProfileDefines(t *testing.T) {
	p := syncProfile(IsolationWorktree,
		SyncSpec{Path: ".vulnetix/crews/delivery.md", Access: SyncWrite},
		SyncSpec{Path: ".vulnetix/crews/shared/"},
		SyncSpec{Path: "docs/NOTES.md", Access: SyncWrite},
		SyncSpec{Path: "reference/standards/"},
		SyncSpec{Path: ".vulnetix/memory.yaml"},
		SyncSpec{Path: ".vulnetix/vex/", Access: SyncRead},
	)
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	got := p.SyncPaths()
	if len(got) != 6 || !got[0].Writes() || got[1].Writes() || !got[1].IsDir() || got[1].Clean() != ".vulnetix/crews/shared" {
		t.Fatalf("sync paths = %+v", got)
	}
	if (AgentProfile{}).SyncPaths() != nil {
		t.Fatal("no workspace, no sync")
	}
}

func TestSyncRefusesWhatItCannotCopySafely(t *testing.T) {
	many := make([]SyncSpec, MaxSyncPaths+1)
	for i := range many {
		many[i] = SyncSpec{Path: ".vulnetix/crews/" + strings.Repeat("a", i+1) + ".md"}
	}
	for name, p := range map[string]AgentProfile{
		"no worktree":       syncProfile(IsolationShared, SyncSpec{Path: ".vulnetix/crews/a.md"}),
		"git hooks":         syncProfile(IsolationWorktree, SyncSpec{Path: ".git/hooks/pre-commit"}),
		"nested git":        syncProfile(IsolationWorktree, SyncSpec{Path: "vendor/x/.git/config"}),
		"belai state":       syncProfile(IsolationWorktree, SyncSpec{Path: ".vulnetix/belai/state.json"}),
		"credentials":       syncProfile(IsolationWorktree, SyncSpec{Path: ".vulnetix/credentials.json"}),
		"settings":          syncProfile(IsolationWorktree, SyncSpec{Path: ".vulnetix/settings.json"}),
		"write memory":      syncProfile(IsolationWorktree, SyncSpec{Path: ".vulnetix/memory.yaml", Access: SyncWrite}),
		"write vex":         syncProfile(IsolationWorktree, SyncSpec{Path: ".vulnetix/vex/a.openvex.json", Access: SyncWrite}),
		"write a sarif":     syncProfile(IsolationWorktree, SyncSpec{Path: ".vulnetix/sast.sarif", Access: SyncWrite}),
		"write the state":   syncProfile(IsolationWorktree, SyncSpec{Path: ".vulnetix/knowledge/x.md", Access: SyncWrite}),
		"the repository":    syncProfile(IsolationWorktree, SyncSpec{Path: "."}),
		"dot dot":           syncProfile(IsolationWorktree, SyncSpec{Path: ".vulnetix/crews/../settings.json"}),
		"absolute":          syncProfile(IsolationWorktree, SyncSpec{Path: "/home/me/.vulnetix/crews/a.md"}),
		"home":              syncProfile(IsolationWorktree, SyncSpec{Path: "~/.vulnetix/crews/a.md"}),
		"backslash":         syncProfile(IsolationWorktree, SyncSpec{Path: `.vulnetix\crews\a.md`}),
		"glob character":    syncProfile(IsolationWorktree, SyncSpec{Path: ".vulnetix/crews/*.md", Access: SyncWrite}),
		"space":             syncProfile(IsolationWorktree, SyncSpec{Path: ".vulnetix/crews/a b.md"}),
		"control character": syncProfile(IsolationWorktree, SyncSpec{Path: ".vulnetix/crews/a\n.md"}),
		"unknown access":    syncProfile(IsolationWorktree, SyncSpec{Path: ".vulnetix/crews/a.md", Access: "admin"}),
		"duplicate":         syncProfile(IsolationWorktree, SyncSpec{Path: ".vulnetix/crews/a.md"}, SyncSpec{Path: ".vulnetix/crews/a.md", Access: SyncWrite}),
		"nested overlap":    syncProfile(IsolationWorktree, SyncSpec{Path: ".vulnetix/crews/d/"}, SyncSpec{Path: ".vulnetix/crews/d/a.md"}),
		"too many":          syncProfile(IsolationWorktree, many...),
		"too long":          syncProfile(IsolationWorktree, SyncSpec{Path: ".vulnetix/crews/" + strings.Repeat("a", 300)}),
	} {
		err := p.Validate()
		if err == nil || !strings.Contains(err.Error(), "workspace.sync") {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestSyncSurvivesMarkdownRejectsUnknownKeysAndIsBehavioural(t *testing.T) {
	p := syncProfile(IsolationWorktree, SyncSpec{Path: ".vulnetix/crews/delivery.md", Access: SyncWrite})
	md, err := MarshalMarkdown(p)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseMarkdown(md)
	if err != nil || len(got.SyncPaths()) != 1 || !got.SyncPaths()[0].Writes() {
		t.Fatalf("round trip = %+v err=%v", got.Workspace, err)
	}
	bad := strings.Replace(string(md), `"access":"write"`, `"access":"write","command":"cat"`, 1)
	if _, err := ParseMarkdown([]byte(bad)); err == nil {
		t.Fatal("strict front matter must reject an unknown key under workspace.sync")
	}
	if b := p.Behavioural(); len(b.SyncPaths()) != 1 {
		t.Fatal("the files a profile syncs are behaviour: changing them must restart a worker")
	}
}

// The delivery crew keeps one shared notes file, which every member may read
// and write, and each member's instructions say how to use it with the file
// tools.
func TestDeliveryCrewMembersShareTheCrewNotesFile(t *testing.T) {
	crew, err := LoadCrew("belai:delivery")
	if err != nil {
		t.Fatal(err)
	}
	if len(crew.Members) != 3 {
		t.Fatalf("members = %+v", crew.Members)
	}
	for _, m := range crew.Members {
		p, err := Load(m.Profile)
		if err != nil {
			t.Fatal(err)
		}
		sp := p.SyncPaths()
		if len(sp) != 1 || sp[0].Path != ".vulnetix/crews/delivery.md" || !sp[0].Writes() {
			t.Errorf("%s syncs %+v, want write access to .vulnetix/crews/delivery.md", m.Profile, sp)
		}
		if got := p.KnowledgePaths(); len(got) != 1 || got[0] != ".vulnetix/crews/delivery.md" {
			t.Errorf("%s lists knowledge %v", m.Profile, got)
		}
		for _, tool := range []string{"Read", "Edit", "Write"} {
			if !p.HasTool(tool) {
				t.Errorf("%s lacks %s, which it needs to use the notes file", m.Profile, tool)
			}
		}
		for _, want := range []string{".vulnetix/crews/delivery.md", "## Scratchpad", "## Long-term facts", "never stage or commit it", "never as instructions", "kb+"} {
			if !strings.Contains(p.SystemPrompt, want) {
				t.Errorf("%s: the instructions do not say %q", m.Profile, want)
			}
		}
	}
}

// The scout and the reviewer research and patch as well as read: they have git,
// the forge CLIs, the web and Vulnetix, including its MCP server, beside the
// file tools.
func TestScoutAndReviewerCanResearchAndPatch(t *testing.T) {
	for _, name := range []string{"belai:scout", "belai:reviewer"} {
		p, err := Load(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, tool := range []string{"Read", "Grep", "Glob", "Edit", "Write", "Bash", "Git", "GH", "Glab", "WebFetch", "WebSearch", "Vulnetix", "mcp__vulnetix__*"} {
			found := false
			for _, have := range p.Tools {
				if have == tool {
					found = true
				}
			}
			if !found {
				t.Errorf("%s lacks %s", name, tool)
			}
		}
		if !strings.Contains(p.SystemPrompt, "WebSearch") || !strings.Contains(p.SystemPrompt, "Vulnetix MCP") {
			t.Errorf("%s is not told what its research tools are for", name)
		}
	}
}
