package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/signet/internal/config"
)

func modelScreen(t *testing.T) *App {
	t.Helper()
	a := New(Options{})
	a.Update(tea.WindowSizeMsg{Width: 80, Height: 40})
	_ = a.enterModel()
	return a
}

func rowByKey(rows []modelRow, key string) (modelRow, bool) {
	for _, r := range rows {
		if r.key == key {
			return r, true
		}
	}
	return modelRow{}, false
}

func TestModelRowsPhasesHiddenForLLM(t *testing.T) {
	a := modelScreen(t)
	a.settings.Classifier = &config.ClassifierSettings{Kind: "llm"}
	for _, r := range a.modelRows() {
		if r.key == "phase1" || r.key == "phase2" || r.key == "phase3" {
			t.Fatalf("phase row %q must be hidden when kind == llm", r.key)
		}
	}
	if _, ok := rowByKey(a.modelRows(), "kind"); !ok {
		t.Fatal("kind row must be present when kind == llm")
	}
}

func TestModelRowsPhasesForModelsVanilla(t *testing.T) {
	t.Setenv("HF_TOKEN", "")
	t.Setenv("HUGGINGFACE_TOKEN", "")
	a := modelScreen(t)
	a.settings.Classifier = &config.ClassifierSettings{Kind: "models"}

	p1, ok := rowByKey(a.modelRows(), "phase1")
	if !ok {
		t.Fatal("phase1 row missing for models kind")
	}
	if !strings.Contains(p1.value, "LLM sentinel (no HuggingFace key)") || !p1.disabled {
		t.Fatalf("phase1 row = %+v, want locked 'LLM sentinel (no HuggingFace key)'", p1)
	}

	p2, ok := rowByKey(a.modelRows(), "phase2")
	if !ok {
		t.Fatal("phase2 row missing for models kind")
	}
	if !strings.Contains(p2.value, "disabled") {
		t.Fatalf("phase2 row = %+v, want disabled status", p2)
	}

	p3, ok := rowByKey(a.modelRows(), "phase3")
	if !ok {
		t.Fatal("phase3 row missing for models kind")
	}
	if !p3.disabled || !strings.Contains(p3.value, "off — set classifier provider + model to enable") {
		t.Fatalf("phase3 row = %+v, want locked off status", p3)
	}
}

func TestModelPhase3RowOn(t *testing.T) {
	a := modelScreen(t)
	a.settings.Classifier = &config.ClassifierSettings{Kind: "models", Provider: "openai", Model: "gpt-5"}
	row := a.classifierPhase3Row()
	if !row.disabled || !strings.Contains(row.value, "extraction only") || !strings.Contains(row.value, "openai/gpt-5") {
		t.Fatalf("phase3 row = %+v, want locked 'extraction only · openai/gpt-5'", row)
	}
}

func TestModelKindRowEditableOnVanilla(t *testing.T) {
	a := modelScreen(t)
	a.settings.Classifier = &config.ClassifierSettings{Kind: "llm"}
	kind, ok := rowByKey(a.modelRows(), "kind")
	if !ok {
		t.Fatal("kind row missing")
	}
	if kind.disabled {
		t.Fatal("kind row must be editable on a vanilla binary")
	}
	if kind.value != "llm" {
		t.Fatalf("kind value = %q, want llm", kind.value)
	}
}

func TestCycleClassifierKind(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	a := newModelScreen(t, t.TempDir())
	a.modelState.classifierScope = "project"
	a.settings.Classifier = &config.ClassifierSettings{Kind: "llm"}
	a.modelState.rows = a.modelRows()

	_ = a.cycleClassifierKind([]string{"llm", "models"})
	if a.settings.Classifier.Kind != "models" {
		t.Fatalf("kind = %q, want models after cycle", a.settings.Classifier.Kind)
	}
	_ = a.cycleClassifierKind([]string{"llm", "models"})
	if a.settings.Classifier.Kind != "llm" {
		t.Fatalf("kind = %q, want llm after second cycle", a.settings.Classifier.Kind)
	}
}
