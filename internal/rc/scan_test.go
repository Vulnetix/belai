package rc

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/docparity"
	"github.com/vulnetix/belai/internal/harness"
	"github.com/vulnetix/belai/internal/libitem"
	"github.com/vulnetix/belai/internal/libscan"
	"github.com/vulnetix/belai/internal/sessionsync"
)

// scanHost is an itemHarness whose home directory holds a few harnesses' files
// and a trusted repository with none.
type scanHost struct {
	*itemHarness
	userHome, repo, empty string
}

func newScanHost(t *testing.T) *scanHost {
	t.Helper()
	h := newItemHarness(t)
	s := &scanHost{itemHarness: h, userHome: t.TempDir(), repo: t.TempDir(), empty: t.TempDir()}
	t.Setenv("HOME", s.userHome)
	oldH, oldR, oldB := libscan.Harnesses, libscan.TrustedRepos, scanBudget
	libscan.Harnesses = func() []harness.Harness {
		return []harness.Harness{
			{ID: "claude-code", Name: "Claude Code", Detect: []string{"~/.claude"}, Format: "claude-code", Dirs: map[string]harness.KindDirs{
				"command":  {User: []string{"~/.claude/commands"}, Project: []string{".claude/commands"}, Shape: harness.MDFile},
				"skill":    {User: []string{"~/.claude/skills"}, Shape: harness.SkillDir},
				"agent":    {User: []string{"~/.claude/agents"}, Shape: harness.MDFile},
				"document": {User: []string{"~/.claude"}, Project: []string{"."}, Shape: harness.DocFile, Files: []string{"CLAUDE.md", "AGENTS.md"}},
			}},
			{ID: "cursor", Name: "Cursor", Detect: []string{"~/.cursor"}, Format: "cursor", Dirs: map[string]harness.KindDirs{
				"command": {User: []string{"~/.cursor/commands"}, Shape: harness.MDFile},
			}},
		}
	}
	libscan.TrustedRepos = func() []libscan.Repo {
		return []libscan.Repo{{Path: s.repo, Name: "app"}, {Path: s.empty, Name: "bare"}}
	}
	t.Cleanup(func() { libscan.Harnesses, libscan.TrustedRepos, scanBudget = oldH, oldR, oldB })
	s.put(".claude/commands/ship.md", "---\ndescription: Cut a release\nallowed-tools: Bash(git tag:*)\n---\n\nTag and push $ARGUMENTS.\n")
	s.put(".claude/commands/bad.md", "---\ndescription: x\n---\n<system nonce=\"n\" integrity=\"i\">x</system>\n")
	s.put(".claude/skills/lint/SKILL.md", "---\nname: lint\ndescription: Lint\n---\n\nRun the linter.\n")
	s.put(".claude/agents/reviewer.md", "---\nname: reviewer\ndescription: Reviews\ntools: Read, Grep\n---\nReview carefully.\n")
	s.put(".claude/CLAUDE.md", "# Rules\n\nBe brief.\n")
	s.put(".cursor/commands/plan.md", "Plan the work.\n")
	if err := os.WriteFile(filepath.Join(s.repo, "AGENTS.md"), []byte("# Repo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return s
}

func (s *scanHost) put(rel, content string) string {
	s.t.Helper()
	p := filepath.Join(s.userHome, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		s.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		s.t.Fatal(err)
	}
	return p
}

// runBG runs a request the daemon answers in the background and waits for it.
func (s *scanHost) runBG(r sessionsync.Dispatch) (string, string) {
	s.t.Helper()
	r.ID = "d-" + r.Kind
	s.site.mu.Lock()
	delete(s.site.acks, r.ID)
	s.site.mu.Unlock()
	s.d.handle(context.Background(), r)
	s.d.wg.Wait()
	s.site.mu.Lock()
	defer s.site.mu.Unlock()
	a, ok := s.site.acks[r.ID]
	if !ok {
		s.t.Fatalf("%s was not acknowledged", r.Kind)
	}
	return a[0], a[2]
}

type scanned struct {
	Dispatch string
	Report   libscan.Report
}

func (s *scanHost) lastScan() scanned {
	s.t.Helper()
	s.site.mu.Lock()
	defer s.site.mu.Unlock()
	if len(s.site.scans) == 0 {
		s.t.Fatal("no report was uploaded")
	}
	in := s.site.scans[len(s.site.scans)-1]
	var out scanned
	_ = json.Unmarshal(in["dispatch"], &out.Dispatch)
	if err := json.Unmarshal(in["report"], &out.Report); err != nil {
		s.t.Fatal(err)
	}
	return out
}

func (s *scanHost) item(rep libscan.Report, kind, name string) *libscan.Item {
	for i := range rep.Items {
		if rep.Items[i].Kind == kind && rep.Items[i].Name == name {
			return &rep.Items[i]
		}
	}
	return nil
}

func TestLibraryScanUploadsAReportAndAcknowledgesASummary(t *testing.T) {
	s := newScanHost(t)
	// Always on: every library switch is off and the scan still runs.
	for k := range s.on {
		s.on[k] = false
	}
	status, why := s.runBG(sessionsync.Dispatch{Kind: "library_scan"})
	if status != sessionsync.DispatchStarted || !strings.Contains(why, "found in") || !strings.Contains(why, "locations") {
		t.Fatalf("scan: %s %q", status, why)
	}
	got := s.lastScan()
	if got.Dispatch != "d-library_scan" || got.Report.Partial {
		t.Fatalf("%+v", got)
	}
	ship := s.item(got.Report, "command", "ship")
	if ship == nil || ship.Harness != "claude-code" || ship.Verdict != libscan.Valid || ship.Path != filepath.Join(s.userHome, ".claude/commands/ship.md") {
		t.Fatalf("ship = %+v", ship)
	}
	if bad := s.item(got.Report, "command", "bad"); bad == nil || bad.Verdict != libscan.Invalid {
		t.Errorf("bad = %+v", bad)
	}
	for _, want := range [][2]string{{"skill", "lint"}, {"agent", "reviewer"}, {"document", "CLAUDE.md"}, {"command", "plan"}, {"document", "AGENTS.md"}} {
		if s.item(got.Report, want[0], want[1]) == nil {
			t.Errorf("%v not reported", want)
		}
	}
	// The trusted repository with nothing is listed with where it looked.
	var bare *libscan.RepoReport
	for i := range got.Report.Repos {
		if got.Report.Repos[i].Name == "bare" {
			bare = &got.Report.Repos[i]
		}
	}
	if bare == nil || len(bare.Found) != 0 || len(bare.Looked) == 0 {
		t.Errorf("bare = %+v", bare)
	}
	// What leaves the host is a report, never a document body.
	s.site.mu.Lock()
	raw := string(s.site.scans[0]["report"])
	s.site.mu.Unlock()
	if strings.Contains(raw, "Tag and push") || strings.Contains(raw, "Review carefully") {
		t.Error("the report carries document text")
	}
	if !strings.Contains(s.log.String(), "library_scan: ") {
		t.Errorf("log = %q", s.log.String())
	}
}

func TestLibraryScanKindFilterAndRefusals(t *testing.T) {
	s := newScanHost(t)
	if status, why := s.runBG(sessionsync.Dispatch{Kind: "library_scan", ItemKind: "skill"}); status != sessionsync.DispatchStarted {
		t.Fatalf("%s %q", status, why)
	}
	for _, it := range s.lastScan().Report.Items {
		if it.Kind != "skill" {
			t.Errorf("a %s in a skill scan", it.Kind)
		}
	}
	for _, k := range []string{"agent", "crew", "document"} {
		if status, why := s.runBG(sessionsync.Dispatch{Kind: "library_scan", ItemKind: k}); status != sessionsync.DispatchStarted {
			t.Fatalf("%s: %s %q", k, status, why)
		}
		items := s.lastScan().Report.Items
		for _, it := range items {
			if it.Kind != k {
				t.Errorf("a %s in a %s scan", it.Kind, k)
			}
		}
		if k != "crew" && len(items) == 0 {
			t.Errorf("%s scan found nothing", k)
		}
	}
	for _, bad := range []string{"skills", "launch", "../x"} {
		if status, why := s.runBG(sessionsync.Dispatch{Kind: "library_scan", ItemKind: bad}); status != sessionsync.DispatchRefused || !strings.Contains(why, "not an item kind") {
			t.Errorf("%q: %s %q", bad, status, why)
		}
	}
	// A scan already running refuses the next.
	s.d.scanSlot <- struct{}{}
	if status, why := s.runBG(sessionsync.Dispatch{Kind: "library_scan"}); status != sessionsync.DispatchRefused || !strings.Contains(why, "already scanning") {
		t.Errorf("busy: %s %q", status, why)
	}
	<-s.d.scanSlot
}

func TestLibraryScanIsPartialAtItsBudget(t *testing.T) {
	s := newScanHost(t)
	scanBudget = time.Nanosecond
	if status, why := s.runBG(sessionsync.Dispatch{Kind: "library_scan"}); status != sessionsync.DispatchStarted || !strings.Contains(why, "partial") {
		t.Fatalf("%s %q", status, why)
	}
	if !s.lastScan().Report.Partial {
		t.Error("the report is not flagged partial")
	}
	if scanBudget != time.Nanosecond {
		t.Error("budget changed")
	}
}

func TestLibraryScanRefusedWhenTheLibraryTakesNoReport(t *testing.T) {
	s := newScanHost(t)
	s.site.fail = true
	status, why := s.runBG(sessionsync.Dispatch{Kind: "library_scan"})
	if status != sessionsync.DispatchRefused || !strings.Contains(why, "did not take the scan report") {
		t.Fatalf("%s %q", status, why)
	}
}

func TestScanBudgetIsTwentyFiveSeconds(t *testing.T) {
	if scanBudget != 25*time.Second {
		t.Errorf("scanBudget = %v", scanBudget)
	}
}

// importOf runs a library_import for the item a fresh scan reported.
func (s *scanHost) importOf(kind, name string, mutate func(*sessionsync.Dispatch)) (string, string) {
	s.t.Helper()
	if status, why := s.runBG(sessionsync.Dispatch{Kind: "library_scan"}); status != sessionsync.DispatchStarted {
		s.t.Fatalf("scan: %s %q", status, why)
	}
	it := s.item(s.lastScan().Report, kind, name)
	if it == nil {
		s.t.Fatalf("%s %s was not scanned", kind, name)
	}
	r := sessionsync.Dispatch{Kind: "library_import", ItemKind: kind, Path: it.Path, SHA256: it.SHA256, ScanDispatch: "scan-1"}
	if mutate != nil {
		mutate(&r)
	}
	return s.runBG(r)
}

func (s *scanHost) lastImport() map[string]json.RawMessage {
	s.t.Helper()
	s.site.mu.Lock()
	defer s.site.mu.Unlock()
	if len(s.site.imports) == 0 {
		s.t.Fatal("nothing was imported")
	}
	return s.site.imports[len(s.site.imports)-1]
}

func (s *scanHost) imports() int {
	s.site.mu.Lock()
	defer s.site.mu.Unlock()
	return len(s.site.imports)
}

func TestLibraryImportUploadsTheCanonicalItem(t *testing.T) {
	s := newScanHost(t)
	for k := range s.on {
		s.on[k] = false
	}
	status, why := s.importOf("command", "ship", nil)
	if status != sessionsync.DispatchStarted || !strings.Contains(why, "imported command ship from claude-code as version "+itemVer) {
		t.Fatalf("%s %q", status, why)
	}
	up := s.lastImport()
	var body, kind, name, dispatch string
	_ = json.Unmarshal(up["body"], &body)
	_ = json.Unmarshal(up["kind"], &kind)
	_ = json.Unmarshal(up["name"], &name)
	_ = json.Unmarshal(up["dispatch"], &dispatch)
	if kind != "command" || name != "ship" || dispatch != "d-library_import" {
		t.Fatalf("upload = %v", up)
	}
	d, err := libitem.ParseCommand([]byte(body))
	if err != nil || d.Name != "ship" || len(d.AllowedTools) != 0 || !strings.Contains(d.Body, "Tag and push") {
		t.Fatalf("body %q: %+v %v", body, d, err)
	}
	if _, has := up["target"]; has {
		t.Error("a command carries a target")
	}
	// The host's own library is untouched by an import.
	if _, err := os.Stat(filepath.Join(s.home, "commands")); !os.IsNotExist(err) {
		t.Errorf("an import wrote on the host: %v", err)
	}
}

func TestLibraryImportBodiesByKind(t *testing.T) {
	s := newScanHost(t)
	// An agent is profile Markdown (a string).
	if status, why := s.importOf("agent", "reviewer", nil); status != sessionsync.DispatchStarted {
		t.Fatalf("agent: %s %q", status, why)
	}
	var md string
	if err := json.Unmarshal(s.lastImport()["body"], &md); err != nil || !strings.Contains(md, "Review carefully") {
		t.Errorf("agent body = %s", s.lastImport()["body"])
	}
	// A document needs the agent that receives it, and carries it.
	if status, why := s.importOf("document", "CLAUDE.md", nil); status != sessionsync.DispatchRefused || !strings.Contains(why, "agent that receives") {
		t.Fatalf("document without a target: %s %q", status, why)
	}
	before := s.imports()
	if status, why := s.importOf("document", "CLAUDE.md", func(r *sessionsync.Dispatch) { r.Target = "reviewer" }); status != sessionsync.DispatchStarted {
		t.Fatalf("document: %s %q", status, why)
	}
	if s.imports() != before+1 {
		t.Error("the document was not uploaded")
	}
	var tgt string
	_ = json.Unmarshal(s.lastImport()["target"], &tgt)
	if tgt != "reviewer" {
		t.Errorf("target = %q", tgt)
	}
	// A JSON kind is an object: a Belai process and a crew.
	if err := os.MkdirAll(filepath.Join(s.home, "processes"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(s.home, "processes", "010-build.json"), []byte(`{"command":"make"}`), 0o644)
	os.MkdirAll(filepath.Join(s.home, "profiles", "crews"), 0o755)
	os.WriteFile(filepath.Join(s.home, "profiles", "crews", "team.json"), []byte(`{"name":"team","members":[{"profile":"scout"}]}`), 0o644)
	for _, k := range [][2]string{{"process", "build"}, {"crew", "team"}} {
		if status, why := s.importOf(k[0], k[1], nil); status != sessionsync.DispatchStarted {
			t.Fatalf("%s: %s %q", k[0], status, why)
		}
		if b := strings.TrimSpace(string(s.lastImport()["body"])); !strings.HasPrefix(b, "{") {
			t.Errorf("%s body is not an object: %s", k[0], b)
		}
	}
}

func TestLibraryImportRefusals(t *testing.T) {
	s := newScanHost(t)
	ship := filepath.Join(s.userHome, ".claude/commands/ship.md")

	// The file changed after the scan.
	status, why := s.importOf("command", "ship", func(r *sessionsync.Dispatch) {
		s.put(".claude/commands/ship.md", "---\ndescription: Cut a release\n---\n\nSomething else now.\n")
	})
	if status != sessionsync.DispatchRefused || !strings.Contains(why, "changed since the scan") {
		t.Fatalf("changed: %s %q", status, why)
	}
	s.put(".claude/commands/ship.md", "---\ndescription: Cut a release\nallowed-tools: Bash(git tag:*)\n---\n\nTag and push $ARGUMENTS.\n")

	cases := []struct {
		name   string
		mutate func(*sessionsync.Dispatch)
		want   string
	}{
		{"a path outside every root", func(r *sessionsync.Dispatch) { r.Path = "/etc/passwd" }, "not in a place this host scans"},
		{"a path in the home but not a scanned place", func(r *sessionsync.Dispatch) { r.Path = filepath.Join(s.userHome, ".ssh/id_rsa") }, "not in a place this host scans"},
		{"a traversal", func(r *sessionsync.Dispatch) {
			r.Path = filepath.Join(s.userHome, ".claude/commands/../../.ssh/id_rsa")
		}, "not in a place this host scans"},
		{"a relative path", func(r *sessionsync.Dispatch) { r.Path = ".claude/commands/ship.md" }, "not in a place this host scans"},
		{"a kind the root does not hold", func(r *sessionsync.Dispatch) { r.ItemKind = "skill" }, "not in a place this host scans"},
		{"an unknown kind", func(r *sessionsync.Dispatch) { r.ItemKind = "launch" }, "not an item kind"},
		{"no hash", func(r *sessionsync.Dispatch) { r.SHA256 = "" }, "no item hash"},
		{"a short hash", func(r *sessionsync.Dispatch) { r.SHA256 = "abc" }, "no item hash"},
		{"a wrong hash", func(r *sessionsync.Dispatch) { r.SHA256 = strings.Repeat("0", 64) }, "changed since the scan"},
		{"no path", func(r *sessionsync.Dispatch) { r.Path = "" }, "no path"},
		{"a long path", func(r *sessionsync.Dispatch) { r.Path = "/" + strings.Repeat("a", 1100) }, "no path"},
		{"an invalid item", func(r *sessionsync.Dispatch) {
			r.Path = filepath.Join(s.userHome, ".claude/commands/bad.md")
			r.SHA256 = strings.Repeat("a", 64)
		}, "refused"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			before := s.imports()
			status, why := s.importOf("command", "ship", c.mutate)
			if status != sessionsync.DispatchRefused || !strings.Contains(why, c.want) {
				t.Fatalf("%s %q, want a refusal naming %q", status, why, c.want)
			}
			if s.imports() != before {
				t.Error("something was uploaded")
			}
			if strings.Contains(why, "Tag and push") {
				t.Error("the reason carries the document")
			}
		})
	}

	// A link swapped in for the file after the scan is refused, and so is one for its folder.
	status, why = s.importOf("command", "ship", func(r *sessionsync.Dispatch) {
		os.Remove(ship)
		os.Symlink(filepath.Join(s.userHome, ".claude/commands/bad.md"), ship)
	})
	if status != sessionsync.DispatchRefused || !strings.Contains(why, "symbolic link") {
		t.Fatalf("link swap: %s %q", status, why)
	}
	os.Remove(ship)
	s.put(".claude/commands/ship.md", "---\ndescription: Cut a release\n---\n\nx\n")
	other := t.TempDir()
	os.WriteFile(filepath.Join(other, "SKILL.md"), []byte("---\nname: lint\ndescription: Lint\n---\n\nx\n"), 0o644)
	status, why = s.importOf("skill", "lint", func(r *sessionsync.Dispatch) {
		os.RemoveAll(filepath.Join(s.userHome, ".claude/skills/lint"))
		os.Symlink(other, filepath.Join(s.userHome, ".claude/skills/lint"))
	})
	if status != sessionsync.DispatchRefused {
		t.Fatalf("a folder link: %s %q", status, why)
	}
}

func TestLibraryImportRefusesALibraryFailure(t *testing.T) {
	s := newScanHost(t)
	if status, why := s.runBG(sessionsync.Dispatch{Kind: "library_scan"}); status != sessionsync.DispatchStarted {
		t.Fatal(why)
	}
	it := s.item(s.lastScan().Report, "command", "ship")
	s.site.fail = true
	status, why := s.runBG(sessionsync.Dispatch{Kind: "library_import", ItemKind: "command", Path: it.Path, SHA256: it.SHA256})
	if status != sessionsync.DispatchRefused || !strings.Contains(why, "library did not take the command") {
		t.Fatalf("%s %q", status, why)
	}
}

func TestOnlyATrustedRepositoryIsRead(t *testing.T) {
	s := newScanHost(t)
	stranger := t.TempDir()
	p := filepath.Join(stranger, ".claude/commands/x.md")
	os.MkdirAll(filepath.Dir(p), 0o755)
	os.WriteFile(p, []byte("---\ndescription: x\n---\nx\n"), 0o644)
	sum := strings.Repeat("a", 64)
	status, why := s.runBG(sessionsync.Dispatch{Kind: "library_import", ItemKind: "command", Path: p, SHA256: sum})
	if status != sessionsync.DispatchRefused || !strings.Contains(why, "not in a place this host scans") {
		t.Fatalf("%s %q", status, why)
	}
	// The same file is found, and read, once its repository is trusted.
	libscan.TrustedRepos = func() []libscan.Repo { return []libscan.Repo{{Path: stranger, Name: "x"}} }
	if status, why := s.runBG(sessionsync.Dispatch{Kind: "library_scan"}); status != sessionsync.DispatchStarted {
		t.Fatal(why)
	}
	if s.item(s.lastScan().Report, "command", "x") == nil {
		t.Error("a trusted repository's command is not reported")
	}
}

func TestLibraryRequestsAreDocumented(t *testing.T) {
	doc := docparity.Read(t, "docs/remote-control.md")
	for _, want := range []string{"`library_scan`", "`library_import`", "library/scans", "library/imports"} {
		if !strings.Contains(doc, want) {
			t.Errorf("docs/remote-control.md lacks %q", want)
		}
	}
	audit := docparity.Read(t, "docs/audit.md")
	for _, want := range []string{"library_scan", "library_import"} {
		if !strings.Contains(audit, want) {
			t.Errorf("docs/audit.md lacks %q", want)
		}
	}
}
