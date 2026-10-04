package teleport

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/forge"
	"github.com/vulnetix/belai/internal/session"
	"github.com/vulnetix/belai/internal/sessionsync"
)

const (
	originID   = "11111111-1111-4111-8111-111111111111"
	newID1     = "aaaaaaaa-1111-4111-8111-111111111111"
	newID2     = "bbbbbbbb-2222-4222-8222-222222222222"
	profileID  = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
	crewID     = "dddddddd-dddd-4ddd-8ddd-dddddddddddd"
	libVersion = "202610010941"
	remote     = "https://github.com/acme/app.git"
)

// fakeAPI is the backend, in memory. Nothing here touches the network.
type fakeAPI struct {
	mu       sync.Mutex
	tp       sessionsync.Teleport
	states   []sessionsync.TeleportState // returned by successive Gets; the last repeats
	entries  []sessionsync.Entry
	profiles map[string]string // library id -> markdown
	crews    map[string]sessionsync.CrewFetched
	createEr error
	ackErr   error

	puts, gets int
	acks       []ack
}

type ack struct{ status, session, reason string }

func (f *fakeAPI) PutHost(context.Context, string, sessionsync.Host) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.puts++
	return nil
}

func (f *fakeAPI) TeleportCreate(_ context.Context, _, _ string) (sessionsync.Teleport, error) {
	return f.tp, f.createEr
}

func (f *fakeAPI) TeleportGet(context.Context, string, string) (sessionsync.TeleportState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	i := f.gets
	if i >= len(f.states) {
		i = len(f.states) - 1
	}
	f.gets++
	return f.states[i], nil
}

func (f *fakeAPI) TeleportEntries(_ context.Context, _, _ string, after int64) ([]sessionsync.Entry, error) {
	var out []sessionsync.Entry
	for _, e := range f.entries {
		if e.Seq > after && len(out) < 2 { // small pages, so paging is exercised
			out = append(out, e)
		}
	}
	return out, nil
}

func (f *fakeAPI) TeleportProfile(_ context.Context, _, _, id, _ string) (string, []sessionsync.FileRef, error) {
	md, ok := f.profiles[id]
	if !ok {
		return "", nil, errors.New("not in the manifest")
	}
	return md, nil, nil
}

func (f *fakeAPI) TeleportCrew(_ context.Context, _, _, id, _ string) (sessionsync.CrewFetched, error) {
	c, ok := f.crews[id]
	if !ok {
		return sessionsync.CrewFetched{}, errors.New("not in the manifest")
	}
	return c, nil
}

func (f *fakeAPI) TeleportFile(context.Context, string, string, string) ([]byte, error) {
	return nil, errors.New("no files")
}

func (f *fakeAPI) TeleportAck(_ context.Context, _, _, status, sid, why string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.acks = append(f.acks, ack{status, sid, why})
	if status == "completed" && f.ackErr != nil {
		return f.ackErr
	}
	return nil
}

func (f *fakeAPI) last() ack {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.acks) == 0 {
		return ack{}
	}
	return f.acks[len(f.acks)-1]
}

func lines(n int) []sessionsync.Entry {
	var out []sessionsync.Entry
	parent := ""
	for i := 0; i < n; i++ {
		e := sessionsync.Entry{Seq: int64(i), ID: "e" + string(rune('a'+i)), ParentID: parent, Type: "user", Role: "user", Content: "line", Timestamp: int64(1000 + i)}
		if i == 0 {
			e.Type, e.Role = session.EntryTypeSessionMeta, ""
			e.Content = `{"schema":2,"cwd":"/origin/path","mode":"plan","activePlan":"p.md","activeGoal":"g.md","activeProfile":"builder"}`
		}
		out = append(out, e)
		parent = e.ID
	}
	return out
}

func readyAPI(n int, git string) *fakeAPI {
	st := sessionsync.TeleportState{
		Teleport: sessionsync.Teleport{ID: "tp1", OriginSessionID: originID, Status: sessionsync.TeleportReady, SnapshotSeq: int64(n - 1)},
		Git:      json.RawMessage(git),
	}
	return &fakeAPI{
		tp:      sessionsync.Teleport{ID: "tp1", OriginSessionID: originID, Status: sessionsync.TeleportReady, SnapshotSeq: int64(n - 1)},
		states:  []sessionsync.TeleportState{st},
		entries: lines(n),
	}
}

// repo makes a git checkout whose origin is remote, with two commits. It
// returns the directory and the two full commit ids, oldest first.
func repo(t *testing.T, remote string) (string, [2]string) {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) string {
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com", "-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("init", "-q", "-b", "main")
	if remote != "" {
		run("remote", "add", "origin", remote)
	}
	var shas [2]string
	for i := range shas {
		run("commit", "-q", "--allow-empty", "-m", "c")
		shas[i] = run("rev-parse", "HEAD")
	}
	return dir, shas
}

func opts(t *testing.T, api *fakeAPI, dir string, ids ...string) (Options, *session.Store) {
	t.Helper()
	t.Setenv("BELAI_HOME", t.TempDir())
	store := session.NewStoreAt(t.TempDir())
	next := 0
	return Options{
		Ref: originID[:8], Workdir: dir, API: api, HostID: "host-1", Store: store,
		WorktreesDir: t.TempDir(), Poll: time.Millisecond, Wait: time.Second,
		NewID: func() string { id := ids[next%len(ids)]; next++; return id },
	}, store
}

func git(t *testing.T, o Options, dir string, args ...string) string {
	t.Helper()
	out, err := forge.HardenedGit("", "")(context.Background(), dir, append([]string{"git"}, args...)...)
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(out))
}

func TestVerifyRefusesAnythingButTheFrozenTranscript(t *testing.T) {
	good := lines(4)
	if err := verify(good, 3); err != nil {
		t.Fatalf("a whole transcript: %v", err)
	}
	cases := map[string]func() ([]sessionsync.Entry, int64){
		"a short page": func() ([]sessionsync.Entry, int64) { return good[:3], 3 },
		"a gap": func() ([]sessionsync.Entry, int64) {
			e := append([]sessionsync.Entry{}, good...)
			e[2].Seq = 5
			return e, 3
		},
		"a repeated id": func() ([]sessionsync.Entry, int64) {
			e := append([]sessionsync.Entry{}, good...)
			e[2].ID = e[1].ID
			return e, 3
		},
		"a bad id": func() ([]sessionsync.Entry, int64) {
			e := append([]sessionsync.Entry{}, good...)
			e[1].ID = "a b"
			return e, 3
		},
		"an odd type": func() ([]sessionsync.Entry, int64) {
			e := append([]sessionsync.Entry{}, good...)
			e[1].Type = "User\n"
			return e, 3
		},
		"an odd role": func() ([]sessionsync.Entry, int64) {
			e := append([]sessionsync.Entry{}, good...)
			e[1].Role = "<x>"
			return e, 3
		},
		"no lines at all": func() ([]sessionsync.Entry, int64) { return nil, -1 },
	}
	for name, mk := range cases {
		e, snap := mk()
		if err := verify(e, snap); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

func TestBuildRebuildsOneRootAndLeavesTheOriginsRecordsBehind(t *testing.T) {
	in := []sessionsync.Entry{
		{Seq: 0, ID: "m1", Type: session.EntryTypeSessionMeta, Content: `{"schema":2,"cwd":"/origin","mode":"goal","activePlan":"p.md","activeGoal":"g.md","activeProfile":"builder","originCwd":"/x"}`},
		{Seq: 1, ID: "u1", ParentID: "m1", Type: "user", Role: "user", Content: "hello \x1b[2J there"},
		{Seq: 2, ID: "a1", ParentID: "u1", Type: "assistant", Role: "assistant", Content: "ok",
			Meta: json.RawMessage(`{"model":"m-1","tool_calls":[{"name":"Bash","args":"ls \u001b[31m"}]}`)},
		{Seq: 3, ID: "m2", ParentID: "a1", Type: session.EntryTypeSessionMeta, Content: `{"schema":2,"mode":"plan"}`},
		{Seq: 4, ID: "u2", ParentID: "m2", Type: "user", Role: "user", Content: "again"},
	}
	out, written, err := build(in, session.Meta{Schema: session.SchemaVersion, Cwd: "/here", TeleportedFrom: originID},
		func(name string) bool { return name == "builder" })
	if err != nil {
		t.Fatal(err)
	}
	roots, byID := 0, map[string]session.Entry{}
	for _, e := range out {
		byID[e.ID] = e
		if e.ParentID == "" {
			roots++
		}
	}
	if roots != 1 || out[0].Type != session.EntryTypeSessionMeta || len(out) != 4 {
		t.Fatalf("shape = %d roots, %d entries", roots, len(out))
	}
	if byID["u1"].ParentID != out[0].ID || byID["u2"].ParentID != "a1" {
		t.Fatalf("chain: u1 -> %q, u2 -> %q", byID["u1"].ParentID, byID["u2"].ParentID)
	}
	if strings.Contains(byID["u1"].Content, "\x1b") || strings.Contains(string(mustJSON(byID["a1"].Meta)), "\\u001b") {
		t.Fatalf("an escape sequence survived: %q %v", byID["u1"].Content, byID["a1"].Meta)
	}
	if written.Cwd != "/here" || written.TeleportedFrom != originID || written.ActivePlan != "" || written.ActiveGoal != "" ||
		written.OriginCwd != "" || written.ActiveProfile != "builder" || written.Mode != "plan" {
		t.Fatalf("meta = %+v", written)
	}
	// A profile this host could not install is not recorded as active.
	_, written, _ = build(in, session.Meta{Schema: session.SchemaVersion, Cwd: "/here"}, func(string) bool { return false })
	if written.ActiveProfile != "" {
		t.Fatalf("active profile kept without the profile: %+v", written)
	}
}

func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }

func TestRunContinuesInThisCheckoutWhenItIsAtTheSameCommit(t *testing.T) {
	dir, shas := repo(t, remote)
	api := readyAPI(5, `{"remote":"acme/app","host":"github.com","branch":"main","head":"`+shas[1][:12]+`"}`)
	o, store := opts(t, api, dir, newID1)

	res, err := Run(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if res.Workdir != dir || res.SessionID != newID1 || res.OriginID != originID || res.TeleportID != "tp1" {
		t.Fatalf("result = %+v", res)
	}
	entries, err := store.ReadFrom(res.Key, res.SessionID)
	if err != nil || len(entries) != 5 {
		t.Fatalf("session = %d entries, %v", len(entries), err)
	}
	m, _ := session.LatestMeta(entries)
	if m.TeleportedFrom != originID || m.Cwd != dir || m.Mode != "plan" || m.ActivePlan != "" || m.ActiveGoal != "" {
		t.Fatalf("meta = %+v", m)
	}
	if got := api.last(); got.status != "completed" || got.session != newID1 {
		t.Fatalf("ack = %+v", got)
	}
	if api.puts != 1 {
		t.Fatalf("host registered %d times", api.puts)
	}
}

func TestRunMakesAWorktreeAtTheOriginsCommitWhenTheCheckoutIsElsewhere(t *testing.T) {
	dir, shas := repo(t, remote)
	api := readyAPI(3, `{"remote":"acme/app","host":"github.com","branch":"feature/x","head":"`+shas[0][:12]+`","dirty":true}`)
	o, store := opts(t, api, dir, newID1)
	var trusted []string
	o.Trust = func(d string) error { trusted = append(trusted, d); return nil }

	res, err := Run(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if res.Workdir == dir || !strings.HasPrefix(res.Workdir, o.WorktreesDir) {
		t.Fatalf("workdir = %s", res.Workdir)
	}
	if got := git(t, o, res.Workdir, "rev-parse", "HEAD"); got != shas[0] {
		t.Fatalf("worktree is at %s, want %s", got, shas[0])
	}
	if branch := git(t, o, res.Workdir, "rev-parse", "--abbrev-ref", "HEAD"); branch != "teleport/feature-x-aaaaaaaa" {
		t.Fatalf("branch = %s", branch)
	}
	// The checkout the user was in did not move.
	if got := git(t, o, dir, "rev-parse", "HEAD"); got != shas[1] {
		t.Fatalf("the original checkout moved to %s", got)
	}
	if len(trusted) != 1 || trusted[0] != res.Workdir {
		t.Fatalf("trusted = %v", trusted)
	}
	wantKey, _ := session.KeyFor(res.Workdir)
	if res.Key != wantKey {
		t.Fatalf("key = %s, want %s", res.Key, wantKey)
	}
	if _, err := store.ReadFrom(res.Key, res.SessionID); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(res.Notices, "\n")
	if !strings.Contains(joined, "uncommitted") || !strings.Contains(joined, "worktree") {
		t.Fatalf("notices = %v", res.Notices)
	}
}

func TestRunRefusesAndLeavesNothingBehind(t *testing.T) {
	cases := map[string]struct {
		remote string
		git    func(shas [2]string) string
		want   string
	}{
		"another repository": {remote: "https://github.com/other/thing.git", want: "acme/app",
			git: func(s [2]string) string {
				return `{"remote":"acme/app","host":"github.com","head":"` + s[1][:12] + `"}`
			}},
		"another host": {remote: "https://gitlab.com/acme/app.git", want: "acme/app",
			git: func(s [2]string) string {
				return `{"remote":"acme/app","host":"github.com","head":"` + s[1][:12] + `"}`
			}},
		"a commit that is not here": {remote: remote, want: "push that branch",
			git: func([2]string) string {
				return `{"remote":"acme/app","host":"github.com","branch":"main","head":"0123456789ab"}`
			}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			dir, shas := repo(t, c.remote)
			api := readyAPI(3, c.git(shas))
			o, store := opts(t, api, dir, newID1)
			// No network: a fetch fails, anything else is real git.
			real := forge.HardenedGit("", "")
			o.Git = func(ctx context.Context, d string, argv ...string) ([]byte, error) {
				if len(argv) > 1 && argv[1] == "fetch" {
					return nil, errors.New("git: could not read from remote")
				}
				return real(ctx, d, argv...)
			}
			_, err := Run(context.Background(), o)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want it to mention %q", err, c.want)
			}
			if got := api.last(); got.status != "refused" || got.session != "" || got.reason == "" {
				t.Fatalf("ack = %+v", got)
			}
			key, _ := session.KeyFor(dir)
			if infos, _ := store.SessionsIn(key); len(infos) != 0 {
				t.Fatalf("a session was left behind: %v", infos)
			}
			if entries, _ := os.ReadDir(o.WorktreesDir); len(entries) != 0 {
				t.Fatalf("a worktree was left behind: %v", entries)
			}
		})
	}
}

func TestRunRefusesAnOverrideRefThatIsNotARef(t *testing.T) {
	dir, shas := repo(t, remote)
	api := readyAPI(3, `{"remote":"acme/app","host":"github.com","head":"`+shas[1][:12]+`"}`)
	o, _ := opts(t, api, dir, newID1)
	o.RefOverride = "--upload-pack=touch /tmp/x"
	if _, err := Run(context.Background(), o); err == nil || !strings.Contains(err.Error(), "-teleport-ref") {
		t.Fatalf("err = %v", err)
	}
}

func TestRunNeedsAGitCheckoutAndAsksNothingWithout(t *testing.T) {
	api := readyAPI(3, `{}`)
	o, _ := opts(t, api, t.TempDir(), newID1)
	if _, err := Run(context.Background(), o); err == nil || !strings.Contains(err.Error(), "git repository") {
		t.Fatalf("err = %v", err)
	}
	if api.puts != 0 || len(api.acks) != 0 {
		t.Fatalf("the backend was asked: %d puts, %d acks", api.puts, len(api.acks))
	}
}

func TestRunWithNoGitFactsContinuesInThisCheckoutWithANote(t *testing.T) {
	dir, _ := repo(t, remote)
	api := readyAPI(3, `{}`)
	o, _ := opts(t, api, dir, newID1)
	res, err := Run(context.Background(), o)
	if err != nil || res.Workdir != dir {
		t.Fatalf("%+v %v", res, err)
	}
	if !strings.Contains(strings.Join(res.Notices, "\n"), "no remote") {
		t.Fatalf("notices = %v", res.Notices)
	}
}

func TestRunReportsTheBackendsReasonWhenTheTeleportFails(t *testing.T) {
	dir, _ := repo(t, remote)
	api := readyAPI(3, `{}`)
	api.tp = sessionsync.Teleport{ID: "tp1", OriginSessionID: originID, Status: sessionsync.TeleportFailed, Reason: "start belai rc on the origin host"}
	o, _ := opts(t, api, dir, newID1)
	if _, err := Run(context.Background(), o); err == nil || err.Error() != "start belai rc on the origin host" {
		t.Fatalf("err = %v", err)
	}
	api.createEr = &sessionsync.TeleportError{Status: 404}
	if _, err := Run(context.Background(), o); err == nil || !strings.Contains(err.Error(), "no session by that id") {
		t.Fatalf("err = %v", err)
	}
}

func TestRunWaitsWhileTheOriginBacksUp(t *testing.T) {
	dir, shas := repo(t, remote)
	api := readyAPI(3, `{"remote":"acme/app","host":"github.com","head":"`+shas[1][:12]+`"}`)
	ready := api.states[0]
	api.tp.Status = sessionsync.TeleportBackingUp
	waiting := sessionsync.TeleportState{Teleport: sessionsync.Teleport{ID: "tp1", Status: sessionsync.TeleportBackingUp}}
	api.states = []sessionsync.TeleportState{waiting, waiting, ready}
	o, _ := opts(t, api, dir, newID1)
	var said []string
	o.Progress = func(s string) { said = append(said, s) }
	if _, err := Run(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(said, "|"), "back up") {
		t.Fatalf("progress = %v", said)
	}
	// And gives up when the origin never answers.
	api2 := readyAPI(3, `{}`)
	api2.tp.Status = sessionsync.TeleportBackingUp
	api2.states = []sessionsync.TeleportState{waiting}
	o2, _ := opts(t, api2, dir, newID1)
	o2.Wait = 20 * time.Millisecond
	if _, err := Run(context.Background(), o2); err == nil || !strings.Contains(err.Error(), "belai rc") {
		t.Fatalf("err = %v", err)
	}
}

func profileMD(t *testing.T, mode, schedule string) string {
	t.Helper()
	md, err := agentprofile.MarshalMarkdown(agentprofile.AgentProfile{
		Name: "builder", Description: "d", SystemPrompt: "build", Mode: mode, Schedule: schedule, ID: profileID,
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(md)
}

func TestRunInstallsTheProfileTheSessionRanUnderAndSkipsAScheduledOne(t *testing.T) {
	dir, shas := repo(t, remote)
	manifest := sessionsync.TeleportManifest{
		Profile: "builder", Profiles: []sessionsync.CrewMemberRef{{Profile: "builder", Library: profileID, Version: libVersion}},
	}
	git := `{"remote":"acme/app","host":"github.com","head":"` + shas[1][:12] + `"}`

	api := readyAPI(3, git)
	api.states[0].Manifest = manifest
	api.profiles = map[string]string{profileID: profileMD(t, agentprofile.ModeSingle, "")}
	o, store := opts(t, api, dir, newID1)
	res, err := Run(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if p, err := agentprofile.Load("builder"); err != nil || p.ID != profileID {
		t.Fatalf("the profile was not installed: %v", err)
	}
	entries, _ := store.ReadFrom(res.Key, res.SessionID)
	if m, _ := session.LatestMeta(entries); m.ActiveProfile != "builder" {
		t.Fatalf("active profile = %q", m.ActiveProfile)
	}

	// A scheduled profile is not teleported: the session opens without it.
	api2 := readyAPI(3, git)
	api2.states[0].Manifest = manifest
	api2.profiles = map[string]string{profileID: profileMD(t, agentprofile.ModeScheduled, "0 9 * * *")}
	o2, store2 := opts(t, api2, dir, newID1)
	res2, err := Run(context.Background(), o2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := agentprofile.Load("builder"); err == nil {
		t.Fatal("a scheduled profile was installed")
	}
	entries2, _ := store2.ReadFrom(res2.Key, res2.SessionID)
	if m, _ := session.LatestMeta(entries2); m.ActiveProfile != "" {
		t.Fatalf("active profile = %q", m.ActiveProfile)
	}
	joined := strings.Join(res2.Notices, "\n")
	if !strings.Contains(joined, "scheduled agents are not teleported") || !strings.Contains(joined, "default agent") {
		t.Fatalf("notices = %v", res2.Notices)
	}
}

func TestRunWithAProfileTheLibraryLacksSaysSo(t *testing.T) {
	dir, shas := repo(t, remote)
	api := readyAPI(3, `{"remote":"acme/app","host":"github.com","head":"`+shas[1][:12]+`"}`)
	api.states[0].Manifest = sessionsync.TeleportManifest{Profile: "builder"}
	o, _ := opts(t, api, dir, newID1)
	res, err := Run(context.Background(), o)
	if err != nil || !strings.Contains(strings.Join(res.Notices, "\n"), "not in the library") {
		t.Fatalf("%v %v", res.Notices, err)
	}
}

func TestRunKeepsNoSessionWhenTheBackendWillNotRecordIt(t *testing.T) {
	dir, shas := repo(t, remote)
	api := readyAPI(3, `{"remote":"acme/app","host":"github.com","head":"`+shas[1][:12]+`"}`)
	api.ackErr = errors.New("down")
	o, store := opts(t, api, dir, newID1)
	if _, err := Run(context.Background(), o); err == nil || !strings.Contains(err.Error(), "not kept") {
		t.Fatalf("err = %v", err)
	}
	key, _ := session.KeyFor(dir)
	if infos, _ := store.SessionsIn(key); len(infos) != 0 {
		t.Fatalf("a session was kept: %v", infos)
	}
}

func TestRunAgainMakesAnotherSessionAndKeepsTheFirst(t *testing.T) {
	dir, shas := repo(t, remote)
	api := readyAPI(3, `{"remote":"acme/app","host":"github.com","head":"`+shas[1][:12]+`"}`)
	o, store := opts(t, api, dir, newID1, newID2)
	first, err := Run(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Run(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if first.SessionID == second.SessionID {
		t.Fatalf("both teleports made %s", first.SessionID)
	}
	for _, r := range []Result{first, second} {
		if _, err := store.ReadFrom(r.Key, r.SessionID); err != nil {
			t.Fatalf("session %s: %v", r.SessionID, err)
		}
	}
}
