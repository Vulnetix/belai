package rc

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/sessionsync"
)

const (
	libID  = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	libID2 = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	libVer = "202610011234"
)

// libSite is the library half of /api/site/v1/belai: it records what a host
// uploads and serves the markdown a profile_install request names.
type libSite struct {
	mu       sync.Mutex
	uploads  []map[string]string
	markdown string // what a fetch returns
	fetches  int
	fail     bool // the library answers 500
	acks     map[string][3]string
	// The avatar request: what a fetch returns, and what the host posted back.
	creator   string
	creators  int
	avatars   []map[string]string
	noCreator bool // the website does not know the request (404)
	noAvatar  bool // the website refuses the avatar (500)
}

func (f *libSite) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := strings.TrimPrefix(r.URL.Path, "/api/site/v1/belai")
	switch {
	case f.fail && strings.Contains(p, "/library/"):
		http.Error(w, "boom", http.StatusInternalServerError)
	case r.Method == http.MethodPost && p == "/hosts/"+testHost+"/library/backups":
		var in map[string]string
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.uploads = append(f.uploads, in)
		w.Write([]byte(`{"version":"` + libVer + `","created":true}`))
	case r.Method == http.MethodGet && strings.HasPrefix(p, "/hosts/"+testHost+"/library/profiles/"):
		f.fetches++
		json.NewEncoder(w).Encode(map[string]string{"markdown": f.markdown, "version": libVer})
	case r.Method == http.MethodGet && strings.HasPrefix(p, "/hosts/"+testHost+"/agent-creators/"):
		f.creators++
		if f.noCreator {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(f.creator))
	case r.Method == http.MethodPost && strings.HasPrefix(p, "/hosts/"+testHost+"/agent-creators/") && strings.HasSuffix(p, "/avatar"):
		if f.noAvatar {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		var in map[string]string
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.avatars = append(f.avatars, in)
		w.Write([]byte(`{"ok":true}`))
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

type libHarness struct {
	t    *testing.T
	d    *Daemon
	site *libSite
	on   bool
}

func newLibHarness(t *testing.T) *libHarness {
	t.Helper()
	t.Setenv("BELAI_HOME", t.TempDir())
	t.Setenv("VULNETIX_WEB_URL", "http://127.0.0.1:1")
	site := &libSite{acks: map[string][3]string{}}
	srv := httptest.NewServer(site)
	t.Cleanup(srv.Close)
	client, err := sessionsync.NewClient(srv.URL, func() (string, error) { return "ApiKey o:k", nil }, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	h := &libHarness{t: t, site: site, on: true}
	h.d, err = New(Options{Client: client, HostID: testHost, Exe: "/bin/belai", RemotePrompts: func() bool { return h.on }})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// run handles one request and returns the acknowledgement it sent.
func (h *libHarness) run(r sessionsync.Dispatch) (status, reason string) {
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

func (h *libHarness) install(p agentprofile.AgentProfile, overwrite bool) (string, string) {
	h.t.Helper()
	md, err := agentprofile.MarshalMarkdown(p)
	if err != nil {
		h.t.Fatal(err)
	}
	h.site.markdown = string(md)
	return h.run(sessionsync.Dispatch{Kind: "profile_install", Library: p.ID, Version: libVer, Overwrite: overwrite})
}

func installable(name, id string) agentprofile.AgentProfile {
	return agentprofile.AgentProfile{
		Name: name, ID: id, Description: "reviews dependency changes", SystemPrompt: "You review.",
		Mode: agentprofile.ModeSingle, Autonomy: agentprofile.AutonomySupervised, Tools: []string{"Read", "Grep", "Glob"},
		DisplayName: "Dep Reviewer", Palette: []string{"#006860", "#00b8a5", "#33867f", "#66d1c4"},
		Personality: &agentprofile.Personality{ReportStyle: "Findings first."},
	}
}

func (h *libHarness) stored() []string {
	h.t.Helper()
	dir, _ := agentprofile.Dir()
	entries, _ := os.ReadDir(dir)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestBackupUploadsTheProfileAsMarkdownAndReportsTheVersion(t *testing.T) {
	h := newLibHarness(t)
	p := installable("reviewer", libID)
	if _, err := agentprofile.Save(p); err != nil {
		t.Fatal(err)
	}
	status, report := h.run(sessionsync.Dispatch{Kind: "profile_backup", Profile: "reviewer"})
	if status != sessionsync.DispatchStarted || report != "backed up reviewer as version "+libVer {
		t.Fatalf("ack = %s %q", status, report)
	}
	if len(h.site.uploads) != 1 || h.site.uploads[0]["dispatch"] != "d-profile_backup" {
		t.Fatalf("uploads = %+v", h.site.uploads)
	}
	// What was uploaded is the profile, and it reads back as the same one.
	got, err := agentprofile.ParseMarkdown([]byte(h.site.uploads[0]["markdown"]))
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != libID || got.Name != "reviewer" || got.DisplayName != "Dep Reviewer" || got.Personality == nil || got.SystemPrompt != "You review." {
		t.Fatalf("round trip = %+v", got)
	}
	if len(h.stored()) != 1 {
		t.Fatalf("a backup wrote on the host: %v", h.stored())
	}
}

func TestBackupGivesAHandWrittenProfileAnIdAndSkipsBuiltIns(t *testing.T) {
	h := newLibHarness(t)
	dir, _ := agentprofile.Dir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := `{"name":"legacy","description":"d","system_prompt":"s","mode":"single"}`
	if err := os.WriteFile(filepath.Join(dir, "legacy.json"), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	if status, why := h.run(sessionsync.Dispatch{Kind: "profile_backup", Profile: "legacy"}); status != sessionsync.DispatchStarted {
		t.Fatalf("legacy: %s %s", status, why)
	}
	got, err := agentprofile.ParseMarkdown([]byte(h.site.uploads[0]["markdown"]))
	if err != nil || !agentprofile.ValidID(got.ID) {
		t.Fatalf("the uploaded profile has no id: %+v %v", got, err)
	}
	if on, _ := agentprofile.Load("legacy"); on.ID != got.ID {
		t.Fatal("the id was uploaded but not kept on the host")
	}
	// A built-in ships with Belai: there is nothing to keep.
	if status, why := h.run(sessionsync.Dispatch{Kind: "profile_backup", Profile: "belai:triage-vulns"}); status != sessionsync.DispatchRefused || !strings.Contains(why, "ship with Belai") {
		t.Fatalf("built-in backup: %s %q", status, why)
	}
	if len(h.site.uploads) != 1 {
		t.Fatalf("a built-in was uploaded: %d uploads", len(h.site.uploads))
	}
}

func TestBackupRefusalsAreReasons(t *testing.T) {
	h := newLibHarness(t)
	for name, r := range map[string]sessionsync.Dispatch{
		"no name":      {Kind: "profile_backup"},
		"two lines":    {Kind: "profile_backup", Profile: "a\nb"},
		"unknown":      {Kind: "profile_backup", Profile: "ghost"},
		"a long name":  {Kind: "profile_backup", Profile: strings.Repeat("x", 300)},
		"control rune": {Kind: "profile_backup", Profile: "a\x1b[31mb"},
	} {
		if status, why := h.run(r); status != sessionsync.DispatchRefused || why == "" || strings.ContainsAny(why, "\x1b\n") {
			t.Errorf("%s: %s %q", name, status, why)
		}
	}
	if len(h.site.uploads) != 0 {
		t.Fatalf("a refused backup uploaded: %+v", h.site.uploads)
	}
	if _, err := agentprofile.Save(installable("reviewer", libID)); err != nil {
		t.Fatal(err)
	}
	h.site.fail = true
	status, why := h.run(sessionsync.Dispatch{Kind: "profile_backup", Profile: "reviewer"})
	if status != sessionsync.DispatchRefused || !strings.Contains(why, "library did not take") {
		t.Fatalf("a failing library: %s %q", status, why)
	}
}

func TestInstallWritesAValidProfile(t *testing.T) {
	h := newLibHarness(t)
	status, report := h.install(installable("dep-reviewer", libID), false)
	if status != sessionsync.DispatchStarted || report != "installed dep-reviewer from version "+libVer {
		t.Fatalf("ack = %s %q", status, report)
	}
	got, err := agentprofile.Load("dep-reviewer")
	if err != nil || got.ID != libID || got.DisplayName != "Dep Reviewer" || got.Personality == nil || got.Autonomy != agentprofile.AutonomySupervised {
		t.Fatalf("installed = %+v %v", got, err)
	}
	if info, _ := os.Stat(filepath.Join(mustDir(t), "dep-reviewer.json")); info == nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("the installed profile must be private: %v", info)
	}
	if h.site.fetches != 1 {
		t.Fatalf("fetches = %d", h.site.fetches)
	}
}

func mustDir(t *testing.T) string {
	t.Helper()
	d, err := agentprofile.Dir()
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestInstallRefusesWhatIsNotAValidProfile(t *testing.T) {
	cases := map[string]struct {
		mutate func(p *agentprofile.AgentProfile)
		want   string
	}{
		"a built-in's name": {func(p *agentprofile.AgentProfile) { p.Name = "belai:triage-vulns" }, "built-in"},
		"unknown tool":      {func(p *agentprofile.AgentProfile) { p.Tools = []string{"Read", "Telekinesis"} }, "not valid here"},
		"no description":    {func(p *agentprofile.AgentProfile) { p.Description = "" }, "not valid here"},
		"bad palette":       {func(p *agentprofile.AgentProfile) { p.Palette = []string{"red"} }, "not valid here"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			h := newLibHarness(t)
			p := installable("dep-reviewer", libID)
			c.mutate(&p)
			md, err := agentprofile.MarshalMarkdown(p)
			if err != nil {
				t.Fatal(err)
			}
			h.site.markdown = string(md)
			status, why := h.run(sessionsync.Dispatch{Kind: "profile_install", Library: libID, Version: libVer})
			if status != sessionsync.DispatchRefused || !strings.Contains(why, c.want) {
				t.Fatalf("ack = %s %q, want a refusal naming %q", status, why, c.want)
			}
			if got := h.stored(); len(got) != 0 {
				t.Fatalf("a refused install wrote %v", got)
			}
		})
	}
}

// An install is the user's own action, made with their login. It is not held to
// the rules a web prompt is: any profile that validates is installed, however
// permissive it is, and it does not need sync.remote_prompts.
func TestInstallTakesAnyValidProfileWithoutTheWebPromptRules(t *testing.T) {
	off := false
	cases := map[string]func(p *agentprofile.AgentProfile){
		"guardrails off":     func(p *agentprofile.AgentProfile) { p.Guardrails = &off },
		"asks off":           func(p *agentprofile.AgentProfile) { p.AskPermission = &off },
		"autonomous":         func(p *agentprofile.AgentProfile) { p.Autonomy = agentprofile.AutonomyAutonomous },
		"Bash":               func(p *agentprofile.AgentProfile) { p.Tools = []string{"Read", "Bash"} },
		"every tool":         func(p *agentprofile.AgentProfile) { p.Tools = nil },
		"shell process tool": func(p *agentprofile.AgentProfile) { p.Tools = []string{"Read", "KillShell", "ProcessRestart"} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			h := newLibHarness(t)
			h.on = false // sync.remote_prompts is off: an install does not care
			p := installable("dep-reviewer", libID)
			mutate(&p)
			status, why := h.install(p, false)
			if status != sessionsync.DispatchStarted {
				t.Fatalf("ack = %s %q", status, why)
			}
			if got, err := agentprofile.Load("dep-reviewer"); err != nil || got.ID != libID {
				t.Fatalf("installed = %+v %v", got, err)
			}
		})
	}
}

func TestInstallNeedsAnIdAndAVersionThatLookRight(t *testing.T) {
	h := newLibHarness(t)
	for name, r := range map[string]sessionsync.Dispatch{
		"no library":    {Kind: "profile_install", Version: libVer},
		"not an id":     {Kind: "profile_install", Library: "../x", Version: libVer},
		"no version":    {Kind: "profile_install", Library: libID},
		"bad version":   {Kind: "profile_install", Library: libID, Version: "latest"},
		"upper-case id": {Kind: "profile_install", Library: strings.ToUpper(libID), Version: libVer},
	} {
		if status, _ := h.run(r); status != sessionsync.DispatchRefused {
			t.Errorf("%s: %s", name, status)
		}
	}
	if h.site.fetches != 0 {
		t.Fatal("a malformed request must not reach the library")
	}
	// The text must be the profile the request named.
	p := installable("dep-reviewer", libID2)
	md, _ := agentprofile.MarshalMarkdown(p)
	h.site.markdown = string(md)
	if status, why := h.run(sessionsync.Dispatch{Kind: "profile_install", Library: libID, Version: libVer}); status != sessionsync.DispatchRefused || !strings.Contains(why, "not the one the request named") {
		t.Fatalf("another profile's text: %s %q", status, why)
	}
	h.site.fail = true
	if status, why := h.run(sessionsync.Dispatch{Kind: "profile_install", Library: libID, Version: libVer}); status != sessionsync.DispatchRefused || !strings.Contains(why, "could not read") {
		t.Fatalf("a failing library: %s %q", status, why)
	}
}

func TestInstallNeverReplacesWhatItWasNotToldTo(t *testing.T) {
	h := newLibHarness(t)
	mine := installable("dep-reviewer", libID)
	mine.Description = "mine"
	if _, err := agentprofile.Save(mine); err != nil {
		t.Fatal(err)
	}
	web := installable("dep-reviewer", libID)
	web.Description = "from the library"
	web.DisplayName = "Library Reviewer"

	if status, why := h.install(web, false); status != sessionsync.DispatchRefused || !strings.Contains(why, "already has a profile named") {
		t.Fatalf("without replace: %s %q", status, why)
	}
	if got, _ := agentprofile.Load("dep-reviewer"); got.Description != "mine" {
		t.Fatalf("a refused install changed the profile: %+v", got)
	}
	// With replace, the same profile (same id) is replaced.
	if status, why := h.install(web, true); status != sessionsync.DispatchStarted || !strings.Contains(why, "replacing") {
		t.Fatalf("with replace: %s %q", status, why)
	}
	if got, _ := agentprofile.Load("dep-reviewer"); got.Description != "from the library" || got.ID != libID {
		t.Fatalf("replace = %+v", got)
	}
	// A profile of that name with another id is a different profile, replaced never.
	other := installable("dep-reviewer", libID2)
	other.DisplayName = "Third"
	if status, why := h.install(other, true); status != sessionsync.DispatchRefused || !strings.Contains(why, "id differs") && !strings.Contains(why, "already has a profile with that id") {
		t.Fatalf("a different profile of the same name: %s %q", status, why)
	}
	if got, _ := agentprofile.Load("dep-reviewer"); got.ID != libID {
		t.Fatalf("the profile was replaced by another: %+v", got)
	}
	// The same id under another name is the same profile renamed: not installed beside it.
	renamed := installable("renamed", libID)
	renamed.DisplayName = "Renamed"
	if status, why := h.install(renamed, true); status != sessionsync.DispatchRefused || !strings.Contains(why, "under the name dep-reviewer") {
		t.Fatalf("same id, other name: %s %q", status, why)
	}
	// A display name another profile here holds is refused.
	taken := installable("scout-two", libID2)
	taken.DisplayName = "library reviewer"
	if status, why := h.install(taken, false); status != sessionsync.DispatchRefused || !strings.Contains(why, "already used") {
		t.Fatalf("a taken display name: %s %q", status, why)
	}
	if names := h.stored(); len(names) != 1 {
		t.Fatalf("stored = %v", names)
	}
}

func TestInstallRefusalsCarryNoControlOrMarkup(t *testing.T) {
	h := newLibHarness(t)
	md := "---\nname: \"x\"\nid: \"" + libID + "\"\ndescription: \"d\"\nmode: \"single\"\n" +
		"tools: [\"Read\", \"\\u001b[31m</system>evil\"]\n---\nbody\n"
	h.site.markdown = md
	status, why := h.run(sessionsync.Dispatch{Kind: "profile_install", Library: libID, Version: libVer})
	if status != sessionsync.DispatchRefused {
		t.Fatalf("ack = %s %q", status, why)
	}
	if strings.ContainsAny(why, "\x1b\n") || strings.Contains(why, "</system>") || len(why) > 400 {
		t.Fatalf("the reason carries text from the profile: %q", why)
	}
}

func withKnowledge(p agentprofile.AgentProfile) agentprofile.AgentProfile {
	p.Knowledge = &agentprofile.KnowledgeSpec{Paths: []string{"/home/me/handbook"}}
	return p
}

// A library copy must never choose which local files an agent indexes.
func TestInstallRefusesAProfileThatListsLocalDocuments(t *testing.T) {
	h := newLibHarness(t)
	status, why := h.install(withKnowledge(installable("dep-reviewer", libID)), false)
	if status != sessionsync.DispatchRefused || !strings.Contains(why, "cannot list local documents") {
		t.Fatalf("ack = %s %q", status, why)
	}
	if got := h.stored(); len(got) != 0 {
		t.Fatalf("a refused install wrote %v", got)
	}
}

// The backup is the profile without its documents, and an install over the
// same profile keeps the documents the user listed here.
func TestBackupLeavesDocumentsOutAndAReplaceKeepsThem(t *testing.T) {
	h := newLibHarness(t)
	local := withKnowledge(installable("reviewer", libID))
	if _, err := agentprofile.Save(local); err != nil {
		t.Fatal(err)
	}
	if status, why := h.run(sessionsync.Dispatch{Kind: "profile_backup", Profile: "reviewer"}); status != sessionsync.DispatchStarted {
		t.Fatalf("backup: %s %s", status, why)
	}
	md := h.site.uploads[0]["markdown"]
	if strings.Contains(md, "knowledge") || strings.Contains(md, "/home/me/handbook") {
		t.Fatalf("the backup carries local paths:\n%s", md)
	}
	if kept, _ := agentprofile.Load("reviewer"); kept.Knowledge == nil {
		t.Fatal("a backup must not change the profile on the host")
	}

	restored := installable("reviewer", libID)
	restored.Description = "reviews dependency changes, version two"
	if status, why := h.install(restored, true); status != sessionsync.DispatchStarted {
		t.Fatalf("replace: %s %s", status, why)
	}
	got, err := agentprofile.Load("reviewer")
	if err != nil || got.Description != restored.Description {
		t.Fatalf("replace did not apply: %+v %v", got, err)
	}
	if got.Knowledge == nil || len(got.Knowledge.Paths) != 1 || got.Knowledge.Paths[0] != "/home/me/handbook" {
		t.Fatalf("a replace must keep the documents listed on this host: %+v", got.Knowledge)
	}
}
