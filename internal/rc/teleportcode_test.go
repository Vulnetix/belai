package rc

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/forge"
	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/sessionsync"
	"github.com/vulnetix/belai/internal/teleport/changes"
)

// The origin half of a teleport's code transfer, with real git: an origin clone
// with unpushed and uncommitted work, a bare repository as the forge, and a fake
// website that takes the upload.

func tcGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func tcWrite(t *testing.T, dir, rel, body string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func tcAllowFile(env ...string) forge.Runner {
	inner := forge.HardenedGit("", "", env...)
	return func(ctx context.Context, dir string, argv ...string) ([]byte, error) {
		if len(argv) > 0 && argv[0] == "git" {
			argv = append([]string{"git", "-c", "protocol.file.allow=always"}, argv[1:]...)
		}
		return inner(ctx, dir, argv...)
	}
}

type tcWorld struct {
	bare, dir string
	d         *Daemon
	mu        sync.Mutex
	uploads   []sessionsync.TeleportCode
	paths     []string
	status    int
}

func newTCWorld(t *testing.T, policy string, distill func(context.Context, []rolemanager.TeleportFile, []string, string) rolemanager.Distilled) *tcWorld {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	t.Setenv("BELAI_HOME", t.TempDir())
	root := t.TempDir()
	w := &tcWorld{bare: filepath.Join(root, "bare.git"), dir: filepath.Join(root, "origin"), status: http.StatusOK}
	tcGit(t, root, "init", "--bare", "-b", "main", w.bare)
	tcGit(t, root, "clone", w.bare, w.dir)
	tcWrite(t, w.dir, "main.go", "package main\n\nfunc main() {}\n")
	tcGit(t, w.dir, "add", "-A")
	tcGit(t, w.dir, "commit", "-m", "init")
	tcGit(t, w.dir, "push", "origin", "HEAD:main")
	tcGit(t, w.dir, "branch", "--set-upstream-to=origin/main")
	tcGit(t, w.dir, "remote", "set-url", "origin", "https://github.com/acme/app.git")
	tcGit(t, w.dir, "config", "url."+w.bare+".insteadOf", "https://github.com/acme/app.git")
	tcWrite(t, w.dir, "main.go", "package main\n\nfunc main() { feature() }\n")
	tcWrite(t, w.dir, ".env", "SECRET=1\n")

	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var c sessionsync.TeleportCode
		_ = json.Unmarshal(b, &c)
		w.mu.Lock()
		w.uploads = append(w.uploads, c)
		w.paths = append(w.paths, r.Method+" "+r.URL.RequestURI())
		code := w.status
		w.mu.Unlock()
		rw.WriteHeader(code)
		_, _ = rw.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)
	client, err := sessionsync.NewClient(srv.URL, func() (string, error) { return "ApiKey o:k", nil }, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	real, _ := Normalize(w.dir)
	d, err := New(Options{
		Client: client, HostID: testHost, Dirs: []Dir{{Path: real, Name: "app", Source: SourceArg}},
		TeleportPush: func() string { return policy }, TeleportGit: tcAllowFile, Distill: distill,
	})
	if err != nil {
		t.Fatal(err)
	}
	w.d, w.dir = d, real
	return w
}

func (w *tcWorld) request(over sessionsync.Dispatch) sessionsync.Dispatch {
	r := sessionsync.Dispatch{ID: "d1", Kind: "teleport_code", Cwd: w.dir, Teleport: "3f2a9c10-6b7e-4c1d-8a52-0d9e4f1b7a33"}
	if over.Push {
		r.Push = true
	}
	if over.Replay {
		r.Replay = true
	}
	return r
}

func (w *tcWorld) last(t *testing.T) sessionsync.TeleportCode {
	t.Helper()
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.uploads) == 0 {
		t.Fatal("nothing was uploaded")
	}
	return w.uploads[len(w.uploads)-1]
}

func TestOriginSendsAReplayWhenItWasNotAskedToPush(t *testing.T) {
	w := newTCWorld(t, config.TeleportPushAsk, func(_ context.Context, files []rolemanager.TeleportFile, skipped []string, patch string) rolemanager.Distilled {
		if len(files) != 1 || files[0].Path != "main.go" || !strings.Contains(patch, "feature()") || len(skipped) != 1 || !strings.HasPrefix(skipped[0], ".env") {
			t.Errorf("the model was shown %v %v", files, skipped)
		}
		return rolemanager.Distilled{Summary: "calls feature()", Instructions: "- main.go (modified): call feature()", FromModel: true}
	})

	report, why := w.d.teleportCode(context.Background(), w.request(sessionsync.Dispatch{}))

	if why != "" || !strings.Contains(report, "replay") {
		t.Fatalf("report=%q why=%q", report, why)
	}
	c := w.last(t)
	if c.Mode != sessionsync.CodeReplay || c.Summary != "calls feature()" || !strings.Contains(c.Patch, "+++ b/main.go") || strings.Contains(c.Patch, "SECRET") {
		t.Fatalf("upload = %+v", c)
	}
	if !strings.Contains(c.Reason, "was not asked to push") || !strings.Contains(c.Reason, "-teleport-push") {
		t.Fatalf("reason = %q", c.Reason)
	}
	if len(c.Files) != 1 || len(c.Skipped) != 1 || c.Skipped[0].Reason != changes.SkipSensitive {
		t.Fatalf("files %+v skipped %+v", c.Files, c.Skipped)
	}
	if w.paths[0] != "PUT /hosts/"+testHost+"/teleports/3f2a9c10-6b7e-4c1d-8a52-0d9e4f1b7a33/code?dispatch=d1" {
		t.Fatalf("path = %q", w.paths[0])
	}
}

func TestOriginPushesABranchOnlyWithTheUsersAgreementOrItsOwnAllow(t *testing.T) {
	for name, c := range map[string]struct {
		policy string
		push   bool
		want   string
	}{
		"ask with consent":    {config.TeleportPushAsk, true, sessionsync.CodeBranch},
		"ask without consent": {config.TeleportPushAsk, false, sessionsync.CodeReplay},
		"allow":               {config.TeleportPushAllow, false, sessionsync.CodeBranch},
		"never":               {config.TeleportPushNever, true, sessionsync.CodeReplay},
		"unknown policy":      {"sometimes", true, sessionsync.CodeReplay},
	} {
		w := newTCWorld(t, c.policy, nil)
		if _, why := w.d.teleportCode(context.Background(), w.request(sessionsync.Dispatch{Push: c.push})); why != "" {
			t.Fatalf("%s: %s", name, why)
		}
		got := w.last(t)
		if got.Mode != c.want {
			t.Errorf("%s: mode %q, want %q (%+v)", name, got.Mode, c.want, got)
		}
		if got.Mode == sessionsync.CodeBranch {
			if !strings.HasPrefix(got.Branch, "belai/teleport/3f2a9c10") || tcGit(t, w.bare, "rev-parse", "refs/heads/"+got.Branch) != got.Commit {
				t.Errorf("%s: the branch is not on the forge: %+v", name, got)
			}
			if got.Patch != "" {
				t.Errorf("%s: a pushed branch carries no patch through the backend", name)
			}
		} else if names := tcGit(t, w.bare, "for-each-ref", "--format=%(refname)", "refs/heads/belai/"); names != "" {
			t.Errorf("%s: a branch was pushed although it should not be: %s", name, names)
		}
	}
}

func TestOriginNeverPushesWhenTheTargetAsksForAReplayAndExplainsAFailedPush(t *testing.T) {
	w := newTCWorld(t, config.TeleportPushAllow, nil)
	if _, why := w.d.teleportCode(context.Background(), w.request(sessionsync.Dispatch{Replay: true})); why != "" {
		t.Fatal(why)
	}
	if got := w.last(t); got.Mode != sessionsync.CodeReplay || !strings.Contains(got.Reason, "could not fetch the teleport branch") {
		t.Fatalf("a replay request: %+v", got)
	}
	if names := tcGit(t, w.bare, "for-each-ref", "--format=%(refname)", "refs/heads/belai/"); names != "" {
		t.Fatalf("a branch was pushed for a replay: %s", names)
	}

	// The hardened default cannot push to a path remote, so a push that fails falls back to a replay and says why.
	w2 := newTCWorld(t, config.TeleportPushAllow, nil)
	w2.d.o.TeleportGit = nil
	if _, why := w2.d.teleportCode(context.Background(), w2.request(sessionsync.Dispatch{})); why != "" {
		t.Fatal(why)
	}
	if got := w2.last(t); got.Mode != sessionsync.CodeReplay || !strings.Contains(got.Reason, "could not push a teleport branch") {
		t.Fatalf("a failed push: %+v", got)
	}
}

func TestOriginWithNothingToMoveOrNoBaseAnswersNoneWithAReason(t *testing.T) {
	w := newTCWorld(t, config.TeleportPushAsk, nil)
	tcGit(t, w.dir, "checkout", "--", "main.go")
	_ = os.Remove(filepath.Join(w.dir, ".env"))
	if _, why := w.d.teleportCode(context.Background(), w.request(sessionsync.Dispatch{})); why != "" {
		t.Fatal(why)
	}
	if got := w.last(t); got.Mode != sessionsync.CodeNone || !strings.Contains(got.Reason, "nothing left to move") {
		t.Fatalf("clean: %+v", got)
	}

	lone := t.TempDir()
	tcGit(t, lone, "init", "-b", "main")
	tcWrite(t, lone, "a", "a\n")
	tcGit(t, lone, "add", "-A")
	tcGit(t, lone, "commit", "-m", "x")
	real, _ := Normalize(lone)
	w.d.o.Dirs = append(w.d.o.Dirs, Dir{Path: real, Name: "lone", Source: SourceArg})
	r := w.request(sessionsync.Dispatch{})
	r.Cwd = real
	tcWrite(t, lone, "a", "b\n")
	if _, why := w.d.teleportCode(context.Background(), r); why != "" {
		t.Fatal(why)
	}
	if got := w.last(t); got.Mode != sessionsync.CodeNone || !strings.Contains(got.Reason, "no pushed commit to build on") {
		t.Fatalf("no base: %+v", got)
	}
}

func TestOriginWithoutAModelSendsTheHarnessesFileList(t *testing.T) {
	w := newTCWorld(t, config.TeleportPushAsk, nil)

	if _, why := w.d.teleportCode(context.Background(), w.request(sessionsync.Dispatch{})); why != "" {
		t.Fatal(why)
	}
	got := w.last(t)
	if !strings.Contains(got.Summary, "1 file changed") || !strings.Contains(got.Instructions, "- main.go (modified)") {
		t.Fatalf("hand-over = %q / %q", got.Summary, got.Instructions)
	}
}

func TestOriginRefusesWhatItShould(t *testing.T) {
	w := newTCWorld(t, config.TeleportPushAsk, nil)

	r := w.request(sessionsync.Dispatch{})
	r.Cwd = filepath.Dir(w.dir)
	if _, why := w.d.teleportCode(context.Background(), r); !strings.Contains(why, "does not offer that directory") {
		t.Fatalf("a directory it does not offer: %q", why)
	}
	for _, id := range []string{"", "../../x", "not hex!", "--force"} {
		r = w.request(sessionsync.Dispatch{})
		r.Teleport = id
		if _, why := w.d.teleportCode(context.Background(), r); why == "" {
			t.Errorf("teleport id %q accepted", id)
		}
	}
	if len(w.uploads) != 0 {
		t.Fatalf("a refused request uploaded %d times", len(w.uploads))
	}
	// A website that does not take the upload is the reason.
	w.status = http.StatusNotFound
	if _, why := w.d.teleportCode(context.Background(), w.request(sessionsync.Dispatch{})); !strings.Contains(why, "did not take the changes") {
		t.Fatalf("a refused upload: %q", why)
	}
}
