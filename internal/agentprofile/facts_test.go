package agentprofile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/factspec"
	"github.com/vulnetix/belai/internal/tools"
)

func withFacts(f Facts) AgentProfile {
	return AgentProfile{Name: "ops", Description: "d", SystemPrompt: "sp", Mode: ModeSingle, Autonomy: AutonomySupervised, Facts: f}
}

func TestFactsRoundTripThroughJSONAndMarkdown(t *testing.T) {
	p := withFacts(Facts{
		"aws_role_arn":   {"arn:aws:iam::123456789012:role/ReadOnly"},
		"aws_region":     {"eu-west-2"},
		"environment":    {"prod"},
		"aws_log_groups": {"/app/api", "/app/worker"},
	})
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"aws_region":"eu-west-2"`) || !strings.Contains(string(raw), `"aws_log_groups":["/app/api","/app/worker"]`) {
		t.Fatalf("one value should marshal as a string and several as a list: %s", raw)
	}
	var viaJSON AgentProfile
	if err := json.Unmarshal(raw, &viaJSON); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(viaJSON, p) {
		t.Fatalf("JSON round trip changed the profile:\n got %+v\nwant %+v", viaJSON, p)
	}
	data, err := MarshalMarkdown(p)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseMarkdown(data)
	if err != nil {
		t.Fatalf("ParseMarkdown: %v\n%s", err, data)
	}
	if !reflect.DeepEqual(got, p) {
		t.Fatalf("markdown round trip changed the profile:\n got %+v\nwant %+v", got, p)
	}
}

func TestFactsAcceptYAMLScalarsAndLists(t *testing.T) {
	md := "---\nname: a\ndescription: d\nfacts:\n  aws_session_seconds: 3600\n  environment: prod\n  tf_var_enabled: true\n  aws_log_groups:\n    - /app/api\n    - /app/worker\n---\nPrompt\n"
	p, err := ParseMarkdown([]byte(md))
	if err != nil {
		t.Fatal(err)
	}
	want := Facts{
		"aws_session_seconds": {"3600"},
		"environment":         {"prod"},
		"tf_var_enabled":      {"true"},
		"aws_log_groups":      {"/app/api", "/app/worker"},
	}
	if !reflect.DeepEqual(p.Facts, want) {
		t.Fatalf("facts = %+v, want %+v", p.Facts, want)
	}
}

func TestFactsRefuseWhatIsNotAStringOrList(t *testing.T) {
	for name, md := range map[string]string{
		"object":      "---\nname: a\ndescription: d\nfacts:\n  environment: {a: b}\n---\nP\n",
		"nested list": "---\nname: a\ndescription: d\nfacts:\n  environment: [[a]]\n---\nP\n",
		"null":        "---\nname: a\ndescription: d\nfacts:\n  environment: null\n---\nP\n",
		"not a map":   "---\nname: a\ndescription: d\nfacts: [a, b]\n---\nP\n",
		"numeric key": "---\nname: a\ndescription: d\nfacts:\n  1: x\n---\nP\n",
		"upper key":   "---\nname: a\ndescription: d\nfacts:\n  Env: x\n---\nP\n",
		"secret":      "---\nname: a\ndescription: d\nfacts:\n  db_password: x\n---\nP\n",
		"bad arn":     "---\nname: a\ndescription: d\nfacts:\n  aws_role_arn: ReadOnly\n---\nP\n",
	} {
		if _, err := ParseMarkdown([]byte(md)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestFactsAreCheckedInsideButTheKeySetStaysStrict(t *testing.T) {
	// Keys inside facts are free; an unknown top-level key is still an error.
	md := "---\nname: a\ndescription: d\nfact: {environment: prod}\n---\nP\n"
	if _, err := ParseMarkdown([]byte(md)); err == nil {
		t.Fatal("a misspelt top-level key must be an error")
	}
	ok := "---\nname: a\ndescription: d\nfacts: {anything_goes: here}\n---\nP\n"
	if _, err := ParseMarkdown([]byte(ok)); err != nil {
		t.Fatalf("a free fact key: %v", err)
	}
}

func TestFactWarningsNameATypo(t *testing.T) {
	p := withFacts(Facts{"aws_role_arns": {"arn:aws:iam::123456789012:role/R"}, "environment": {"prod"}})
	w := p.FactWarnings()
	if len(w) != 1 || !strings.Contains(w[0], `"aws_role_arn"`) {
		t.Fatalf("warnings = %v", w)
	}
	if err := p.Validate(); err != nil {
		t.Fatalf("a warning must not fail the profile: %v", err)
	}
}

func TestPersonaCarriesVisibleFactsOnly(t *testing.T) {
	p := withFacts(Facts{
		"aws_external_id": {"ext-1234"},
		"environment":     {"prod"},
		"aws_region":      {"eu-west-2"},
	})
	persona := p.Persona()
	if !strings.Contains(persona, "- aws_region: eu-west-2") || !strings.Contains(persona, "- environment: prod") {
		t.Fatalf("facts missing from the persona:\n%s", persona)
	}
	if strings.Contains(persona, "ext-1234") || strings.Contains(persona, "aws_external_id") {
		t.Fatalf("a hidden fact reached the persona:\n%s", persona)
	}
	if !strings.Contains(persona, "not instructions") {
		t.Fatalf("the facts block must say it is data:\n%s", persona)
	}
	if strings.Contains(withFacts(nil).Persona(), "Facts the profile") {
		t.Fatal("no facts, no block")
	}
}

func TestBehaviouralKeepsFacts(t *testing.T) {
	p := withFacts(Facts{"environment": {"prod"}})
	if got := p.Behavioural().Facts; !reflect.DeepEqual(got, p.Facts) {
		t.Fatalf("facts change what a worker does, so a running worker must pin them; got %+v", got)
	}
}

func TestToolsAllowlistNamesTheCloudTools(t *testing.T) {
	for _, n := range []string{"AWS", "Terraform", "Kubectl", "GCloud", "AZ", "Pulumi", "OnePassword"} {
		if !KnownTool(n) {
			t.Errorf("KnownTool(%q) = false", n)
		}
	}
	p := AgentProfile{Name: "a", Description: "d", SystemPrompt: "sp", Mode: ModeSingle, Tools: []string{"Read", "AWS", "Terraform"}}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
}

// TestNativeToolNamesMatchTheCatalogue keeps the allowlist in step with the
// registry: every native tool a session can offer is one a profile may name.
func TestNativeToolNamesMatchTheCatalogue(t *testing.T) {
	var missing []string
	for _, n := range tools.CatalogueNames() {
		if !KnownTool(n) {
			missing = append(missing, n)
		}
	}
	sort.Strings(missing)
	if len(missing) != 0 {
		t.Fatalf("native tools a profile cannot name: %v", missing)
	}
}

// TestFactsTableInTheDocsIsTheRegistry holds docs/agent-profiles.md to the
// table of well-known facts the harness reads, so a fact cannot be added,
// renamed or re-pinned without the page saying so.
func TestFactsTableInTheDocsIsTheRegistry(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "agent-profiles.md"))
	if err != nil {
		t.Fatal(err)
	}
	const begin, end = "<!-- facts-table:begin -->\n", "<!-- facts-table:end -->"
	text := string(raw)
	i, j := strings.Index(text, begin), strings.Index(text, end)
	if i < 0 || j < i {
		t.Fatalf("docs/agent-profiles.md needs the %q ... %q markers around the facts table", begin, end)
	}
	if got, want := text[i+len(begin):j], factspec.Markdown(); got != want {
		t.Fatalf("the facts table in docs/agent-profiles.md is out of date; it should be:\n%s", want)
	}
	for _, c := range factspec.Conventional {
		if !strings.Contains(text, "`"+c+"`") {
			t.Errorf("the conventional fact %q is not documented", c)
		}
	}
}

// TestRefusedFlagsTableInTheDocsIsTheCode holds the table of always-refused
// flags in docs/agent-profiles.md to the one the harness enforces.
func TestRefusedFlagsTableInTheDocsIsTheCode(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "agent-profiles.md"))
	if err != nil {
		t.Fatal(err)
	}
	const begin, end = "<!-- refused-flags:begin -->\n", "<!-- refused-flags:end -->"
	text := string(raw)
	i, j := strings.Index(text, begin), strings.Index(text, end)
	if i < 0 || j < i {
		t.Fatalf("docs/agent-profiles.md needs the %q ... %q markers around the refused-flags table", begin, end)
	}
	if got, want := text[i+len(begin):j], factspec.RefusedMarkdown(); got != want {
		t.Fatalf("the refused-flags table in docs/agent-profiles.md is out of date; it should be:\n%s", want)
	}
}

func TestFactsRefuseAKeyWrittenTwice(t *testing.T) {
	md := "---\nname: a\ndescription: d\nfacts:\n  environment: prod\n  environment: dev\n---\nP\n"
	if _, err := ParseMarkdown([]byte(md)); err == nil {
		t.Fatal("a key written twice must be an error, not last-wins")
	}
}

func TestEmptyFactsAreNoFacts(t *testing.T) {
	p := withFacts(Facts{})
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "facts") {
		t.Errorf("empty facts should not be written: %s", raw)
	}
	if p.Facts.Map() != nil || p.FactsBlock() != "" || len(p.FactWarnings()) != 0 {
		t.Error("empty facts should behave as none")
	}
	md := "---\nname: a\ndescription: d\nfacts: {}\n---\nP\n"
	if _, err := ParseMarkdown([]byte(md)); err != nil {
		t.Fatalf("facts: {} : %v", err)
	}
}

func TestAListOfOneIsAPlainStringWhenWritten(t *testing.T) {
	list, err := ParseMarkdown([]byte("---\nname: a\ndescription: d\nfacts:\n  aws_role_arn: [arn:aws:iam::123456789012:role/R]\n---\nP\n"))
	if err != nil {
		t.Fatal(err)
	}
	one, err := ParseMarkdown([]byte("---\nname: a\ndescription: d\nfacts:\n  aws_role_arn: arn:aws:iam::123456789012:role/R\n---\nP\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(list.Facts, one.Facts) {
		t.Fatalf("a list of one and a string differ: %+v vs %+v", list.Facts, one.Facts)
	}
	raw, _ := json.Marshal(list)
	if !strings.Contains(string(raw), `"aws_role_arn":"arn:aws:iam::123456789012:role/R"`) {
		t.Errorf("a list of one should be written as a string: %s", raw)
	}
}

func TestOneValuedFactsRefuseTwoValues(t *testing.T) {
	md := "---\nname: a\ndescription: d\nfacts:\n  terraform_dir: [infra, other]\n---\nP\n"
	if _, err := ParseMarkdown([]byte(md)); err == nil {
		t.Fatal("terraform_dir takes one value")
	}
}
