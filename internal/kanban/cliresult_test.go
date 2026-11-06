package kanban

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

const goodCliResult = `{"job":"sca","exitCode":0,"counts":{"critical":0,"high":1,"medium":0,"low":0},"findings":[{"id":"CVE-2024-1","kind":"sca","path":"go.mod","severity":"high","ref":"x@1"}],"signals":[],"artifacts":["memory.yaml"]}`

// The result is harness-set: a pull fills an empty field, validates what it
// fills, and never replaces or clears a local value.
func TestCliResultIsFillOnlyAndValidated(t *testing.T) {
	s := testStore(t)
	clock := time.UnixMilli(9_000_000)
	s.now = func() time.Time { return clock }
	it := addItem(t, s, ItemInput{Title: "job", Labels: []string{"cli-job:sca"}})
	local, _ := s.Get(it.ID)

	w := ToWire(local)
	w.UpdatedAt = local.Updated + 10
	w.Agent.CliResult = json.RawMessage(`{"job":"sca","exitCode":0,"counts":{"critical":0,"high":0,"medium":0,"low":0},"findings":[],"signals":[],"artifacts":[],"extra":1}`)
	if _, err := s.Merge([]Item{FromWire(w)}, 1); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Get(it.ID); got.CliResult != "" {
		t.Fatalf("a result outside the closed shape was taken: %q", got.CliResult)
	}

	w.Agent.CliResult = json.RawMessage(goodCliResult)
	w.UpdatedAt = local.Updated + 20
	if _, err := s.Merge([]Item{FromWire(w)}, 2); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(it.ID)
	if got.CliResult != goodCliResult {
		t.Fatalf("an empty field takes a valid pulled result: %q", got.CliResult)
	}

	w.Agent.CliResult = json.RawMessage(strings.Replace(goodCliResult, `"high":1`, `"high":9`, 1))
	w.UpdatedAt = local.Updated + 30
	if _, err := s.Merge([]Item{FromWire(w)}, 3); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Get(it.ID); got.CliResult != goodCliResult {
		t.Fatalf("a pull replaced the local result: %q", got.CliResult)
	}
	w.Agent.CliResult = nil
	w.UpdatedAt = local.Updated + 40
	if _, err := s.Merge([]Item{FromWire(w)}, 4); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Get(it.ID); got.CliResult != goodCliResult {
		t.Fatalf("a pull without the field cleared it: %q", got.CliResult)
	}
	if out := ToWire(got); string(out.Agent.CliResult) != goodCliResult {
		t.Fatalf("the result must ride the wire as an object: %s", out.Agent.CliResult)
	}
}

// Only a release by the claiming worker sets the result, and a malformed one
// is not stored.
func TestReleaseRecordsOnlyAValidCliResult(t *testing.T) {
	s := testStore(t)
	it := addItem(t, s, ItemInput{Title: "job", Labels: []string{"cli-job:sca"}})
	req := ClaimRequest{Worker: "w1", Lease: time.Minute, LabelPrefix: "cli-job:"}
	if _, err := s.Claim(req); err != nil {
		t.Fatal(err)
	}
	out, err := s.Release(it.ID, "w1", Outcome{To: Done, CliResult: `{"job":"rm"}`})
	if err != nil || out.CliResult != "" {
		t.Fatalf("a malformed result was stored: %+v %v", out, err)
	}
	it2 := addItem(t, s, ItemInput{Title: "job2", Labels: []string{"cli-job:sca"}})
	if _, err := s.Claim(req); err != nil {
		t.Fatal(err)
	}
	out, err = s.Release(it2.ID, "w1", Outcome{To: Done, CliResult: goodCliResult})
	if err != nil || out.CliResult != goodCliResult {
		t.Fatalf("%+v %v", out, err)
	}
}

func TestClaimLabelPrefixMatchesOnlyCliJobCards(t *testing.T) {
	s := testStore(t)
	addItem(t, s, ItemInput{Title: "plain", Labels: []string{"cli-job"}})
	addItem(t, s, ItemInput{Title: "empty", Labels: []string{"cli-job:"}})
	want := addItem(t, s, ItemInput{Title: "job", Labels: []string{"cli-job:scout"}})
	got, err := s.Claim(ClaimRequest{Worker: "w1", Lease: time.Minute, LabelPrefix: "cli-job:"})
	if err != nil || got.ID != want.ID {
		t.Fatalf("claimed %+v %v", got, err)
	}
}
