package kanban

import (
	"crypto/sha256"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/docparity"
	"github.com/vulnetix/belai/internal/sessionsync"
)

// exampleBoard is the board the page walks through.
func exampleBoard() Board {
	return Board{Cursor: 7, Items: []Item{{
		ID: "3f9a2c00-0000-4000-8000-000000000000", Title: "Run the tests", List: Review,
		Project: "belai", Created: 1790498651688, Updated: 1790498651688, Dirty: true,
		History: []Move{{ID: "m1", To: Review, At: 1790498651688}},
	}}}
}

// TestBkanPageSizesAndConstantsMatchTheEncoder fails when the encoder's output
// size or constants drift from the page: the Size table, the minimum file size
// and the constants snippet are all derived from the code.
func TestBkanPageSizesAndConstantsMatchTheEncoder(t *testing.T) {
	doc := docparity.Read(t, "docs/bkan.md")
	empty, err := Encode(Board{})
	if err != nil {
		t.Fatal(err)
	}
	example, err := Encode(exampleBoard())
	if err != nil {
		t.Fatal(err)
	}
	payload := len(empty) - headerLen - sha256.Size
	for _, want := range []string{
		fmt.Sprintf("| empty | %d: %d header, %d payload (the schema alone), %d checksum |", len(empty), headerLen, payload, sha256.Size),
		fmt.Sprintf("| the one-item example above | %d (519 when the format was version 1) |", len(example)),
		fmt.Sprintf("Today `Encode` writes version %d and\nthe same board is %d bytes", formatVersion, len(example)),
		fmt.Sprintf("shorter than %d bytes (header plus checksum)", headerLen+sha256.Size),
	} {
		flat := strings.Join(strings.Fields(doc), " ")
		if !strings.Contains(flat, strings.Join(strings.Fields(want), " ")) {
			t.Errorf("docs/bkan.md does not say %q", want)
		}
	}
	block := regexp.MustCompile("(?s)```go\nconst \\((.*?)\\n\\)\n```").FindStringSubmatch(doc)
	if block == nil {
		t.Fatal("the constants snippet moved")
	}
	snippet := strings.Join(strings.Fields(block[1]), " ")
	for _, want := range []string{
		`magic = "BKAN"`,
		fmt.Sprintf("formatVersion = %d", formatVersion),
		fmt.Sprintf("minVersion = %d", minVersion),
		"headerLen = len(magic) + 2",
	} {
		if !strings.Contains(snippet, want) {
			t.Errorf("the constants snippet does not show %q", want)
		}
	}
}

// TestBkanPageDocumentsEveryStoredField keeps the field tables and the struct
// snippets equal to the types gob stores. A new exported field must get a table
// row, and a row must name a field that exists.
func TestBkanPageDocumentsEveryStoredField(t *testing.T) {
	doc := docparity.Read(t, "docs/bkan.md")
	for name, typ := range map[string]reflect.Type{"Board": reflect.TypeOf(Board{}), "Item": reflect.TypeOf(Item{}), "Move": reflect.TypeOf(Move{})} {
		section := regexp.MustCompile("(?s)### `" + name + "`\n(.*?)(?:\n### |\n## |$)").FindStringSubmatch(doc)
		if section == nil {
			t.Errorf("no ### `%s` section", name)
			continue
		}
		rows := map[string]bool{}
		for _, m := range regexp.MustCompile("(?m)^\\| ((?:`[A-Za-z]+`(?:, )?)+) \\|").FindAllStringSubmatch(section[1], -1) {
			for _, f := range regexp.MustCompile("`([A-Za-z]+)`").FindAllStringSubmatch(m[1], -1) {
				rows[f[1]] = true
			}
		}
		fields := map[string]bool{}
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			fields[f.Name] = true
			if !rows[f.Name] {
				t.Errorf("the %s table has no row for the field %s", name, f.Name)
			}
			if !regexp.MustCompile(`(?m)^\s+` + f.Name + `\b|^\s+[A-Za-z, ]*\b` + f.Name + `\b.*\n`).MatchString(doc) {
				t.Errorf("the struct snippet does not show the field %s.%s", name, f.Name)
			}
		}
		for r := range rows {
			if !fields[r] {
				t.Errorf("the %s table documents %s, which the type does not have", name, r)
			}
		}
	}
}

// TestBkanSyncTableMatchesTheWireTypes keeps the Sync mapping table equal to
// what ToWire sends: every JSON key of the wire item and of its agent block is
// in the table, and every field of Item has a row, either mapped to a key or
// marked local only.
func TestBkanSyncTableMatchesTheWireTypes(t *testing.T) {
	doc := docparity.Read(t, "docs/bkan.md")
	i := strings.Index(doc, "## Sync mapping")
	if i < 0 {
		t.Fatal("no Sync mapping section")
	}
	table := doc[i:]
	tag := func(f reflect.StructField) string { return strings.Split(f.Tag.Get("json"), ",")[0] }
	wire := reflect.TypeOf(sessionsync.KanbanItem{})
	for j := 0; j < wire.NumField(); j++ {
		k := tag(wire.Field(j))
		if k == "" || k == "agent" {
			continue
		}
		if !strings.Contains(table, "| `"+k+"` |") {
			t.Errorf("the sync table has no JSON field `%s`", k)
		}
	}
	agent := reflect.TypeOf(sessionsync.KanbanAgent{})
	for j := 0; j < agent.NumField(); j++ {
		if k := tag(agent.Field(j)); !strings.Contains(table, "`agent."+k+"`") {
			t.Errorf("the sync table does not show `agent.%s`", k)
		}
	}
	// Every Item field is on a row of the table, as a Go field in the first cell.
	cells := map[string]bool{}
	for _, m := range regexp.MustCompile("(?m)^\\| ((?:`[A-Za-z]+`(?:, )?)+) \\|").FindAllStringSubmatch(table, -1) {
		for _, f := range regexp.MustCompile("`([A-Za-z]+)`").FindAllStringSubmatch(m[1], -1) {
			cells[f[1]] = true
		}
	}
	item := reflect.TypeOf(Item{})
	for j := 0; j < item.NumField(); j++ {
		name := item.Field(j).Name
		if name == "remoteAgent" {
			continue // in memory only, never stored or sent
		}
		if !cells[name] {
			t.Errorf("the sync table has no row for the Item field %s", name)
		}
	}
	// The security fields are sent, so the table must not say they are local only.
	if strings.Contains(table, "| `Finding`, `SeenRef`, `Verdict`, `VEX` | — |") {
		t.Error("the sync table calls the security fields local only, but ToWire sends them")
	}
}
