package rc

import (
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/repos"
	"github.com/vulnetix/belai/internal/sessionsync"
)

func TestParseClone(t *testing.T) {
	for _, c := range []struct {
		in          *sessionsync.DispatchClone
		owner, name string
		ok          bool
	}{
		{&sessionsync.DispatchClone{Repo: "Acme/API"}, "acme", "api", true},
		{&sessionsync.DispatchClone{Repo: "acme/api.git", Ref: "release/1.2"}, "acme", "api", true},
		{&sessionsync.DispatchClone{Repo: "acme/.github"}, "acme", ".github", true},
		{nil, "", "", false},
		{&sessionsync.DispatchClone{Repo: "acme"}, "", "", false},
		{&sessionsync.DispatchClone{Repo: "acme/api/x"}, "", "", false},
		{&sessionsync.DispatchClone{Repo: "-acme/api"}, "", "", false},
		{&sessionsync.DispatchClone{Repo: "acme/-api"}, "", "", false},
		{&sessionsync.DispatchClone{Repo: "acme/.."}, "", "", false},
		{&sessionsync.DispatchClone{Repo: "https://github.com/acme/api"}, "", "", false},
		{&sessionsync.DispatchClone{Repo: "acme/api", Ref: "--upload-pack=x"}, "", "", false},
		{&sessionsync.DispatchClone{Repo: "acme/api", Ref: "a..b"}, "", "", false},
	} {
		owner, name, ok := ParseClone(c.in)
		if ok != c.ok || owner != c.owner || name != c.name {
			t.Errorf("%+v = %q %q %v", c.in, owner, name, ok)
		}
	}
}

// fakeClonedRepo makes a directory that is a git checkout whose origin is the
// GitHub url of owner/name, as cloneRepo leaves one.
func fakeClonedRepo(t *testing.T, owner, name string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "repos", owner, name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q"}, {"remote", "add", "origin", "https://github.com/" + owner + "/" + name + ".git"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	return dir
}

// A worker request that names a repository the host does not offer is cloned,
// offered with its remote on the next advertisement, and its workers start
// there; a second request for it reuses the checkout. A clone failure refuses
// the request with the reason.
func TestDaemonClonesARepositoryAndStartsTheWorkersThere(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	t.Setenv("BELAI_HOME", t.TempDir())
	cloned := fakeClonedRepo(t, "acme", "api")
	real, _ := Normalize(cloned)
	site := &fakeSite{acks: map[string][3]string{}, delivered: make(chan struct{}, 8)}
	srv := httptest.NewServer(site)
	defer srv.Close()
	client, err := sessionsync.NewClient(srv.URL, func() (string, error) { return "ApiKey o:k", nil }, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var starts []WorkerStart
	var clones []string
	d, err := New(Options{
		Client: client, HostID: testHost, HeartbeatEvery: 10 * time.Millisecond, PollWait: time.Second, Exe: "/bin/belai",
		Inventory: func() Inventory {
			return Inventory{MaxWorkers: 4, Profiles: []sessionsync.RCProfile{{Name: "belai:bug", Lists: []string{"backlog"}}}}
		},
		StartWorkers: func(w WorkerStart) (string, error) {
			mu.Lock()
			defer mu.Unlock()
			starts = append(starts, w)
			return "bug-1  idle", nil
		},
		CloneEnabled: func() bool { return true },
		Clone: func(_ context.Context, owner, name, ref string) (string, error) {
			mu.Lock()
			defer mu.Unlock()
			clones = append(clones, owner+"/"+name+"@"+ref)
			if name == "missing" {
				return "", errors.New("repository not found")
			}
			return cloned, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	site.queue = []sessionsync.Dispatch{
		{ID: "k1", Kind: "worker", Profile: "belai:bug", Clone: &sessionsync.DispatchClone{Repo: "Acme/API", Ref: "main"}},
		{ID: "k2", Kind: "worker", Profile: "belai:bug", Clone: &sessionsync.DispatchClone{Repo: "acme/missing"}},
		{ID: "k3", Kind: "worker", Profile: "belai:bug", Clone: &sessionsync.DispatchClone{Repo: "../etc"}},
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = d.Run(ctx); close(done) }()
	for i := 0; i < 3; i++ {
		select {
		case <-site.delivered:
		case <-time.After(10 * time.Second):
			t.Fatal("acks did not arrive")
		}
	}
	// The clone is advertised with its remote, and canClone with it.
	deadline := time.Now().Add(5 * time.Second)
	for {
		site.mu.Lock()
		var dirs []sessionsync.RCDir
		can := false
		if site.host.RC != nil {
			dirs, can = site.host.RC.Dirs, site.host.RC.CanClone
		}
		site.mu.Unlock()
		if can && len(dirs) == 1 && dirs[0].Path == real && dirs[0].Remote == "acme/api" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the clone was not advertised: canClone %v dirs %+v", can, dirs)
		}
		time.Sleep(10 * time.Millisecond)
	}
	// A second request for the repository reuses the offered checkout.
	if _, reason := d.cloneDir(ctx, &sessionsync.DispatchClone{Repo: "acme/api"}); reason != "" {
		t.Fatalf("reuse refused: %s", reason)
	}
	cancel()
	<-done

	site.mu.Lock()
	defer site.mu.Unlock()
	if a := site.acks["k1"]; a[0] != sessionsync.DispatchStarted {
		t.Errorf("k1 ack = %v", a)
	}
	if a := site.acks["k2"]; a[0] != sessionsync.DispatchRefused || !strings.Contains(a[2], "could not clone acme/missing: repository not found") {
		t.Errorf("k2 ack = %v", a)
	}
	if a := site.acks["k3"]; a[0] != sessionsync.DispatchRefused || !strings.Contains(a[2], "not a repository to clone") {
		t.Errorf("k3 ack = %v", a)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(starts) != 1 || starts[0].Cwd != real || starts[0].Profile != "belai:bug" {
		t.Errorf("starts = %+v", starts)
	}
	// The clones run beside the request loop, so their order is not fixed.
	sort.Strings(clones)
	if strings.Join(clones, ",") != "acme/api@main,acme/missing@" {
		t.Errorf("clones = %v (the reuse must not clone again)", clones)
	}
}

// With -no-clone, or agents.clone off, the daemon does not advertise canClone
// and refuses a clone request without running git.
func TestDaemonWithoutCloneRefusesAndDoesNotAdvertise(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	client, err := sessionsync.NewClient("http://127.0.0.1:1", func() (string, error) { return "ApiKey o:k", nil }, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		noClone, setting bool
	}{{true, true}, {false, false}} {
		called := false
		d, err := New(Options{Client: client, HostID: testHost, NoClone: c.noClone,
			CloneEnabled: func() bool { return c.setting },
			Clone:        func(context.Context, string, string, string) (string, error) { called = true; return "", nil },
		})
		if err != nil {
			t.Fatal(err)
		}
		if d.canClone() {
			t.Errorf("%+v: canClone advertised", c)
		}
		if _, reason := d.cloneDir(context.Background(), &sessionsync.DispatchClone{Repo: "acme/api"}); !strings.Contains(reason, "does not clone") || called {
			t.Errorf("%+v: reason %q, cloned %v", c, reason, called)
		}
	}
	// The default reads the global settings: on unless agents.clone is false.
	d, err := New(Options{Client: client, HostID: testHost})
	if err != nil {
		t.Fatal(err)
	}
	if !d.canClone() {
		t.Error("agents.clone is on by default")
	}
}

// cloneWith goes through the repos engine: a fresh clone under the repos
// directory at the remote's default branch, trusted, and reused the next time.
func TestCloneWithUsesTheReposEngine(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root := t.TempDir()
	home := filepath.Join(root, "home")
	bare := filepath.Join(root, "origin.git")
	work := filepath.Join(root, "work")
	env := append(os.Environ(), "HOME="+home, "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.test", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.test")
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir, cmd.Env = dir, env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	for _, d := range []string{home, work} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	git(root, "init", "-q", "--bare", "-b", "trunk", bare)
	git(work, "init", "-q", "-b", "trunk")
	if err := os.WriteFile(filepath.Join(work, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(work, "add", "a.txt")
	git(work, "commit", "-q", "-m", "first")
	git(work, "remote", "add", "origin", bare)
	git(work, "push", "-q", "origin", "trunk")
	base := filepath.Join(root, "worktrees", "repos")
	opts := repos.Options{ReposDir: base, GitHubCLI: true,
		LookPath: func(n string) (string, error) {
			if n == "git" {
				return exec.LookPath("git")
			}
			return "", errors.New("not found")
		},
		Env: []string{"HOME=" + home, "GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_CONFIG_NOSYSTEM=1", "GIT_ALLOW_PROTOCOL=file:https:ssh",
			"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=url." + bare + ".insteadOf", "GIT_CONFIG_VALUE_0=https://github.com/acme/api.git"}}
	var trusted []string
	trust := func(dir string) error { trusted = append(trusted, dir); return nil }
	path, err := cloneWith(context.Background(), "acme", "api", "", opts, trust)
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(base, "acme", "api") || len(trusted) != 1 || trusted[0] != path {
		t.Fatalf("path %q trusted %v", path, trusted)
	}
	if b, err := os.ReadFile(filepath.Join(path, "a.txt")); err != nil || string(b) != "one\n" {
		t.Fatalf("checkout: %q %v", b, err)
	}
	if again, err := cloneWith(context.Background(), "acme", "api", "trunk", opts, trust); err != nil || again != path {
		t.Fatalf("reuse: %q %v", again, err)
	}
}
