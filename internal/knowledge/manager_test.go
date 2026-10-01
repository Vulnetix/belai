package knowledge

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/config"
)

const testProfileID = "0b8f6a2e-3c1d-4e5f-8a9b-1c2d3e4f5a6b"

func TestStoreRefreshSearchAndReopen(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	root, docs := t.TempDir(), t.TempDir()
	write(t, filepath.Join(docs, "auth.md"), "Rotate the signing key every ninety days and revoke tokens on logout.")
	write(t, filepath.Join(root, ".vulnetix", "sast.sarif"), `{"runs":[{"tool":{"driver":{"rules":[]}},"results":[{"ruleId":"VNX-GO-SQLI","level":"error","locations":[{"physicalLocation":{"artifactLocation":{"uri":"db/query.go"},"region":{"startLine":40}}}]}]}]}`)
	o := Options{Root: root, Profile: &Profile{ID: testProfileID, Name: "reviewer", Paths: []string{docs}}}

	s := Open(o)
	if !s.Set().Empty() {
		t.Fatal("a fresh store has nothing")
	}
	rep, err := s.Refresh(context.Background())
	if err != nil || rep.Profile.Docs != 1 || rep.Project.Docs != 1 || len(rep.Warnings) != 0 {
		t.Fatalf("report = %+v err=%v", rep, err)
	}
	set := s.Set()
	if h := set.Search("signing key rotation", 0, nil); len(h) == 0 || !strings.HasPrefix(h[0].Address, "kb+reviewer/") {
		t.Fatalf("profile hit = %+v", h)
	}
	if h := set.Search("VNX-GO-SQLI query", 0, nil); len(h) == 0 || !strings.HasPrefix(h[0].Address, "kb+project/.vulnetix/") {
		t.Fatalf("project hit = %+v", h)
	}

	// A new session opens the persisted indexes without any gate or refresh.
	again := Open(o)
	if h := again.Set().Search("signing key rotation", 0, nil); len(h) == 0 {
		t.Fatal("the saved profile index must load")
	}
	for _, dir := range []string{mustDir(t)(config.ProfileKnowledgeDir(testProfileID)), mustDir(t)(config.ProjectKnowledgeDir(root))} {
		if info, err := os.Stat(Path(dir)); err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("index file in %s: %v %v", dir, info, err)
		}
	}
}

func mustDir(t *testing.T) func(string, error) string {
	return func(d string, err error) string {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
}

func TestStoreCorruptIndexIsEmptyWarnedAndNotOverwritten(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	root := t.TempDir()
	write(t, filepath.Join(root, ".vulnetix", "memory.yaml"), "notes: uses postgres\n")
	dir := mustDir(t)(config.ProjectKnowledgeDir(root))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(dir), []byte("not an index at all, but long enough to pass the size checks.....xxxxxxxxxxxxxxxxxxxx"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := Open(Options{Root: root})
	if len(s.Warnings()) != 1 || !s.Set().Empty() {
		t.Fatalf("warnings = %v", s.Warnings())
	}
	rep, err := s.Refresh(context.Background())
	if err != nil || len(rep.Warnings) != 1 || !strings.Contains(rep.Warnings[0], "not saved") {
		t.Fatalf("a corrupt file must not be overwritten: %+v err=%v", rep, err)
	}
	if h := s.Set().Search("postgres storage", 0, nil); len(h) == 0 {
		t.Fatal("the in-memory index still serves this session")
	}
	if b, _ := os.ReadFile(Path(dir)); !strings.HasPrefix(string(b), "not an index") {
		t.Fatal("the corrupt file was changed")
	}
}

func TestStoreFailsClosedWhenTheGateErrors(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	root := t.TempDir()
	write(t, filepath.Join(root, ".vulnetix", "memory.yaml"), "notes: uses postgres\n")
	boom := errors.New("classifier down")
	s := Open(Options{Root: root, ProjectGate: func(context.Context, string) (bool, error) { return false, boom }})
	rep, err := s.Refresh(context.Background())
	if err != nil || rep.Project.Failed != 1 || !s.Set().Empty() {
		t.Fatalf("a gate error must index nothing: %+v err=%v", rep, err)
	}
}

func TestStoreSessionFilesShareTheProjectCap(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	root := t.TempDir()
	write(t, filepath.Join(root, ".vulnetix", "memory.yaml"), strings.Repeat("notes about the database layout and its migrations\n", 12))
	s := Open(Options{Root: root, ProjectTokens: 400})
	if _, err := s.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	used := s.project.Tokens()
	if used == 0 || used > 400 {
		t.Fatalf("project tokens = %d", used)
	}
	big := strings.Repeat("a long attached file about retries and backoff\n", 100)
	st, err := s.AddSessionText(context.Background(), "notes.md", "/w/notes.md", big)
	if err != nil && !errors.Is(err, ErrCapReached) {
		t.Fatal(err)
	}
	if got := s.project.Tokens() + s.session.Tokens(); got > 400 {
		t.Fatalf("project plus session = %d tokens, cap 400 (%+v)", got, st)
	}
	// Replacing the same file does not double count it.
	if _, err := s.AddSessionText(context.Background(), "notes.md", "/w/notes.md", big+"extra line\n"); err != nil && !errors.Is(err, ErrCapReached) {
		t.Fatal(err)
	}
	if got := s.project.Tokens() + s.session.Tokens(); got > 400 {
		t.Fatalf("after replacement %d tokens, cap 400", got)
	}
	// Nothing was written for the session: a reopened store has no session text.
	if h := Open(Options{Root: root}).Set().Search("retries backoff attached", 0, nil); len(h) != 0 {
		t.Fatalf("session files must not persist: %+v", h)
	}
}

func TestStoreNilAndNoProfileAreInert(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	var s *Store
	if s.Set() != nil || len(s.Warnings()) != 0 || len(s.Docs()) != 0 {
		t.Fatal("a nil store is empty")
	}
	if rep, err := s.Refresh(context.Background()); err != nil || rep.Profile.Docs != 0 {
		t.Fatalf("%+v %v", rep, err)
	}
	o := Open(Options{})
	if !o.Set().Empty() {
		t.Fatal("no root and no profile is empty")
	}
	if _, err := Open(Options{Profile: &Profile{ID: "../../etc", Name: "x", Paths: []string{"/tmp"}}}).Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestProfileKnowledgeDirRefusesNonIDs(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	for _, bad := range []string{"", "../x", "profile", strings.ToUpper(testProfileID), testProfileID + "/.."} {
		if _, err := config.ProfileKnowledgeDir(bad); err == nil {
			t.Errorf("%q must be refused", bad)
		}
	}
}

// A session holds one Set for its life. The TUI swaps the profile under it as
// the user engages another agent.
func TestSetFollowsASwappedProfile(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	docs := t.TempDir()
	write(t, filepath.Join(docs, "auth.md"), "Rotate the signing key every ninety days and revoke tokens on logout.")
	s := Open(Options{})
	set := s.Set()
	if !set.Empty() {
		t.Fatal("nothing indexed yet")
	}
	p := &Profile{ID: testProfileID, Name: "reviewer", Paths: []string{docs}}
	s.SetProfile(p)
	if _, err := s.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if h := set.Search("signing key rotation", 0, nil); len(h) == 0 {
		t.Fatal("a Set taken before SetProfile must see the new profile")
	}
	s.SetProfile(&Profile{ID: testProfileID, Name: "reviewer", Paths: []string{docs}}) // same: no-op, no reload
	if h := set.Search("signing key rotation", 0, nil); len(h) == 0 {
		t.Fatal("an identical profile must keep its index")
	}
	s.SetProfile(nil)
	if h := set.Search("signing key rotation", 0, nil); len(h) != 0 {
		t.Fatalf("clearing the profile must stop its documents being searchable: %+v", h)
	}
	s.SetProfile(&Profile{ID: testProfileID, Name: "reviewer", Paths: nil})
	if !set.Empty() {
		t.Fatal("a profile with no paths has nothing to search")
	}
}

func TestUnchangedFilesAreNotReadAgain(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	root := t.TempDir()
	vx := filepath.Join(root, ".vulnetix")
	write(t, filepath.Join(vx, "memory.yaml"), "notes: uses postgres\n")
	write(t, filepath.Join(vx, "a.sarif"), `{"runs":[{"tool":{"driver":{"rules":[]}},"results":[{"ruleId":"R1","level":"error"}]}]}`)
	s := Open(Options{Root: root})
	if rep, err := s.Refresh(context.Background()); err != nil || rep.Project.Docs != 2 {
		t.Fatalf("first refresh: %+v %v", rep, err)
	}
	// Make a read impossible: an unchanged stat must not need one.
	if err := os.Chmod(filepath.Join(vx, "memory.yaml"), 0); err != nil {
		t.Skip("cannot chmod")
	}
	defer os.Chmod(filepath.Join(vx, "memory.yaml"), 0o644)
	rep, err := s.Refresh(context.Background())
	if err != nil || rep.Project.Docs != 2 || rep.Project.Reused != 2 {
		t.Fatalf("second refresh must reuse both without reading: %+v %v", rep, err)
	}
}

func TestRefreshAsyncIsThrottled(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	s := Open(Options{Root: t.TempDir()})
	if !s.RefreshAsync(context.Background(), time.Hour) {
		t.Fatal("the first refresh starts")
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		running := s.running
		s.mu.Unlock()
		if !running {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if s.RefreshAsync(context.Background(), time.Hour) {
		t.Fatal("a second refresh inside the interval must not start")
	}
	if !s.RefreshAsync(context.Background(), 0) {
		t.Fatal("with no interval it starts again")
	}
	var nilStore *Store
	if nilStore.RefreshAsync(context.Background(), 0) {
		t.Fatal("nil store")
	}
}

func TestSetLimitsTrimsAndGatesCanBeReplaced(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	root := t.TempDir()
	write(t, filepath.Join(root, ".vulnetix", "memory.yaml"), strings.Repeat("notes about the database layout and its migrations\n", 40))
	s := Open(Options{Root: root, ProjectTokens: 100000})
	if _, err := s.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s.project.Tokens() < 300 {
		t.Fatalf("fixture too small: %d", s.project.Tokens())
	}
	s.SetLimits(0, 200)
	if s.project.Tokens() > 200 {
		t.Fatalf("SetLimits left %d tokens", s.project.Tokens())
	}
	called := false
	s.SetGates(nil, func(context.Context, string) (bool, error) { called = true; return true, nil })
	write(t, filepath.Join(root, ".vulnetix", "memory.yaml"), "notes: a new sentence about migrations here\n")
	if _, err := s.Refresh(context.Background()); err != nil || !called {
		t.Fatalf("the replaced gate must be used: called=%v err=%v", called, err)
	}
}

// A profile engaged while a refresh is already running must still be indexed:
// the forced request queues one more pass instead of being dropped.
func TestForcedRefreshDuringARunIsQueued(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	root, docs := t.TempDir(), t.TempDir()
	write(t, filepath.Join(root, ".vulnetix", "memory.yaml"), "notes: uses postgres\n")
	write(t, filepath.Join(docs, "auth.md"), "Rotate the signing key every ninety days and revoke tokens on logout.")
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	slow := func(context.Context, string) (bool, error) {
		once.Do(func() { close(started) })
		<-release
		return true, nil
	}
	s := Open(Options{Root: root, ProjectGate: slow})
	if !s.RefreshAsync(context.Background(), 0) {
		t.Fatal("first refresh starts")
	}
	<-started // the first refresh is inside the gate
	s.SetProfile(&Profile{ID: testProfileID, Name: "reviewer", Paths: []string{docs}})
	if s.RefreshAsync(context.Background(), 0) {
		t.Fatal("a refresh is running, so this one only queues")
	}
	close(release)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if h := s.Set().Search("signing key rotation", 0, nil); len(h) > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the queued pass never indexed the new profile")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
