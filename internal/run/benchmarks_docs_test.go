package run

import (
	"bytes"
	"encoding/json"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/docparity"
)

// TestBenchmarksPageExampleMatchesTheSummaryFormat decodes the example in
// docs/benchmarks.md into the real summary type, refusing unknown fields, and
// checks the example shows every top-level field the type has.
func TestBenchmarksPageExampleMatchesTheSummaryFormat(t *testing.T) {
	doc := docparity.Read(t, "docs/benchmarks.md")
	block := regexp.MustCompile("(?s)```json\n(.*?)```").FindStringSubmatch(doc)
	if block == nil {
		t.Fatal("docs/benchmarks.md has no json example")
	}
	example := strings.ReplaceAll(block[1], "{ … }", "{}")
	var sum usageSummaryJSON
	dec := json.NewDecoder(bytes.NewReader([]byte(example)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&sum); err != nil {
		t.Fatalf("the example does not decode into the summary type: %v", err)
	}
	typ := reflect.TypeOf(sum)
	for i := 0; i < typ.NumField(); i++ {
		tag := strings.Split(typ.Field(i).Tag.Get("json"), ",")[0]
		if !strings.Contains(example, `"`+tag+`"`) {
			t.Errorf("the example does not show the summary field %q", tag)
		}
	}
	for _, tag := range []string{"system", "tool_defs", "history", "tool_results"} {
		if !strings.Contains(example, `"`+tag+`"`) {
			t.Errorf("the example does not show the agent_request field %q", tag)
		}
	}
}

// TestBenchmarksPageNamesTheRoles keeps the roles the page lists for a usage
// event equal to the constants the code reports.
func TestBenchmarksPageNamesTheRoles(t *testing.T) {
	doc := docparity.Read(t, "docs/benchmarks.md")
	for _, role := range []string{RoleAgent, RoleSecurity} {
		if !strings.Contains(doc, "`"+role+"`") {
			t.Errorf("docs/benchmarks.md does not name the role %q", role)
		}
	}
}
