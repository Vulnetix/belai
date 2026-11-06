package tui

import (
	"strconv"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/config"
)

func knowledgeApp(t *testing.T, scope config.Scope) *App {
	t.Helper()
	t.Setenv("BELAI_HOME", t.TempDir())
	t.Setenv("VULNETIX_WEB_URL", "http://127.0.0.1:1")
	a := New(Options{Workdir: t.TempDir()})
	a.push(viewSettings)
	a.settingsState.scope = scope
	if err := a.reloadSettings(); err != nil {
		t.Fatal(err)
	}
	return a
}

func TestSettingsKnowledgeRowsDefaultAndGrouped(t *testing.T) {
	a := knowledgeApp(t, config.ScopeGlobal)
	for key, want := range map[string]int{
		"knowledge.max_index_tokens":   config.DefaultKnowledgeIndexTokens,
		"knowledge.max_project_tokens": config.DefaultKnowledgeProjectTokens,
		"knowledge.max_result_tokens":  config.DefaultKnowledgeResultTokens,
		"knowledge.topic_chunks":       config.DefaultKnowledgeTopicChunks,
		"knowledge.topic_budget_docs":  config.DefaultKnowledgeTopicDocs,
	} {
		row, idx := settingsRowByKey(a, key)
		if idx < 0 || row.value != strconv.Itoa(want) || row.kind != "text" {
			t.Fatalf("%s row = %+v", key, row)
		}
		if row.group != "knowledge" || !settingsWritesGlobalOnly(key) {
			t.Fatalf("%s must be in the knowledge group and global only", key)
		}
	}
}

// TestSettingsKnowledgeIsSavedGlobalWhateverTheScope pins that a project scope
// on the screen still writes the user's own file.
func TestSettingsKnowledgeIsSavedGlobalWhateverTheScope(t *testing.T) {
	a := knowledgeApp(t, config.ScopeProject)
	row, _ := settingsRowByKey(a, "knowledge.max_result_tokens")
	if err := a.commitTextRow(row, "5000"); err != nil {
		t.Fatal(err)
	}
	g, err := config.LoadGlobal()
	if err != nil || g.Knowledge == nil || g.Knowledge.MaxResultTokens == nil || *g.Knowledge.MaxResultTokens != 5000 {
		t.Fatalf("global = %+v err=%v", g.Knowledge, err)
	}
	if row, _ := settingsRowByKey(a, "knowledge.max_result_tokens"); row.value != "5000" {
		t.Fatalf("row after edit = %q", row.value)
	}
	if got := a.rawValue("knowledge.max_result_tokens"); got != "5000" {
		t.Fatalf("editor starts at %q", got)
	}
}

func TestSettingsKnowledgeRejectsOutOfRangeAndClears(t *testing.T) {
	a := knowledgeApp(t, config.ScopeGlobal)
	row, _ := settingsRowByKey(a, "knowledge.max_index_tokens")
	for _, bad := range []string{"12", "abc", "99999999", "-4"} {
		if err := a.commitTextRow(row, bad); err == nil {
			t.Fatalf("%q must be refused", bad)
		}
	}
	if err := a.commitTextRow(row, "300000"); err != nil {
		t.Fatal(err)
	}
	if err := a.commitTextRow(row, ""); err != nil {
		t.Fatal(err)
	}
	g, _ := config.LoadGlobal()
	if g.Knowledge != nil {
		t.Fatalf("clearing the only key must drop the block: %+v", g.Knowledge)
	}
	if row, _ := settingsRowByKey(a, "knowledge.max_index_tokens"); !strings.Contains(row.value, strconv.Itoa(config.DefaultKnowledgeIndexTokens)) {
		t.Fatalf("row after clear = %q", row.value)
	}
}

func TestSettingsKnowledgeSourceRowsAndToggle(t *testing.T) {
	a := knowledgeApp(t, config.ScopeProject)
	for key, want := range map[string]string{
		"knowledge.source_index":          "off",
		"knowledge.source_max_tokens":     strconv.Itoa(config.DefaultKnowledgeSourceTokens),
		"knowledge.source_max_file_bytes": strconv.Itoa(config.DefaultKnowledgeSourceFileBytes),
	} {
		row, idx := settingsRowByKey(a, key)
		if idx < 0 || row.value != want {
			t.Fatalf("%s row = %+v", key, row)
		}
		if row.group != "knowledge" || !settingsWritesGlobalOnly(key) {
			t.Fatalf("%s must be in the knowledge group and global only", key)
		}
	}
	// A project scope on the screen still writes the user's own file.
	if err := a.cycleToggle("knowledge.source_index"); err != nil {
		t.Fatal(err)
	}
	g, err := config.LoadGlobal()
	if err != nil || g.Knowledge == nil || g.Knowledge.SourceIndex == nil || !*g.Knowledge.SourceIndex {
		t.Fatalf("global = %+v err=%v", g.Knowledge, err)
	}
	if on, _, _ := a.settings.KnowledgeSource(); !on {
		t.Fatal("the live settings must follow the toggle")
	}
	row, _ := settingsRowByKey(a, "knowledge.max_index_tokens")
	_ = row
	if err := a.cycleToggle("knowledge.source_index"); err != nil {
		t.Fatal(err)
	}
	if on, _, _ := a.settings.KnowledgeSource(); on {
		t.Fatal("a second toggle turns it off")
	}
	size, _ := settingsRowByKey(a, "knowledge.source_max_file_bytes")
	if err := a.commitTextRow(size, "100"); err == nil {
		t.Fatal("a file size under the floor must be refused")
	}
	if err := a.commitTextRow(size, "65536"); err != nil {
		t.Fatal(err)
	}
	if err := a.knowledgeUnset("knowledge.source_index"); err != nil {
		t.Fatal(err)
	}
}
