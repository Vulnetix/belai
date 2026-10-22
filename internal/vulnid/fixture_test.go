package vulnid

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

// testdata/vulnid.json pins the identifier shapes. The same file is committed
// in vdb-site at api/internal/facts/testdata/vulnid.json, where the Fact index
// extracts vulnerability ids from session transcripts with the same shapes and
// the same boundary rules. Change both together: the two tests are the only
// thing that keeps the extractor from drifting from the identifiers Belai
// itself links to.
//
// The fixture's "valid" map is what Valid accepts and what it canonicalises it
// to; "invalid" is what it refuses; "find" is what Find reports for running
// text (which, unlike vdb-site's user-text pass, never accepts a lower-case
// prefix).
func TestSharedFixture(t *testing.T) {
	b, err := os.ReadFile("testdata/vulnid.json")
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Valid   map[string]string `json:"valid"`
		Invalid []string          `json:"invalid"`
		Find    []struct {
			Text string   `json:"text"`
			Want []string `json:"want"`
		} `json:"find"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	for in, want := range f.Valid {
		if got, ok := Valid(in); !ok || got != want {
			t.Errorf("Valid(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, in := range f.Invalid {
		if got, ok := Valid(in); ok {
			t.Errorf("Valid(%q) = %q; want refused", in, got)
		}
	}
	for _, c := range f.Find {
		got := Find(c.Text, 0)
		if len(got) == 0 {
			got = []string{}
		}
		if !reflect.DeepEqual(got, c.Want) {
			t.Errorf("Find(%q) = %v; want %v", c.Text, got, c.Want)
		}
	}
}
