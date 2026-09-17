package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/wire"
)

// newModelPickerApp writes a custom provider carrying the given model ids into
// the test's SIGNET_HOME and returns an App with that provider selected in the
// /model picker. Workdir is a temp dir so a commit never touches real settings.
func newModelPickerApp(t *testing.T, ids []string) *App {
	t.Helper()
	t.Setenv("SIGNET_HOME", t.TempDir())

	models := make([]config.ProviderModel, len(ids))
	for i, id := range ids {
		models[i] = config.ProviderModel{ID: id}
	}
	if err := config.SaveGlobal(config.Settings{Providers: map[string]config.ProviderProfile{
		"my-llm": {BaseURL: "https://llm.example/v1", API: wire.SurfaceOpenAIChat, Models: models},
	}}); err != nil {
		t.Fatalf("SaveGlobal: %v", err)
	}

	a := New(Options{Workdir: t.TempDir()})
	a.modelState = modelViewState{providerIdx: indexOfString(a.providerNames(), "my-llm"), scope: "project"}
	return a
}

// numberedModels returns n ids m00..m<n-1>, so substring assertions can
// distinguish one row from another without accidental prefix matches.
func numberedModels(n int) []string {
	ids := make([]string, n)
	for i := range ids {
		ids[i] = fmt.Sprintf("m%02d", i)
	}
	return ids
}

// opusModels returns 45 non-matching ids followed by 5 ids containing "opus",
// so a filter on "opus" narrows 50 down to 5.
func opusModels() []string {
	ids := numberedModels(45)
	for i := 0; i < 5; i++ {
		ids = append(ids, fmt.Sprintf("opus-%d", i))
	}
	return ids
}

// typeFilter feeds each rune of s through the /model key handler, the same
// path a typed filter takes.
func typeFilter(a *App, s string) {
	for _, r := range s {
		a.handleModelKey(runeKey(r))
	}
}

func TestWindowStart(t *testing.T) {
	cases := []struct {
		name           string
		off, cursor, n int
		rows           int
		want           int
	}{
		{"rows-cover-all", 2, 1, 3, 5, 0},
		{"rows-equal-n", 4, 3, 10, 10, 0},
		{"already-visible", 3, 5, 10, 4, 3},
		{"cursor-above-window", 5, 2, 10, 4, 2},
		{"cursor-below-window", 0, 7, 10, 4, 4},
		{"wrap-up-to-last", 0, 9, 10, 4, 6},
		{"wrap-down-to-first", 6, 0, 10, 4, 0},
		{"off-past-bottom-clamped", 8, 5, 10, 4, 5},
		{"negative-off-clamped", -2, 1, 10, 4, 0},
		{"cursor-below-with-high-off", 9, 8, 10, 4, 6},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := windowStart(tc.off, tc.cursor, tc.n, tc.rows); got != tc.want {
				t.Fatalf("windowStart(%d, %d, %d, %d) = %d, want %d",
					tc.off, tc.cursor, tc.n, tc.rows, got, tc.want)
			}
		})
	}
}

func TestModelViewKeepsChromeVisible(t *testing.T) {
	a := newModelPickerApp(t, numberedModels(50))
	a.height = 24

	view := a.modelView()
	for _, want := range []string{"project", "effort", "credentials"} {
		if !strings.Contains(view, want) {
			t.Errorf("chrome missing %q:\n%s", want, view)
		}
	}
	// The view fills the terminal exactly: one row short wastes a model row,
	// one row over scrolls the help bar off the bottom. The counter/overflow
	// line under the list is part of that budget and must be counted once.
	if h := lipgloss.Height(view); h != 24 {
		t.Fatalf("modelView height = %d, want exactly 24:\n%s", h, view)
	}
}

// The row budget is measured from the real chrome, so a taller terminal shows
// proportionally more models and still fills the height exactly.
func TestModelViewFillsEveryHeight(t *testing.T) {
	for _, height := range []int{20, 24, 30, 40} {
		a := newModelPickerApp(t, numberedModels(50))
		a.height = height
		if h := lipgloss.Height(a.modelView()); h != height {
			t.Errorf("height %d rendered %d rows", height, h)
		}
	}
}

func TestModelViewFallbackRowsWithoutSize(t *testing.T) {
	a := newModelPickerApp(t, numberedModels(50))
	a.height = 0

	view := a.modelView()
	if !strings.Contains(view, "m00") {
		t.Fatalf("first model missing:\n%s", view)
	}
	if strings.Contains(view, "m10") {
		t.Fatalf("fallback should render at most 10 models, but m10 is present:\n%s", view)
	}
}

func TestModelViewWindowFollowsCursor(t *testing.T) {
	a := newModelPickerApp(t, numberedModels(50))
	a.height = 0 // 10 fallback rows

	// Drive down past the window edge: the cursor moves to index 10 and the
	// window slides so the first id scrolls out.
	for i := 0; i < 10; i++ {
		a.handleModelKey(tea.KeyMsg{Type: tea.KeyDown})
	}
	view := a.modelView()
	if strings.Contains(view, "m00") {
		t.Fatalf("first id should have scrolled out:\n%s", view)
	}
	if !strings.Contains(view, "m10") {
		t.Fatalf("cursor id should be visible:\n%s", view)
	}

	// Walk back up to index 0, then wrap once more to the last id.
	for i := 0; i < 10; i++ {
		a.handleModelKey(tea.KeyMsg{Type: tea.KeyUp})
	}
	a.handleModelKey(tea.KeyMsg{Type: tea.KeyUp}) // wrap 0 -> n-1
	view = a.modelView()
	if !strings.Contains(view, "m49") {
		t.Fatalf("last id should be visible after wrapping:\n%s", view)
	}
	if strings.Contains(view, "m00") {
		t.Fatalf("first id should be gone after wrapping to the end:\n%s", view)
	}
}

func TestModelFilterNarrowsList(t *testing.T) {
	a := newModelPickerApp(t, opusModels())
	a.height = 0

	a.handleModelKey(runeKey('/')) // enter the filter box
	typeFilter(a, "opus")

	view := a.modelView()
	if strings.Contains(view, "m00") {
		t.Fatalf("non-matching id should be filtered out:\n%s", view)
	}
	if !strings.Contains(view, "opus-0") {
		t.Fatalf("matching id should be present:\n%s", view)
	}
	if !strings.Contains(view, "1/5") || !strings.Contains(view, "(of 50)") {
		t.Fatalf("counter should read 1/5 (of 50):\n%s", view)
	}
}

func TestModelFilterEscClears(t *testing.T) {
	a := newModelPickerApp(t, opusModels())
	a.height = 0
	a.view = viewModel

	a.handleModelKey(runeKey('/'))
	typeFilter(a, "opus")

	if view := a.modelView(); strings.Contains(view, "m00") || !strings.Contains(view, "opus-0") {
		t.Fatalf("filter should narrow the list before esc:\n%s", view)
	}

	a.handleModelKey(tea.KeyMsg{Type: tea.KeyEsc})

	if a.view != viewModel {
		t.Fatalf("esc should clear the filter and stay in /model, view = %v", a.view)
	}
	if a.modelState.filtering || a.modelState.filter != "" {
		t.Fatalf("filter state should be cleared, filtering=%v filter=%q", a.modelState.filtering, a.modelState.filter)
	}
	if view := a.modelView(); !strings.Contains(view, "m00") {
		t.Fatalf("full list should be restored after esc:\n%s", view)
	}
}

func TestModelCommitUsesFilteredSelection(t *testing.T) {
	a := newModelPickerApp(t, opusModels())
	a.height = 0

	a.handleModelKey(runeKey('/'))
	typeFilter(a, "opus")
	a.handleModelKey(tea.KeyMsg{Type: tea.KeyDown}) // second filtered row

	// Accept the filter (leave the box, keep cursor), then commit.
	a.handleModelKey(tea.KeyMsg{Type: tea.KeyEnter})
	a.handleModelKey(tea.KeyMsg{Type: tea.KeyEnter})

	if a.cfg.Model != "opus-1" {
		t.Fatalf("cfg.Model = %q, want opus-1 (filtered row 1)", a.cfg.Model)
	}
	if a.cfg.Model == "m01" {
		t.Fatalf("committed the unfiltered index 1 instead of the filtered row")
	}
}

func TestModelViewShowsLoadingState(t *testing.T) {
	a := newModelPickerApp(t, []string{"m1"})
	// Simulate an in-flight fetch by flagging the provider as loading.
	a.catalogLoading = map[string]bool{"my-llm": true}
	a.height = 0

	view := a.modelView()
	if !strings.Contains(view, "Fetching models") {
		t.Fatalf("expected loading indicator in view, got:\n%s", view)
	}
	// When loading the empty-catalog hint must not appear.
	if strings.Contains(view, "no models in this profile") {
		t.Fatalf("loading state must suppress empty hint, got:\n%s", view)
	}
}
