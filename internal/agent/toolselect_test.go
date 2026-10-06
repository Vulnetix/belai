package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/decisions"
	"github.com/vulnetix/belai/internal/modes"
	"github.com/vulnetix/belai/internal/permissions"
	"github.com/vulnetix/belai/internal/posture"
	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/rolemanager/jev"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/tools"
)

// keyDecider rates each item by its key ("tool:GH", "skill:git-ops").
type keyDecider struct {
	mu     sync.Mutex
	scores map[string]float64
	err    error
	calls  int
	seen   []string
}

func (d *keyDecider) Decide(_ context.Context, r decisions.Request) (decisions.Result, error) {
	d.mu.Lock()
	d.calls++
	d.mu.Unlock()
	if d.err != nil {
		return decisions.Result{}, d.err
	}
	out := map[string]decisions.Answer{}
	for q := range r.Questions {
		key := strings.TrimPrefix(q, "s:")
		d.mu.Lock()
		d.seen = append(d.seen, key)
		d.mu.Unlock()
		if sc, ok := d.scores[key]; ok {
			out[q] = decisions.Answer{Type: decisions.TypeNoul, Noul: sc}
		}
	}
	return decisions.Result{Answers: out}, nil
}
func (d *keyDecider) Identity() string           { return "fake/systemone" }
func (d *keyDecider) Backend() decisions.Backend { return decisions.BackendSystemOne }

func cand(name string, skill bool) tools.Candidate {
	return tools.Candidate{Name: name, Description: "does " + strings.ToLower(name), Skill: skill}
}

func names(cs []tools.Candidate) string {
	var out []string
	for _, c := range cs {
		out = append(out, c.Name)
	}
	return strings.Join(out, ",")
}

func TestMergeSearchDropsPoorAndAddsStrongAndOrdersByScore(t *testing.T) {
	det := []tools.Candidate{cand("A", false), cand("B", false), cand("C", false), cand("D", false)}
	pool := append(append([]tools.Candidate(nil), det...), cand("E", false), cand("F", true), cand("G", false))
	scores := map[string]float64{
		"tool:A": 0.05, // below DropAt: dropped
		"tool:B": 0.30, // kept
		"tool:C": 0.90, // kept, ranks first
		// D unrated: kept as a middling entry
		"tool:E":  0.85, // strong: added
		"skill:F": 0.80, // strong: added (exactly StrongAt)
		"tool:G":  0.79, // not strong enough
	}
	got := names(mergeSearch(det, pool, scores, 10))
	if got != "C,E,F,D,B" {
		t.Fatalf("merged = %s, want C,E,F,D,B", got)
	}
	if got := names(mergeSearch(det, pool, scores, 2)); got != "C,E" {
		t.Fatalf("limited = %s", got)
	}
	// Exactly DropAt survives; everything dropped leaves nothing.
	if got := names(mergeSearch(det[:1], pool, map[string]float64{"tool:A": 0.10}, 5)); got != "A" {
		t.Fatalf("a score of exactly DropAt was dropped: %s", got)
	}
	if got := names(mergeSearch(det[:1], nil, map[string]float64{"tool:A": 0.01}, 5)); got != "" {
		t.Fatalf("a rejected entry survived: %s", got)
	}
}

func TestMergeSearchTiesKeepTheDeterministicOrder(t *testing.T) {
	det := []tools.Candidate{cand("X", false), cand("Y", false), cand("Z", false)}
	scores := map[string]float64{"tool:X": 0.6, "tool:Y": 0.6, "tool:Z": 0.6}
	if got := names(mergeSearch(det, det, scores, 5)); got != "X,Y,Z" {
		t.Fatalf("got %s", got)
	}
}

func TestSearchPoolStartsWithTheDeterministicListAndRespectsTheCap(t *testing.T) {
	var pool []tools.Candidate
	for _, n := range []string{"Aa", "Bb", "Cc", "Dd", "Ee", "Ff"} {
		pool = append(pool, tools.Candidate{Name: n, Description: "misc"})
	}
	pool = append(pool, tools.Candidate{Name: "Grepper", Description: "search text with patterns"})
	det := []tools.Candidate{pool[3]}
	got := searchPool("search patterns", det, pool, 4)
	if len(got) != 4 || got[0].Name != "Dd" || got[1].Name != "Grepper" {
		t.Fatalf("pool = %s", names(got))
	}
	seen := map[string]bool{}
	for _, c := range got {
		if seen[c.Key()] {
			t.Fatalf("duplicate %s", c.Name)
		}
		seen[c.Key()] = true
	}
	if len(searchPool("x", nil, pool, 100)) != len(pool) {
		t.Fatal("a large cap should include every candidate")
	}
}

func TestChooseSurface(t *testing.T) {
	rated := []tools.Candidate{
		cand("T1", false), cand("T2", false), cand("T3", false), cand("T4", false), cand("T5", false),
		cand("T6", false), cand("T7", false), cand("T8", false),
		cand("s1", true), cand("s2", true), cand("s3", true), cand("s4", true), cand("s5", true), cand("s6", true), cand("named-skill", true),
	}
	scores := map[string]float64{}
	for i, n := range []string{"T1", "T2", "T3", "T4", "T5", "T6", "T7"} {
		scores["tool:"+n] = 0.99 - float64(i)*0.01
	}
	scores["tool:T8"] = 0.49 // below KeepAt
	for i, n := range []string{"s1", "s2", "s3", "s4", "s5", "s6"} {
		scores["skill:"+n] = 0.95 - float64(i)*0.01
	}
	preload, kept := chooseSurface("please use the named-skill for this", rated, scores)
	if strings.Join(preload, ",") != "T1,T2,T3,T4,T5,T6" {
		t.Fatalf("preload = %v (at most %d, best first, none below KeepAt)", preload, maxPreloadTools)
	}
	var skills []string
	for _, c := range rated {
		if c.Skill && kept[c.Key()] {
			skills = append(skills, c.Name)
		}
	}
	if strings.Join(skills, ",") != "s1,s2,s3,s4,s5,named-skill" {
		t.Fatalf("skills = %v (top %d plus the skill the request names)", skills, maxListedSkills)
	}
}

// selection wires a session with deferred tools and installed skills.
func selectionSession(t *testing.T, d decisions.Decider, on func(config.JevJob) bool) (*Session, []tools.Candidate) {
	t.Helper()
	root := t.TempDir()
	calls := 0
	reg := tools.NewRegistry(&tools.Read{Root: root, MaxBytes: 1024}, frob{calls: &calls}, tools.Skill{})
	sess, err := NewSession(Options{
		Cfg:      run.Config{Provider: "openai", BaseURL: "http://127.0.0.1:1", APIKey: "k", Model: "m"},
		Registry: reg, Posture: posture.Defaults(), Workdir: root, SkipNonceSeed: true,
		Jev: &jev.Jobs{Client: jev.NewWith(d), On: on},
	})
	if err != nil {
		t.Fatal(err)
	}
	skills := []tools.Candidate{
		{Name: "git-ops", Description: "Rebase and open pull requests", Skill: true},
		{Name: "pdf-tools", Description: "Fill PDF forms", Skill: true},
		{Name: "data-viz", Description: "Draw charts", Skill: true},
	}
	sess.skillCands = skills
	return sess, skills
}

func TestSelectSurfacePreloadsToolsAndListsOnlyRelevantSkills(t *testing.T) {
	d := &keyDecider{scores: map[string]float64{"tool:Frob": 0.9, "skill:git-ops": 0.8, "skill:pdf-tools": 0.1, "skill:data-viz": 0.2}}
	sess, skills := selectionSession(t, d, nil)
	listed, more := sess.selectSurface(context.Background(), "frobnicate the widget and open a pull request", string(modes.ModeAgent), skills)
	if names(listed) != "git-ops" || more != 2 {
		t.Fatalf("listed %s, more %d", names(listed), more)
	}
	if got := sess.deferral.loadedNames(); len(got) != 1 || got[0] != "Frob" {
		t.Fatalf("loaded = %v; the tool the request needs should be loaded before the model asks", got)
	}
}

func TestSelectSurfaceListsEverySkillWhenItCannotDecide(t *testing.T) {
	cases := map[string]func() (*Session, []tools.Candidate){
		"backend error": func() (*Session, []tools.Candidate) {
			return selectionSession(t, &keyDecider{err: &decisions.Error{Class: decisions.ClassUnavailable, Status: 503}}, nil)
		},
		"nothing answered": func() (*Session, []tools.Candidate) { return selectionSession(t, &keyDecider{}, nil) },
		"job off": func() (*Session, []tools.Candidate) {
			return selectionSession(t, &keyDecider{scores: map[string]float64{"skill:git-ops": 0.9}},
				func(j config.JevJob) bool { return j != config.JevToolSelection })
		},
	}
	for name, mk := range cases {
		sess, skills := mk()
		listed, more := sess.selectSurface(context.Background(), "do a thing", string(modes.ModeAgent), skills)
		if len(listed) != 3 || more != 0 {
			t.Errorf("%s: listed %d more %d, want every skill", name, len(listed), more)
		}
		if len(sess.deferral.loadedNames()) != 0 {
			t.Errorf("%s: a tool was loaded", name)
		}
	}
	sess, skills := selectionSession(t, &keyDecider{scores: map[string]float64{"skill:git-ops": 0.9}}, nil)
	if listed, more := sess.selectSurface(context.Background(), "   ", string(modes.ModeAgent), skills); len(listed) != 3 || more != 0 {
		t.Errorf("empty request: listed %d more %d", len(listed), more)
	}
}

func TestSelectSurfaceLeavesSkillsAloneWhenToolSearchIsNotThere(t *testing.T) {
	d := &keyDecider{scores: map[string]float64{"skill:git-ops": 0.9}}
	root := t.TempDir()
	f := false
	sess, err := NewSession(Options{
		Cfg:      run.Config{Provider: "openai", BaseURL: "http://127.0.0.1:1", APIKey: "k", Model: "m"},
		Registry: tools.NewRegistry(&tools.Read{Root: root, MaxBytes: 1024}, tools.Skill{}), Posture: posture.Defaults(),
		Workdir: root, SkipNonceSeed: true, Settings: config.Settings{DeferTools: &f},
		Jev: &jev.Jobs{Client: jev.NewWith(d)},
	})
	if err != nil {
		t.Fatal(err)
	}
	skills := []tools.Candidate{cand("git-ops", true), cand("other", true)}
	if listed, more := sess.selectSurface(context.Background(), "rebase", "agent", skills); len(listed) != 2 || more != 0 || d.calls != 0 {
		t.Fatalf("deferral off: listed %d more %d calls %d; an unlisted skill would be unreachable", len(listed), more, d.calls)
	}
}

// scribe is a non-core tool that writes, so plan mode does not offer it.
type scribe struct{}

func (scribe) Definition() tools.Definition {
	return tools.Definition{Name: "Scribe", Description: "Write notes to a file."}
}
func (scribe) Kind() tools.Kind              { return tools.KindWrite }
func (scribe) Subject(map[string]any) string { return "" }
func (scribe) Execute(context.Context, map[string]any) (tools.Result, error) {
	return tools.Result{Kind: tools.KindWrite, Content: "written"}, nil
}

func TestSelectSurfaceOnlyLoadsToolsOnTheCurrentSurface(t *testing.T) {
	build := func(planMode bool) *Session {
		root := t.TempDir()
		calls := 0
		sess, err := NewSession(Options{
			Cfg:      run.Config{Provider: "openai", BaseURL: "http://127.0.0.1:1", APIKey: "k", Model: "m"},
			Registry: tools.NewRegistry(&tools.Read{Root: root, MaxBytes: 1024}, frob{calls: &calls}, scribe{}, tools.Skill{}),
			Posture:  posture.Defaults(), Workdir: root, SkipNonceSeed: true, PlanMode: planMode,
			PlanSurface: tools.PlanSurface{Perms: permissions.Settings{}},
			Jev:         &jev.Jobs{Client: jev.NewWith(&keyDecider{scores: map[string]float64{"tool:Scribe": 0.99, "tool:Frob": 0.99}})},
		})
		if err != nil {
			t.Fatal(err)
		}
		return sess
	}
	agentSess := build(false)
	agentSess.selectSurface(context.Background(), "write notes", string(modes.ModeAgent), nil)
	if got := strings.Join(agentSess.deferral.loadedNames(), ","); got != "Frob,Scribe" && got != "Scribe,Frob" {
		t.Fatalf("agent mode loaded %q, want both", got)
	}
	planSess := build(true)
	planSess.selectSurface(context.Background(), "write notes", string(modes.ModePlan), nil)
	for _, n := range planSess.deferral.loadedNames() {
		if n == "Scribe" {
			t.Fatal("plan mode loaded a writer")
		}
	}
	if got := planSess.deferral.loadedNames(); len(got) != 1 || got[0] != "Frob" {
		t.Fatalf("plan mode loaded %v, want only the read-only tool", got)
	}
}

func TestRankRefinesASearch(t *testing.T) {
	d := &keyDecider{scores: map[string]float64{"tool:A": 0.02, "tool:B": 0.7, "skill:S": 0.9}}
	sess, _ := selectionSession(t, d, nil)
	det := []tools.Candidate{cand("A", false), cand("B", false)}
	pool := append(append([]tools.Candidate(nil), det...), cand("S", true))
	got, ok := sess.Rank(context.Background(), "some query", det, pool, 5)
	if !ok || names(got) != "S,B" {
		t.Fatalf("ranked = %s ok %v", names(got), ok)
	}
	off, _ := selectionSession(t, d, func(j config.JevJob) bool { return j != config.JevToolSearch })
	if _, ok := off.Rank(context.Background(), "q", det, pool, 5); ok {
		t.Fatal("a switched-off job ranked")
	}
	bad, _ := selectionSession(t, &keyDecider{err: &decisions.Error{Class: decisions.ClassUnavailable}}, nil)
	if _, ok := bad.Rank(context.Background(), "q", det, pool, 5); ok {
		t.Fatal("an unavailable backend ranked")
	}
}

func TestRecordsCountsOnly(t *testing.T) {
	var got []rolemanager.Activity
	cancel := rolemanager.AddSink(func(a rolemanager.Activity) {
		if a.Event == rolemanager.EventToolSelect || a.Event == rolemanager.EventToolSearch {
			got = append(got, a)
		}
	})
	defer cancel()
	d := &keyDecider{scores: map[string]float64{"tool:Frob": 0.9, "skill:git-ops": 0.8, "tool:A": 0.9}}
	sess, skills := selectionSession(t, d, nil)
	sess.selectSurface(context.Background(), "secret request text", "agent", skills)
	det := []tools.Candidate{cand("A", false)}
	sess.Rank(context.Background(), "secret query", det, det, 3)
	if len(got) != 2 {
		t.Fatalf("recorded %d events", len(got))
	}
	for _, a := range got {
		if strings.Contains(a.Detail, "secret") || strings.Contains(a.Subject, "secret") {
			t.Fatalf("the request or query was recorded: %+v", a)
		}
	}
}

// writeSkill installs a skill in the state directory.
func writeSkill(t *testing.T, home, name, desc string) {
	t.Helper()
	dir := filepath.Join(home, "skills", name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: " + name + "\ndescription: " + desc + "\n---\nUse this skill when " + desc + ".\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// End to end: with a backend, the system block lists the relevant skill, says
// how many were left out, and ToolSearch stays advertised to find them.
func TestSystemBlockListsOnlyTheRelevantSkills(t *testing.T) {
	home := t.TempDir()
	t.Setenv("BELAI_HOME", home)
	writeSkill(t, home, "git-ops", "Rebase and open pull requests")
	writeSkill(t, home, "pdf-tools", "Fill PDF forms")
	writeSkill(t, home, "data-viz", "Draw charts")

	var mu sync.Mutex
	var system string
	var advertised []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var req struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
		}
		_ = json.Unmarshal(raw, &req)
		sys := ""
		for _, m := range req.Messages {
			if m.Role == "system" {
				sys = m.Content
			}
		}
		if strings.Contains(sys, "security classifier") {
			writeChatJSON(w, "SAFE")
			return
		}
		mu.Lock()
		system = sys
		advertised = nil
		for _, tl := range req.Tools {
			advertised = append(advertised, tl.Function.Name)
		}
		mu.Unlock()
		writeChatJSON(w, "done")
	}))
	defer srv.Close()

	d := &keyDecider{scores: map[string]float64{"skill:git-ops": 0.9, "skill:pdf-tools": 0.1, "skill:data-viz": 0.1, "tool:Frob": 0.9}}
	root := t.TempDir()
	calls := 0
	sess, err := NewSession(Options{
		Cfg: run.Config{Provider: "openai", BaseURL: srv.URL, APIKey: "k", Model: "m"}, Client: srv.Client(),
		Registry: tools.NewRegistry(&tools.Read{Root: root, MaxBytes: 1024}, frob{calls: &calls}, tools.Skill{}),
		Posture:  posture.Defaults(), Workdir: root, SkipNonceSeed: true,
		Jev: &jev.Jobs{Client: jev.NewWith(d)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sess.RunInput(context.Background(), TurnInput{Prompt: "rebase my branch and open a pull request", ForceMode: modes.ModeAgent}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(system, "git-ops: Rebase and open pull requests") {
		t.Fatalf("the relevant skill is not listed:\n%s", system)
	}
	if strings.Contains(system, "pdf-tools") || strings.Contains(system, "data-viz") {
		t.Fatalf("an irrelevant skill was listed:\n%s", system)
	}
	if !strings.Contains(system, "2 more skills are installed but not listed") {
		t.Fatalf("the system block does not say skills were left out:\n%s", system)
	}
	hasSearch, hasFrob := false, false
	for _, n := range advertised {
		hasSearch = hasSearch || n == "ToolSearch"
		hasFrob = hasFrob || n == "Frob"
	}
	if !hasSearch || !hasFrob {
		t.Fatalf("advertised %v: ToolSearch must stay so skills can be found, and the likely tool is preloaded", advertised)
	}
}

// Without a backend nothing changes: every skill is listed and no extra line.
func TestWithoutABackendEverySkillIsListed(t *testing.T) {
	home := t.TempDir()
	t.Setenv("BELAI_HOME", home)
	writeSkill(t, home, "git-ops", "Rebase and open pull requests")
	writeSkill(t, home, "pdf-tools", "Fill PDF forms")
	var mu sync.Mutex
	var system string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var req struct {
			Messages []struct{ Role, Content string } `json:"messages"`
		}
		_ = json.Unmarshal(raw, &req)
		sys := ""
		for _, m := range req.Messages {
			if m.Role == "system" {
				sys = m.Content
			}
		}
		if strings.Contains(sys, "security classifier") {
			writeChatJSON(w, "SAFE")
			return
		}
		mu.Lock()
		system = sys
		mu.Unlock()
		writeChatJSON(w, "done")
	}))
	defer srv.Close()
	root := t.TempDir()
	sess, err := NewSession(Options{
		Cfg: run.Config{Provider: "openai", BaseURL: srv.URL, APIKey: "k", Model: "m"}, Client: srv.Client(),
		Registry: tools.NewRegistry(&tools.Read{Root: root, MaxBytes: 1024}, tools.Skill{}),
		Posture:  posture.Defaults(), Workdir: root, SkipNonceSeed: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sess.RunInput(context.Background(), TurnInput{Prompt: "hello there", ForceMode: modes.ModeAgent}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(system, "git-ops") || !strings.Contains(system, "pdf-tools") || strings.Contains(system, "more skills are installed") {
		t.Fatalf("system block:\n%s", system)
	}
}

// A session whose registry carries a profile's skills list shows that skill and
// no other; an ordinary session shows no builtin skill at all.
func TestSystemBlockListsOnlyTheProfilesSkills(t *testing.T) {
	home := t.TempDir()
	t.Setenv("BELAI_HOME", home)
	writeSkill(t, home, "git-ops", "Rebase and open pull requests")
	systemFor := func(reg *tools.Registry) string {
		var mu sync.Mutex
		var system string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw, _ := io.ReadAll(r.Body)
			var req struct {
				Messages []struct{ Role, Content string } `json:"messages"`
			}
			_ = json.Unmarshal(raw, &req)
			sys := ""
			for _, m := range req.Messages {
				if m.Role == "system" {
					sys = m.Content
				}
			}
			if strings.Contains(sys, "security classifier") {
				writeChatJSON(w, "SAFE")
				return
			}
			mu.Lock()
			system = sys
			mu.Unlock()
			writeChatJSON(w, "done")
		}))
		defer srv.Close()
		root := t.TempDir()
		sess, err := NewSession(Options{
			Cfg: run.Config{Provider: "openai", BaseURL: srv.URL, APIKey: "k", Model: "m"}, Client: srv.Client(),
			Registry: reg, Posture: posture.Defaults(), Workdir: root, SkipNonceSeed: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := sess.RunInput(context.Background(), TurnInput{Prompt: "hello there", ForceMode: modes.ModeAgent}); err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		defer mu.Unlock()
		return system
	}
	root := t.TempDir()
	base := tools.NewRegistry(&tools.Read{Root: root, MaxBytes: 1024}, tools.Skill{})

	plain := systemFor(base)
	if !strings.Contains(plain, "git-ops") || strings.Contains(plain, "belai-scout") {
		t.Fatalf("an ordinary session:\n%s", plain)
	}
	scoped := systemFor(base.WithSkills([]string{"belai-scout"}))
	if !strings.Contains(scoped, "belai-scout: ") || strings.Contains(scoped, "git-ops") || strings.Contains(scoped, "belai-patcher") {
		t.Fatalf("a session scoped to belai-scout:\n%s", scoped)
	}
}
