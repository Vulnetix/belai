package ticketrules

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// The golden file is a copy of vdb-site's api/internal/handler/testdata/
// ticket-standard.golden.json; both sides must return the same codes.
func TestGolden(t *testing.T) {
	raw, err := os.ReadFile("testdata/ticket-standard.golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var g struct {
		Sections []string `json:"sections"`
		Cases    []struct {
			Name, Title, Body string
			Codes             []string
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &g); err != nil {
		t.Fatal(err)
	}
	if strings.Join(g.Sections, ",") != strings.Join(Sections, ",") {
		t.Fatalf("sections = %v, golden %v", Sections, g.Sections)
	}
	for _, c := range g.Cases {
		var got []string
		for _, i := range Check(c.Title, c.Body) {
			got = append(got, i.Code)
		}
		if strings.Join(got, ",") != strings.Join(c.Codes, ",") {
			t.Errorf("%s: %v, want %v", c.Name, got, c.Codes)
		}
	}
}

func TestRulesNameEverySection(t *testing.T) {
	for _, s := range Sections {
		if !strings.Contains(Rules, "## "+s) {
			t.Errorf("the rules do not name %s", s)
		}
	}
	once := Append("prompt")
	if Append(once) != once || !strings.HasPrefix(once, "prompt\n\n") {
		t.Fatalf("Append is not idempotent: %q", once)
	}
	if strings.ContainsAny(Rules, "—–") {
		t.Fatal("the rules use a dash the prose rules forbid")
	}
}
