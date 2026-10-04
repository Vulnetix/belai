package libinstall

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/sessionsync"
)

const (
	idA = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	idB = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	ver = "202610011234"
)

// fakeSource serves library versions from memory.
type fakeSource struct {
	profiles map[string]string
	crew     sessionsync.CrewFetched
	reads    int
}

func (f *fakeSource) Profile(_ context.Context, library, _ string) (string, []sessionsync.FileRef, error) {
	f.reads++
	md, ok := f.profiles[library]
	if !ok {
		return "", nil, errors.New("not served")
	}
	return md, nil, nil
}

func (f *fakeSource) File(context.Context, string) ([]byte, error) {
	return nil, errors.New("no files")
}

func (f *fakeSource) Crew(context.Context, string, string) (sessionsync.CrewFetched, error) {
	return f.crew, nil
}

func profile(t *testing.T, name, id string) string {
	t.Helper()
	md, err := agentprofile.MarshalMarkdown(agentprofile.AgentProfile{
		Name: name, ID: id, Description: "d", SystemPrompt: "do", Mode: agentprofile.ModeSingle, DisplayName: "Agent " + name,
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(md)
}

func TestProfileInstallsOnceAndRefusesToReplaceWithoutSayingSo(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	src := &fakeSource{profiles: map[string]string{idA: profile(t, "builder", idA)}}
	var saved []string
	in := Installer{Src: src, Saved: func(kind, id string, _ []byte) { saved = append(saved, kind+":"+id) }}

	p, _, why := in.Profile(context.Background(), idA, ver, false)
	if why != "" || p.Name != "builder" {
		t.Fatalf("install: %+v %q", p, why)
	}
	if on, err := agentprofile.Load("builder"); err != nil || on.ID != idA {
		t.Fatalf("not on the host: %v", err)
	}
	if len(saved) != 1 || saved[0] != "agent:"+idA {
		t.Fatalf("saved = %v", saved)
	}
	if _, _, why := in.Profile(context.Background(), idA, ver, false); !strings.Contains(why, "already has a profile named builder") {
		t.Fatalf("a second install without replace: %q", why)
	}
	if _, _, why := in.Profile(context.Background(), idA, ver, true); why != "" {
		t.Fatalf("a replace of the same profile: %q", why)
	}
}

func TestProfileRefusesWhatIsNotTheProfileAskedFor(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	src := &fakeSource{profiles: map[string]string{
		idA:                                    profile(t, "builder", idB),          // markdown for another id
		idB:                                    "---\nname: x\n---\nno description", // not a valid profile
		"cccccccc-cccc-4ccc-8ccc-cccccccccccc": profile(t, "belai:sneaky", "cccccccc-cccc-4ccc-8ccc-cccccccccccc"),
	}}
	in := Installer{Src: src}
	cases := map[string]struct{ lib, version string }{
		"a path":                 {"../x", ver},
		"a bad version":          {idA, "latest"},
		"markdown of another id": {idA, ver},
		"an invalid profile":     {idB, ver},
		"a built-in name":        {"cccccccc-cccc-4ccc-8ccc-cccccccccccc", ver},
		"one not served":         {"dddddddd-dddd-4ddd-8ddd-dddddddddddd", ver},
	}
	for name, c := range cases {
		if _, _, why := in.Profile(context.Background(), c.lib, c.version, false); why == "" {
			t.Errorf("%s was installed", name)
		}
	}
	if _, err := agentprofile.Load("builder"); err == nil {
		t.Fatal("a refused install left a profile behind")
	}
}

func TestCrewIsWrittenOnlyAfterEveryMemberIsInstalled(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	crew := `{"id":"eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee","name":"team","description":"d","members":[{"profile":"builder","replicas":1}]}`
	src := &fakeSource{
		profiles: map[string]string{idA: workerProfile(t, "builder", idA)},
		crew: sessionsync.CrewFetched{
			Version: ver, Crew: []byte(crew),
			Members: []sessionsync.CrewMemberRef{{Profile: "builder", Library: idA, Version: ver}},
		},
	}
	in := Installer{Src: src}

	// A member the crew does not list is refused before anything is written.
	bad := src.crew
	bad.Members = append(append([]sessionsync.CrewMemberRef{}, bad.Members...), sessionsync.CrewMemberRef{Profile: "stranger", Library: idB, Version: ver})
	src.crew = bad
	if _, why := in.Crew(context.Background(), "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee", ver, false); why == "" {
		t.Fatal("a crew with a member it does not list was installed")
	}
	if _, err := agentprofile.Load("builder"); err == nil {
		t.Fatal("a member was installed for a refused crew")
	}

	src.crew.Members = bad.Members[:1]
	report, why := in.Crew(context.Background(), "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee", ver, false)
	if why != "" || !strings.Contains(report, "installed crew team") {
		t.Fatalf("install: %q %q", report, why)
	}
	if _, ok := StoredCrew("team"); !ok {
		t.Fatal("the crew was not written")
	}
	// Installing again, with the member already here, keeps it.
	if _, why := in.Crew(context.Background(), "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee", ver, false); !strings.Contains(why, "already has a crew") {
		t.Fatalf("a second install without replace: %q", why)
	}
}

func workerProfile(t *testing.T, name, id string) string {
	t.Helper()
	md, err := agentprofile.MarshalMarkdown(agentprofile.AgentProfile{
		Name: name, ID: id, Description: "d", SystemPrompt: "do", Mode: agentprofile.ModeWorker, DisplayName: "Agent " + name,
		Autonomy: agentprofile.AutonomySupervised, Tools: []string{"Read", "Edit"},
		Kanban:    &agentprofile.KanbanSpec{Lists: []string{"backlog"}, OnSuccess: agentprofile.Route{List: "done"}},
		Workspace: &agentprofile.WorkspaceSpec{Isolation: agentprofile.IsolationWorktree},
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(md)
}
