package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/decisions"
	"github.com/vulnetix/belai/internal/explore"
	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/rolemanager/jev"
	"github.com/vulnetix/belai/internal/tools"
)

// labelDecider scores every item by the first table key its label contains.
type labelDecider struct {
	mu       sync.Mutex
	table    map[string]float64
	backend  decisions.Backend
	err      error
	labels   []string
	requests int
}

func (d *labelDecider) Decide(_ context.Context, r decisions.Request) (decisions.Result, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.requests++
	if d.err != nil {
		return decisions.Result{}, d.err
	}
	state := r.State.(map[string]any)
	items := state["items"].(map[string]string)
	out := map[string]decisions.Answer{}
	for id, label := range items {
		d.labels = append(d.labels, label)
		best := -1
		for sub, p := range d.table {
			if strings.Contains(label, sub) && len(sub) > best {
				best = len(sub)
				out["s:"+id] = decisions.Answer{Type: decisions.TypeNoul, Noul: p}
			}
		}
	}
	return decisions.Result{Answers: out}, nil
}
func (d *labelDecider) Identity() string { return "fake/jev-9" }

// Hosted says this fake is a server the user runs.
func (d *labelDecider) Hosted() bool { return false }
func (d *labelDecider) Backend() decisions.Backend {
	if d.backend == "" {
		return decisions.BackendSystemOne
	}
	return d.backend
}

func locateSession(t *testing.T, dec *labelDecider) *Session {
	t.Helper()
	root := t.TempDir()
	for rel, body := range map[string]string{
		"internal/auth/login.go":      "package auth\n\nfunc Login() {}\nfunc VerifyPassword() {}\n",
		"internal/auth/login_test.go": "package auth\n\nfunc TestLogin() {}\n",
		"internal/billing/invoice.go": "package billing\n\nfunc Invoice() {}\n",
		"docs/auth.md":                "# Auth\n",
		".env":                        "SECRET=1\n",
		"node_modules/x/login.js":     "function login() {}\n",
	} {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	s := &Session{workdir: root}
	if dec != nil {
		s.jev = &jev.Jobs{Client: jev.NewWith(dec)}
	}
	return s
}

func TestLocateRanksWithTheBackendAndNamesNoFileText(t *testing.T) {
	dec := &labelDecider{table: map[string]float64{"internal/auth": 0.9, "docs": 0.05, "internal/auth/login.go": 0.97, "login_test.go": 0.6}}
	s := locateSession(t, dec)
	res := s.locateFiles(context.Background(), "where is the verify password check", 10)
	if res.By != "jev-9" || len(res.Hits) != 2 {
		t.Fatalf("result = %+v", res)
	}
	if h := res.Hits[0]; h.Path != "internal/auth/login.go" || h.Percent != 97 || h.Line != 4 || h.Lead {
		t.Errorf("top hit = %+v", h)
	}
	for _, l := range dec.labels {
		for _, banned := range []string{".env", "node_modules", "SECRET", "package auth"} {
			if strings.Contains(l, banned) {
				t.Errorf("the backend was shown %q: %q", banned, l)
			}
		}
	}
}

func TestLocatePreviewsFollowTheBackendAndTheSetting(t *testing.T) {
	cases := []struct {
		name    string
		backend decisions.Backend
		pref    string
		want    bool
	}{
		{"self-hosted, default", decisions.BackendSystemOne, "", true},
		{"local, default", decisions.BackendLocal, "", true},
		{"hosted, default", decisions.BackendOpenRouter, "", false},
		{"hosted, allowed", decisions.BackendOpenRouter, "hosted", true},
		{"self-hosted, off", decisions.BackendSystemOne, "off", false},
	}
	for _, c := range cases {
		dec := &labelDecider{backend: c.backend, table: map[string]float64{"": 0.9}}
		s := locateSession(t, dec)
		s.settings.Jev = nil
		if c.pref != "" {
			s.settings.Jev = &config.JevSettings{LocatePreviews: c.pref}
		}
		s.locateFiles(context.Background(), "login", 10)
		got := false
		for _, l := range dec.labels {
			if strings.Contains(l, "declares") {
				got = true
			}
		}
		if got != c.want {
			t.Errorf("%s: declarations in labels = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestLocateFallsBackToKeywordsWhenTheBackendFails(t *testing.T) {
	dec := &labelDecider{err: context.DeadlineExceeded}
	s := locateSession(t, dec)
	res := s.locateFiles(context.Background(), "login password", 10)
	if res.By != "keywords" || len(res.Hits) == 0 || res.Hits[0].Path != "internal/auth/login.go" {
		t.Fatalf("result = %+v", res)
	}
}

func TestLocateNeverRanksAFileWhoseReadWasWithheld(t *testing.T) {
	dec := &labelDecider{table: map[string]float64{"": 0.9}}
	s := locateSession(t, dec)
	abs, _ := filepath.EvalSymlinks(filepath.Join(s.workdir, "internal", "auth", "login.go"))
	s.flagged.flag(tools.Result{Kind: tools.KindRead, Meta: map[string]any{"abs": abs}}, rolemanager.SentinelPromptInjection)
	res := s.locateFiles(context.Background(), "login", 10)
	for _, h := range res.Hits {
		if h.Path == "internal/auth/login.go" {
			t.Errorf("a flagged file was ranked: %+v", h)
		}
	}
	for _, l := range dec.labels {
		if strings.Contains(l, "internal/auth/login.go") {
			t.Errorf("a flagged file was shown to the backend: %q", l)
		}
	}
}

func TestLocateIsOffWithoutABackend(t *testing.T) {
	s := locateSession(t, nil)
	if res := s.locateFiles(context.Background(), "login", 10); len(res.Hits) != 0 {
		t.Errorf("no backend, but hits = %+v", res)
	}
	if got := s.seedTasks(context.Background(), []explore.Task{{Prompt: "x"}}, "login"); got[0].Prompt != "x" {
		t.Errorf("no backend, but the task changed: %q", got[0].Prompt)
	}
}

func TestSeedTasksNamesFilesAndDropsWhatTheRankingDoesNotBack(t *testing.T) {
	dec := &labelDecider{table: map[string]float64{"internal/auth": 0.9, "internal/auth/login.go": 0.97, "internal/auth/login_test.go": 0.02, "docs/auth.md": 0.02, "docs": 0.02}}
	s := locateSession(t, dec)
	tasks := explore.PlanSurveyWithEntrypoints("fix login", []string{"cmd/x"})
	got := s.seedTasks(context.Background(), tasks, "fix the login password check")
	var refs []string
	for _, tk := range got {
		refs = append(refs, tk.Reference)
		if !strings.Contains(tk.Prompt, "internal/auth/login.go:3") {
			t.Errorf("task %q was not seeded: %q", tk.Reference, tk.Prompt)
		}
		if !strings.HasSuffix(tk.Prompt, explore.ReportContract()) {
			t.Errorf("task %q lost its report contract", tk.Reference)
		}
	}
	for _, r := range refs {
		if r == explore.SurveyTests || r == explore.SurveyDocs {
			t.Errorf("task %q should have been dropped, refs = %v", r, refs)
		}
	}
	if len(refs) == 0 || refs[0] != explore.SurveyReference {
		t.Errorf("refs = %v", refs)
	}
}

func TestLocateToolListsRankedFiles(t *testing.T) {
	dec := &labelDecider{table: map[string]float64{"": 0.9}}
	s := locateSession(t, dec)
	out, err := tools.Locate{Locator: &locateCatalog{s: s}}.Execute(context.Background(), map[string]any{"query": "login"})
	if err != nil {
		t.Fatal(err)
	}
	if out.Kind != tools.KindGlob || !strings.Contains(out.Content, "ranked by jev-9") || !strings.Contains(out.Content, "internal/auth/login.go:3") {
		t.Errorf("result = %+v", out)
	}
	if strings.Contains(out.Content, "package auth") || strings.Contains(out.Content, "Login()") {
		t.Errorf("file text in the result: %q", out.Content)
	}
	if _, err := (tools.Locate{Locator: &locateCatalog{s: s}}).Execute(context.Background(), map[string]any{}); err == nil {
		t.Error("an empty query must be an error")
	}
}
