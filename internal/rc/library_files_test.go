package rc

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/agentfiles"
	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/libitem"
	"github.com/vulnetix/belai/internal/sessionsync"
)

const (
	crewID  = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
	crewVer = "202610020900"
)

// fullSite is the whole library half of /api/site/v1/belai: backups with files,
// installs with files, crews, and the acknowledgements.
type fullSite struct {
	mu       sync.Mutex
	acks     map[string][3]string
	backups  []map[string]any
	crewUps  []map[string]any
	uploaded map[string][]byte // sha -> bytes the host uploaded
	// What an install fetch serves.
	profiles map[string]fullProfile // library id -> version
	blobs    map[string][]byte      // sha -> bytes
	crew     json.RawMessage
	members  []sessionsync.CrewMemberRef
	noFiles  bool // the server predates agent files: its file routes are 404
	fetched  []string
}

type fullProfile struct {
	markdown string
	files    []sessionsync.FileRef
}

func (f *fullSite) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := strings.TrimPrefix(r.URL.Path, "/api/site/v1/belai")
	h := "/hosts/" + testHost
	switch {
	case r.Method == http.MethodPost && p == h+"/library/backups":
		var in map[string]any
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.backups = append(f.backups, in)
		w.Write([]byte(`{"version":"` + libVer + `","created":true}`))
	case r.Method == http.MethodPut && strings.HasPrefix(p, h+"/library/files/"):
		if f.noFiles {
			http.NotFound(w, r)
			return
		}
		var in map[string]string
		_ = json.NewDecoder(r.Body).Decode(&in)
		b, _ := base64.StdEncoding.DecodeString(in["contentBase64"])
		f.uploaded[strings.TrimPrefix(p, h+"/library/files/")] = b
		w.Write([]byte(`{"ok":true}`))
	case r.Method == http.MethodGet && strings.HasPrefix(p, h+"/library/profiles/"):
		id := strings.Split(strings.TrimPrefix(p, h+"/library/profiles/"), "/")[0]
		fp, ok := f.profiles[id]
		if !ok {
			http.NotFound(w, r)
			return
		}
		f.fetched = append(f.fetched, id)
		json.NewEncoder(w).Encode(map[string]any{"markdown": fp.markdown, "version": libVer, "files": fp.files})
	case r.Method == http.MethodGet && strings.HasPrefix(p, h+"/library/files/"):
		b, ok := f.blobs[strings.TrimPrefix(p, h+"/library/files/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"contentBase64": base64.StdEncoding.EncodeToString(b)})
	case r.Method == http.MethodPost && p == h+"/library/crew-backups":
		var in map[string]any
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.crewUps = append(f.crewUps, in)
		w.Write([]byte(`{"version":"` + crewVer + `","created":true}`))
	case r.Method == http.MethodGet && strings.HasPrefix(p, h+"/library/crews/"):
		json.NewEncoder(w).Encode(map[string]any{"version": crewVer, "crew": f.crew, "members": f.members, "overwrite": false})
	case strings.HasPrefix(p, "/dispatches/") && strings.HasSuffix(p, "/ack"):
		id := strings.TrimSuffix(strings.TrimPrefix(p, "/dispatches/"), "/ack")
		var in map[string]string
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.acks[id] = [3]string{in["status"], in["sessionId"], in["reason"]}
		w.Write([]byte(`{"ok":true}`))
	default:
		http.NotFound(w, r)
	}
}

type fullHarness struct {
	t     *testing.T
	d     *Daemon
	site  *fullSite
	repo  string
	home  string
	index []string // "dir|profile" for each Index call
	// indexErr, when set, is what the indexer returns.
	indexErr error
}

func newFullHarness(t *testing.T, trusted bool) *fullHarness {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("BELAI_HOME", filepath.Join(home, "state"))
	t.Setenv("VULNETIX_WEB_URL", "http://127.0.0.1:1")
	repo := filepath.Join(home, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	site := &fullSite{acks: map[string][3]string{}, uploaded: map[string][]byte{}, profiles: map[string]fullProfile{}, blobs: map[string][]byte{}}
	srv := httptest.NewServer(site)
	t.Cleanup(srv.Close)
	client, err := sessionsync.NewClient(srv.URL, func() (string, error) { return "ApiKey o:k", nil }, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	h := &fullHarness{t: t, site: site, repo: repo, home: home}
	var dirs []Dir
	if trusted {
		dirs = []Dir{{Path: repo, Name: "repo", Source: SourceTrusted}}
	}
	h.d, err = New(Options{
		Client: client, HostID: testHost, Exe: "/bin/belai", Dirs: dirs,
		RemotePrompts: func() bool { return true },
		Index: func(_ context.Context, _, dir, profile string) (string, error) {
			h.index = append(h.index, dir+"|"+profile)
			return " (3 documents)", h.indexErr
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func (h *fullHarness) run(r sessionsync.Dispatch) (status, reason string) {
	h.t.Helper()
	r.ID = "d-" + r.Kind
	h.d.handle(context.Background(), r)
	h.site.mu.Lock()
	defer h.site.mu.Unlock()
	a, ok := h.site.acks[r.ID]
	if !ok {
		h.t.Fatalf("%s was not acknowledged", r.Kind)
	}
	return a[0], a[2]
}

func (h *fullHarness) write(rel, content string) {
	h.t.Helper()
	p := filepath.Join(h.repo, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		h.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		h.t.Fatal(err)
	}
}

// serve puts a profile and its files where an install fetch finds them.
func (h *fullHarness) serve(p agentprofile.AgentProfile, files map[string]string) {
	h.t.Helper()
	md, err := agentprofile.MarshalMarkdown(p)
	if err != nil {
		h.t.Fatal(err)
	}
	fp := fullProfile{markdown: string(md)}
	for path, content := range files {
		sum := agentfiles.Sum([]byte(content))
		fp.files = append(fp.files, sessionsync.FileRef{Path: path, SHA256: sum})
		h.site.blobs[sum] = []byte(content)
	}
	h.site.profiles[p.ID] = fp
}

func withDocs(p agentprofile.AgentProfile, paths ...string) agentprofile.AgentProfile {
	p.Knowledge = &agentprofile.KnowledgeSpec{Paths: paths}
	return p
}

func ownedFile(t *testing.T, id, rel string) (string, bool) {
	t.Helper()
	dir, err := config.ProfileFilesDir(id)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
	return string(b), err == nil
}

// ── Backup with files ────────────────────────────────────────────────────

func TestBackupUploadsTheFilesAProfileNamesAndListsThemInTheManifest(t *testing.T) {
	h := newFullHarness(t, true)
	h.write(".vulnetix/crews/aws-infra.md", "crew notes")
	h.write("docs/a.md", "doc a")
	h.write("docs/leak.md", "-----BEGIN RSA PRIVATE KEY-----\nabc")
	p := withDocs(installable("reviewer", libID), ".vulnetix/crews/aws-infra.md", "docs", "missing.md")
	if _, err := agentprofile.Save(p); err != nil {
		t.Fatal(err)
	}
	status, report := h.run(sessionsync.Dispatch{Kind: "profile_backup", Profile: "reviewer"})
	if status != sessionsync.DispatchStarted {
		t.Fatalf("ack = %s %q", status, report)
	}
	for _, want := range []string{"backed up reviewer as version " + libVer, "with 2 files", "1 not found here", "1 holding a key or token"} {
		if !strings.Contains(report, want) {
			t.Errorf("report %q lacks %q", report, want)
		}
	}
	if strings.Contains(report, "crew notes") || strings.Contains(report, "BEGIN") {
		t.Fatalf("the acknowledgement carries file text: %q", report)
	}
	if len(h.site.uploaded) != 2 || string(h.site.uploaded[agentfiles.Sum([]byte("crew notes"))]) != "crew notes" {
		t.Fatalf("uploaded %d files", len(h.site.uploaded))
	}
	if _, leaked := h.site.uploaded[agentfiles.Sum([]byte("-----BEGIN RSA PRIVATE KEY-----\nabc"))]; leaked {
		t.Fatal("a file holding a private key was uploaded")
	}
	files, _ := h.site.backups[0]["files"].([]any)
	got := map[string]string{}
	for _, f := range files {
		m := f.(map[string]any)
		got[m["path"].(string)] = m["sha256"].(string)
	}
	if len(got) != 2 || got[".vulnetix/crews/aws-infra.md"] != agentfiles.Sum([]byte("crew notes")) || got["docs/a.md"] != agentfiles.Sum([]byte("doc a")) {
		t.Fatalf("manifest = %v", got)
	}
}

func TestBackupNeverEmptiesTheLibrarysFilesBecauseThisHostHasNone(t *testing.T) {
	h := newFullHarness(t, true)
	// The profile names files, but this host has none of them.
	if _, err := agentprofile.Save(withDocs(installable("reviewer", libID), "docs/gone.md")); err != nil {
		t.Fatal(err)
	}
	status, report := h.run(sessionsync.Dispatch{Kind: "profile_backup", Profile: "reviewer"})
	if status != sessionsync.DispatchStarted || !strings.Contains(report, "no file could be read here") {
		t.Fatalf("ack = %s %q", status, report)
	}
	if _, present := h.site.backups[0]["files"]; present {
		t.Fatalf("the backup said something about files: %v", h.site.backups[0]["files"])
	}
	// A profile that names no files says so: an empty manifest, which clears.
	if _, err := agentprofile.Save(distinct("plain", libID2)); err != nil {
		t.Fatal(err)
	}
	h.site.acks = map[string][3]string{}
	h.run(sessionsync.Dispatch{Kind: "profile_backup", Profile: "plain"})
	files, present := h.site.backups[1]["files"]
	if !present || len(files.([]any)) != 0 {
		t.Fatalf("a profile with no files should send an empty manifest: %v %v", files, present)
	}
}

func TestBackupStillBacksTheProfileUpOnAServerThatTakesNoFiles(t *testing.T) {
	h := newFullHarness(t, true)
	h.site.noFiles = true
	h.write("docs/a.md", "doc a")
	if _, err := agentprofile.Save(withDocs(installable("reviewer", libID), "docs")); err != nil {
		t.Fatal(err)
	}
	status, report := h.run(sessionsync.Dispatch{Kind: "profile_backup", Profile: "reviewer"})
	if status != sessionsync.DispatchStarted || !strings.Contains(report, "its files were not uploaded") {
		t.Fatalf("ack = %s %q", status, report)
	}
	if _, present := h.site.backups[0]["files"]; present {
		t.Fatal("a manifest was sent for files that were never uploaded")
	}
}

// ── Install with files ───────────────────────────────────────────────────

func TestInstallWritesTheFilesIntoTheOwnedDirAndIndexesThem(t *testing.T) {
	h := newFullHarness(t, true)
	p := withDocs(installable("reviewer", libID), ".vulnetix/crews/aws-infra.md", "~/handbook")
	h.serve(p, map[string]string{".vulnetix/crews/aws-infra.md": "crew notes", "~/handbook/h.md": "handbook"})
	status, report := h.run(sessionsync.Dispatch{Kind: "profile_install", Library: libID, Version: libVer})
	if status != sessionsync.DispatchStarted {
		t.Fatalf("ack = %s %q", status, report)
	}
	for _, want := range []string{"installed reviewer", "with 2 files", "indexed its documents (3 documents)"} {
		if !strings.Contains(report, want) {
			t.Errorf("report %q lacks %q", report, want)
		}
	}
	if got, ok := ownedFile(t, libID, "rel/.vulnetix/crews/aws-infra.md"); !ok || got != "crew notes" {
		t.Fatalf("owned rel file = %q %v", got, ok)
	}
	if got, ok := ownedFile(t, libID, "home/handbook/h.md"); !ok || got != "handbook" {
		t.Fatalf("owned home file = %q %v", got, ok)
	}
	if len(h.index) != 1 || h.index[0] != h.repo+"|reviewer" {
		t.Fatalf("index calls = %v", h.index)
	}
	// Nothing was written into the repository or the home directory.
	if _, err := os.Stat(filepath.Join(h.repo, ".vulnetix")); err == nil {
		t.Fatal("an install wrote into the repository")
	}
	if _, err := os.Stat(filepath.Join(h.home, "handbook")); err == nil {
		t.Fatal("an install wrote into the home directory")
	}
	// An install is settled, so the automatic sync does not ask about it.
	for _, it := range localItems(true, nil) {
		if it.id == libID && h.d.libsync.due(it, time.Now()) {
			t.Fatal("a profile that was just installed is due for a sync check")
		}
	}
}

func TestInstallWithoutATrustedDirectorySaysWhenItIndexes(t *testing.T) {
	h := newFullHarness(t, false)
	h.serve(withDocs(installable("reviewer", libID), "docs"), map[string]string{"docs/a.md": "doc a"})
	status, report := h.run(sessionsync.Dispatch{Kind: "profile_install", Library: libID, Version: libVer})
	if status != sessionsync.DispatchStarted || !strings.Contains(report, "indexed the first time the agent runs") || len(h.index) != 0 {
		t.Fatalf("ack = %s %q (index calls %v)", status, report, h.index)
	}
	if got, ok := ownedFile(t, libID, "rel/docs/a.md"); !ok || got != "doc a" {
		t.Fatalf("the files are installed even when nothing is indexed: %q %v", got, ok)
	}
}

func TestInstallIsNotUndoneByAnIndexingFailure(t *testing.T) {
	h := newFullHarness(t, true)
	h.indexErr = errors.New("classifier unavailable")
	h.serve(withDocs(installable("reviewer", libID), "docs"), map[string]string{"docs/a.md": "doc a"})
	status, report := h.run(sessionsync.Dispatch{Kind: "profile_install", Library: libID, Version: libVer})
	if status != sessionsync.DispatchStarted || !strings.Contains(report, "could not index its documents yet") {
		t.Fatalf("ack = %s %q", status, report)
	}
	if _, err := agentprofile.Load("reviewer"); err != nil {
		t.Fatalf("the profile should still be installed: %v", err)
	}
}

func TestInstallRefusesFilesItWillNotWriteAndSavesNothing(t *testing.T) {
	cases := map[string]func(h *fullHarness) map[string]string{
		"a path that climbs out": func(h *fullHarness) map[string]string { return map[string]string{"../escape.md": "x"} },
		"a binary file":          func(h *fullHarness) map[string]string { return map[string]string{"docs/b.md": "a\x00b"} },
	}
	for name, files := range cases {
		t.Run(name, func(t *testing.T) {
			h := newFullHarness(t, true)
			h.serve(withDocs(installable("reviewer", libID), "docs"), files(h))
			status, why := h.run(sessionsync.Dispatch{Kind: "profile_install", Library: libID, Version: libVer})
			if status != sessionsync.DispatchRefused || !strings.Contains(why, "not ones this host will write") {
				t.Fatalf("ack = %s %q", status, why)
			}
			if _, err := agentprofile.Load("reviewer"); err == nil {
				t.Fatal("the profile was saved although its files were refused")
			}
			if dir, _ := config.ProfileFilesDir(libID); dir != "" {
				if _, err := os.Stat(dir); err == nil {
					t.Fatal("a refused install left files behind")
				}
			}
		})
	}
}

func TestInstallRefusesAFileThatDoesNotMatchItsHash(t *testing.T) {
	h := newFullHarness(t, true)
	h.serve(withDocs(installable("reviewer", libID), "docs"), map[string]string{"docs/a.md": "doc a"})
	// The library lists one hash and serves other bytes.
	fp := h.site.profiles[libID]
	h.site.blobs[fp.files[0].SHA256] = []byte("tampered")
	status, why := h.run(sessionsync.Dispatch{Kind: "profile_install", Library: libID, Version: libVer})
	if status != sessionsync.DispatchRefused || !strings.Contains(why, "not ones this host will write") {
		t.Fatalf("ack = %s %q", status, why)
	}
	if _, err := agentprofile.Load("reviewer"); err == nil {
		t.Fatal("the profile was saved although a file did not match")
	}
}

func TestInstallOfAVersionWithNoFilesLeavesTheOwnedFilesAlone(t *testing.T) {
	h := newFullHarness(t, true)
	h.serve(withDocs(installable("reviewer", libID), "docs"), map[string]string{"docs/a.md": "doc a"})
	if status, why := h.run(sessionsync.Dispatch{Kind: "profile_install", Library: libID, Version: libVer}); status != sessionsync.DispatchStarted {
		t.Fatalf("first: %s %s", status, why)
	}
	// An older version backed up before the profile had files: no manifest.
	h.serve(withDocs(installable("reviewer", libID), "docs"), nil)
	h.site.acks = map[string][3]string{}
	if status, why := h.run(sessionsync.Dispatch{Kind: "profile_install", Library: libID, Version: libVer, Overwrite: true}); status != sessionsync.DispatchStarted {
		t.Fatalf("second: %s %s", status, why)
	}
	if got, ok := ownedFile(t, libID, "rel/docs/a.md"); !ok || got != "doc a" {
		t.Fatalf("an install with no manifest removed the owned files: %q %v", got, ok)
	}
}

// ── Crews ────────────────────────────────────────────────────────────────

func crewDoc(name string, members ...string) json.RawMessage {
	ms := make([]agentprofile.Member, len(members))
	for i, m := range members {
		ms[i] = agentprofile.Member{Profile: m, Replicas: 1}
	}
	js, _ := agentprofile.Crew{ID: crewID, Name: name, Description: "d", Members: ms}.CanonicalJSON()
	return js
}

// distinct is a profile with a display name of its own, since a host allows one
// agent per display name.
func distinct(name, id string) agentprofile.AgentProfile {
	p := installable(name, id)
	p.DisplayName = "Agent " + name
	return p
}

func worker(name, id string) agentprofile.AgentProfile { return withSync(distinct(name, id)) }

func TestCrewBackupUploadsTheCanonicalCrewAndGivesItAnId(t *testing.T) {
	h := newFullHarness(t, true)
	if _, err := agentprofile.Save(worker("log", libID)); err != nil {
		t.Fatal(err)
	}
	// A crew file written by hand has no id.
	dir, _ := agentprofile.CrewsDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "aws-infra.json"), []byte(`{"name":"aws-infra","description":"d","members":[{"profile":"log","replicas":1}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	status, report := h.run(sessionsync.Dispatch{Kind: "crew_backup", Crew: "aws-infra"})
	if status != sessionsync.DispatchStarted || report != "backed up crew aws-infra as version "+crewVer {
		t.Fatalf("ack = %s %q", status, report)
	}
	up, _ := json.Marshal(h.site.crewUps[0]["crew"])
	var c agentprofile.Crew
	if err := json.Unmarshal(up, &c); err != nil || !agentprofile.ValidID(c.ID) || c.Name != "aws-infra" || len(c.Members) != 1 {
		t.Fatalf("uploaded %s (%v)", up, err)
	}
	if on, ok := agentprofile.CrewByID(c.ID); !ok || on.Name != "aws-infra" {
		t.Fatal("the id was uploaded but not kept on the host")
	}
	// A crew the library takes is settled, so the sync does not ask again.
	for _, it := range localItems(true, nil) {
		if it.kind == "crew" && h.d.libsync.due(it, time.Now()) {
			t.Fatal("a backed-up crew is due for a sync check")
		}
	}
	for name, r := range map[string]sessionsync.Dispatch{
		"built in": {Kind: "crew_backup", Crew: "belai:delivery"}, "unknown": {Kind: "crew_backup", Crew: "ghost"},
		"no name": {Kind: "crew_backup"}, "two lines": {Kind: "crew_backup", Crew: "a\nb"},
	} {
		h.site.acks = map[string][3]string{}
		if status, why := h.run(r); status != sessionsync.DispatchRefused || why == "" {
			t.Errorf("%s: %s %q", name, status, why)
		}
	}
	if len(h.site.crewUps) != 1 {
		t.Fatalf("a refused backup uploaded: %d", len(h.site.crewUps))
	}
}

func (h *fullHarness) serveCrew(name string, members ...agentprofile.AgentProfile) {
	h.t.Helper()
	var names []string
	var refs []sessionsync.CrewMemberRef
	for _, m := range members {
		names = append(names, m.Name)
		h.serve(m, map[string]string{"notes/" + m.Name + ".md": "notes of " + m.Name})
		refs = append(refs, sessionsync.CrewMemberRef{Profile: m.Name, Library: m.ID, Version: libVer})
	}
	h.site.crew, h.site.members = crewDoc(name, names...), refs
}

func TestCrewInstallBringsItsMembersFirstWithTheirFiles(t *testing.T) {
	h := newFullHarness(t, true)
	log, tf := withDocs(worker("log", libID), "notes"), withDocs(worker("tf", libID2), "notes")
	h.serveCrew("aws-infra", log, tf)
	status, report := h.run(sessionsync.Dispatch{Kind: "crew_install", Library: crewID, Version: crewVer})
	if status != sessionsync.DispatchStarted {
		t.Fatalf("ack = %s %q", status, report)
	}
	for _, want := range []string{"installed crew aws-infra", "installed log, tf"} {
		if !strings.Contains(report, want) {
			t.Errorf("report %q lacks %q", report, want)
		}
	}
	for _, name := range []string{"log", "tf"} {
		if _, err := agentprofile.Load(name); err != nil {
			t.Fatalf("member %s: %v", name, err)
		}
	}
	if c, err := agentprofile.LoadCrew("aws-infra"); err != nil || c.ID != crewID || len(c.Members) != 2 {
		t.Fatalf("crew = %+v %v", c, err)
	}
	if got, ok := ownedFile(t, libID, "rel/notes/log.md"); !ok || got != "notes of log" {
		t.Fatalf("a member's files = %q %v", got, ok)
	}
	if len(h.index) != 2 {
		t.Fatalf("each member's documents are indexed: %v", h.index)
	}
}

func TestCrewInstallLeavesMembersThisHostHasAndRefusesADifferentOne(t *testing.T) {
	h := newFullHarness(t, true)
	if _, err := agentprofile.Save(worker("log", libID)); err != nil {
		t.Fatal(err)
	}
	h.serveCrew("aws-infra", worker("log", libID), worker("tf", libID2))
	status, report := h.run(sessionsync.Dispatch{Kind: "crew_install", Library: crewID, Version: crewVer})
	if status != sessionsync.DispatchStarted || !strings.Contains(report, "installed tf") || !strings.Contains(report, "kept log") {
		t.Fatalf("ack = %s %q", status, report)
	}
	for _, id := range h.site.fetched {
		if id == libID {
			t.Fatal("a member this host already has was fetched and replaced")
		}
	}

	// A profile of the same name with another id is a different profile.
	h2 := newFullHarness(t, true)
	if _, err := agentprofile.Save(worker("log", "dddddddd-dddd-4ddd-8ddd-dddddddddddd")); err != nil {
		t.Fatal(err)
	}
	h2.serveCrew("aws-infra", worker("log", libID))
	status, why := h2.run(sessionsync.Dispatch{Kind: "crew_install", Library: crewID, Version: crewVer})
	if status != sessionsync.DispatchRefused || !strings.Contains(why, "different profile") {
		t.Fatalf("ack = %s %q", status, why)
	}
	if _, err := agentprofile.LoadCrew("aws-infra"); err == nil {
		t.Fatal("the crew was written although a member conflicted")
	}
}

func TestCrewInstallWritesNothingOfTheCrewWhenAMemberFails(t *testing.T) {
	h := newFullHarness(t, true)
	h.serveCrew("aws-infra", worker("log", libID), worker("tf", libID2))
	// The second member's version is not in the library.
	delete(h.site.profiles, libID2)
	status, why := h.run(sessionsync.Dispatch{Kind: "crew_install", Library: crewID, Version: crewVer})
	if status != sessionsync.DispatchRefused || !strings.Contains(why, "could not install member tf") || !strings.Contains(why, "1 member installed first") {
		t.Fatalf("ack = %s %q", status, why)
	}
	if _, err := agentprofile.LoadCrew("aws-infra"); err == nil {
		t.Fatal("the crew was written although a member failed")
	}
}

func TestCrewInstallChecksTheRequestAgainstTheCrew(t *testing.T) {
	cases := map[string]func(h *fullHarness){
		"a member the crew does not list": func(h *fullHarness) {
			h.site.members = append(h.site.members, sessionsync.CrewMemberRef{Profile: "stranger", Library: libID2, Version: libVer})
		},
		"another crew's id": func(h *fullHarness) {
			js, _ := agentprofile.Crew{ID: "dddddddd-dddd-4ddd-8ddd-dddddddddddd", Name: "aws-infra", Members: []agentprofile.Member{{Profile: "log"}}}.CanonicalJSON()
			h.site.crew = js
		},
		"an unknown field": func(h *fullHarness) {
			h.site.crew = json.RawMessage(`{"id":"` + crewID + `","name":"aws-infra","members":[{"profile":"log"}],"run":"rm -rf"}`)
		},
		"a built-in name": func(h *fullHarness) { h.site.crew = crewDoc("belai:delivery", "log") },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			h := newFullHarness(t, true)
			h.serveCrew("aws-infra", worker("log", libID))
			mutate(h)
			status, why := h.run(sessionsync.Dispatch{Kind: "crew_install", Library: crewID, Version: crewVer})
			if status != sessionsync.DispatchRefused || why == "" {
				t.Fatalf("ack = %s %q", status, why)
			}
			if _, err := agentprofile.Load("log"); err == nil {
				t.Fatal("a member was installed for a refused crew")
			}
		})
	}
	h := newFullHarness(t, true)
	for name, r := range map[string]sessionsync.Dispatch{
		"no library": {Kind: "crew_install", Version: crewVer}, "not an id": {Kind: "crew_install", Library: "../x", Version: crewVer},
		"bad version": {Kind: "crew_install", Library: crewID, Version: "latest"},
	} {
		h.site.acks = map[string][3]string{}
		if status, _ := h.run(r); status != sessionsync.DispatchRefused {
			t.Errorf("%s: %s", name, status)
		}
	}
}

func TestCrewInstallReplacesOnlyTheSameCrewWhenTold(t *testing.T) {
	h := newFullHarness(t, true)
	h.serveCrew("aws-infra", worker("log", libID))
	if status, why := h.run(sessionsync.Dispatch{Kind: "crew_install", Library: crewID, Version: crewVer}); status != sessionsync.DispatchStarted {
		t.Fatalf("first: %s %s", status, why)
	}
	h.site.acks = map[string][3]string{}
	status, why := h.run(sessionsync.Dispatch{Kind: "crew_install", Library: crewID, Version: crewVer})
	if status != sessionsync.DispatchRefused || !strings.Contains(why, "replace turned on") {
		t.Fatalf("a second install without replace: %s %q", status, why)
	}
	h.site.acks = map[string][3]string{}
	if status, why := h.run(sessionsync.Dispatch{Kind: "crew_install", Library: crewID, Version: crewVer, Overwrite: true}); status != sessionsync.DispatchStarted {
		t.Fatalf("with replace: %s %s", status, why)
	}
}

// ── Automatic sync ───────────────────────────────────────────────────────

type fakeRemote struct {
	mu       sync.Mutex
	answers  map[string]string // item id -> action
	asked    [][]string
	pushed   []string
	pushErr  map[string]error
	askErr   error
	versions int
	// itemHashes is the hash each item was last asked about with, pushedBody the
	// document each was pushed with, and noItems makes the server answer none (a
	// website that predates items).
	itemHashes map[string]string
	pushedBody map[string]string
	noItems    bool
}

func (f *fakeRemote) LibrarySyncAll(_ context.Context, _ string, agents, crews []sessionsync.SyncItem, items []sessionsync.SyncItemRef) ([]sessionsync.SyncAnswer, []sessionsync.SyncAnswer, []sessionsync.SyncItemAnswer, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.askErr != nil {
		return nil, nil, nil, f.askErr
	}
	var ids []string
	answer := func(items []sessionsync.SyncItem) []sessionsync.SyncAnswer {
		var out []sessionsync.SyncAnswer
		for _, it := range items {
			ids = append(ids, it.ID)
			a := f.answers[it.ID]
			if a == "" {
				a = sessionsync.SyncPush
			}
			out = append(out, sessionsync.SyncAnswer{ID: it.ID, Action: a})
		}
		return out
	}
	aa, ca := answer(agents), answer(crews)
	var ia []sessionsync.SyncItemAnswer
	for _, it := range items {
		key := it.Kind + "/" + it.Name
		ids = append(ids, key)
		f.itemHashes[key] = it.SHA256
		if f.noItems {
			continue
		}
		a := f.answers[key]
		if a == "" {
			a = sessionsync.SyncPush
		}
		ia = append(ia, sessionsync.SyncItemAnswer{Kind: it.Kind, Name: it.Name, Action: a})
	}
	f.asked = append(f.asked, ids)
	return aa, ca, ia, nil
}

func (f *fakeRemote) LibrarySyncItem(_ context.Context, _ string, kind libitem.Kind, name string, body []byte) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := string(kind) + "/" + name
	if err := f.pushErr[key]; err != nil {
		return "", err
	}
	f.pushed = append(f.pushed, key)
	f.pushedBody[key] = string(body)
	f.versions++
	return libVer, nil
}

func (f *fakeRemote) LibrarySyncProfile(_ context.Context, _, id, markdown string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.pushErr[id]; err != nil {
		return "", err
	}
	f.pushed = append(f.pushed, id)
	f.versions++
	return libVer, nil
}

func (f *fakeRemote) LibrarySyncCrew(ctx context.Context, host, id string, crew json.RawMessage) (string, error) {
	return f.LibrarySyncProfile(ctx, host, id, string(crew))
}

type syncHarness struct {
	t      *testing.T
	d      *Daemon
	remote *fakeRemote
	on     bool
	log    strings.Builder
	now    time.Time
}

func newSyncHarness(t *testing.T) *syncHarness {
	t.Helper()
	t.Setenv("BELAI_HOME", t.TempDir())
	t.Setenv("VULNETIX_WEB_URL", "http://127.0.0.1:1")
	client, err := sessionsync.NewClient("http://127.0.0.1:1", func() (string, error) { return "ApiKey o:k", nil }, nil)
	if err != nil {
		t.Fatal(err)
	}
	h := &syncHarness{t: t, remote: newFakeRemote(), on: true, now: time.Now()}
	h.d, err = New(Options{
		Client: client, HostID: testHost, Exe: "/bin/belai", LibraryRemote: h.remote, Out: &h.log,
		SyncProfiles: func() bool { return h.on }, Now: func() time.Time { return h.now },
	})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func (h *syncHarness) tick() { h.d.syncLibrary(context.Background()) }

func TestAutoSyncPushesWhatTheServerSaysToPushAndThenLeavesItAlone(t *testing.T) {
	h := newSyncHarness(t)
	if _, err := agentprofile.Save(installable("reviewer", libID)); err != nil {
		t.Fatal(err)
	}
	h.tick()
	if len(h.remote.pushed) != 1 || h.remote.pushed[0] != libID {
		t.Fatalf("pushed = %v", h.remote.pushed)
	}
	// Nothing changed: the next check asks nothing.
	h.tick()
	if len(h.remote.asked) != 1 {
		t.Fatalf("a settled profile was asked about again: %v", h.remote.asked)
	}
	// An edit on the host is asked about, and pushed.
	edited := installable("reviewer", libID)
	edited.Description = "edited on the host"
	if _, err := agentprofile.Save(edited); err != nil {
		t.Fatal(err)
	}
	h.tick()
	if len(h.remote.asked) != 2 || len(h.remote.pushed) != 2 {
		t.Fatalf("asked %v, pushed %v", h.remote.asked, h.remote.pushed)
	}
}

func TestLibrarySyncNowAsksAboutSettledItemsAndReportsCountsOnly(t *testing.T) {
	h := newSyncHarness(t)
	for name, id := range map[string]string{"first": libID, "second": libID2} {
		if _, err := agentprofile.Save(distinct(name, id)); err != nil {
			t.Fatal(err)
		}
	}
	h.remote.answers[libID2] = sessionsync.SyncDiverged
	h.tick()
	if len(h.remote.asked) != 1 || len(h.remote.pushed) != 1 {
		t.Fatalf("first pass: asked %v, pushed %v", h.remote.asked, h.remote.pushed)
	}
	// The timer leaves settled items alone; a request asks about all of them.
	h.tick()
	if len(h.remote.asked) != 1 {
		t.Fatalf("the timer asked about settled items: %v", h.remote.asked)
	}
	h.remote.answers[libID] = sessionsync.SyncCurrent
	report, why := h.d.librarySyncNow(context.Background())
	if why != "" {
		t.Fatalf("refused: %s", why)
	}
	if len(h.remote.asked) != 2 || len(h.remote.asked[1]) != 2 {
		t.Fatalf("a request did not ask about every item: %v", h.remote.asked)
	}
	if report != "0 pushed, 1 already kept, 1 changed on the website since this host last installed it" {
		t.Fatalf("report = %q", report)
	}
	for _, name := range []string{"first", "second", libID, libID2} {
		if strings.Contains(report, name) {
			t.Fatalf("the report names an item: %q", report)
		}
	}
}

func TestLibrarySyncNowRefusesWithEverySwitchOffAndSaysWhyOnError(t *testing.T) {
	h := newSyncHarness(t)
	h.on = false
	itemsOn := h.d.o.SyncItem
	h.d.o.SyncItem = func(libitem.Kind) bool { return false }
	if _, why := h.d.librarySyncNow(context.Background()); !strings.Contains(why, "switched off") {
		t.Fatalf("with sync off: %q", why)
	}
	h.on = true
	h.d.o.SyncItem = itemsOn
	if _, err := agentprofile.Save(installable("reviewer", libID)); err != nil {
		t.Fatal(err)
	}
	h.remote.askErr = sessionsync.ErrNotFound
	if _, why := h.d.librarySyncNow(context.Background()); !strings.Contains(why, "does not take library sync") {
		t.Fatalf("not found: %q", why)
	}
}

func TestAutoSyncNeverPushesOverAWebEditAndAsksAgainLater(t *testing.T) {
	h := newSyncHarness(t)
	if _, err := agentprofile.Save(installable("reviewer", libID)); err != nil {
		t.Fatal(err)
	}
	h.remote.answers[libID] = sessionsync.SyncDiverged
	h.tick()
	if len(h.remote.pushed) != 0 || !strings.Contains(h.log.String(), "differs from the website's copy") {
		t.Fatalf("pushed %v, log %q", h.remote.pushed, h.log.String())
	}
	// It is not asked about again, nor logged again, until the answer goes stale.
	h.tick()
	if len(h.remote.asked) != 1 || strings.Count(h.log.String(), "differs from the website's copy") != 1 {
		t.Fatalf("asked %v, log %q", h.remote.asked, h.log.String())
	}
	h.now = h.now.Add(settledFor + time.Second)
	h.remote.answers[libID] = sessionsync.SyncPush
	h.tick()
	if len(h.remote.asked) != 2 || len(h.remote.pushed) != 1 {
		t.Fatalf("after the answer went stale: asked %v, pushed %v", h.remote.asked, h.remote.pushed)
	}
}

func TestAutoSyncTreatsAConflictOnPushAsDivergedAndKeepsGoing(t *testing.T) {
	h := newSyncHarness(t)
	for name, id := range map[string]string{"first": libID, "second": libID2} {
		if _, err := agentprofile.Save(distinct(name, id)); err != nil {
			t.Fatal(err)
		}
	}
	h.remote.pushErr[libID] = sessionsync.ErrConflict
	h.remote.pushErr[libID2] = errors.New("HTTP 500")
	h.tick()
	// The conflict settles one as diverged; a refused one is skipped and logged;
	// neither stops the other from being looked at.
	if !strings.Contains(h.log.String(), "was not taken") {
		t.Fatalf("log = %q", h.log.String())
	}
	h.tick()
	if len(h.remote.asked) != 1 {
		t.Fatalf("settled items were asked about again: %v", h.remote.asked)
	}
}

func TestAutoSyncOffMeansNoRequestsAndAFailureIsLoggedOnce(t *testing.T) {
	h := newSyncHarness(t)
	if _, err := agentprofile.Save(installable("reviewer", libID)); err != nil {
		t.Fatal(err)
	}
	h.on = false
	h.tick()
	if len(h.remote.asked) != 0 {
		t.Fatalf("sync.profiles is off but the host asked: %v", h.remote.asked)
	}
	h.on = true
	h.remote.askErr = errors.New("sessionsync: HTTP 502")
	h.tick()
	h.tick()
	if strings.Count(h.log.String(), "library sync:") != 1 {
		t.Fatalf("log = %q", h.log.String())
	}
	h.remote.askErr = nil
	h.tick()
	if !strings.Contains(h.log.String(), "working again") || len(h.remote.pushed) != 1 {
		t.Fatalf("log = %q, pushed %v", h.log.String(), h.remote.pushed)
	}
}

func TestAutoSyncSendsCrewsAndGivesThemIdsButNeverBuiltIns(t *testing.T) {
	h := newSyncHarness(t)
	if _, err := agentprofile.Save(worker("log", libID)); err != nil {
		t.Fatal(err)
	}
	dir, _ := agentprofile.CrewsDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "aws-infra.json"), []byte(`{"name":"aws-infra","description":"d","members":[{"profile":"log"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	h.tick()
	var crewPushed bool
	for _, id := range h.remote.pushed {
		if id == libID {
			continue
		}
		crewPushed = agentprofile.ValidID(id)
	}
	if !crewPushed || len(h.remote.pushed) != 2 {
		t.Fatalf("pushed %v: the crew should have been given an id and pushed", h.remote.pushed)
	}
	for _, it := range localItems(true, nil) {
		if strings.HasPrefix(it.name, "belai:") {
			t.Fatalf("a built-in %s %s is synced", it.kind, it.name)
		}
	}
}

func TestAutoSyncRunsWithTheDefaultClock(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	t.Setenv("VULNETIX_WEB_URL", "http://127.0.0.1:1")
	client, err := sessionsync.NewClient("http://127.0.0.1:1", func() (string, error) { return "ApiKey o:k", nil }, nil)
	if err != nil {
		t.Fatal(err)
	}
	var log strings.Builder
	remote := newFakeRemote()
	d, err := New(Options{Client: client, HostID: testHost, Exe: "/bin/belai", LibraryRemote: remote, Out: &log,
		SyncProfiles: func() bool { return true }})
	if err != nil {
		t.Fatal(err)
	}
	d.syncLibrary(context.Background())
}

func TestAutoSyncAgainstAWebsiteThatPredatesItBacksOffAndSaysWhy(t *testing.T) {
	h := newSyncHarness(t)
	if _, err := agentprofile.Save(installable("reviewer", libID)); err != nil {
		t.Fatal(err)
	}
	h.remote.askErr = sessionsync.ErrNotFound
	h.tick()
	if !strings.Contains(h.log.String(), "does not take automatic sync yet") || strings.Contains(h.log.String(), "not found") {
		t.Fatalf("log = %q", h.log.String())
	}
	// It is not asked again on the next checks, nor logged again.
	h.remote.mu.Lock()
	h.remote.askErr = errors.New("would be asked again")
	h.remote.mu.Unlock()
	h.now = h.now.Add(time.Minute)
	h.tick()
	h.tick()
	if strings.Count(h.log.String(), "library sync:") != 1 {
		t.Fatalf("it kept logging: %q", h.log.String())
	}
	// After the wait it asks again, and a website that has learned the routes is used.
	h.now = h.now.Add(unsupportedFor)
	h.remote.askErr = nil
	h.tick()
	if len(h.remote.pushed) != 1 {
		t.Fatalf("pushed %v after the website learned the routes", h.remote.pushed)
	}
}

// A replace keeps the files this host shares with its crew when the library copy
// lists none. Sharing needs a worktree, so the carry must never turn a valid
// library copy into a profile the host then refuses to save.
func TestReplaceCarriesSharedFilesAndTheWorktreeTheyNeed(t *testing.T) {
	h := newLibHarness(t)
	if _, err := agentprofile.Save(withSync(installable("analyzer", libID))); err != nil {
		t.Fatal(err)
	}
	// The library copy is a worker that changes nothing, with no workspace block at all.
	lib := withSync(installable("analyzer", libID))
	lib.Workspace = nil
	lib.Tools = []string{"Read", "Grep"}
	if status, why := h.install(lib, true); status != sessionsync.DispatchStarted {
		t.Fatalf("replace: %s %q", status, why)
	}
	got, err := agentprofile.Load("analyzer")
	if err != nil || got.IsolationMode() != agentprofile.IsolationWorktree || len(got.SyncPaths()) != 1 {
		t.Fatalf("installed = %+v %v", got.Workspace, err)
	}
}

func TestReplaceLeavesTheLibraryCopyAloneWhenTheSharedFilesCannotGoWithIt(t *testing.T) {
	h := newLibHarness(t)
	if _, err := agentprofile.Save(withSync(installable("analyzer", libID))); err != nil {
		t.Fatal(err)
	}
	// The library copy shares the repository itself, so there is no worktree to copy into.
	lib := withSync(installable("analyzer", libID))
	lib.Workspace = &agentprofile.WorkspaceSpec{Isolation: agentprofile.IsolationShared}
	status, why := h.install(lib, true)
	if status != sessionsync.DispatchStarted {
		t.Fatalf("a valid library copy must not be refused because of what the host shared: %s %q", status, why)
	}
	if !strings.Contains(why, "not carried over") {
		t.Fatalf("the acknowledgement should say what was left behind: %q", why)
	}
	got, err := agentprofile.Load("analyzer")
	if err != nil || got.IsolationMode() != agentprofile.IsolationShared || len(got.SyncPaths()) != 0 {
		t.Fatalf("installed = %+v %v", got.Workspace, err)
	}
}

func newFakeRemote() *fakeRemote {
	return &fakeRemote{answers: map[string]string{}, pushErr: map[string]error{}, itemHashes: map[string]string{}, pushedBody: map[string]string{}}
}
