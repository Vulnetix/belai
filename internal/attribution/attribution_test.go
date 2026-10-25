package attribution

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

type fixtures struct {
	Trailers []struct {
		Name  string   `json:"name"`
		Ctx   Context  `json:"ctx"`
		Lines []string `json:"lines"`
	} `json:"trailers"`
	Appends []struct {
		Name     string  `json:"name"`
		Ctx      Context `json:"ctx"`
		Markdown bool    `json:"markdown"`
		Text     string  `json:"text"`
		Want     string  `json:"want"`
	} `json:"appends"`
}

func load(t *testing.T) fixtures {
	t.Helper()
	raw, err := os.ReadFile("testdata/attribution-cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var f fixtures
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}

	return f
}

func TestTrailers(t *testing.T) {
	for _, c := range load(t).Trailers {
		t.Run(c.Name, func(t *testing.T) {
			if got := Trailers(c.Ctx); !reflect.DeepEqual(got, c.Lines) {
				t.Fatalf("got %q\nwant %q", got, c.Lines)
			}
		})
	}
}

func TestAppend(t *testing.T) {
	for _, c := range load(t).Appends {
		t.Run(c.Name, func(t *testing.T) {
			if got := Append(c.Text, c.Ctx, c.Markdown); got != c.Want {
				t.Fatalf("got %q\nwant %q", got, c.Want)
			}
		})
	}
}

// The website owns the cases. When it is checked out beside this repository the
// copy must match it byte for byte.
func TestFixturesMatchTheWebsite(t *testing.T) {
	site, err := os.ReadFile("../../../website/src/shared/attribution-cases.json")
	if err != nil {
		t.Skip("website checkout not present")
	}
	mine, err := os.ReadFile("testdata/attribution-cases.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(site, mine) {
		t.Fatal("testdata/attribution-cases.json differs from website/src/shared/attribution-cases.json")
	}
}
