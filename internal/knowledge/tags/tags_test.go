package tags

import (
	"context"
	"strings"
	"testing"
)

func TestVocabularyIsWellFormed(t *testing.T) {
	defs := Vocabulary()
	if len(defs) < 250 {
		t.Fatalf("vocabulary has %d topics, want at least 250", len(defs))
	}
	seen := map[string]bool{}
	for _, d := range defs {
		if seen[d.ID] {
			t.Fatalf("duplicate id %q", d.ID)
		}
		seen[d.ID] = true
		for _, r := range d.ID {
			if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
				t.Fatalf("id %q has a character outside [a-z0-9-]", d.ID)
			}
		}
		if d.Label == "" || len(d.Label) > 40 {
			t.Fatalf("topic %q label %q is empty or over 40 bytes", d.ID, d.Label)
		}
		if d.Prio < 1 || d.Prio > 3 {
			t.Fatalf("topic %q priority %d", d.ID, d.Prio)
		}
		i := byID[d.ID]
		if len(topics[i].keys) == 0 && len(topics[i].res) == 0 && len(topics[i].paths) == 0 {
			t.Fatalf("topic %q has no patterns", d.ID)
		}
	}
	if got := len(Domains()); got < 12 {
		t.Fatalf("only %d domains", got)
	}
}

func TestDetectLabelsAGoTestFile(t *testing.T) {
	r := Detect(Input{
		Rel: "internal/auth/login_test.go", Size: 2048,
		Chunks: []string{"package auth\n\nfunc TestLoginRejectsBadPassword(t *testing.T) {\n\t// TODO check mfa and totp\n}\n"},
	})
	if r.Kind != "test" || r.Lang != "Go" {
		t.Fatalf("kind %q lang %q", r.Kind, r.Lang)
	}
	for _, want := range []string{"type:test", "lang:go", "ext:go", "dir:internal", "size:small", "has:tests", "has:todo"} {
		if !r.Has(want) {
			t.Errorf("missing label %s in %v", want, r.Labels)
		}
	}
	if r.Ver != Version || r.Src != SrcRegex {
		t.Fatalf("ver %d src %v", r.Ver, r.Src)
	}
}

func TestLabelKinds(t *testing.T) {
	cases := []struct {
		in         Input
		kind, want string
	}{
		{Input{Rel: "README.md", Chunks: []string{"# Title\n\ntext"}}, "doc", "kind:readme"},
		{Input{Rel: "go.mod", Chunks: []string{"module x\n"}}, "dependency", "eco:golang"},
		{Input{Rel: "package-lock.json", Chunks: []string{"{}"}}, "dependency", "kind:lockfile"},
		{Input{Rel: ".github/workflows/ci.yml", Chunks: []string{"on: push"}}, "ci", "type:ci"},
		{Input{Rel: "Dockerfile", Chunks: []string{"FROM scratch"}}, "iac", "type:iac"},
		{Input{Rel: "docs/adr/0001-use-go.md", Chunks: []string{"# 1. Use Go"}}, "doc", "kind:adr"},
		{Input{Rel: ".vulnetix/x.sarif", Artifact: "sarif", Tool: "semgrep", Records: true, Chunks: []string{"r"}}, "scanner", "tool:semgrep"},
		{Input{Rel: "scripts/run.sh", Chunks: []string{"#!/bin/sh"}}, "script", "lang:shell"},
		{Input{Rel: "data/rows.csv", Chunks: []string{"a,b"}}, "data", "type:data"},
	}
	for _, c := range cases {
		r := Detect(c.in)
		if r.Kind != c.kind || !r.Has(c.want) {
			t.Errorf("%s: kind %q (want %q), labels %v, want %s", c.in.Rel, r.Kind, c.kind, r.Labels, c.want)
		}
	}
}

func TestLabelsAreCleanAndBounded(t *testing.T) {
	r := Detect(Input{Rel: "Weird Dir/ünï.cöde.go", Size: 5 << 20, Chunks: []string{"# <script>alert(1)</script> Heading Words\n"}})
	if len(r.Labels) > maxLabels {
		t.Fatalf("%d labels", len(r.Labels))
	}
	for _, l := range r.Labels {
		k, v, ok := strings.Cut(l, ":")
		if !ok || k == "" || v == "" || len(l) > 60 {
			t.Fatalf("bad label %q", l)
		}
		for _, c := range l {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || strings.ContainsRune(":._+-", c)) {
				t.Fatalf("label %q has %q", l, c)
			}
		}
	}
}

func TestMatchFindsTopicsFromWordsAndRegexes(t *testing.T) {
	text := `# Handling a vulnerability
CVE-2024-12345 affects the transitive dependency. The SBOM lists the component with its purl,
and the VEX statement records not affected with a justification. Triage the advisory and patch.`
	got := ids(Match(Input{Rel: "docs/vuln.md", Chunks: []string{text}}))
	for _, want := range []string{"vuln-mgmt", "sbom", "vex"} {
		if !got[want] {
			t.Errorf("topic %s not found in %v", want, got)
		}
	}
	if got["kubernetes"] || got["lang-rust"] {
		t.Errorf("unrelated topics found: %v", got)
	}
}

func TestMatchSplitsIdentifiers(t *testing.T) {
	got := ids(Match(Input{Rel: "x.go", Chunks: []string{"func verifyJWTSignature(accessToken string) { oauthClient.RefreshToken(); bearerToken := jwks.Claims() }"}}))
	if !got["jwt"] || !got["oauth"] {
		t.Fatalf("camelCase identifiers were not split: %v", got)
	}
}

func TestMatchUsesPathHints(t *testing.T) {
	got := ids(Match(Input{Rel: ".github/workflows/release.yml", Chunks: []string{"on: push\njobs:\n  build:\n    steps:\n      - uses: actions/checkout@v4\n"}}))
	if !got["ci-cd"] {
		t.Fatalf("ci-cd not found: %v", got)
	}
}

func TestMatchBoundsItsWork(t *testing.T) {
	big := strings.Repeat("kubernetes pod deployment helm namespace ", 200000)
	r := Match(Input{Rel: "x.md", Chunks: []string{big}})
	if len(r) == 0 || len(r) > MaxTopics {
		t.Fatalf("%d topics", len(r))
	}
}

func TestMatchEmptyDocument(t *testing.T) {
	if got := Match(Input{Rel: "empty.txt"}); len(got) != 0 {
		t.Fatalf("topics from nothing: %v", got)
	}
}

func TestScoresAreConfidences(t *testing.T) {
	for _, tp := range Match(Input{Rel: "k.yaml", Chunks: []string{"apiVersion: apps/v1\nkind: Deployment\nkubernetes kubectl helm pod namespace ingress"}}) {
		if tp.Score < 0.5 || tp.Score >= 1 || tp.Src != SrcRegex {
			t.Fatalf("topic %+v", tp)
		}
	}
}

func TestCandidatesPutFoundTopicsFirstThenSpreadDomains(t *testing.T) {
	found := []Topic{{ID: "jwt", Score: 0.9}, {ID: "sbom", Score: 0.6}, {ID: "no-such-topic", Score: 1}}
	got := Candidates(found, 24)
	if len(got) != 24 || got[0] != "jwt" || got[1] != "sbom" {
		t.Fatalf("candidates %v", got)
	}
	doms := map[string]bool{}
	seen := map[string]bool{}
	for _, id := range got {
		if seen[id] {
			t.Fatalf("duplicate %s", id)
		}
		seen[id] = true
		d, ok := Lookup(id)
		if !ok {
			t.Fatalf("unknown id %s", id)
		}
		doms[d.Domain] = true
	}
	if len(doms) < 10 {
		t.Fatalf("only %d domains in 24 candidates: %v", len(doms), got)
	}
	if n := len(Candidates(nil, 128)); n != 128 {
		t.Fatalf("128 candidates, got %d", n)
	}
	if n := len(Candidates(nil, 10000)); n != len(Vocabulary()) {
		t.Fatalf("a huge limit returns the whole vocabulary, got %d", n)
	}
	if Candidates(nil, 0) != nil {
		t.Fatal("limit 0")
	}
}

func TestSample(t *testing.T) {
	mk := func(n int) []string {
		var c []string
		for i := 0; i < n; i++ {
			c = append(c, strings.Repeat(string(rune('a'+i%26)), 100))
		}
		return c
	}
	if got := Sample(mk(3), 12, 10000); len(got) != 3 {
		t.Fatalf("all chunks fit: %d", len(got))
	}
	got := Sample(mk(100), 5, 10000)
	if len(got) != 5 || got[0] != mk(100)[0] || got[4] != mk(100)[99] {
		t.Fatalf("sample must keep the first and last chunk in order: %d", len(got))
	}
	got = Sample(mk(100), 12, 350)
	total := 0
	for _, g := range got {
		total += len(g)
	}
	if total > 350 || len(got) == 0 {
		t.Fatalf("sample of %d chunks is %d bytes over a 350 byte budget", len(got), total)
	}
	one := Sample([]string{strings.Repeat("é", 500)}, 12, 101)
	if len(one) != 1 || len(one[0]) > 101 || !strings.HasSuffix(one[0], "é") {
		t.Fatalf("a single big chunk is cut to the budget on a rune boundary: %d bytes", len(one[0]))
	}
	if Sample(nil, 12, 100) != nil || Sample(mk(2), 12, 0) != nil {
		t.Fatal("nothing to sample")
	}
}

func TestMergeLetsTheBackendDecideWhatItAnswered(t *testing.T) {
	found := []Topic{{ID: "jwt", Score: 0.7, Src: SrcRegex}, {ID: "sbom", Score: 0.6, Src: SrcRegex}, {ID: "tls", Score: 0.55, Src: SrcRegex}}
	asked := map[string]bool{"jwt": true, "sbom": true, "oauth": true, "crypto": true}
	scores := map[string]float64{"jwt": 0.2, "oauth": 0.9, "crypto": 0.3, "not-asked": 0.99}
	got := Merge(found, asked, scores, 0.7)
	m := map[string]Topic{}
	for _, tp := range got {
		m[tp.ID] = tp
	}
	if _, ok := m["jwt"]; ok {
		t.Error("jwt was answered below the cut and must be dropped")
	}
	if tp := m["oauth"]; tp.Src != SrcJev || tp.Score != 0.9 {
		t.Errorf("oauth %+v", tp)
	}
	if _, ok := m["crypto"]; ok {
		t.Error("crypto below the cut")
	}
	if tp := m["sbom"]; tp.Src != SrcRegex {
		t.Errorf("sbom was asked but unanswered, so the detector's verdict stays: %+v", tp)
	}
	if tp := m["tls"]; tp.Src != SrcRegex {
		t.Errorf("tls was not asked: %+v", tp)
	}
	if _, ok := m["not-asked"]; ok {
		t.Error("a score for a topic that was not asked is ignored")
	}
	if got[0].ID != "oauth" {
		t.Errorf("best first: %v", got)
	}
}

func TestMergeWithNoAnswersIsTheDetector(t *testing.T) {
	found := []Topic{{ID: "jwt", Score: 0.7, Src: SrcRegex}}
	got := Merge(found, map[string]bool{"jwt": true}, nil, 0.7)
	if len(got) != 1 || got[0].ID != "jwt" || got[0].Src != SrcRegex {
		t.Fatalf("%+v", got)
	}
}

func TestLabelTextHoldsOnlyVocabulary(t *testing.T) {
	r := Result{Kind: "source", Lang: "Go", Labels: []string{"type:source", "lang:go"}, Topics: []Topic{{ID: "jwt", Score: 0.8}}}
	txt := LabelText(r, "internal/auth/token.go")
	for _, want := range []string{"type:source", "lang:go", "topic:jwt", "golang", "JSON Web Tokens", "internal auth token.go"} {
		if !strings.Contains(txt, want) {
			t.Errorf("%q missing from %q", want, txt)
		}
	}
}

func TestFastTaggerRefreshesOnlyStaleVersions(t *testing.T) {
	var tg Tagger = Fast{}
	r := tg.Tag(context.Background(), Input{Rel: "a.go", Chunks: []string{"package a"}})
	if tg.Refresh(r) {
		t.Fatal("a fresh result is not stale")
	}
	r.Ver--
	if !tg.Refresh(r) {
		t.Fatal("an older version must refresh")
	}
}

func ids(ts []Topic) map[string]bool {
	m := map[string]bool{}
	for _, t := range ts {
		m[t.ID] = true
	}
	return m
}

func TestResultAllIsSortedAndHasIgnoresCase(t *testing.T) {
	r := Result{Labels: []string{"type:doc", "lang:go"}, Topics: []Topic{{ID: "sbom"}, {ID: "jwt"}}}
	all := r.All()
	want := []string{"lang:go", "topic:jwt", "topic:sbom", "type:doc"}
	if strings.Join(all, ",") != strings.Join(want, ",") {
		t.Fatalf("All = %v, want %v", all, want)
	}
	if !r.Has("Topic:JWT") || !r.Has("type:doc") || r.Has("topic:kubernetes") {
		t.Fatal("Has compares the normalised label")
	}
	if SrcJev.String() != "jev" || SrcRegex.String() != "patterns" || SrcNone.String() != "none" {
		t.Fatalf("source names: %s %s %s", SrcJev, SrcRegex, SrcNone)
	}
}
