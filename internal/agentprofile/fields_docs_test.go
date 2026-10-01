package agentprofile

import (
	"time"

	"github.com/vulnetix/belai/internal/kanban"
	"reflect"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/docparity"
)

// jsonKeys returns the json key of each exported field of a struct type.
func jsonKeys(typ reflect.Type) []string {
	var out []string
	for i := 0; i < typ.NumField(); i++ {
		tag := strings.Split(typ.Field(i).Tag.Get("json"), ",")[0]
		if tag != "" && tag != "-" {
			out = append(out, tag)
		}
	}
	return out
}

// TestProfilesPageDocumentsEveryTopLevelField keeps the Fields table equal to
// the profile's JSON keys: a key the code reads is a row, and a row is a key.
func TestProfilesPageDocumentsEveryTopLevelField(t *testing.T) {
	doc := docparity.Read(t, "docs/agent-profiles.md")
	// The four worker blocks are documented key by key in the worker section,
	// which TestProfilesPageDocumentsEveryWorkerField checks.
	blocks := map[string]bool{"kanban": true, "workspace": true, "memory": true, "budget": true}
	for _, k := range jsonKeys(reflect.TypeOf(AgentProfile{})) {
		if blocks[k] {
			continue
		}
		if !strings.Contains(doc, "| `"+k+"` |") {
			t.Errorf("the Fields table has no row for the profile key %q", k)
		}
	}
}

// TestProfilesPageDocumentsEveryWorkerField checks the worker table: every
// key of each nested spec appears as `block.key` (or under its own block).
func TestProfilesPageDocumentsEveryWorkerField(t *testing.T) {
	doc := docparity.Read(t, "docs/agent-profiles.md")
	for block, typ := range map[string]reflect.Type{
		"kanban":    reflect.TypeOf(KanbanSpec{}),
		"workspace": reflect.TypeOf(WorkspaceSpec{}),
		"memory":    reflect.TypeOf(MemorySpec{}),
		"budget":    reflect.TypeOf(BudgetSpec{}),
	} {
		for _, k := range jsonKeys(typ) {
			field, _ := typ.FieldByNameFunc(func(n string) bool { return strings.Split(mustField(typ, n), ",")[0] == k })
			if field.Type.Kind() == reflect.Ptr && field.Type.Elem().Kind() == reflect.Struct {
				continue // a nested spec: its own keys are checked below
			}
			if !strings.Contains(doc, block+"."+k) {
				t.Errorf("the worker table does not document `%s.%s`", block, k)
			}
		}
	}
	for prefix, typ := range map[string]reflect.Type{
		"kanban.survey.":   reflect.TypeOf(SurveySpec{}),
		"kanban.security.": reflect.TypeOf(SecuritySpec{}),
		"kanban.quality.":  reflect.TypeOf(QualitySpec{}),
		"kanban.gates.":    reflect.TypeOf(GatesSpec{}),
	} {
		for _, k := range jsonKeys(typ) {
			if !strings.Contains(doc, prefix+k) {
				t.Errorf("the worker table does not document `%s%s`", prefix, k)
			}
		}
	}
}

func mustField(typ reflect.Type, name string) string {
	f, _ := typ.FieldByName(name)
	return f.Tag.Get("json")
}

// TestProfilesPageStatesTheModesAndValues pins the mode, autonomy and effort
// vocabularies the Fields table gives.
func TestProfilesPageStatesTheModesAndValues(t *testing.T) {
	doc := strings.Join(strings.Fields(docparity.Read(t, "docs/agent-profiles.md")), " ")
	for _, want := range []string{
		"One of `" + ModeSingle + "`, `" + ModeLoop + "`, `" + ModeScheduled + "`, `" + ModeMonitor + "`, `" + ModeWorker + "`",
		"`" + AutonomySupervised + "` (default) or `" + AutonomyAutonomous + "`",
		"one of `low`, `medium`, `high`, `none`",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("docs/agent-profiles.md does not say %q", want)
		}
	}
	for _, e := range []string{"low", "medium", "high", "none"} {
		if !validEfforts[e] {
			t.Errorf("the page lists the effort %q, which the validator rejects", e)
		}
	}
}

// TestProfilesPageStatesTheWorkerDefaultsAndBounds pins the numbers in the
// worker table to the constants the validator and the claim loop use.
func TestProfilesPageStatesTheWorkerDefaultsAndBounds(t *testing.T) {
	doc := strings.Join(strings.Fields(docparity.Read(t, "docs/agent-profiles.md")), " ")
	for _, want := range []string{
		"(default 3)",
		"Claim lease (1m–2h, default 20m) and idle poll (at least 5s, default 30s)",
		"(at least `1h`, default `24h`)",
		"(default 8 KiB, at most 64 KiB)",
		"0 to 5 (0 or 1: one turn)",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("docs/agent-profiles.md does not say %q", want)
		}
	}
	if DefaultMaxAttempts != 3 || DefaultLease != 20*time.Minute || DefaultPoll != 30*time.Second {
		t.Errorf("claim defaults %d/%v/%v disagree with the page", DefaultMaxAttempts, DefaultLease, DefaultPoll)
	}
	if kanban.MinLease != time.Minute || kanban.MaxLease != 2*time.Hour {
		t.Errorf("lease bounds %v..%v disagree with the page", kanban.MinLease, kanban.MaxLease)
	}
	if DefaultMemoryBytes != 8<<10 || MaxMemoryBytes != 64<<10 || MaxRounds != 5 || DefaultSurveyEvery != 24*time.Hour {
		t.Errorf("memory %d..%d, rounds %d, survey %v disagree with the page", DefaultMemoryBytes, MaxMemoryBytes, MaxRounds, DefaultSurveyEvery)
	}
}
