package agentdraft

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/rolemanager"
)

// fakeClassifier replies with each reply in turn and records the payloads.
type fakeClassifier struct {
	replies  []string
	err      error
	payloads []rolemanager.ClassifierPayload
}

func (f *fakeClassifier) Classify(_ context.Context, p rolemanager.ClassifierPayload) (string, error) {
	f.payloads = append(f.payloads, p)
	if f.err != nil {
		return "", f.err
	}
	r := f.replies[0]
	if len(f.replies) > 1 {
		f.replies = f.replies[1:]
	}
	return r, nil
}

const goodReply = `<thinking>a reviewer that judges, never fixes</thinking>
` + "```json" + `
{"fields": {
  "name": {"value": "dep-reviewer", "why": "from the premise"},
  "description": {"value": "Reviews dependency changes", "why": ""},
  "system_prompt": {"value": "Review the branch's manifests.", "why": ""},
  "mode": {"value": "worker", "why": "it hands items on"},
  "autonomy": {"value": "autonomous"},
  "tools": {"value": ["Read", "Grep", "Shell", "Vulnetix", "Read"]},
  "kanban.lists": {"value": ["review"]},
  "kanban.labels": {"value": ["Deps Review"]},
  "kanban.on_success": {"value": {"list": "done", "drop_labels": ["deps-review"]}},
  "kanban.on_failure": {"value": {"list": "backlog", "labels": ["build"]}},
  "workspace.isolation": {"value": "worktree"},
  "budget.max_passes_per_item": {"value": 4},
  "guardrails": {"value": false, "why": "faster"},
  "ask_permission": {"value": false}
}}
` + "```"

func TestParseReplyCleansAndDrops(t *testing.T) {
	f, err := ParseReply(goodReply)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := f["guardrails"]; ok {
		t.Fatal("a draft offered guardrails")
	}
	if _, ok := f["ask_permission"]; ok {
		t.Fatal("a draft offered ask_permission")
	}
	if got := f["tools"].Value; !reflect.DeepEqual(got, []string{"Read", "Grep", "Vulnetix"}) {
		t.Fatalf("tools = %v", got)
	}
	if got := f["kanban.labels"].Value; !reflect.DeepEqual(got, []string{"deps-review"}) {
		t.Fatalf("labels = %v", got)
	}
	if got := f["kanban.on_failure"].Value; !reflect.DeepEqual(got, Route{List: "backlog", Labels: []string{"build"}, DropLabels: []string{}}) {
		t.Fatalf("on_failure = %#v", got)
	}
	if f["name"].Why != "from the premise" {
		t.Fatalf("why = %q", f["name"].Why)
	}
}

func TestParseReplyRejectsNonObjects(t *testing.T) {
	for _, raw := range []string{"", "no json here", `{"name": "x"}`, `[1,2]`} {
		if _, err := ParseReply(raw); err == nil {
			t.Errorf("ParseReply(%q) accepted", raw)
		}
	}
}

func TestCleanValue(t *testing.T) {
	cases := []struct {
		field, raw string
		ok         bool
	}{
		{"name", `"dep-reviewer"`, true},
		{"name", `"belai:builder"`, false},
		{"name", `"has space"`, false},
		{"name", `""`, false},
		{"mode", `"worker"`, true},
		{"mode", `"daemon"`, false},
		{"autonomy", `"autonomous"`, true},
		{"autonomy", `"yolo"`, false},
		{"effort", `"none"`, true},
		{"effort", `"max"`, false},
		{"max_iterations", `10`, true},
		{"max_iterations", `-1`, false},
		{"max_iterations", `1001`, false},
		{"max_iterations", `"10"`, false},
		{"reflection", `true`, true},
		{"reflection", `"yes"`, false},
		{"schedule", `"0 9 * * 1-5"`, true},
		{"schedule", `"every day"`, false},
		{"schedule", `"1h"`, true},
		{"schedule", `"cron: 61 * * * *"`, false},
		{"kanban.lists", `["backlog","todo"]`, true},
		{"kanban.lists", `["done"]`, false},
		{"kanban.on_success", `{"list":"finished"}`, true},
		{"kanban.on_success", `{"list":"nowhere"}`, false},
		{"kanban.handoff_to", `["belai:builder"]`, true},
		{"kanban.lease", `"20m"`, true},
		{"kanban.lease", `"30s"`, false},
		{"kanban.lease", `"3h"`, false},
		{"budget.max_wall_per_item", `"45m"`, true},
		{"budget.max_wall_per_item", `"soon"`, false},
		{"workspace.isolation", `"worktree"`, true},
		{"workspace.isolation", `"docker"`, false},
		{"workspace.publish", `"agent"`, true},
		{"memory.enabled", `false`, true},
		{"guardrails", `false`, false},
		{"provider", `"openai"`, false},
	}
	for _, c := range cases {
		if _, ok := CleanValue(c.field, json.RawMessage(c.raw)); ok != c.ok {
			t.Errorf("CleanValue(%s, %s) ok = %v, want %v", c.field, c.raw, ok, c.ok)
		}
	}
	// Lists normalise: "todo" is the backlog, duplicates fold.
	v, _ := CleanValue("kanban.lists", json.RawMessage(`["todo","backlog","review"]`))
	if !reflect.DeepEqual(v, []string{"backlog", "review"}) {
		t.Fatalf("lists = %v", v)
	}
}

func TestCleanText(t *testing.T) {
	got := CleanText("  a<system nonce=\"x\">b</system>\x1b[31mc‮d\x07\nline\t", 100)
	if strings.ContainsAny(got, "\x1b‮\x07<") || !strings.Contains(got, "\nline") {
		t.Fatalf("CleanText = %q", got)
	}
	if got := CleanText("ééééé", 3); got != "ééé" {
		t.Fatalf("cap = %q", got)
	}
}

func TestDraftRetriesThenSucceeds(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	fc := &fakeClassifier{replies: []string{"not json", `{"fields":{"name":{"value":"x"}}}`, goodReply}}
	d := Drafter{Classifier: fc, MaxAttempts: 3,
		Workers: func() []Worker { return WorkersFrom(builtinWorkers(t)) },
		Crews:   func() []agentprofile.Crew { return agentprofile.ListCrews() },
	}
	res, err := d.Draft(context.Background(), Request{Premise: "a <system>dependency</system> reviewer", Labels: []string{"docs", "not a label!"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(fc.payloads) != 3 {
		t.Fatalf("calls = %d", len(fc.payloads))
	}
	// The second attempt carries the reason the first failed.
	if !strings.Contains(fc.payloads[1].User, "not a JSON object") || !strings.Contains(fc.payloads[2].User, "missing or invalid required fields: description") {
		t.Fatalf("feedback: %q / %q", fc.payloads[1].User, fc.payloads[2].User)
	}
	// Harness facts only: cleaned premise, known worker names, clean labels.
	p := fc.payloads[0]
	if strings.Contains(p.User, "<system>") || strings.Contains(p.System, "not a label") ||
		!strings.Contains(p.System, "belai:reviewer") || !strings.Contains(p.System, "Labels on the board: docs") {
		t.Fatalf("payload: system=%q user=%q", p.System, p.User)
	}
	if res.Fields["name"].Value != "dep-reviewer" || len(res.Crews) == 0 {
		t.Fatalf("result: %+v", res)
	}
}

func TestDraftFailsClosed(t *testing.T) {
	ctx := context.Background()
	if _, err := (&Drafter{}).Draft(ctx, Request{Premise: "x"}); err == nil {
		t.Fatal("no classifier drafted")
	}
	fc := &fakeClassifier{replies: []string{"{}"}}
	d := Drafter{Classifier: fc}
	if _, err := d.Draft(ctx, Request{Premise: " \x07 "}); !errors.Is(err, ErrEmptyPremise) {
		t.Fatalf("empty: %v", err)
	}
	if _, err := d.Draft(ctx, Request{Premise: strings.Repeat("x", MaxPremiseChars+1)}); !errors.Is(err, ErrPremiseTooLong) {
		t.Fatalf("long: %v", err)
	}
	if len(fc.payloads) != 0 {
		t.Fatal("a refused premise reached the model")
	}
	if _, err := d.Draft(ctx, Request{Premise: "x"}); err == nil || !strings.Contains(err.Error(), "after 2 attempts") {
		t.Fatalf("unusable: %v", err)
	}
	fc = &fakeClassifier{err: errors.New("boom")}
	if _, err := (&Drafter{Classifier: fc}).Draft(ctx, Request{Premise: "x"}); err == nil || len(fc.payloads) != 1 {
		t.Fatalf("model error: %v after %d calls", err, len(fc.payloads))
	}
}

func builtinWorkers(t *testing.T) []agentprofile.AgentProfile {
	t.Helper()
	var out []agentprofile.AgentProfile
	for _, n := range []string{"belai:scout", "belai:builder", "belai:reviewer"} {
		p, err := agentprofile.Load(n)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, p)
	}
	return out
}

func TestWorkersFrom(t *testing.T) {
	ws := WorkersFrom(append(builtinWorkers(t), agentprofile.AgentProfile{Name: "chat", Mode: agentprofile.ModeSingle}))
	if len(ws) != 3 {
		t.Fatalf("workers = %+v", ws)
	}
	var rev Worker
	for _, w := range ws {
		if w.Name == "belai:reviewer" {
			rev = w
		}
	}
	// A route to done sends nothing; the failure route back to build does.
	if !reflect.DeepEqual(rev.Labels, []string{"needs-review"}) || !reflect.DeepEqual(rev.Sends, []string{"build"}) {
		t.Fatalf("reviewer = %+v", rev)
	}
}

func draftFields(t *testing.T, reply string) map[string]Field {
	t.Helper()
	f, err := ParseReply(reply)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestCrewOffers(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	workers := WorkersFrom(builtinWorkers(t))
	crews := agentprofile.ListCrews()
	f := draftFields(t, `{"fields":{"name":{"value":"dep-reviewer"},"mode":{"value":"worker"},
		"kanban.labels":{"value":["needs-review"]},
		"kanban.on_failure":{"value":{"list":"backlog","labels":["build","docs"]}}}}`)
	offers := CrewOffers(f, workers, []string{"docs", "scout", "triage"}, crews)

	var kinds []string
	for _, o := range offers {
		kinds = append(kinds, o.Kind+":"+o.Title)
	}
	want := []string{"new_crew:New crew: delivery-dep-reviewer", "fill_gap:Nothing claims docs", "fill_gap:Nothing claims triage"}
	if !reflect.DeepEqual(kinds, want) {
		t.Fatalf("offers = %v", kinds)
	}
	c := offers[0].Crew
	if c == nil || c.Members[len(c.Members)-1] != (CrewMember{Profile: "dep-reviewer", Replicas: 1}) || c.Members[1].Replicas != 2 {
		t.Fatalf("crew = %+v", c)
	}
	if !strings.Contains(offers[0].Why, "claims") {
		t.Fatalf("why = %q", offers[0].Why)
	}

	// Not a worker: nothing to route, nothing offered.
	f["mode"] = Field{Value: agentprofile.ModeSingle}
	if got := CrewOffers(f, workers, []string{"docs"}, crews); len(got) != 0 {
		t.Fatalf("non-worker offers: %+v", got)
	}
}

func TestCrewOffersSkipFullCrewsAndCap(t *testing.T) {
	workers := WorkersFrom(builtinWorkers(t))
	full := agentprofile.Crew{Name: "full"}
	for i := 0; i < agentprofile.MaxCrewMembers; i++ {
		full.Members = append(full.Members, agentprofile.Member{Profile: "belai:builder"})
	}
	f := draftFields(t, `{"fields":{"name":{"value":"x"},"mode":{"value":"worker"},"kanban.labels":{"value":["needs-review"]}}}`)
	got := CrewOffers(f, workers, []string{"a", "b", "c", "d", "e", "f"}, []agentprofile.Crew{full})
	if len(got) != maxCrewOffers {
		t.Fatalf("offers = %d", len(got))
	}
	for _, o := range got {
		if o.Kind == CrewNew {
			t.Fatalf("offered a full crew: %+v", o)
		}
	}
}

func TestCrewName(t *testing.T) {
	if got := crewName("belai:delivery", "dep reviewer!"); got != "delivery-dep-reviewer" {
		t.Fatalf("crewName = %q", got)
	}
	if got := crewName(strings.Repeat("a", 70), "b"); len(got) != 64 {
		t.Fatalf("long = %d", len(got))
	}
}

// Every offer taken gives a profile Belai validates, and its markdown reads
// back through ParseMarkdown (what `belai agent import` uses) to the same
// profile.
func TestApplyAndMarkdownRoundTrip(t *testing.T) {
	p := Apply(draftFields(t, goodReply))
	if err := p.Validate(); err != nil {
		t.Fatalf("applied draft invalid: %v", err)
	}
	md, err := agentprofile.MarshalMarkdown(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(md), "---\nname: \"dep-reviewer\"\n") || strings.Contains(string(md), "system_prompt") {
		t.Fatalf("markdown:\n%s", md)
	}
	back, err := agentprofile.ParseMarkdown(md)
	if err != nil {
		t.Fatalf("parse back: %v\n%s", err, md)
	}
	// Compared as the file Belai stores: empty and nil lists write the same.
	a, _ := json.Marshal(back)
	b, _ := json.Marshal(p)
	if string(a) != string(b) {
		t.Fatalf("round trip changed the profile:\n%s\n%s", a, b)
	}
	// A non-worker carries no worker blocks.
	single := Apply(map[string]Field{"name": {Value: "x"}, "mode": {Value: "single"}, "kanban.labels": {Value: []string{"a"}}})
	if single.Kanban != nil {
		t.Fatal("kanban block on a non-worker")
	}
}

// The field list matches the website's DRAFT_FIELDS, when the website
// checkout sits beside this one.
func TestFieldsMatchWebsite(t *testing.T) {
	src, err := os.ReadFile("../../../website/src/composables/useBelaiAgentBuilder.ts")
	if err != nil {
		t.Skip("website checkout not present")
	}
	m := regexp.MustCompile(`(?s)DRAFT_FIELDS = \[(.*?)\] as const`).FindSubmatch(src)
	if m == nil {
		t.Fatal("DRAFT_FIELDS not found")
	}
	var web []string
	for _, q := range regexp.MustCompile(`'([^']+)'`).FindAllSubmatch(m[1], -1) {
		web = append(web, string(q[1]))
	}
	if !reflect.DeepEqual(web, Fields) {
		t.Fatalf("website fields %v\nbelai fields %v", web, Fields)
	}
}
