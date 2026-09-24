package tui

import (
	"strings"
	"testing"

	"github.com/vulnetix/signet/internal/config"
)

func selectRow(t *testing.T, a *App, role modelRole, key string) {
	t.Helper()
	a.modelState.rows = a.modelRows()
	for i, r := range a.modelState.rows {
		if r.role == role && r.key == key {
			a.modelState.selected = i
			return
		}
	}
	t.Fatalf("no %s/%s row", role, key)
}

func TestModelScreenFastTierRows(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	a := newModelScreen(t, t.TempDir())
	a.modelState.routingScope = "project"

	// Enter on the provider row assigns one; the model follows that
	// provider's registry default until one is picked.
	selectRow(t, a, roleFast, "provider")
	_ = a.changeModelRow()
	if a.settings.Routing == nil || a.settings.Routing.Fast == nil || a.settings.Routing.Fast.Provider == "" {
		t.Fatalf("fast provider not stored: %+v", a.settings.Routing)
	}
	if a.settings.Routing.Fast.Model != "" {
		t.Fatal("a provider change must clear the fast model")
	}

	// Picking a model keeps the provider.
	prov := a.settings.Routing.Fast.Provider
	_ = a.setFastModel("tiny-model")
	if f := a.settings.Routing.Fast; f.Provider != prov || f.Model != "tiny-model" {
		t.Fatalf("fast = %+v", f)
	}

	// Clearing the model keeps the provider; clearing the provider drops both.
	selectRow(t, a, roleFast, "model")
	_, _ = a.handleModelKey(modelKey("c"))
	if f := a.settings.Routing.Fast; f == nil || f.Model != "" || f.Provider != prov {
		t.Fatalf("after clearing the model: %+v", f)
	}
	selectRow(t, a, roleFast, "provider")
	_, _ = a.handleModelKey(modelKey("c"))
	if a.settings.Routing != nil && a.settings.Routing.Fast != nil {
		t.Fatalf("after clearing the provider: %+v", a.settings.Routing.Fast)
	}
}

func TestModelScreenClassifierTierToggle(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	a := newModelScreen(t, t.TempDir())
	a.modelState.classifierScope = "project"
	selectRow(t, a, roleClassifier, "tier")
	if row := a.modelState.rows[a.modelState.selected]; row.value != config.ClassifierTierMain {
		t.Fatalf("default tier = %q, want main", row.value)
	}
	_ = a.changeModelRow()
	if a.settings.Classifier == nil || a.settings.Classifier.Tier != config.ClassifierTierFast {
		t.Fatalf("tier not toggled: %+v", a.settings.Classifier)
	}
	selectRow(t, a, roleClassifier, "tier")
	_, _ = a.handleModelKey(modelKey("c"))
	if a.settings.Classifier != nil && a.settings.Classifier.Tier != "" {
		t.Fatalf("tier not cleared: %+v", a.settings.Classifier)
	}
}

func TestModelScreenSaysWhoAnswersWhat(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	a := newModelScreen(t, t.TempDir())
	view := a.modelView()
	for _, want := range []string{"IN EFFECT", "work", "verdicts", "drafting", "security", "FAST TIER", "answers one-token verdicts", "does the work"} {
		if !strings.Contains(view, want) {
			t.Fatalf("/model view missing %q:\n%s", want, view)
		}
	}
}

// TestModelRoutingBlurbFollowsKind pins that the routing group describes the
// kind actually set: under defined the pool is unused, and only routed claims
// Jev is picking.
func TestModelRoutingBlurbFollowsKind(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	a := newModelScreen(t, t.TempDir())
	if got := a.modelRoleBlurb(roleRouting); !strings.Contains(got, "unused") || strings.Contains(got, "Jev picks") {
		t.Fatalf("defined blurb = %q", got)
	}
	a.settings.Routing = &config.RoutingSettings{Kind: config.RoutingRouted}
	if got := a.modelRoleBlurb(roleRouting); !strings.Contains(got, "Jev picks") {
		t.Fatalf("routed blurb = %q", got)
	}
}
