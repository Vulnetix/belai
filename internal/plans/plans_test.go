package plans

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/vulnetix/signet/internal/config"
)

func TestPlanSaveLoadRoundTrip(t *testing.T) {
	workdir := t.TempDir()
	p := Plan{Name: "review", Content: "1. read the code\n2. fix it"}

	path, err := Save(workdir, p)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if filepath.Dir(path) != config.ProjectPlansDir(workdir) {
		t.Fatalf("plan path dir = %q, want %q", filepath.Dir(path), config.ProjectPlansDir(workdir))
	}

	got, err := Load(workdir, "review")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Name != "review" || got.Content != p.Content {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
}

func TestPlanList(t *testing.T) {
	workdir := t.TempDir()
	for _, n := range []string{"b-plan", "a-plan"} {
		if _, err := Save(workdir, Plan{Name: n, Content: n}); err != nil {
			t.Fatalf("Save(%s): %v", n, err)
		}
	}
	plans, err := List(workdir)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(plans) != 2 {
		t.Fatalf("expected 2 plans, got %d", len(plans))
	}
	names := []string{plans[0].Name, plans[1].Name}
	if !reflect.DeepEqual(names, []string{"a-plan", "b-plan"}) {
		t.Fatalf("plan order = %v", names)
	}
}

func TestPlanListEmptyDir(t *testing.T) {
	plans, err := List(t.TempDir())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(plans) != 0 {
		t.Fatalf("expected no plans, got %d", len(plans))
	}
}

func TestPlanInvalidName(t *testing.T) {
	if _, err := Save(t.TempDir(), Plan{Name: "///", Content: "x"}); err == nil {
		t.Fatalf("expected invalid plan name to be rejected")
	}
}
