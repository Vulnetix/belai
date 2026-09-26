package tools

import (
	"slices"
	"testing"

	"github.com/vulnetix/belai/internal/permissions"
	"github.com/vulnetix/belai/internal/repoindex"
)

// Plan mode drops every mutating tool and Bash, read-only or not. Read-only
// Bash is not mutating, so ReadOnly alone would have kept it.
func TestRegistryPlanDropsBashAndMutatingTools(t *testing.T) {
	reg := Default(t.TempDir(), false)
	plan := reg.Plan()
	names := plan.Names()

	for _, gone := range []string{"Bash", "Write", "Edit"} {
		if slices.Contains(names, gone) {
			t.Errorf("plan registry still offers %q: %v", gone, names)
		}
	}
	for _, kept := range []string{"Read", "Grep", "Glob", "WebFetch"} {
		if !slices.Contains(names, kept) {
			t.Errorf("plan registry dropped %q: %v", kept, names)
		}
	}
	if _, ok := plan.Find("Bash"); ok {
		t.Error("Bash is still findable in the plan registry")
	}
}

// Read-only Bash survives ReadOnly (it does not mutate) but not Plan. This is
// the difference between the two narrowings, and it is the whole reason Plan
// exists as its own method.
func TestRegistryPlanIsStricterThanReadOnly(t *testing.T) {
	reg := Default(t.TempDir(), true)
	if _, ok := reg.Find("Bash"); !ok {
		t.Fatal("read-only Bash should still be registered under the read-only switch")
	}
	if _, ok := reg.Plan().Find("Bash"); ok {
		t.Fatal("read-only Bash must not survive the plan-mode narrowing")
	}
}

// Every tool the plan registry keeps must be read-only, so the plan surface
// can never contain something that writes.
func TestRegistryPlanKeepsOnlyReadOnlyTools(t *testing.T) {
	plan := Default(t.TempDir(), false).Plan()
	for _, tool := range plan.tools {
		if Mutates(tool) {
			t.Errorf("plan registry kept mutating tool %q", tool.Definition().Name)
		}
	}
}

// Narrowing an already-narrowed registry changes nothing: Plan is idempotent,
// so applying it on a path that already applied it cannot drop more.
func TestRegistryPlanIsIdempotent(t *testing.T) {
	once := Default(t.TempDir(), false).Plan().Names()
	twice := Default(t.TempDir(), false).Plan().Plan().Names()
	if !slices.Equal(once, twice) {
		t.Fatalf("Plan is not idempotent: once=%v twice=%v", once, twice)
	}
}

// Narrowing an empty registry is an empty registry, not a panic.
func TestRegistryPlanOnEmptyRegistry(t *testing.T) {
	if names := NewRegistry().Plan().Names(); len(names) != 0 {
		t.Fatalf("empty registry narrowed to %v", names)
	}
}

// Native catalogue tools are read-only by construction and must survive plan
// mode: they are what replaces Bash there.
func TestRegistryPlanKeepsNativeTools(t *testing.T) {
	caps := Capabilities{local: map[string]bool{"Cat": true, "LS": true, "Git": true}}
	plan := DefaultWithCaps(t.TempDir(), false, caps, repoindex.Index{}).Plan()
	names := plan.Names()
	for _, want := range []string{"Cat", "LS", "Git"} {
		if !slices.Contains(names, want) {
			t.Errorf("plan registry dropped native tool %q: %v", want, names)
		}
	}
}

// PlanWith guardrails off returns the full registry unchanged, including
// mutating tools and full Bash.
func TestRegistryPlanWithGuardrailsOffReturnsFullSurface(t *testing.T) {
	reg := Default(t.TempDir(), false)
	plan := reg.PlanWith(PlanSurface{GuardrailsOff: true})
	for _, name := range []string{"Bash", "Write", "Edit"} {
		if _, ok := plan.Find(name); !ok {
			t.Errorf("guardrails-off plan registry dropped %q", name)
		}
	}
}

// PlanWith a Bash allow rule advertises read-only Bash and keeps the rest of
// the fail-closed surface.
func TestRegistryPlanWithBashRuleKeepsReadOnlyBash(t *testing.T) {
	reg := Default(t.TempDir(), false)
	perms := permissions.From([]string{"Bash(git status)"}, nil, nil)
	plan := reg.PlanWith(PlanSurface{Perms: perms})
	if _, ok := plan.Find("Bash"); !ok {
		t.Fatal("Bash allow rule should keep read-only Bash in plan registry")
	}
	if _, ok := plan.Find("Write"); ok {
		t.Fatal("plan registry must still drop Write")
	}
}

// PlanWith zero surface drops Bash, matching the legacy Plan behaviour.
func TestRegistryPlanWithZeroSurfaceDropsBash(t *testing.T) {
	reg := Default(t.TempDir(), false)
	plan := reg.PlanWith(PlanSurface{})
	if _, ok := plan.Find("Bash"); ok {
		t.Fatal("zero plan surface must drop Bash")
	}
}
